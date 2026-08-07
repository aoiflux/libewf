// Command mkcorpus builds the golden-image corpus that validates the reader
// against images written by real acquisition tools.
//
// Two modes:
//
// Generate — acquire synthetic raw devices with ewfacquire across a matrix of
// formats and compression settings. The oracle is the raw image itself: the
// decoded device must reproduce it byte for byte.
//
//	go run ./tools/mkcorpus -out testdata/corpus
//
// Adopt — register images you already have. The oracle is the acquisition
// digest the imaging tool embedded, which it computed from the original
// device, so reproducing it from the decoded stream still validates decoding.
// This needs no external tooling.
//
//	go run ./tools/mkcorpus -out testdata/corpus -adopt /evidence/case1.E01
//
// Expected values are never taken from this library. Anything it reports is
// the thing under test, so recording it and asserting it later would prove
// nothing. Sizes and digests come from the raw source or from the acquisition
// tool's own embedded digests.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	libewf "github.com/aoiflux/libewf"
	"github.com/aoiflux/libewf/internal/corpus"
	"github.com/aoiflux/libewf/internal/segname"
)

func main() {
	var (
		out       = flag.String("out", "testdata/corpus", "corpus directory to write")
		adopt     = flag.String("adopt", "", "comma-separated first segments of existing images to register")
		keepRaw   = flag.Bool("keep-raw", false, "keep the generated raw source images for debugging")
		sizeMiB   = flag.Int("size", 4, "size in MiB of each generated raw source image")
		skipTools = flag.Bool("no-generate", false, "skip ewfacquire generation, adopt only")
	)
	flag.Parse()

	if err := run(*out, *adopt, *sizeMiB, *keepRaw, *skipTools); err != nil {
		fmt.Fprintln(os.Stderr, "mkcorpus:", err)
		os.Exit(1)
	}
}

func run(outDir, adoptList string, sizeMiB int, keepRaw, skipGenerate bool) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	manifest, _, err := corpus.Load(outDir)
	if err != nil {
		return err
	}
	if manifest == nil {
		manifest = &corpus.Manifest{Version: 1}
	}

	byName := make(map[string]int, len(manifest.Entries))
	for i, e := range manifest.Entries {
		byName[e.Name] = i
	}
	upsert := func(e corpus.Entry) {
		if i, ok := byName[e.Name]; ok {
			manifest.Entries[i] = e
			return
		}
		byName[e.Name] = len(manifest.Entries)
		manifest.Entries = append(manifest.Entries, e)
	}

	added := 0

	if adoptList != "" {
		for _, path := range strings.Split(adoptList, ",") {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			entry, err := adoptImage(outDir, path)
			if err != nil {
				return fmt.Errorf("adopting %s: %w", path, err)
			}
			upsert(entry)
			added++
			fmt.Printf("adopted %-28s %d bytes, oracle=%s\n", entry.Name, entry.Size, entry.Oracle)
		}
	}

	if !skipGenerate {
		if _, err := exec.LookPath("ewfacquire"); err != nil {
			if added == 0 {
				return fmt.Errorf("ewfacquire not found in PATH and nothing was adopted.\n" +
					"Install libewf-tools to generate a corpus, or register existing\n" +
					"images with -adopt <image.E01>[,<image2.E01>...]")
			}
			fmt.Fprintln(os.Stderr, "warning: ewfacquire not found in PATH; generated entries skipped")
		} else {
			entries, err := generate(outDir, sizeMiB, keepRaw)
			if err != nil {
				return err
			}
			for _, e := range entries {
				upsert(e)
				added++
			}
		}
	}

	if added == 0 {
		return errors.New("no corpus entries were produced")
	}

	for _, e := range manifest.Entries {
		if err := e.Validate(); err != nil {
			return err
		}
	}
	if err := corpus.Save(outDir, manifest); err != nil {
		return err
	}

	fmt.Printf("\nwrote %s with %d entries\n", filepath.Join(outDir, corpus.ManifestName), len(manifest.Entries))
	fmt.Println("verify with: go test -run TestCorpus ./...")
	return nil
}

// ---------------------------------------------------------------------------
// Adopt an existing image
// ---------------------------------------------------------------------------

