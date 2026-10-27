package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

var allErrors = []error{ErrSourceMismatch, ErrTooLarge, ErrGitHubUnavailable, ErrArchiveUnreadable, ErrInvalidPlan}

// onlyError fails unless err is want and none of the other sentinels: a caller maps each to its own reason.
func onlyError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("want %v, got %v", want, err)
		return
	}
	for _, other := range allErrors {
		if other != want && errors.Is(err, other) {
			t.Errorf("%v must not also be %v", err, other)
		}
	}
}

func TestInspectDigestsTheFilesOfAGitHubArchive(t *testing.T) {
	members := []member{
		file("README.md", "hello\n"),
		file("cmd/app/main.go", "package main\n"),
		executable("scripts/run.sh", "#!/bin/sh\n"),
		symlink("docs/readme", "../README.md"),
		file("empty.txt", ""),
		directory("vendor/sub"), // git archive writes a submodule as an empty directory
	}
	tree := append(treeOf(members...),
		execution.TreeEntry{Path: "vendor/sub", Mode: execution.GitSubmodule, BlobSHA1: strings.Repeat("1", 40)},
		execution.TreeEntry{Path: "cmd", Mode: execution.GitTree, BlobSHA1: strings.Repeat("2", 40)},
	)
	got, err := Inspect(context.Background(), bytes.NewReader(targz(t, members...)), tree, generous)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	want, err := artifact.NewManifest([]artifact.Entry{
		{Path: "README.md", Content: artifact.Hash([]byte("hello\n"))},
		{Path: "cmd/app/main.go", Content: artifact.Hash([]byte("package main\n"))},
		{Path: "scripts/run.sh", Content: artifact.Hash([]byte("#!/bin/sh\n"))},
		{Path: "docs/readme", Content: artifact.Hash([]byte("../README.md"))},
		{Path: "empty.txt", Content: artifact.Hash(nil)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Files.Digest() != want.Digest() {
		t.Errorf("the manifest holds %v, want %v", got.Files.Entries(), want.Entries())
	}
	if len(got.Submodules) != 1 || got.Submodules[0] != "vendor/sub" {
		t.Errorf("submodules %q, want [vendor/sub]", got.Submodules)
	}
	if wantBytes := int64(len("hello\n") + len("package main\n") + len("#!/bin/sh\n") + len("../README.md")); got.Bytes != wantBytes {
		t.Errorf("Bytes = %d, want %d", got.Bytes, wantBytes)
	}
}

func TestInspectReportsEveryDifferenceAsASourceMismatch(t *testing.T) {
	good := []member{file("a.go", "package a\n"), file("b/c.go", "package c\n")}
	hexOf := strings.Repeat("3", 40)
	for name, tc := range map[string]struct {
		members []member
		tree    []execution.TreeEntry
	}{
		"a file the archive lacks (export-ignore)": {good, append(treeOf(good...), execution.TreeEntry{Path: "d.go", Mode: execution.GitRegular, BlobSHA1: hexOf})},
		"a file the tree lacks":                    {append([]member{file("x.go", "x")}, good...), treeOf(good...)},
		"a file with other bytes (export-subst)":   {[]member{file("a.go", "package a // $Format:%H$\n"), good[1]}, treeOf(good...)},
		"a file that became executable":            {[]member{executable("a.go", "package a\n"), good[1]}, treeOf(good...)},
		"a symlink where the tree has a file":      {[]member{symlink("a.go", "b/c.go"), good[1]}, treeOf(good...)},
		"a file where the tree has a symlink":      {good, append(treeOf(good[1]), execution.TreeEntry{Path: "a.go", Mode: execution.GitSymlink, BlobSHA1: execution.BlobSHA1([]byte("package a\n"))})},
		"a symlink that leaves the tree": {
			[]member{symlink("l", "../../etc/passwd")}, treeOf(symlink("l", "../../etc/passwd")),
		},
		"a symlink to an absolute path": {
			[]member{symlink("l", "/etc/passwd")}, treeOf(symlink("l", "/etc/passwd")),
		},
		"two names that fold together by case": {
			[]member{file("A.go", "1"), file("a.go", "2")}, treeOf(file("A.go", "1"), file("a.go", "2")),
		},
		"a name spelled like an 8.3 short name": {
			[]member{file("INTEGR~1/x.go", "1")}, treeOf(file("INTEGR~1/x.go", "1")),
		},
		"two names that differ by a trailing dot": {
			[]member{file("a.go", "1"), file("a.go.", "2")}, treeOf(file("a.go", "1"), file("a.go.", "2")),
		},
		"a path listed twice":    {[]member{file("a.go", "1"), file("a.go", "1")}, treeOf(file("a.go", "1"))},
		"a hard link":            {[]member{{hdr: tarHeader("h", 0o664, '1', "a.go")}}, nil},
		"a fifo":                 {[]member{{hdr: tarHeader("f", 0o664, '6', "")}}, nil},
		"a path that climbs":     {[]member{file("a/../../b", "1")}, treeOf(file("a/../../b", "1"))},
		"a second top directory": {[]member{outsideTop(file("other-0123/x.go", "1"))}, nil},
		"a file beside the top directory": {
			[]member{outsideTop(file("stray.txt", "1"))}, nil,
		},
		"an absolute path":          {[]member{outsideTop(file("/etc/passwd", "1"))}, nil},
		"a path of 5000 bytes":      {[]member{file(strings.Repeat("a", 5000), "1")}, nil},
		"a symlink target too long": {[]member{symlink("l", strings.Repeat("a", 5000))}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Inspect(context.Background(), bytes.NewReader(targz(t, tc.members...)), tc.tree, generous)
			onlyError(t, err, ErrSourceMismatch)
		})
	}
}

func TestInspectNamesTheDomainErrorOfAnUnsafeSymlink(t *testing.T) {
	m := symlink("l", "../../etc/passwd")
	_, err := Inspect(context.Background(), bytes.NewReader(targz(t, m)), treeOf(m), generous)
	if !errors.Is(err, execution.ErrUnsafeSymlink) {
		t.Errorf("want the domain's ErrUnsafeSymlink inside the error, got %v", err)
	}
}

func TestInspectRefusesWhatPassesItsLimits(t *testing.T) {
	big := file("big.bin", strings.Repeat("0", 600<<10)) // compresses to almost nothing
	for name, tc := range map[string]struct {
		members []member
		limits  Limits
		reader  func(data []byte) io.Reader
	}{
		"a file over its cap":          {[]member{big}, Limits{ArchiveBytes: 1 << 20, FileBytes: 500 << 10, TmpfsMiB: 64}, nil},
		"a tree over half the tmpfs":   {[]member{big}, Limits{ArchiveBytes: 1 << 20, FileBytes: 1 << 20, TmpfsMiB: 1}, nil},
		"an archive over its limit":    {[]member{file("r.bin", incompressible(40<<10))}, Limits{ArchiveBytes: 10 << 10, FileBytes: 1 << 20, TmpfsMiB: 64}, nil},
		"a stream the service aborted": {[]member{file("r.bin", incompressible(40<<10))}, Limits{ArchiveBytes: 10 << 10, FileBytes: 1 << 20, TmpfsMiB: 64}, func(d []byte) io.Reader { return abortAfter(d, 10<<10) }},
	} {
		t.Run(name, func(t *testing.T) {
			data := targz(t, tc.members...)
			var r io.Reader = bytes.NewReader(data)
			if tc.reader != nil {
				r = tc.reader(data)
			}
			_, err := Inspect(context.Background(), r, treeOf(tc.members...), tc.limits)
			onlyError(t, err, ErrTooLarge)
		})
	}
}

func TestInspectAcceptsAnArchiveOfExactlyItsLimit(t *testing.T) {
	m := file("r.bin", incompressible(4<<10))
	data := targz(t, m)
	_, err := Inspect(context.Background(), bytes.NewReader(data), treeOf(m), Limits{ArchiveBytes: int64(len(data)), FileBytes: 1 << 20, TmpfsMiB: 64})
	if err != nil {
		t.Errorf("an archive that ends exactly at its limit is whole: %v", err)
	}
}

func TestInspectTellsABrokenStreamFromAnUnreadableArchive(t *testing.T) {
	m := file("r.bin", incompressible(40<<10))
	data := targz(t, m)
	tree := treeOf(m)
	corrupt := bytes.Clone(data)
	corrupt[len(corrupt)-8] ^= 0xff // the CRC in the gzip trailer
	var garbageTar bytes.Buffer
	gz := gzip.NewWriter(&garbageTar)
	_, _ = gz.Write(bytes.Repeat([]byte("x"), 1024))
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		reader io.Reader
		want   error
	}{
		"a stream cut off by an error, short of the limit": {abortAfter(data, len(data)/2), ErrGitHubUnavailable},
		"a stream that ends early and cleanly":             {bytes.NewReader(data[:len(data)/2]), ErrGitHubUnavailable},
		"an empty stream":                                  {bytes.NewReader(nil), ErrGitHubUnavailable},
		"bytes that are not gzip":                          {strings.NewReader("this is not a gzip stream at all"), ErrArchiveUnreadable},
		"a gzip stream of bytes that are not a tar":        {&garbageTar, ErrArchiveUnreadable},
		"a damaged gzip trailer":                           {bytes.NewReader(corrupt), ErrArchiveUnreadable},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Inspect(context.Background(), tc.reader, tree, generous)
			onlyError(t, err, tc.want)
		})
	}
}

