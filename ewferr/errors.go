// Package ewferr defines the sentinel error values returned by libewf.
//
// It is a leaf package so that internal packages can return typed errors
// without importing the root package. The root package re-exports every
// value here, so callers normally use github.com/aoiflux/libewf directly.
//
// All errors are designed for use with errors.Is:
//
//	if _, err := libewf.Open(f); errors.Is(err, libewf.ErrEncrypted) {
//		// prompt for a key
//	}
package ewferr

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	// ErrNotImplemented marks API endpoints that are intentionally absent.
	ErrNotImplemented = errors.New("libewf: not implemented")

	// ErrUnsupportedFormat indicates the source is not a recognised EWF
	// segment, or uses a format variant this library cannot decode.
	ErrUnsupportedFormat = errors.New("libewf: unsupported format")

	// ErrCorruptImage indicates structural damage that prevents decoding:
	// an unreadable section chain, impossible geometry, or a chunk table
	// that does not describe a usable device.
	ErrCorruptImage = errors.New("libewf: corrupt image")

	// ErrInvalidOffset indicates a negative or otherwise unusable offset
	// was passed to ReadAt.
	ErrInvalidOffset = errors.New("libewf: invalid offset")

	// ErrEncrypted indicates the image is encrypted and no usable key was
	// supplied. Readers never return ciphertext as device data; opening an
	// encrypted image without a key fails rather than decoding garbage.
	ErrEncrypted = errors.New("libewf: image is encrypted")

	// ErrMissingSegment indicates a gap in the supplied segment numbering,
	// for example segments 1 and 3 with no segment 2.
	ErrMissingSegment = errors.New("libewf: missing segment")

	// ErrIncompleteSegmentSet indicates the supplied segments do not form a
	// complete evidence set: the highest-numbered segment does not carry a
	// "done" section, so at least one trailing segment was not supplied.
	ErrIncompleteSegmentSet = errors.New("libewf: incomplete segment set")

	// ErrChecksumMismatch indicates stored checksum validation failed and the
	// configured policy treats that as fatal.
	ErrChecksumMismatch = errors.New("libewf: checksum mismatch")
)

// Every value above is returned by some code path. Sentinels for features that
// do not exist yet are deliberately absent: an exported error that can never
// occur invites callers to write a branch that never runs, and reads as though
// the feature were handled. They will be added along with the code that returns
// them.

// SegmentError identifies the segment in a multi-segment set that failed,
// so callers can report which file on disk is at fault.
type SegmentError struct {
	// Segment is the EWF segment number, or 0 when the segment could not be
	// parsed far enough to determine it.
	Segment uint32
	// Index is the position of the segment in the slice passed to
	// OpenSegments, which maps back to the caller's file list.
	Index int
	// Op names the operation that failed, e.g. "parse" or "read".
	Op  string
	Err error
}

func (e *SegmentError) Error() string {
	if e.Segment != 0 {
		return fmt.Sprintf("libewf: segment %d (index %d): %s: %v", e.Segment, e.Index, e.Op, e.Err)
	}
	return fmt.Sprintf("libewf: segment at index %d: %s: %v", e.Index, e.Op, e.Err)
}

func (e *SegmentError) Unwrap() error { return e.Err }

// maxListedSegments bounds how many segment numbers or names an error message
// spells out. A set missing three segments should say which three; a set
// missing six hundred should still produce an error a human can read.
const maxListedSegments = 10

// MissingSegmentsError reports the segment numbers absent from a set that was
// discovered on disk, naming the files to go and find rather than only saying
// that the set is incomplete.
//
// It unwraps to ErrMissingSegment, so code written against the sentinel keeps
// working:
//
//	if _, err := libewf.OpenPath(path); errors.Is(err, libewf.ErrMissingSegment) {
//		var missing *libewf.MissingSegmentsError
//		if errors.As(err, &missing) {
//			// missing.Expected names the files that were not found
//		}
//	}
type MissingSegmentsError struct {
	// Path is the path the set was discovered from.
	Path string

	// Missing lists the absent segment numbers, ascending.
	Missing []uint32

	// Present lists the segment numbers that were found, ascending.
	Present []uint32

	// Expected names the missing segment files, parallel to Missing. It is
	// empty when the naming family could not express them.
	Expected []string
}

func (e *MissingSegmentsError) Error() string {
	numbers := make([]string, len(e.Missing))
	for i, number := range e.Missing {
		numbers[i] = strconv.FormatUint(uint64(number), 10)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "libewf: segment set %s is incomplete: segment", e.Path)
	if len(e.Missing) != 1 {
		b.WriteByte('s')
	}
	fmt.Fprintf(&b, " %s missing", listSegments(numbers))
	if len(e.Expected) > 0 {
		fmt.Fprintf(&b, " (expected %s)", listSegments(e.Expected))
	}
	fmt.Fprintf(&b, "; %d of %d present", len(e.Present), len(e.Present)+len(e.Missing))
	return b.String()
}

func (e *MissingSegmentsError) Unwrap() error { return ErrMissingSegment }

func listSegments(items []string) string {
	if len(items) <= maxListedSegments {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more",
		strings.Join(items[:maxListedSegments], ", "), len(items)-maxListedSegments)
}
