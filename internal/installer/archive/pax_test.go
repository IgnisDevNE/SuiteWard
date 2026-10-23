package archive

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"filippo.io/age"
)

// paxHeader is a PAX extended header (its block and its records) written by hand: tar.Writer refuses GNU.sparse records.
// kv holds the keys and values in turn. The header applies to the entry that follows it.
func paxHeader(kv ...string) []byte {
	var records []byte
	for i := 0; i+1 < len(kv); i += 2 {
		rest := " " + kv[i] + "=" + kv[i+1] + "\n"
		// A record starts with its own length, digits included.
		n := len(rest) + 1
		for len(strconv.Itoa(n))+len(rest) != n {
			n = len(strconv.Itoa(n)) + len(rest)
		}
		records = append(records, strconv.Itoa(n)+rest...)
	}
	hdr := make([]byte, 512)
	copy(hdr[0:], "PaxHeaders.0/x")
	copy(hdr[100:], "0000600\x00")
	copy(hdr[108:], "0000000\x00")
	copy(hdr[116:], "0000000\x00")
	copy(hdr[124:], fmt.Sprintf("%011o\x00", len(records)))
	copy(hdr[136:], "00000000000\x00")
	copy(hdr[148:], "        ")
	hdr[156] = tar.TypeXHeader
	copy(hdr[257:], "ustar\x0000")
	total := 0
	for _, c := range hdr {
		total += int(c)
	}
	copy(hdr[148:], fmt.Sprintf("%06o\x00 ", total))
	return append(append(hdr, records...), make([]byte, (512-len(records)%512)%512)...)
}

// sparseEntries are a status.json that GNU sparse 1.0 headers make of 8 MiB of zeros out of one block of data, and a manifest
// that lists those zeros. Go's tar reader presents it as a regular file of the size the archive claims: nothing in the archive
// is as large as what a reader would write or hash.
func sparseEntries() []rawEntry {
	const logical = 8 << 20
	member := rawEntry{
		pre:  paxHeader("GNU.sparse.major", "1", "GNU.sparse.minor", "0", "GNU.sparse.name", "status.json", "GNU.sparse.realsize", strconv.Itoa(logical)),
		hdr:  tar.Header{Name: "GNUSparseFile.0/status.json", Typeflag: tar.TypeReg, Mode: 0o600, Size: 512},
		body: "0\n" + strings.Repeat("\x00", 510),
	}
	manifest := Manifest{Format: 1, Info: testInfo(), Members: []Member{{Name: "status.json", Size: logical, SHA256: sum(strings.Repeat("\x00", logical))}}}
	return []rawEntry{member, reg("manifest.json", mustJSON(manifest))}
}

func TestThePaxHeaderOfTheSparseCasesReadsAsPaxToGoAndMakesTheRegularFileSparse(t *testing.T) {
	// The control of the hand-built bytes: Go itself reads them as the sparse file the cases claim.
	rec, id := newKeyPair(t)
	plain := plainReader(t, rawArchive(t, rec, sparseEntries()), id)
	hdr, err := tar.NewReader(plain).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if hdr.Typeflag != tar.TypeReg || hdr.Name != "status.json" || hdr.Size != 8<<20 {
		t.Errorf("Go reads %q (type %q) of %d bytes, want status.json as a regular file of 8 MiB", hdr.Name, hdr.Typeflag, hdr.Size)
	}
	if _, ok := hdr.PAXRecords["GNU.sparse.realsize"]; !ok {
		t.Errorf("the PAX records are %v", hdr.PAXRecords)
	}
}

func TestAPaxRecordThatIsNotSparseIsAccepted(t *testing.T) {
	// The writer emits a PAX size record for a member of 8 GiB or more, so PAX as such is allowed.
	rec, id := newKeyPair(t)
	member := reg("status.json", "{}")
	member.hdr.PAXRecords = map[string]string{"SUITEWARD.note": "a record that is not sparse"}
	data := rawArchive(t, rec, withManifest(member))

	if hdr, err := tar.NewReader(plainReader(t, data, id)).Next(); err != nil || len(hdr.PAXRecords) == 0 {
		t.Fatalf("the control has no PAX record: %+v, %v", hdr, err)
	}
	rd, err := Open(bytes.NewReader(data), id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	m, err := rd.ReadManifest()
	if err != nil {
		t.Fatalf("a member with a PAX record that is not sparse is refused: %v", err)
	}
	if len(m.Members) != 1 || m.Members[0].Name != "status.json" {
		t.Errorf("manifest = %+v", m)
	}
}

func plainReader(t *testing.T, data []byte, id *age.HybridIdentity) io.Reader {
	t.Helper()
	plain, err := age.Decrypt(bytes.NewReader(data), id)
	if err != nil {
		t.Fatalf("age cannot decrypt the archive: %v", err)
	}
	return plain
}
