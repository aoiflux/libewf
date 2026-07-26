package libewf_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"time"

	libewf "github.com/aoiflux/libewf"
	"github.com/aoiflux/libewf/internal/corpus"
	"github.com/aoiflux/libewf/metadata"
)

// corpusDir holds images produced by real acquisition tools. It is populated
// by tools/mkcorpus and is not committed by default; see
// testdata/corpus/README.md.
const corpusDir = "testdata/corpus"

// TestCorpus validates the reader against images written by real acquisition
// tools. Synthetic tests prove the reader matches the test author's model of
// the format; only this test proves it matches the format as shipped.
//
// It skips when no corpus is present so the suite stays runnable everywhere,
// but a green suite without a corpus must not be read as format validation.
func TestCorpus(t *testing.T) {
	manifest, present, err := corpus.Load(corpusDir)
	if err != nil {
		t.Fatalf("loading corpus manifest: %v", err)
	}
	if !present {
		t.Skipf("no corpus present in %s; run: go run ./tools/mkcorpus -out %s", corpusDir, corpusDir)
	}
	if len(manifest.Entries) == 0 {
		t.Fatalf("corpus manifest in %s lists no entries", corpusDir)
	}

	for _, entry := range manifest.Entries {
		t.Run(entry.Name, func(t *testing.T) {
			if err := entry.Validate(); err != nil {
				t.Fatalf("manifest entry is not usable: %v", err)
			}
			runCorpusEntry(t, entry)
		})
	}
}

