package libewf

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"fmt"
	"io"
)

// BadRange records a span of the device that could not be decoded.
type BadRange struct {
	// Offset is the byte offset of the span within the decoded device.
	Offset int64
	// Length is the length of the span in bytes.
	Length int64
	// Err is the error reported for the span.
	Err string
}

// VerifyResult reports the outcome of recomputing acquisition digests over a
// decoded image.
type VerifyResult struct {
	// Size is the logical device size that was hashed.
	Size int64
	// BytesHashed counts the bytes fed to the digests, including any zero
	// fill substituted for unreadable spans.
	BytesHashed int64

	// HasStoredMD5 reports whether the image records an acquisition MD5.
	HasStoredMD5 bool
	// StoredMD5 is the digest recorded at acquisition time.
	StoredMD5 []byte
	// ComputedMD5 is the digest of the decoded device.
	ComputedMD5 []byte
	// MD5Match is true when a stored MD5 exists and matches.
	MD5Match bool

	// HasStoredSHA1 reports whether the image records an acquisition SHA-1.
	HasStoredSHA1 bool
	// StoredSHA1 is the digest recorded at acquisition time.
	StoredSHA1 []byte
	// ComputedSHA1 is the digest of the decoded device.
	ComputedSHA1 []byte
	// SHA1Match is true when a stored SHA-1 exists and matches.
	SHA1Match bool

	// BadRanges lists spans that could not be decoded. Each was replaced with
	// zero bytes so that hashing could continue, which means any entry here
	// invalidates the computed digests.
	BadRanges []BadRange
}

// OK reports whether every stored digest was reproduced and no span failed to
// decode. An image that stores no digests can never be OK, because there is
// nothing to verify against.
func (v *VerifyResult) OK() bool {
	if len(v.BadRanges) > 0 {
		return false
	}
	if !v.HasStoredMD5 && !v.HasStoredSHA1 {
		return false
	}
	if v.HasStoredMD5 && !v.MD5Match {
		return false
	}
	if v.HasStoredSHA1 && !v.SHA1Match {
		return false
	}
	return true
}

// Verify recomputes MD5 and SHA-1 over the whole decoded device and compares
// them against the digests stored in the image at acquisition time.
//
// This is the strongest self-contained integrity check available for an EWF
// image: the stored digests were computed by the acquisition tool from the
// original device, so reproducing them from the decoded stream confirms both
// that the media is intact and that it was decoded correctly.
//
// Spans that fail to decode are replaced with zero bytes, recorded in
// BadRanges, and hashing continues, so a damaged image still yields a report
// of how much is unreadable. Any such span makes the digests meaningless;
// check OK or BadRanges before trusting a mismatch as evidence of tampering.
//
// Verify reads the entire device and is therefore O(image size). It honours
// ctx cancellation between reads.
func Verify(ctx context.Context, r Reader) (*VerifyResult, error) {
	size := r.Size()
	if size <= 0 {
		return nil, fmt.Errorf("libewf: image has no logical size to verify: %w", ErrCorruptImage)
	}

	meta := r.Metadata()
	res := &VerifyResult{Size: size}

	if meta.HasMD5Digest {
		res.HasStoredMD5 = true
		res.StoredMD5 = append([]byte(nil), meta.MD5Digest[:]...)
	}
	if meta.HasSHA1Digest {
		res.HasStoredSHA1 = true
		res.StoredSHA1 = append([]byte(nil), meta.SHA1Digest[:]...)
	}

	md5h := md5.New()
	sha1h := sha1.New()
	sink := io.MultiWriter(md5h, sha1h)

	// Hash a chunk at a time so a decode failure is attributed to the
	// narrowest span the format can isolate.
	step := int64(1 << 20)
	if meta.Media != nil {
		if cs := int64(meta.Media.SectorsPerChunk) * int64(meta.Media.BytesPerSector); cs > 0 {
			step = cs
		}
	}
	buf := make([]byte, step)

	for off := int64(0); off < size; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		want := step
		if remaining := size - off; want > remaining {
			want = remaining
		}

		n, err := r.ReadAt(buf[:want], off)
		if n > 0 {
			if _, werr := sink.Write(buf[:n]); werr != nil {
				return nil, werr
			}
			res.BytesHashed += int64(n)
		}

		if int64(n) < want {
			// Substitute zeros for the undecodable remainder so the offsets
			// of everything after this span still line up.
			gap := want - int64(n)
			msg := "short read"
			if err != nil {
				msg = err.Error()
			}
			res.BadRanges = append(res.BadRanges, BadRange{
				Offset: off + int64(n),
				Length: gap,
				Err:    msg,
			})
			if werr := writeZeros(sink, gap); werr != nil {
				return nil, werr
			}
			res.BytesHashed += gap
		}

		off += want
	}

	res.ComputedMD5 = md5h.Sum(nil)
	res.ComputedSHA1 = sha1h.Sum(nil)
	res.MD5Match = res.HasStoredMD5 && bytes.Equal(res.StoredMD5, res.ComputedMD5)
	res.SHA1Match = res.HasStoredSHA1 && bytes.Equal(res.StoredSHA1, res.ComputedSHA1)

	return res, nil
}

func writeZeros(w io.Writer, n int64) error {
	var zeros [32 << 10]byte
	for n > 0 {
		step := int64(len(zeros))
		if n < step {
			step = n
		}
		if _, err := w.Write(zeros[:step]); err != nil {
			return err
		}
		n -= step
	}
	return nil
}
