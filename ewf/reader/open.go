package reader

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"

	"github.com/aoiflux/libewf/ewf/compression"
	"github.com/aoiflux/libewf/ewf/internal/binaryutil"
	"github.com/aoiflux/libewf/ewf/metadata"
	"github.com/aoiflux/libewf/ewf/types"
)

// SegmentFileType identifies the segment file family.
type SegmentFileType uint8

const (
	SegmentFileTypeUnknown SegmentFileType = iota
	SegmentFileTypeEWF1
	SegmentFileTypeEWF1Logical
	SegmentFileTypeEWF2
	SegmentFileTypeEWF2Logical
)

// ImageReader is the initial reader implementation backing libewf.Open.
type ImageReader struct {
	source          io.ReaderAt
	segmentFileType SegmentFileType
	header          FileHeaderInfo
	sections        []SectionInfo
	metadata        metadata.Info
	chunkTable      []chunkDescriptor
}

// FileHeaderInfo contains parsed file-header fields needed for subsequent parsing.
type FileHeaderInfo struct {
	Signature         [8]uint8
	MajorVersion      uint8
	MinorVersion      uint8
	CompressionMethod uint16
	SegmentNumber     uint32
}

type parsedSegment struct {
	source          io.ReaderAt
	segmentFileType SegmentFileType
	header          FileHeaderInfo
	sections        []SectionInfo
	metadata        metadata.Info
	chunkTable      []chunkDescriptor
}

// Open inspects the segment file signature and returns a reader instance.
func Open(source io.ReaderAt) (*ImageReader, error) {
	return OpenSegments([]io.ReaderAt{source})
}

// OpenSegments prepares a single logical reader from one or more EWF segments.
func OpenSegments(sources []io.ReaderAt) (*ImageReader, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("reader: no segment sources provided")
	}

	segments := make([]parsedSegment, 0, len(sources))
	for i, source := range sources {
		segment, err := parseSegment(source)
		if err != nil {
			return nil, fmt.Errorf("reader: unable to parse segment %d: %w", i, err)
		}
		segments = append(segments, segment)
	}

	if err := validateSegmentSet(segments); err != nil {
		return nil, err
	}

	sort.Slice(segments, func(i, j int) bool {
		return segments[i].header.SegmentNumber < segments[j].header.SegmentNumber
	})

	mergedMeta := mergeSegmentMetadata(segments)
	mergedChunks := make([]chunkDescriptor, 0)
	for _, segment := range segments {
		mergedChunks = append(mergedChunks, segment.chunkTable...)
	}

	return &ImageReader{
		source:          segments[0].source,
		segmentFileType: segments[0].segmentFileType,
		header:          segments[0].header,
		sections:        segments[0].sections,
		metadata:        mergedMeta,
		chunkTable:      mergedChunks,
	}, nil
}

func parseSegment(source io.ReaderAt) (parsedSegment, error) {
	signature, err := binaryutil.ReadSlice(source, 0, 8)
	if err != nil {
		return parsedSegment{}, fmt.Errorf("reader: unable to read file signature: %w", err)
	}

	segmentFileType, err := detectSegmentFileType(signature)
	if err != nil {
		return parsedSegment{}, err
	}

	header, err := parseFileHeader(source, segmentFileType)
	if err != nil {
		return parsedSegment{}, err
	}

	sections, err := parseSectionDescriptors(source, header.MajorVersion)
	if err != nil {
		return parsedSegment{}, err
	}

	meta := buildMetadata(header, sections)
	populateMetadataFromSectionBodies(source, header, sections, &meta)
	chunkTable := buildChunkTable(source, header, sections)

	return parsedSegment{
		source:          source,
		segmentFileType: segmentFileType,
		header:          header,
		sections:        sections,
		metadata:        meta,
		chunkTable:      chunkTable,
	}, nil
}

