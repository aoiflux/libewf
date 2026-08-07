package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/aoiflux/libewf"
	"github.com/aoiflux/libewf/metadata"
	libtable "github.com/aoiflux/libtable"
)

const usage = "usage: offsets [-pt-offset N] [-verbose-detect] [-dump-sectors N] <image.E01> [segment2.E02 ...]\n" +
	"       one path is enough: the rest of the segment set is discovered"

// args holds parsed command-line arguments.
type args struct {
	ptOffset      uint64
	verboseDetect bool
	dumpSectors   int
	segments      []string
}

// detectAttempt describes one partition-table probe strategy.
type detectAttempt struct {
	label string
	opts  libtable.Options
	size  uint64
}

func main() {
	a := parseArgs()

	segments, err := resolveSegments(a.segments)
	if err != nil {
		fmt.Fprintln(os.Stderr, "segment set error:", err)
		os.Exit(1)
	}

	sources, closeAll, err := openFiles(segments)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open error:", err)
		os.Exit(1)
	}
	defer closeAll()

	r, err := openEWFReader(sources)
	if err != nil {
		fmt.Fprintln(os.Stderr, "libewf error:", err)
		os.Exit(1)
	}
	defer r.Close()

	meta := r.Metadata()
	printImageReport(meta, segments, a.ptOffset)

	if a.dumpSectors > 0 {
		dumpStreamSectors(r, a.dumpSectors)
	}

	tbl, strategy, err := detectPartitionTable(r, meta, a.ptOffset, a.verboseDetect)
	if err != nil {
		fsType, fsOff := probeFilesystems(r, candidateOffsets(a.ptOffset), a.verboseDetect)
		if fsType != "" {
			fmt.Printf("Filesystem detected at offset %d: %s (no partition table)\n", fsOff, fsType)
			return
		}
		fmt.Printf("No partition table or filesystem detected: %v\n", err)
		return
	}

	fmt.Printf("Detection strategy: %s\n\n", strategy)
	printPartitionTable(tbl)
}

// --- argument parsing ---

func parseArgs() args {
	var a args
	flag.Uint64Var(&a.ptOffset, "pt-offset", 0, "byte offset where partition table parsing starts")
	flag.BoolVar(&a.verboseDetect, "verbose-detect", false, "print all partition/filesystem detection attempts")
	flag.IntVar(&a.dumpSectors, "dump-sectors", 0, "hex dump of first N sectors (512 bytes each) from decoded stream")
	flag.Usage = func() { fmt.Println(usage) }
	flag.Parse()

	a.segments = flag.Args()
	if len(a.segments) == 0 {
		fmt.Println(usage)
		os.Exit(1)
	}
	return a
}

// --- I/O helpers ---

// resolveSegments expands a single path into the whole segment set, so that the
// report names every file the image was decoded from rather than only the one
// that was typed. Several paths are taken as given, for a set whose files do
// not follow the EWF naming progression.
func resolveSegments(paths []string) ([]string, error) {
	if len(paths) != 1 {
		return paths, nil
	}
	return libewf.SegmentPaths(paths[0])
}

func openFiles(paths []string) ([]io.ReaderAt, func(), error) {
	files := make([]*os.File, 0, len(paths))
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			for _, opened := range files {
				_ = opened.Close()
			}
			return nil, nil, err
		}
		files = append(files, f)
	}
	sources := make([]io.ReaderAt, len(files))
	for i, f := range files {
		sources[i] = f
	}
	closeAll := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	return sources, closeAll, nil
}

func openEWFReader(sources []io.ReaderAt) (libewf.Reader, error) {
	if len(sources) == 1 {
		return libewf.Open(sources[0])
	}
	return libewf.OpenSegments(sources)
}

func readBytes(r libewf.Reader, off int64, size int) ([]byte, error) {
	buf := make([]byte, size)
	n, err := r.ReadAt(buf, off)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if n <= 0 {
		return nil, io.EOF
	}
	return buf[:n], nil
}

