package libewf

import (
	"io"

	"github.com/aoiflux/libewf/ewferr"
	"github.com/aoiflux/libewf/metadata"
	"github.com/aoiflux/libewf/reader"
)

// Sentinel errors returned by this package. Compare with errors.Is.
var (
	// ErrNotImplemented marks API endpoints that are intentionally absent.
	ErrNotImplemented = ewferr.ErrNotImplemented
	// ErrUnsupportedFormat indicates an unrecognised or undecodable format.
	ErrUnsupportedFormat = ewferr.ErrUnsupportedFormat
	// ErrCorruptImage indicates structural damage that prevents decoding.
	ErrCorruptImage = ewferr.ErrCorruptImage
	// ErrInvalidOffset indicates a negative or unusable ReadAt offset.
	ErrInvalidOffset = ewferr.ErrInvalidOffset
	// ErrEncrypted indicates the image is encrypted and no key was supplied.
	ErrEncrypted = ewferr.ErrEncrypted
	// ErrMissingSegment indicates a gap in the supplied segment numbering.
	ErrMissingSegment = ewferr.ErrMissingSegment
	// ErrIncompleteSegmentSet indicates trailing segments were not supplied.
	ErrIncompleteSegmentSet = ewferr.ErrIncompleteSegmentSet
	// ErrChecksumMismatch indicates checksum validation failed under
	// ChecksumStrict.
	ErrChecksumMismatch = ewferr.ErrChecksumMismatch
)

// SegmentError identifies which segment in a set failed. See ewferr.SegmentError.
type SegmentError = ewferr.SegmentError

// Reader exposes read operations over an EWF image presented as one
// contiguous decoded device.
//
// Implementations are safe for concurrent use provided the underlying
// io.ReaderAt sources are.
type Reader interface {
	io.ReaderAt

	// Size returns the logical size of the decoded device in bytes.
	Size() int64

	// SectorSize returns the logical sector size in bytes, or 0 if unknown.
	SectorSize() int

	// Metadata returns parsed image metadata.
	Metadata() metadata.Info

	// Close releases resources held by the reader. The caller retains
	// ownership of the io.ReaderAt sources and must close them separately.
	Close() error
}

// Writer exposes high-level write operations for EWF images.
//
// Deprecated: libewf is a read-only library by design. No write path is
// planned; Create always returns ErrNotImplemented.
type Writer interface {
	Write(p []byte) (n int, err error)
	Close() error
}

// Option configures how an image is opened.
type Option func(*reader.Options)

// ChecksumPolicy selects what happens when stored checksum validation fails.
type ChecksumPolicy = reader.ChecksumPolicy

// Checksum policies. The default is ChecksumWarn.
const (
	// ChecksumWarn decodes the image anyway and reports failures in
	// Metadata().ChunkTablesInvalid.
	ChecksumWarn = reader.ChecksumWarn
	// ChecksumIgnore suppresses checksum accounting entirely.
	ChecksumIgnore = reader.ChecksumIgnore
	// ChecksumStrict refuses to open an image with an unverifiable chunk table.
	ChecksumStrict = reader.ChecksumStrict
)

// WithChecksumPolicy selects the response to a chunk table that fails its
// stored Adler-32 checksum.
//
// The default, ChecksumWarn, decodes the image and records the failure, because
// damaged evidence should still yield whatever is readable. Pass ChecksumStrict
// when unverified chunk offsets are worse than no image at all; Open then fails
// with ErrChecksumMismatch.
func WithChecksumPolicy(p ChecksumPolicy) Option {
	return func(o *reader.Options) { o.ChecksumPolicy = p }
}

// WithChunkCache sets how many decoded chunks the reader keeps cached.
//
// A chunk is the smallest decodable unit of an EWF image, so a caller reading
// 512 bytes at a time would otherwise re-decompress the whole enclosing chunk
// on every call. Filesystem parsers read exactly like that, which is why
// caching is on by default.
//
// Pass a positive depth to override the default of 16, or a negative value to
// disable caching when memory matters more than throughput. Memory use is the
// depth multiplied by the image's chunk size, capped internally so an unusual
// chunk size cannot turn a small depth into a large allocation.
func WithChunkCache(chunks int) Option {
	return func(o *reader.Options) { o.ChunkCacheChunks = chunks }
}

// AllowIncompleteSegmentSet permits opening a segment set that does not begin
// at segment 1 or whose final segment carries no "done" section.
//
// Such a set decodes only part of the device: Size reports the full device
// size declared by the volume section, but reads past the supplied data return
// io.EOF. Use it for metadata inspection and triage of damaged evidence, never
// for content that will be hashed or carved.
func AllowIncompleteSegmentSet() Option {
	return func(o *reader.Options) { o.AllowIncompleteSegmentSet = true }
}

func buildOptions(opts []Option) reader.Options {
	var o reader.Options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// Open prepares an EWF reader from a random-access source.
//
// The source must be a complete single-segment image. To open a multi-segment
// set, pass every segment to OpenSegments.
func Open(source io.ReaderAt) (Reader, error) {
	return reader.Open(source)
}

// OpenSegments prepares an EWF reader from a full segment set. Segments may be
// supplied in any order; they are ordered by segment number before decoding.
//
// The set must be complete. A gap in the numbering, or a final segment that
// does not terminate with a "done" section, is an error: decoding a partial
// set would silently present a truncated or misaligned device.
func OpenSegments(sources []io.ReaderAt) (Reader, error) {
	return reader.OpenSegments(sources)
}

// OpenWithOptions prepares an EWF reader from a single source with options.
func OpenWithOptions(source io.ReaderAt, opts ...Option) (Reader, error) {
	return reader.OpenWithOptions(source, buildOptions(opts))
}

// OpenSegmentsWithOptions prepares an EWF reader from a segment set with options.
func OpenSegmentsWithOptions(sources []io.ReaderAt, opts ...Option) (Reader, error) {
	return reader.OpenSegmentsWithOptions(sources, buildOptions(opts))
}

// Create is not implemented and never will be.
//
// libewf is a read-only library: a forensic acquisition tool must not depend
// on an unproven write path, and no EWF writer is planned. Create exists only
// so the intent is explicit rather than an open TODO.
func Create(_ io.Writer) (Writer, error) {
	return nil, ErrNotImplemented
}
