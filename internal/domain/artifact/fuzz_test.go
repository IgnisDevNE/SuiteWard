package artifact_test

import (
	"errors"
	"path"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// FuzzValidPath checks the path grammar against an independent statement of it and against NewManifest, which must
// accept exactly the paths ValidPath accepts.
func FuzzValidPath(f *testing.F) {
	for _, seed := range []string{"tests/a_test.go", "tests/caf\u00e9.go", "", "a//b", "./a", "a/../b", "a\x5cb", "a:b", "a\x00b", "tests/\xff.go"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, p string) {
		ok := artifact.ValidPath(p)
		if ok {
			if !utf8.ValidString(p) || strings.ContainsAny(p, "\x00\x5c:") || path.IsAbs(p) || path.Clean(p) != p {
				t.Fatalf("ValidPath(%q) accepted a path that is not a clean relative slash path", p)
			}
		} else if utf8.ValidString(p) && !strings.ContainsAny(p, "\x00\x5c:") && path.Clean(p) == p && !path.IsAbs(p) && p != "." && p != ".." && !strings.HasPrefix(p, "../") {
			t.Fatalf("ValidPath(%q) rejected a clean relative slash path", p)
		}
		m, err := artifact.NewManifest([]artifact.Entry{{Path: p, Content: artifact.Hash([]byte("x"))}})
		switch {
		case ok && err != nil:
			t.Fatalf("NewManifest rejected the valid path %q: %v", p, err)
		case !ok && !errors.Is(err, artifact.ErrInvalidPath):
			t.Fatalf("NewManifest(%q) = %v, want ErrInvalidPath", p, err)
		case ok && (len(m.Entries()) != 1 || m.Entries()[0].Path != p):
			t.Fatalf("NewManifest changed the path %q to %+v", p, m.Entries())
		}
	})
}

// FuzzManifestOrder checks that a manifest does not depend on the order of its entries, and refuses a duplicate path.
func FuzzManifestOrder(f *testing.F) {
	f.Add("tests/a.go", "tests/b.go")
	f.Add("tests/A.go", "tests/a.go")
	f.Add("tests/caf\u00e9.go", "tests/cafe\u0301.go")
	f.Add("a\b", "a")
	f.Fuzz(func(t *testing.T, a, b string) {
		ea := artifact.Entry{Path: a, Content: artifact.Hash([]byte("a\b"))}
		eb := artifact.Entry{Path: b, Content: artifact.Hash([]byte("b"))}
		forward, errF := artifact.NewManifest([]artifact.Entry{ea, eb})
		backward, errB := artifact.NewManifest([]artifact.Entry{eb, ea})
		if (errF == nil) != (errB == nil) {
			t.Fatalf("entry order changed the outcome: %v vs %v", errF, errB)
		}
		if errF != nil {
			if a == b && artifact.ValidPath(a) && !errors.Is(errF, artifact.ErrDuplicatePath) {
				t.Fatalf("duplicate path %q: %v, want ErrDuplicatePath", a, errF)
			}
			return
		}
		if a == b || forward.Digest() != backward.Digest() {
			t.Fatalf("accepted a duplicate or let order change the digest for %q, %q", a, b)
		}
		got := forward.Entries()
		if got[0].Path > got[1].Path {
			t.Fatalf("entries not in byte order: %q before %q", got[0].Path, got[1].Path)
		}
	})
}
