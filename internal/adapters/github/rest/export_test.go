package rest

import "testing"

// LimitBodiesTo lowers the response body limit for the duration of the test.
func LimitBodiesTo(t testing.TB, n int64) {
	t.Helper()
	old := maxBody
	maxBody = n
	t.Cleanup(func() { maxBody = old })
}

// LimitCacheTo lowers the bound of the ETag cache for the duration of the test.
func LimitCacheTo(t testing.TB, n int) {
	t.Helper()
	old := maxCacheEntries
	maxCacheEntries = n
	t.Cleanup(func() { maxCacheEntries = old })
}

// CacheLen is the number of ETags the client holds.
func (c *Client) CacheLen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cache)
}

// LimitCacheBytesTo lowers the byte bound of the ETag cache for the duration of the test.
func LimitCacheBytesTo(t testing.TB, n int) {
	t.Helper()
	old := maxCacheBytes
	maxCacheBytes = n
	t.Cleanup(func() { maxCacheBytes = old })
}

// CacheBytes is the total size of the bodies the client caches.
func (c *Client) CacheBytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cacheBytes
}
