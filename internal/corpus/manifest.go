// Package corpus describes the golden-image corpus used to validate the
// reader against images produced by real acquisition tools.
//
// The corpus exists because synthetic test images can only prove that the
// reader agrees with the test author's understanding of the format. Only
// images written by EnCase, FTK Imager, Guymager or ewfacquire can prove it
// agrees with the format as deployed.
//
// Every expected value in a manifest entry must come from a source
// independent of this library: the raw image that was acquired, the
// parameters the acquisition was run with, or the digests the acquisition
// tool embedded. Recording what this library reports and asserting it later
// proves nothing.
package corpus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ManifestName is the file name of the manifest within the corpus directory.
const ManifestName = "manifest.json"

// Oracle names the independent source of truth for an entry.
type Oracle string

const (
	// OracleRawSource means the entry was produced by acquiring a raw image
	// whose SHA-256 is recorded in DecodedSHA256. The decoded device must
	// reproduce those bytes exactly. This is the strongest oracle.
	OracleRawSource Oracle = "raw-source"

	// OracleStoredDigest means the raw source is unavailable, so the check
	// relies on the acquisition digests embedded in the image. The
	// acquisition tool computed them from the original device, so
	// reproducing them from the decoded stream still validates decoding.
	OracleStoredDigest Oracle = "stored-digest"
)

// Entry describes one corpus image and what a correct reader must produce
// from it.
type Entry struct {
	// Name identifies the entry in test output.
	Name string `json:"name"`

	// Segments lists the segment files in acquisition order, relative to the
	// corpus directory.
	Segments []string `json:"segments"`

	// Oracle names where the expected values came from.
	Oracle Oracle `json:"oracle"`

	// Producer records the tool and arguments that created the image, so a
	// failure can be traced to a specific writer dialect.
	Producer string `json:"producer,omitempty"`

	// Notes records anything unusual about the entry.
	Notes string `json:"notes,omitempty"`

	// Size is the logical device size in bytes. Zero means unknown.
	Size int64 `json:"size,omitempty"`

	// SectorSize is the logical sector size in bytes. Zero means unknown.
	SectorSize int `json:"sectorSize,omitempty"`

	// DecodedSHA256 is the hex SHA-256 of the whole decoded device. Set only
	// for OracleRawSource entries, where it is the digest of the raw image
	// that was acquired.
	DecodedSHA256 string `json:"decodedSHA256,omitempty"`

	// ExpectStoredMD5 and ExpectStoredSHA1 are the hex acquisition digests
	// the image is expected to carry. Empty means "do not check".
	ExpectStoredMD5  string `json:"expectStoredMD5,omitempty"`
	ExpectStoredSHA1 string `json:"expectStoredSHA1,omitempty"`

	// RequireVerifyOK asserts that Verify reproduces every stored digest
	// with no undecodable spans.
	RequireVerifyOK bool `json:"requireVerifyOK,omitempty"`

	// ExpectAcquisition holds the provenance the image should report. For
	// generated entries these are the values passed to the acquisition tool on
	// its command line, which makes them an oracle no decode can influence.
	ExpectAcquisition *ExpectedAcquisition `json:"expectAcquisition,omitempty"`

	// ExpectIncomplete marks an entry that is deliberately missing segments,
	// so opening it must fail rather than decode a truncated device.
	ExpectIncomplete bool `json:"expectIncomplete,omitempty"`
}

// ExpectedAcquisition is the provenance a correct reader must recover from an
// image's header sections. An empty field means "do not check".
type ExpectedAcquisition struct {
	CaseNumber     string `json:"caseNumber,omitempty"`
	EvidenceNumber string `json:"evidenceNumber,omitempty"`
	Description    string `json:"description,omitempty"`
	ExaminerName   string `json:"examinerName,omitempty"`
	Notes          string `json:"notes,omitempty"`

	// AcquiryDateUnix is the acquisition instant in seconds since the epoch.
	// Zero means "do not check". Header sections store this three different
	// ways, so pinning it catches a date decoded from the wrong encoding.
	//
	// It applies only to images whose stored date carries a timezone — a POSIX
	// timestamp, as header2 and case_data write. A date stored as six
	// space-separated numbers or as a ctime-like string denotes no instant at
	// all until a zone is assumed, so asserting one would only hold in the
	// timezone the corpus was generated in. Use AcquiryDateWall for those.
	AcquiryDateUnix int64 `json:"acquiryDateUnix,omitempty"`

	// AcquiryDateWall is the acquisition wall clock, "2006-01-02 15:04:05",
	// for images whose stored date carries no timezone. Empty means
	// "do not check".
	//
	// It is the same reading as AcquiryDateUnix, written the way a zone-less
	// date can actually be checked: the reader interprets the stored fields in
	// the local zone, so the wall clock it reports must be the one the writer
	// stored, on any machine. Comparing that still catches a date taken from
	// the wrong section or with its fields transposed, which is what this
	// expectation is for.
	AcquiryDateWall string `json:"acquiryDateWall,omitempty"`
}

// AcquiryDateWallLayout is the format of ExpectedAcquisition.AcquiryDateWall.
const AcquiryDateWallLayout = "2006-01-02 15:04:05"

// Manifest is the corpus index.
type Manifest struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Load reads the manifest from dir. It reports whether the manifest exists so
// callers can skip rather than fail when no corpus is present.
func Load(dir string) (*Manifest, bool, error) {
	path := filepath.Join(dir, ManifestName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, true, fmt.Errorf("corpus: parsing %s: %w", path, err)
	}
	if m.Version != 1 {
		return nil, true, fmt.Errorf("corpus: unsupported manifest version %d", m.Version)
	}
	return &m, true, nil
}

// Save writes the manifest to dir, sorting entries so the file is stable
// across regenerations.
func Save(dir string, m *Manifest) error {
	sort.Slice(m.Entries, func(i, j int) bool { return m.Entries[i].Name < m.Entries[j].Name })

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dir, ManifestName), data, 0o644)
}

// Validate reports whether an entry is internally coherent, catching manifests
// that would silently assert nothing.
func (e Entry) Validate() error {
	if e.Name == "" {
		return fmt.Errorf("corpus: entry has no name")
	}
	if len(e.Segments) == 0 {
		return fmt.Errorf("corpus: entry %q lists no segments", e.Name)
	}
	if e.ExpectIncomplete {
		return nil
	}
	switch e.Oracle {
	case OracleRawSource:
		if e.DecodedSHA256 == "" {
			return fmt.Errorf("corpus: entry %q uses the raw-source oracle but records no decodedSHA256", e.Name)
		}
	case OracleStoredDigest:
		if e.ExpectStoredMD5 == "" && e.ExpectStoredSHA1 == "" {
			return fmt.Errorf("corpus: entry %q uses the stored-digest oracle but records no expected digest", e.Name)
		}
		if !e.RequireVerifyOK {
			return fmt.Errorf("corpus: entry %q uses the stored-digest oracle but does not require Verify to succeed, so it checks nothing", e.Name)
		}
	default:
		return fmt.Errorf("corpus: entry %q has unknown oracle %q", e.Name, e.Oracle)
	}
	return nil
}
