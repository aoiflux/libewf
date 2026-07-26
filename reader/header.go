package reader

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/aoiflux/libewf/compression"
	"github.com/aoiflux/libewf/metadata"
)

// headerPayloads collects the header sections found in a segment so they can be
// resolved in precedence order rather than in the order they happen to appear.
//
// An image commonly carries several: EnCase 6 writes "header2" twice plus a
// "header", and EWF-X adds an "xheader". They describe the same acquisition, so
// the most capable encoding wins rather than whichever came last.
type headerPayloads struct {
	header  []byte // zlib, 8-bit codepage, tab-delimited
	header2 []byte // zlib, UTF-16LE, tab-delimited
	xheader []byte // zlib, XML
}

func (h *headerPayloads) empty() bool {
	return h.header == nil && h.header2 == nil && h.xheader == nil
}

// resolveAcquisition decodes the collected header sections into Info.
//
// Precedence is header2, then header, then xheader. header2 is preferred
// because it is UTF-16 (so it cannot mangle non-ASCII case notes) and stores
// dates as POSIX timestamps rather than zone-less local time. Lower-precedence
// sources still fill fields the winner left empty.
func resolveAcquisition(payloads headerPayloads, info *metadata.Info) {
	if payloads.empty() {
		return
	}

	type candidate struct {
		name   string
		values map[string]string
	}
	var candidates []candidate

	if values := decodeHeaderText(payloads.header2, true); len(values) > 0 {
		candidates = append(candidates, candidate{"header2", values})
	}
	if values := decodeHeaderText(payloads.header, false); len(values) > 0 {
		candidates = append(candidates, candidate{"header", values})
	}
	if values := decodeXHeader(payloads.xheader); len(values) > 0 {
		candidates = append(candidates, candidate{"xheader", values})
	}
	if len(candidates) == 0 {
		return
	}

	acq := &metadata.Acquisition{
		Values: make(map[string]string),
		Source: candidates[0].name,
	}
	// Merge lowest precedence first so higher-precedence values overwrite.
	for i := len(candidates) - 1; i >= 0; i-- {
		for key, value := range candidates[i].values {
			if value == "" {
				continue
			}
			acq.Values[key] = value
		}
	}

	applyHeaderValues(acq)
	info.Acquisition = acq
}

// headerFields maps on-disk identifiers to the fields they populate. The short
// forms are used by header/header2; xheader spells them out.
var headerFields = map[string]func(*metadata.Acquisition, string){
	"c":                        func(a *metadata.Acquisition, v string) { a.CaseNumber = v },
	"case_number":              func(a *metadata.Acquisition, v string) { a.CaseNumber = v },
	"n":                        func(a *metadata.Acquisition, v string) { a.EvidenceNumber = v },
	"evidence_number":          func(a *metadata.Acquisition, v string) { a.EvidenceNumber = v },
	"a":                        func(a *metadata.Acquisition, v string) { a.Description = v },
	"description":              func(a *metadata.Acquisition, v string) { a.Description = v },
	"e":                        func(a *metadata.Acquisition, v string) { a.ExaminerName = v },
	"examiner_name":            func(a *metadata.Acquisition, v string) { a.ExaminerName = v },
	"t":                        func(a *metadata.Acquisition, v string) { a.Notes = v },
	"notes":                    func(a *metadata.Acquisition, v string) { a.Notes = v },
	"md":                       func(a *metadata.Acquisition, v string) { a.Model = v },
	"model":                    func(a *metadata.Acquisition, v string) { a.Model = v },
	"sn":                       func(a *metadata.Acquisition, v string) { a.SerialNumber = v },
	"serial_number":            func(a *metadata.Acquisition, v string) { a.SerialNumber = v },
	"l":                        func(a *metadata.Acquisition, v string) { a.DeviceLabel = v },
	"device_label":             func(a *metadata.Acquisition, v string) { a.DeviceLabel = v },
	"av":                       func(a *metadata.Acquisition, v string) { a.SoftwareVersion = v },
	"acquiry_software_version": func(a *metadata.Acquisition, v string) { a.SoftwareVersion = v },
	"ov":                       func(a *metadata.Acquisition, v string) { a.OperatingSystem = v },
	"acquiry_operating_system": func(a *metadata.Acquisition, v string) { a.OperatingSystem = v },
	"r":                        func(a *metadata.Acquisition, v string) { a.CompressionType = v },
	"compression_type":         func(a *metadata.Acquisition, v string) { a.CompressionType = v },
	"p":                        setPasswordHash,
	"password":                 setPasswordHash,
	"pid":                      func(a *metadata.Acquisition, v string) { a.ProcessIdentifier = v },
	"process_identifier":       func(a *metadata.Acquisition, v string) { a.ProcessIdentifier = v },
}

// setPasswordHash records a password hash, treating the "0" that writers use to
// mean "no password" as absent rather than as a hash of zero.
func setPasswordHash(a *metadata.Acquisition, v string) {
	if v == "0" {
		return
	}
	a.PasswordHash = v
}

