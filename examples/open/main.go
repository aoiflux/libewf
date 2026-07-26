package main

import (
	"fmt"
	"io"
	"os"

	"github.com/aoiflux/libewf"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: go run ./examples/open <segment1.E01> [segment2.E02 ...]")
		os.Exit(1)
	}

	files := make([]*os.File, 0, len(os.Args)-1)
	sources := make([]io.ReaderAt, 0, len(os.Args)-1)
	for _, path := range os.Args[1:] {
		f, err := os.Open(path)
		if err != nil {
			fmt.Println("open file error:", err)
			os.Exit(1)
		}
		files = append(files, f)
		sources = append(sources, f)
	}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()

	var (
		r   libewf.Reader
		err error
	)
	if len(sources) == 1 {
		r, err = libewf.Open(sources[0])
	} else {
		r, err = libewf.OpenSegments(sources)
	}
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
