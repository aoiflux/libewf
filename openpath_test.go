package libewf_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	libewf "github.com/aoiflux/libewf"
)

// These tests run against the golden corpus rather than synthetic images,
// because OpenPath's whole job is to find files on a real filesystem and hand
// them to the reader in the right order. The corpus already holds the shapes
// that matter: single-segment sets, a two-segment set, a five-segment set, and
// the lowercase, logical and EWF2 spellings.

// corpusSet returns the path to the first segment of a corpus set, skipping
// when no corpus is present.
func corpusSet(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(corpusDir, name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no corpus image %s; run: go run ./tools/mkcorpus -out %s", name, corpusDir)
	}
	return path
}

// deviceDigest hashes the whole decoded device, which is the only comparison
// that proves two readers decoded the same evidence.
func deviceDigest(t *testing.T, r libewf.Reader) string {
	t.Helper()
	sum := sha256.New()
	if _, err := io.Copy(sum, io.NewSectionReader(r, 0, r.Size())); err != nil {
		t.Fatalf("reading device: %v", err)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// openSegmentsDigest decodes the same files through the existing API, so each
// OpenPath result is checked against the constructor it delegates to rather
// than against a value recorded from itself.
func openSegmentsDigest(t *testing.T, paths ...string) string {
	t.Helper()
	sources := make([]io.ReaderAt, 0, len(paths))
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("opening %s: %v", path, err)
		}
		t.Cleanup(func() { _ = file.Close() })
		sources = append(sources, file)
	}

	r, err := libewf.OpenSegments(sources)
	if err != nil {
		t.Fatalf("OpenSegments() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return deviceDigest(t, r)
}

// copySet copies the named corpus files into a temporary directory, renaming
// them as given, so a set can be reshaped — a hole punched in it, a tail
// removed — without touching the corpus.
func copySet(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for source, target := range files {
		data, err := os.ReadFile(filepath.Join(corpusDir, source))
		if err != nil {
			t.Skipf("no corpus image %s; run: go run ./tools/mkcorpus -out %s", source, corpusDir)
		}
		if err := os.WriteFile(filepath.Join(dir, target), data, 0o600); err != nil {
			t.Fatalf("writing %s: %v", target, err)
		}
	}
	return dir
}

func TestOpenPathSingleSegment(t *testing.T) {
	for _, name := range []string{
		"encase2-deflate.E01",
		"encase6-deflate.E01",
		"ewf-deflate.e01",
		"ex01-deflate.Ex01",
		"smart-deflate.s01",
	} {
		t.Run(name, func(t *testing.T) {
			path := corpusSet(t, name)

			r, err := libewf.OpenPath(path)
			if err != nil {
				t.Fatalf("OpenPath() error = %v", err)
			}
			t.Cleanup(func() { _ = r.Close() })

			if want := openSegmentsDigest(t, path); deviceDigest(t, r) != want {
				t.Error("OpenPath decoded a different device than OpenSegments on the same file")
			}
		})
	}
}

func TestOpenPathMultiSegment(t *testing.T) {
	tests := []struct {
		name     string
		segments []string
	}{
		{
			name:     "two segments",
			segments: []string{"encase5-multiseg.E01", "encase5-multiseg.E02"},
		},
		{
			name: "five segments",
			segments: []string{
				"encase6-multiseg-none.E01", "encase6-multiseg-none.E02",
				"encase6-multiseg-none.E03", "encase6-multiseg-none.E04",
				"encase6-multiseg-none.E05",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := make([]string, len(tt.segments))
			for i, name := range tt.segments {
				paths[i] = corpusSet(t, name)
			}

			found, err := libewf.SegmentPaths(paths[0])
			if err != nil {
				t.Fatalf("SegmentPaths() error = %v", err)
			}
			if len(found) != len(paths) {
				t.Fatalf("SegmentPaths() found %d segments (%v), want %d", len(found), found, len(paths))
			}
			for i := range found {
				if filepath.Base(found[i]) != filepath.Base(paths[i]) {
					t.Errorf("SegmentPaths()[%d] = %s, want %s", i, found[i], paths[i])
				}
			}

			r, err := libewf.OpenPath(paths[0])
			if err != nil {
				t.Fatalf("OpenPath() error = %v", err)
			}
			t.Cleanup(func() { _ = r.Close() })

			if want := openSegmentsDigest(t, paths...); deviceDigest(t, r) != want {
				t.Error("OpenPath decoded a different device than OpenSegments over the same set")
			}
		})
	}
}

// TestOpenPathFromLaterSegment checks that naming a middle segment still
// decodes the whole device: the set is enumerated from segment 1, not from the
// file that was named.
func TestOpenPathFromLaterSegment(t *testing.T) {
	first := corpusSet(t, "encase6-multiseg-none.E01")
	third := corpusSet(t, "encase6-multiseg-none.E03")

	fromFirst, err := libewf.OpenPath(first)
	if err != nil {
		t.Fatalf("OpenPath(first) error = %v", err)
	}
	t.Cleanup(func() { _ = fromFirst.Close() })

	fromThird, err := libewf.OpenPath(third)
	if err != nil {
		t.Fatalf("OpenPath(third) error = %v", err)
	}
	t.Cleanup(func() { _ = fromThird.Close() })

	if deviceDigest(t, fromThird) != deviceDigest(t, fromFirst) {
		t.Error("opening by .E03 decoded a different device than opening by .E01")
	}
}

func TestOpenPathMissingSegment(t *testing.T) {
	// Segment 2 renamed to 3, so the set runs 1, 3, 4, 5 with a hole at 2.
	dir := copySet(t, map[string]string{
		"encase6-multiseg-none.E01": "gapped.E01",
		"encase6-multiseg-none.E02": "gapped.E03",
		"encase6-multiseg-none.E04": "gapped.E04",
		"encase6-multiseg-none.E05": "gapped.E05",
	})
	first := filepath.Join(dir, "gapped.E01")

	_, err := libewf.OpenPath(first)
	if !errors.Is(err, libewf.ErrMissingSegment) {
		t.Fatalf("OpenPath() error = %v, want ErrMissingSegment", err)
	}

	var missing *libewf.MissingSegmentsError
	if !errors.As(err, &missing) {
		t.Fatalf("OpenPath() error = %v, want a *MissingSegmentsError", err)
	}
	if len(missing.Missing) != 1 || missing.Missing[0] != 2 {
		t.Errorf("Missing = %v, want [2]", missing.Missing)
	}
	if len(missing.Expected) != 1 || missing.Expected[0] != "gapped.E02" {
		t.Errorf("Expected = %v, want [gapped.E02]", missing.Expected)
	}
	if !strings.Contains(err.Error(), "gapped.E02") {
		t.Errorf("error message %q does not name the missing file", err)
	}
}

// TestOpenPathIncompleteTail pins the division of labour: a set that simply
// stops looks complete on disk, and is caught by the reader instead.
func TestOpenPathIncompleteTail(t *testing.T) {
	dir := copySet(t, map[string]string{
		"encase6-multiseg-none.E01": "truncated.E01",
	})
	first := filepath.Join(dir, "truncated.E01")

	if _, err := libewf.OpenPath(first); !errors.Is(err, libewf.ErrIncompleteSegmentSet) {
		t.Fatalf("OpenPath() error = %v, want ErrIncompleteSegmentSet", err)
	}
}

func TestOpenPathAllowIncompleteSegmentSet(t *testing.T) {
	dir := copySet(t, map[string]string{
		"encase6-multiseg-none.E01": "partial.E01",
		"encase6-multiseg-none.E03": "partial.E03",
	})
	first := filepath.Join(dir, "partial.E01")

	if _, err := libewf.OpenPath(first); !errors.Is(err, libewf.ErrMissingSegment) {
		t.Fatalf("OpenPath() error = %v, want ErrMissingSegment", err)
	}

	r, err := libewf.OpenPathWithOptions(first, libewf.AllowIncompleteSegmentSet())
	if err != nil {
		t.Fatalf("OpenPathWithOptions(AllowIncompleteSegmentSet) error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if r.Size() <= 0 {
		t.Errorf("Size() = %d, want the full device size the volume declares", r.Size())
	}
}

// TestOpenPathIgnoresUnrelatedFiles puts the files that really do sit beside
// evidence into the directory: sidecar digests, logs, and the same acquisition
// in another format.
func TestOpenPathIgnoresUnrelatedFiles(t *testing.T) {
	dir := copySet(t, map[string]string{
		"encase5-multiseg.E01": "case1.E01",
		"encase5-multiseg.E02": "case1.E02",
	})
	for _, sidecar := range []string{"case1.E01.md5", "case1.txt", "case1.log", "case1.json", "case1"} {
		if err := os.WriteFile(filepath.Join(dir, sidecar), []byte("not evidence"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", sidecar, err)
		}
	}

	paths, err := libewf.SegmentPaths(filepath.Join(dir, "case1.E01"))
	if err != nil {
		t.Fatalf("SegmentPaths() error = %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("SegmentPaths() = %v, want only the two segments", paths)
	}

	r, err := libewf.OpenPath(filepath.Join(dir, "case1.E01"))
	if err != nil {
		t.Fatalf("OpenPath() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
}

func TestOpenPathMissingFile(t *testing.T) {
	_, err := libewf.OpenPath(filepath.Join(t.TempDir(), "absent.E01"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("OpenPath() error = %v, want fs.ErrNotExist", err)
	}
}

func TestOpenPathNonEWFFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("this is not an EWF image"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	if _, err := libewf.OpenPath(path); !errors.Is(err, libewf.ErrUnsupportedFormat) {
		t.Fatalf("OpenPath() error = %v, want ErrUnsupportedFormat", err)
	}
}

// TestOpenPathCloseReleasesFiles is the ownership test. On Windows an open
// handle blocks removal outright; everywhere else t.TempDir's own cleanup
// would fail. Either way a leaked segment handle fails the test rather than
// passing silently.
func TestOpenPathCloseReleasesFiles(t *testing.T) {
	dir := copySet(t, map[string]string{
		"encase5-multiseg.E01": "closing.E01",
		"encase5-multiseg.E02": "closing.E02",
	})
	first := filepath.Join(dir, "closing.E01")

	r, err := libewf.OpenPath(first)
	if err != nil {
		t.Fatalf("OpenPath() error = %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	// Closing a reader twice happens whenever ownership is shared; it must not
	// close a descriptor the process has since reused.
	if err := r.Close(); err != nil {
		t.Fatalf("second Close() error = %v, want nil", err)
	}

	for _, name := range []string{"closing.E01", "closing.E02"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Errorf("removing %s after Close: %v (a segment handle was leaked)", name, err)
		}
	}
}

// TestOpenPathUnwrap checks that the concrete reader stays reachable, since
// the wrapper OpenPath returns would otherwise hide Header, Sections and the
// stored digests behind the Reader interface.
func TestOpenPathUnwrap(t *testing.T) {
	path := corpusSet(t, "encase6-deflate.E01")

	r, err := libewf.OpenPath(path)
	if err != nil {
		t.Fatalf("OpenPath() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	unwrapper, ok := r.(interface{ Unwrap() libewf.Reader })
	if !ok {
		t.Fatal("OpenPath's Reader does not expose Unwrap")
	}
	if unwrapper.Unwrap() == nil {
		t.Fatal("Unwrap() returned nil")
	}
	if unwrapper.Unwrap().Size() != r.Size() {
		t.Error("Unwrap() returned a reader over different data")
	}
}
