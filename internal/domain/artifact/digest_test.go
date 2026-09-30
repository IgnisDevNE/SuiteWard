package artifact_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

func TestHashKnownContent(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{"nil", nil, "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"empty", []byte{}, "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", []byte("abc"), "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := artifact.Hash(tt.content)
			if got.String() != tt.want {
				t.Errorf("Hash(%q) = %q, want %q", tt.content, got.String(), tt.want)
			}
			if got.IsZero() {
				t.Error("hashed content must have an identity, including empty content")
			}
		})
	}
}

func TestDigestAbsentIsDifferentFromEmptyContent(t *testing.T) {
	var absent artifact.Digest
	if !absent.IsZero() || absent.String() != "" {
		t.Fatal("unconstructed digest must be absent with an empty display string")
	}
	if absent == artifact.Hash(nil) {
		t.Fatal("absent digest must differ from the identity of empty content")
	}
}

func TestHashPreservesExactBytes(t *testing.T) {
	tests := []struct {
		name        string
		left, right []byte
	}{
		{"content change", []byte("abc"), []byte("abd")},
		{"line endings", []byte("test\n"), []byte("test\r\n")},
		{"unicode", []byte("caf\u00e9"), []byte("cafe\u0301")},
		{"binary", []byte{0, 255, 1}, []byte{0, 255, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if artifact.Hash(tt.left) == artifact.Hash(tt.right) {
				t.Fatal("different exact byte sequences must not share the tested identity")
			}
			copyOfLeft := append([]byte(nil), tt.left...)
			if artifact.Hash(tt.left) != artifact.Hash(copyOfLeft) {
				t.Fatal("identical bytes must have identical digests")
			}
		})
	}
}

func TestDigestDoesNotRetainInputBytes(t *testing.T) {
	content := []byte("abc")
	digest := artifact.Hash(content)
	content[0] = 'z'
	if digest != artifact.Hash([]byte("abc")) {
		t.Fatal("mutating the caller's bytes changed an existing digest")
	}
}
