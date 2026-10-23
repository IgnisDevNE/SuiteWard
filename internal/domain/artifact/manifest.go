package artifact

import (
	"bytes"
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
	ErrInvalidDigest = errors.New("invalid content digest")
	// ErrInvalidEncoding reports bytes that are not the canonical encoding of a manifest.
	ErrInvalidEncoding = errors.New("invalid manifest encoding")
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
	for i, entry := range ordered {
		if !ValidPath(entry.Path) {
			return Manifest{}, fmt.Errorf("%w: %q", ErrInvalidPath, entry.Path)
		}
		if i > 0 && ordered[i-1].Path == entry.Path {
			return Manifest{}, fmt.Errorf("%w: %q", ErrDuplicatePath, entry.Path)
		}
		if entry.Content.IsZero() {
			return Manifest{}, fmt.Errorf("%w for %q", ErrInvalidDigest, entry.Path)
		}
	}
	return Manifest{entries: ordered, digest: Hash(encodeEntries(ordered))}, nil
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

// manifestMagic starts the canonical encoding, domain- and version-separated. A big-endian uint64 entry count follows, then each entry in
// path order as a big-endian uint64 path length, the path and the raw 32 bytes of its digest.
const manifestMagic = "suiteward.manifest.v1\x00"

// minEncodedEntry is the least an entry takes in the encoding: the length of its path and its digest.
const minEncodedEntry = 8 + 32

// encodeEntries writes the canonical encoding of entries, which the caller has validated and put in path order.
func encodeEntries(entries []Entry) []byte {
	encoded := binary.BigEndian.AppendUint64([]byte(manifestMagic), uint64(len(entries)))
	for _, entry := range entries {
		encoded = binary.BigEndian.AppendUint64(encoded, uint64(len(entry.Path)))
		encoded = append(encoded, entry.Path...)
		encoded = append(encoded, entry.Content.sum[:]...)
	}
	return encoded
}

// Encoding returns the canonical encoding that identifies the manifest: the bytes whose SHA-256 is its Digest, so a content store that
// addresses bytes by their digest can hold the manifest under it. It is nil for an absent manifest, and a copy the caller may change.
func (m Manifest) Encoding() []byte {
	if m.IsZero() {
		return nil
	}
	return encodeEntries(m.entries)
}

// ParseManifest reads the canonical encoding of a manifest and refuses every other byte sequence with ErrInvalidEncoding: another format
// or version, a count or a length beyond the bytes, trailing bytes, an invalid or repeated path, and entries out of path order (which no
// manifest encodes). A manifest it returns has Hash(raw) as its digest.
func ParseManifest(raw []byte) (Manifest, error) {
	rest, ok := bytes.CutPrefix(raw, []byte(manifestMagic))
	if !ok || len(rest) < 8 {
		return Manifest{}, fmt.Errorf("%w: not a manifest", ErrInvalidEncoding)
	}
	count := binary.BigEndian.Uint64(rest)
	rest = rest[8:]
	if count > uint64(len(rest))/minEncodedEntry { // so that no count makes the parser allocate more than the bytes hold
		return Manifest{}, fmt.Errorf("%w: a count of entries beyond the bytes", ErrInvalidEncoding)
	}
	var entries []Entry
	for range count {
		if len(rest) < 8 {
			return Manifest{}, fmt.Errorf("%w: cut in an entry", ErrInvalidEncoding)
		}
		size := binary.BigEndian.Uint64(rest)
		rest = rest[8:]
		if size > uint64(len(rest)) || uint64(len(rest))-size < 32 {
			return Manifest{}, fmt.Errorf("%w: a path beyond the bytes", ErrInvalidEncoding)
		}
		entry := Entry{Path: string(rest[:size]), Content: Digest{valid: true}}
		copy(entry.Content.sum[:], rest[size:size+32])
		entries = append(entries, entry)
		rest = rest[size+32:]
	}
	if len(rest) != 0 {
		return Manifest{}, fmt.Errorf("%w: trailing bytes", ErrInvalidEncoding)
	}
	m, err := NewManifest(entries)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: %w", ErrInvalidEncoding, err)
	}
	if Hash(raw) != m.digest {
		return Manifest{}, fmt.Errorf("%w: the entries are not in path order", ErrInvalidEncoding)
	}
	return m, nil
}

// ValidPath reports whether path is a well-formed repository-relative,
// slash-separated file path: valid UTF-8, free of NUL, backslash and colon,
// and free of empty, "." or ".." segments.
func ValidPath(path string) bool {
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
