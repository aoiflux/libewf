package reader

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/aoiflux/libewf/compression"
	"github.com/aoiflux/libewf/ewferr"
	"github.com/aoiflux/libewf/internal/binaryutil"
	"github.com/aoiflux/libewf/metadata"
	"github.com/aoiflux/libewf/types"
)

// chunkTrailerSize is the Adler-32 checksum EWF v1 stores after each
// uncompressed chunk. It is not device data.
const chunkTrailerSize = 4

// SegmentFileType identifies the segment file family.
type SegmentFileType uint8

const (
	SegmentFileTypeUnknown SegmentFileType = iota
	SegmentFileTypeEWF1
	SegmentFileTypeEWF1Logical
	SegmentFileTypeEWF2
	SegmentFileTypeEWF2Logical
)

// Options controls how a segment set is opened. The zero value is the default,
// strict behaviour.
type Options struct {
	// AllowIncompleteSegmentSet permits opening a segment set that does not
	// begin at segment 1 or whose final segment carries no "done" section.
	// Such a set decodes only a prefix of the device, so reads past the
	// supplied data return io.EOF even though Size reports the full device.
	// Use it for metadata inspection and for triage of damaged evidence;
	// never for content that will be hashed or carved.
	AllowIncompleteSegmentSet bool
}

// ImageReader presents a segment set as one contiguous decoded device.
//
// ImageReader is safe for concurrent use provided the underlying io.ReaderAt
// sources are, as os.File and io.SectionReader are.
type ImageReader struct {
	source          io.ReaderAt
	segmentFileType SegmentFileType
	header          FileHeaderInfo
	sections        []SectionInfo
	metadata        metadata.Info
	chunkTable      []chunkDescriptor

	// chunkSize is the decoded size of one full chunk in bytes.
	chunkSize int64
	// logicalSize is the size of the decoded device in bytes. No byte at or
	// beyond this offset is ever returned by ReadAt.
	logicalSize int64
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
	stats           chunkTableStats
}

// Open inspects the segment file signature and returns a reader instance.
func Open(source io.ReaderAt) (*ImageReader, error) {
	return OpenSegments([]io.ReaderAt{source})
}

// OpenSegments prepares a single logical reader from one or more EWF segments.
func OpenSegments(sources []io.ReaderAt) (*ImageReader, error) {
	return OpenSegmentsWithOptions(sources, Options{})
}

// OpenWithOptions opens a single segment with explicit options.
func OpenWithOptions(source io.ReaderAt, opts Options) (*ImageReader, error) {
	return OpenSegmentsWithOptions([]io.ReaderAt{source}, opts)
}

// OpenSegmentsWithOptions prepares a reader from a segment set with explicit
// options. Segments may be supplied in any order; they are ordered by segment
// number before decoding.
func OpenSegmentsWithOptions(sources []io.ReaderAt, opts Options) (*ImageReader, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("reader: no segment sources provided: %w", ewferr.ErrUnsupportedFormat)
	}

	segments := make([]parsedSegment, 0, len(sources))
	for i, source := range sources {
		segment, err := parseSegment(source)
		if err != nil {
			return nil, &ewferr.SegmentError{Segment: segment.header.SegmentNumber, Index: i, Op: "parse", Err: err}
		}
		segments = append(segments, segment)
	}

	sort.Slice(segments, func(i, j int) bool {
		return segments[i].header.SegmentNumber < segments[j].header.SegmentNumber
	})

	if err := validateSegmentSet(segments, opts); err != nil {
		return nil, err
	}

	mergedChunks := make([]chunkDescriptor, 0)
	for _, segment := range segments {
		mergedChunks = append(mergedChunks, segment.chunkTable...)
	}
	mergedMeta := mergeSegmentMetadata(segments, mergedChunks)

	chunkSize, logicalSize, err := computeGeometry(mergedMeta.Media)
	if err != nil {
		return nil, err
	}

	return &ImageReader{
		source:          segments[0].source,
		segmentFileType: segments[0].segmentFileType,
		header:          segments[0].header,
		sections:        segments[0].sections,
		metadata:        mergedMeta,
		chunkTable:      mergedChunks,
		chunkSize:       chunkSize,
		logicalSize:     logicalSize,
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
	chunkTable, stats := buildChunkTable(source, header, sections)

	return parsedSegment{
		source:          source,
		segmentFileType: segmentFileType,
		header:          header,
		sections:        sections,
		metadata:        meta,
		chunkTable:      chunkTable,
		stats:           stats,
	}, nil
}

