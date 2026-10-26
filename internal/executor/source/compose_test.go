package source

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

type archives struct {
	data   map[Side][]byte
	opened []Side
}

func (a *archives) Open(_ context.Context, side Side) (io.ReadCloser, error) {
	data, ok := a.data[side]
	if !ok {
		return nil, fmt.Errorf("the service has no %s archive", side)
	}
	a.opened = append(a.opened, side)
	return io.NopCloser(bytes.NewReader(data)), nil
}

type manifestFiles struct {
	data   map[artifact.Digest][]byte
	opened int
}

func (f *manifestFiles) Open(_ context.Context, d artifact.Digest) (io.ReadCloser, error) {
	f.opened++
	data, ok := f.data[d]
	if !ok {
		return nil, fmt.Errorf("the service has no file %s", d)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *manifestFiles) serve(body string) artifact.Digest {
	if f.data == nil {
		f.data = map[artifact.Digest][]byte{}
	}
	d := artifact.Hash([]byte(body))
	f.data[d] = []byte(body)
	return d
}

func treeFile(path, body string, from execution.Source) execution.TreeFile {
	return execution.TreeFile{Path: path, Content: artifact.Hash([]byte(body)), Source: from}
}

// composition is a plan with the head and the base archives that serve it.
type composition struct {
	tree    []execution.TreeFile
	head    []member
	base    []member
	files   *manifestFiles
	limits  Limits
	headTar []byte // the archive data, set by build
	baseTar []byte
}

func (c *composition) build(t *testing.T) (arch *archives, entries map[Side][]execution.TreeEntry) {
	t.Helper()
	arch = &archives{data: map[Side][]byte{}}
	entries = map[Side][]execution.TreeEntry{}
	if c.head != nil {
		arch.data[SideHead], entries[SideHead] = targz(t, c.head...), treeOf(c.head...)
	}
	if c.base != nil {
		arch.data[SideBase], entries[SideBase] = targz(t, c.base...), treeOf(c.base...)
	}
	if c.limits == (Limits{}) {
		c.limits = generous
	}
	return arch, entries
}

// compose runs Compose into a buffer. It closes the tar when Compose succeeds, and returns the bytes written either way.
func (c *composition) compose(t *testing.T) (*bytes.Buffer, *Appender, error) {
	t.Helper()
	arch, entries := c.build(t)
	var out bytes.Buffer
	files := c.files
	if files == nil {
		files = &manifestFiles{}
	}
	app, err := Compose(context.Background(), &out, c.tree, arch, entries, files, c.limits)
	return &out, app, err
}

type tarFile struct {
	typeflag byte
	mode     int64
	body     string
	link     string
}

func readTar(t *testing.T, data []byte) map[string]tarFile {
	t.Helper()
	found := map[string]tarFile{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return found
		}
		if err != nil {
			t.Fatalf("the composed tar is unreadable: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %q: %v", h.Name, err)
		}
		if _, dup := found[h.Name]; dup {
			t.Fatalf("%q is written twice", h.Name)
		}
		found[h.Name] = tarFile{h.Typeflag, h.Mode & 0o777, string(body), h.Linkname}
	}
}

func TestComposeWritesExactlyTheFilesOfThePlan(t *testing.T) {
	files := &manifestFiles{}
	declaration := "execution:\n  canonical: required\n"
	c := &composition{
		head: []member{file("a.go", "package a\n"), executable("bin/run.sh", "#!/bin/sh\n"), symlink("lnk", "a.go"), file("not_in_plan.go", "package x\n")},
		base: []member{file("b.go", "package b\n"), file("a.go", "package old\n"), file("also_not_in_plan.go", "package y\n")},
		tree: []execution.TreeFile{
			treeFile("a.go", "package a\n", execution.FromHead),
			treeFile("bin/run.sh", "#!/bin/sh\n", execution.FromHead),
			treeFile("lnk", "a.go", execution.FromHead),
			treeFile("b.go", "package b\n", execution.FromBase),
			{Path: ".suiteward.yml", Content: files.serve(declaration), Source: execution.FromManifest},
		},
		files: files,
	}
	out, app, err := c.compose(t)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	want := map[string]tarFile{
		"a.go":           {tar.TypeReg, 0o644, "package a\n", ""},
		"bin/run.sh":     {tar.TypeReg, 0o755, "#!/bin/sh\n", ""},
		"lnk":            {tar.TypeSymlink, 0o777, "", "a.go"},
		"b.go":           {tar.TypeReg, 0o644, "package b\n", ""},
		".suiteward.yml": {tar.TypeReg, 0o644, declaration, ""},
	}
	// Close ends the tar with the footer: two blocks of zeros.
	if !bytes.HasSuffix(out.Bytes(), make([]byte, 1024)) {
		t.Errorf("Close must end the tar")
	}
	found := readTar(t, out.Bytes())
	for name, w := range want {
		if found[name] != w {
			t.Errorf("%s = %+v, want %+v", name, found[name], w)
		}
	}
	if len(found) != len(want) {
		t.Errorf("the tar holds %d files, want exactly the %d of the plan: %v", len(found), len(want), keys(found))
	}
}

