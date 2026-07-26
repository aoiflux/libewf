# libewf

libewf is a pure Go library for **reading** Expert Witness Format (EWF) disk
images. It is read-only by design: see [Non-goals](#non-goals).

It is designed to be easy to embed in forensic tools, scripts, and analysis
pipelines.

This is an independent implementation. It shares no code with the C `libewf`
project and is not a port of it; it is distributed under the MIT license.

## Features

- Pure Go (no CGO dependency)
- Root import API: `github.com/aoiflux/libewf`
- EWF format support:
  - EWF v1 (EVF) — `.E01` physical images
  - EWF v2 (EVF2) — `.Ex01` physical images
  - EWF v1 Logical (LVF) `.L01` and EWF v2 Logical (LEF2) `.Lx01` — **detected
    only**; per-file enumeration is not implemented yet (see [Limitations](#limitations))
- Multi-segment image support (`OpenSegments`), with completeness validation
- Chunk decompression:
  - None (method 0)
  - Deflate / zlib (method 1)
  - bzip2 (method 2)
- Chunk tables validated against their stored Adler-32 checksums, with
  automatic recovery from the `table2` backup copy
- Rich image metadata:
  - Version, segment number, section inventory
  - Media geometry (chunks, sectors per chunk, bytes per sector, total sectors)
  - MD5 and SHA1 acquisition digests, from the binary `hash`/`digest` sections
    and from the compressed-XML `xhash` section that EWF-X uses
  - Session entries and acquisition error ranges
- `Verify` recomputes the acquisition digests over the decoded device
- `io.ReaderAt`-compatible decoded stream for random-access reads, bounded by
  the logical device size and safe for concurrent use

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

| Function                                                                    | Description                                                                          |
| --------------------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| `Open(source io.ReaderAt) (Reader, error)`                                  | Open a single-segment EWF image                                                      |
| `OpenSegments(sources []io.ReaderAt) (Reader, error)`                       | Open a multi-segment EWF image; segments may be supplied in any order                |
| `OpenWithOptions(source io.ReaderAt, opts ...Option) (Reader, error)`       | As `Open`, with options                                                              |
| `OpenSegmentsWithOptions(sources []io.ReaderAt, opts ...Option) (Reader, error)` | As `OpenSegments`, with options                                                 |
| `Create(w io.Writer) (Writer, error)`                                       | Always returns `ErrNotImplemented`; see [Non-goals](#non-goals)                      |

### Reader interface

```go
type Reader interface {
    io.ReaderAt

    // Size returns the logical size of the decoded device in bytes.
    Size() int64

    // SectorSize returns the logical sector size in bytes, or 0 if unknown.
    SectorSize() int

    Metadata() metadata.Info
    Close() error
}
```

`ReadAt` decodes and decompresses chunks on demand and exposes the decoded
logical device at any byte offset. It never returns data at or beyond `Size()`,
returns a non-nil error whenever `n < len(p)`, and is safe for concurrent use
provided the underlying `io.ReaderAt` sources are.

`Close` does not close the underlying sources; the caller retains ownership of
them.

### Segment set completeness

A multi-segment set must be complete. A gap in the segment numbering, or a
final segment that does not terminate with a `done` section, is an error —
decoding a partial set would otherwise present a silently truncated device:

```go
r, err := libewf.OpenSegments(sources)
switch {
case errors.Is(err, libewf.ErrMissingSegment):
    // a segment file between the first and last was not supplied
case errors.Is(err, libewf.ErrIncompleteSegmentSet):
    // trailing segment files were not supplied
}
```

To inspect an incomplete set anyway — for metadata, or triage of damaged
evidence — opt in explicitly. `Size()` still reports the full declared device,
but reads past the supplied data return `io.EOF`, and
`Metadata().ObservedChunkCount` will be below `Metadata().Media.NumberOfChunks`:

```go
r, err := libewf.OpenWithOptions(f, libewf.AllowIncompleteSegmentSet())
```

Never use that option for content that will be hashed or carved.

### Integrity verification

`Verify` recomputes MD5 and SHA-1 over the whole decoded device and compares
them against the digests the acquisition tool embedded in the image. Because
those digests were computed from the original media before the container was
written, reproducing them confirms both that the evidence is intact and that it
was decoded correctly:

```go
result, err := libewf.Verify(ctx, r)
if err != nil {
    return err
}
if !result.OK() {
    for _, bad := range result.BadRanges {
        log.Printf("unreadable: offset %d length %d: %s", bad.Offset, bad.Length, bad.Err)
    }
    log.Printf("md5 stored=%x computed=%x match=%v",
        result.StoredMD5, result.ComputedMD5, result.MD5Match)
}
```

Spans that fail to decode are zero-filled, recorded in `BadRanges`, and hashing
continues, so a damaged image still yields a full report of what is unreadable.
Any entry in `BadRanges` makes the digests meaningless — check it before reading
a mismatch as evidence of tampering. `OK()` is false for an image that stores no
digests at all, because there is then nothing to verify against.

`Verify` reads the entire device and honours `ctx` cancellation.

### Errors

All errors are comparable with `errors.Is`:

| Error                     | Meaning                                                     |
| ------------------------- | ----------------------------------------------------------- |
| `ErrUnsupportedFormat`    | Not a recognised EWF segment                                 |
| `ErrCorruptImage`         | Structural damage that prevents decoding                     |
| `ErrInvalidOffset`        | Negative or unusable `ReadAt` offset                         |
| `ErrMissingSegment`       | Gap in the supplied segment numbering                        |
| `ErrIncompleteSegmentSet` | Trailing segments were not supplied                          |
| `ErrEncrypted`            | Image is encrypted and no key was supplied                   |
| `ErrChecksumMismatch`     | Stored checksum validation failed                            |
| `ErrNoLogicalEvidence`    | Logical-evidence operation on a physical image               |
| `ErrNotImplemented`       | Intentionally absent API                                     |

Failures that can be attributed to one segment of a set are wrapped in a
`*SegmentError`, which records the segment number and its index in the slice
passed to `OpenSegments`.

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
| `ObservedChunkCount`                | `uint64`             | Chunks actually decoded from the supplied segments |
| `ChunkTablesRecovered`              | `int`                | Chunk tables read from their `table2` backup      |
| `ChunkTablesInvalid`                | `int`                | Chunk tables where no copy passed its checksum    |

`Media.NumberOfChunks` is the count **declared** by the volume section.
`ObservedChunkCount` is the count actually **decoded**. They differ when
segments are missing or a chunk table could not be read, so comparing them is
the cheapest integrity check available after opening an image.

A non-zero `ChunkTablesInvalid` means some chunk offsets were used without
checksum verification; data decoded through them should be treated as suspect.

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

## Validation status

The reader is checked against images written by `ewfacquire` across 26
configurations, with the decoded stream compared byte-for-byte against the raw
device that was acquired, and the acquisition digests recomputed from the decode
and compared with the ones `ewfinfo` reads out of the container.

Covered: `ewf`, `ewfx`, `smart` (`.s01`), `ftk`, `encase2` through `encase7`,
`linen5` through `linen7`; uncompressed, deflate fast/best/empty-block; 16, 64
and 512 sectors per chunk; 512- and 4096-byte sectors; single and multi-segment
sets; all-zero and incompressible payloads; and devices whose size is not a
whole multiple of the chunk size.

Not covered: EWF2 (`.Ex01`) — see [Limitations](#limitations) — and images from
EnCase, FTK Imager or Guymager proper. Those writers do not share a code path
with `ewfacquire` and each has its own header dialect, so the commercial writers
remain unvalidated. See [testdata/corpus/README.md](testdata/corpus/README.md).

## Limitations

Known gaps, stated explicitly so they are not discovered at integration time:

- **Logical evidence (`.L01` / `.Lx01`) is detected but not enumerated.** The
  file signature is recognised and metadata parses, but the `ltree` and
  `single_files_data` sections are not decoded, so individual files cannot be
  listed or read yet.
- **Encrypted images are not decrypted.** `Metadata().IsEncrypted` reports
  detection only. Decryption of `.Ex01` is not implemented.
- **Acquisition metadata is not parsed.** The `header`, `header2` and `xheader`
  sections carry case number, examiner, acquisition date and imaging tool.
  They are listed in the section inventory but their contents are not decoded.
  (Digests in `xhash` *are* decoded; the descriptive fields are not.)
- **EWF v2 volume geometry is not parsed**, so `Size()` returns 0 for `.Ex01`
  images and `ReadAt` reports `ErrCorruptImage`.
- **EWF v2 requires more than a bare `io.ReaderAt`.** Its section chain is
  walked from the end of the file, so the source must also provide `Size()`,
  `Len()` or `Stat()` — `os.File`, `bytes.Reader` and `io.SectionReader` all do.
- **Chunks are decoded on every read.** There is no chunk cache yet, so small
  sequential reads within one chunk repeat its decompression.

## Non-goals

**Writing EWF images.** libewf is read-only by design: a forensic tool must not
depend on an unproven acquisition path. `Create` exists only so this intent is
explicit rather than an open TODO, and always returns `ErrNotImplemented`. No
write support is planned.

## Development

Run tests:

```
go test ./...
go test -race ./...
go test ./reader/ -fuzz FuzzOpenAndRead -fuzztime 60s
```

The synthetic images used by the tests are built to the shape EnCase actually
writes — every chunk group has a `table` plus its `table2` backup, all section
and table checksums are real Adler-32 values, and uncompressed chunks carry
their trailing checksum. Tests that skip those details cannot catch the bugs
that matter; do not simplify the builder.

### Golden-image corpus

Synthetic tests prove the reader matches our model of the format. Validating it
against the format as actually shipped requires images from real acquisition
tools, so `TestCorpus` runs over `testdata/corpus`:

```
# register an image you already have (no external tooling needed)
go run ./tools/mkcorpus -out testdata/corpus -no-generate -adopt /evidence/case1.E01

# or generate a matrix of writer dialects (requires libewf-tools)
go run ./tools/mkcorpus -out testdata/corpus

go test -run TestCorpus ./...
```

`TestCorpus` **skips** when the corpus is empty, so a green suite on a machine
without one is not evidence of format conformance. See
[testdata/corpus/README.md](testdata/corpus/README.md) for how the expected
values are derived and why they must never come from this library.
