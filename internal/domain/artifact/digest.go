package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ParseDigest reconstructs an exact qualified content identity.
func ParseDigest(value string) (Digest, error) {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return Digest{}, ErrInvalidDigest
	}
	decoded, err := hex.DecodeString(value[7:])
	if err != nil || hex.EncodeToString(decoded) != value[7:] {
		return Digest{}, ErrInvalidDigest
	}
	var sum [32]byte
	copy(sum[:], decoded)
	return Digest{sum: sum, valid: true}, nil
}

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