func keys(m map[string]tarFile) []string {
	var names []string
	for name := range m {
		names = append(names, name)
	}
	return names
}

func TestComposeOpensOnlyTheSourcesThePlanUses(t *testing.T) {
	c := &composition{
		head: []member{file("a.go", "package a\n")},
		base: []member{file("b.go", "package b\n")},
		tree: []execution.TreeFile{treeFile("a.go", "package a\n", execution.FromHead)},
	}
	arch, entries := c.build(t)
	files := &manifestFiles{}
	if _, err := Compose(context.Background(), io.Discard, c.tree, arch, entries, files, generous); err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(arch.opened) != 1 || arch.opened[0] != SideHead || files.opened != 0 {
		t.Errorf("a plan of head files needs the head archive and nothing else: opened %v, %d files", arch.opened, files.opened)
	}
}

func TestComposeVerifiesEveryFileWhileItStreams(t *testing.T) {
	headA := file("a.go", "package a\n")
	for name, tc := range map[string]struct {
		c    *composition
		want error
	}{
		"bytes that are not the tree's": {
			&composition{head: []member{file("a.go", "package evil\n")}, tree: []execution.TreeFile{treeFile("a.go", "package a\n", execution.FromHead)}},
			ErrSourceMismatch,
		},
		"a file that is missing": {
			&composition{head: []member{headA}, tree: []execution.TreeFile{treeFile("a.go", "package a\n", execution.FromHead), treeFile("b.go", "package b\n", execution.FromHead)}},
			ErrSourceMismatch,
		},
		"a path listed twice in the stream": {
			&composition{head: []member{headA, headA}, tree: []execution.TreeFile{treeFile("a.go", "package a\n", execution.FromHead)}},
			ErrSourceMismatch,
		},
		"a mode that is not the tree's": {
			&composition{head: []member{executable("a.go", "package a\n")}, tree: []execution.TreeFile{treeFile("a.go", "package a\n", execution.FromHead)}},
			ErrSourceMismatch,
		},
		"a symlink where the tree has a file": {
			&composition{head: []member{symlink("a.go", "package a\n")}, tree: []execution.TreeFile{treeFile("a.go", "package a\n", execution.FromHead)}},
			ErrSourceMismatch,
		},
		"a digest the plan does not expect": { // the blob SHA-1 matches the tree, the SHA-256 does not match the plan
			&composition{head: []member{headA}, tree: []execution.TreeFile{treeFile("a.go", "package other\n", execution.FromHead)}},
			ErrSourceMismatch,
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, app, err := tc.c.compose(t)
			onlyError(t, err, tc.want)
			if app != nil {
				t.Errorf("a failed composition must not hand out an appender")
			}
			if bytes.HasSuffix(out.Bytes(), make([]byte, 1024)) {
				t.Errorf("a failed composition must not end the tar, or a tar reader would take it for a whole one")
			}
		})
	}
}

func TestComposeChecksTheSymlinksOfTheComposedTree(t *testing.T) {
	// Each link resolves inside the tree of its own commit: in the head x/q is a directory, in the base it is a link to the root.
	// Composed, the walk p/l -> ../x/q/../.. follows the base's link and steps above the root.
	c := &composition{
		head: []member{symlink("p/l", "../x/q/../..")},
		base: []member{symlink("x/q", "..")},
		tree: []execution.TreeFile{treeFile("p/l", "../x/q/../..", execution.FromHead), treeFile("x/q", "..", execution.FromBase)},
	}
	_, _, err := c.compose(t)
	onlyError(t, err, ErrSourceMismatch)
	if !errors.Is(err, execution.ErrUnsafeSymlink) {
		t.Errorf("want the domain's ErrUnsafeSymlink inside the error, got %v", err)
	}
}