// computeGeometry derives chunk and device sizes from the volume geometry.
// A segment set with no volume section (metadata-only, or EWF v2 where the
// volume is not yet parsed) yields zero sizes and a reader that reports
// ErrCorruptImage from ReadAt rather than failing to open.
func computeGeometry(media *metadata.MediaInfo) (chunkSize, logicalSize int64, err error) {
	if media == nil {
		return 0, 0, nil
	}
	if media.BytesPerSector == 0 || media.SectorsPerChunk == 0 {
		return 0, 0, fmt.Errorf("reader: invalid geometry (sectors_per_chunk=%d bytes_per_sector=%d): %w",
			media.SectorsPerChunk, media.BytesPerSector, ewferr.ErrCorruptImage)
	}

	spc := uint64(media.SectorsPerChunk)
	bps := uint64(media.BytesPerSector)
	if spc > uint64(math.MaxInt64)/bps {
		return 0, 0, fmt.Errorf("reader: chunk size overflows (sectors_per_chunk=%d bytes_per_sector=%d): %w",
			spc, bps, ewferr.ErrCorruptImage)
	}
	chunkSize = int64(spc * bps)

	if media.NumberOfSectors > uint64(math.MaxInt64)/bps {
		return 0, 0, fmt.Errorf("reader: device size overflows (sectors=%d bytes_per_sector=%d): %w",
			media.NumberOfSectors, bps, ewferr.ErrCorruptImage)
	}
	logicalSize = int64(media.NumberOfSectors * bps)

	return chunkSize, logicalSize, nil
}

// Size returns the logical size of the decoded device in bytes, which is
// NumberOfSectors * BytesPerSector. It returns 0 when the segment set carries
// no volume geometry.
func (r *ImageReader) Size() int64 { return r.logicalSize }

// SectorSize returns the logical sector size in bytes, or 0 when unknown.
func (r *ImageReader) SectorSize() int {
	if r.metadata.Media == nil {
		return 0
	}
	return int(r.metadata.Media.BytesPerSector)
}

// ReadAt reads decoded device bytes at the given offset, spanning chunk
// boundaries as needed and transparently decompressing stored chunks.
//
// It satisfies io.ReaderAt: it returns a non-nil error whenever n < len(p),
// and never returns data at or beyond Size.
func (r *ImageReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, fmt.Errorf("reader: offset %d: %w", off, ewferr.ErrInvalidOffset)
	}
	if r.chunkSize <= 0 || r.logicalSize <= 0 {
		return 0, fmt.Errorf("reader: no usable media geometry: %w", ewferr.ErrCorruptImage)
	}
	if len(r.chunkTable) == 0 {
		return 0, fmt.Errorf("reader: no chunk table available: %w", ewferr.ErrCorruptImage)
	}
	if off >= r.logicalSize {
		return 0, io.EOF
	}

	// Never hand back bytes past the end of the device: the stored final
	// chunk is padded, and EWF v1 appends a checksum after each chunk.
	want := int64(len(p))
	if remaining := r.logicalSize - off; want > remaining {
		want = remaining
	}

	totalRead := 0
	for int64(totalRead) < want {
		currentOff := off + int64(totalRead)
		chunkNum := int(currentOff / r.chunkSize)
		chunkOff := int(currentOff % r.chunkSize)

		if chunkNum >= len(r.chunkTable) {
			return totalRead, io.EOF
		}

		chunkData, err := r.chunkData(chunkNum)
		if err != nil {
			return totalRead, err
		}

		available := len(chunkData) - chunkOff
		if chunkRemaining := int(r.chunkSize) - chunkOff; chunkRemaining < available {
			available = chunkRemaining
		}
		if available <= 0 {
			return totalRead, io.EOF
		}

		toCopy := int(want) - totalRead
		if toCopy > available {
			toCopy = available
		}
		copy(p[totalRead:], chunkData[chunkOff:chunkOff+toCopy])
		totalRead += toCopy
	}

	if totalRead < len(p) {
		return totalRead, io.EOF
	}
	return totalRead, nil
}

