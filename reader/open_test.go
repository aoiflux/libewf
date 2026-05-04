package reader

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/aoiflux/libewf/types"
)

func TestOpenV1Header(t *testing.T) {
	const (
		v1HeaderOffset = 13
		v1VolumeSize   = 76 + 1088
		v1DigestSize   = 76 + 80
	)
	image := make([]byte, 1536)
	copy(image[0:8], types.SignatureEVFv1[:])
	image[8] = 0x01
	image[9] = 0x02
	image[10] = 0x00
	image[11] = 0x00
	image[12] = 0x00

	volumeOffset := v1HeaderOffset
	digestOffset := volumeOffset + v1VolumeSize
	doneOffset := digestOffset + v1DigestSize

	writeV1SectionDescriptor(image, volumeOffset, "volume", v1VolumeSize)
	writeV1SectionDescriptor(image, digestOffset, "digest", v1DigestSize)
	writeV1SectionDescriptor(image, doneOffset, "done", 0)

	volumeDataOffset := volumeOffset + 76
	binary.LittleEndian.PutUint32(image[volumeDataOffset+4:volumeDataOffset+8], 16)
	binary.LittleEndian.PutUint32(image[volumeDataOffset+8:volumeDataOffset+12], 64)
	binary.LittleEndian.PutUint32(image[volumeDataOffset+12:volumeDataOffset+16], 512)
	binary.LittleEndian.PutUint64(image[volumeDataOffset+16:volumeDataOffset+24], 1024)

	digestDataOffset := digestOffset + 76
	image[digestDataOffset] = 0x11
	image[digestDataOffset+16] = 0x22

	r, err := Open(bytes.NewReader(image))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got, want := r.SegmentFileType(), SegmentFileTypeEWF1; got != want {
		t.Fatalf("SegmentFileType() = %v, want %v", got, want)
	}
	if got, want := r.Header().SegmentNumber, uint32(2); got != want {
		t.Fatalf("Header().SegmentNumber = %d, want %d", got, want)
	}
	if got, want := len(r.Sections()), 3; got != want {
		t.Fatalf("len(Sections()) = %d, want %d", got, want)
	}
	if got, want := r.Sections()[2].Type, uint32(types.SectionTypeDone); got != want {
		t.Fatalf("Sections()[2].Type = %d, want %d", got, want)
	}
	meta := r.Metadata()
	if got, want := meta.SectionCount, 3; got != want {
		t.Fatalf("Metadata().SectionCount = %d, want %d", got, want)
	}
	if !meta.HasDoneSection {
		t.Fatal("Metadata().HasDoneSection = false, want true")
	}
	if meta.Media == nil {
		t.Fatal("Metadata().Media = nil, want parsed media info")
	}
	if got, want := meta.Media.NumberOfChunks, uint64(16); got != want {
		t.Fatalf("Metadata().Media.NumberOfChunks = %d, want %d", got, want)
	}
	if got, want := meta.Media.BytesPerSector, uint32(512); got != want {
		t.Fatalf("Metadata().Media.BytesPerSector = %d, want %d", got, want)
	}
	if !meta.HasMD5Digest {
		t.Fatal("Metadata().HasMD5Digest = false, want true")
	}
	if !meta.HasSHA1Digest {
		t.Fatal("Metadata().HasSHA1Digest = false, want true")
	}
	if got, want := meta.MD5Digest[0], byte(0x11); got != want {
		t.Fatalf("Metadata().MD5Digest[0] = 0x%02x, want 0x%02x", got, want)
	}
	if got, want := meta.SHA1Digest[0], byte(0x22); got != want {
		t.Fatalf("Metadata().SHA1Digest[0] = 0x%02x, want 0x%02x", got, want)
	}
}

