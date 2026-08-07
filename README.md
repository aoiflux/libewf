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
  - EWF v2 (EVF2) — `.Ex01` physical images, including pattern-fill chunks
  - EWF v1 Logical (LVF) `.L01` and EWF v2 Logical (LEF2) `.Lx01` — **detected
    only**; per-file enumeration is not implemented yet (see
    [Limitations](#limitations))
- Multi-segment image support (`OpenSegments`), with completeness validation
- Segment sets discovered from one path (`OpenPath`), following the EWF naming
  progression — `.E01`…`.E99`, then `.EAA`…
- Chunk decompression:
  - None (method 0)
  - Deflate / zlib (method 1)
  - bzip2 (method 2)
- Chunk tables validated against their stored Adler-32 checksums, with automatic
  recovery from the `table2` backup copy
- Rich image metadata:
  - Version, segment number, section inventory
  - Media geometry (chunks, sectors per chunk, bytes per sector, total sectors)
  - MD5 and SHA1 acquisition digests, from the binary `hash`/`digest` sections
    and from the compressed-XML `xhash` section that EWF-X uses
  - Session entries and acquisition error ranges
- `Verify` recomputes the acquisition digests over the decoded device
- `io.ReaderAt`-compatible decoded stream for random-access reads, bounded by
  the logical device size and safe for concurrent use
- Bounded LRU chunk cache, so small reads within a chunk do not repeat its
  decompression
- Encrypted images are detected and rejected rather than decoded as ciphertext

## Install

```
go get github.com/aoiflux/libewf
```

## Quick Start

### From a path

`OpenPath` finds the rest of the segment set itself, which is what most callers
want:

```go
r, err := libewf.OpenPath("/evidence/case1.E01")
if err != nil {
	panic(err)
}
defer r.Close()   // this Reader owns the files it opened, and closes them
```

The path may name any numbered member of the set — the set is enumerated from
segment 1 either way — and a path whose extension names no EWF family is opened
as a single file.

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

Enumerating segments by hand is only necessary when they do not come from a
local filesystem, or when the caller needs the handles for something else.
`SegmentPaths` returns the same ordering without opening anything, for reports
that must record which files an image was decoded from.

## API

### Open functions

| Function                                                                         | Description                                                           |
| -------------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| `OpenPath(path string) (Reader, error)`                                          | Open an image from one segment path, discovering the rest of the set  |
| `OpenPathWithOptions(path string, opts ...Option) (Reader, error)`               | As `OpenPath`, with options                                           |
| `SegmentPaths(path string) ([]string, error)`                                    | The set's paths in segment order, without opening them                |
| `Open(source io.ReaderAt) (Reader, error)`                                       | Open a single-segment EWF image                                       |
| `OpenSegments(sources []io.ReaderAt) (Reader, error)`                            | Open a multi-segment EWF image; segments may be supplied in any order |
| `OpenWithOptions(source io.ReaderAt, opts ...Option) (Reader, error)`            | As `Open`, with options                                               |
| `OpenSegmentsWithOptions(sources []io.ReaderAt, opts ...Option) (Reader, error)` | As `OpenSegments`, with options                                       |
| `Create(w io.Writer) (Writer, error)`                                            | Always returns `ErrNotImplemented`; see [Non-goals](#non-goals)       |

`OpenPath` is the one constructor whose `Reader` owns its sources: it opened the
files, so its `Close` closes them. Every other constructor leaves the caller's
`io.ReaderAt`s to the caller.

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
them. The exception is a `Reader` from `OpenPath`, which opened its own files
and therefore closes them.

### Segment set completeness

A multi-segment set must be complete. A gap in the segment numbering, or a final
segment that does not terminate with a `done` section, is an error — decoding a
partial set would otherwise present a silently truncated device:

```go
r, err := libewf.OpenSegments(sources)
switch {
case errors.Is(err, libewf.ErrMissingSegment):
    // a segment file between the first and last was not supplied
case errors.Is(err, libewf.ErrIncompleteSegmentSet):
    // trailing segment files were not supplied
}
```

`OpenPath` splits the same two cases by where they can be detected. A hole in
the numbering is visible in the directory, and fails before anything is read
with a `*MissingSegmentsError` naming the files that were not found. A set that
simply stops early is not — nothing on disk records how many segments the
acquisition wrote — so it is caught when the segments are read, as
`ErrIncompleteSegmentSet`:

```go
_, err := libewf.OpenPath("/evidence/case1.E01")

var missing *libewf.MissingSegmentsError
if errors.As(err, &missing) {
    fmt.Println(missing.Expected) // [case1.E02 case1.E03]
}
```

`*MissingSegmentsError` unwraps to `ErrMissingSegment`, so existing
`errors.Is` checks keep working.

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

### Acquisition provenance

`Metadata().Acquisition` carries what the imaging tool recorded: who acquired
the evidence, from what, when, and with which tool. It is nil when the image has
no header section this library can decode.

```go
if a := r.Metadata().Acquisition; a != nil {
    fmt.Println(a.CaseNumber, a.EvidenceNumber, a.ExaminerName)
    fmt.Println(a.AcquiryDate, a.SoftwareVersion, a.OperatingSystem)
}
```

In EWF v1 the same information is stored in up to three sections with different
encodings — `header` (8-bit, tab-delimited), `header2` (UTF-16LE) and `xheader`
(XML) — and most images carry more than one. `header2` wins where present, since
it cannot mangle non-ASCII case notes and stores dates as unambiguous POSIX
timestamps; lower-precedence sections still fill fields it left empty.
`Acquisition.Source` names the section that won, and `Acquisition.Values`
exposes every raw identifier/value pair including ones with no dedicated field.

EWF v2 replaces those with `case_data` and `device_information`, which between
them carry both the provenance and the media geometry. Both contribute to one
`Acquisition`: the device model, serial number and label come from
`device_information`, everything else from `case_data`.

Dates are stored three different ways: a POSIX timestamp, six space-separated
numbers, or a ctime-like string. Only the first carries a timezone, so the other
two are interpreted in the local zone — `AcquiryDateRaw` and `SystemDateRaw`
preserve the stored text for anything that must round-trip exactly.

### Errors

All errors are comparable with `errors.Is`:

| Error                     | Meaning                                           |
| ------------------------- | ------------------------------------------------- |
| `ErrUnsupportedFormat`    | Not a recognised EWF segment                      |
| `ErrCorruptImage`         | Structural damage that prevents decoding          |
| `ErrInvalidOffset`        | Negative or unusable `ReadAt` offset              |
| `ErrMissingSegment`       | Gap in the supplied segment numbering             |
| `ErrIncompleteSegmentSet` | Trailing segments were not supplied               |
| `ErrEncrypted`            | Image is encrypted; decryption is not supported   |
| `ErrChecksumMismatch`     | Checksum validation failed under `ChecksumStrict` |
| `ErrNotImplemented`       | Intentionally absent API                          |

Every error above is returned by some code path. Sentinels for unimplemented
features are deliberately absent: an exported error that can never occur invites
a branch that never runs and reads as though the feature were handled.

Failures that can be attributed to one segment of a set are wrapped in a
`*SegmentError`, which records the segment number and its index in the slice
passed to `OpenSegments` — or, for `OpenPath`, in the discovered set.

A gap found on disk by `OpenPath` is reported as a `*MissingSegmentsError`,
which lists the absent segment numbers and the names they would carry, and
unwraps to `ErrMissingSegment`.

### Options

`OpenWithOptions`, `OpenSegmentsWithOptions` and `OpenPathWithOptions` accept:

| Option                        | Default        | Effect                                                |
| ----------------------------- | -------------- | ----------------------------------------------------- |
| `WithChunkCache(n)`           | 16             | Decoded chunks kept cached. Negative disables caching |
| `WithChecksumPolicy(p)`       | `ChecksumWarn` | Response to a chunk table that fails its checksum     |
| `AllowIncompleteSegmentSet()` | off            | Permit opening a partial segment set                  |

```go
r, err := libewf.OpenSegmentsWithOptions(sources,
    libewf.WithChunkCache(64),
    libewf.WithChecksumPolicy(libewf.ChecksumStrict),
)
```

Checksum policies:

- `ChecksumWarn` (default) decodes the image and records failures in
  `Metadata().ChunkTablesInvalid`. Damaged evidence still yields what is
  readable, with the caller told it is unverified.
- `ChecksumStrict` refuses to open an image in which any chunk table failed
  validation and could not be recovered from its `table2` backup, returning
  `ErrChecksumMismatch`.
- `ChecksumIgnore` suppresses the accounting entirely.

### Encryption

Encrypted images are detected — via an encryption-keys section or a per-section
encrypted flag — and `Open` fails with `ErrEncrypted`. Decryption is not
implemented, and decoding the chunks anyway would hand back ciphertext presented
as device content. There is no option to override this.

### Performance

A chunk is the smallest decodable unit of an EWF image, so without caching a
caller reading 512 bytes at a time re-decompresses the whole enclosing 32 KiB
chunk on every call. Filesystem parsers read exactly like that, so the cache is
on by default. Measured with `go test ./reader/ -bench ReadAtCacheSize`:

| Cache    | ns/op  | Throughput | Allocations |
| -------- | ------ | ---------- | ----------- |
| disabled | 78,397 | 6.5 MB/s   | 88/op       |
| enabled  | ~700   | ~730 MB/s  | 1/op        |

Streaming whole chunks sequentially gains nothing from the cache, since every
read lands in a new chunk; the benefit is entirely in repeated or scattered
reads within a chunk.

## Metadata Model

`metadata.Info` is returned by `Reader.Metadata()` after opening a segment.

| Field                               | Type                 | Description                                        |
| ----------------------------------- | -------------------- | -------------------------------------------------- |
| `MajorVersion` / `MinorVersion`     | `uint8`              | EWF format version                                 |
| `SegmentNumber`                     | `uint32`             | Segment index in the set                           |
| `SectionCount`                      | `int`                | Total number of sections parsed                    |
| `HasNextSection` / `HasDoneSection` | `bool`               | Segment chain flags                                |
| `IsEncrypted`                       | `bool`               | Whether the image is encrypted                     |
| `HasIntegrityHashBlocks`            | `bool`               | Whether hash blocks are present                    |
| `SectionTypeCounts`                 | `map[uint32]int`     | Count of each section type code                    |
| `Sections`                          | `[]Section`          | Ordered section descriptor list                    |
| `Media`                             | `*MediaInfo`         | Media geometry and acquisition parameters          |
| `HasMD5Digest` / `MD5Digest`        | `bool` / `[16]byte`  | MD5 integrity digest                               |
| `HasSHA1Digest` / `SHA1Digest`      | `bool` / `[20]byte`  | SHA1 integrity digest                              |
| `Sessions`                          | `[]SessionEntry`     | Session table entries                              |
| `AcquisitionErrors`                 | `[]AcquisitionError` | Sectors that could not be read during acquisition  |
| `Acquisition`                       | `*Acquisition`       | Provenance recorded at imaging time, or nil        |
| `ObservedChunkCount`                | `uint64`             | Chunks actually decoded from the supplied segments |
| `ChunkTablesRecovered`              | `int`                | Chunk tables read from their `table2` backup       |
| `ChunkTablesInvalid`                | `int`                | Chunk tables where no copy passed its checksum     |

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

Each takes one path and discovers the rest of the segment set; several paths are
still accepted, for a set whose files do not follow the EWF naming progression.

### 1) Print image metadata

```
go run ./examples/open <image.E01> [segment2.E02 ...]
```

Prints version, segment number, section count, and media geometry.

### 2) Hex dump first bytes of decoded stream

```
go run ./examples/readat <image.E01>
```

Reads 64 bytes at offset 0 from the decoded logical stream and hex-dumps them.

### 3) Partition table and filesystem detection

```
go run ./examples/offsets [flags] <image.E01> [segment2.E02 ...]
```

Uses [libtable](https://github.com/aoiflux/libtable) to auto-detect the
partition table and print all partition entries.

Optional flags:

- `-pt-offset <bytes>` — byte offset where partition table parsing starts
- `-verbose-detect` — print all detection attempts
- `-dump-sectors N` — hex dump first N sectors from the decoded stream

## Validation status

The reader is checked against images written by `ewfacquire` across 30
configurations, with the decoded stream compared byte-for-byte against the raw
device that was acquired, the acquisition digests recomputed from the decode and
compared with the ones `ewfinfo` reads out of the container, and the provenance
compared against the values the acquisition was invoked with.

Covered: `ewf`, `ewfx`, `smart` (`.s01`), `ftk`, `encase2` through `encase7`,
`linen5` through `linen7`, and `encase7-v2` (`.Ex01`); uncompressed, deflate
fast/best/empty-block, and EWF2 pattern-fill chunks; 16, 64 and 512 sectors per
chunk; 512- and 4096-byte sectors; single and multi-segment sets; all-zero and
incompressible payloads; and devices whose size is not a whole multiple of the
chunk size.

Not covered: images from EnCase, FTK Imager or Guymager proper. Those writers do
not share a code path with `ewfacquire` and each has its own header dialect, so
the commercial writers remain unvalidated. Nor is bzip2 chunk compression:
`ewfacquire` falls back to deflate when asked for it, so no test image exercises
that path. See [testdata/corpus/README.md](testdata/corpus/README.md).

Generating the `.Ex01` entries needs libewf 20240506 or newer. Distribution
packages (Debian and Ubuntu ship 20140506-era builds) accept `-f encase7-v2` and
then silently write an `.E01`; `mkcorpus` detects that and skips those variants
rather than recording an entry that claims EWF2 coverage it does not have.

## Limitations

Known gaps, stated explicitly so they are not discovered at integration time:

- **Logical evidence (`.L01` / `.Lx01`) is detected but not enumerated.** The
  file signature is recognised and metadata parses, but the `ltree` and
  `single_files_data` sections are not decoded, so individual files cannot be
  listed or read yet. No libewf tool can write logical evidence, so there is no
  way to validate an implementation against a real sample — see
  [testdata/corpus/README.md](testdata/corpus/README.md).
- **Encrypted images cannot be read.** They are detected and rejected with
  `ErrEncrypted` rather than decoded; decryption of `.Ex01` is not implemented.
- **Source-device blocks are not parsed.** EnCase 6 and later append `srce` and
  `sub` blocks to the header describing the acquired device in a nested table
  form. The `main` block, which carries the acquisition provenance, is decoded;
  these are skipped.
- **EWF v2 requires more than a bare `io.ReaderAt`.** Its section chain is
  walked from the end of the file, so the source must also provide `Size()`,
  `Len()` or `Stat()` — `os.File`, `bytes.Reader` and `io.SectionReader` all do.
- **Only chunk-table checksums are validated.** Section descriptors carry their
  own Adler-32 values, which are read but not currently verified.
- **bzip2 chunk compression is untested.** EWF2 permits it and the code path
  exists, but no available writer produces it.

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
