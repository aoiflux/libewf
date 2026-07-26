package reader

import (
	"strconv"

	"github.com/aoiflux/libewf/metadata"
	"github.com/aoiflux/libewf/types"
)

// EWF2 replaces the fixed-layout v1 "volume" section with two text sections,
// device_information and case_data. Both are zlib-compressed UTF-16LE documents
// in the same block format as the v1 "header2" section, so they reuse its
// decoder; only the identifiers differ.
//
// Geometry is split across the two: device_information carries the sector size
// and count, case_data the sectors per chunk. Both are needed before a device
// can be read, which is why an image missing either has no usable size.

// parseDeviceInformationV2 decodes the EWF2 device_information section.
func parseDeviceInformationV2(data []byte, info *metadata.Info) {
	values := decodeHeaderText(data, true)
	if len(values) == 0 {
		return
	}
	media := ensureMedia(info)

	if v, ok := parseUint(values["bp"]); ok {
		media.BytesPerSector = uint32(v)
	}
	if v, ok := parseUint(values["ts"]); ok {
		media.NumberOfSectors = v
	}
	if mediaType, ok := ewf2MediaType(values["dt"]); ok {
		media.MediaType = mediaType
	}
	// "ph" marks the source as a physical device rather than a logical volume.
	if values["ph"] == "1" {
		media.MediaFlags |= types.MediaFlagPhysical
	}

	acq := ensureAcquisition(info)
	setIfEmpty(&acq.Model, values["md"])
	setIfEmpty(&acq.SerialNumber, values["sn"])
	setIfEmpty(&acq.DeviceLabel, values["lb"])
	setIfEmpty(&acq.ProcessIdentifier, values["pid"])
	mergeValues(acq, values)
}

// parseCaseDataV2 decodes the EWF2 case_data section.
func parseCaseDataV2(data []byte, info *metadata.Info) {
	values := decodeHeaderText(data, true)
	if len(values) == 0 {
		return
	}
	media := ensureMedia(info)

	if v, ok := parseUint(values["sb"]); ok {
		media.SectorsPerChunk = uint32(v)
	}
	if v, ok := parseUint(values["tb"]); ok {
		media.NumberOfChunks = v
	}
	if v, ok := parseUint(values["gr"]); ok {
		media.ErrorGranularity = uint32(v)
	}

	acq := ensureAcquisition(info)
	acq.Source = "case_data"
	setIfEmpty(&acq.Description, values["nm"])
	setIfEmpty(&acq.CaseNumber, values["cn"])
	setIfEmpty(&acq.EvidenceNumber, values["en"])
	setIfEmpty(&acq.ExaminerName, values["ex"])
	setIfEmpty(&acq.Notes, values["nt"])
	setIfEmpty(&acq.SoftwareVersion, values["av"])
	setIfEmpty(&acq.OperatingSystem, values["os"])
	setIfEmpty(&acq.CompressionType, values["cp"])

	// "at" is the acquisition date and "tt" the acquiring host's clock, both as
	// POSIX timestamps. The names are easy to transpose, so they are taken
	// straight from libewf's own parser rather than inferred.
	if raw := values["at"]; raw != "" {
		acq.AcquiryDateRaw = raw
		if when, ok := parseHeaderTime(raw); ok {
			acq.AcquiryDate = when
		}
	}
	if raw := values["tt"]; raw != "" {
		acq.SystemDateRaw = raw
		if when, ok := parseHeaderTime(raw); ok {
			acq.SystemDate = when
		}
	}
	mergeValues(acq, values)
}

// ewf2MediaType maps the single-letter device type EWF2 stores.
func ewf2MediaType(value string) (uint8, bool) {
	switch value {
	case "f":
		return types.MediaTypeFixed, true
	case "r":
		return types.MediaTypeRemovable, true
	case "c":
		return types.MediaTypeOptical, true
	case "l":
		return types.MediaTypeSingleFiles, true
	case "m":
		return types.MediaTypeMemory, true
	default:
		// "a" (RAM disk) and "p" (Palm) have no constant here; the raw letter
		// stays available in Acquisition.Values.
		return 0, false
	}
}

func ensureMedia(info *metadata.Info) *metadata.MediaInfo {
	if info.Media == nil {
		info.Media = &metadata.MediaInfo{}
	}
	return info.Media
}

// ensureAcquisition returns the shared Acquisition, creating it on first use.
// device_information and case_data each hold part of the provenance, so they
// contribute to one record rather than replacing each other's.
func ensureAcquisition(info *metadata.Info) *metadata.Acquisition {
	if info.Acquisition == nil {
		info.Acquisition = &metadata.Acquisition{
			Values: make(map[string]string),
			Source: "device_information",
		}
	}
	if info.Acquisition.Values == nil {
		info.Acquisition.Values = make(map[string]string)
	}
	return info.Acquisition
}

func setIfEmpty(target *string, value string) {
	if *target == "" && value != "" {
		*target = value
	}
}

func mergeValues(acq *metadata.Acquisition, values map[string]string) {
	for key, value := range values {
		if value == "" {
			continue
		}
		if _, exists := acq.Values[key]; !exists {
			acq.Values[key] = value
		}
	}
}

func parseUint(value string) (uint64, bool) {
	if value == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
