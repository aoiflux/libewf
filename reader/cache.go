package reader

import (
	"container/list"
	"sync"
)

const (
	// defaultChunkCacheChunks is the cache depth when none is requested.
	// A 32 KiB chunk is typical, so this costs about 512 KiB per reader while
	// collapsing the 64 sector-sized reads a filesystem parser makes within a
	// chunk down to a single decode.
	defaultChunkCacheChunks = 16

	// maxChunkCacheBytes bounds the cache regardless of the depth requested.
	// Chunk size comes from the image, so an unusual sectors-per-chunk value
	// could otherwise turn a modest-looking depth into a large allocation.
	maxChunkCacheBytes = 64 << 20
)

// chunkCache is a bounded LRU cache of decoded chunks.
//
// Without it every ReadAt re-reads and re-decompresses the chunk it lands in,
// so a caller issuing 512-byte reads across a 32 KiB chunk pays for 64
// decompressions of the same data.
//
// A nil *chunkCache is a valid disabled cache, so callers need no nil checks.
type chunkCache struct {
	mu      sync.Mutex
	entries map[int]*list.Element
	order   *list.List // front is most recently used
	max     int
}

type cacheEntry struct {
	chunk int
	data  []byte
}

// newChunkCache returns a cache holding up to max chunks, or nil when caching
// is disabled.
func newChunkCache(max int) *chunkCache {
	if max <= 0 {
		return nil
	}
	return &chunkCache{
		entries: make(map[int]*list.Element, max),
		order:   list.New(),
		max:     max,
	}
}

// get returns the cached bytes for a chunk.
//
// The returned slice is shared with the cache and with other callers, so it
// must be treated as read-only.
func (c *chunkCache) get(chunk int) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.entries[chunk]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(element)
	return element.Value.(*cacheEntry).data, true
}

// put stores a decoded chunk, evicting least-recently-used entries as needed.
func (c *chunkCache) put(chunk int, data []byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if element, ok := c.entries[chunk]; ok {
		element.Value.(*cacheEntry).data = data
		c.order.MoveToFront(element)
		return
	}

	c.entries[chunk] = c.order.PushFront(&cacheEntry{chunk: chunk, data: data})

	for c.order.Len() > c.max {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*cacheEntry).chunk)
	}
}

// len reports the number of cached chunks. Used by tests.
func (c *chunkCache) len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// effectiveCacheChunks resolves a requested depth against the byte ceiling.
//
// A negative request disables caching; zero selects the default. The result is
// clamped so that depth multiplied by chunk size stays within
// maxChunkCacheBytes, except that one chunk is always allowed: caching a single
// large chunk still removes the repeated decode of sequential reads inside it.
func effectiveCacheChunks(requested int, chunkSize int64) int {
	if requested < 0 || chunkSize <= 0 {
		return 0
	}
	if requested == 0 {
		requested = defaultChunkCacheChunks
	}

	ceiling := int(maxChunkCacheBytes / chunkSize)
	if ceiling < 1 {
		ceiling = 1
	}
	if requested > ceiling {
		return ceiling
	}
	return requested
}
