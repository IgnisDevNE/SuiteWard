package main

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestProtectedDigest(t *testing.T) {
	tests := []struct {
		name    string
		entries []treeEntry
		want    string
		files   int
	}{
		{"empty tree", nil, sum(""), 0},
		{"nothing under the prefix", []treeEntry{{"README.md", "blob", "a1"}, {"src/x.go", "blob", "a2"}}, sum(""), 0},
		{
			"sorted by path whatever the input order",
			[]treeEntry{{"tests/b.go", "blob", "bb"}, {"tests/a.go", "blob", "aa"}},
			sum("tests/a.go\x00aa\ntests/b.go\x00bb\n"), 2,
		},
		{
			"prefix filter keeps only blobs under tests/",
			[]treeEntry{
				{"tests", "tree", "t0"},          // the directory itself
				{"tests/sub", "tree", "t1"},      // nested directory
				{"tests/sub/c.go", "blob", "cc"}, // kept
				{"tests_extra/d.go", "blob", "dd"},
				{"src/tests/e.go", "blob", "ee"},
				{"tests/mod", "commit", "ff"}, // submodule
			},
			sum("tests/sub/c.go\x00cc\n"), 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, files := protectedDigest(tt.entries, "tests/")
			if got != tt.want || files != tt.files {
				t.Fatalf("digest = %s (%d files), want %s (%d files)", got, files, tt.want, tt.files)
			}
		})
	}
}
