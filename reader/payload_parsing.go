package reader

import (
	"encoding/binary"
	"io"

	"github.com/aoiflux/libewf/internal/binaryutil"
	"github.com/aoiflux/libewf/metadata"
	"github.com/aoiflux/libewf/types"
)

const (
	ewfVolumeSize      = 1052 // sizeof(ewf_volume_t)       — EWF-E01/L01
	ewfVolumeSmartSize = 94   // sizeof(ewf_volume_smart_t) — EWF-S01
	ewfHashSize        = 36 // sizeof(ewf_hash_t)   — v1 "hash" section
	ewfDigestSize      = 80 // sizeof(ewf_digest_t) — v1 "digest" section

	// Digest lengths, not section sizes. The EWF2 md5_hash and sha1_hash
	// sections are 32 bytes on disk, but the trailing padding is reported
	// separately and stripped before the body reaches a parser, so requiring
	// the full 32 bytes would reject every real one: an md5_hash body arrives
	// as 20 bytes (digest plus checksum) and a sha1_hash body as 24.
	ewfMD5DigestLength  = 16
	ewfSHA1DigestLength = 20

	// maxSectionPayload bounds a metadata section body read. Section sizes
	// are read straight from the image, so a damaged size field must not be
	// allowed to drive a huge allocation. The largest legitimate metadata
	// section is orders of magnitude below this.
	maxSectionPayload = 64 << 20
)

func populateMetadataFromSectionBodies(source io.ReaderAt, header FileHeaderInfo, sections []SectionInfo, info *metadata.Info) {
	// EWF writes backup copies of some sections under a second name within
	// the same segment ("error" / "error2"). Parsing both would duplicate
	// every entry in the accumulated slices, so only the first of each
	// list-valued section is consumed per segment. Scalar sections such as
	// the digests are idempotent and need no such guard.
	sessionParsed := false
	errorsParsed := false

	// Header sections are collected rather than applied as they are met, so
	// their precedence does not depend on the order they appear in the file.
	var headers headerPayloads

	for _, section := range sections {
		payloadSize := sectionPayloadSize(section)
		if payloadSize == 0 || payloadSize > maxSectionPayload {
			continue
		}
		data, err := binaryutil.ReadSlice(source, section.DataOffset, int(payloadSize))
		if err != nil {
			continue
		}

		switch section.Type {
		case types.SectionTypeDeviceInformation:
			if header.MajorVersion == 1 {
				switch {
				case len(data) >= ewfVolumeSize:
					parseVolumeSectionE01(data, info)
				case len(data) >= ewfVolumeSmartSize:
					parseVolumeSectionS01(data, info)
				}
			} else {
				parseDeviceInformationV2(data, info)
			}

		case types.SectionTypeCaseData:
			if header.MajorVersion != 1 {
				parseCaseDataV2(data, info)
			}
		case types.SectionTypeMD5Hash:
			parseMD5LikeSection(header.MajorVersion, data, info)
		case types.SectionTypeSHA1Hash:
			parseSHA1Section(header.MajorVersion, data, info)
		case types.SectionTypeSessionTable:
			if sessionParsed {
				continue
			}
			parseSessionSection(header.MajorVersion, data, info)
			sessionParsed = true
		case types.SectionTypeErrorTable:
			if errorsParsed {
				continue
			}
			parseErrorSection(header.MajorVersion, data, info)
			errorsParsed = true

		default:
			// EWF v1 identifies sections by name and has several with no
			// counterpart in the v2 type enumeration, so they carry no type
			// code. Dispatch those on their name.
			if header.MajorVersion != 1 {
				continue
			}
			switch section.TypeString {
			case "xhash":
				parseXHashSection(data, info)
			case "header":
				if headers.header == nil {
					headers.header = data
				}
			case "header2":
				if headers.header2 == nil {
					headers.header2 = data
				}
			case "xheader":
				if headers.xheader == nil {
					headers.xheader = data
				}
			}
		}
	}

	resolveAcquisition(headers, info)
}

