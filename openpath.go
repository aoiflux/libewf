package libewf

import (
	"errors"
	"io"
	"os"
	"sync"

	"github.com/aoiflux/libewf/ewferr"
	"github.com/aoiflux/libewf/internal/segname"
	"github.com/aoiflux/libewf/reader"
)

// OpenPath prepares an EWF reader from a path to one segment file, discovering
// the rest of the set on disk.
//
// This is the constructor most callers want. Open and OpenSegments take
// io.ReaderAt because the library should not assume evidence lives on a local
// filesystem, but the common case is that it does, and every such caller was
// otherwise obliged to reimplement the EWF naming progression — which runs
// .E01 to .E99 and then continues .EAA, not .E100 — before they could open an
// image at all.
//
// path may name any numbered member of the set, not only the first: the set is
// identified by the stem and family of the name and then enumerated from
// segment 1, so an image opened by its .E03 still decodes from its .E01. A path
// whose extension names no EWF family is opened as a single file.
//
// # Ownership
//
// Unlike every other constructor in this package, the returned Reader owns the
// files it opened and its Close closes them. It has to: the caller never sees
// the handles, so nobody else can. Callers needing the concrete reader's
// methods can reach it with an Unwrap() Reader type assertion.
//
// # Completeness
//
// A hole in the numbering fails with a *MissingSegmentsError naming the files
// that were not found. A set that stops short of its true end cannot be
// detected from the directory — nothing there records how many segments the
// acquisition wrote — and is caught instead when the segments are read, as
// ErrIncompleteSegmentSet. Both are suppressed by AllowIncompleteSegmentSet.
func OpenPath(path string) (Reader, error) {
	return OpenPathWithOptions(path)
}

// OpenPathWithOptions prepares an EWF reader from a segment path with options.
// See OpenPath for how the segment set is discovered and who owns the files.
func OpenPathWithOptions(path string, opts ...Option) (Reader, error) {
	options := buildOptions(opts)

	paths, err := discoverSegments(path, options.AllowIncompleteSegmentSet)
	if err != nil {
		return nil, err
	}

	files := make([]*os.File, 0, len(paths))
	sources := make([]io.ReaderAt, 0, len(paths))
	for i, segmentPath := range paths {
		file, err := os.Open(segmentPath)
		if err != nil {
			closeFiles(files)
			return nil, &ewferr.SegmentError{Index: i, Op: "open", Err: err}
		}
		files = append(files, file)
		sources = append(sources, file)
	}

	image, err := reader.OpenSegmentsWithOptions(sources, options)
	if err != nil {
		closeFiles(files)
		return nil, err
	}
	return &pathReader{Reader: image, files: files}, nil
}

// SegmentPaths returns the paths of every segment that belongs with path, in
// segment order, without opening any of them.
//
// It exists so that a caller can record which files an image was decoded from.
// An examiner's report that names the evidence is worth more than one that says
// a set was complete, and the same ordering is what OpenPath itself uses.
func SegmentPaths(path string) ([]string, error) {
	return discoverSegments(path, false)
}

func discoverSegments(path string, allowIncomplete bool) ([]string, error) {
	set, err := segname.Discover(path)
	if err != nil {
		return nil, err
	}

	if !allowIncomplete {
		if missing := set.Missing(); len(missing) > 0 {
			return nil, &ewferr.MissingSegmentsError{
				Path:     path,
				Missing:  missing,
				Present:  set.Numbers,
				Expected: set.Names(missing),
			}
		}
	}
	return set.Paths, nil
}

// pathReader is a Reader over files this package opened, and therefore has to
// close.
type pathReader struct {
	Reader

	files     []*os.File
	closeOnce sync.Once
	closeErr  error
}

// Close closes the decoded reader and every file OpenPath opened for it.
//
// It is idempotent and safe to call concurrently, because a reader handed
// around a program is closed by whichever part of it finishes last, and
// double-closing a file descriptor is worse than doing nothing.
func (r *pathReader) Close() error {
	r.closeOnce.Do(func() {
		errs := make([]error, 0, len(r.files)+1)
		errs = append(errs, r.Reader.Close())
		for _, file := range r.files {
			errs = append(errs, file.Close())
		}
		r.closeErr = errors.Join(errs...)
	})
	return r.closeErr
}

// Unwrap returns the reader over the opened files, so callers can reach the
// concrete reader's methods that the Reader interface does not carry.
func (r *pathReader) Unwrap() Reader { return r.Reader }

func closeFiles(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}
