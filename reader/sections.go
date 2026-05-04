package reader

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/aoiflux/libewf/internal/binaryutil"
	"github.com/aoiflux/libewf/types"
)

const (
	sectionDescriptorV1Size = 76
	sectionDescriptorV2Size = 64
)

// SectionInfo represents a parsed on-disk section descriptor.
type SectionInfo struct {
	Offset         int64
	DescriptorSize uint32
	Type           uint32
	TypeString     string
	DataFlags      uint32
	Size           uint64
	DataOffset     int64
	DataSize       uint64
	PaddingSize    uint32
}

func parseSectionDescriptors(source io.ReaderAt, majorVersion uint8) ([]SectionInfo, error) {
	switch majorVersion {
	case 1:
		return parseSectionsV1(source)
	case 2:
		return parseSectionsV2(source)
	default:
		return nil, fmt.Errorf("reader: unsupported major version for sections: %d", majorVersion)
	}
}

func parseSectionsV1(source io.ReaderAt) ([]SectionInfo, error) {
	offset := int64(13)
	sections := make([]SectionInfo, 0, 16)
	lastSectionFound := false

	for i := 0; i < 1<<16; i++ {
		descriptor, err := readSectionDescriptorV1(source, offset)
		if err != nil {
			if len(sections) == 0 {
				return nil, fmt.Errorf("reader: unable to read first v1 section descriptor: %w", err)
			}
			return nil, fmt.Errorf("reader: unable to read v1 section descriptor at offset %d: %w", offset, err)
		}
		sections = append(sections, descriptor)

		offset += int64(descriptor.Size)

		if descriptor.Type == types.SectionTypeNext || descriptor.Type == types.SectionTypeDone {
			lastSectionFound = true
			if descriptor.Type == types.SectionTypeDone {
				// Done section marks final segment in v1 sets; exposed via Type.
			}
			if descriptor.Size == 0 {
				offset += sectionDescriptorV1Size
			}
			break
		}
	}

	if !lastSectionFound {
		return nil, fmt.Errorf("reader: missing next or done section in v1 descriptor chain")
	}
	return sections, nil
}

func parseSectionsV2(source io.ReaderAt) ([]SectionInfo, error) {
	fileSize, err := sourceSize(source)
	if err != nil {
		return nil, fmt.Errorf("reader: unable to determine v2 segment size: %w", err)
	}
	if fileSize < sectionDescriptorV2Size {
		return nil, fmt.Errorf("reader: v2 segment file too small: %d", fileSize)
	}

	offset := fileSize - sectionDescriptorV2Size
	sections := make([]SectionInfo, 0, 16)
	lastSectionFound := false

	for i := 0; i < 1<<16 && offset > 0 && offset < fileSize; i++ {
		descriptor, err := readSectionDescriptorV2(source, offset)
		if err != nil {
			return nil, fmt.Errorf("reader: unable to read v2 section descriptor at offset %d: %w", offset, err)
		}
		sections = append(sections, descriptor)

		if len(sections) == 1 {
			if descriptor.Type == types.SectionTypeNext || descriptor.Type == types.SectionTypeDone {
				lastSectionFound = true
			}
		}

		offset -= int64(descriptor.Size)
	}

	if !lastSectionFound {
		return nil, fmt.Errorf("reader: missing next or done section in v2 descriptor chain")
	}
	reverseSections(sections)
	return sections, nil
}

func readSectionDescriptorV1(source io.ReaderAt, offset int64) (SectionInfo, error) {
	data, err := binaryutil.ReadSlice(source, offset, sectionDescriptorV1Size)
	if err != nil {
		return SectionInfo{}, err
	}

	var raw types.SectionDescriptorV1
	copy(raw.TypeString[:], data[0:16])
	copy(raw.NextOffset[:], data[16:24])
	copy(raw.Size[:], data[24:32])
	copy(raw.Padding[:], data[32:72])
	copy(raw.Checksum[:], data[72:76])

	nextOffset := raw.NextOffsetValue(binary.LittleEndian)
	sectionSize := raw.SizeValue(binary.LittleEndian)
	if sectionSize == 0 && nextOffset > uint64(offset) {
		sectionSize = nextOffset - uint64(offset)
	}

	dataSize := uint64(0)
	if sectionSize >= sectionDescriptorV1Size {
		dataSize = sectionSize - sectionDescriptorV1Size
	}

	typeString := parseTypeString(raw.TypeString[:])
	return SectionInfo{
		Offset:         offset,
		DescriptorSize: sectionDescriptorV1Size,
		Type:           mapV1TypeString(typeString),
		TypeString:     typeString,
		DataFlags:      0,
		Size:           sectionSize,
		DataOffset:     offset + sectionDescriptorV1Size,
		DataSize:       dataSize,
		PaddingSize:    0,
	}, nil
}