// ReadAt reads image bytes at the given byte offset, spanning chunk boundaries
// as needed. Compressed chunks are transparently decompressed.
func (r *ImageReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.chunkTable) == 0 {
		return 0, fmt.Errorf("reader: no chunk table available")
	}
	if r.metadata.Media == nil {
		return 0, fmt.Errorf("reader: no media geometry available")
	}
	media := r.metadata.Media
	chunkSize := int64(media.SectorsPerChunk) * int64(media.BytesPerSector)
	if chunkSize <= 0 {
		return 0, fmt.Errorf("reader: invalid chunk size (sectors_per_chunk=%d bytes_per_sector=%d)",
			media.SectorsPerChunk, media.BytesPerSector)
	}

	totalRead := 0
	for totalRead < len(p) {
		currentOff := off + int64(totalRead)
		chunkNum := int(currentOff / chunkSize)
		chunkOff := int(currentOff % chunkSize)

		if chunkNum >= len(r.chunkTable) {
			return totalRead, io.EOF
		}

		desc := r.chunkTable[chunkNum]
		readSource := desc.dataSource
		if readSource == nil {
			readSource = r.source
		}
		rawData, err := binaryutil.ReadSlice(readSource, desc.dataOffset, int(desc.dataSize))
		if err != nil {
			return totalRead, fmt.Errorf("reader: unable to read chunk %d at offset %d: %w", chunkNum, desc.dataOffset, err)
		}

		var chunkData []byte
		if desc.compressed {
			// EWF v1 always uses deflate; v2 uses the segment header compression method.
			method := desc.compressionMethod
			if desc.majorVersion == 1 {
				method = types.CompressionMethodDeflate
			}
			chunkData, err = compression.Decompress(rawData, method)
			if err != nil {
				return totalRead, fmt.Errorf("reader: unable to decompress chunk %d: %w", chunkNum, err)
			}
		} else {
			chunkData = rawData
		}

		available := len(chunkData) - chunkOff
		chunkRemaining := int(chunkSize) - chunkOff
		if chunkRemaining < available {
			available = chunkRemaining
		}
		if available <= 0 {
			return totalRead, io.EOF
		}
		toCopy := len(p) - totalRead
		if toCopy > available {
			toCopy = available
		}
		copy(p[totalRead:], chunkData[chunkOff:chunkOff+toCopy])
		totalRead += toCopy
	}
	return totalRead, nil
}

func validateSegmentSet(segments []parsedSegment) error {
	if len(segments) <= 1 {
		return nil
	}
	first := segments[0]
	seen := make(map[uint32]struct{}, len(segments))
	for i, segment := range segments {
		if segment.header.MajorVersion != first.header.MajorVersion {
			return fmt.Errorf("reader: segment %d major version %d does not match segment 0 major version %d", i, segment.header.MajorVersion, first.header.MajorVersion)
		}
		if segment.segmentFileType != first.segmentFileType {
			return fmt.Errorf("reader: segment %d type %d does not match segment 0 type %d", i, segment.segmentFileType, first.segmentFileType)
		}
		if _, ok := seen[segment.header.SegmentNumber]; ok {
			return fmt.Errorf("reader: duplicate segment number %d", segment.header.SegmentNumber)
		}
		seen[segment.header.SegmentNumber] = struct{}{}
	}
	return nil
}

func mergeSegmentMetadata(segments []parsedSegment) metadata.Info {
	merged := segments[0].metadata
	for i := 1; i < len(segments); i++ {
		meta := segments[i].metadata
		merged.SectionCount += meta.SectionCount
		if merged.SectionTypeCounts == nil {
			merged.SectionTypeCounts = make(map[uint32]int)
		}
		for key, count := range meta.SectionTypeCounts {
			merged.SectionTypeCounts[key] += count
		}
		merged.HasNextSection = merged.HasNextSection || meta.HasNextSection
		merged.HasDoneSection = merged.HasDoneSection || meta.HasDoneSection
		merged.IsEncrypted = merged.IsEncrypted || meta.IsEncrypted
		merged.HasIntegrityHashBlocks = merged.HasIntegrityHashBlocks || meta.HasIntegrityHashBlocks
		merged.Sessions = append(merged.Sessions, meta.Sessions...)
		merged.AcquisitionErrors = append(merged.AcquisitionErrors, meta.AcquisitionErrors...)
		if !merged.HasMD5Digest && meta.HasMD5Digest {
			merged.HasMD5Digest = true
			merged.MD5Digest = meta.MD5Digest
		}
		if !merged.HasSHA1Digest && meta.HasSHA1Digest {
			merged.HasSHA1Digest = true
			merged.SHA1Digest = meta.SHA1Digest
		}
		merged.Sections = append(merged.Sections, meta.Sections...)
	}
	if merged.Media != nil && len(segments) > 0 {
		totalChunks := len(collectAllChunks(segments))
		if totalChunks > 0 {
			merged.Media.NumberOfChunks = uint64(totalChunks)
		}
	}
	return merged
}