// adoptImage registers an image that already exists, copying its segments into
// the corpus directory. The expected values come from the acquisition digests
// embedded by the imaging tool, never from this library's decode.
func adoptImage(outDir, firstSegment string) (corpus.Entry, error) {
	// The library's own discovery decides what belongs to the set, so a corpus
	// entry can never disagree with the code it exists to test. It also refuses
	// a set with a hole in it, which would otherwise be adopted as though it
	// were whole.
	segments, err := libewf.SegmentPaths(firstSegment)
	if err != nil {
		return corpus.Entry{}, err
	}

	base := strings.TrimSuffix(filepath.Base(firstSegment), filepath.Ext(firstSegment))
	name := sanitise(base)

	// The copies are opened explicitly rather than rediscovered by path: the
	// corpus directory may already hold segments left by an earlier adoption of
	// the same name, and an entry must describe the files it just wrote.
	sources := make([]io.ReaderAt, 0, len(segments))
	copied := make([]string, 0, len(segments))
	for _, src := range segments {
		dst := filepath.Join(outDir, name+filepath.Ext(src))
		if err := copyFile(src, dst); err != nil {
			return corpus.Entry{}, err
		}
		f, err := os.Open(dst)
		if err != nil {
			return corpus.Entry{}, err
		}
		defer f.Close()
		sources = append(sources, f)
		copied = append(copied, filepath.Base(dst))
	}

	// Opening the image is how the embedded acquisition digests are read.
	// Those digests are the oracle: the imaging tool computed them from the
	// original device, so they are independent of anything decoded here.
	r, err := libewf.OpenSegments(sources)
	if err != nil {
		return corpus.Entry{}, fmt.Errorf("cannot open image to read its acquisition digests: %w", err)
	}
	defer r.Close()

	entry := corpus.Entry{
		Name:            name,
		Segments:        copied,
		Oracle:          corpus.OracleStoredDigest,
		Producer:        "adopted from " + firstSegment,
		Size:            r.Size(),
		SectorSize:      r.SectorSize(),
		RequireVerifyOK: true,
	}

	meta := r.Metadata()
	if meta.HasMD5Digest {
		entry.ExpectStoredMD5 = hex.EncodeToString(meta.MD5Digest[:])
	}
	if meta.HasSHA1Digest {
		entry.ExpectStoredSHA1 = hex.EncodeToString(meta.SHA1Digest[:])
	}
	if entry.ExpectStoredMD5 == "" && entry.ExpectStoredSHA1 == "" {
		return corpus.Entry{}, errors.New("image records no acquisition digest, so there is nothing " +
			"independent to verify against; acquire a raw source instead")
	}
	return entry, nil
}

// ---------------------------------------------------------------------------
// Generate with ewfacquire
// ---------------------------------------------------------------------------

type variant struct {
	name         string
	format       string // ewfacquire -f
	compression  string // ewfacquire -c
	sectors      int64  // device size in sectors
	sectorSize   int    // ewfacquire -P
	chunkSectors int    // ewfacquire -b
	segmentBytes int64  // ewfacquire -S, 0 for the default
	pattern      string
}

