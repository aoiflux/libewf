package reader

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/adler32"
	"io"
	"testing"

	"github.com/aoiflux/libewf/ewferr"
	"github.com/aoiflux/libewf/types"
)

// ---------------------------------------------------------------------------
// Synthetic EWF v1 image builder
//
// This builder emits images shaped like the ones EnCase actually writes:
// every chunk group is a "sectors" section followed by a "table" section and
// a byte-identical "table2" backup, all section and table checksums are real
// Adler-32 values, and uncompressed chunks carry their trailing checksum.
// Tests that skip any of those details cannot catch the bugs that matter.
// ---------------------------------------------------------------------------

const (
	v1FileHeaderSize = 13
	v1DescSize       = 76
	v1VolumePayload  = 1052 // sizeof(ewf_volume_t)
)

type extraSection struct {
	name    string
	payload []byte
}

type v1Image struct {
	segmentNumber uint16
	terminal      string     // "next" or "done"
	groups        [][][]byte // decoded chunk payloads, grouped per sectors/table group
	extras        []extraSection

	withTable2     bool
	addTrailer     bool // append the Adler-32 that EWF v1 stores after uncompressed chunks
	compress       bool // store chunks as zlib streams
	corruptPrimary bool // damage every primary "table" so the backup must be used
	omitVolume     bool

	sectorsPerChunk uint32
	bytesPerSector  uint32
	numberOfSectors uint64
	numberOfChunks  uint32
}

// defaultV1 returns a complete, single-segment, realistically-shaped image
// with one group of 8-byte chunks.
func defaultV1(chunks ...[]byte) v1Image {
	return v1Image{
		segmentNumber:   1,
		terminal:        "done",
		groups:          [][][]byte{chunks},
		withTable2:      true,
		addTrailer:      true,
		sectorsPerChunk: 1,
		bytesPerSector:  8,
		numberOfSectors: uint64(len(chunks)),
		numberOfChunks:  uint32(len(chunks)),
	}
}

func (spec v1Image) storeChunk(payload []byte) []byte {
	if spec.compress {
		var buf bytes.Buffer
		w := zlib.NewWriter(&buf)
		_, _ = w.Write(payload)
		_ = w.Close()
		return buf.Bytes()
	}
	out := append([]byte{}, payload...)
	if spec.addTrailer {
		var sum [4]byte
		binary.LittleEndian.PutUint32(sum[:], adler32.Checksum(payload))
		out = append(out, sum[:]...)
	}
	return out
}

func tableSectionSize(entries int) uint64 {
	return uint64(v1DescSize + tableHeaderV1Size + entries*tableEntryV1Size + tableChecksumSize)
}

func (spec v1Image) build() []byte {
	stored := make([][][]byte, len(spec.groups))
	for gi, group := range spec.groups {
		stored[gi] = make([][]byte, len(group))
		for ci, payload := range group {
			stored[gi][ci] = spec.storeChunk(payload)
		}
	}

	// Layout pass.
	type groupLayout struct {
		sectorsOffset, sectorsData, tableOffset, table2Offset int
		sectorsPayload                                        int
	}

	offset := v1FileHeaderSize
	volumeOffset := offset
	if !spec.omitVolume {
		offset += v1DescSize + v1VolumePayload
	}

	extraOffsets := make([]int, len(spec.extras))
	for i, e := range spec.extras {
		extraOffsets[i] = offset
		offset += v1DescSize + len(e.payload)
	}

	layouts := make([]groupLayout, len(stored))
	for gi, group := range stored {
		total := 0
		for _, c := range group {
			total += len(c)
		}
		l := groupLayout{sectorsOffset: offset, sectorsPayload: total}
		l.sectorsData = l.sectorsOffset + v1DescSize
		l.tableOffset = l.sectorsData + total
		offset = l.tableOffset + int(tableSectionSize(len(group)))
		if spec.withTable2 {
			l.table2Offset = offset
			offset += int(tableSectionSize(len(group)))
		}
		layouts[gi] = l
	}
	terminalOffset := offset
	buf := make([]byte, terminalOffset+v1DescSize)

	// File header.
	copy(buf[0:8], types.SignatureEVFv1[:])
	buf[8] = 0x01
	binary.LittleEndian.PutUint16(buf[9:11], spec.segmentNumber)

	if !spec.omitVolume {
		writeV1Descriptor(buf, volumeOffset, "volume", uint64(v1DescSize+v1VolumePayload))
		v := volumeOffset + v1DescSize
		buf[v] = types.MediaTypeFixed
		binary.LittleEndian.PutUint32(buf[v+4:v+8], spec.numberOfChunks)
		binary.LittleEndian.PutUint32(buf[v+8:v+12], spec.sectorsPerChunk)
		binary.LittleEndian.PutUint32(buf[v+12:v+16], spec.bytesPerSector)
		binary.LittleEndian.PutUint64(buf[v+16:v+24], spec.numberOfSectors)
	}

	for i, e := range spec.extras {
		writeV1Descriptor(buf, extraOffsets[i], e.name, uint64(v1DescSize+len(e.payload)))
		copy(buf[extraOffsets[i]+v1DescSize:], e.payload)
	}

	for gi, group := range stored {
		l := layouts[gi]
		writeV1Descriptor(buf, l.sectorsOffset, "sectors", uint64(v1DescSize+l.sectorsPayload))
		at := l.sectorsData
		for _, c := range group {
			copy(buf[at:], c)
			at += len(c)
		}

		writeV1Descriptor(buf, l.tableOffset, "table", tableSectionSize(len(group)))
		writeV1Table(buf, l.tableOffset, l.sectorsData, group, spec.compress, spec.corruptPrimary)

		if spec.withTable2 {
			writeV1Descriptor(buf, l.table2Offset, "table2", tableSectionSize(len(group)))
			writeV1Table(buf, l.table2Offset, l.sectorsData, group, spec.compress, false)
		}
	}

	writeV1Descriptor(buf, terminalOffset, spec.terminal, 0)
	return buf
}