func sectionPayloadSize(section SectionInfo) uint64 {
	if section.DataSize == 0 {
		return 0
	}
	if section.PaddingSize > 0 {
		padding := uint64(section.PaddingSize)
		if section.DataSize > padding {
			return section.DataSize - padding
		}
	}
	return section.DataSize
}

func parseVolumeSectionE01(data []byte, info *metadata.Info) {
	if info.Media == nil {
		info.Media = &metadata.MediaInfo{}
	}
	info.Media.MediaType = data[0]
	info.Media.NumberOfChunks = uint64(binary.LittleEndian.Uint32(data[4:8]))
	info.Media.SectorsPerChunk = binary.LittleEndian.Uint32(data[8:12])
	info.Media.BytesPerSector = binary.LittleEndian.Uint32(data[12:16])
	info.Media.NumberOfSectors = binary.LittleEndian.Uint64(data[16:24])
	info.Media.MediaFlags = data[36]
	info.Media.CompressionLevel = data[52]
	info.Media.ErrorGranularity = binary.LittleEndian.Uint32(data[56:60])
	copy(info.Media.SetIdentifier[:], data[64:80])
}

// parseVolumeSectionS01 parses the EWF-S01 (SMART) volume section (ewf_volume_smart_t, 94 bytes).
func parseVolumeSectionS01(data []byte, info *metadata.Info) {
	if info.Media == nil {
		info.Media = &metadata.MediaInfo{}
	}
	// unknown1[4] at 0 (reserved, skip)
	info.Media.NumberOfChunks = uint64(binary.LittleEndian.Uint32(data[4:8]))
	info.Media.SectorsPerChunk = binary.LittleEndian.Uint32(data[8:12])
	info.Media.BytesPerSector = binary.LittleEndian.Uint32(data[12:16])
	info.Media.NumberOfSectors = uint64(binary.LittleEndian.Uint32(data[16:20]))
}

func parseMD5LikeSection(majorVersion uint8, data []byte, info *metadata.Info) {
	if majorVersion == 1 {
		if len(data) >= ewfDigestSize {
			copy(info.MD5Digest[:], data[0:16])
			copy(info.SHA1Digest[:], data[16:36])
			info.HasMD5Digest = true
			info.HasSHA1Digest = true
			return
		}
		if len(data) >= ewfHashSize {
			copy(info.MD5Digest[:], data[0:16])
			info.HasMD5Digest = true
		}
		return
	}
	if len(data) >= ewfMD5DigestLength {
		copy(info.MD5Digest[:], data[0:ewfMD5DigestLength])
		info.HasMD5Digest = true
	}
}

func parseSHA1Section(majorVersion uint8, data []byte, info *metadata.Info) {
	_ = majorVersion // both versions place the digest at offset 0
	if len(data) >= ewfSHA1DigestLength {
		copy(info.SHA1Digest[:], data[0:ewfSHA1DigestLength])
		info.HasSHA1Digest = true
	}
}

// Session section sizes:
//
//	v1 header: 4(count) + 28(unknown) + 4(checksum) = 36 bytes
//	v1 entry:  4(flags) + 4(start_sector) + 24(unknown) = 32 bytes
//	v2 header: 4(count) + 12(unknown) + 4(checksum) + 12(padding) = 32 bytes
//	v2 entry:  8(start_sector) + 4(flags) + 20(unknown) = 32 bytes
const (
	sessionHeaderV1Size = 36
	sessionEntryV1Size  = 32
	sessionHeaderV2Size = 32
	sessionEntryV2Size  = 32
)