func TestComposeRefusesAPlanItCannotServe(t *testing.T) {
	files := &manifestFiles{}
	for name, tc := range map[string]*composition{
		"a base file with no base side": {
			head: []member{file("a.go", "1")},
			tree: []execution.TreeFile{treeFile("b.go", "1", execution.FromBase)},
		},
		"a source the plan does not know": {
			head: []member{file("a.go", "1")},
			tree: []execution.TreeFile{treeFile("a.go", "1", execution.Source(0))},
		},
		"a path listed twice": {
			head: []member{file("a.go", "1")},
			tree: []execution.TreeFile{treeFile("a.go", "1", execution.FromHead), treeFile("a.go", "1", execution.FromHead)},
		},
		"a manifest file with no digest": {
			head: []member{file("a.go", "1")},
			tree: []execution.TreeFile{{Path: "m", Source: execution.FromManifest}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			tc.files = files
			_, app, err := tc.compose(t)
			onlyError(t, err, ErrInvalidPlan)
			if app != nil {
				t.Errorf("a refused plan must not hand out an appender")
			}
		})
	}
}

func TestComposeChecksManifestFilesByDigest(t *testing.T) {
	good := &manifestFiles{}
	digest := good.serve("the governing bytes\n")
	plan := func(f *manifestFiles) *composition {
		return &composition{tree: []execution.TreeFile{{Path: "m.txt", Content: digest, Source: execution.FromManifest}}, files: f}
	}
	t.Run("the digest holds", func(t *testing.T) {
		out, app, err := plan(good).compose(t)
		if err != nil || app == nil {
			t.Fatalf("Compose: %v", err)
		}
		if found := readTar(t, out.Bytes()); found["m.txt"].body != "the governing bytes\n" {
			t.Errorf("the file was not written: %+v", found)
		}
	})
	t.Run("other bytes arrive", func(t *testing.T) {
		_, _, err := plan(&manifestFiles{data: map[artifact.Digest][]byte{digest: []byte("tampered\n")}}).compose(t)
		onlyError(t, err, ErrArchiveUnreadable)
	})
	t.Run("the service has no such file", func(t *testing.T) {
		_, _, err := plan(&manifestFiles{}).compose(t)
		onlyError(t, err, ErrArchiveUnreadable)
	})
	t.Run("the file is over its cap", func(t *testing.T) {
		c := plan(good)
		c.limits = Limits{ArchiveBytes: 1 << 20, FileBytes: 5, TmpfsMiB: 64}
		_, _, err := c.compose(t)
		onlyError(t, err, ErrTooLarge)
	})
}

func TestComposeStopsAtHalfTheTmpfs(t *testing.T) {
	zeros := strings.Repeat("0", 300<<10)
	c := &composition{
		head: []member{file("a.bin", zeros+"a"), file("b.bin", zeros+"b")},
		tree: []execution.TreeFile{treeFile("a.bin", zeros+"a", execution.FromHead), treeFile("b.bin", zeros+"b", execution.FromHead)},
		// The budget is half of 1 MiB, 512 KiB: one file fits and two do not.
		limits: Limits{ArchiveBytes: 1 << 20, FileBytes: 1 << 20, TmpfsMiB: 1},
	}
	out, app, err := c.compose(t)
	onlyError(t, err, ErrTooLarge)
	if app != nil {
		t.Errorf("no appender for a tree over the budget")
	}
	if bytes.Contains(out.Bytes(), []byte(zeros+"b")) {
		t.Errorf("the file that would pass the budget must not be written")
	}
}

