package reader

import (
	"encoding/binary"
	"hash/adler32"
	"io"
	"math"

	"github.com/aoiflux/libewf/internal/binaryutil"
	"github.com/aoiflux/libewf/types"
)

const (
	tableHeaderV1Size = 24 // 4(entries)+4(pad)+8(base_offset)+4(pad)+4(checksum)
	tableEntryV1Size  = 4
	tableHeaderV2Size = 32 // 8+4+4+4+12
	tableEntryV2Size  = 16 // 8+4+4

	// tableChecksumSize is the trailing Adler-32 that follows the entry array.
	tableChecksumSize = 4

	// maxTableEntries bounds allocation from a hostile or damaged table
	// header. 2^24 entries at 32 KiB per chunk describes a 512 GiB span in a
	// single table, well beyond anything a real writer emits.
	maxTableEntries = 1 << 24
)

// chunkDescriptor locates and describes one stored chunk within a segment file.
type chunkDescriptor struct {
	dataSource        io.ReaderAt
	dataOffset        int64
	dataSize          uint32
	compressed        bool
	majorVersion      uint8
	compressionMethod uint16

	// pattern marks an EWF2 chunk stored as a repeating fill rather than as
	// data: the stored bytes are one pattern unit to be repeated across the
	// whole chunk. Writers use it for long runs of identical bytes, so it is
	// common rather than exotic — an all-zero region produces nothing else.
	pattern bool

	// hasChecksum marks a chunk whose stored bytes end with a trailing
	// Adler-32 that is not device data. EWF2 states this per chunk; EWF v1
	// leaves it implied by the stored size.
	hasChecksum bool
}

// chunkTableStats records how the chunk table was recovered, for reporting.
type chunkTableStats struct {
	// TablesTotal is the number of chunk-table groups consumed.
	TablesTotal int
	// TablesRecovered counts groups where the primary "table" failed its
	// checksum and the "table2" backup copy was used instead.
	TablesRecovered int
	// TablesInvalid counts groups where neither copy validated and the
	// primary was used unverified.
	TablesInvalid int
}

// buildChunkTable returns chunk descriptors indexed by chunk number.
//
// EWF v1 writes each chunk group as a "sectors" section followed by a "table"
// section and a byte-identical "table2" backup copy. Both carry the same
// SectionTypeSectorTable code, so they must be paired and consumed as a single
// group; consuming both would duplicate every chunk and silently misalign the
// decoded device. The backup is used only when the primary fails its stored
// Adler-32 checksum, which is what it exists for.
//
// EWF v2 does not duplicate tables, so its sections are consumed one by one.
func buildChunkTable(source io.ReaderAt, header FileHeaderInfo, sections []SectionInfo) ([]chunkDescriptor, chunkTableStats) {
	var (
		table []chunkDescriptor
		stats chunkTableStats
	)

	// Collect sector-table sections so pairing is unaffected by any other
	// section types interleaved between a table and its backup.
	tables := make([]SectionInfo, 0, len(sections))
	for _, section := range sections {
		if section.Type == types.SectionTypeSectorTable {
			tables = append(tables, section)
		}
	}

	for i := 0; i < len(tables); i++ {
		primary := tables[i]

		if primary.DescriptorSize != sectionDescriptorV1Size {
			// EWF v2: no backup copies, each table stands alone.
			table = append(table, parseTableSectionV2(source, header, primary)...)
			stats.TablesTotal++
			continue
		}

		var backup *SectionInfo
		if primary.TypeString == "table" && i+1 < len(tables) && tables[i+1].TypeString == "table2" {
			backup = &tables[i+1]
			i++ // consumed as this group's backup, never as a group of its own
		}

		chunks, outcome := parseTableGroupV1(source, header, primary, backup)
		table = append(table, chunks...)
		stats.TablesTotal++
		switch outcome {
		case tableUsedBackup:
			stats.TablesRecovered++
		case tableUnverified:
			stats.TablesInvalid++
		}
	}
	return table, stats
}

type tableOutcome int

const (
	tableUsedPrimary tableOutcome = iota
	tableUsedBackup
	tableUnverified
)