func collectAllChunks(segments []parsedSegment) []chunkDescriptor {
	table := make([]chunkDescriptor, 0)
	for _, segment := range segments {
		table = append(table, segment.chunkTable...)
	}
	return table
}

// Close releases resources held by the reader.
func (r *ImageReader) Close() error {
	return nil
}

// SegmentFileType returns the detected segment file type.
func (r *ImageReader) SegmentFileType() SegmentFileType {
	return r.segmentFileType
}

// Header returns parsed file-header information.
func (r *ImageReader) Header() FileHeaderInfo {
	return r.header
}

// Sections returns parsed section descriptors in logical order.
func (r *ImageReader) Sections() []SectionInfo {
	out := make([]SectionInfo, len(r.sections))
	copy(out, r.sections)
	return out
}

// Metadata returns the parsed descriptor-level metadata summary.
func (r *ImageReader) Metadata() metadata.Info {
	out := r.metadata
	out.Sections = make([]metadata.Section, len(r.metadata.Sections))
	copy(out.Sections, r.metadata.Sections)
	out.SectionTypeCounts = make(map[uint32]int, len(r.metadata.SectionTypeCounts))
	for key, value := range r.metadata.SectionTypeCounts {
		out.SectionTypeCounts[key] = value
	}
	if r.metadata.Media != nil {
		mediaCopy := *r.metadata.Media
		out.Media = &mediaCopy
	}
	return out
}

func detectSegmentFileType(signature []byte) (SegmentFileType, error) {
	switch {
	case bytes.Equal(signature, types.SignatureEVFv1[:]):
		return SegmentFileTypeEWF1, nil
	case bytes.Equal(signature, types.SignatureLVFv1[:]):
		return SegmentFileTypeEWF1Logical, nil
	case bytes.Equal(signature, types.SignatureEVFv2[:]):
		return SegmentFileTypeEWF2, nil
	case bytes.Equal(signature, types.SignatureLEFv2[:]):
		return SegmentFileTypeEWF2Logical, nil
	default:
		return SegmentFileTypeUnknown, fmt.Errorf("reader: unsupported segment signature: %x", signature)
	}
}

func parseFileHeader(source io.ReaderAt, segmentFileType SegmentFileType) (FileHeaderInfo, error) {
	switch segmentFileType {
	case SegmentFileTypeEWF1, SegmentFileTypeEWF1Logical:
		return parseFileHeaderV1(source)
	case SegmentFileTypeEWF2, SegmentFileTypeEWF2Logical:
		return parseFileHeaderV2(source)
	default:
		return FileHeaderInfo{}, fmt.Errorf("reader: cannot parse header for segment type: %d", segmentFileType)
	}
}

func parseFileHeaderV1(source io.ReaderAt) (FileHeaderInfo, error) {
	data, err := binaryutil.ReadSlice(source, 0, 13)
	if err != nil {
		return FileHeaderInfo{}, fmt.Errorf("reader: unable to read v1 file header: %w", err)
	}

	if data[8] != 0x01 {
		return FileHeaderInfo{}, fmt.Errorf("reader: invalid v1 header fields_start: 0x%02x", data[8])
	}
	if data[11] != 0x00 || data[12] != 0x00 {
		return FileHeaderInfo{}, fmt.Errorf("reader: invalid v1 header fields_end: %02x%02x", data[11], data[12])
	}

	info := FileHeaderInfo{}
	copy(info.Signature[:], data[0:8])
	info.MajorVersion = 1
	info.MinorVersion = 0
	info.CompressionMethod = types.CompressionMethodNone
	info.SegmentNumber = uint32(binary.LittleEndian.Uint16(data[9:11]))

	return info, nil
}

func parseFileHeaderV2(source io.ReaderAt) (FileHeaderInfo, error) {
	data, err := binaryutil.ReadSlice(source, 0, 32)
	if err != nil {
		return FileHeaderInfo{}, fmt.Errorf("reader: unable to read v2 file header: %w", err)
	}

	info := FileHeaderInfo{}
	copy(info.Signature[:], data[0:8])
	info.MajorVersion = data[8]
	info.MinorVersion = data[9]
	info.CompressionMethod = binary.LittleEndian.Uint16(data[10:12])
	info.SegmentNumber = binary.LittleEndian.Uint32(data[12:16])

	return info, nil
}
