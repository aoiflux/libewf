# Golden-image corpus

This directory holds EWF images written by **real acquisition tools**, together
with a `manifest.json` recording what a correct reader must produce from each.

It is empty by default. `TestCorpus` skips when it is empty — so **a green test
suite on a machine with no corpus is not evidence of format conformance.** The
synthetic images in `reader/open_test.go` prove the reader agrees with our model
of the format. Only this corpus proves it agrees with the format as shipped by
EnCase, FTK Imager, Guymager and `ewfacquire`.

## Why expected values must not come from this library

Every value in the manifest has to come from a source independent of the code
under test. Recording what libewf reports and asserting it later proves only
that libewf is self-consistent — which it was while it returned silently
corrupt data for every multi-group E01.

Two oracles are supported:

| Oracle          | Source of truth                                                     | Needs                |
| --------------- | ------------------------------------------------------------------- | -------------------- |
| `raw-source`    | SHA-256 of the raw image that was acquired; the decode must reproduce it | `ewfacquire`     |
| `stored-digest` | The acquisition MD5/SHA-1 the imaging tool embedded in the image     | nothing              |

`stored-digest` works because the imaging tool computed those digests from the
original device before writing the container. Recomputing them from our decoded
stream is a genuine external check — this is what `libewf.Verify` does.

### Acquisition dates need two expectations

EWF stores the acquisition date three ways and only one of them, the POSIX
timestamp `header2` and `case_data` write, denotes an instant. Six
space-separated numbers (`header`, as `encase2`, `encase3`, `ewf`, `ftk` and
`smart` write it) and the ctime-like string are wall clocks with no timezone,
so the reader interprets them in the local zone.

The manifest therefore records both readings of the same `ewfinfo` date:

| Field             | Asserted for                    | Why                                           |
| ----------------- | ------------------------------- | --------------------------------------------- |
| `acquiryDateUnix` | dates stored as a POSIX timestamp | zone-bearing, so the instant is portable     |
| `acquiryDateWall` | dates stored without a timezone   | the instant is not portable; the wall clock is |

Asserting an instant for a zone-less date pins the timezone the corpus was
generated in, and fails on every machine elsewhere — which it did, for those
five dialects, until the wall-clock expectation was added. The wall clock still
catches a date read from the wrong section or with its fields transposed, which
is what this expectation is for.

## Populating it

### From existing images (no tooling required)

Any real `.E01` you already have can be registered. Sibling segments
(`.E02`, `.E03`, …) are picked up automatically.

```
go run ./tools/mkcorpus -out testdata/corpus -no-generate -adopt /evidence/case1.E01
go test -run TestCorpus ./...
```

The image must carry an acquisition digest; `mkcorpus` refuses it otherwise,
because there would be nothing independent to verify against.

### Generated with ewfacquire (preferred for CI)

Install `libewf-tools`, then:

```
go run ./tools/mkcorpus -out testdata/corpus
go test -run TestCorpus ./...
```

This acquires deterministic raw devices across a matrix of writer dialects —
`encase5`, `encase6`, `encase7`, `encase7-v2`, uncompressed / deflate, single
and multi-segment, all-zero and incompressible payloads, and a device size that
is deliberately not a whole multiple of the chunk size.

## What is and is not committed

Nothing here is committed except this file. The corpus is a build artefact:
generated images are reproducible from `mkcorpus`, and adopted ones are usually
real evidence that must not be published.

`manifest.json` is excluded too. Committing it without the images it references
would make a fresh clone *fail* to open them rather than skip the corpus test.
What the corpus should contain lives in `tools/mkcorpus` (`buildMatrix`), where
it is version-controlled as code.

## What this corpus has caught

Worth recording, because it is the argument for keeping it:

- The `smart` and `ewf` dialects have no separate `sectors` section — chunk data
  lives inside the `table` section itself, with offsets absolute from the start
  of the file. The reader's table checksum validation assumed the bytes after
  the entry array were always a checksum, so it read chunk data and reported a
  false checksum failure on every image in those dialects.
- EWF-X records its SHA-1 only in the compressed-XML `xhash` section. The reader
  did not decode it, so the digest was invisible even though `ewfinfo` could read
  it. Handling for it exists because the corpus made the gap visible.
- Only `encase6`/`encase7` and `linen6`/`linen7` store a SHA-1 at all. The
  generator originally took expected digests from the acquisition log, which
  records what `ewfacquire` *computed* rather than what it *stored* — so it
  asserted a SHA-1 on ten dialects that have nowhere to put one. Expected values
  now come from `ewfinfo`.

## Getting EWF2 (`.Ex01`) coverage

The `ex01-*` variants need a writer that can actually produce EWF2. Distribution
packages cannot: Debian and Ubuntu ship libewf 20140814, which accepts
`-f encase7-v2` and then prints *"Unsupported EWF format defaulting to:
encase6"* and writes a `.E01`. `mkcorpus` detects that from the extension and
skips those variants, so a corpus built with the packaged tool silently has no
EWF2 entries — deliberately, rather than recording entries that claim coverage
they do not have.

To get them, build a recent libewf. EWF2 write support lives in the
*experimental* tarballs. No root is needed; run the binaries from the build tree:

```
curl -sSLO https://github.com/libyal/libewf/releases/download/20240506/libewf-experimental-20240506.tar.gz
tar xzf libewf-experimental-20240506.tar.gz
cd libewf-20240506 && ./configure && make -j"$(nproc)"

PATH="$PWD/ewftools:$PATH" go run ./tools/mkcorpus -out testdata/corpus
```

Confirm with `ewfacquire -V` that you are on 20240506 or newer, and that
`ls testdata/corpus/*.Ex01` is non-empty afterwards.

## Still missing

- **Images from EnCase, FTK Imager and Guymager proper.** `ewfacquire` and those
  tools share no code path, and each has its own header dialect, so a corpus
  built only from `ewfacquire` leaves the commercial writers unvalidated. The
  `-adopt` mode exists for exactly this.
- **Logical evidence (`.L01` / `.Lx01`).** No libewf tool writes logical
  evidence — `ewfacquire` only performs physical acquisition — so there is no way
  to produce a sample here. Implementing the `ltree` parser without one would
  mean testing the reader against our own model of the format, which is exactly
  the circularity that let the `table2` chunk-table bug survive a green suite.
  An adopted real `.L01` is a prerequisite, not a nice-to-have.
- **bzip2 chunk compression.** EWF2 permits it, but `ewfacquire` falls back to
  deflate when asked, so the code path has no test image.
- **Cross-validation against `ewfexport`.** The raw-source oracle is stronger,
  but an independent decoder disagreeing would be worth knowing about.
