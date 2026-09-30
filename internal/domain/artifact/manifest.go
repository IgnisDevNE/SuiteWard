package artifact

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidPath   = errors.New("invalid manifest path")
	ErrDuplicatePath = errors.New("duplicate manifest path")
	ErrInvalidDigest = errors.New("absent content digest")
)

// Entry binds an exact relative path to its content identity.
type Entry struct {
	Path    string
	Content Digest
}

// Manifest is an immutable protected inventory. Its zero value is absent.
type Manifest struct {
	entries []Entry
	digest  Digest
}

// NewManifest validates and copies entries in canonical path order.
func NewManifest(entries []Entry) (Manifest, error) {
	ordered := slices.Clone(entries)
	slices.SortFunc(ordered, func(left, right Entry) int {
		return strings.Compare(left.Path, right.Path)
	})

	// Encoding v1 is domain/version-separated, followed by a big-endian uint64
	// entry count and length-prefixed paths with their raw 32-byte digests.
	encoded := []byte("suiteward.manifest.v1\x00")
	encoded = binary.BigEndian.AppendUint64(encoded, uint64(len(ordered)))
	for i, entry := range ordered {
		if !validPath(entry.Path) {
			return Manifest{}, fmt.Errorf("%w: %q", ErrInvalidPath, entry.Path)
		}
		if i > 0 && ordered[i-1].Path == entry.Path {
			return Manifest{}, fmt.Errorf("%w: %q", ErrDuplicatePath, entry.Path)
		}
		if entry.Content.IsZero() {
			return Manifest{}, fmt.Errorf("%w for %q", ErrInvalidDigest, entry.Path)
		}
		encoded = binary.BigEndian.AppendUint64(encoded, uint64(len(entry.Path)))
		encoded = append(encoded, entry.Path...)
		encoded = append(encoded, entry.Content.sum[:]...)
	}
	return Manifest{entries: ordered, digest: Hash(encoded)}, nil
}

// Entries returns an independent copy of the canonically ordered inventory.
func (m Manifest) Entries() []Entry {
	return slices.Clone(m.entries)
}

// Digest identifies the manifest's versioned canonical encoding.
func (m Manifest) Digest() Digest {
	return m.digest
}

// IsZero reports whether the manifest is absent.
func (m Manifest) IsZero() bool {
	return m.digest.IsZero()
}

func validPath(path string) bool {
	if !utf8.ValidString(path) || strings.ContainsAny(path, "\x00\\:") {
		return false
	}
	for _, component := range strings.Split(path, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}