func applyHeaderValues(acq *metadata.Acquisition) {
	for key, value := range acq.Values {
		if apply, ok := headerFields[key]; ok {
			apply(acq, value)
		}
	}

	for _, key := range []string{"m", "acquiry_date"} {
		if raw, ok := acq.Values[key]; ok && raw != "" {
			acq.AcquiryDateRaw = raw
			if parsed, ok := parseHeaderTime(raw); ok {
				acq.AcquiryDate = parsed
			}
			break
		}
	}
	for _, key := range []string{"u", "system_date"} {
		if raw, ok := acq.Values[key]; ok && raw != "" {
			acq.SystemDateRaw = raw
			if parsed, ok := parseHeaderTime(raw); ok {
				acq.SystemDate = parsed
			}
			break
		}
	}
}

// decodeHeaderText inflates a header section and returns the identifier/value
// pairs from its "main" block.
//
// A header is a sequence of named blocks. Only "main" carries acquisition
// provenance; EnCase 5 and later add "srce" and "sub" blocks describing the
// source device in a nested table form that is not decoded here.
func decodeHeaderText(payload []byte, utf16LE bool) map[string]string {
	if len(payload) == 0 {
		return nil
	}
	raw, err := compression.DecompressZlib(payload)
	if err != nil {
		// Some writers store the header uncompressed.
		raw = payload
	}

	var text string
	if utf16LE {
		text = decodeUTF16LE(raw)
	} else {
		// The 8-bit variant is written in a codepage that defaults to ASCII.
		// Latin-1 maps every byte to a rune, so no byte is ever lost even if
		// the true codepage differs; a mismatch shows up as odd accents rather
		// than dropped data or an error.
		text = decodeLatin1(raw)
	}
	return parseHeaderMainBlock(text)
}

func parseHeaderMainBlock(text string) map[string]string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	for i, line := range lines {
		if strings.TrimSpace(line) != "main" {
			continue
		}
		// The two lines after the block name are the identifiers and their
		// values, both tab-delimited.
		if i+2 >= len(lines) {
			return nil
		}
		identifiers := strings.Split(lines[i+1], "\t")
		values := strings.Split(lines[i+2], "\t")

		out := make(map[string]string, len(identifiers))
		for j, identifier := range identifiers {
			identifier = strings.TrimSpace(identifier)
			if identifier == "" {
				continue
			}
			value := ""
			if j < len(values) {
				value = strings.TrimSpace(values[j])
			}
			out[identifier] = value
		}
		return out
	}
	return nil
}

// decodeXHeader extracts element names and values from an xheader document.
func decodeXHeader(payload []byte) map[string]string {
	if len(payload) == 0 {
		return nil
	}
	doc, ok := inflateXML(payload)
	if !ok {
		return nil
	}
	return extractXMLValues(doc, "xheader")
}

func decodeUTF16LE(raw []byte) string {
	raw = bytes.TrimPrefix(raw, []byte{0xFF, 0xFE})
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(raw[i:i+2]))
	}
	return string(utf16.Decode(units))
}

func decodeLatin1(raw []byte) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, c := range raw {
		b.WriteRune(rune(c))
	}
	return b.String()
}

// headerTimeLayouts covers the textual forms writers use. The zone-less ones are
// interpreted in the local zone, matching how the writer recorded them.
var headerTimeLayouts = []string{
	"Mon Jan 2 15:04:05 2006 MST",
	"Mon Jan _2 15:04:05 2006 MST",
	"Mon Jan 2 15:04:05 2006",
	"Mon Jan _2 15:04:05 2006",
	"1/2/2006 15:04:05",
	"01/02/2006 15:04:05",
	"2006-01-02 15:04:05",
	time.RFC3339,
}

// parseHeaderTime interprets the three date encodings EWF headers use:
// a POSIX timestamp (header2), six space-separated numbers (header), and a
// ctime-like string (xheader).
func parseHeaderTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}

	// header2: seconds since the Unix epoch.
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		// Guard against a bare year or another small number being read as an
		// instant seconds after 1970.
		if seconds > 100000000 {
			return time.Unix(seconds, 0), true
		}
		return time.Time{}, false
	}

	// header: "2026 7 26 10 36 27" as year month day hour minute second.
	if fields := strings.Fields(raw); len(fields) == 6 {
		numbers := make([]int, 0, 6)
		for _, field := range fields {
			n, err := strconv.Atoi(field)
			if err != nil {
				numbers = nil
				break
			}
			numbers = append(numbers, n)
		}
		if len(numbers) == 6 {
			return time.Date(numbers[0], time.Month(numbers[1]), numbers[2],
				numbers[3], numbers[4], numbers[5], 0, time.Local), true
		}
	}

	// xheader and assorted writer-specific spellings.
	for _, layout := range headerTimeLayouts {
		if parsed, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}