// --- image report ---

func printImageReport(meta metadata.Info, segments []string, ptOffset uint64) {
	fmt.Printf("Input segments (%d):\n", len(segments))
	for i, path := range segments {
		fmt.Printf("  [%d] %s\n", i+1, path)
	}
	fmt.Printf("Partition parse offset: %d bytes\n\n", ptOffset)

	fmt.Println("EWF metadata")
	fmt.Printf("  version            : %d.%d\n", meta.MajorVersion, meta.MinorVersion)
	fmt.Printf("  segment_number     : %d\n", meta.SegmentNumber)
	fmt.Printf("  compression_method : %d\n", meta.CompressionMethod)
	fmt.Printf("  section_count      : %d\n", meta.SectionCount)
	fmt.Printf("  has_next           : %t\n", meta.HasNextSection)
	fmt.Printf("  has_done           : %t\n", meta.HasDoneSection)
	fmt.Printf("  encrypted          : %t\n", meta.IsEncrypted)
	fmt.Printf("  integrity_hashes   : %t\n", meta.HasIntegrityHashBlocks)
	if meta.HasMD5Digest {
		fmt.Printf("  md5                : %s\n", hex.EncodeToString(meta.MD5Digest[:]))
	}
	if meta.HasSHA1Digest {
		fmt.Printf("  sha1               : %s\n", hex.EncodeToString(meta.SHA1Digest[:]))
	}

	if meta.Media != nil {
		m := meta.Media
		fmt.Println("  Media geometry")
		fmt.Printf("    bytes_per_sector  : %d\n", m.BytesPerSector)
		fmt.Printf("    sectors_per_chunk : %d\n", m.SectorsPerChunk)
		fmt.Printf("    number_of_sectors : %d\n", m.NumberOfSectors)
		fmt.Printf("    number_of_chunks  : %d\n", m.NumberOfChunks)
		fmt.Printf("    total_size        : %s\n", formatSize(uint64(m.BytesPerSector)*m.NumberOfSectors))
		fmt.Printf("    media_type        : %d\n", m.MediaType)
		fmt.Printf("    media_flags       : 0x%02x\n", m.MediaFlags)
		fmt.Printf("    compression_level : %d\n", m.CompressionLevel)
		fmt.Printf("    error_granularity : %d\n", m.ErrorGranularity)
		fmt.Printf("    set_identifier    : %s\n", hex.EncodeToString(m.SetIdentifier[:]))
	}

	if len(meta.Sessions) > 0 {
		fmt.Printf("  sessions           : %d\n", len(meta.Sessions))
	}
	if len(meta.AcquisitionErrors) > 0 {
		fmt.Printf("  acquisition_errors : %d\n", len(meta.AcquisitionErrors))
	}

	if len(meta.SectionTypeCounts) > 0 {
		fmt.Println("  Section type counts")
		types := make([]uint32, 0, len(meta.SectionTypeCounts))
		for t := range meta.SectionTypeCounts {
			types = append(types, t)
		}
		sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
		for _, t := range types {
			fmt.Printf("    type %-2d : %d\n", t, meta.SectionTypeCounts[t])
		}
	}
	fmt.Println()
}

// --- partition table printing ---

func printPartitionTable(tbl *libtable.Table) {
	backup := ""
	if tbl.IsBackup {
		backup = " (backup copy)"
	}
	fmt.Println("Partition table")
	fmt.Printf("  type       : %s%s\n", tbl.Type, backup)
	fmt.Printf("  block_size : %d bytes\n", tbl.BlockSize)
	fmt.Printf("  offset     : %d bytes\n", tbl.Offset)
	fmt.Printf("  partitions : %d\n\n", len(tbl.Partitions))

	if len(tbl.Partitions) == 0 {
		fmt.Println("  (no partitions)")
		return
	}

	for _, p := range tbl.Partitions {
		printPartition(tbl, p)
	}
}

