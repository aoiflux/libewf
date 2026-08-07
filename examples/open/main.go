package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/aoiflux/libewf"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: go run ./examples/open <image.E01> [segment2.E02 ...]")
		fmt.Println("       one path is enough: the rest of the segment set is discovered")
		os.Exit(1)
	}

	r, err := open(os.Args[1:])
	if err != nil {
		fmt.Println("libewf open error:", err)
		os.Exit(1)
	}
	defer r.Close()

	m := r.Metadata()
	fmt.Printf("version=%d.%d segment=%d sections=%d\n", m.MajorVersion, m.MinorVersion, m.SegmentNumber, m.SectionCount)
	fmt.Printf("size=%d bytes sector=%d bytes\n", r.Size(), r.SectorSize())

	if m.Media != nil {
		fmt.Printf("chunks declared=%d observed=%d sectors/chunk=%d bytes/sector=%d sectors=%d\n",
			m.Media.NumberOfChunks,
			m.ObservedChunkCount,
			m.Media.SectorsPerChunk,
			m.Media.BytesPerSector,
			m.Media.NumberOfSectors,
		)
	}

	if a := m.Acquisition; a != nil {
		fmt.Printf("case=%q evidence=%q description=%q\n", a.CaseNumber, a.EvidenceNumber, a.Description)
		fmt.Printf("examiner=%q notes=%q\n", a.ExaminerName, a.Notes)
		fmt.Printf("tool=%q os=%q\n", a.SoftwareVersion, a.OperatingSystem)
		if a.Model != "" || a.SerialNumber != "" {
			fmt.Printf("device model=%q serial=%q\n", a.Model, a.SerialNumber)
		}
		if !a.AcquiryDate.IsZero() {
			fmt.Printf("acquired=%s\n", a.AcquiryDate.Format(time.RFC3339))
		} else if a.AcquiryDateRaw != "" {
			fmt.Printf("acquired=%q (unparsed)\n", a.AcquiryDateRaw)
		}
	}

	if m.HasMD5Digest {
		fmt.Printf("acquisition md5=%x\n", m.MD5Digest)
	}
	if m.HasSHA1Digest {
		fmt.Printf("acquisition sha1=%x\n", m.SHA1Digest)
	}
	if m.ChunkTablesRecovered > 0 {
		fmt.Printf("warning: %d chunk table(s) recovered from their table2 backup\n", m.ChunkTablesRecovered)
	}
	if m.ChunkTablesInvalid > 0 {
		fmt.Printf("warning: %d chunk table(s) failed checksum validation; data is unverified\n", m.ChunkTablesInvalid)
	}
	if len(m.AcquisitionErrors) > 0 {
		fmt.Printf("warning: %d sector range(s) were unreadable at acquisition time\n", len(m.AcquisitionErrors))
	}
}

// open takes the path form when it can, because OpenPath finds the rest of the
// segment set itself and closes what it opened. Several paths are still
// accepted, for a set whose files do not follow the EWF naming progression.
func open(paths []string) (libewf.Reader, error) {
	if len(paths) == 1 {
		return libewf.OpenPath(paths[0])
	}

	sources := make([]io.ReaderAt, 0, len(paths))
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		// The files outlive this function and are released when the process
		// exits, which for an example is honest enough.
		sources = append(sources, f)
	}
	return libewf.OpenSegments(sources)
}
