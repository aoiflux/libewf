package binaryutil

import (
	"encoding/binary"
	"fmt"
	"io"
)

// StructDecoder defines a fixed-size binary type that can decode itself.
type StructDecoder interface {
	BinarySize() int
	DecodeBinary(data []byte, order binary.ByteOrder) error
}

// MaxReadSize bounds a single ReadSlice allocation.
//
// Every size passed to ReadSlice ultimately comes from the image being
// parsed, so a corrupt or hostile length field would otherwise drive an
// unbounded allocation. This is a backstop: call sites that know a tighter
// bound should enforce it themselves and report a more specific error.
const MaxReadSize = 1 << 30

// ReadSlice reads a raw byte slice at the given offset.
func ReadSlice(r io.ReaderAt, off int64, n int) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("binaryutil: negative size: %d", n)
	}
	if n > MaxReadSize {
		return nil, fmt.Errorf("binaryutil: read size %d exceeds maximum %d", n, MaxReadSize)
	}
	if off < 0 {
		return nil, fmt.Errorf("binaryutil: negative offset: %d", off)
	}
	buf := make([]byte, n)
	_, err := r.ReadAt(buf, off)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// ReadUint16 reads a uint16 at the given offset with explicit byte order.
func ReadUint16(r io.ReaderAt, off int64, order binary.ByteOrder) (uint16, error) {
	buf, err := ReadSlice(r, off, 2)
	if err != nil {
		return 0, err
	}
	return order.Uint16(buf), nil
}

// ReadUint32 reads a uint32 at the given offset with explicit byte order.
func ReadUint32(r io.ReaderAt, off int64, order binary.ByteOrder) (uint32, error) {
	buf, err := ReadSlice(r, off, 4)
	if err != nil {
		return 0, err
	}
	return order.Uint32(buf), nil
}

// ReadUint64 reads a uint64 at the given offset with explicit byte order.
func ReadUint64(r io.ReaderAt, off int64, order binary.ByteOrder) (uint64, error) {
	buf, err := ReadSlice(r, off, 8)
	if err != nil {
		return 0, err
	}
	return order.Uint64(buf), nil
}

// ReadStruct reads a fixed-size type implementing StructDecoder at the given offset.
func ReadStruct(r io.ReaderAt, off int64, order binary.ByteOrder, out StructDecoder) error {
	size := out.BinarySize()
	if size < 0 {
		return fmt.Errorf("binaryutil: negative struct size: %d", size)
	}
	buf, err := ReadSlice(r, off, size)
	if err != nil {
		return err
	}
	return out.DecodeBinary(buf, order)
}