func TestOpenV2Header(t *testing.T) {
	image := make([]byte, 320)
	copy(image[0:8], types.SignatureEVFv2[:])
	image[8] = 2
	image[9] = 1
	image[10] = 0x01
	image[11] = 0x00
	image[12] = 0x07
	image[13] = 0x00
	image[14] = 0x00
	image[15] = 0x00

	writeV2SectionDescriptor(image, 256, types.SectionTypeDone, 64, 0)
	writeV2SectionDescriptor(image, 192, types.SectionTypeDeviceInformation, 192, types.SectionDataFlagHasIntegrityHash)

	r, err := Open(bytes.NewReader(image))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got, want := r.SegmentFileType(), SegmentFileTypeEWF2; got != want {
		t.Fatalf("SegmentFileType() = %v, want %v", got, want)
	}
	if got, want := r.Header().CompressionMethod, uint16(1); got != want {
		t.Fatalf("Header().CompressionMethod = %d, want %d", got, want)
	}
	if got, want := r.Header().SegmentNumber, uint32(7); got != want {
		t.Fatalf("Header().SegmentNumber = %d, want %d", got, want)
	}
	if got, want := len(r.Sections()), 2; got != want {
		t.Fatalf("len(Sections()) = %d, want %d", got, want)
	}
	if got, want := r.Sections()[0].Type, uint32(types.SectionTypeDeviceInformation); got != want {
		t.Fatalf("Sections()[0].Type = %d, want %d", got, want)
	}
	if got, want := r.Sections()[1].Type, uint32(types.SectionTypeDone); got != want {
		t.Fatalf("Sections()[1].Type = %d, want %d", got, want)
	}
	meta := r.Metadata()
	if got, want := meta.SectionTypeCounts[types.SectionTypeDone], 1; got != want {
		t.Fatalf("Metadata().SectionTypeCounts[done] = %d, want %d", got, want)
	}
	if !meta.HasIntegrityHashBlocks {
		t.Fatal("Metadata().HasIntegrityHashBlocks = false, want true")
	}
}

func TestOpenInvalidSignature(t *testing.T) {
	data := make([]byte, 32)
	copy(data[0:8], []byte{0, 1, 2, 3, 4, 5, 6, 7})

	_, err := Open(bytes.NewReader(data))
	if err == nil {
		t.Fatal("Open() expected error for unsupported signature")
	}
}

func TestOpenSegmentsAndReadAcrossBoundary(t *testing.T) {
	segment1 := buildSyntheticV1DataSegment(1, types.SectionTypeNext, []byte("AAAAAAAA"))
	segment2 := buildSyntheticV1DataSegment(2, types.SectionTypeDone, []byte("BBBBBBBB"))

	r, err := OpenSegments([]io.ReaderAt{bytes.NewReader(segment2), bytes.NewReader(segment1)})
	if err != nil {
		t.Fatalf("OpenSegments() error = %v", err)
	}

	buf := make([]byte, 16)
	n, err := r.ReadAt(buf, 0)
	if err != nil {
		t.Fatalf("ReadAt() error = %v", err)
	}
	if got, want := n, 16; got != want {
		t.Fatalf("ReadAt() bytes read = %d, want %d", got, want)
	}
	if got, want := string(buf[:8]), "AAAAAAAA"; got != want {
		t.Fatalf("first chunk = %q, want %q", got, want)
	}
	if got, want := string(buf[8:]), "BBBBBBBB"; got != want {
		t.Fatalf("second chunk = %q, want %q", got, want)
	}

	meta := r.Metadata()
	if meta.Media == nil {
		t.Fatal("Metadata().Media = nil")
	}
	if got, want := meta.Media.NumberOfChunks, uint64(2); got != want {
		t.Fatalf("Metadata().Media.NumberOfChunks = %d, want %d", got, want)
	}
}

func TestOpenSegmentsDuplicateSegmentNumber(t *testing.T) {
	segment1 := buildSyntheticV1DataSegment(1, types.SectionTypeNext, []byte("AAAAAAAA"))
	segment1Dup := buildSyntheticV1DataSegment(1, types.SectionTypeDone, []byte("BBBBBBBB"))

	_, err := OpenSegments([]io.ReaderAt{bytes.NewReader(segment1), bytes.NewReader(segment1Dup)})
	if err == nil {
		t.Fatal("OpenSegments() expected duplicate segment number error")
	}
}

