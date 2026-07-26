package reader

import (
	"bytes"
	"io"
	"sync"
	"testing"
)

func TestChunkCacheStoresAndRetrieves(t *testing.T) {
	c := newChunkCache(4)

	c.put(7, []byte("seven"))
	got, ok := c.get(7)
	if !ok {
		t.Fatal("get(7) reported a miss after put")
	}
	if string(got) != "seven" {
		t.Fatalf("get(7) = %q, want %q", got, "seven")
	}
	if _, ok := c.get(8); ok {
		t.Fatal("get(8) reported a hit for a chunk never stored")
	}
}

func TestChunkCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newChunkCache(3)

	c.put(1, []byte("one"))
	c.put(2, []byte("two"))
	c.put(3, []byte("three"))

	// Touch 1 so 2 becomes the least recently used.
	if _, ok := c.get(1); !ok {
		t.Fatal("get(1) missed")
	}

	c.put(4, []byte("four"))

	if c.len() != 3 {
		t.Fatalf("len() = %d, want 3", c.len())
	}
	if _, ok := c.get(2); ok {
		t.Error("chunk 2 survived; it was the least recently used and should have been evicted")
	}
	for _, chunk := range []int{1, 3, 4} {
		if _, ok := c.get(chunk); !ok {
			t.Errorf("chunk %d was evicted unexpectedly", chunk)
		}
	}
}

func TestChunkCacheReplacesExistingEntry(t *testing.T) {
	c := newChunkCache(2)
	c.put(1, []byte("first"))
	c.put(1, []byte("second"))

	if c.len() != 1 {
		t.Fatalf("len() = %d, want 1 after replacing the same chunk", c.len())
	}
	got, _ := c.get(1)
	if string(got) != "second" {
		t.Fatalf("get(1) = %q, want %q", got, "second")
	}
}

// TestChunkCacheNilIsUsableDisabled pins the nil-receiver contract, which is
// what lets the read path skip nil checks.
func TestChunkCacheNilIsUsableDisabled(t *testing.T) {
	var c *chunkCache

	c.put(1, []byte("ignored"))
	if _, ok := c.get(1); ok {
		t.Error("a disabled cache reported a hit")
	}
	if c.len() != 0 {
		t.Errorf("len() = %d, want 0", c.len())
	}
	if newChunkCache(0) != nil || newChunkCache(-1) != nil {
		t.Error("newChunkCache should return nil for a non-positive depth")
	}
}

func TestEffectiveCacheChunks(t *testing.T) {
	const mib = 1 << 20

	for _, tc := range []struct {
		name      string
		requested int
		chunkSize int64
		want      int
	}{
		{"negative disables", -1, 32 << 10, 0},
		{"zero selects the default", 0, 32 << 10, defaultChunkCacheChunks},
		{"explicit depth is honoured", 4, 32 << 10, 4},
		{"unknown chunk size disables", 8, 0, 0},
		// 16 MiB chunks: the 64 MiB ceiling allows only four.
		{"byte ceiling clamps the depth", 32, 16 * mib, 4},
		// A chunk larger than the whole ceiling still gets one slot, because
		// caching it removes the repeated decode of sequential reads inside it.
		{"oversized chunk still gets one slot", 8, 128 * mib, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveCacheChunks(tc.requested, tc.chunkSize); got != tc.want {
				t.Fatalf("effectiveCacheChunks(%d, %d) = %d, want %d",
					tc.requested, tc.chunkSize, got, tc.want)
			}
		})
	}
}

// TestCachedReadsMatchUncached is the test that matters: the cache must be
// invisible. Any divergence between a cached and an uncached read is a
// correctness bug, not a performance one.
func TestCachedReadsMatchUncached(t *testing.T) {
	for _, compress := range []bool{false, true} {
		name := "uncompressed"
		if compress {
			name = "deflate"
		}
		t.Run(name, func(t *testing.T) {
			image := benchImage(t, 8, compress)

			uncached := benchReader(t, image, Options{ChunkCacheChunks: -1})
			// A depth of 2 against 8 chunks forces constant eviction, so
			// entries are repeatedly discarded and re-decoded.
			cached := benchReader(t, image, Options{ChunkCacheChunks: 2})

			if uncached.Size() != cached.Size() {
				t.Fatalf("Size() differs: %d vs %d", uncached.Size(), cached.Size())
			}

			// Walk forwards, then backwards, so eviction and re-fetch both run.
			offsets := make([]int64, 0, 256)
			for off := int64(0); off < cached.Size(); off += 997 {
				offsets = append(offsets, off)
			}
			for i := len(offsets) - 1; i >= 0; i-- {
				offsets = append(offsets, offsets[i])
			}

			for _, off := range offsets {
				want := make([]byte, 1024)
				nWant, errWant := uncached.ReadAt(want, off)

				got := make([]byte, 1024)
				nGot, errGot := cached.ReadAt(got, off)

				if nWant != nGot {
					t.Fatalf("at offset %d: cached read %d bytes, uncached read %d", off, nGot, nWant)
				}
				if (errWant == nil) != (errGot == nil) {
					t.Fatalf("at offset %d: errors differ: cached=%v uncached=%v", off, errGot, errWant)
				}
				if !bytes.Equal(want[:nWant], got[:nGot]) {
					t.Fatalf("at offset %d: cached bytes differ from uncached", off)
				}
			}
		})
	}
}

// TestCacheConcurrentReadsAgree exercises the cache from many goroutines at
// once. Run under -race this also covers the locking.
func TestCacheConcurrentReadsAgree(t *testing.T) {
	image := benchImage(t, 8, true)
	r := benchReader(t, image, Options{ChunkCacheChunks: 3})

	want := make([]byte, r.Size())
	if _, err := r.ReadAt(want, 0); err != nil && err != io.EOF {
		t.Fatalf("ReadAt() error = %v", err)
	}

	var wg sync.WaitGroup
	for worker := 0; worker < 12; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			buf := make([]byte, 4096)
			// Each worker starts at a different offset so they contend for
			// different chunks and force eviction.
			start := int64(worker) * 4096
			for off := start; off+4096 <= r.Size(); off += 4096 * 3 {
				n, err := r.ReadAt(buf, off)
				if err != nil && err != io.EOF {
					t.Errorf("ReadAt(%d) error = %v", off, err)
					return
				}
				if !bytes.Equal(buf[:n], want[off:off+int64(n)]) {
					t.Errorf("worker %d: bytes at offset %d differ under concurrency", worker, off)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
}