// chunkData returns the decoded bytes of one stored chunk.
func (r *ImageReader) chunkData(chunkNum int) ([]byte, error) {
	desc := r.chunkTable[chunkNum]
	readSource := desc.dataSource
	if readSource == nil {
		readSource = r.source
	}
	if desc.dataOffset < 0 {
		return nil, fmt.Errorf("reader: chunk %d has negative offset %d: %w",
			chunkNum, desc.dataOffset, ewferr.ErrCorruptImage)
	}
	if desc.dataSize == 0 {
		return nil, fmt.Errorf("reader: chunk %d has zero stored size: %w", chunkNum, ewferr.ErrCorruptImage)
	}
	// A stored chunk holds at most one decoded chunk plus its checksum, or a
	// compressed stream of it. Anything substantially larger means the table
	// entry is damaged, so refuse it rather than allocate on its word.
	if maxStored := r.chunkSize*2 + 4096; int64(desc.dataSize) > maxStored {
		return nil, fmt.Errorf("reader: chunk %d stored size %d exceeds maximum %d: %w",
			chunkNum, desc.dataSize, maxStored, ewferr.ErrCorruptImage)
	}

	rawData, err := binaryutil.ReadSlice(readSource, desc.dataOffset, int(desc.dataSize))
	if err != nil {
		return nil, fmt.Errorf("reader: unable to read chunk %d at offset %d: %w", chunkNum, desc.dataOffset, err)
	}

	if desc.compressed {
		// EWF v1 always uses deflate; v2 uses the segment header compression method.
		method := desc.compressionMethod
		if desc.majorVersion == 1 {
			method = types.CompressionMethodDeflate
		}
		out, err := compression.Decompress(rawData, method)
		if err != nil {
			return nil, fmt.Errorf("reader: unable to decompress chunk %d: %w", chunkNum, err)
		}
		return out, nil
	}

	// An uncompressed v1 chunk that overruns a full chunk is carrying its
	// trailing Adler-32; strip it. A short final chunk is bounded by the
	// logical device size instead, so it needs no adjustment here.
	if desc.majorVersion == 1 && int64(len(rawData)) > r.chunkSize {
		return rawData[:len(rawData)-chunkTrailerSize], nil
	}
	return rawData, nil
}

// validateSegmentSet checks that the supplied segments form one coherent,
// complete evidence set. segments must already be sorted by segment number.
func validateSegmentSet(segments []parsedSegment, opts Options) error {
	first := segments[0]

	seen := make(map[uint32]struct{}, len(segments))
	for i, segment := range segments {
		if segment.header.MajorVersion != first.header.MajorVersion {
			return fmt.Errorf("reader: segment %d major version %d does not match segment %d major version %d: %w",
				segment.header.SegmentNumber, segment.header.MajorVersion,
				first.header.SegmentNumber, first.header.MajorVersion, ewferr.ErrCorruptImage)
		}
		if segment.segmentFileType != first.segmentFileType {
			return fmt.Errorf("reader: segment %d type %d does not match segment %d type %d: %w",
				segment.header.SegmentNumber, segment.segmentFileType,
				first.header.SegmentNumber, first.segmentFileType, ewferr.ErrCorruptImage)
		}
		if _, ok := seen[segment.header.SegmentNumber]; ok {
			return fmt.Errorf("reader: duplicate segment number %d: %w",
				segment.header.SegmentNumber, ewferr.ErrCorruptImage)
		}
		seen[segment.header.SegmentNumber] = struct{}{}

		// Sorted, so numbering must run 1..n with no holes. A hole means a
		// segment was not supplied and the device would decode misaligned.
		if want := uint32(i + 1); segment.header.SegmentNumber != want {
			if opts.AllowIncompleteSegmentSet {
				continue
			}
			if segment.header.SegmentNumber > want {
				return fmt.Errorf("reader: segment %d is missing from the set: %w", want, ewferr.ErrMissingSegment)
			}
			return fmt.Errorf("reader: segment numbering does not start at 1 (found %d): %w",
				segment.header.SegmentNumber, ewferr.ErrIncompleteSegmentSet)
		}
	}

	// The final segment of a complete set terminates with a "done" section;
	// every earlier segment terminates with "next". A trailing "next" means
	// at least one more segment file exists and was not supplied.
	if !opts.AllowIncompleteSegmentSet {
		last := segments[len(segments)-1]
		if !last.metadata.HasDoneSection {
			return fmt.Errorf("reader: highest segment %d has no done section, later segments are missing: %w",
				last.header.SegmentNumber, ewferr.ErrIncompleteSegmentSet)
		}
	}
	return nil
}

