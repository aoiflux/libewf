package libewf_test

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"errors"
	"io"
	"testing"

	libewf "github.com/aoiflux/libewf"
	"github.com/aoiflux/libewf/metadata"
)

// fakeReader is a libewf.Reader over an in-memory device. Verify depends only
// on the Reader interface, so it can be exercised exactly — including partial
// reads and absent digests — without constructing an EWF image.
type fakeReader struct {
	data       []byte
	sectorSize int
	chunkSize  int

	storedMD5  []byte // nil means the image records no MD5
	storedSHA1 []byte

	// failFrom/failLen mark a span that cannot be decoded, modelling a
	// damaged chunk.
	failFrom int64
	failLen  int64
}

func (f *fakeReader) Size() int64     { return int64(len(f.data)) }
func (f *fakeReader) SectorSize() int { return f.sectorSize }
func (f *fakeReader) Close() error    { return nil }

func (f *fakeReader) Metadata() metadata.Info {
	info := metadata.Info{
		Media: &metadata.MediaInfo{
			BytesPerSector:  uint32(f.sectorSize),
			SectorsPerChunk: uint32(f.chunkSize / f.sectorSize),
			NumberOfSectors: uint64(len(f.data) / f.sectorSize),
		},
	}
	if f.storedMD5 != nil {
		info.HasMD5Digest = true
		copy(info.MD5Digest[:], f.storedMD5)
	}
	if f.storedSHA1 != nil {
		info.HasSHA1Digest = true
		copy(info.SHA1Digest[:], f.storedSHA1)
	}
	return info
}

// ReadAt mirrors the real reader's contract: clamped to the device, io.EOF at
// the end, and a non-nil error whenever n < len(p).
func (f *fakeReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, libewf.ErrInvalidOffset
	}
	if off >= f.Size() {
		return 0, io.EOF
	}
	if f.failLen > 0 && off < f.failFrom+f.failLen && off+int64(len(p)) > f.failFrom {
		// Serve whatever precedes the damaged span, then fail.
		if good := f.failFrom - off; good > 0 {
			n := copy(p, f.data[off:off+good])
			return n, errors.New("simulated unreadable chunk")
		}
		return 0, errors.New("simulated unreadable chunk")
	}

	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func deviceBytes(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i*31 + 7)
	}
	return data
}

func newFake(data []byte) *fakeReader {
	sum5 := md5.Sum(data)
	sum1 := sha1.Sum(data)
	return &fakeReader{
		data:       data,
		sectorSize: 512,
		chunkSize:  32 << 10,
		storedMD5:  sum5[:],
		storedSHA1: sum1[:],
	}
}

func TestVerifyMatchesStoredDigests(t *testing.T) {
	r := newFake(deviceBytes(150 << 10)) // not a whole number of chunks

	result, err := libewf.Verify(context.Background(), r)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	if !result.MD5Match {
		t.Errorf("MD5Match = false: computed %x, stored %x", result.ComputedMD5, result.StoredMD5)
	}
	if !result.SHA1Match {
		t.Errorf("SHA1Match = false: computed %x, stored %x", result.ComputedSHA1, result.StoredSHA1)
	}
	if len(result.BadRanges) != 0 {
		t.Errorf("BadRanges = %v, want none", result.BadRanges)
	}
	if result.BytesHashed != r.Size() {
		t.Errorf("BytesHashed = %d, want %d", result.BytesHashed, r.Size())
	}
	if !result.OK() {
		t.Error("OK() = false, want true")
	}
}

func TestVerifyDetectsAlteredDevice(t *testing.T) {
	r := newFake(deviceBytes(64 << 10))
	r.data[1234] ^= 0xFF // the device no longer matches its acquisition digests

	result, err := libewf.Verify(context.Background(), r)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	if result.MD5Match || result.SHA1Match {
		t.Error("digests matched an altered device, want mismatch")
	}
	if result.OK() {
		t.Error("OK() = true for an altered device, want false")
	}
}

// TestVerifyRecordsUnreadableSpans checks that a damaged image still yields a
// complete report: the bad span is located, the rest is hashed, and the result
// is not silently presented as a clean mismatch.
func TestVerifyRecordsUnreadableSpans(t *testing.T) {
	data := deviceBytes(128 << 10)
	r := newFake(data)
	r.failFrom = 32 << 10 // start of the second chunk
	r.failLen = 32 << 10

	result, err := libewf.Verify(context.Background(), r)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	if len(result.BadRanges) == 0 {
		t.Fatal("BadRanges is empty, want the unreadable span recorded")
	}
	if got := result.BadRanges[0].Offset; got != 32<<10 {
		t.Errorf("BadRanges[0].Offset = %d, want %d", got, 32<<10)
	}
	// Offsets after the gap must still line up, so the whole device is hashed.
	if result.BytesHashed != r.Size() {
		t.Errorf("BytesHashed = %d, want %d (gaps are zero-filled)", result.BytesHashed, r.Size())
	}
	if result.OK() {
		t.Error("OK() = true despite an unreadable span, want false")
	}
}

// TestVerifyWithoutStoredDigests guards against reporting success for an image
// that carries nothing to verify against.
func TestVerifyWithoutStoredDigests(t *testing.T) {
	r := newFake(deviceBytes(4 << 10))
	r.storedMD5 = nil
	r.storedSHA1 = nil

	result, err := libewf.Verify(context.Background(), r)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	if result.HasStoredMD5 || result.HasStoredSHA1 {
		t.Error("reported stored digests where the image has none")
	}
	if len(result.ComputedMD5) == 0 || len(result.ComputedSHA1) == 0 {
		t.Error("digests were not computed")
	}
	if result.OK() {
		t.Error("OK() = true with nothing to verify against, want false")
	}
}

func TestVerifyMD5OnlyImage(t *testing.T) {
	r := newFake(deviceBytes(8 << 10))
	r.storedSHA1 = nil // EnCase 5 and earlier record only an MD5

	result, err := libewf.Verify(context.Background(), r)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !result.MD5Match {
		t.Error("MD5Match = false, want true")
	}
	if result.SHA1Match {
		t.Error("SHA1Match = true with no stored SHA-1, want false")
	}
	if !result.OK() {
		t.Error("OK() = false, want true: every digest the image carries matched")
	}
}

func TestVerifyHonoursContextCancellation(t *testing.T) {
	r := newFake(deviceBytes(8 << 20))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := libewf.Verify(ctx, r); !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify() error = %v, want context.Canceled", err)
	}
}

func TestVerifyRejectsSizelessImage(t *testing.T) {
	r := newFake(nil)
	if _, err := libewf.Verify(context.Background(), r); !errors.Is(err, libewf.ErrCorruptImage) {
		t.Fatalf("Verify() error = %v, want ErrCorruptImage", err)
	}
}