// parseTableGroupV1 decodes one (table, table2) group, preferring whichever
// copy passes its stored checksum.
func parseTableGroupV1(source io.ReaderAt, header FileHeaderInfo, primary SectionInfo, backup *SectionInfo) ([]chunkDescriptor, tableOutcome) {
	primaryChunks, primaryValid := parseTableSectionV1(source, header, primary)
	if primaryValid && len(primaryChunks) > 0 {
		return primaryChunks, tableUsedPrimary
	}

	if backup != nil {
		backupChunks, backupValid := parseTableSectionV1(source, header, *backup)
		if backupValid && len(backupChunks) > 0 {
			return backupChunks, tableUsedBackup
		}
		// Neither validated; prefer whichever yielded entries at all.
		if len(primaryChunks) == 0 && len(backupChunks) > 0 {
			return backupChunks, tableUnverified
		}
	}

	if len(primaryChunks) == 0 {
		return nil, tableUnverified
	}
	return primaryChunks, tableUnverified
}

// parseTableSectionV1 reads a v1 "table" or "table2" section and returns chunk
// descriptors along with whether every stored checksum validated.
//
// In EWF-E01 the chunk data resides in the preceding "sectors" section; each
// entry stores a relative offset from base_offset. Bit 31 of each 32-bit entry
// is the compressed flag; bits 30:0 are the relative byte offset.
func parseTableSectionV1(source io.ReaderAt, header FileHeaderInfo, section SectionInfo) ([]chunkDescriptor, bool) {
	if section.DataSize < tableHeaderV1Size {
		return nil, false
	}
	hdrData, err := binaryutil.ReadSlice(source, section.DataOffset, tableHeaderV1Size)
	if err != nil {
		return nil, false
	}
	numberOfEntries := int(binary.LittleEndian.Uint32(hdrData[0:4]))
	baseOffset := binary.LittleEndian.Uint64(hdrData[8:16])

	// The header checksum covers the first 20 bytes.
	headerValid := adler32.Checksum(hdrData[0:20]) == binary.LittleEndian.Uint32(hdrData[20:24])

	if numberOfEntries <= 0 || numberOfEntries > maxTableEntries {
		return nil, false
	}
	// Refuse to allocate beyond what the section actually contains.
	if uint64(numberOfEntries)*tableEntryV1Size > section.DataSize-tableHeaderV1Size {
		return nil, false
	}

	entriesData, err := binaryutil.ReadSlice(source, section.DataOffset+tableHeaderV1Size, numberOfEntries*tableEntryV1Size)
	if err != nil {
		return nil, false
	}

	// An entries checksum trails the entry array, but only in layouts where
	// the table section holds nothing else. The EWF-S01 and legacy EnCase 1
	// dialects have no separate "sectors" section: the chunk data lives inside
	// the table section after the entries, so the bytes in that position are
	// chunk data and comparing them to a checksum would fail every time.
	//
	// Recognise the self-contained layout by an exact size match, and treat
	// anything else as "no checksum present" rather than "checksum invalid".
	// Skipping a check loses a little assurance; failing one wrongly would
	// route healthy images through the recovery path and flag their data as
	// suspect.
	entriesValid := true
	entriesEnd := section.DataOffset + tableHeaderV1Size + int64(numberOfEntries*tableEntryV1Size)
	exactSize := uint64(tableHeaderV1Size) + uint64(numberOfEntries)*tableEntryV1Size + tableChecksumSize
	if section.DataSize == exactSize {
		if stored, err := binaryutil.ReadSlice(source, entriesEnd, tableChecksumSize); err == nil {
			entriesValid = adler32.Checksum(entriesData) == binary.LittleEndian.Uint32(stored)
		}
	}

	rawOffsets := make([]uint32, numberOfEntries)
	for i := 0; i < numberOfEntries; i++ {
		rawOffsets[i] = binary.LittleEndian.Uint32(entriesData[i*tableEntryV1Size : i*tableEntryV1Size+4])
	}

	chunks := make([]chunkDescriptor, numberOfEntries)
	overflow := false

	for i := 0; i < numberOfEntries; i++ {
		var compressed bool
		var relOffset uint64

		if !overflow {
			compressed = (rawOffsets[i] >> 31) != 0
			relOffset = uint64(rawOffsets[i] & 0x7FFFFFFF)
		} else {
			relOffset = uint64(rawOffsets[i])
		}

		absOffset := int64(baseOffset + relOffset)
		chunks[i].dataSource = source
		chunks[i].dataOffset = absOffset
		chunks[i].compressed = compressed
		chunks[i].majorVersion = header.MajorVersion
		chunks[i].compressionMethod = header.CompressionMethod

		var dataSize uint32
		if i < numberOfEntries-1 {
			var nextRelOffset uint64
			if !overflow {
				nextRelOffset = uint64(rawOffsets[i+1] & 0x7FFFFFFF)
			} else {
				nextRelOffset = uint64(rawOffsets[i+1])
			}

			if nextRelOffset < relOffset {
				// EnCase 6.7 >2 GiB overflow: use raw next value minus current
				dataSize = uint32(uint64(rawOffsets[i+1]) - relOffset)
			} else {
				dataSize = uint32(nextRelOffset - relOffset)
			}

			// Detect overflow condition: absolute position crossed INT32_MAX
			if !overflow && (absOffset+int64(dataSize)) > int64(^uint32(0)>>1) {
				overflow = true
			}
		} else {
			dataSize = lastChunkSizeV1(section, absOffset)
		}
		chunks[i].dataSize = dataSize
	}
	return chunks, headerValid && entriesValid
}

