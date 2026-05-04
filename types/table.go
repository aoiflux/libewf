package types

import "encoding/binary"

// TableHeaderV1 represents an on-disk EWF v1 table header.
type TableHeaderV1 struct {
	NumberOfEntries [4]uint8
	Padding1        [4]uint8
	BaseOffset      [8]uint8
	Padding2        [4]uint8
	Checksum        [4]uint8
}

func (h TableHeaderV1) NumberOfEntriesValue(order binary.ByteOrder) uint32 {
	return order.Uint32(h.NumberOfEntries[:])
}

func (h TableHeaderV1) BaseOffsetValue(order binary.ByteOrder) uint64 {
	return order.Uint64(h.BaseOffset[:])
}

func (h TableHeaderV1) ChecksumValue(order binary.ByteOrder) uint32 {
	return order.Uint32(h.Checksum[:])
}

// TableEntryV1 represents an on-disk EWF v1 table entry.
type TableEntryV1 struct {
	ChunkDataOffset [4]uint8
}

func (e TableEntryV1) ChunkDataOffsetValue(order binary.ByteOrder) uint32 {
	return order.Uint32(e.ChunkDataOffset[:])
}

// TableHeaderV2 represents an on-disk EWF v2 table header.
type TableHeaderV2 struct {
	FirstChunkNumber [8]uint8
	NumberOfEntries  [4]uint8
	Unknown1         [4]uint8
	Checksum         [4]uint8
	Padding          [12]uint8
}

func (h TableHeaderV2) FirstChunkNumberValue(order binary.ByteOrder) uint64 {
	return order.Uint64(h.FirstChunkNumber[:])
}

func (h TableHeaderV2) NumberOfEntriesValue(order binary.ByteOrder) uint32 {
	return order.Uint32(h.NumberOfEntries[:])
}

func (h TableHeaderV2) ChecksumValue(order binary.ByteOrder) uint32 {
	return order.Uint32(h.Checksum[:])
}

// TableEntryV2 represents an on-disk EWF v2 table entry.
type TableEntryV2 struct {
	ChunkDataOffset [8]uint8
	ChunkDataSize   [4]uint8
	ChunkDataFlags  [4]uint8
}

func (e TableEntryV2) ChunkDataOffsetValue(order binary.ByteOrder) uint64 {
	return order.Uint64(e.ChunkDataOffset[:])
}

func (e TableEntryV2) ChunkDataSizeValue(order binary.ByteOrder) uint32 {
	return order.Uint32(e.ChunkDataSize[:])
}

func (e TableEntryV2) ChunkDataFlagsValue(order binary.ByteOrder) uint32 {
	return order.Uint32(e.ChunkDataFlags[:])
}
