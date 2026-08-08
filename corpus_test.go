package libewf_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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

	checkAcquiryDate(t, got, want)
}

// checkAcquiryDate compares the acquisition date the way the stored encoding
// allows.
//
// EWF stores this three ways and only one of them, the POSIX timestamp, denotes
// an instant. Six space-separated numbers and a ctime-like string are wall
// clocks with no zone, which the reader therefore interprets in the local zone.
// Asserting an absolute instant for those would pin the timezone the corpus
// happened to be generated in, and fail everywhere else — so they are compared
// as the wall clock the writer stored, which holds on any machine and still
// catches a date read from the wrong section or with its fields transposed.
func checkAcquiryDate(t *testing.T, got *metadata.Acquisition, want *corpus.ExpectedAcquisition) {
	t.Helper()

	if mismatch := acquiryDateMismatch(got, want); mismatch != "" {
		t.Error(mismatch)
	}
}

// acquiryDateMismatch returns why the decoded acquisition date is not the
// expected one, or "" when it is. It is separate from the assertion so that the
// rule it encodes can be tested against dates from several timezones, which is
// the one thing a test running in a single zone cannot otherwise check.
func acquiryDateMismatch(got *metadata.Acquisition, want *corpus.ExpectedAcquisition) string {
	if want.AcquiryDateUnix == 0 && want.AcquiryDateWall == "" {
		return ""
	}
	if got.AcquiryDate.IsZero() {
		return fmt.Sprintf("Acquisition.AcquiryDate is zero, want %s (raw %q)",
			expectedDateText(want), got.AcquiryDateRaw)
	}

	// The stored text decides which comparison is meaningful, and it is the
	// text as stored rather than anything derived from it: an all-digit date
	// is the POSIX form, everything else is zone-less.
	if _, err := strconv.ParseInt(strings.TrimSpace(got.AcquiryDateRaw), 10, 64); err == nil {
		if want.AcquiryDateUnix == 0 {
			return fmt.Sprintf("image stores the acquisition date as a POSIX timestamp (%q) but the "+
				"manifest records only a wall clock; regenerate the corpus with: "+
				"go run ./tools/mkcorpus -out %s", got.AcquiryDateRaw, corpusDir)
		}
		if gotUnix := got.AcquiryDate.Unix(); gotUnix != want.AcquiryDateUnix {
			return fmt.Sprintf("Acquisition.AcquiryDate = %s (%d), want %s (%d); raw value was %q",
				got.AcquiryDate, gotUnix,
				time.Unix(want.AcquiryDateUnix, 0), want.AcquiryDateUnix,
				got.AcquiryDateRaw)
		}
		return ""
	}

	if want.AcquiryDateWall == "" {
		return fmt.Sprintf("image stores the acquisition date without a timezone (%q) but the manifest "+
			"records only an instant, which holds solely in the timezone the corpus was generated in; "+
			"regenerate the corpus with: go run ./tools/mkcorpus -out %s",
			got.AcquiryDateRaw, corpusDir)
	}
	if gotWall := got.AcquiryDate.Format(corpus.AcquiryDateWallLayout); gotWall != want.AcquiryDateWall {
		return fmt.Sprintf("Acquisition.AcquiryDate = %s, want the wall clock %s; raw value was %q",
			gotWall, want.AcquiryDateWall, got.AcquiryDateRaw)
	}
	return ""
}