func TestInspectStopsWhenTheContextEnds(t *testing.T) {
	m := file("a.go", "package a\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Inspect(ctx, bytes.NewReader(targz(t, m)), treeOf(m), generous); !errors.Is(err, context.Canceled) {
		t.Errorf("want the context's error, got %v", err)
	}
}

func TestInspectHoldsNoWholeEntryInMemory(t *testing.T) {
	m := file("big.bin", strings.Repeat("0", 32<<20))
	data := targz(t, m)
	tree := treeOf(m)
	l := Limits{ArchiveBytes: 1 << 20, FileBytes: 64 << 20, TmpfsMiB: 1024}
	var err error
	if got := allocated(func() { _, err = Inspect(context.Background(), bytes.NewReader(data), tree, l) }); err != nil || got > 8<<20 {
		t.Errorf("a file of 32 MiB must stream: %d bytes allocated, %v", got, err)
	}
}

func TestInspectReadsAFileWithTheOldRegularFlag(t *testing.T) {
	// Old tar writers mark a regular file with a NUL; the reader reads it as a regular file, and so must the check.
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, h := range []tar.Header{
		{Name: topDir + "/", Typeflag: tar.TypeDir, Mode: 0o775},
		{Name: topDir + "/a.go", Typeflag: tar.TypeReg, Mode: 0o664, Size: 3},
	} {
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tw.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	b := raw.Bytes()
	header := b[512 : 2*512] // the second header: the file
	header[156] = 0
	sum := 0
	for i, c := range header {
		if i >= 148 && i < 156 {
			c = ' '
		}
		sum += int(c)
	}
	copy(header[148:], fmt.Sprintf("%06o\x00 ", sum))
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	tree := []execution.TreeEntry{{Path: "a.go", Mode: execution.GitRegular, BlobSHA1: execution.BlobSHA1([]byte("abc"))}}
	if _, err := Inspect(context.Background(), &gz, tree, generous); err != nil {
		t.Errorf("a file with the old flag is a file: %v", err)
	}
}