func writeV1Descriptor(buf []byte, offset int, typeString string, size uint64) {
	copy(buf[offset:offset+16], typeString)
	binary.LittleEndian.PutUint64(buf[offset+16:offset+24], uint64(offset)+size)
	binary.LittleEndian.PutUint64(buf[offset+24:offset+32], size)
	binary.LittleEndian.PutUint32(buf[offset+72:offset+76], adler32.Checksum(buf[offset:offset+72]))
}

func writeV1Table(buf []byte, tableOffset, baseOffset int, storedChunks [][]byte, compressed, corrupt bool) {
	td := tableOffset + v1DescSize
	n := len(storedChunks)

	binary.LittleEndian.PutUint32(buf[td:td+4], uint32(n))
	binary.LittleEndian.PutUint64(buf[td+8:td+16], uint64(baseOffset))
	binary.LittleEndian.PutUint32(buf[td+20:td+24], adler32.Checksum(buf[td:td+20]))

	entriesStart := td + tableHeaderV1Size
	rel := uint32(0)
	for i, c := range storedChunks {
		value := rel
		if compressed {
			value |= 1 << 31
		}
		binary.LittleEndian.PutUint32(buf[entriesStart+i*4:entriesStart+i*4+4], value)
		rel += uint32(len(c))
	}
	entriesEnd := entriesStart + n*tableEntryV1Size
	binary.LittleEndian.PutUint32(buf[entriesEnd:entriesEnd+4], adler32.Checksum(buf[entriesStart:entriesEnd]))

	if corrupt {
		// Damage an entry without refreshing the checksums, so the copy fails
		// validation exactly as a bit-rotted table on disk would.
		buf[entriesStart] ^= 0xFF
	}
}

func writeV2SectionDescriptor(buf []byte, offset int, sectionType uint32, size uint64, flags uint32) {
	previousOffset := uint64(0)
	if size > uint64(offset)+32 {
		panic("invalid v2 descriptor size for synthetic test image")
	}
	if size <= uint64(offset) {
		previousOffset = uint64(offset) - size
	}
	binary.LittleEndian.PutUint32(buf[offset:offset+4], sectionType)
	binary.LittleEndian.PutUint32(buf[offset+4:offset+8], flags)
	binary.LittleEndian.PutUint64(buf[offset+8:offset+16], previousOffset)
	binary.LittleEndian.PutUint64(buf[offset+16:offset+24], size)
	binary.LittleEndian.PutUint32(buf[offset+24:offset+28], 64)
}

func mustOpen(t *testing.T, image []byte, opts ...Options) *ImageReader {
	t.Helper()
	o := Options{}
	if len(opts) > 0 {
		o = opts[0]
	}
	r, err := OpenWithOptions(bytes.NewReader(image), o)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return r
}

func readAll(t *testing.T, r *ImageReader) []byte {
	t.Helper()
	buf := make([]byte, r.Size())
	n, err := r.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt() error = %v", err)
	}
	return buf[:n]
}

// ---------------------------------------------------------------------------
// Regression tests for the chunk-table duplication defect
// ---------------------------------------------------------------------------