// buildMatrix enumerates the writer dialects and encodings to acquire.
//
// Device sizes are expressed in whole sectors: ewfacquire records a sector
// count, so a raw source that is not sector-aligned would give the decoded
// device a different length than the file we hashed, breaking the oracle.
//
// EWF2 formats (encase7-v2, producing .Ex01) are deliberately absent. This
// reader does not decode EWF2 volume geometry yet, so those entries would fail
// for a documented reason rather than a discovered one. Adding them is the
// natural first corpus extension once Ex01 support lands.
func buildMatrix(baseSectors int64) []variant {
	const (
		sec512  = 512
		chunk64 = 64 // sectors per chunk => 32 KiB chunks
	)

	v := []variant{
		// Dialect sweep at fixed encoding: each writes a different header and
		// volume layout. encase2/3/4 predate the 8-byte sector count, which is
		// exactly where a misparse would go unnoticed.
		{"ewf-deflate", "ewf", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},
		{"ewfx-deflate", "ewfx", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},
		{"smart-deflate", "smart", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},
		{"ftk-deflate", "ftk", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},
		{"encase2-deflate", "encase2", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},
		{"encase3-deflate", "encase3", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},
		{"encase4-deflate", "encase4", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},
		{"encase5-deflate", "encase5", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},
		{"encase6-deflate", "encase6", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},
		{"encase7-deflate", "encase7", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},
		{"linen5-deflate", "linen5", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},
		{"linen6-deflate", "linen6", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},
		{"linen7-deflate", "linen7", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},

		// Encoding sweep on one dialect.
		{"encase6-none", "encase6", "none", baseSectors, sec512, chunk64, 0, "mixed"},
		{"encase6-fast", "encase6", "deflate:fast", baseSectors, sec512, chunk64, 0, "mixed"},
		{"encase6-emptyblock", "encase6", "deflate:empty-block", baseSectors, sec512, chunk64, 0, "mixed"},

		// Payload extremes: all-zero compresses to almost nothing, random not
		// at all, so the stored chunk sizes bracket both ends.
		{"encase6-zeros", "encase6", "deflate:best", baseSectors, sec512, chunk64, 0, "zeros"},
		{"encase6-random", "encase6", "deflate:best", baseSectors, sec512, chunk64, 0, "random"},

		// Geometry variations.
		{"encase6-chunk16", "encase6", "deflate:best", baseSectors, sec512, 16, 0, "mixed"},
		{"encase6-chunk512", "encase6", "deflate:best", baseSectors, sec512, 512, 0, "mixed"},
		{"encase6-sector4096", "encase6", "deflate:best", baseSectors / 8, 4096, chunk64, 0, "mixed"},

		// Multi-segment sets, compressed and not.
		{"encase6-multiseg", "encase6", "deflate:fast", baseSectors, sec512, chunk64, 1 << 20, "mixed"},
		{"encase6-multiseg-none", "encase6", "none", baseSectors, sec512, chunk64, 1 << 20, "random"},
		{"encase5-multiseg", "encase5", "deflate:best", baseSectors, sec512, chunk64, 1 << 20, "mixed"},
	}

	// One extra sector makes the device a whole number of sectors but not a
	// whole number of chunks, exercising the partial final chunk that every
	// real image whose size is not chunk-aligned ends with.
	v = append(v,
		variant{"encase6-partial-chunk", "encase6", "deflate:best", baseSectors + 1, sec512, chunk64, 0, "mixed"},
		variant{"encase6-partial-chunk-none", "encase6", "none", baseSectors + 1, sec512, chunk64, 0, "mixed"},
	)

	// EWF2 (.Ex01). These are skipped unless the acquisition tool can actually
	// write EWF2: libewf up to at least 20140814, which is what Debian and
	// Ubuntu package, accepts -f encase7-v2 and then silently falls back to
	// encase6. runEwfacquire detects that from the extension it produced.
	//
	// The zeros payload matters most here: EWF2 encodes long runs of identical
	// bytes as pattern-fill chunks, which are stored as a single repeating unit
	// and have no counterpart in EWF v1.
	v = append(v,
		variant{"ex01-deflate", "encase7-v2", "deflate:best", baseSectors, sec512, chunk64, 0, "mixed"},
		variant{"ex01-none", "encase7-v2", "none", baseSectors, sec512, chunk64, 0, "mixed"},
		variant{"ex01-zeros-pattern", "encase7-v2", "deflate:best", baseSectors, sec512, chunk64, 0, "zeros"},
		variant{"ex01-partial-chunk", "encase7-v2", "deflate:best", baseSectors + 1, sec512, chunk64, 0, "mixed"},
	)
	return v
}

// wantsEWF2 reports whether a format name selects the EWF2 container.
func wantsEWF2(format string) bool { return strings.HasSuffix(format, "-v2") }

