package reader

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/aoiflux/libewf/ewferr"
	"github.com/aoiflux/libewf/types"
)

// ---------------------------------------------------------------------------
// Synthetic EWF2 (.Ex01) builder
//
// EWF2 inverts the v1 layout: a 64-byte section descriptor sits at the END of
// its section data, and the chain is walked backwards from the last descriptor
// via previous_offset. Geometry lives in two zlib-compressed UTF-16 text
// sections rather than one fixed-layout volume struct.
// ---------------------------------------------------------------------------

const v2DescSize = 64

type v2Section struct {
	typ     uint32
	flags   uint32
	payload []byte
	padding uint32
}

func buildV2Image(compressionMethod uint16, sections []v2Section) []byte {
	var buf bytes.Buffer

	// File header: signature, major, minor, compression method, segment number.
	header := make([]byte, 32)
	copy(header[0:8], types.SignatureEVFv2[:])
	header[8] = 2
	header[9] = 1
	binary.LittleEndian.PutUint16(header[10:12], compressionMethod)
	binary.LittleEndian.PutUint32(header[12:16], 1)
	buf.Write(header)

	previousDescriptor := uint64(0)
	for _, s := range sections {
		buf.Write(s.payload)

		descriptorOffset := uint64(buf.Len())
		d := make([]byte, v2DescSize)
		binary.LittleEndian.PutUint32(d[0:4], s.typ)
		binary.LittleEndian.PutUint32(d[4:8], s.flags)
		binary.LittleEndian.PutUint64(d[8:16], previousDescriptor)
		binary.LittleEndian.PutUint64(d[16:24], uint64(len(s.payload)))
		binary.LittleEndian.PutUint32(d[24:28], v2DescSize)
		binary.LittleEndian.PutUint32(d[28:32], s.padding)
		buf.Write(d)

		previousDescriptor = descriptorOffset
	}
	return buf.Bytes()
}

// v2TextSection encodes the block format device_information and case_data use:
// zlib-compressed UTF-16LE, identifiers and values tab-delimited.
func v2TextSection(identifiers, values []string) []byte {
	text := strings.Join([]string{
		"1",
		"main",
		strings.Join(identifiers, "\t"),
		strings.Join(values, "\t"),
		"",
		"",
	}, "\n")
	return zlibBytes(utf16LEWithBOM(text))
}

// v2Table builds a sector_table section for chunks already laid out in the
// sector_data section.
func v2Table(offsets []uint64, sizes []uint32, flags []uint32) []byte {
	out := make([]byte, tableHeaderV2Size)
	binary.LittleEndian.PutUint64(out[0:8], 0) // first chunk number
	binary.LittleEndian.PutUint32(out[8:12], uint32(len(offsets)))

	for i := range offsets {
		entry := make([]byte, tableEntryV2Size)
		binary.LittleEndian.PutUint64(entry[0:8], offsets[i])
		binary.LittleEndian.PutUint32(entry[8:12], sizes[i])
		binary.LittleEndian.PutUint32(entry[12:16], flags[i])
		out = append(out, entry...)
	}
	return out
}

func deflateChunk(payload []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(payload)
	_ = w.Close()
	return buf.Bytes()
}

// ewf2Image assembles a complete single-segment EWF2 image around a set of
// stored chunks. sectorsPerChunk and bytesPerSector give a chunk size; the
// chunk offsets are resolved once the sector_data section's position is known.
type ewf2Chunk struct {
	stored []byte
	flags  uint32
}

