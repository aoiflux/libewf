package types

import "encoding/binary"

// SectionDescriptorV1 represents an on-disk EWF v1 section descriptor.
type SectionDescriptorV1 struct {
	TypeString [16]uint8
	NextOffset [8]uint8
	Size       [8]uint8
	Padding    [40]uint8
	Checksum   [4]uint8
}

func (d SectionDescriptorV1) NextOffsetValue(order binary.ByteOrder) uint64 {
	return order.Uint64(d.NextOffset[:])
}

func (d SectionDescriptorV1) SizeValue(order binary.ByteOrder) uint64 {
	return order.Uint64(d.Size[:])
}

func (d SectionDescriptorV1) ChecksumValue(order binary.ByteOrder) uint32 {
	return order.Uint32(d.Checksum[:])
}

// SectionDescriptorV2 represents an on-disk EWF v2 section descriptor.
type SectionDescriptorV2 struct {
	Type              [4]uint8
	DataFlags         [4]uint8
	PreviousOffset    [8]uint8
	DataSize          [8]uint8
	DescriptorSize    [4]uint8
	PaddingSize       [4]uint8
	DataIntegrityHash [16]uint8
	Padding           [12]uint8
	Checksum          [4]uint8
}

func (d SectionDescriptorV2) TypeValue(order binary.ByteOrder) uint32 {
	return order.Uint32(d.Type[:])
}

func (d SectionDescriptorV2) DataFlagsValue(order binary.ByteOrder) uint32 {
	return order.Uint32(d.DataFlags[:])
}

func (d SectionDescriptorV2) PreviousOffsetValue(order binary.ByteOrder) uint64 {
	return order.Uint64(d.PreviousOffset[:])
}

func (d SectionDescriptorV2) DataSizeValue(order binary.ByteOrder) uint64 {
	return order.Uint64(d.DataSize[:])
}

func (d SectionDescriptorV2) DescriptorSizeValue(order binary.ByteOrder) uint32 {
	return order.Uint32(d.DescriptorSize[:])
}

func (d SectionDescriptorV2) PaddingSizeValue(order binary.ByteOrder) uint32 {
	return order.Uint32(d.PaddingSize[:])
}

func (d SectionDescriptorV2) ChecksumValue(order binary.ByteOrder) uint32 {
	return order.Uint32(d.Checksum[:])
}
