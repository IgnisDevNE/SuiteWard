package github

import (
	"slices"
	"sync"
)

// maxBlobCacheBytes bounds the blob bytes a Reader keeps: the service container has 256 MiB, and a cold head of a large repository
// is a few MiB.
const maxBlobCacheBytes = 32 << 20

// blobCache keeps blob contents by repository and git blob SHA, which names the content, so a hit is the bytes GitHub would send.
// A pass cut short by the job timeout leaves what it read for the next one. It is safe for concurrent use.
type blobCache struct {
	mu    sync.Mutex
	max   int
	size  int
	blobs map[string][]byte
}

func newBlobCache(max int) *blobCache { return &blobCache{max: max, blobs: map[string][]byte{}} }

// get returns a copy of the blob kept under key.
func (c *blobCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.blobs[key]
	return slices.Clone(data), ok
}

// put keeps a copy of data under key. A blob larger than the bound is not kept.
// shortcut: a full cache is emptied whole, since one cold head fits several times over; upgrade to FIFO if heads outgrow that.
func (c *blobCache) put(key string, data []byte) {
	if len(data) > c.max {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, kept := c.blobs[key]; kept {
		c.size -= len(old)
	}
	if c.size+len(data) > c.max {
		clear(c.blobs)
		c.size = 0
	}
	c.blobs[key] = slices.Clone(data)
	c.size += len(data)
}