func printPartition(tbl *libtable.Table, p libtable.Partition) {
	flags := partFlagsString(p.Flags)
	startByte := tbl.Offset + p.StartLBA*uint64(tbl.BlockSize)
	sizeBytes := p.LengthLBA * uint64(tbl.BlockSize)

	fmt.Printf("  Partition %d  [%s]\n", p.Index, flags)
	fmt.Printf("    start_lba   : %d  (byte offset %d)\n", p.StartLBA, startByte)
	fmt.Printf("    length      : %d sectors  (%s)\n", p.LengthLBA, formatSize(sizeBytes))
	fmt.Printf("    type        : %s  (code 0x%x)\n", p.TypeName, p.TypeCode)
	if p.GUIDType != "" {
		fmt.Printf("    guid_type   : %s\n", p.GUIDType)
	}
	if p.GUIDUnique != "" {
		fmt.Printf("    guid_unique : %s\n", p.GUIDUnique)
	}
	if p.Name != "" {
		fmt.Printf("    name        : %s\n", p.Name)
	}
	if p.Attributes != 0 {
		fmt.Printf("    attributes  : 0x%016x\n", p.Attributes)
	}
	if p.TableNumber >= 0 || p.SlotNumber >= 0 {
		fmt.Printf("    slot        : table=%d slot=%d\n", p.TableNumber, p.SlotNumber)
	}
	fmt.Println()
}

func partFlagsString(f libtable.PartFlag) string {
	var parts []string
	if f&libtable.PartFlagAlloc != 0 {
		parts = append(parts, "allocated")
	}
	if f&libtable.PartFlagUnalloc != 0 {
		parts = append(parts, "unallocated")
	}
	if f&libtable.PartFlagMeta != 0 {
		parts = append(parts, "meta")
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, ", ")
}

func formatSize(b uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)
	switch {
	case b >= TB:
		return fmt.Sprintf("%.2f TB", float64(b)/TB)
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/GB)
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/MB)
	case b >= KB:
		return fmt.Sprintf("%.2f KB", float64(b)/KB)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// --- partition table detection ---

