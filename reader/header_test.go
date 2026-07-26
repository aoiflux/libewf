package reader

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func zlibBytes(raw []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(raw)
	_ = w.Close()
	return buf.Bytes()
}

// utf16LEWithBOM encodes text the way the header2 section stores it.
func utf16LEWithBOM(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 0, 2+len(units)*2)
	out = append(out, 0xFF, 0xFE)
	for _, u := range units {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return out
}

// headerBlock assembles the block layout EWF headers use: a count, a block
// name, then tab-delimited identifiers and values.
func headerBlock(identifiers, values []string) string {
	return strings.Join([]string{
		"1",
		"main",
		strings.Join(identifiers, "\t"),
		strings.Join(values, "\t"),
		"",
		"",
	}, "\n")
}

// TestHeaderSectionLatin1 covers the 8-bit "header" section, whose date is six
// space-separated numbers in local time.
func TestHeaderSectionLatin1(t *testing.T) {
	text := headerBlock(
		[]string{"c", "n", "a", "e", "t", "av", "ov", "m", "u", "p", "r"},
		[]string{"CASE-7", "EV-3", "suspect disk", "A. Examiner", "seized at scene",
			"20140814", "Linux", "2026 7 26 10 36 25", "2026 7 26 10 36 25", "0", "b"},
	)

	spec := defaultV1([]byte("AAAAAAAA"))
	spec.extras = []extraSection{{name: "header", payload: zlibBytes([]byte(text))}}

	acq := mustOpen(t, spec.build()).Metadata().Acquisition
	if acq == nil {
		t.Fatal("Acquisition is nil, want decoded provenance")
	}

	if got, want := acq.Source, "header"; got != want {
		t.Errorf("Source = %q, want %q", got, want)
	}
	if got, want := acq.CaseNumber, "CASE-7"; got != want {
		t.Errorf("CaseNumber = %q, want %q", got, want)
	}
	if got, want := acq.EvidenceNumber, "EV-3"; got != want {
		t.Errorf("EvidenceNumber = %q, want %q", got, want)
	}
	// The identifier order differs between header and header2, so a positional
	// mapping would swap description and case number here.
	if got, want := acq.Description, "suspect disk"; got != want {
		t.Errorf("Description = %q, want %q", got, want)
	}
	if got, want := acq.ExaminerName, "A. Examiner"; got != want {
		t.Errorf("ExaminerName = %q, want %q", got, want)
	}
	if got, want := acq.Notes, "seized at scene"; got != want {
		t.Errorf("Notes = %q, want %q", got, want)
	}
	if got, want := acq.CompressionType, "b"; got != want {
		t.Errorf("CompressionType = %q, want %q", got, want)
	}
	// "0" is the writers' sentinel for "no password", not a hash.
	if acq.PasswordHash != "" {
		t.Errorf("PasswordHash = %q, want empty for the \"0\" sentinel", acq.PasswordHash)
	}

	want := time.Date(2026, time.July, 26, 10, 36, 25, 0, time.Local)
	if !acq.AcquiryDate.Equal(want) {
		t.Errorf("AcquiryDate = %s, want %s", acq.AcquiryDate, want)
	}
	if got, want := acq.AcquiryDateRaw, "2026 7 26 10 36 25"; got != want {
		t.Errorf("AcquiryDateRaw = %q, want %q", got, want)
	}
}

// TestHeader2SectionUTF16 covers the UTF-16 "header2" section, whose date is a
// POSIX timestamp, and confirms non-ASCII survives.
func TestHeader2SectionUTF16(t *testing.T) {
	text := headerBlock(
		[]string{"a", "c", "n", "e", "t", "md", "sn", "av", "ov", "m", "u", "p", "dc"},
		[]string{"disque", "CASE-9", "EV-1", "Émile Zöller", "notes with é",
			"ST1000DM010", "Z1234ABC", "20140814", "Linux", "1785076587", "1785076587", "0", ""},
	)

	spec := defaultV1([]byte("AAAAAAAA"))
	spec.extras = []extraSection{{name: "header2", payload: zlibBytes(utf16LEWithBOM(text))}}

	acq := mustOpen(t, spec.build()).Metadata().Acquisition
	if acq == nil {
		t.Fatal("Acquisition is nil, want decoded provenance")
	}

	if got, want := acq.Source, "header2"; got != want {
		t.Errorf("Source = %q, want %q", got, want)
	}
	if got, want := acq.ExaminerName, "Émile Zöller"; got != want {
		t.Errorf("ExaminerName = %q, want %q: UTF-16 text must survive decoding", got, want)
	}
	if got, want := acq.Notes, "notes with é"; got != want {
		t.Errorf("Notes = %q, want %q", got, want)
	}
	if got, want := acq.Model, "ST1000DM010"; got != want {
		t.Errorf("Model = %q, want %q", got, want)
	}
	if got, want := acq.SerialNumber, "Z1234ABC"; got != want {
		t.Errorf("SerialNumber = %q, want %q", got, want)
	}
	if got, want := acq.AcquiryDate.Unix(), int64(1785076587); got != want {
		t.Errorf("AcquiryDate.Unix() = %d, want %d", got, want)
	}
}