// TestTable2IsNotConsumedAsChunks is the regression test for the defect where
// "table" and "table2" both mapped to SectionTypeSectorTable and both had
// their chunks appended, doubling the chunk table and silently returning the
// wrong bytes for every offset past the first group.
func TestTable2IsNotConsumedAsChunks(t *testing.T) {
	spec := defaultV1()
	spec.groups = [][][]byte{
		{[]byte("AAAAAAAA")},
		{[]byte("BBBBBBBB")},
		{[]byte("CCCCCCCC")},
	}
	spec.numberOfSectors = 3
	spec.numberOfChunks = 3

	r := mustOpen(t, spec.build())

	if got, want := r.Size(), int64(24); got != want {
		t.Fatalf("Size() = %d, want %d", got, want)
	}
	if got, want := r.Metadata().ObservedChunkCount, uint64(3); got != want {
		t.Fatalf("ObservedChunkCount = %d, want %d (table2 copies must not be counted)", got, want)
	}
	if got, want := string(readAll(t, r)), "AAAAAAAABBBBBBBBCCCCCCCC"; got != want {
		t.Fatalf("decoded device = %q, want %q", got, want)
	}
}

// TestTable2RecoversCorruptPrimaryTable verifies the backup copy is used for
// what it exists for: recovery when the primary table fails its checksum.
func TestTable2RecoversCorruptPrimaryTable(t *testing.T) {
	spec := defaultV1([]byte("AAAAAAAA"), []byte("BBBBBBBB"))
	spec.corruptPrimary = true

	r := mustOpen(t, spec.build())

	if got, want := string(readAll(t, r)), "AAAAAAAABBBBBBBB"; got != want {
		t.Fatalf("decoded device = %q, want %q", got, want)
	}
	meta := r.Metadata()
	if meta.ChunkTablesRecovered != 1 {
		t.Fatalf("ChunkTablesRecovered = %d, want 1", meta.ChunkTablesRecovered)
	}
	if meta.ChunkTablesInvalid != 0 {
		t.Fatalf("ChunkTablesInvalid = %d, want 0", meta.ChunkTablesInvalid)
	}
}