func TestReadAtIgnoresV1ChunkTrailerBytes(t *testing.T) {
	// 8 bytes payload + 4 bytes simulated trailer/checksum in stored chunk.
	segment := buildSyntheticV1DataSegment(1, types.SectionTypeDone, []byte("ABCDEFGH\xde\xad\xbe\xef"))

	// Force logical chunk size to 8 bytes, so trailer bytes must not be exposed.
	volumeDataOffset := 13 + 76
	binary.LittleEndian.PutUint32(segment[volumeDataOffset+12:volumeDataOffset+16], 8)
	binary.LittleEndian.PutUint64(segment[volumeDataOffset+16:volumeDataOffset+24], 8)

	r, err := Open(bytes.NewReader(segment))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	buf := make([]byte, 12)
	n, err := r.ReadAt(buf, 0)
	if err != io.EOF {
		t.Fatalf("ReadAt() error = %v, want io.EOF", err)
	}
	if got, want := n, 8; got != want {
		t.Fatalf("ReadAt() bytes read = %d, want %d", got, want)
	}
	if got, want := string(buf[:8]), "ABCDEFGH"; got != want {
		t.Fatalf("first 8 bytes = %q, want %q", got, want)
	}
}

func buildSyntheticV1DataSegment(segmentNumber uint16, terminalType uint32, chunk []byte) []byte {
	const (
		headerSize = 13
		descSize   = 76
	)

	volumeSize := uint64(descSize + 1088)
	sectorsSize := uint64(descSize + len(chunk))
	tableSize := uint64(descSize + 24 + 4)
	terminalSize := uint64(0)

	volumeOffset := headerSize
	sectorsOffset := volumeOffset + int(volumeSize)
	tableOffset := sectorsOffset + int(sectorsSize)
	terminalOffset := tableOffset + int(tableSize)
	totalSize := terminalOffset + descSize

	buf := make([]byte, totalSize)
	copy(buf[0:8], types.SignatureEVFv1[:])
	buf[8] = 0x01
	binary.LittleEndian.PutUint16(buf[9:11], segmentNumber)
	buf[11] = 0x00
	buf[12] = 0x00

	writeV1SectionDescriptor(buf, volumeOffset, "volume", volumeSize)
	writeV1SectionDescriptor(buf, sectorsOffset, "sectors", sectorsSize)
	writeV1SectionDescriptor(buf, tableOffset, "table", tableSize)
	if terminalType == types.SectionTypeDone {
		writeV1SectionDescriptor(buf, terminalOffset, "done", terminalSize)
	} else {
		writeV1SectionDescriptor(buf, terminalOffset, "next", terminalSize)
	}

	volumeDataOffset := volumeOffset + descSize
	binary.LittleEndian.PutUint32(buf[volumeDataOffset+4:volumeDataOffset+8], 1)
	binary.LittleEndian.PutUint32(buf[volumeDataOffset+8:volumeDataOffset+12], 1)
	binary.LittleEndian.PutUint32(buf[volumeDataOffset+12:volumeDataOffset+16], uint32(len(chunk)))
	binary.LittleEndian.PutUint64(buf[volumeDataOffset+16:volumeDataOffset+24], uint64(len(chunk)))

	sectorsDataOffset := sectorsOffset + descSize
	copy(buf[sectorsDataOffset:sectorsDataOffset+len(chunk)], chunk)

	tableDataOffset := tableOffset + descSize
	binary.LittleEndian.PutUint32(buf[tableDataOffset:tableDataOffset+4], 1)
	binary.LittleEndian.PutUint64(buf[tableDataOffset+8:tableDataOffset+16], uint64(sectorsDataOffset))
	binary.LittleEndian.PutUint32(buf[tableDataOffset+24:tableDataOffset+28], 0)

	return buf
}

func writeV1SectionDescriptor(buf []byte, offset int, typeString string, size uint64) {
	copy(buf[offset:offset+16], []byte(typeString))
	binary.LittleEndian.PutUint64(buf[offset+16:offset+24], uint64(offset)+size)
	binary.LittleEndian.PutUint64(buf[offset+24:offset+32], size)
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