func parseSessionSection(majorVersion uint8, data []byte, info *metadata.Info) {
	if majorVersion == 1 {
		if len(data) < sessionHeaderV1Size {
			return
		}
		count := int(binary.LittleEndian.Uint32(data[0:4]))
		if count < 0 || count > (1<<20) {
			return
		}
		required := sessionHeaderV1Size + count*sessionEntryV1Size
		if len(data) < required {
			return
		}
		entries := make([]metadata.SessionEntry, 0, count)
		for i := 0; i < count; i++ {
			off := sessionHeaderV1Size + i*sessionEntryV1Size
			flags := binary.LittleEndian.Uint32(data[off : off+4])
			start := uint64(binary.LittleEndian.Uint32(data[off+4 : off+8]))
			entries = append(entries, metadata.SessionEntry{Flags: flags, StartSector: start})
		}
		info.Sessions = append(info.Sessions, entries...)
		return
	}
	// v2
	if len(data) < sessionHeaderV2Size {
		return
	}
	count := int(binary.LittleEndian.Uint32(data[0:4]))
	if count < 0 || count > (1<<20) {
		return
	}
	required := sessionHeaderV2Size + count*sessionEntryV2Size
	if len(data) < required {
		return
	}
	entries := make([]metadata.SessionEntry, 0, count)
	for i := 0; i < count; i++ {
		off := sessionHeaderV2Size + i*sessionEntryV2Size
		start := binary.LittleEndian.Uint64(data[off : off+8])
		flags := binary.LittleEndian.Uint32(data[off+8 : off+12])
		entries = append(entries, metadata.SessionEntry{Flags: flags, StartSector: start})
	}
	info.Sessions = append(info.Sessions, entries...)
}

// Error section sizes:
//
//	v1 header: 4(count) + 512(unknown) + 4(checksum) = 520 bytes
//	v1 entry:  4(start_sector) + 4(number_of_sectors) = 8 bytes
//	v2 header: 4(count) + 12(unknown) + 4(checksum) + 12(padding) = 32 bytes
//	v2 entry:  8(start_sector) + 4(number_of_sectors) + 4(padding) = 16 bytes
const (
	errorHeaderV1Size = 520
	errorEntryV1Size  = 8
	errorHeaderV2Size = 32
	errorEntryV2Size  = 16
)

func parseErrorSection(majorVersion uint8, data []byte, info *metadata.Info) {
	if majorVersion == 1 {
		if len(data) < errorHeaderV1Size {
			return
		}
		count := int(binary.LittleEndian.Uint32(data[0:4]))
		if count < 0 || count > (1<<20) {
			return
		}
		required := errorHeaderV1Size + count*errorEntryV1Size
		if len(data) < required {
			return
		}
		errs := make([]metadata.AcquisitionError, 0, count)
		for i := 0; i < count; i++ {
			off := errorHeaderV1Size + i*errorEntryV1Size
			start := uint64(binary.LittleEndian.Uint32(data[off : off+4]))
			nsectors := binary.LittleEndian.Uint32(data[off+4 : off+8])
			errs = append(errs, metadata.AcquisitionError{StartSector: start, NumberOfSectors: nsectors})
		}
		info.AcquisitionErrors = append(info.AcquisitionErrors, errs...)
		return
	}
	// v2
	if len(data) < errorHeaderV2Size {
		return
	}
	count := int(binary.LittleEndian.Uint32(data[0:4]))
	if count < 0 || count > (1<<20) {
		return
	}
	required := errorHeaderV2Size + count*errorEntryV2Size
	if len(data) < required {
		return
	}
	errs := make([]metadata.AcquisitionError, 0, count)
	for i := 0; i < count; i++ {
		off := errorHeaderV2Size + i*errorEntryV2Size
		start := binary.LittleEndian.Uint64(data[off : off+8])
		nsectors := binary.LittleEndian.Uint32(data[off+8 : off+12])
		errs = append(errs, metadata.AcquisitionError{StartSector: start, NumberOfSectors: nsectors})
	}
	info.AcquisitionErrors = append(info.AcquisitionErrors, errs...)
}
