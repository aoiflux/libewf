package metadata

// Info captures parsed image-level metadata available after opening a segment.
type Info struct {
	MajorVersion           uint8
	MinorVersion           uint8
	CompressionMethod      uint16
	SegmentNumber          uint32
	SectionCount           int
	HasNextSection         bool
	HasDoneSection         bool
	IsEncrypted            bool
	HasIntegrityHashBlocks bool
	SectionTypeCounts      map[uint32]int
	Sections               []Section
	Media                  *MediaInfo
	HasMD5Digest           bool
	MD5Digest              [16]byte
	HasSHA1Digest          bool
	SHA1Digest             [20]byte
	Sessions               []SessionEntry
	AcquisitionErrors      []AcquisitionError
}

// Section captures descriptor-level metadata for one section in logical order.
type Section struct {
	Offset     int64
	Type       uint32
	TypeName   string
	DataFlags  uint32
	Size       uint64
	DataOffset int64
	DataSize   uint64
	Padding    uint32
	Descriptor uint32
}

// SessionEntry represents one entry from a session section.
type SessionEntry struct {
	Flags       uint32
	StartSector uint64
}

// AcquisitionError represents one entry from an error2 section.
type AcquisitionError struct {
	StartSector     uint64
	NumberOfSectors uint32
}

// MediaInfo stores parsed media geometry and acquisition metadata.
type MediaInfo struct {
	MediaType        uint8
	NumberOfChunks   uint64
	SectorsPerChunk  uint32
	BytesPerSector   uint32
	NumberOfSectors  uint64
	MediaFlags       uint8
	CompressionLevel uint8
	ErrorGranularity uint32
	SetIdentifier    [16]byte
}
