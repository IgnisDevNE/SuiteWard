package artifact

import (
	"crypto/sha256"
	"encoding/hex"
)

// Digest identifies exact content bytes. Its zero value has no identity.
type Digest struct {
	sum   [32]byte
	valid bool
}

// Hash identifies the supplied bytes without retaining them.
func Hash(content []byte) Digest {
	return Digest{sum: sha256.Sum256(content), valid: true}
}

// String returns a qualified lowercase hexadecimal digest, or empty when absent.
func (d Digest) String() string {
	if d.IsZero() {
		return ""
	}
	return "sha256:" + hex.EncodeToString(d.sum[:])
}

// IsZero reports whether the digest is absent.
func (d Digest) IsZero() bool {
	return !d.valid
}
