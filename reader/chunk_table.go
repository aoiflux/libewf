package reader

import (
	"encoding/binary"
	"io"
	"math"

	"github.com/aoiflux/libewf/internal/binaryutil"
	"github.com/aoiflux/libewf/types"
)

const (
	tableHeaderV1Size = 24 // 4+4+8+4+4
	tableEntryV1Size  = 4
	tableHeaderV2Size = 32 // 8+4+4+4+12
	tableEntryV2Size  = 16 // 8+4+4
)

// chunkDescriptor locates and describes one stored chunk within a segment file.
type chunkDescriptor struct {
	dataSource        io.ReaderAt
	dataOffset        int64
	dataSize          uint32
	compressed        bool
	majorVersion      uint8
	compressionMethod uint16
}

// buildChunkTable scans all sector-table sections and returns an ordered slice of
// chunk descriptors indexed by chunk number.
func buildChunkTable(source io.ReaderAt, header FileHeaderInfo, sections []SectionInfo) []chunkDescriptor {
	var table []chunkDescriptor
	for _, section := range sections {
		if section.Type != types.SectionTypeSectorTable {
			continue
		}
		var chunks []chunkDescriptor
		if section.DescriptorSize == sectionDescriptorV1Size {
			chunks = parseTableSectionV1(source, header, section)
		} else {
			chunks = parseTableSectionV2(source, header, section)
		}
		table = append(table, chunks...)
	}
	return table
}

// parseTableSectionV1 reads a v1 "table" or "table2" section and returns
// chunk descriptors. In EWF-E01 the chunk data resides in the preceding
// "sectors" section; each entry stores a relative offset from base_offset.
// Bit 31 of each 32-bit entry is the compressed flag; bits 30:0 are the
// relative byte offset from base_offset.
func parseTableSectionV1(source io.ReaderAt, header FileHeaderInfo, section SectionInfo) []chunkDescriptor {
	if section.DataSize < tableHeaderV1Size {
		return nil
	}
	hdrData, err := binaryutil.ReadSlice(source, section.DataOffset, tableHeaderV1Size)
	if err != nil {
		return nil
	}
	numberOfEntries := int(binary.LittleEndian.Uint32(hdrData[0:4]))
	baseOffset := binary.LittleEndian.Uint64(hdrData[8:16])

	if numberOfEntries <= 0 || numberOfEntries > (1<<24) {
		return nil
	}

	entriesData, err := binaryutil.ReadSlice(source, section.DataOffset+tableHeaderV1Size, numberOfEntries*tableEntryV1Size)
	if err != nil {
		return nil
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
			sectionStart := section.Offset
			sectionEnd := section.Offset
			if section.Size <= uint64(math.MaxInt64-section.Offset) {
				sectionEnd = section.Offset + int64(section.Size)
			}

			chunkDataEnd := int64(0)
			if section.TypeString == "table2" {
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

			if chunkDataEnd > absOffset {
				lastSize := chunkDataEnd - absOffset
				if lastSize > int64(math.MaxUint32) {
					lastSize = int64(math.MaxUint32)
				}
				dataSize = uint32(lastSize)
			}
		}
		chunks[i].dataSize = dataSize
	}
	return chunks
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

	if numberOfEntries <= 0 || numberOfEntries > (1<<24) {
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
		chunks[i].compressed = (flags & types.ChunkDataFlagCompressed) != 0
		chunks[i].majorVersion = header.MajorVersion
		chunks[i].compressionMethod = header.CompressionMethod
	}
	return chunks
}
