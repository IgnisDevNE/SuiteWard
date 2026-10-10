package main

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

type treeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// protectedDigest hashes the lines "<path>\0<blob sha>\n" of every blob under
// prefix, sorted by path, and reports how many files it covered.
func protectedDigest(entries []treeEntry, prefix string) (digest string, files int) {
	var set []treeEntry
	for _, e := range entries {
		if e.Type == "blob" && strings.HasPrefix(e.Path, prefix) {
			set = append(set, e)
		}
	}
	sort.Slice(set, func(i, j int) bool { return set[i].Path < set[j].Path })
	h := sha256.New()
	for _, e := range set {
		h.Write([]byte(e.Path + "\x00" + e.SHA + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), len(set)
}