// TestAcquiryDateExpectationSurvivesTimezone is the check the corpus test
// cannot make of itself: it runs in one timezone, and the expectation it
// asserts has to hold in every one.
//
// The same image is presented as it would decode on machines in four zones. A
// zone-less date yields a different instant on each, which is exactly why
// pinning one failed everywhere outside the zone the corpus was generated in.
func TestAcquiryDateExpectationSurvivesTimezone(t *testing.T) {
	zones := []*time.Location{
		time.UTC,
		time.FixedZone("EDT", -4*3600),
		time.FixedZone("IST", 5*3600+1800),
		time.FixedZone("LINT", 14*3600),
	}
	want := &corpus.ExpectedAcquisition{AcquiryDateWall: "2026-07-26 12:32:18"}

	instants := make(map[int64]struct{}, len(zones))
	for _, zone := range zones {
		got := &metadata.Acquisition{
			AcquiryDateRaw: "2026 7 26 12 32 18",
			AcquiryDate:    time.Date(2026, 7, 26, 12, 32, 18, 0, zone),
		}
		if mismatch := acquiryDateMismatch(got, want); mismatch != "" {
			t.Errorf("in %s: %s", zone, mismatch)
		}
		instants[got.AcquiryDate.Unix()] = struct{}{}
	}

	// Without this the test could pass while proving nothing: the zones have to
	// actually disagree about the instant for the wall clock to be the thing
	// under test.
	if len(instants) != len(zones) {
		t.Fatalf("the %d zones produced %d distinct instants; they must all differ", len(zones), len(instants))
	}
}

func TestAcquiryDateMismatch(t *testing.T) {
	const (
		posixRaw = "1785083538"
		wallRaw  = "2026 7 26 12 32 18"
	)
	posixDate := time.Unix(1785083538, 0)
	wallDate := time.Date(2026, 7, 26, 12, 32, 18, 0, time.Local)

	tests := []struct {
		name       string
		got        metadata.Acquisition
		want       corpus.ExpectedAcquisition
		wantReport bool
	}{
		{
			name: "no expectation",
			got:  metadata.Acquisition{AcquiryDateRaw: wallRaw, AcquiryDate: wallDate},
		},
		{
			name: "posix instant matches",
			got:  metadata.Acquisition{AcquiryDateRaw: posixRaw, AcquiryDate: posixDate},
			want: corpus.ExpectedAcquisition{AcquiryDateUnix: 1785083538},
		},
		{
			name:       "posix instant differs",
			got:        metadata.Acquisition{AcquiryDateRaw: posixRaw, AcquiryDate: posixDate},
			want:       corpus.ExpectedAcquisition{AcquiryDateUnix: 1785083539},
			wantReport: true,
		},
		{
			name:       "posix date with only a wall expectation is a stale manifest",
			got:        metadata.Acquisition{AcquiryDateRaw: posixRaw, AcquiryDate: posixDate},
			want:       corpus.ExpectedAcquisition{AcquiryDateWall: "2026-07-26 12:32:18"},
			wantReport: true,
		},
		{
			name: "zone-less wall clock matches",
			got:  metadata.Acquisition{AcquiryDateRaw: wallRaw, AcquiryDate: wallDate},
			want: corpus.ExpectedAcquisition{AcquiryDateWall: "2026-07-26 12:32:18"},
		},
		{
			// The check exists to catch a date taken from the wrong section or
			// with its fields transposed; a wall clock still catches both.
			name:       "zone-less wall clock transposed",
			got:        metadata.Acquisition{AcquiryDateRaw: wallRaw, AcquiryDate: time.Date(2026, 7, 26, 12, 18, 32, 0, time.Local)},
			want:       corpus.ExpectedAcquisition{AcquiryDateWall: "2026-07-26 12:32:18"},
			wantReport: true,
		},
		{
			name:       "zone-less date with only an instant expectation is a stale manifest",
			got:        metadata.Acquisition{AcquiryDateRaw: wallRaw, AcquiryDate: wallDate},
			want:       corpus.ExpectedAcquisition{AcquiryDateUnix: 1785083538},
			wantReport: true,
		},
		{
			name:       "date not decoded at all",
			got:        metadata.Acquisition{AcquiryDateRaw: wallRaw},
			want:       corpus.ExpectedAcquisition{AcquiryDateWall: "2026-07-26 12:32:18"},
			wantReport: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mismatch := acquiryDateMismatch(&tt.got, &tt.want)
			if got := mismatch != ""; got != tt.wantReport {
				t.Errorf("acquiryDateMismatch() reported %v (%q), want reported = %v",
					got, mismatch, tt.wantReport)
			}
		})
	}
}

func expectedDateText(want *corpus.ExpectedAcquisition) string {
	if want.AcquiryDateWall != "" {
		return want.AcquiryDateWall
	}
	return time.Unix(want.AcquiryDateUnix, 0).String()
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