func buildEx01(t testing.TB, sectorsPerChunk, bytesPerSector uint32, numberOfSectors uint64, chunks []ewf2Chunk, extra ...v2Section) []byte {
	t.Helper()

	device := v2TextSection(
		[]string{"sn", "md", "lb", "ts", "dt", "bp", "ph"},
		[]string{"SN123", "MODEL-X", "LABEL-Y", itoa(numberOfSectors), "f", itoa(uint64(bytesPerSector)), "1"},
	)
	caseData := v2TextSection(
		[]string{"nm", "cn", "en", "ex", "nt", "av", "os", "at", "tt", "tb", "sb", "gr"},
		[]string{"descX", "caseX", "evX", "examX", "notesX", "20240506", "Linux",
			"1785082957", "1785082957", itoa(uint64(len(chunks))), itoa(uint64(sectorsPerChunk)), "64"},
	)

	var sectorData []byte
	for _, c := range chunks {
		sectorData = append(sectorData, c.stored...)
	}

	// The sector_data payload begins right after the case_data descriptor:
	// 32 (file header) + each preceding section's payload and descriptor.
	dataStart := uint64(32 + len(device) + v2DescSize + len(caseData) + v2DescSize)

	offsets := make([]uint64, len(chunks))
	sizes := make([]uint32, len(chunks))
	flags := make([]uint32, len(chunks))
	at := dataStart
	for i, c := range chunks {
		offsets[i] = at
		sizes[i] = uint32(len(c.stored))
		flags[i] = c.flags
		at += uint64(len(c.stored))
	}

	sections := []v2Section{
		{typ: types.SectionTypeDeviceInformation, payload: device},
		{typ: types.SectionTypeCaseData, payload: caseData},
		{typ: types.SectionTypeSectorData, payload: sectorData},
		{typ: types.SectionTypeSectorTable, payload: v2Table(offsets, sizes, flags)},
	}
	sections = append(sections, extra...)
	sections = append(sections, v2Section{typ: types.SectionTypeDone})

	return buildV2Image(types.CompressionMethodDeflate, sections)
}

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestEWF2GeometryAndAcquisition covers the two text sections that replaced the
// v1 volume struct. Geometry is split across them, so a reader that decodes only
// one reports no usable size.
func TestEWF2GeometryAndAcquisition(t *testing.T) {
	const chunkSize = 4 * 512
	payload := bytes.Repeat([]byte("ABCD"), chunkSize/4)

	image := buildEx01(t, 4, 512, 8, []ewf2Chunk{
		{stored: payload, flags: 0},
		{stored: payload, flags: 0},
	})

	r := mustOpen(t, image)

	if got, want := r.Size(), int64(8*512); got != want {
		t.Errorf("Size() = %d, want %d", got, want)
	}
	if got, want := r.SectorSize(), 512; got != want {
		t.Errorf("SectorSize() = %d, want %d", got, want)
	}

	m := r.Metadata()
	if m.Media == nil {
		t.Fatal("Media is nil, want geometry from device_information and case_data")
	}
	if got, want := m.Media.SectorsPerChunk, uint32(4); got != want {
		t.Errorf("SectorsPerChunk = %d, want %d (from case_data \"sb\")", got, want)
	}
	if got, want := m.Media.NumberOfSectors, uint64(8); got != want {
		t.Errorf("NumberOfSectors = %d, want %d (from device_information \"ts\")", got, want)
	}
	if got, want := m.Media.NumberOfChunks, uint64(2); got != want {
		t.Errorf("NumberOfChunks = %d, want %d (from case_data \"tb\")", got, want)
	}
	if got, want := m.Media.ErrorGranularity, uint32(64); got != want {
		t.Errorf("ErrorGranularity = %d, want %d (from case_data \"gr\")", got, want)
	}
	if got, want := m.Media.MediaType, types.MediaTypeFixed; got != want {
		t.Errorf("MediaType = 0x%02x, want 0x%02x for \"f\"", got, want)
	}
	if m.Media.MediaFlags&types.MediaFlagPhysical == 0 {
		t.Error("MediaFlags is missing the physical bit set by \"ph\"")
	}

	a := m.Acquisition
	if a == nil {
		t.Fatal("Acquisition is nil, want provenance from case_data")
	}
	// "at" is the acquisition date and "tt" the system date; transposing them is
	// an easy mistake so both are pinned.
	if got, want := a.AcquiryDate.Unix(), int64(1785082957); got != want {
		t.Errorf("AcquiryDate.Unix() = %d, want %d", got, want)
	}
	for _, f := range []struct{ name, got, want string }{
		{"CaseNumber", a.CaseNumber, "caseX"},
		{"Description", a.Description, "descX"},
		{"EvidenceNumber", a.EvidenceNumber, "evX"},
		{"ExaminerName", a.ExaminerName, "examX"},
		{"Notes", a.Notes, "notesX"},
		{"Model", a.Model, "MODEL-X"},
		{"SerialNumber", a.SerialNumber, "SN123"},
		{"DeviceLabel", a.DeviceLabel, "LABEL-Y"},
	} {
		if f.got != f.want {
			t.Errorf("Acquisition.%s = %q, want %q", f.name, f.got, f.want)
		}
	}
}

// TestEWF2PatternChunk covers pattern-fill chunks, which store a single
// repeating unit instead of the chunk's bytes. They also carry the compressed
// flag, so a reader that checks compression first tries to inflate the pattern
// unit and fails.
func TestEWF2PatternChunk(t *testing.T) {
	const (
		sectorsPerChunk = 4
		bytesPerSector  = 512
		chunkSize       = sectorsPerChunk * bytesPerSector
	)
	unit := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02, 0x03, 0x04}

	image := buildEx01(t, sectorsPerChunk, bytesPerSector, sectorsPerChunk*2, []ewf2Chunk{
		{stored: unit, flags: types.ChunkDataFlagPattern | types.ChunkDataFlagCompressed},
		{stored: bytes.Repeat([]byte{0x5A}, chunkSize), flags: 0},
	})

	r := mustOpen(t, image)
	got := readAll(t, r)

	want := make([]byte, 0, chunkSize*2)
	for len(want) < chunkSize {
		want = append(want, unit...)
	}
	want = append(want, bytes.Repeat([]byte{0x5A}, chunkSize)...)

	if !bytes.Equal(got, want) {
		t.Fatalf("pattern chunk decoded incorrectly:\n got %x...\nwant %x...", got[:32], want[:32])
	}
}

