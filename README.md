# libewf

libewf is a pure Go library for reading Expert Witness Format (EWF) disk images.

It is designed to be easy to embed in forensic tools, scripts, and analysis
pipelines.

## Features

- Pure Go (no CGO dependency)
- Root import API: `github.com/aoiflux/libewf`
- EWF format support:
  - EWF v1 (EVF) — `.E01` physical images
  - EWF v1 Logical (LVF) — `.L01` logical images
  - EWF v2 (EVF2) — `.Ex01` physical images
  - EWF v2 Logical (LEF2) — `.Lx01` logical images
- Multi-segment image support (`OpenSegments`)
- Chunk decompression:
  - None (method 0)
  - Deflate / zlib (method 1)
  - bzip2 (method 2)
- Rich image metadata:
  - Version, segment number, section inventory
  - Media geometry (chunks, sectors per chunk, bytes per sector, total sectors)
  - MD5 and SHA1 digest fields
  - Session entries and acquisition error ranges
- `io.ReaderAt`-compatible decoded stream for random-access reads

## Install

```
go get github.com/aoiflux/libewf
```

## Quick Start

### Single segment

```go
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/aoiflux/libewf"
)

func main() {
	f, err := os.Open("image.E01")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	r, err := libewf.Open(f)
	if err != nil {
		panic(err)
	}
	defer r.Close()

	m := r.Metadata()
	fmt.Printf("version=%d.%d segment=%d sections=%d\n",
		m.MajorVersion, m.MinorVersion, m.SegmentNumber, m.SectionCount)

	if m.Media != nil {
		fmt.Printf("chunks=%d sectors/chunk=%d bytes/sector=%d total_sectors=%d\n",
			m.Media.NumberOfChunks,
			m.Media.SectorsPerChunk,
			m.Media.BytesPerSector,
			m.Media.NumberOfSectors,
		)
	}

	buf := make([]byte, 512)
	n, err := r.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		panic(err)
	}
	fmt.Printf("read %d bytes from offset 0\n", n)
}
```

### Multiple segments

```go
sources := make([]io.ReaderAt, 0)
for _, path := range []string{"image.E01", "image.E02", "image.E03"} {
	f, _ := os.Open(path)
	defer f.Close()
	sources = append(sources, f)
}

r, err := libewf.OpenSegments(sources)
if err != nil {
	panic(err)
}
defer r.Close()
```

## API

### Open functions

| Function                                              | Description                                                                         |
| ----------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `Open(source io.ReaderAt) (Reader, error)`            | Open a single EWF segment                                                           |
| `OpenSegments(sources []io.ReaderAt) (Reader, error)` | Open a multi-segment EWF image; segments are sorted by segment number automatically |
| `Create(w io.Writer) (Writer, error)`                 | Prepare an EWF writer _(not yet implemented)_                                       |

### Reader interface

```go
type Reader interface {
    ReadAt(p []byte, off int64) (n int, err error)
    Metadata() metadata.Info
    Close() error
}
```

`ReadAt` decodes and decompresses chunks on demand and exposes the raw logical
stream at any byte offset.

## Metadata Model

`metadata.Info` is returned by `Reader.Metadata()` after opening a segment.

| Field                               | Type                 | Description                                       |
| ----------------------------------- | -------------------- | ------------------------------------------------- |
| `MajorVersion` / `MinorVersion`     | `uint8`              | EWF format version                                |
| `SegmentNumber`                     | `uint32`             | Segment index in the set                          |
| `SectionCount`                      | `int`                | Total number of sections parsed                   |
| `HasNextSection` / `HasDoneSection` | `bool`               | Segment chain flags                               |
| `IsEncrypted`                       | `bool`               | Whether the image is encrypted                    |
| `HasIntegrityHashBlocks`            | `bool`               | Whether hash blocks are present                   |
| `SectionTypeCounts`                 | `map[uint32]int`     | Count of each section type code                   |
| `Sections`                          | `[]Section`          | Ordered section descriptor list                   |
| `Media`                             | `*MediaInfo`         | Media geometry and acquisition parameters         |
| `HasMD5Digest` / `MD5Digest`        | `bool` / `[16]byte`  | MD5 integrity digest                              |
| `HasSHA1Digest` / `SHA1Digest`      | `bool` / `[20]byte`  | SHA1 integrity digest                             |
| `Sessions`                          | `[]SessionEntry`     | Session table entries                             |
| `AcquisitionErrors`                 | `[]AcquisitionError` | Sectors that could not be read during acquisition |

### MediaInfo

| Field              | Type       | Description                               |
| ------------------ | ---------- | ----------------------------------------- |
| `MediaType`        | `uint8`    | Physical / logical / optical / memory     |
| `NumberOfChunks`   | `uint64`   | Total chunk count                         |
| `SectorsPerChunk`  | `uint32`   | Sectors packed per chunk                  |
| `BytesPerSector`   | `uint32`   | Sector size in bytes                      |
| `NumberOfSectors`  | `uint64`   | Total logical sectors                     |
| `MediaFlags`       | `uint8`    | Removable / physical / logical flags      |
| `CompressionLevel` | `uint8`    | Compression level used during acquisition |
| `ErrorGranularity` | `uint32`   | Number of sectors per error block         |
| `SetIdentifier`    | `[16]byte` | GUID identifying the evidence set         |

## Included Example Programs

### 1) Print image metadata

```
go run ./examples/open <segment1.E01> [segment2.E02 ...]
```

Prints version, segment number, section count, and media geometry.

### 2) Hex dump first bytes of decoded stream

```
go run ./examples/readat <segment.E01>
```

Reads 64 bytes at offset 0 from the decoded logical stream and hex-dumps them.

### 3) Partition table and filesystem detection

```
go run ./examples/offsets [flags] <segment1.E01> [segment2.E02 ...]
```

Uses [libtable](https://github.com/aoiflux/libtable) to auto-detect the
partition table and print all partition entries.

Optional flags:

- `-pt-offset <bytes>` — byte offset where partition table parsing starts
- `-verbose-detect` — print all detection attempts
- `-dump-sectors N` — hex dump first N sectors from the decoded stream

## Development

Run tests:

```
go test ./...
```