// TestCorruptPrimaryWithoutBackupIsReportedUnverified covers the case where no
// usable copy exists: the chunks are still decoded, but the caller is told.
func TestCorruptPrimaryWithoutBackupIsReportedUnverified(t *testing.T) {
	spec := defaultV1([]byte("AAAAAAAA"))
	spec.withTable2 = false
	spec.corruptPrimary = true

	r := mustOpen(t, spec.build())
	if got := r.Metadata().ChunkTablesInvalid; got != 1 {
		t.Fatalf("ChunkTablesInvalid = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// ReadAt bounds and io.ReaderAt conformance
// ---------------------------------------------------------------------------

func TestReadAtNegativeOffsetReturnsErrorNotPanic(t *testing.T) {
	r := mustOpen(t, defaultV1([]byte("AAAAAAAA")).build())

	defer func() {
		if e := recover(); e != nil {
			t.Fatalf("ReadAt() panicked on negative offset: %v", e)
		}
	}()

	n, err := r.ReadAt(make([]byte, 4), -1)
	if n != 0 {
		t.Fatalf("ReadAt() n = %d, want 0", n)
	}
	if !errors.Is(err, ewferr.ErrInvalidOffset) {
		t.Fatalf("ReadAt() error = %v, want ErrInvalidOffset", err)
	}
}

// TestReadAtStopsAtLogicalEnd covers a partial final chunk, the normal case for
// any image whose size is not a whole multiple of the chunk size. The stored
// chunk carries an Adler-32 trailer that must never surface as device data.
func TestReadAtStopsAtLogicalEnd(t *testing.T) {
	payload := []byte("ABCDEFGHIJKLMNOPQRST") // 20 bytes
	spec := defaultV1(payload)
	spec.sectorsPerChunk = 8 // chunk size = 8 * 4 = 32 bytes
	spec.bytesPerSector = 4
	spec.numberOfSectors = 5 // logical device = 20 bytes
	spec.numberOfChunks = 1

	r := mustOpen(t, spec.build())
	if got, want := r.Size(), int64(20); got != want {
		t.Fatalf("Size() = %d, want %d", got, want)
	}

	buf := make([]byte, 32)
	n, err := r.ReadAt(buf, 0)
	if err != io.EOF {
		t.Fatalf("ReadAt() error = %v, want io.EOF", err)
	}
	if got, want := n, 20; got != want {
		t.Fatalf("ReadAt() n = %d, want %d (checksum trailer must not be returned)", got, want)
	}
	if got, want := string(buf[:n]), string(payload); got != want {
		t.Fatalf("ReadAt() = %q, want %q", got, want)
	}
}

func TestReadAtPastLogicalEnd(t *testing.T) {
	r := mustOpen(t, defaultV1([]byte("AAAAAAAA")).build())

	n, err := r.ReadAt(make([]byte, 8), r.Size())
	if n != 0 || err != io.EOF {
		t.Fatalf("ReadAt(at Size()) = (%d, %v), want (0, io.EOF)", n, err)
	}
	n, err = r.ReadAt(make([]byte, 8), r.Size()+4096)
	if n != 0 || err != io.EOF {
		t.Fatalf("ReadAt(past Size()) = (%d, %v), want (0, io.EOF)", n, err)
	}
}

// TestReadAtSplitReadsMatchWholeRead asserts the io.ReaderAt property that
// results do not depend on read granularity.
func TestReadAtSplitReadsMatchWholeRead(t *testing.T) {
	spec := defaultV1([]byte("AAAAAAAA"), []byte("BBBBBBBB"), []byte("CCCCCCCC"))
	r := mustOpen(t, spec.build())

	whole := readAll(t, r)
	for _, step := range []int{1, 3, 5, 7, 8, 16} {
		got := make([]byte, 0, len(whole))
		for off := int64(0); off < r.Size(); off += int64(step) {
			buf := make([]byte, step)
			n, err := r.ReadAt(buf, off)
			if err != nil && err != io.EOF {
				t.Fatalf("step %d: ReadAt(%d) error = %v", step, off, err)
			}
			got = append(got, buf[:n]...)
		}
		if !bytes.Equal(got, whole) {
			t.Fatalf("step %d: split read = %q, want %q", step, got, whole)
		}
	}
}

func TestReadAtEmptyBuffer(t *testing.T) {
	r := mustOpen(t, defaultV1([]byte("AAAAAAAA")).build())
	if n, err := r.ReadAt(nil, 0); n != 0 || err != nil {
		t.Fatalf("ReadAt(nil) = (%d, %v), want (0, nil)", n, err)
	}
}

func TestReadAtCompressedChunks(t *testing.T) {
	spec := defaultV1([]byte("AAAAAAAA"), []byte("BBBBBBBB"))
	spec.compress = true
	spec.addTrailer = false

	r := mustOpen(t, spec.build())
	if got, want := string(readAll(t, r)), "AAAAAAAABBBBBBBB"; got != want {
		t.Fatalf("decoded device = %q, want %q", got, want)
	}
}

func TestReadAtConcurrent(t *testing.T) {
	spec := defaultV1([]byte("AAAAAAAA"), []byte("BBBBBBBB"), []byte("CCCCCCCC"))
	r := mustOpen(t, spec.build())
	want := readAll(t, r)

	done := make(chan []byte, 8)
	for i := 0; i < 8; i++ {
		go func() {
			buf := make([]byte, r.Size())
			n, _ := r.ReadAt(buf, 0)
			done <- buf[:n]
		}()
	}
	for i := 0; i < 8; i++ {
		if got := <-done; !bytes.Equal(got, want) {
			t.Fatalf("concurrent read = %q, want %q", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Geometry
// ---------------------------------------------------------------------------

func TestSizeAndSectorSize(t *testing.T) {
	spec := defaultV1([]byte("AAAAAAAA"), []byte("BBBBBBBB"))
	spec.sectorsPerChunk = 1
	spec.bytesPerSector = 8
	spec.numberOfSectors = 2

	r := mustOpen(t, spec.build())
	if got, want := r.Size(), int64(16); got != want {
		t.Fatalf("Size() = %d, want %d", got, want)
	}
	if got, want := r.SectorSize(), 8; got != want {
		t.Fatalf("SectorSize() = %d, want %d", got, want)
	}
}

func TestInvalidGeometryRejectedAtOpen(t *testing.T) {
	for _, tc := range []struct {
		name            string
		sectorsPerChunk uint32
		bytesPerSector  uint32
	}{
		{"zero bytes per sector", 1, 0},
		{"zero sectors per chunk", 0, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := defaultV1([]byte("AAAAAAAA"))
			spec.sectorsPerChunk = tc.sectorsPerChunk
			spec.bytesPerSector = tc.bytesPerSector

			_, err := Open(bytes.NewReader(spec.build()))
			if !errors.Is(err, ewferr.ErrCorruptImage) {
				t.Fatalf("Open() error = %v, want ErrCorruptImage", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Segment sets
// ---------------------------------------------------------------------------

func multiSegmentSet(t *testing.T, chunkPerSegment ...[]byte) []io.ReaderAt {
	t.Helper()
	sources := make([]io.ReaderAt, len(chunkPerSegment))
	for i, chunk := range chunkPerSegment {
		spec := defaultV1(chunk)
		spec.segmentNumber = uint16(i + 1)
		spec.terminal = "next"
		if i == len(chunkPerSegment)-1 {
			spec.terminal = "done"
		}
		if i > 0 {
			spec.omitVolume = true
		}
		// Segment 1 declares the geometry of the whole set.
		spec.numberOfSectors = uint64(len(chunkPerSegment))
		spec.numberOfChunks = uint32(len(chunkPerSegment))
		sources[i] = bytes.NewReader(spec.build())
	}
	return sources
}

func TestOpenSegmentsReadsAcrossBoundary(t *testing.T) {
	sources := multiSegmentSet(t, []byte("AAAAAAAA"), []byte("BBBBBBBB"))
	// Supplied out of order on purpose.
	r, err := OpenSegments([]io.ReaderAt{sources[1], sources[0]})
	if err != nil {
		t.Fatalf("OpenSegments() error = %v", err)
	}

	if got, want := r.Size(), int64(16); got != want {
		t.Fatalf("Size() = %d, want %d", got, want)
	}
	if got, want := string(readAll(t, r)), "AAAAAAAABBBBBBBB"; got != want {
		t.Fatalf("decoded device = %q, want %q", got, want)
	}

	meta := r.Metadata()
	// The declared count comes from the volume section and is preserved;
	// the observed count is reported separately.
	if got, want := meta.Media.NumberOfChunks, uint64(2); got != want {
		t.Fatalf("Media.NumberOfChunks = %d, want %d", got, want)
	}
	if got, want := meta.ObservedChunkCount, uint64(2); got != want {
		t.Fatalf("ObservedChunkCount = %d, want %d", got, want)
	}
}

func TestOpenSegmentsMissingSegment(t *testing.T) {
	sources := multiSegmentSet(t, []byte("AAAAAAAA"), []byte("BBBBBBBB"), []byte("CCCCCCCC"))
	// Hand over segments 1 and 3 only: segment 2 is missing.
	_, err := OpenSegments([]io.ReaderAt{sources[0], sources[2]})
	if !errors.Is(err, ewferr.ErrMissingSegment) {
		t.Fatalf("OpenSegments() error = %v, want ErrMissingSegment", err)
	}
}

func TestOpenSegmentsIncompleteSetRejected(t *testing.T) {
	sources := multiSegmentSet(t, []byte("AAAAAAAA"), []byte("BBBBBBBB"))
	// Only the first segment: it terminates with "next", so more must exist.
	_, err := OpenSegments([]io.ReaderAt{sources[0]})
	if !errors.Is(err, ewferr.ErrIncompleteSegmentSet) {
		t.Fatalf("OpenSegments() error = %v, want ErrIncompleteSegmentSet", err)
	}
}

func TestOpenSegmentsIncompleteSetAllowed(t *testing.T) {
	sources := multiSegmentSet(t, []byte("AAAAAAAA"), []byte("BBBBBBBB"))
	r, err := OpenSegmentsWithOptions([]io.ReaderAt{sources[0]}, Options{AllowIncompleteSegmentSet: true})
	if err != nil {
		t.Fatalf("OpenSegmentsWithOptions() error = %v", err)
	}

	// Size still reports the whole declared device...
	if got, want := r.Size(), int64(16); got != want {
		t.Fatalf("Size() = %d, want %d", got, want)
	}
	// ...but only the supplied prefix decodes, and the shortfall is visible.
	if got, want := string(readAll(t, r)), "AAAAAAAA"; got != want {
		t.Fatalf("decoded device = %q, want %q", got, want)
	}
	meta := r.Metadata()
	if meta.ObservedChunkCount >= meta.Media.NumberOfChunks {
		t.Fatalf("ObservedChunkCount = %d, want fewer than declared %d",
			meta.ObservedChunkCount, meta.Media.NumberOfChunks)
	}
}

func TestOpenSegmentsDuplicateSegmentNumber(t *testing.T) {
	a := defaultV1([]byte("AAAAAAAA"))
	a.terminal = "next"
	b := defaultV1([]byte("BBBBBBBB"))

	_, err := OpenSegments([]io.ReaderAt{bytes.NewReader(a.build()), bytes.NewReader(b.build())})
	if !errors.Is(err, ewferr.ErrCorruptImage) {
		t.Fatalf("OpenSegments() error = %v, want ErrCorruptImage for duplicate segment number", err)
	}
}

func TestOpenSegmentsEmpty(t *testing.T) {
	if _, err := OpenSegments(nil); err == nil {
		t.Fatal("OpenSegments(nil) expected an error")
	}
}

// ---------------------------------------------------------------------------
// Headers, sections and digests
// ---------------------------------------------------------------------------

func TestParseFileHeaderV1Fields(t *testing.T) {
	data := make([]byte, 13)
	copy(data[0:8], types.SignatureEVFv1[:])
	data[8] = 0x01
	binary.LittleEndian.PutUint16(data[9:11], 2)

	info, err := parseFileHeaderV1(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parseFileHeaderV1() error = %v", err)
	}
	if got, want := info.SegmentNumber, uint32(2); got != want {
		t.Fatalf("SegmentNumber = %d, want %d", got, want)
	}
	if got, want := info.MajorVersion, uint8(1); got != want {
		t.Fatalf("MajorVersion = %d, want %d", got, want)
	}
}

func TestParseFileHeaderV1Invalid(t *testing.T) {
	data := make([]byte, 13)
	copy(data[0:8], types.SignatureEVFv1[:])
	data[8] = 0x02 // fields_start must be 0x01

	if _, err := parseFileHeaderV1(bytes.NewReader(data)); !errors.Is(err, ewferr.ErrCorruptImage) {
		t.Fatalf("parseFileHeaderV1() error = %v, want ErrCorruptImage", err)
	}
}

// TestDigestSectionYieldsBothHashes covers the 80-byte "digest" section, which
// carries an MD5 at offset 0 and a SHA-1 at offset 16.
func TestDigestSectionYieldsBothHashes(t *testing.T) {
	digest := make([]byte, 80)
	digest[0] = 0x11
	digest[16] = 0x22

	spec := defaultV1([]byte("AAAAAAAA"))
	spec.extras = []extraSection{{name: "digest", payload: digest}}

	r := mustOpen(t, spec.build())
	meta := r.Metadata()

	if !meta.HasMD5Digest || !meta.HasSHA1Digest {
		t.Fatalf("HasMD5Digest = %v, HasSHA1Digest = %v, want both true", meta.HasMD5Digest, meta.HasSHA1Digest)
	}
	if got := meta.MD5Digest[0]; got != 0x11 {
		t.Fatalf("MD5Digest[0] = 0x%02x, want 0x11", got)
	}
	if got := meta.SHA1Digest[0]; got != 0x22 {
		t.Fatalf("SHA1Digest[0] = 0x%02x, want 0x22", got)
	}

	md5, ok := r.MD5()
	if !ok || md5[0] != 0x11 {
		t.Fatalf("MD5() = (%x, %v), want digest starting 0x11", md5, ok)
	}
	sha1, ok := r.SHA1()
	if !ok || sha1[0] != 0x22 {
		t.Fatalf("SHA1() = (%x, %v), want digest starting 0x22", sha1, ok)
	}
}

// TestHashSectionYieldsMD5NotSHA1 is the regression test for the mapping bug
// where the 36-byte v1 "hash" section was classified as a SHA-1 section. Its
// payload is an MD5; reading it as a SHA-1 fabricated a digest that was never
// in the image and reported no MD5 at all.
func TestHashSectionYieldsMD5NotSHA1(t *testing.T) {
	hash := make([]byte, 36) // ewf_hash_t: md5[16] + unknown[16] + checksum[4]
	hash[0] = 0x33

	spec := defaultV1([]byte("AAAAAAAA"))
	spec.extras = []extraSection{{name: "hash", payload: hash}}

	meta := mustOpen(t, spec.build()).Metadata()

	if !meta.HasMD5Digest {
		t.Fatal("HasMD5Digest = false, want true: the hash section carries an MD5")
	}
	if got := meta.MD5Digest[0]; got != 0x33 {
		t.Fatalf("MD5Digest[0] = 0x%02x, want 0x33", got)
	}
	if meta.HasSHA1Digest {
		t.Fatal("HasSHA1Digest = true, want false: a hash section contains no SHA-1")
	}
}

// TestXHashSectionDoesNotFabricateDigest guards the other half of that bug:
// "xhash" is a compressed XML document, so raw bytes in that position must not
// be mistaken for a digest.
func TestXHashSectionDoesNotFabricateDigest(t *testing.T) {
	spec := defaultV1([]byte("AAAAAAAA"))
	spec.extras = []extraSection{{name: "xhash", payload: bytes.Repeat([]byte{0x44}, 64)}}

	meta := mustOpen(t, spec.build()).Metadata()
	if meta.HasSHA1Digest {
		t.Fatal("HasSHA1Digest = true, want false: xhash is XML, not a SHA-1")
	}
}

// zlibXML compresses an XML document the way libewf stores xhash and xheader.
func zlibXML(doc string) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write([]byte("\xef\xbb\xbf" + doc)) // libewf emits a UTF-8 BOM
	_ = w.Close()
	return buf.Bytes()
}

// TestXHashSectionYieldsDigests covers the EWF-X dialect, which records its
// SHA-1 only in the compressed XML xhash section. The corpus proved this was
// invisible to the reader while ewfinfo could see it.
func TestXHashSectionYieldsDigests(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<xhash>
	<MD5>85f0fb6e7af9549c5f179db5d22a4358</MD5>
	<SHA1>87084f2cd58c096bc59adc7e0b484a50c3d65435</SHA1>
</xhash>`

	spec := defaultV1([]byte("AAAAAAAA"))
	spec.extras = []extraSection{{name: "xhash", payload: zlibXML(doc)}}

	meta := mustOpen(t, spec.build()).Metadata()

	if !meta.HasMD5Digest {
		t.Fatal("HasMD5Digest = false, want true")
	}
	if got, want := hex.EncodeToString(meta.MD5Digest[:]), "85f0fb6e7af9549c5f179db5d22a4358"; got != want {
		t.Errorf("MD5Digest = %s, want %s", got, want)
	}
	if !meta.HasSHA1Digest {
		t.Fatal("HasSHA1Digest = false, want true")
	}
	if got, want := hex.EncodeToString(meta.SHA1Digest[:]), "87084f2cd58c096bc59adc7e0b484a50c3d65435"; got != want {
		t.Errorf("SHA1Digest = %s, want %s", got, want)
	}
}

// TestXHashLowercaseTags checks the case-insensitive element matching: a
// case-sensitive struct decode would silently yield nothing here.
func TestXHashLowercaseTags(t *testing.T) {
	const doc = `<xhash><md5>85f0fb6e7af9549c5f179db5d22a4358</md5></xhash>`

	spec := defaultV1([]byte("AAAAAAAA"))
	spec.extras = []extraSection{{name: "xhash", payload: zlibXML(doc)}}

	meta := mustOpen(t, spec.build()).Metadata()
	if !meta.HasMD5Digest {
		t.Fatal("HasMD5Digest = false, want true for lowercase <md5>")
	}
}

// TestBinaryDigestSectionWinsOverXHash pins the precedence rule: fixed-layout
// sections cannot be ambiguous, so they outrank the XML copy.
func TestBinaryDigestSectionWinsOverXHash(t *testing.T) {
	binaryHash := make([]byte, 36)
	binaryHash[0] = 0xAB

	spec := defaultV1([]byte("AAAAAAAA"))
	spec.extras = []extraSection{
		{name: "hash", payload: binaryHash},
		{name: "xhash", payload: zlibXML(`<xhash><MD5>85f0fb6e7af9549c5f179db5d22a4358</MD5></xhash>`)},
	}

	meta := mustOpen(t, spec.build()).Metadata()
	if got := meta.MD5Digest[0]; got != 0xAB {
		t.Fatalf("MD5Digest[0] = 0x%02x, want 0xAB from the binary hash section", got)
	}
}

// TestXHashMalformedIsIgnored ensures a damaged XML section cannot inject a
// bogus digest or crash the parse.
func TestXHashMalformedIsIgnored(t *testing.T) {
	for name, payload := range map[string][]byte{
		"truncated zlib": zlibXML(`<xhash><SHA1>87084f2c`)[:8],
		"not xml":        zlibXML("plain text, no markup at all"),
		"short digest":   zlibXML(`<xhash><SHA1>abcd</SHA1></xhash>`),
		"not hex":        zlibXML(`<xhash><SHA1>zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz</SHA1></xhash>`),
	} {
		t.Run(name, func(t *testing.T) {
			spec := defaultV1([]byte("AAAAAAAA"))
			spec.extras = []extraSection{{name: "xhash", payload: payload}}

			meta := mustOpen(t, spec.build()).Metadata()
			if meta.HasSHA1Digest {
				t.Fatalf("HasSHA1Digest = true, want false for %s", name)
			}
		})
	}
}

// TestErrorSectionsAreNotDoubleCounted guards against the same duplication
// class as table2: "error" and "error2" both map to the error-table type.
func TestErrorSectionsAreNotDoubleCounted(t *testing.T) {
	payload := make([]byte, errorHeaderV1Size+errorEntryV1Size)
	binary.LittleEndian.PutUint32(payload[0:4], 1)
	binary.LittleEndian.PutUint32(payload[errorHeaderV1Size:], 64)  // start sector
	binary.LittleEndian.PutUint32(payload[errorHeaderV1Size+4:], 8) // sector count

	spec := defaultV1([]byte("AAAAAAAA"))
	spec.extras = []extraSection{
		{name: "error2", payload: payload},
		{name: "error", payload: payload},
	}

	meta := mustOpen(t, spec.build()).Metadata()
	if got, want := len(meta.AcquisitionErrors), 1; got != want {
		t.Fatalf("len(AcquisitionErrors) = %d, want %d", got, want)
	}
	if got, want := meta.AcquisitionErrors[0].StartSector, uint64(64); got != want {
		t.Fatalf("AcquisitionErrors[0].StartSector = %d, want %d", got, want)
	}
}

func TestSectionInventory(t *testing.T) {
	spec := defaultV1([]byte("AAAAAAAA"))
	r := mustOpen(t, spec.build())

	sections := r.Sections()
	// volume, sectors, table, table2, done
	if got, want := len(sections), 5; got != want {
		t.Fatalf("len(Sections()) = %d, want %d", got, want)
	}
	if got, want := sections[len(sections)-1].TypeString, "done"; got != want {
		t.Fatalf("last section = %q, want %q", got, want)
	}
	if !r.Metadata().HasDoneSection {
		t.Fatal("HasDoneSection = false, want true")
	}
	if got, want := r.SegmentFileType(), SegmentFileTypeEWF1; got != want {
		t.Fatalf("SegmentFileType() = %v, want %v", got, want)
	}
}

func TestOpenV2Header(t *testing.T) {
	image := make([]byte, 320)
	copy(image[0:8], types.SignatureEVFv2[:])
	image[8] = 2
	image[9] = 1
	binary.LittleEndian.PutUint16(image[10:12], 1)
	binary.LittleEndian.PutUint32(image[12:16], 7)

	writeV2SectionDescriptor(image, 256, types.SectionTypeDone, 64, 0)
	writeV2SectionDescriptor(image, 192, types.SectionTypeDeviceInformation, 192, types.SectionDataFlagHasIntegrityHash)

	// Segment 7 on its own is not a complete set; open it for inspection.
	r, err := OpenWithOptions(bytes.NewReader(image), Options{AllowIncompleteSegmentSet: true})
	if err != nil {
		t.Fatalf("OpenWithOptions() error = %v", err)
	}

	if got, want := r.SegmentFileType(), SegmentFileTypeEWF2; got != want {
		t.Fatalf("SegmentFileType() = %v, want %v", got, want)
	}
	if got, want := r.Header().CompressionMethod, uint16(1); got != want {
		t.Fatalf("CompressionMethod = %d, want %d", got, want)
	}
	if got, want := r.Header().SegmentNumber, uint32(7); got != want {
		t.Fatalf("SegmentNumber = %d, want %d", got, want)
	}
	if got, want := len(r.Sections()), 2; got != want {
		t.Fatalf("len(Sections()) = %d, want %d", got, want)
	}
	if !r.Metadata().HasIntegrityHashBlocks {
		t.Fatal("HasIntegrityHashBlocks = false, want true")
	}
	// No volume geometry was decoded, so the device has no usable size.
	if got := r.Size(); got != 0 {
		t.Fatalf("Size() = %d, want 0", got)
	}
	if _, err := r.ReadAt(make([]byte, 4), 0); !errors.Is(err, ewferr.ErrCorruptImage) {
		t.Fatalf("ReadAt() error = %v, want ErrCorruptImage", err)
	}
}

func TestOpenInvalidSignature(t *testing.T) {
	data := make([]byte, 32)
	copy(data[0:8], []byte{0, 1, 2, 3, 4, 5, 6, 7})

	_, err := Open(bytes.NewReader(data))
	if !errors.Is(err, ewferr.ErrUnsupportedFormat) {
		t.Fatalf("Open() error = %v, want ErrUnsupportedFormat", err)
	}
}

// ---------------------------------------------------------------------------
// Damaged input must never panic
// ---------------------------------------------------------------------------

func TestTruncatedAndCorruptInputsDoNotPanic(t *testing.T) {
	spec := defaultV1([]byte("AAAAAAAA"), []byte("BBBBBBBB"))
	full := spec.build()

	exercise := func(t *testing.T, image []byte) {
		t.Helper()
		defer func() {
			if e := recover(); e != nil {
				t.Fatalf("panic on damaged input: %v", e)
			}
		}()
		r, err := OpenWithOptions(bytes.NewReader(image), Options{AllowIncompleteSegmentSet: true})
		if err != nil {
			return // rejecting damaged input is a valid outcome
		}
		for _, off := range []int64{0, 1, 7, 8, 4096} {
			_, _ = r.ReadAt(make([]byte, 64), off)
		}
	}

	t.Run("truncation", func(t *testing.T) {
		for cut := 0; cut < len(full); cut += 7 {
			exercise(t, full[:cut])
		}
	})

	t.Run("single byte corruption", func(t *testing.T) {
		for pos := 0; pos < len(full); pos += 13 {
			damaged := append([]byte{}, full...)
			damaged[pos] ^= 0xFF
			exercise(t, damaged)
		}
	})
}

func FuzzOpenAndRead(f *testing.F) {
	f.Add(defaultV1([]byte("AAAAAAAA")).build())

	spec := defaultV1([]byte("AAAAAAAA"), []byte("BBBBBBBB"))
	spec.compress = true
	spec.addTrailer = false
	f.Add(spec.build())

	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := OpenWithOptions(bytes.NewReader(data), Options{AllowIncompleteSegmentSet: true})
		if err != nil {
			return
		}
		buf := make([]byte, 512)
		for _, off := range []int64{0, 1, 512, 1 << 20} {
			n, err := r.ReadAt(buf, off)
			if n < 0 || n > len(buf) {
				t.Fatalf("ReadAt returned n = %d for buffer of %d", n, len(buf))
			}
			if n < len(buf) && err == nil {
				t.Fatalf("ReadAt returned short read n = %d with nil error", n)
			}
		}
	})
}
