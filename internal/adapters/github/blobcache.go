package github

// blobCache is a stub; the implementation follows the tests.
type blobCache struct{ size int }

func newBlobCache(max int) *blobCache { return &blobCache{} }

func (c *blobCache) get(key string) ([]byte, bool) { return nil, false }

func (c *blobCache) put(key string, data []byte) {}