func generate(outDir string, sizeMiB int, keepRaw bool) ([]corpus.Entry, error) {
	baseSectors := int64(sizeMiB) << 20 / 512

	rawDir := filepath.Join(outDir, "raw")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		return nil, err
	}
	if !keepRaw {
		defer os.RemoveAll(rawDir)
	}

	variants := buildMatrix(baseSectors)
	entries := make([]corpus.Entry, 0, len(variants))
	var failed []string

	for _, v := range variants {
		size := v.sectors * int64(v.sectorSize)

		rawPath := filepath.Join(rawDir, v.name+".raw")
		sum, err := writeRawImage(rawPath, size, v.pattern)
		if err != nil {
			return nil, err
		}

		segments, digests, err := runEwfacquire(outDir, rawDir, rawPath, v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  SKIP %-28s %v\n", v.name, err)
			failed = append(failed, v.name)
			continue
		}

		entry := corpus.Entry{
			Name:     v.name,
			Segments: segments,
			Oracle:   corpus.OracleRawSource,
			Producer: fmt.Sprintf("ewfacquire 20140814 -f %s -c %s -b %d -P %d",
				v.format, v.compression, v.chunkSectors, v.sectorSize),
			Notes: fmt.Sprintf("payload %q, %d sectors of %d bytes", v.pattern, v.sectors, v.sectorSize),

			// Every expected value below comes from the raw source we acquired
			// or from ewfacquire's own log, never from this library.
			Size:          size,
			SectorSize:    v.sectorSize,
			DecodedSHA256: sum,

			// Expect only the digests the container actually holds, as
			// reported by ewfinfo. Older dialects have nowhere to put a SHA-1
			// even though ewfacquire computed one.
			ExpectStoredMD5:  digests.storedMD5,
			ExpectStoredSHA1: digests.storedSHA1,
			RequireVerifyOK:  digests.storedMD5 != "" || digests.storedSHA1 != "",

			// The provenance strings are what we handed ewfacquire; the date is
			// ewfinfo's independent reading of what it wrote.
			ExpectAcquisition: &corpus.ExpectedAcquisition{
				CaseNumber:      acqCaseNumber,
				EvidenceNumber:  acqEvidenceNumber,
				Description:     v.name,
				ExaminerName:    acqExaminerName,
				Notes:           acqNotes,
				AcquiryDateUnix: digests.acquiryDateUnix,
			},
		}
		entries = append(entries, entry)
		fmt.Printf("  ok   %-28s %8d bytes  %d segment(s)\n", v.name, size, len(segments))
	}

	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d variant(s) could not be acquired: %s\n",
			len(failed), strings.Join(failed, ", "))
	}
	if len(entries) == 0 {
		return nil, errors.New("ewfacquire produced no usable images")
	}
	return entries, nil
}

