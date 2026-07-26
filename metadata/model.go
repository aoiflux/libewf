package metadata

import "time"

// Acquisition holds the provenance recorded when the image was created: who
// acquired it, from what, when, and with which tool.
//
// It is decoded from the "header", "header2" and "xheader" sections. Those
// carry the same information in three encodings, and an image often contains
// more than one; the richer source wins. Fields absent from the image are left
// zero.
type Acquisition struct {
	CaseNumber     string
	EvidenceNumber string
	Description    string
	ExaminerName   string
	Notes          string

	// Model, SerialNumber and DeviceLabel describe the acquired device. Only
	// EnCase 6 and later record them.
	Model        string
	SerialNumber string
	DeviceLabel  string

	// SoftwareVersion and OperatingSystem identify the imaging tool.
	SoftwareVersion string
	OperatingSystem string

	// CompressionType is the writer's own label for the compression it used
	// ("b" best, "f" fast, "n" none), which is not always consistent with the
	// per-chunk encoding.
	CompressionType string

	// PasswordHash is recorded by some writers. It is not a decryption key and
	// this library never uses it.
	PasswordHash string

	// ProcessIdentifier is written by EnCase 6 and later.
	ProcessIdentifier string

	// AcquiryDate is when imaging started; SystemDate is the acquiring host's
	// clock at that moment.
	//
	// The stored formats carry no timezone, so a value that is not a POSIX
	// timestamp is interpreted in the local zone. Compare with care across
	// machines, and prefer the Raw fields for anything that must round-trip.
	AcquiryDate time.Time
	SystemDate  time.Time

	// AcquiryDateRaw and SystemDateRaw preserve the stored text exactly,
	// including values this library could not parse into a time.Time.
	AcquiryDateRaw string
	SystemDateRaw  string

	// Values holds every identifier and value from the header, including any
	// with no dedicated field above. Keys are the short on-disk identifiers
	// ("c", "av", "ov") for the header/header2 sections, or the element names
	// for xheader.
	Values map[string]string

	// Source names the section the values were taken from.
	Source string
}

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

	// ObservedChunkCount is the number of chunk descriptors actually decoded
	// from the supplied segments. Media.NumberOfChunks is the count declared
	// by the volume section; the two differ when segments are missing or a
	// chunk table could not be decoded.
	ObservedChunkCount uint64

	// ChunkTablesRecovered counts chunk-table groups whose primary "table"
	// section failed its checksum and were decoded from the "table2" backup.
	ChunkTablesRecovered int

	// ChunkTablesInvalid counts chunk-table groups where neither the primary
	// nor the backup copy validated. Their chunks were decoded unverified and
	// the data they describe should be treated as suspect.
	ChunkTablesInvalid int

	// Acquisition holds the provenance recorded at imaging time, or nil when
	// the image carries no header section this library can decode.
	Acquisition *Acquisition
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