func runCorpusEntry(t *testing.T, entry corpus.Entry) {
	t.Helper()

	sources := make([]io.ReaderAt, 0, len(entry.Segments))
	for _, name := range entry.Segments {
		path := filepath.Join(corpusDir, name)
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("opening segment %s: %v", path, err)
		}
		t.Cleanup(func() { _ = f.Close() })
		sources = append(sources, f)
	}

	r, err := libewf.OpenSegments(sources)

	if entry.ExpectIncomplete {
		if err == nil {
			t.Fatal("OpenSegments() succeeded on a deliberately incomplete set, want an error")
		}
		if !errors.Is(err, libewf.ErrMissingSegment) && !errors.Is(err, libewf.ErrIncompleteSegmentSet) {
			t.Fatalf("OpenSegments() error = %v, want ErrMissingSegment or ErrIncompleteSegmentSet", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("OpenSegments() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// --- geometry ---------------------------------------------------------
	if entry.Size != 0 && r.Size() != entry.Size {
		t.Errorf("Size() = %d, want %d", r.Size(), entry.Size)
	}
	if entry.SectorSize != 0 && r.SectorSize() != entry.SectorSize {
		t.Errorf("SectorSize() = %d, want %d", r.SectorSize(), entry.SectorSize)
	}

	meta := r.Metadata()
	if meta.ChunkTablesInvalid != 0 {
		t.Errorf("ChunkTablesInvalid = %d, want 0: no chunk table in a healthy image should fail its checksum",
			meta.ChunkTablesInvalid)
	}
	if meta.Media != nil && meta.Media.NumberOfChunks != 0 && meta.ObservedChunkCount != meta.Media.NumberOfChunks {
		t.Errorf("ObservedChunkCount = %d, want %d (declared)", meta.ObservedChunkCount, meta.Media.NumberOfChunks)
	}

	// --- stored digests ---------------------------------------------------
	if want := entry.ExpectStoredMD5; want != "" {
		if !meta.HasMD5Digest {
			t.Errorf("image reports no stored MD5, want %s", want)
		} else if got := hex.EncodeToString(meta.MD5Digest[:]); got != strings.ToLower(want) {
			t.Errorf("stored MD5 = %s, want %s", got, want)
		}
	}
	if want := entry.ExpectStoredSHA1; want != "" {
		if !meta.HasSHA1Digest {
			t.Errorf("image reports no stored SHA1, want %s", want)
		} else if got := hex.EncodeToString(meta.SHA1Digest[:]); got != strings.ToLower(want) {
			t.Errorf("stored SHA1 = %s, want %s", got, want)
		}
	}

	// --- acquisition provenance -------------------------------------------
	if want := entry.ExpectAcquisition; want != nil {
		checkAcquisition(t, meta.Acquisition, want)
	}

	// --- decoded stream against the raw source ----------------------------
	if entry.DecodedSHA256 != "" {
		sum := sha256.New()
		n, err := io.Copy(sum, io.NewSectionReader(r, 0, r.Size()))
		if err != nil {
			t.Fatalf("streaming decoded device: %v", err)
		}
		if n != r.Size() {
			t.Errorf("streamed %d bytes, want %d", n, r.Size())
		}
		if got := hex.EncodeToString(sum.Sum(nil)); got != strings.ToLower(entry.DecodedSHA256) {
			t.Errorf("decoded device SHA-256 = %s, want %s\n"+
				"the decoded stream does not match the raw image that was acquired", got, entry.DecodedSHA256)
		}
	}

	// --- acquisition digests recomputed from the decode --------------------
	if entry.RequireVerifyOK {
		result, err := libewf.Verify(context.Background(), r)
		if err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		for _, bad := range result.BadRanges {
			t.Errorf("undecodable span at offset %d length %d: %s", bad.Offset, bad.Length, bad.Err)
		}
		if result.HasStoredMD5 && !result.MD5Match {
			t.Errorf("recomputed MD5 %x does not match the acquisition MD5 %x",
				result.ComputedMD5, result.StoredMD5)
		}
		if result.HasStoredSHA1 && !result.SHA1Match {
			t.Errorf("recomputed SHA-1 %x does not match the acquisition SHA-1 %x",
				result.ComputedSHA1, result.StoredSHA1)
		}
		if !result.OK() {
			t.Error("Verify() reported the image did not verify")
		}
	}

	// --- read-granularity independence ------------------------------------
	// A filesystem parser issues small unaligned reads; results must not
	// depend on how the caller slices them up.
	checkGranularity(t, r)
}

// checkAcquisition compares the decoded provenance against values that came
// from outside this library: the strings handed to the acquisition tool, and
// ewfinfo's reading of the date it wrote.
func checkAcquisition(t *testing.T, got *metadata.Acquisition, want *corpus.ExpectedAcquisition) {
	t.Helper()

	if got == nil {
		t.Error("Metadata().Acquisition is nil, want provenance decoded from the header sections")
		return
	}

	for _, field := range []struct {
		name      string
		got, want string
	}{
		{"CaseNumber", got.CaseNumber, want.CaseNumber},
		{"EvidenceNumber", got.EvidenceNumber, want.EvidenceNumber},
		{"Description", got.Description, want.Description},
		{"ExaminerName", got.ExaminerName, want.ExaminerName},
		{"Notes", got.Notes, want.Notes},
	} {
		if field.want != "" && field.got != field.want {
			t.Errorf("Acquisition.%s = %q, want %q (from the acquisition command line)",
				field.name, field.got, field.want)
		}
	}

	if want.AcquiryDateUnix != 0 {
		if got.AcquiryDate.IsZero() {
			t.Errorf("Acquisition.AcquiryDate is zero, want %s (raw %q)",
				time.Unix(want.AcquiryDateUnix, 0), got.AcquiryDateRaw)
		} else if gotUnix := got.AcquiryDate.Unix(); gotUnix != want.AcquiryDateUnix {
			t.Errorf("Acquisition.AcquiryDate = %s (%d), want %s (%d); raw value was %q",
				got.AcquiryDate, gotUnix,
				time.Unix(want.AcquiryDateUnix, 0), want.AcquiryDateUnix,
				got.AcquiryDateRaw)
		}
	}
}

func checkGranularity(t *testing.T, r libewf.Reader) {
	t.Helper()

	// Sample a window that straddles a chunk boundary rather than the whole
	// device, so the check stays fast on multi-gigabyte corpus entries.
	window := int64(256 << 10)
	if r.Size() < window {
		window = r.Size()
	}
	start := int64(0)
	if meta := r.Metadata(); meta.Media != nil {
		chunk := int64(meta.Media.SectorsPerChunk) * int64(meta.Media.BytesPerSector)
		if chunk > 0 && r.Size() > chunk+window {
			start = chunk - 1 // straddle the first chunk boundary
		}
	}

	whole := make([]byte, window)
	if _, err := r.ReadAt(whole, start); err != nil && err != io.EOF {
		t.Fatalf("ReadAt(%d, %d) error = %v", window, start, err)
	}

	// Bound the read count per step. Without a chunk cache every ReadAt
	// re-decompresses its chunk, so a 1-byte walk over a large window costs
	// window/step decompressions; cap the span instead of the granularity so
	// small steps stay cheap while still crossing a chunk boundary.
	const maxReadsPerStep = 4096

	for _, step := range []int{1, 3, 511, 512, 4096} {
		if int64(step) > window {
			continue
		}
		span := window
		if limit := int64(step) * maxReadsPerStep; span > limit {
			span = limit
		}

		got := make([]byte, 0, span)
		for off := int64(0); off < span; off += int64(step) {
			size := int64(step)
			if remaining := span - off; size > remaining {
				size = remaining
			}
			buf := make([]byte, size)
			n, err := r.ReadAt(buf, start+off)
			if err != nil && err != io.EOF {
				t.Fatalf("step %d: ReadAt(%d) error = %v", step, start+off, err)
			}
			got = append(got, buf[:n]...)
		}
		if !bytes.Equal(got, whole[:span]) {
			t.Fatalf("step %d: split reads differ from a single read over the same %d-byte window", step, span)
		}
	}
}
