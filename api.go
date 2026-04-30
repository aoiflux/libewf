package libewf

import (
	"errors"
	"io"

	"github.com/aoiflux/libewf/ewf/metadata"
	"github.com/aoiflux/libewf/ewf/reader"
)

var (
	// ErrNotImplemented marks API endpoints that are scaffolded but not yet ported.
	ErrNotImplemented = errors.New("libewf: not implemented")
)

// Reader exposes high-level read operations for EWF images.
type Reader interface {
	ReadAt(p []byte, off int64) (n int, err error)
	Metadata() metadata.Info
	Close() error
}

// Writer exposes high-level write operations for EWF images.
type Writer interface {
	Write(p []byte) (n int, err error)
	Close() error
}

// Open prepares an EWF reader from a random-access source.
func Open(source io.ReaderAt) (Reader, error) {
	return reader.Open(source)
}

// OpenSegments prepares an EWF reader from a full segment set.
func OpenSegments(sources []io.ReaderAt) (Reader, error) {
	return reader.OpenSegments(sources)
}

// Create prepares an EWF writer.
func Create(_ io.Writer) (Writer, error) {
	return nil, ErrNotImplemented
}