func readSectionDescriptorV2(source io.ReaderAt, offset int64) (SectionInfo, error) {
	data, err := binaryutil.ReadSlice(source, offset, sectionDescriptorV2Size)
	if err != nil {
		return SectionInfo{}, err
	}

	var raw types.SectionDescriptorV2
	copy(raw.Type[:], data[0:4])
	copy(raw.DataFlags[:], data[4:8])
	copy(raw.PreviousOffset[:], data[8:16])
	copy(raw.DataSize[:], data[16:24])
	copy(raw.DescriptorSize[:], data[24:28])
	copy(raw.PaddingSize[:], data[28:32])
	copy(raw.DataIntegrityHash[:], data[32:48])
	copy(raw.Padding[:], data[48:60])
	copy(raw.Checksum[:], data[60:64])

	previousOffset := raw.PreviousOffsetValue(binary.LittleEndian)
	sectionSize := uint64(0)
	dataOffset := int64(0)

	if previousOffset == 0 {
		dataOffset = 32
		sectionSize = uint64(offset) + 32
	} else {
		if previousOffset > uint64(offset) {
			return SectionInfo{}, fmt.Errorf("reader: invalid previous offset %d for descriptor at %d", previousOffset, offset)
		}
		dataOffset = int64(previousOffset) + sectionDescriptorV2Size
		sectionSize = uint64(offset) - previousOffset
	}

	typ := raw.TypeValue(binary.LittleEndian)
	return SectionInfo{
		Offset:         offset,
		DescriptorSize: sectionDescriptorV2Size,
		Type:           typ,
		TypeString:     mapSectionTypeToString(typ),
		DataFlags:      raw.DataFlagsValue(binary.LittleEndian),
		Size:           sectionSize,
		DataOffset:     dataOffset,
		DataSize:       raw.DataSizeValue(binary.LittleEndian),
		PaddingSize:    raw.PaddingSizeValue(binary.LittleEndian),
	}, nil
}

func parseTypeString(raw []byte) string {
	n := bytes.IndexByte(raw, 0)
	if n == -1 {
		n = len(raw)
	}
	return strings.TrimSpace(string(raw[:n]))
}

func mapV1TypeString(typeString string) uint32 {
	switch typeString {
	case "next":
		return types.SectionTypeNext
	case "done":
		return types.SectionTypeDone
	case "table", "table2":
		return types.SectionTypeSectorTable
	case "sectors":
		return types.SectionTypeSectorData
	case "error", "error2":
		return types.SectionTypeErrorTable
	case "session":
		return types.SectionTypeSessionTable
	case "digest":
		return types.SectionTypeMD5Hash
	case "xhash", "hash":
		return types.SectionTypeSHA1Hash
	case "disk", "volume", "data":
		return types.SectionTypeDeviceInformation
	default:
		return 0
	}
}

func mapSectionTypeToString(sectionType uint32) string {
	switch sectionType {
	case types.SectionTypeDeviceInformation:
		return "device_information"
	case types.SectionTypeCaseData:
		return "case_data"
	case types.SectionTypeSectorData:
		return "sector_data"
	case types.SectionTypeSectorTable:
		return "sector_table"
	case types.SectionTypeErrorTable:
		return "error_table"
	case types.SectionTypeSessionTable:
		return "session_table"
	case types.SectionTypeIncrementData:
		return "increment_data"
	case types.SectionTypeMD5Hash:
		return "md5_hash"
	case types.SectionTypeSHA1Hash:
		return "sha1_hash"
	case types.SectionTypeRestartData:
		return "restart_data"
	case types.SectionTypeEncryptionKeys:
		return "encryption_keys"
	case types.SectionTypeMemoryExtents:
		return "memory_extents"
	case types.SectionTypeNext:
		return "next"
	case types.SectionTypeFinalInformation:
		return "final_information"
	case types.SectionTypeDone:
		return "done"
	case types.SectionTypeAnalyticalData:
		return "analytical_data"
	case types.SectionTypeSingleFilesData:
		return "single_files_data"
	default:
		return "unknown"
	}
}

func reverseSections(sections []SectionInfo) {
	for i, j := 0, len(sections)-1; i < j; i, j = i+1, j-1 {
		sections[i], sections[j] = sections[j], sections[i]
	}
}

func sourceSize(source io.ReaderAt) (int64, error) {
	switch s := source.(type) {
	case interface{ Size() int64 }:
		return s.Size(), nil
	case interface{ Len() int }:
		return int64(s.Len()), nil
	case interface{ Stat() (fs.FileInfo, error) }:
		info, err := s.Stat()
		if err != nil {
			return 0, err
		}
		return info.Size(), nil
	default:
		return 0, fmt.Errorf("unsupported ReaderAt type %T (needs Size(), Len(), or Stat())", source)
	}
}
