package artifact_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// rawManifest writes entries as given, in the format that NewManifest hashes: a magic, a big-endian uint64 count, then each entry as a
// big-endian uint64 path length, the path and the 32 raw bytes of its digest. It restates the format on purpose: it pins the bytes, not their producer.
func rawManifest(entries ...artifact.Entry) []byte {
	raw := binary.BigEndian.AppendUint64([]byte("suiteward.manifest.v1\x00"), uint64(len(entries)))
	for _, e := range entries {
		raw = binary.BigEndian.AppendUint64(raw, uint64(len(e.Path)))
		raw = append(raw, e.Path...)
		raw = append(raw, digestBytes(e.Content)...)
	}
	return raw
}

func digestBytes(d artifact.Digest) []byte {
	sum, err := hex.DecodeString(strings.TrimPrefix(d.String(), "sha256:"))
	if err != nil {
		panic(err)
	}
	return sum
}

func entry(path, content string) artifact.Entry {
	return artifact.Entry{Path: path, Content: artifact.Hash([]byte(content))}
}

func TestManifestEncodingIsWhatItsDigestHashes(t *testing.T) {
	many := make([]artifact.Entry, 50)
	for i := range many {
		many[i] = entry(fmt.Sprintf("tests/case_%02d_test.go", 49-i), fmt.Sprint(i))
	}
	for name, entries := range map[string][]artifact.Entry{
		"empty": nil, "one file": {entry("tests/a_test.go", "a")}, "unicode": {entry("tests/日本語_test.go", "a"), entry("tests/a b.go", "b")}, "fifty files out of order": many,
	} {
		t.Run(name, func(t *testing.T) {
			m := newManifest(t, entries)
			if artifact.Hash(m.Encoding()) != m.Digest() {
				t.Fatalf("the encoding hashes to %s, not to the digest %s", artifact.Hash(m.Encoding()), m.Digest())
			}
			sorted := m.Entries()
			if want := rawManifest(sorted...); !bytes.Equal(m.Encoding(), want) {
				t.Fatalf("encoding %x, want the format as written: %x", m.Encoding(), want)
			}
			m.Encoding()[0] ^= 0xff
			if artifact.Hash(m.Encoding()) != m.Digest() {
				t.Fatal("a caller changed the encoding the manifest keeps")
			}
		})
	}
	if (artifact.Manifest{}).Encoding() != nil {
		t.Fatal("an absent manifest has an encoding")
	}
}

func TestParseManifestReadsWhatEncodingWrote(t *testing.T) {
	for name, entries := range map[string][]artifact.Entry{
		"empty": nil, "one file": {entry("tests/a_test.go", "a")}, "two files": {entry("b", "b"), entry("a", "a")},
	} {
		t.Run(name, func(t *testing.T) {
			m := newManifest(t, entries)
			back, err := artifact.ParseManifest(m.Encoding())
			if err != nil || back.Digest() != m.Digest() || fmt.Sprint(back.Entries()) != fmt.Sprint(m.Entries()) || back.IsZero() {
				t.Fatalf("ParseManifest = %v, %v; want the manifest %s back", back.Entries(), err, m.Digest())
			}
		})
	}
}

func TestParseManifestRefusesWhatIsNotTheCanonicalEncodingOfAManifest(t *testing.T) {
	a, b := entry("a.txt", "a"), entry("b.txt", "b")
	good := rawManifest(a, b)
	huge := binary.BigEndian.AppendUint64([]byte("suiteward.manifest.v1\x00"), 1<<62)
	longPath := binary.BigEndian.AppendUint64(binary.BigEndian.AppendUint64([]byte("suiteward.manifest.v1\x00"), 1), 1<<62)
	for name, tc := range map[string]struct {
		raw  []byte
		also error // another sentinel the error wraps
	}{
		"nothing": {raw: nil}, "another format": {raw: []byte("not a manifest")}, "another version": {raw: append([]byte("suiteward.manifest.v2\x00"), good[22:]...)},
		"truncated": {raw: good[:len(good)-1]}, "cut in the count": {raw: good[:25]}, "trailing bytes": {raw: append(append([]byte{}, good...), 0)},
		"a count beyond the bytes": {raw: huge}, "a path beyond the bytes": {raw: longPath},
		"entries out of order": {raw: rawManifest(b, a)}, "a path that is not valid": {raw: rawManifest(entry("../x", "a")), also: artifact.ErrInvalidPath},
		"the same path twice": {raw: rawManifest(a, a), also: artifact.ErrDuplicatePath},
	} {
		t.Run(name, func(t *testing.T) {
			m, err := artifact.ParseManifest(tc.raw)
			if !errors.Is(err, artifact.ErrInvalidEncoding) || (tc.also != nil && !errors.Is(err, tc.also)) || !m.IsZero() {
				t.Fatalf("ParseManifest = %v, %v; want an invalid encoding (also %v) and no manifest", m.Entries(), err, tc.also)
			}
		})
	}
}

// FuzzParseManifest checks that bytes are either refused or exactly the encoding of the manifest they read, and that nothing panics.
func FuzzParseManifest(f *testing.F) {
	f.Add(rawManifest())
	f.Add(rawManifest(entry("a", "a"), entry("b", "b")))
	f.Add(rawManifest(entry("b", "b"), entry("a", "a")))
	f.Add(rawManifest(entry("../x", "a")))
	f.Add([]byte("suiteward.manifest.v1\x00"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		m, err := artifact.ParseManifest(raw)
		if err != nil {
			if !errors.Is(err, artifact.ErrInvalidEncoding) || !m.IsZero() {
				t.Fatalf("ParseManifest(%x) = %v, %v; want ErrInvalidEncoding and no manifest", raw, m.Entries(), err)
			}
			return
		}
		if !bytes.Equal(m.Encoding(), raw) || artifact.Hash(raw) != m.Digest() {
			t.Fatalf("ParseManifest(%x) read a manifest whose encoding is %x", raw, m.Encoding())
		}
	})
}