func mergeSegmentMetadata(segments []parsedSegment, mergedChunks []chunkDescriptor) metadata.Info {
	merged := segments[0].metadata

	// Copy the media geometry so merging never mutates the parsed segment.
	if merged.Media != nil {
		mediaCopy := *merged.Media
		merged.Media = &mediaCopy
	}
	merged.ChunkTablesRecovered = segments[0].stats.TablesRecovered
	merged.ChunkTablesInvalid = segments[0].stats.TablesInvalid

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

		// A volume section appears only in the first segment of a set, but
		// fall back to a later one rather than losing geometry entirely.
		if merged.Media == nil && meta.Media != nil {
			mediaCopy := *meta.Media
			merged.Media = &mediaCopy
		}
		merged.ChunkTablesRecovered += segments[i].stats.TablesRecovered
		merged.ChunkTablesInvalid += segments[i].stats.TablesInvalid
	}

	// Media.NumberOfChunks stays as the volume section declared it; the count
	// actually decoded is reported separately so callers can compare.
	merged.ObservedChunkCount = uint64(len(mergedChunks))
	return merged
}

// Close releases resources held by the reader. The caller retains ownership of
// the io.ReaderAt sources and is responsible for closing them.
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

// MD5 returns the stored acquisition MD5 digest, if the image records one.
func (r *ImageReader) MD5() ([]byte, bool) {
	if !r.metadata.HasMD5Digest {
		return nil, false
	}
	out := make([]byte, len(r.metadata.MD5Digest))
	copy(out, r.metadata.MD5Digest[:])
	return out, true
}

// SHA1 returns the stored acquisition SHA-1 digest, if the image records one.
func (r *ImageReader) SHA1() ([]byte, bool) {
	if !r.metadata.HasSHA1Digest {
		return nil, false
	}
	out := make([]byte, len(r.metadata.SHA1Digest))
	copy(out, r.metadata.SHA1Digest[:])
	return out, true
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
		return SegmentFileTypeUnknown, fmt.Errorf("reader: unsupported segment signature %x: %w",
			signature, ewferr.ErrUnsupportedFormat)
	}
}

func parseFileHeader(source io.ReaderAt, segmentFileType SegmentFileType) (FileHeaderInfo, error) {
	switch segmentFileType {
	case SegmentFileTypeEWF1, SegmentFileTypeEWF1Logical:
		return parseFileHeaderV1(source)
	case SegmentFileTypeEWF2, SegmentFileTypeEWF2Logical:
		return parseFileHeaderV2(source)
	default:
		return FileHeaderInfo{}, fmt.Errorf("reader: cannot parse header for segment type %d: %w",
			segmentFileType, ewferr.ErrUnsupportedFormat)
	}
}

func parseFileHeaderV1(source io.ReaderAt) (FileHeaderInfo, error) {
	data, err := binaryutil.ReadSlice(source, 0, 13)
	if err != nil {
		return FileHeaderInfo{}, fmt.Errorf("reader: unable to read v1 file header: %w", err)
	}

	if data[8] != 0x01 {
		return FileHeaderInfo{}, fmt.Errorf("reader: invalid v1 header fields_start 0x%02x: %w",
			data[8], ewferr.ErrCorruptImage)
	}
	if data[11] != 0x00 || data[12] != 0x00 {
		return FileHeaderInfo{}, fmt.Errorf("reader: invalid v1 header fields_end %02x%02x: %w",
			data[11], data[12], ewferr.ErrCorruptImage)
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