// TestEWF2ChecksumFlagStripsTrailer covers an uncompressed EWF2 chunk, whose
// stored size is the chunk plus a trailing Adler-32 that the flag announces.
func TestEWF2ChecksumFlagStripsTrailer(t *testing.T) {
	const chunkSize = 4 * 512
	payload := bytes.Repeat([]byte("XY"), chunkSize/2)
	stored := append(append([]byte{}, payload...), 0xDE, 0xAD, 0xBE, 0xEF)

	image := buildEx01(t, 4, 512, 4, []ewf2Chunk{
		{stored: stored, flags: types.ChunkDataFlagChecksum},
	})

	r := mustOpen(t, image)
	if got, want := r.Size(), int64(chunkSize); got != want {
		t.Fatalf("Size() = %d, want %d", got, want)
	}
	if got := readAll(t, r); !bytes.Equal(got, payload) {
		t.Fatalf("checksum trailer leaked into device data: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestEWF2CompressedChunk(t *testing.T) {
	const chunkSize = 4 * 512
	payload := bytes.Repeat([]byte("compressible-"), chunkSize/13+1)[:chunkSize]

	image := buildEx01(t, 4, 512, 4, []ewf2Chunk{
		{stored: deflateChunk(payload), flags: types.ChunkDataFlagCompressed},
	})

	r := mustOpen(t, image)
	if got := readAll(t, r); !bytes.Equal(got, payload) {
		t.Fatalf("compressed EWF2 chunk decoded incorrectly (%d bytes, want %d)", len(got), len(payload))
	}
}

// TestEWF2DigestSectionsWithPadding is the regression test for the EWF2 digest
// sections. They are 32 bytes on disk but declare trailing padding, so the body
// reaches the parser shorter than the on-disk section: requiring the full 32
// bytes silently discarded every EWF2 digest.
func TestEWF2DigestSectionsWithPadding(t *testing.T) {
	const chunkSize = 4 * 512
	payload := bytes.Repeat([]byte{0x11}, chunkSize)

	md5Payload := make([]byte, 32)
	for i := 0; i < 16; i++ {
		md5Payload[i] = byte(0xA0 + i)
	}
	sha1Payload := make([]byte, 32)
	for i := 0; i < 20; i++ {
		sha1Payload[i] = byte(0xB0 + i)
	}

	image := buildEx01(t, 4, 512, 4, []ewf2Chunk{{stored: payload, flags: 0}},
		v2Section{typ: types.SectionTypeMD5Hash, payload: md5Payload, padding: 12},
		v2Section{typ: types.SectionTypeSHA1Hash, payload: sha1Payload, padding: 8},
	)

	m := mustOpen(t, image).Metadata()

	if !m.HasMD5Digest {
		t.Error("HasMD5Digest = false, want true: the md5_hash section carries a digest")
	} else if got := m.MD5Digest[0]; got != 0xA0 {
		t.Errorf("MD5Digest[0] = 0x%02x, want 0xA0", got)
	}
	if !m.HasSHA1Digest {
		t.Error("HasSHA1Digest = false, want true: the sha1_hash section carries a digest")
	} else if got := m.SHA1Digest[0]; got != 0xB0 {
		t.Errorf("SHA1Digest[0] = 0x%02x, want 0xB0", got)
	}
}

func TestExpandPattern(t *testing.T) {
	for _, tc := range []struct {
		name      string
		unit      []byte
		chunkSize int64
		want      []byte
		wantErr   bool
	}{
		{"exact multiple", []byte{1, 2}, 6, []byte{1, 2, 1, 2, 1, 2}, false},
		{"partial final repeat", []byte{1, 2, 3}, 7, []byte{1, 2, 3, 1, 2, 3, 1}, false},
		{"unit longer than chunk", []byte{1, 2, 3, 4}, 2, []byte{1, 2}, false},
		{"single byte", []byte{0xFF}, 3, []byte{0xFF, 0xFF, 0xFF}, false},
		{"empty unit", nil, 4, nil, true},
		{"unknown chunk size", []byte{1}, 0, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandPattern(tc.unit, tc.chunkSize)
			if tc.wantErr {
				if !errors.Is(err, ewferr.ErrCorruptImage) {
					t.Fatalf("expandPattern() error = %v, want ErrCorruptImage", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expandPattern() error = %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("expandPattern() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEWF2MissingCaseDataHasNoUsableSize checks that geometry split across two
// sections fails closed: without case_data there is no sectors-per-chunk, so
// there is no way to locate a byte and ReadAt must refuse rather than guess.
func TestEWF2MissingCaseDataHasNoUsableSize(t *testing.T) {
	device := v2TextSection(
		[]string{"ts", "dt", "bp", "ph"},
		[]string{"8", "f", "512", "1"},
	)
	image := buildV2Image(types.CompressionMethodDeflate, []v2Section{
		{typ: types.SectionTypeDeviceInformation, payload: device},
		{typ: types.SectionTypeDone},
	})

	r, err := OpenWithOptions(bytes.NewReader(image), Options{})
	if err != nil {
		// Failing at open is an acceptable outcome too.
		if !errors.Is(err, ewferr.ErrCorruptImage) {
			t.Fatalf("Open() error = %v, want ErrCorruptImage", err)
		}
		return
	}
	if _, err := r.ReadAt(make([]byte, 16), 0); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt() error = %v, want a corruption error with no sectors-per-chunk", err)
	}
}
