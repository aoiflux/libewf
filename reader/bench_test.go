package reader

import (
	"bytes"
	"fmt"
	"testing"
)

// benchImage builds an image with realistic geometry: 32 KiB chunks of
// 512-byte sectors, the shape almost every real acquisition uses.
func benchImage(tb testing.TB, chunkCount int, compress bool) []byte {
	tb.Helper()

	const (
		sectorsPerChunk = 64
		bytesPerSector  = 512
		chunkSize       = sectorsPerChunk * bytesPerSector
	)

	chunks := make([][]byte, chunkCount)
	for i := range chunks {
		payload := make([]byte, chunkSize)
		// Partly compressible, partly not, so deflate does real work rather
		// than collapsing the chunk to nothing.
		for j := range payload {
			switch {
			case j%3 == 0:
				payload[j] = byte(i)
			case j%3 == 1:
				payload[j] = 0
			default:
				payload[j] = byte(j*31 + i*7)
			}
		}
		chunks[i] = payload
	}

	spec := v1Image{
		segmentNumber:   1,
		terminal:        "done",
		groups:          [][][]byte{chunks},
		withTable2:      true,
		addTrailer:      !compress,
		compress:        compress,
		sectorsPerChunk: sectorsPerChunk,
		bytesPerSector:  bytesPerSector,
		numberOfSectors: uint64(chunkCount * sectorsPerChunk),
		numberOfChunks:  uint32(chunkCount),
	}
	return spec.build()
}

func benchReader(tb testing.TB, image []byte, opts Options) *ImageReader {
	tb.Helper()
	r, err := OpenWithOptions(bytes.NewReader(image), opts)
	if err != nil {
		tb.Fatalf("Open() error = %v", err)
	}
	return r
}

// readPattern describes how a caller slices up its reads. Filesystem parsers
// issue small unaligned reads; imaging tools stream whole chunks.
type readPattern struct {
	name string
	size int
	// stride advances the offset between reads. A stride smaller than the
	// chunk size means consecutive reads land in the same chunk, which is
	// where caching pays off.
	stride int64
}

var readPatterns = []readPattern{
	{"sector512", 512, 512},
	{"unaligned1k", 1024, 1000},
	{"page4k", 4096, 4096},
	{"wholechunk32k", 32768, 32768},
}

func benchmarkReadAt(b *testing.B, compress bool, opts Options) {
	const chunkCount = 64
	image := benchImage(b, chunkCount, compress)
	r := benchReader(b, image, opts)
	size := r.Size()

	for _, p := range readPatterns {
		b.Run(p.name, func(b *testing.B) {
			buf := make([]byte, p.size)
			b.SetBytes(int64(p.size))
			b.ReportAllocs()
			b.ResetTimer()

			off := int64(0)
			for i := 0; i < b.N; i++ {
				if off+int64(p.size) > size {
					off = 0
				}
				if _, err := r.ReadAt(buf, off); err != nil {
					b.Fatalf("ReadAt(%d) error = %v", off, err)
				}
				off += p.stride
			}
		})
	}
}

func BenchmarkReadAtDeflate(b *testing.B) {
	benchmarkReadAt(b, true, Options{})
}

func BenchmarkReadAtUncompressed(b *testing.B) {
	benchmarkReadAt(b, false, Options{})
}

// BenchmarkReadAtCacheSize shows how cache depth trades memory for throughput
// on a scattered access pattern, which is what a filesystem parser produces.
func BenchmarkReadAtCacheSize(b *testing.B) {
	const chunkCount = 64
	image := benchImage(b, chunkCount, true)

	for _, chunks := range []int{-1, 1, 4, 16, 64} {
		label := fmt.Sprintf("chunks=%d", chunks)
		if chunks < 0 {
			label = "disabled"
		}
		b.Run(label, func(b *testing.B) {
			r := benchReader(b, image, Options{ChunkCacheChunks: chunks})
			buf := make([]byte, 512)
			b.SetBytes(512)
			b.ReportAllocs()
			b.ResetTimer()

			// Walk sectors in order: 64 consecutive reads per chunk, so a
			// cache of any depth should collapse them to one decode.
			off := int64(0)
			for i := 0; i < b.N; i++ {
				if off+512 > r.Size() {
					off = 0
				}
				if _, err := r.ReadAt(buf, off); err != nil {
					b.Fatalf("ReadAt(%d) error = %v", off, err)
				}
				off += 512
			}
		})
	}
}

// BenchmarkVerify measures the whole-device streaming path, which is what an
// integrity check costs.
func BenchmarkVerify(b *testing.B) {
	const chunkCount = 64
	image := benchImage(b, chunkCount, true)
	r := benchReader(b, image, Options{})

	buf := make([]byte, 32768)
	b.SetBytes(r.Size())
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		for off := int64(0); off < r.Size(); off += int64(len(buf)) {
			if _, err := r.ReadAt(buf, off); err != nil {
				b.Fatalf("ReadAt(%d) error = %v", off, err)
			}
		}
	}
}