// TestXHeaderSection covers the XML variant used by EWF-X.
func TestXHeaderSection(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<xheader>
	<case_number>CASE-X</case_number>
	<examiner_name>X. Examiner</examiner_name>
	<acquiry_date>Sun Jul 26 10:36:24 2026 EDT</acquiry_date>
</xheader>`

	spec := defaultV1([]byte("AAAAAAAA"))
	spec.extras = []extraSection{{name: "xheader", payload: zlibBytes([]byte("\xef\xbb\xbf" + doc))}}

	acq := mustOpen(t, spec.build()).Metadata().Acquisition
	if acq == nil {
		t.Fatal("Acquisition is nil, want decoded provenance")
	}
	if got, want := acq.CaseNumber, "CASE-X"; got != want {
		t.Errorf("CaseNumber = %q, want %q", got, want)
	}
	if got, want := acq.ExaminerName, "X. Examiner"; got != want {
		t.Errorf("ExaminerName = %q, want %q", got, want)
	}
	if acq.AcquiryDate.IsZero() {
		t.Errorf("AcquiryDate is zero, want the ctime-style value parsed (raw %q)", acq.AcquiryDateRaw)
	}
}

// TestHeaderPrecedence pins the resolution order. header2 is preferred because
// it is UTF-16 and stores an unambiguous timestamp, but a lower-precedence
// section still fills fields the winner left empty.
func TestHeaderPrecedence(t *testing.T) {
	header2 := headerBlock(
		[]string{"c", "e", "m"},
		[]string{"FROM-HEADER2", "", "1785076587"},
	)
	header := headerBlock(
		[]string{"c", "e", "m"},
		[]string{"FROM-HEADER", "only-in-header", "2026 7 26 10 36 25"},
	)

	spec := defaultV1([]byte("AAAAAAAA"))
	spec.extras = []extraSection{
		// Deliberately file-ordered with the lower-precedence section last, to
		// prove resolution does not depend on position.
		{name: "header2", payload: zlibBytes(utf16LEWithBOM(header2))},
		{name: "header", payload: zlibBytes([]byte(header))},
	}

	acq := mustOpen(t, spec.build()).Metadata().Acquisition
	if acq == nil {
		t.Fatal("Acquisition is nil")
	}
	if got, want := acq.Source, "header2"; got != want {
		t.Errorf("Source = %q, want %q", got, want)
	}
	if got, want := acq.CaseNumber, "FROM-HEADER2"; got != want {
		t.Errorf("CaseNumber = %q, want %q: header2 must win", got, want)
	}
	if got, want := acq.ExaminerName, "only-in-header"; got != want {
		t.Errorf("ExaminerName = %q, want %q: a gap in header2 should be filled from header", got, want)
	}
	if got, want := acq.AcquiryDate.Unix(), int64(1785076587); got != want {
		t.Errorf("AcquiryDate.Unix() = %d, want %d from header2", got, want)
	}
}

// TestHeaderIgnoresSourceBlocks checks that the srce and sub blocks EnCase 6
// appends do not disturb parsing of main. Their nested table layout differs, so
// a parser that assumed fixed line offsets would misread them.
func TestHeaderIgnoresSourceBlocks(t *testing.T) {
	text := strings.Join([]string{
		"3",
		"main",
		"a\tc\tn\te\tt\tm",
		"desc\tCASE-1\tEV-1\texaminer\tnotes\t1785076587",
		"",
		"srce",
		"0\t1",
		"p\tn\tid\tev\ttb\tlo\tpo\tah\tgu\taq",
		"0\t0",
		"\t\t\t\t\t-1\t-1\t\t\t",
		"",
		"sub",
		"0\t1",
		"p\tn\tid\tnu\tco\tgu",
		"0\t0",
		"\t\t\t\t1\t",
		"",
		"",
	}, "\n")

	spec := defaultV1([]byte("AAAAAAAA"))
	spec.extras = []extraSection{{name: "header2", payload: zlibBytes(utf16LEWithBOM(text))}}

	acq := mustOpen(t, spec.build()).Metadata().Acquisition
	if acq == nil {
		t.Fatal("Acquisition is nil")
	}
	if got, want := acq.CaseNumber, "CASE-1"; got != want {
		t.Errorf("CaseNumber = %q, want %q", got, want)
	}
	if got, want := acq.Description, "desc"; got != want {
		t.Errorf("Description = %q, want %q", got, want)
	}
	// Identifiers from srce/sub must not leak in as main values.
	if _, leaked := acq.Values["gu"]; leaked {
		t.Errorf("Values contains %q from the srce/sub blocks, want only main", "gu")
	}
}

func TestNoHeaderSectionYieldsNilAcquisition(t *testing.T) {
	if acq := mustOpen(t, defaultV1([]byte("AAAAAAAA")).build()).Metadata().Acquisition; acq != nil {
		t.Fatalf("Acquisition = %+v, want nil when the image has no header section", acq)
	}
}

func TestMalformedHeaderIsIgnored(t *testing.T) {
	for name, payload := range map[string][]byte{
		"truncated zlib":  zlibBytes([]byte(headerBlock([]string{"c"}, []string{"X"})))[:6],
		"no main block":   zlibBytes([]byte("1\nsrce\np\tn\n0\t0\n")),
		"main at the end": zlibBytes([]byte("1\nmain")),
		"empty":           zlibBytes(nil),
		"binary garbage":  bytes.Repeat([]byte{0x7F}, 64),
	} {
		t.Run(name, func(t *testing.T) {
			spec := defaultV1([]byte("AAAAAAAA"))
			spec.extras = []extraSection{{name: "header", payload: payload}}

			// Must not panic, and must not invent provenance.
			acq := mustOpen(t, spec.build()).Metadata().Acquisition
			if acq != nil && acq.CaseNumber != "" {
				t.Fatalf("CaseNumber = %q, want empty for %s", acq.CaseNumber, name)
			}
		})
	}
}

func TestParseHeaderTime(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		want  time.Time
		valid bool
	}{
		{"posix timestamp", "1785076587", time.Unix(1785076587, 0), true},
		{
			"six space separated numbers",
			"2026 7 26 10 36 25",
			time.Date(2026, time.July, 26, 10, 36, 25, 0, time.Local),
			true,
		},
		{"ctime with zone", "Sun Jul 26 10:36:24 2026 UTC", time.Time{}, true},
		{"us slash format", "7/26/2026 10:36:25", time.Date(2026, time.July, 26, 10, 36, 25, 0, time.Local), true},
		{"iso like", "2026-07-26 10:36:25", time.Date(2026, time.July, 26, 10, 36, 25, 0, time.Local), true},
		{"empty", "", time.Time{}, false},
		{"not a date", "sometime last tuesday", time.Time{}, false},
		// A bare year must not be mistaken for an instant just after the epoch.
		{"bare year", "2026", time.Time{}, false},
		{"wrong field count", "2026 7 26", time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseHeaderTime(tc.raw)
			if ok != tc.valid {
				t.Fatalf("parseHeaderTime(%q) ok = %v, want %v", tc.raw, ok, tc.valid)
			}
			if !tc.valid || tc.want.IsZero() {
				return
			}
			if !got.Equal(tc.want) {
				t.Fatalf("parseHeaderTime(%q) = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}

// TestMetadataAcquisitionIsCopied ensures a caller cannot reach into the
// reader's own state through the returned map.
func TestMetadataAcquisitionIsCopied(t *testing.T) {
	text := headerBlock([]string{"c"}, []string{"CASE-1"})
	spec := defaultV1([]byte("AAAAAAAA"))
	spec.extras = []extraSection{{name: "header", payload: zlibBytes([]byte(text))}}

	r := mustOpen(t, spec.build())
	first := r.Metadata()
	first.Acquisition.Values["c"] = "TAMPERED"
	first.Acquisition.CaseNumber = "TAMPERED"

	second := r.Metadata()
	if got := second.Acquisition.Values["c"]; got != "CASE-1" {
		t.Errorf("Values[\"c\"] = %q, want %q: the map must be copied per call", got, "CASE-1")
	}
	if got := second.Acquisition.CaseNumber; got != "CASE-1" {
		t.Errorf("CaseNumber = %q, want %q", got, "CASE-1")
	}
}
