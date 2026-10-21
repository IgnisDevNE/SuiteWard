package github

import (
	"bytes"
	"fmt"
	"testing"
)

func TestBlobCacheKeepsWhatItHoldsWithinItsByteBound(t *testing.T) {
	const bound = 10 << 10
	c := newBlobCache(bound)
	blob := bytes.Repeat([]byte("x"), 1<<10)
	for i := range 40 {
		key := fmt.Sprintf("repo/%d", i)
		c.put(key, blob)
		if c.size > bound {
			t.Fatalf("the cache holds %d bytes after %d blobs, over its bound of %d", c.size, i+1, bound)
		}
		if got, hit := c.get(key); !hit || !bytes.Equal(got, blob) {
			t.Fatalf("the blob just put is not kept: hit = %v", hit)
		}
	}
}

func TestBlobCacheDoesNotKeepABlobLargerThanItsBound(t *testing.T) {
	c := newBlobCache(1 << 10)
	c.put("small", []byte("s"))
	c.put("large", bytes.Repeat([]byte("x"), 2<<10))
	if _, hit := c.get("large"); hit {
		t.Error("a blob larger than the bound was kept")
	}
	if _, hit := c.get("small"); !hit {
		t.Error("a blob larger than the bound evicted the cache")
	}
}

func TestBlobCacheHandsOutCopies(t *testing.T) {
	c := newBlobCache(1 << 10)
	stored := []byte("abc")
	c.put("k", stored)
	stored[0] = 'X'
	first, hit := c.get("k")
	if !hit {
		t.Fatal("the blob just put is not kept")
	}
	first[1] = 'Y'
	if again, _ := c.get("k"); string(again) != "abc" {
		t.Errorf("the cache holds %q after its caller changed the bytes, want abc", again)
	}
}