// writeRawImage writes a deterministic raw device image and returns its
// hex SHA-256. Determinism matters: the digest is the corpus oracle, so
// regenerating must reproduce identical bytes.
func writeRawImage(path string, size int64, pattern string) (string, error) {
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sum := sha256.New()
	w := io.MultiWriter(f, sum)

	const block = 64 << 10
	buf := make([]byte, block)
	// A simple xorshift keeps this reproducible without depending on the
	// behaviour of any particular math/rand version.
	state := uint64(0x9E3779B97F4A7C15)
	next := func() uint64 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return state
	}

	for written := int64(0); written < size; {
		n := int64(block)
		if remaining := size - written; n > remaining {
			n = remaining
		}

		switch pattern {
		case "zeros":
			for i := range buf[:n] {
				buf[i] = 0
			}
		case "random":
			for i := int64(0); i < n; i += 8 {
				v := next()
				for b := 0; b < 8 && i+int64(b) < n; b++ {
					buf[i+int64(b)] = byte(v >> (8 * b))
				}
			}
		default: // "mixed", "odd": compressible text interleaved with entropy
			if (written/block)%3 == 0 {
				copy(buf[:n], strings.Repeat("FORENSIC-IMAGE-TEST-PATTERN-0123456789 ", block/39+1))
			} else if (written/block)%3 == 1 {
				for i := range buf[:n] {
					buf[i] = 0
				}
			} else {
				for i := int64(0); i < n; i += 8 {
					v := next()
					for b := 0; b < 8 && i+int64(b) < n; b++ {
						buf[i+int64(b)] = byte(v >> (8 * b))
					}
				}
			}
		}

		if _, err := w.Write(buf[:n]); err != nil {
			return "", err
		}
		written += n
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// digestInfo separates the digests ewfacquire computed over the source from the
// digests actually written into the container.
//
// The distinction matters: ewfacquire computes both MD5 and SHA-1 when asked,
// but only encase6/encase7 and linen6/linen7 have a section to store a SHA-1
// in. Expecting a digest merely because it was computed would fail on every
// older dialect for a reason that has nothing to do with the reader.
//
// Both sets come from outside this library — the acquisition log and ewfinfo —
// so neither is circular.
type digestInfo struct {
	computedMD5, computedSHA1 string
	storedMD5, storedSHA1     string

	// acquiryDateUnix is ewfinfo's reading of the acquisition date. Zero when
	// it could not be read or parsed, which means "do not assert it".
	acquiryDateUnix int64
}

// Provenance passed to ewfacquire on the command line. Because these are inputs
// to the acquisition rather than anything read back out, they are an oracle no
// decode can influence.
const (
	acqCaseNumber     = "corpus"
	acqEvidenceNumber = "mkcorpus"
	acqExaminerName   = "libewf-test"
	acqNotes          = "generated by mkcorpus"
)

func runEwfacquire(outDir, rawDir, rawPath string, v variant) ([]string, digestInfo, error) {
	target := filepath.Join(outDir, v.name)
	for _, stale := range mustGlob(target + ".*") {
		_ = os.Remove(stale)
	}

	// The log must not land beside the segments: it would match the segment
	// glob and be recorded as part of the image.
	logPath := filepath.Join(rawDir, v.name+".acq.log")

	args := []string{
		"-u", // unattended
		"-q", // minimal status output
		"-t", target,
		"-f", v.format,
		"-c", v.compression,
		"-b", fmt.Sprint(v.chunkSectors),
		"-P", fmt.Sprint(v.sectorSize),
		"-d", "sha1", // also record a SHA-1 where the dialect supports it
		"-l", logPath,
		"-C", acqCaseNumber, "-D", v.name, "-E", acqEvidenceNumber,
		"-e", acqExaminerName, "-N", acqNotes,
	}
	if v.segmentBytes > 0 {
		args = append(args, "-S", fmt.Sprint(v.segmentBytes))
	}
	args = append(args, rawPath)

	cmd := exec.Command("ewfacquire", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, digestInfo{}, fmt.Errorf("ewfacquire failed: %w\n%s", err, output)
	}

	var produced []string
	for _, p := range mustGlob(target + ".*") {
		if isSegmentFile(p) {
			produced = append(produced, p)
		}
	}
	if len(produced) == 0 {
		return nil, digestInfo{}, errors.New("ewfacquire reported success but produced no segment files")
	}
	sortSegments(produced)

	// libewf before roughly 20240506 accepts -f encase7-v2 and then reports
	// "Unsupported EWF format defaulting to: encase6", writing a .E01. Treat a
	// missing EWF2 extension as an unsupported variant rather than recording an
	// entry that claims to cover EWF2 while holding an EWF v1 image.
	if wantsEWF2(v.format) {
		ext := strings.ToLower(filepath.Ext(produced[0]))
		if !strings.HasPrefix(ext, ".ex") && !strings.HasPrefix(ext, ".lx") {
			for _, p := range produced {
				_ = os.Remove(p)
			}
			return nil, digestInfo{}, fmt.Errorf(
				"this ewfacquire cannot write EWF2 (asked for %s, produced %s); "+
					"needs libewf 20240506 or newer", v.format, ext)
		}
	}

	names := make([]string, 0, len(produced))
	for _, p := range produced {
		names = append(names, filepath.Base(p))
	}

	digests, err := parseAcquireLog(logPath)
	if err != nil {
		return nil, digestInfo{}, err
	}
	if err := readStoredDigests(produced[0], &digests); err != nil {
		return nil, digestInfo{}, err
	}

	// A digest that was computed over the source but differs from the one in
	// the container would mean the acquisition wrote something other than what
	// it read. That is a finding about the writer, not about this reader, but
	// it would silently poison the corpus, so surface it.
	if digests.computedMD5 != "" && digests.storedMD5 != "" && digests.computedMD5 != digests.storedMD5 {
		return nil, digestInfo{}, fmt.Errorf(
			"ewfacquire computed MD5 %s over the source but stored %s in the container",
			digests.computedMD5, digests.storedMD5)
	}
	return names, digests, nil
}

// readStoredDigests asks ewfinfo what the container actually holds: which
// digests, and the acquisition date it reads from the header sections.
//
// Using an independent implementation keeps the expectations non-circular. The
// date matters in particular because header sections encode it three different
// ways, so pinning ewfinfo's reading catches a date decoded from the wrong one.
func readStoredDigests(firstSegment string, out *digestInfo) error {
	cmd := exec.Command("ewfinfo", firstSegment)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ewfinfo %s failed: %w\n%s", filepath.Base(firstSegment), err, output)
	}

	inDigestSection := false
	for _, line := range strings.Split(string(output), "\n") {
		trimmed := strings.TrimSpace(line)

		if value, ok := trimAfter(trimmed, "Acquisition date:"); ok {
			if when, ok := parseEwfinfoDate(value); ok {
				out.acquiryDateUnix = when.Unix()
			}
		}

		if strings.HasPrefix(trimmed, "Digest hash information") {
			inDigestSection = true
			continue
		}
		// Section headings are unindented; a new one ends the digest block.
		if inDigestSection && trimmed != "" && !strings.HasPrefix(line, "\t") {
			inDigestSection = false
		}
		if !inDigestSection {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "MD5:"):
			out.storedMD5 = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "MD5:")))
		case strings.HasPrefix(trimmed, "SHA1:"):
			out.storedSHA1 = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "SHA1:")))
		}
	}
	return nil
}