func TestComposeStopsWhenTheContextEnds(t *testing.T) {
	c := &composition{head: []member{file("a.go", "1")}, tree: []execution.TreeFile{treeFile("a.go", "1", execution.FromHead)}}
	arch, entries := c.build(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Compose(ctx, io.Discard, c.tree, arch, entries, &manifestFiles{}, generous); !errors.Is(err, context.Canceled) {
		t.Errorf("want the context's error, got %v", err)
	}
}

func TestComposePassesOnAWriteFailureAsItIs(t *testing.T) {
	gone := errors.New("the container is gone")
	c := &composition{head: []member{file("a.go", "1")}, tree: []execution.TreeFile{treeFile("a.go", "1", execution.FromHead)}}
	arch, entries := c.build(t)
	_, err := Compose(context.Background(), failingWriter{gone}, c.tree, arch, entries, &manifestFiles{}, generous)
	if !errors.Is(err, gone) {
		t.Fatalf("want the writer's error, got %v", err)
	}
	for _, sentinel := range allErrors {
		if errors.Is(err, sentinel) {
			t.Errorf("a failed write is not %v: the archive is not at fault", sentinel)
		}
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestComposeHoldsNoWholeEntryInMemory(t *testing.T) {
	big := file("big.bin", strings.Repeat("0", 32<<20))
	c := &composition{
		head:   []member{big},
		tree:   []execution.TreeFile{treeFile("big.bin", big.body, execution.FromHead)},
		limits: Limits{ArchiveBytes: 1 << 20, FileBytes: 64 << 20, TmpfsMiB: 1024},
	}
	arch, entries := c.build(t)
	var err error
	if n := allocated(func() {
		_, err = Compose(context.Background(), io.Discard, c.tree, arch, entries, &manifestFiles{}, c.limits)
	}); err != nil || n > 8<<20 {
		t.Errorf("a file of 32 MiB must stream: %d bytes allocated, %v", n, err)
	}
}

func TestAppenderAddsModuleFilesToTheSameTarAndBudget(t *testing.T) {
	// A budget of 512 KiB: the tree takes 300 KiB, so a module of 100 KiB fits and one of 200 KiB more does not.
	zeros := strings.Repeat("0", 300<<10)
	c := &composition{
		head:   []member{file("src/a.bin", zeros), file("src/pkg.go", "package pkg\n")},
		tree:   []execution.TreeFile{treeFile("src/a.bin", zeros, execution.FromHead), treeFile("src/pkg.go", "package pkg\n", execution.FromHead)},
		limits: Limits{ArchiveBytes: 1 << 20, FileBytes: 1 << 20, TmpfsMiB: 1},
	}
	out, app, err := c.compose(t)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	module := strings.Repeat("m", 100<<10)
	if err := app.Add("proxy/example.com/m/@v/v1.0.0.zip", int64(len(module)), strings.NewReader(module)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	more := strings.Repeat("n", 200<<10)
	onlyError(t, app.Add("proxy/example.com/n/@v/v1.0.0.zip", int64(len(more)), strings.NewReader(more)), ErrTooLarge)
	if err := app.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	found := readTar(t, out.Bytes())
	if found["proxy/example.com/m/@v/v1.0.0.zip"].body != module || found["src/pkg.go"].body != "package pkg\n" {
		t.Errorf("the module file and the tree must be in one tar: %v", keys(found))
	}
	if _, wrote := found["proxy/example.com/n/@v/v1.0.0.zip"]; wrote {
		t.Errorf("the file that passed the budget must not be written")
	}
}

func TestAppenderRefusesWhatWouldShadowOrBreakTheTree(t *testing.T) {
	for name, add := range map[string]struct {
		name string
		size int64
		body string
		err  error
	}{
		"the path of a tree file":        {"top.go", 1, "x", ErrInvalidPlan},
		"a path below a tree file":       {"top.go/x", 1, "x", ErrInvalidPlan},
		"the directory of a tree file":   {"a", 1, "x", ErrInvalidPlan},
		"a path that is not clean":       {"x/../y", 1, "x", ErrInvalidPlan},
		"an absolute path":               {"/etc/x", 1, "x", ErrInvalidPlan},
		"a negative size":                {"ok", -1, "x", ErrInvalidPlan},
		"fewer bytes than the size says": {"ok", 5, "abc", io.ErrUnexpectedEOF},
		"more bytes than the size says":  {"ok", 1, "abc", ErrInvalidPlan},
	} {
		t.Run(name, func(t *testing.T) {
			c := &composition{
				head: []member{file("a/b.go", "1"), file("top.go", "2")},
				tree: []execution.TreeFile{treeFile("a/b.go", "1", execution.FromHead), treeFile("top.go", "2", execution.FromHead)},
			}
			_, app, err := c.compose(t)
			if err != nil {
				t.Fatalf("Compose: %v", err)
			}
			if err := app.Add(add.name, add.size, strings.NewReader(add.body)); !errors.Is(err, add.err) {
				t.Errorf("Add(%q) = %v, want %v", add.name, err, add.err)
			}
		})
	}
}

func TestAppenderRefusesAPathItAlreadyAdded(t *testing.T) {
	c := &composition{head: []member{file("a.go", "1")}, tree: []execution.TreeFile{treeFile("a.go", "1", execution.FromHead)}}
	_, app, err := c.compose(t)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if err := app.Add("m.zip", 1, strings.NewReader("x")); err != nil {
		t.Fatalf("the first Add: %v", err)
	}
	if err := app.Add("m.zip", 1, strings.NewReader("y")); !errors.Is(err, ErrInvalidPlan) {
		t.Errorf("the second Add of one path = %v, want ErrInvalidPlan", err)
	}
}

func TestAppenderRefusesFilesAfterClose(t *testing.T) {
	c := &composition{head: []member{file("a.go", "1")}, tree: []execution.TreeFile{treeFile("a.go", "1", execution.FromHead)}}
	_, app, err := c.compose(t)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := app.Add("late", 1, strings.NewReader("x")); err == nil {
		t.Errorf("a closed tar takes no file")
	}
}
