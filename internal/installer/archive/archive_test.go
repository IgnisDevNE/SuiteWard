package archive

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

// blob is a member the caller hands to the writer.
type blob struct{ name, body string }

// A body bigger than one age chunk (64 KiB), so the stream crosses chunk boundaries.
var bigBody = strings.Repeat("0123456789abcdef", 12_000)

func testInfo() Info {
	return Info{
		SuitewardVersion: "v0.3.0",
		ImageDigest:      "sha256:" + strings.Repeat("a", 64),
		Instance:         "acme",
		Tier:             "isolated",
		CreatedAt:        time.Date(2026, 10, 22, 14, 30, 45, 0, time.FixedZone("CEST", 2*60*60)),
	}
}

func newKeyPair(t *testing.T) (*age.HybridRecipient, *age.HybridIdentity) {
	t.Helper()
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	rec := id.Recipient()
	return rec, id
}

func allBlobs() []blob {
	return []blob{
		{"suiteward.sql", "-- dump\nCREATE TABLE t();\n"},
		{"suiteward-artifacts.tar", bigBody},
		{"status.json", `{"version":"v0.2.0"}`},
		{"settings.yaml", "instance: acme\n"},
		{"units/suiteward.container", "[Container]\nImage=x\n"},
		{"units/suiteward-db.volume", "[Volume]\n"},
		{"units/suiteward-net.network", "[Network]\n"},
	}
}

