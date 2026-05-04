package types

import "encoding/binary"

// FileHeaderV1 represents an on-disk EWF v1 file header.
type FileHeaderV1 struct {
	Signature     [8]uint8
	FieldsStart   uint8
	SegmentNumber [2]uint8
	FieldsEnd     [2]uint8
}

// SegmentNumberValue returns the little-endian segment number.
func (h FileHeaderV1) SegmentNumberValue(order binary.ByteOrder) uint16 {
	return order.Uint16(h.SegmentNumber[:])
}

// FileHeaderV2 represents an on-disk EWF v2 file header.
type FileHeaderV2 struct {
	Signature         [8]uint8
	MajorVersion      uint8
	MinorVersion      uint8
	CompressionMethod [2]uint8
	SegmentNumber     [4]uint8
	SetIdentifier     [16]uint8
}

// CompressionMethodValue returns the little-endian compression method.
func (h FileHeaderV2) CompressionMethodValue(order binary.ByteOrder) uint16 {
	return order.Uint16(h.CompressionMethod[:])
}

// SegmentNumberValue returns the little-endian segment number.
func (h FileHeaderV2) SegmentNumberValue(order binary.ByteOrder) uint32 {
	return order.Uint32(h.SegmentNumber[:])
}
