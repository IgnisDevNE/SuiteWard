package artifact

// Digest identifies exact content bytes. Its zero value has no identity.
type Digest struct {
	sum   [32]byte
	valid bool
}

// Hash identifies the supplied bytes without retaining them.
func Hash(content []byte) Digest {
	return Digest{}
}

// String returns a qualified lowercase hexadecimal digest, or empty when absent.
func (d Digest) String() string {
	return ""
}

// IsZero reports whether the digest is absent.
func (d Digest) IsZero() bool {
	return true
}