func writeArchive(t *testing.T, rec *age.HybridRecipient, info Info, blobs []blob) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, rec, info)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for _, b := range blobs {
		if err := w.Add(b.name, int64(len(b.body)), strings.NewReader(b.body)); err != nil {
			t.Fatalf("Add %s: %v", b.name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

func sum(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

// outDir makes a directory the way the caller of Extract does, inside a root that must stay otherwise empty.
func outDir(t *testing.T) (root, dir string) {
	t.Helper()
	root = t.TempDir()
	dir = filepath.Join(root, "out")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return root, dir
}

// walk lists every path under dir, relative, slash-separated, and what it holds.
func walk(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	fsys := os.DirFS(dir)
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == "." {
			return err
		}
		if d.IsDir() {
			got[path+"/"] = ""
			return nil
		}
		b, err := fs.ReadFile(fsys, path)
		got[path] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRoundTripExtractsEveryMemberWithItsContents(t *testing.T) {
	rec, id := newKeyPair(t)
	blobs := allBlobs()
	data := writeArchive(t, rec, testInfo(), blobs)

	rd, err := Open(bytes.NewReader(data), id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, dir := outDir(t)
	m, err := rd.Extract(dir)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	got := walk(t, dir)
	for _, b := range blobs {
		if got[b.name] != b.body {
			t.Errorf("member %s: got %d bytes, want %d", b.name, len(got[b.name]), len(b.body))
		}
	}
	if _, ok := got["units/"]; !ok || len(got) != len(blobs)+1 {
		t.Errorf("want the members and a units directory and nothing else, got %d paths", len(got))
	}
	if runtime.GOOS != "windows" {
		for path := range got {
			info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(path)))
			if err != nil {
				t.Fatal(err)
			}
			want := fs.FileMode(0o600)
			if info.IsDir() {
				want = 0o700
			}
			if info.Mode().Perm() != want {
				t.Errorf("%s has mode %v, want %v", path, info.Mode().Perm(), want)
			}
		}
	}

	if m.Format != 1 || m.SuitewardVersion != "v0.3.0" || m.Instance != "acme" || m.Tier != "isolated" || m.ImageDigest != testInfo().ImageDigest {
		t.Errorf("manifest = %+v", m)
	}
	if !m.CreatedAt.Equal(testInfo().CreatedAt) || m.CreatedAt.Location() != time.UTC {
		t.Errorf("createdAt = %v, want %v in UTC", m.CreatedAt, testInfo().CreatedAt.UTC())
	}
	if len(m.Members) != len(blobs) {
		t.Fatalf("manifest has %d members, want %d", len(m.Members), len(blobs))
	}
	for i, b := range blobs {
		if want := (Member{Name: b.name, Size: int64(len(b.body)), SHA256: sum(b.body)}); m.Members[i] != want {
			t.Errorf("manifest member %d = %+v, want %+v", i, m.Members[i], want)
		}
	}
}

// wireManifest pins the manifest keys independently of the writer's own struct.
type wireManifest struct {
	Format           int    `json:"format"`
	SuitewardVersion string `json:"suitewardVersion"`
	ImageDigest      string `json:"imageDigest"`
	Instance         string `json:"instance"`
	Tier             string `json:"tier"`
	CreatedAt        string `json:"createdAt"`
	Members          []struct {
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"members"`
}

// plainTar decrypts with age itself and reads the tar by hand, so the writer is checked against the format, not against the reader.
func plainTar(t *testing.T, data []byte, id *age.HybridIdentity) (names []string, manifest wireManifest) {
	t.Helper()
	plain, err := age.Decrypt(bytes.NewReader(data), id)
	if err != nil {
		t.Fatalf("age cannot decrypt the archive: %v", err)
	}
	tr := tar.NewReader(plain)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names, manifest
		}
		if err != nil {
			t.Fatalf("the plaintext is not a tar: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Mode != 0o600 {
			t.Errorf("%s: typeflag %q mode %o, want a regular file with mode 600", hdr.Name, hdr.Typeflag, hdr.Mode)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
		if hdr.Name == "manifest.json" {
			if err := json.Unmarshal(body, &manifest); err != nil {
				t.Fatalf("manifest.json: %v", err)
			}
		}
	}
}

func TestWriterProducesAnAgeFileOfATarWithTheManifestLast(t *testing.T) {
	rec, id := newKeyPair(t)
	blobs := allBlobs()
	data := writeArchive(t, rec, testInfo(), blobs)

	if bytes.HasPrefix(data, []byte("-----BEGIN")) {
		t.Fatal("the archive is armored, it must be a binary age file")
	}
	names, manifest := plainTar(t, data, id)

	if last := names[len(names)-1]; last != "manifest.json" || len(names) != len(blobs)+1 {
		t.Fatalf("members = %v, want the data members in order and manifest.json last", names)
	}
	for i, b := range blobs {
		if names[i] != b.name {
			t.Errorf("member %d is %s, want %s", i, names[i], b.name)
		}
	}
	if manifest.Format != 1 || manifest.SuitewardVersion != "v0.3.0" || manifest.ImageDigest != testInfo().ImageDigest || manifest.Instance != "acme" || manifest.Tier != "isolated" {
		t.Errorf("manifest = %+v", manifest)
	}
	if manifest.CreatedAt != "2026-10-22T12:30:45Z" {
		t.Errorf("createdAt = %q, want RFC 3339 in UTC", manifest.CreatedAt)
	}
	if len(manifest.Members) != len(blobs) {
		t.Fatalf("manifest lists %d members, want %d", len(manifest.Members), len(blobs))
	}
	for i, b := range blobs {
		m := manifest.Members[i]
		if m.Name != b.name || m.Size != int64(len(b.body)) || m.SHA256 != sum(b.body) {
			t.Errorf("manifest member %d = %+v", i, m)
		}
	}
}

func TestWriterRefusesWhatBreaksTheFormat(t *testing.T) {
	rec, _ := newKeyPair(t)
	if _, err := NewWriter(io.Discard, nil, testInfo()); err == nil || !strings.Contains(err.Error(), "recipient") {
		t.Errorf("a writer without a recipient would write an archive nobody can open, got %v", err)
	}
	if _, err := NewWriter(io.Discard, rec, Info{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a manifest without a creation time says nothing about the recovery point, got %v", err)
	}

	names := []string{
		"../x", "/etc/x", "units/../../x", `units\..\x`, `..\x`, "", "manifest.json", "SUITEWARD.SQL", "suiteward.sql/",
		"units/suiteward.txt", "units/suiteward.container\n", "units/x.container", "units/sub/suiteward.container", "units/suiteward.container/", "units/suiteward.CONTAINER",
	}
	for _, name := range names {
		w, err := NewWriter(io.Discard, rec, testInfo())
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		if err := w.Add(name, 1, strings.NewReader("x")); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("Add(%q) = %v, want a refusal of the name", name, err)
		}
	}
}

func TestWriterRefusesADuplicateAndAnAddAfterClose(t *testing.T) {
	rec, _ := newKeyPair(t)
	w, err := NewWriter(io.Discard, rec, testInfo())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Add("status.json", 2, strings.NewReader("{}")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := w.Add("status.json", 2, strings.NewReader("{}")); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "twice") {
		t.Errorf("a second member of the same name = %v", err)
	}
	// A refused member does not poison the writer: the data already written stays valid.
	if err := w.Add("settings.yaml", 2, strings.NewReader("a:")); err != nil {
		t.Errorf("Add after a refused name: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Add("suiteward.sql", 1, strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("Add after Close = %v, want it to say the writer is closed", err)
	}
	if err := w.Close(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("a second Close = %v, want it to say the writer is closed", err)
	}
}

func TestWriterRefusesAReaderThatDisagreesWithItsSize(t *testing.T) {
	rec, id := newKeyPair(t)
	cases := []struct {
		name string
		size int64
		body string
	}{
		{"more", 5, "0123456789"},
		{"fewer", 10, "01234"},
		{"nothing", 1, ""},
		{"one more", 5, "012345"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w, err := NewWriter(&buf, rec, testInfo())
			if err != nil {
				t.Fatal(err)
			}
			err = w.Add("suiteward.sql", tc.size, strings.NewReader(tc.body))
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "bytes") {
				t.Fatalf("Add = %v, want a refusal of the size", err)
			}
			if err := w.Add("status.json", 2, strings.NewReader("{}")); err == nil {
				t.Error("a writer that failed went on")
			}
			if err := w.Close(); err == nil {
				t.Fatal("Close claimed success after a failed member")
			}
			rd, err := Open(bytes.NewReader(buf.Bytes()), id)
			if err != nil {
				return
			}
			if _, err := rd.ReadManifest(); err == nil {
				t.Error("the partial output reads as a complete backup")
			}
		})
	}
}

func TestWriterReportsAFailingReaderAndAFailingDestination(t *testing.T) {
	rec, _ := newKeyPair(t)
	boom := errors.New("boom")

	w, err := NewWriter(io.Discard, rec, testInfo())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Add("suiteward.sql", 5, io.MultiReader(strings.NewReader("ab"), errReader{boom})); !errors.Is(err, boom) {
		t.Errorf("Add with a failing reader = %v, want the reader's error", err)
	}
	if err := w.Close(); !errors.Is(err, boom) {
		t.Errorf("Close = %v, want the first error", err)
	}

	// A destination that fails at once, one that fails when the first full chunk goes out, and one that fails when Close flushes the last.
	// The age header holds the key encapsulation, so its size is measured: the destination takes exactly the header.
	var header bytes.Buffer
	if _, err := NewWriter(&header, rec, testInfo()); err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := NewWriter(&failAfter{left: 0, err: boom}, rec, testInfo()); !errors.Is(err, boom) {
		t.Errorf("a destination that fails at once = %v, want its error", err)
	}
	w, err = NewWriter(&failAfter{left: header.Len(), err: boom}, rec, testInfo())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Add("suiteward-artifacts.tar", int64(len(bigBody)), strings.NewReader(bigBody)); !errors.Is(err, boom) {
		t.Errorf("Add to a destination that fails = %v, want its error", err)
	}
	w, err = NewWriter(&failAfter{left: header.Len(), err: boom}, rec, testInfo())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Add("status.json", 2, strings.NewReader("{}")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := w.Close(); !errors.Is(err, boom) {
		t.Errorf("Close on a destination that fails = %v, want its error", err)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// failAfter takes left bytes and then fails.
type failAfter struct {
	left int
	err  error
}

func (f *failAfter) Write(p []byte) (int, error) {
	if len(p) > f.left {
		return 0, f.err
	}
	f.left -= len(p)
	return len(p), nil
}

func TestOpenWithAnotherIdentityNamesTheRecoveryKit(t *testing.T) {
	rec, _ := newKeyPair(t)
	_, other := newKeyPair(t)
	data := writeArchive(t, rec, testInfo(), allBlobs())

	if _, err := Open(bytes.NewReader(data), nil); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Errorf("Open without an identity = %v, want an error naming the identity", err)
	}
	_, err := Open(bytes.NewReader(data), other)
	if !errors.Is(err, ErrWrongKey) {
		t.Fatalf("Open with another identity = %v, want ErrWrongKey", err)
	}
	if !strings.Contains(err.Error(), "recovery kit") {
		t.Errorf("the error does not name the recovery kit: %v", err)
	}
	if strings.Contains(err.Error(), "AGE-SECRET-KEY") || strings.Contains(err.Error(), other.String()) {
		t.Error("the error holds a private key")
	}
}

func TestAnArchiveMadeForAClassicKeyIsNotOpenedByAPostQuantumIdentity(t *testing.T) {
	classic, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc, err := age.Encrypt(&buf, classic.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	_, id := newKeyPair(t)
	if _, err := Open(&buf, id); !errors.Is(err, ErrWrongKey) {
		t.Errorf("Open = %v, want ErrWrongKey", err)
	}
}

func TestATruncatedArchiveIsRefusedAndLeavesNothing(t *testing.T) {
	rec, id := newKeyPair(t)
	data := writeArchive(t, rec, testInfo(), allBlobs())
	// Inside the age header, in the first chunk, in later chunks, about a whole chunk short, and one byte short.
	chunk := 64*1024 + 16
	for _, cut := range []int{20, 200, len(data) / 4, len(data) / 2, len(data) - chunk, len(data) - 1} {
		rd, err := Open(bytes.NewReader(data[:cut]), id)
		if err == nil {
			_, dir := outDir(t)
			_, err = rd.Extract(dir)
			if left := walk(t, dir); len(left) != 0 {
				t.Errorf("cut at %d left %d paths behind", cut, len(left))
			}
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("cut at %d of %d: %v, want ErrInvalid", cut, len(data), err)
		}
	}
	if _, err := Open(bytes.NewReader(data), id); err != nil {
		t.Fatalf("the control did not open: %v", err)
	}
}

// rawEntry is a tar member the writer would never produce.
type rawEntry struct {
	pre  []byte // bytes written as they are before the header, such as a PAX header that tar.Writer would refuse
	hdr  tar.Header
	body string
}

func reg(name, body string) rawEntry {
	return rawEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body))}, body: body}
}

func manifestOf(format int, entries []rawEntry) rawEntry {
	m := Manifest{Format: format, Info: testInfo(), Members: []Member{}}
	for _, e := range entries {
		m.Members = append(m.Members, Member{Name: e.hdr.Name, Size: int64(len(e.body)), SHA256: sum(e.body)})
	}
	return reg("manifest.json", mustJSON(m))
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// sound is a valid archive's entries: two data members and the manifest that lists them.
func sound() []rawEntry {
	data := []rawEntry{reg("status.json", `{"version":"v0.2.0"}`), reg("suiteward.sql", "-- dump\n")}
	return append(data, manifestOf(1, data))
}

// withManifest lists exactly the entries before it, like a correct writer would.
func withManifest(data ...rawEntry) []rawEntry { return append(data, manifestOf(1, data)) }

func rawArchive(t *testing.T, rec *age.HybridRecipient, entries []rawEntry) []byte {
	t.Helper()
	return paddedArchive(t, rec, entries, 0)
}

// paddedArchive follows the tar with padding zero bytes inside the encryption, as tools that write whole records do.
func paddedArchive(t *testing.T, rec *age.HybridRecipient, entries []rawEntry, padding int) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc, err := age.Encrypt(&buf, rec)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(enc)
	for _, e := range entries {
		if e.pre != nil {
			if err := tw.Flush(); err != nil {
				t.Fatal(err)
			}
			if _, err := enc.Write(e.pre); err != nil {
				t.Fatal(err)
			}
		}
		hdr := e.hdr
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(make([]byte, padding)); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestTheHandBuiltArchiveOfTheRejectionTestsIsValid(t *testing.T) {
	// The control: every rejection below changes one thing in this archive, so none may be rejected for another reason.
	rec, id := newKeyPair(t)
	rd, err := Open(bytes.NewReader(rawArchive(t, rec, sound())), id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, dir := outDir(t)
	if _, err := rd.Extract(dir); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got := walk(t, dir); got["status.json"] != `{"version":"v0.2.0"}` || got["suiteward.sql"] != "-- dump\n" || len(got) != 2 {
		t.Errorf("extracted %v", got)
	}
}

func TestAnArchiveCutAfterItsTarEndedIsRefused(t *testing.T) {
	// The tar is complete, but the encryption has chunks to go: only reading to the end of the stream notices the cut.
	rec, id := newKeyPair(t)
	data := paddedArchive(t, rec, sound(), 4*64*1024)

	rd, err := Open(bytes.NewReader(data), id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := rd.ReadManifest(); err != nil {
		t.Fatalf("the control, a padded archive, is refused: %v", err)
	}
	rd, err = Open(bytes.NewReader(data[:len(data)-2*64*1024]), id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := rd.ReadManifest(); !errors.Is(err, ErrInvalid) {
		t.Errorf("ReadManifest of a cut archive = %v, want ErrInvalid", err)
	}
}

func TestAHostileArchiveIsRefusedAndNothingIsLeftBehind(t *testing.T) {
	good := reg("status.json", "{}")
	symlink := rawEntry{hdr: tar.Header{Name: "status.json", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o600}}
	hardlink := rawEntry{hdr: tar.Header{Name: "status.json", Typeflag: tar.TypeLink, Linkname: "suiteward.sql", Mode: 0o600}}
	directory := rawEntry{hdr: tar.Header{Name: "units/", Typeflag: tar.TypeDir, Mode: 0o700}}
	tooLarge := reg("manifest.json", strings.Repeat("x", 2<<20))
	// A correct manifest behind a PAX record in the GNU.sparse family: Go ignores a record it does not know, the format must not.
	sparseManifest := manifestOf(1, []rawEntry{good})
	sparseManifest.pre = paxHeader("GNU.sparse.note", "x")

	cases := []struct {
		name    string
		entries []rawEntry
		want    string
	}{
		{"a parent directory", withManifest(good, reg("../x", "evil")), "not allowed"},
		{"an absolute path", withManifest(good, reg("/etc/x", "evil")), "not allowed"},
		{"a unit that climbs out", withManifest(good, reg("units/../../x", "evil")), "not allowed"},
		{"a backslash", withManifest(good, reg(`units\..\x`, "evil")), "not allowed"},
		{"a name outside the allowlist", withManifest(good, reg("other.txt", "evil")), "not allowed"},
		{"a unit that is not a unit", withManifest(good, reg("units/suiteward.sh", "evil")), "not allowed"},
		{"a nested unit", withManifest(good, reg("units/a/suiteward.container", "evil")), "not allowed"},
		{"a name with a trailing newline", withManifest(good, reg("units/suiteward.container\n", "evil")), "not allowed"},
		{"a name in another case", withManifest(good, reg("Status.json", "evil")), "not allowed"},
		{"a sparse member", sparseEntries(), "sparse"},
		{"a manifest with a sparse record", []rawEntry{good, sparseManifest}, "sparse"},
		{"a duplicate", withManifest(good, good), "twice"},
		{"a symbolic link", withManifest(good, symlink), "not a regular file"},
		{"a hard link", withManifest(good, hardlink), "not a regular file"},
		{"a directory", withManifest(good, directory), "not a regular file"},
		{"a member after the manifest", append(sound(), reg("settings.yaml", "x: y")), "after the manifest"},
		{"a second manifest", append(sound(), manifestOf(1, nil)), "after the manifest"},
		{"no manifest", []rawEntry{good, reg("suiteward.sql", "-- dump\n")}, "no manifest"},
		{"no data and no manifest", nil, "no manifest"},
		{"an unknown format", append([]rawEntry{good}, manifestOf(2, []rawEntry{good})), "format"},
		{"no format", append([]rawEntry{good}, manifestOf(0, []rawEntry{good})), "format"},
		{"a manifest that is not JSON", []rawEntry{good, reg("manifest.json", "not json")}, "is not valid"},
		{"a manifest with an unknown key", []rawEntry{good, reg("manifest.json", `{"format":1,"extra":true}`)}, "is not valid"},
		{"a manifest that is too large", []rawEntry{good, tooLarge}, "too large"},
		{"a member that differs from its hash", append([]rawEntry{reg("status.json", "[]")}, manifestOf(1, []rawEntry{good})), "sha256"},
		{"a size that differs", append([]rawEntry{good}, retag(manifestOf(1, []rawEntry{good}), "status.json", func(m *Member) { m.Size++ })), "size"},
		{"a member the manifest lacks", withLast(good, reg("suiteward.sql", "-- dump\n")), "not in the manifest"},
		{"a member the archive lacks", append([]rawEntry{good}, manifestOf(1, []rawEntry{good, reg("suiteward.sql", "-- dump\n")})), "does not hold"},
		{"members in another order", append([]rawEntry{good, reg("suiteward.sql", "-- dump\n")}, manifestOf(1, []rawEntry{reg("suiteward.sql", "-- dump\n"), good})), "differ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, id := newKeyPair(t)
			data := rawArchive(t, rec, tc.entries)

			rd, err := Open(bytes.NewReader(data), id)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			root, dir := outDir(t)
			_, err = rd.Extract(dir)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Extract = %v, want ErrInvalid mentioning %q", err, tc.want)
			}
			if left := walk(t, root); len(left) != 1 || left["out/"] != "" {
				t.Errorf("a refused archive left %v behind", left)
			}

			rd, err = Open(bytes.NewReader(data), id)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if _, err := rd.ReadManifest(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ReadManifest = %v, want ErrInvalid mentioning %q", err, tc.want)
			}
		})
	}
}

// retag edits one member's record in a manifest entry.
func retag(manifest rawEntry, name string, edit func(*Member)) rawEntry {
	var m Manifest
	if err := json.Unmarshal([]byte(manifest.body), &m); err != nil {
		panic(err)
	}
	for i := range m.Members {
		if m.Members[i].Name == name {
			edit(&m.Members[i])
		}
	}
	return reg("manifest.json", mustJSON(m))
}

// withLast lists only the first entry in the manifest but holds both.
func withLast(first, extra rawEntry) []rawEntry {
	return []rawEntry{first, extra, manifestOf(1, []rawEntry{first})}
}

func TestExtractNeverOverwritesOrRemovesAFileItDidNotCreate(t *testing.T) {
	rec, id := newKeyPair(t)
	data := writeArchive(t, rec, testInfo(), []blob{{"status.json", "{}"}, {"suiteward.sql", "-- dump\n"}})
	rd, err := Open(bytes.NewReader(data), id)
	if err != nil {
		t.Fatal(err)
	}
	_, dir := outDir(t)
	if err := os.WriteFile(filepath.Join(dir, "suiteward.sql"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rd.Extract(dir); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Extract over an existing file = %v, want it to exist already", err)
	}
	if got := walk(t, dir); len(got) != 1 || got["suiteward.sql"] != "mine" {
		t.Errorf("the directory holds %v, want only the file that was there", got)
	}
}

func TestExtractIntoAMissingDirectoryFailsWithoutCreatingIt(t *testing.T) {
	rec, id := newKeyPair(t)
	rd, err := Open(bytes.NewReader(writeArchive(t, rec, testInfo(), allBlobs())), id)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "missing")
	if _, err := rd.Extract(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Extract = %v, want the directory to be missing", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Extract created %s", dir)
	}
}

func TestReadManifestVerifiesEverythingAndAReaderIsSingleUse(t *testing.T) {
	rec, id := newKeyPair(t)
	blobs := allBlobs()
	data := writeArchive(t, rec, testInfo(), blobs)

	rd, err := Open(bytes.NewReader(data), id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	m, err := rd.ReadManifest()
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.Instance != "acme" || len(m.Members) != len(blobs) || m.Members[1].Size != int64(len(bigBody)) {
		t.Errorf("manifest = %+v", m)
	}
	if _, err := rd.ReadManifest(); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("a second read of the same stream = %v, want it to say the stream was already read", err)
	}
	_, dir := outDir(t)
	if _, err := rd.Extract(dir); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("Extract after ReadManifest = %v, want it to say the stream was already read", err)
	}
	if left := walk(t, dir); len(left) != 0 {
		t.Errorf("a refused read wrote %v", left)
	}
}
