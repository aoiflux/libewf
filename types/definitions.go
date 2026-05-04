package types

// Format values map EWF format identifiers.
const (
	FormatUnknown         uint8 = 0x00
	FormatEncase1         uint8 = 0x01
	FormatEncase2         uint8 = 0x02
	FormatEncase3         uint8 = 0x03
	FormatEncase4         uint8 = 0x04
	FormatEncase5         uint8 = 0x05
	FormatEncase6         uint8 = 0x06
	FormatEncase7         uint8 = 0x07
	FormatSmart           uint8 = 0x0e
	FormatFTKImager       uint8 = 0x0f
	FormatLogicalEncase5  uint8 = 0x10
	FormatLogicalEncase6  uint8 = 0x11
	FormatLogicalEncase7  uint8 = 0x12
	FormatLinen5          uint8 = 0x25
	FormatLinen6          uint8 = 0x26
	FormatLinen7          uint8 = 0x27
	FormatV2Encase7       uint8 = 0x37
	FormatV2LogicalEncase uint8 = 0x47
	FormatEWF             uint8 = 0x70
	FormatEWFX            uint8 = 0x71
)

// Compression methods map enum LIBEWF_COMPRESSION_METHODS.
const (
	CompressionMethodNone    uint16 = 0
	CompressionMethodDeflate uint16 = 1
	CompressionMethodBzip2   uint16 = 2
)

// Media types map enum LIBEWF_MEDIA_TYPES.
const (
	MediaTypeRemovable   uint8 = 0x00
	MediaTypeFixed       uint8 = 0x01
	MediaTypeOptical     uint8 = 0x03
	MediaTypeSingleFiles uint8 = 0x0e
	MediaTypeMemory      uint8 = 0x10
)

// Media flags map enum LIBEWF_MEDIA_FLAGS.
const (
	MediaFlagPhysical uint8 = 0x02
	MediaFlagFastbloc uint8 = 0x04
	MediaFlagTableau  uint8 = 0x08
)

// Section types map enum LIBEWF_SECTION_TYPES.
const (
	SectionTypeDeviceInformation uint32 = 0x00000001
	SectionTypeCaseData          uint32 = 0x00000002
	SectionTypeSectorData        uint32 = 0x00000003
	SectionTypeSectorTable       uint32 = 0x00000004
	SectionTypeErrorTable        uint32 = 0x00000005
	SectionTypeSessionTable      uint32 = 0x00000006
	SectionTypeIncrementData     uint32 = 0x00000007
	SectionTypeMD5Hash           uint32 = 0x00000008
	SectionTypeSHA1Hash          uint32 = 0x00000009
	SectionTypeRestartData       uint32 = 0x0000000a
	SectionTypeEncryptionKeys    uint32 = 0x0000000b
	SectionTypeMemoryExtents     uint32 = 0x0000000c
	SectionTypeNext              uint32 = 0x0000000d
	SectionTypeFinalInformation  uint32 = 0x0000000e
	SectionTypeDone              uint32 = 0x0000000f
	SectionTypeAnalyticalData    uint32 = 0x00000010
	SectionTypeSingleFilesData   uint32 = 0x00000020
)

// Section data flags map enum LIBEWF_SECTION_DATA_FLAGS.
const (
	SectionDataFlagHasIntegrityHash uint32 = 0x00000001
	SectionDataFlagEncrypted        uint32 = 0x00000002
)

// Chunk data flags map enum LIBEWF_CHUNK_DATA_FLAGS from public definitions.
const (
	ChunkDataFlagCompressed uint32 = 0x00000001
	ChunkDataFlagChecksum   uint32 = 0x00000002
	ChunkDataFlagPattern    uint32 = 0x00000004
)