// lastChunkSizeV1 derives the stored size of a group's final chunk, which has
// no following entry to subtract from. The chunk data lies in the preceding
// "sectors" section, so the table section's own start bounds it.
func lastChunkSizeV1(section SectionInfo, absOffset int64) uint32 {
	sectionStart := section.Offset
	sectionEnd := section.Offset
	if section.Size <= uint64(math.MaxInt64-section.Offset) {
		sectionEnd = section.Offset + int64(section.Size)
	}

	chunkDataEnd := int64(0)
	if section.TypeString == "table2" {
		// A table2 is preceded by its primary table, so the chunk data ends
		// where that primary begins.
		if section.Size <= uint64(sectionStart) {
			chunkDataEnd = sectionStart - int64(section.Size)
		}
		if absOffset >= chunkDataEnd && absOffset < sectionStart {
			chunkDataEnd = sectionStart
		}
	} else {
		if absOffset < sectionStart {
			chunkDataEnd = sectionStart
		} else if absOffset < sectionEnd {
			chunkDataEnd = sectionEnd
		}
	}

	if chunkDataEnd <= absOffset {
		return 0
	}
	lastSize := chunkDataEnd - absOffset
	if lastSize > int64(math.MaxUint32) {
		lastSize = int64(math.MaxUint32)
	}
	return uint32(lastSize)
}

// parseTableSectionV2 reads a v2 sector-table section. Each entry stores
// an absolute file offset, a size, and flags.
func parseTableSectionV2(source io.ReaderAt, header FileHeaderInfo, section SectionInfo) []chunkDescriptor {
	if section.DataSize < tableHeaderV2Size {
		return nil
	}
	hdrData, err := binaryutil.ReadSlice(source, section.DataOffset, tableHeaderV2Size)
	if err != nil {
		return nil
	}
	// first_chunk_number at [0:8] — informational only here
	numberOfEntries := int(binary.LittleEndian.Uint32(hdrData[8:12]))

	if numberOfEntries <= 0 || numberOfEntries > maxTableEntries {
		return nil
	}
	if uint64(numberOfEntries)*tableEntryV2Size > section.DataSize-tableHeaderV2Size {
		return nil
	}

	entriesData, err := binaryutil.ReadSlice(source, section.DataOffset+tableHeaderV2Size, numberOfEntries*tableEntryV2Size)
	if err != nil {
		return nil
	}

	chunks := make([]chunkDescriptor, numberOfEntries)
	for i := 0; i < numberOfEntries; i++ {
		off := i * tableEntryV2Size
		chunks[i].dataSource = source
		chunks[i].dataOffset = int64(binary.LittleEndian.Uint64(entriesData[off : off+8]))
		chunks[i].dataSize = binary.LittleEndian.Uint32(entriesData[off+8 : off+12])
		flags := binary.LittleEndian.Uint32(entriesData[off+12 : off+16])
		// A pattern-fill chunk also carries the compressed flag, so pattern
		// must be tested first: its stored bytes are a repeating unit, not a
		// compressed stream, and inflating them would fail.
		chunks[i].pattern = (flags & types.ChunkDataFlagPattern) != 0
		chunks[i].compressed = !chunks[i].pattern && (flags&types.ChunkDataFlagCompressed) != 0
		chunks[i].hasChecksum = (flags & types.ChunkDataFlagChecksum) != 0
		chunks[i].majorVersion = header.MajorVersion
		chunks[i].compressionMethod = header.CompressionMethod
	}
	return chunks
}
