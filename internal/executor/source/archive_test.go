package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"io"
	"path"
	"runtime"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

const topDir = "owner-repo-0123456789abcdef0123456789abcdef01234567"

// member is one entry of a test archive, below the top-level directory unless outside is set.
type member struct {
	hdr     tar.Header
	body    string
	outside bool
}

func file(name, body string) member {
	return member{hdr: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o664, Size: int64(len(body))}, body: body}
}

func executable(name, body string) member {
	m := file(name, body)
	m.hdr.Mode = 0o775
	return m
}

func symlink(name, target string) member {
	return member{hdr: tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: target, Mode: 0o777}}
}

func directory(name string) member {
	return member{hdr: tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o775}}
}

func outsideTop(m member) member {
	m.outside = true
	return m
}

// targz builds what GitHub sends: a tar.gz with a pax global header, one top-level directory and an entry for every directory.
func targz(t *testing.T, members ...member) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	write := func(h tar.Header, body string) {
		t.Helper()
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatalf("write the header of %q: %v", h.Name, err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatalf("write %q: %v", h.Name, err)
		}
	}
	write(tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": strings.TrimPrefix(topDir, "owner-repo-")}}, "")
	write(tar.Header{Name: topDir + "/", Typeflag: tar.TypeDir, Mode: 0o775}, "")
	dirs := map[string]bool{}
	for _, m := range members {
		h := m.hdr
		if !m.outside {
			for dir := path.Dir(strings.TrimSuffix(h.Name, "/")); dir != "."; dir = path.Dir(dir) {
				if !dirs[dir] {
					dirs[dir] = true
					write(tar.Header{Name: topDir + "/" + dir + "/", Typeflag: tar.TypeDir, Mode: 0o775}, "")
				}
			}
			h.Name = topDir + "/" + h.Name
		}
		write(h, m.body)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// treeOf is the commit's tree that matches the archive of members.
func treeOf(members ...member) []execution.TreeEntry {
	var entries []execution.TreeEntry
	for _, m := range members {
		switch m.hdr.Typeflag {
		case tar.TypeReg:
			mode := execution.GitRegular
			if m.hdr.Mode&0o100 != 0 {
				mode = execution.GitExecutable
			}
			entries = append(entries, execution.TreeEntry{Path: m.hdr.Name, Mode: mode, BlobSHA1: execution.BlobSHA1([]byte(m.body))})
		case tar.TypeSymlink:
			entries = append(entries, execution.TreeEntry{Path: m.hdr.Name, Mode: execution.GitSymlink, BlobSHA1: execution.BlobSHA1([]byte(m.hdr.Linkname))})
		}
	}
	return entries
}

var (
	errAborted = errors.New("the response was aborted")
	generous   = Limits{ArchiveBytes: 1 << 20, FileBytes: 1 << 20, TmpfsMiB: 64}
)

// abortAfter is a response that the service aborts after n bytes: the bytes, then an error and not an end.
func abortAfter(data []byte, n int) io.Reader {
	return io.MultiReader(bytes.NewReader(data[:n]), errReader{errAborted})
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// incompressible is n bytes that gzip hardly shrinks: random text, five bits of entropy to the byte.
func incompressible(n int) string {
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(rand.Text())
	}
	return b.String()[:n]
}

// allocated is how many bytes f allocated, in total and not live: a copy of a whole entry would show.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func tarHeader(name string, mode int64, typeflag byte, link string) tar.Header {
	return tar.Header{Name: name, Mode: mode, Typeflag: typeflag, Linkname: link}
}