func trimAfter(line, prefix string) (string, bool) {
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
}

// parseEwfinfoDate reads the ctime-style date ewfinfo prints. Values without a
// zone are interpreted locally, which is how the writer recorded them.
func parseEwfinfoDate(value string) (time.Time, bool) {
	for _, layout := range []string{
		"Mon Jan 2 15:04:05 2006 MST",
		"Mon Jan _2 15:04:05 2006 MST",
		"Mon Jan 2 15:04:05 2006",
		"Mon Jan _2 15:04:05 2006",
	} {
		if when, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return when, true
		}
	}
	return time.Time{}, false
}

// isSegmentFile reports whether a path carries an EWF segment extension:
// .E01..E99 and .EAA..EZZ, plus the logical (.L01), SMART (.s01) and EWF2
// (.Ex01) spellings.
//
// It defers to the same naming rules the library uses, so a file this tool
// records as a segment is one the reader will later look for.
func isSegmentFile(path string) bool {
	_, _, ok := segname.ParseFamily(filepath.Ext(path))
	return ok
}

// sortSegments orders segment paths by segment number rather than by name.
// Sorting by name is close enough for a handful of segments and then quietly
// stops being right: past .E99 the progression continues .EAA, and a set
// written in mixed case misorders long before that.
func sortSegments(paths []string) {
	family, _, ok := segname.ParseFamily(filepath.Ext(paths[0]))
	if !ok {
		sort.Strings(paths)
		return
	}
	sort.Slice(paths, func(i, j int) bool {
		left, leftOK := family.Number(filepath.Ext(paths[i]))
		right, rightOK := family.Number(filepath.Ext(paths[j]))
		if !leftOK || !rightOK {
			return paths[i] < paths[j]
		}
		return left < right
	})
}

// parseAcquireLog extracts the digests ewfacquire computed over the source.
// A dialect that stores no hash section produces a log without them, which is
// not an error.
func parseAcquireLog(path string) (digestInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// Older builds only write a log on error; absence is not fatal.
		if os.IsNotExist(err) {
			return digestInfo{}, nil
		}
		return digestInfo{}, err
	}

	var out digestInfo
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		value := strings.ToLower(fields[len(fields)-1])
		switch {
		case strings.HasPrefix(line, "MD5 hash calculated over data:"):
			out.computedMD5 = value
		case strings.HasPrefix(line, "SHA1 hash calculated over data:"):
			out.computedSHA1 = value
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------

func mustGlob(pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	return matches
}

func copyFile(src, dst string) error {
	if abs1, err1 := filepath.Abs(src); err1 == nil {
		if abs2, err2 := filepath.Abs(dst); err2 == nil && abs1 == abs2 {
			return nil // already in place
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func sanitise(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}