func detectPartitionTable(r libewf.Reader, meta metadata.Info, startOffset uint64, verbose bool) (*libtable.Table, string, error) {
	offsets := candidateOffsets(startOffset)
	sizeKnown, imageSize := imageSizeFromMetadata(meta)

	if verbose {
		fmt.Printf("[detect] candidate offsets: %v\n", offsets)
		if sizeKnown {
			fmt.Printf("[detect] image size: %d bytes\n", imageSize)
		} else {
			fmt.Println("[detect] image size: unknown")
		}
	}

	var lastErr error
	for _, off := range offsets {
		for _, attempt := range buildAttempts(off, sizeKnown, imageSize) {
			if verbose {
				fmt.Printf("[detect] trying offset=%d mode=%s size=%d\n", off, attempt.label, attempt.size)
			}
			tbl, err := libtable.Parse(r, attempt.size, attempt.opts)
			if err == nil {
				if verbose {
					fmt.Printf("[detect] success: offset=%d mode=%s\n", off, attempt.label)
				}
				return tbl, fmt.Sprintf("offset=%d, %s", off, attempt.label), nil
			}
			if verbose {
				fmt.Printf("[detect] failed: %v\n", err)
			}
			lastErr = err
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no partition table found")
	}
	return nil, "", lastErr
}

func buildAttempts(off uint64, sizeKnown bool, imageSize uint64) []detectAttempt {
	strict := libtable.Options{Type: libtable.TypeUnknown, Offset: off}
	relaxed := libtable.Options{Type: libtable.TypeUnknown, Offset: off, GPTDisableCRC: true}

	attempts := []detectAttempt{
		{label: "unknown-size/strict", opts: strict, size: 0},
		{label: "unknown-size/gpt-crc-relax", opts: relaxed, size: 0},
	}
	if sizeKnown {
		attempts = append(attempts,
			detectAttempt{label: "known-size/strict", opts: strict, size: imageSize},
			detectAttempt{label: "known-size/gpt-crc-relax", opts: relaxed, size: imageSize},
		)
	}
	return attempts
}

func imageSizeFromMetadata(meta metadata.Info) (bool, uint64) {
	if meta.Media == nil || meta.Media.BytesPerSector == 0 || meta.Media.NumberOfSectors == 0 {
		return false, 0
	}
	return true, uint64(meta.Media.BytesPerSector) * meta.Media.NumberOfSectors
}

func candidateOffsets(requested uint64) []uint64 {
	seen := make(map[uint64]struct{})
	all := []uint64{requested, 0, 63 * 512, 2048 * 512, 4096}
	out := make([]uint64, 0, len(all))
	for _, off := range all {
		if _, dup := seen[off]; dup {
			continue
		}
		seen[off] = struct{}{}
		out = append(out, off)
	}
	return out
}

// --- filesystem detection (fallback when no partition table found) ---

func probeFilesystems(r libewf.Reader, offsets []uint64, verbose bool) (string, uint64) {
	for _, off := range offsets {
		if verbose {
			fmt.Printf("[detect] probing filesystem at offset=%d\n", off)
		}
		fsType := detectFilesystem(r, int64(off))
		if fsType != "" {
			if verbose {
				fmt.Printf("[detect] filesystem match: offset=%d type=%s\n", off, fsType)
			}
			return fsType, off
		}
	}
	if verbose {
		fmt.Println("[detect] no filesystem signatures matched")
	}
	return "", 0
}

func detectFilesystem(r libewf.Reader, baseOffset int64) string {
	if boot, err := readBytes(r, baseOffset, 4096); err == nil {
		switch {
		case hasMarker(boot, 3, "NTFS    "):
			return "NTFS"
		case hasMarker(boot, 82, "FAT32   "):
			return "FAT32"
		case hasMarker(boot, 3, "EXFAT   "):
			return "exFAT"
		case hasMarker(boot, 32, "NXSB"):
			return "APFS"
		}
	}
	if ext, err := readBytes(r, baseOffset+1024, 2048); err == nil && len(ext) >= 58 {
		if ext[56] == 0x53 && ext[57] == 0xef {
			return "ext2/3/4"
		}
		if ext[0] == 'H' && (ext[1] == '+' || ext[1] == 'X') {
			return "HFS+"
		}
	}
	return ""
}

func hasMarker(data []byte, off int, marker string) bool {
	if off < 0 || off+len(marker) > len(data) {
		return false
	}
	return string(data[off:off+len(marker)]) == marker
}

// --- hex dump ---

func dumpStreamSectors(r libewf.Reader, sectors int) {
	buf := make([]byte, sectors*512)
	n, err := r.ReadAt(buf, 0)
	buf = buf[:n]

	fmt.Printf("\n--- hex dump: first %d sector(s) from decoded stream (%d bytes) ---\n", sectors, n)
	const cols = 16
	for i := 0; i < len(buf); i += cols {
		row := buf[i:]
		if len(row) > cols {
			row = row[:cols]
		}
		var hexPart strings.Builder
		var asciiPart strings.Builder
		for j, b := range row {
			if j == 8 {
				hexPart.WriteByte(' ')
			}
			fmt.Fprintf(&hexPart, "%02x ", b)
			if b >= 0x20 && b < 0x7f {
				asciiPart.WriteByte(b)
			} else {
				asciiPart.WriteByte('.')
			}
		}
		for j := len(row); j < cols; j++ {
			if j == 8 {
				hexPart.WriteByte(' ')
			}
			hexPart.WriteString("   ")
		}
		fmt.Printf("%08x  %-*s |%s|\n", i, cols*3+1, hexPart.String(), asciiPart.String())
	}

	if err != nil && err != io.EOF {
		fmt.Printf("read error after %d bytes: %v\n", n, err)
	}
	fmt.Println("---")
}
