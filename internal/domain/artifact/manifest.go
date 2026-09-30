package artifact

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
	return Manifest{}, nil
}

// Entries returns an independent copy of the canonically ordered inventory.
func (m Manifest) Entries() []Entry {
	return nil
}

// Digest identifies the manifest's versioned canonical encoding.
func (m Manifest) Digest() Digest {
	return Digest{}
}

// IsZero reports whether the manifest is absent.
func (m Manifest) IsZero() bool {
	return true
}
