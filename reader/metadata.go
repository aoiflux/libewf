package reader

import (
	"github.com/aoiflux/libewf/metadata"
	"github.com/aoiflux/libewf/types"
)

func buildMetadata(header FileHeaderInfo, sections []SectionInfo) metadata.Info {
	info := metadata.Info{
		MajorVersion:      header.MajorVersion,
		MinorVersion:      header.MinorVersion,
		CompressionMethod: header.CompressionMethod,
		SegmentNumber:     header.SegmentNumber,
		SectionCount:      len(sections),
		SectionTypeCounts: make(map[uint32]int),
		Sections:          make([]metadata.Section, 0, len(sections)),
	}

	for _, section := range sections {
		applySectionSummary(&info, section)
	}
	return info
}

func applySectionSummary(info *metadata.Info, section SectionInfo) {
	info.Sections = append(info.Sections, metadata.Section{
		Offset:     section.Offset,
		Type:       section.Type,
		TypeName:   section.TypeString,
		DataFlags:  section.DataFlags,
		Size:       section.Size,
		DataOffset: section.DataOffset,
		DataSize:   section.DataSize,
		Padding:    section.PaddingSize,
		Descriptor: section.DescriptorSize,
	})
	info.SectionTypeCounts[section.Type]++

	switch section.Type {
	case types.SectionTypeNext:
		info.HasNextSection = true
	case types.SectionTypeDone:
		info.HasDoneSection = true
	case types.SectionTypeEncryptionKeys:
		info.IsEncrypted = true
	}
	if (section.DataFlags & types.SectionDataFlagHasIntegrityHash) != 0 {
		info.HasIntegrityHashBlocks = true
	}
}
