package source

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// What the fake openers answer when they have nothing: errors the tests can recognize, with no sentinel of the package in them.
var (
	errNoArchive = errors.New("the service has no such archive")
	errNoFile    = errors.New("the service has no such file")
)

type archives struct {
	data   map[Side][]byte
	opened []Side
}

func (a *archives) Open(_ context.Context, side Side) (io.ReadCloser, error) {
	data, ok := a.data[side]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errNoArchive, side)
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
		return nil, fmt.Errorf("%w: %s", errNoFile, d)
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
	tree   []execution.TreeFile
	head   []member
	base   []member
	files  *manifestFiles
	limits Limits
	// treeEntries replaces the tree entries that build derives from the members of a side, for a tree that differs from its archive.
	treeEntries map[Side][]execution.TreeEntry
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
	maps.Copy(entries, c.treeEntries)
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
		"src/a.go":             {tar.TypeReg, 0o644, "package a\n", ""},
		"src/bin/run.sh":       {tar.TypeReg, 0o755, "#!/bin/sh\n", ""},
		"src/lnk":              {tar.TypeSymlink, 0o777, "", "a.go"},
		"src/b.go":             {tar.TypeReg, 0o644, "package b\n", ""},
		"src/.suiteward.yml":   {tar.TypeReg, 0o644, declaration, ""},
		sandbox.CompleteMarker: {tar.TypeReg, 0o644, "", ""}, // Close ends the tar with it
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
		t.Errorf("the tar holds %d files, want exactly the %d of the plan and the marker: %v", len(found), len(want), keys(found))
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
			&composition{
				head:        []member{executable("a.go", "package a\n")},
				tree:        []execution.TreeFile{treeFile("a.go", "package a\n", execution.FromHead)},
				treeEntries: map[Side][]execution.TreeEntry{SideHead: treeOf(file("a.go", "package a\n"))},
			},
			ErrSourceMismatch,
		},
		"a symlink where the tree has a file": {
			&composition{
				head:        []member{symlink("a.go", "package a\n")},
				tree:        []execution.TreeFile{treeFile("a.go", "package a\n", execution.FromHead)},
				treeEntries: map[Side][]execution.TreeEntry{SideHead: treeOf(file("a.go", "package a\n"))},
			},
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
		if found := readTar(t, out.Bytes()); found["src/m.txt"].body != "the governing bytes\n" {
			t.Errorf("the file was not written: %+v", found)
		}
	})
	t.Run("other bytes arrive", func(t *testing.T) {
		_, _, err := plan(&manifestFiles{data: map[artifact.Digest][]byte{digest: []byte("tampered\n")}}).compose(t)
		onlyError(t, err, ErrArchiveUnreadable)
	})
	t.Run("the service has no such file", func(t *testing.T) {
		_, _, err := plan(&manifestFiles{}).compose(t)
		passedOn(t, err, errNoFile)
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
		head:   []member{file("lib/a.bin", zeros), file("lib/pkg.go", "package pkg\n")},
		tree:   []execution.TreeFile{treeFile("lib/a.bin", zeros, execution.FromHead), treeFile("lib/pkg.go", "package pkg\n", execution.FromHead)},
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
	if found["proxy/example.com/m/@v/v1.0.0.zip"].body != module || found["src/lib/pkg.go"].body != "package pkg\n" {
		t.Errorf("the module file and the tree must be in one tar: %v", keys(found))
	}
	if _, wrote := found["proxy/example.com/n/@v/v1.0.0.zip"]; wrote {
		t.Errorf("the file that passed the budget must not be written")
	}
}

func TestAppenderNamesStayOutsideTheTreeDirectory(t *testing.T) {
	// The tree extracts to /work/src, and go's own state (the module proxy) lives beside it, so no appended name may land inside it.
	for name, add := range map[string]string{
		"the tree directory itself":    "src",
		"a file of the tree directory": "src/a/b.go",
		"a path of a tree file":        "src/top.go",
		"a deep path below it":         "src/x/y/z.zip",
	} {
		t.Run(name, func(t *testing.T) {
			c := &composition{
				head: []member{file("a/b.go", "1"), file("top.go", "2")},
				tree: []execution.TreeFile{treeFile("a/b.go", "1", execution.FromHead), treeFile("top.go", "2", execution.FromHead)},
			}
			out, app, err := c.compose(t)
			if err != nil {
				t.Fatalf("Compose: %v", err)
			}
			before := out.Len()
			onlyError(t, app.Add(add, 1, strings.NewReader("x")), ErrInvalidPlan)
			if out.Len() != before {
				t.Errorf("a refused name must write nothing")
			}
		})
	}
}

func TestAppenderWritesItsNamesBesideTheTreeDirectory(t *testing.T) {
	c := &composition{head: []member{file("a.go", "1")}, tree: []execution.TreeFile{treeFile("a.go", "1", execution.FromHead)}}
	out, app, err := c.compose(t)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	names := []string{".suiteward/proxy/m/@v/v1.zip", "src.txt", "srcx/y", "a.go"} // a.go is the tree's own name, but under src/
	for _, name := range names {
		if err := app.Add(name, 1, strings.NewReader("x")); err != nil {
			t.Errorf("Add(%q): %v", name, err)
		}
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	found := readTar(t, out.Bytes())
	for _, name := range append(names, "src/a.go") {
		if _, ok := found[name]; !ok {
			t.Errorf("%q must be in the tar as it was named: %v", name, keys(found))
		}
	}
}

func TestAppenderRefusesWhatWouldShadowOrBreakItsOwnFiles(t *testing.T) {
	for name, add := range map[string]struct {
		name string
		size int64
		body string
		err  error
	}{
		"a path that is not clean":       {"x/../y", 1, "x", ErrInvalidPlan},
		"an absolute path":               {"/etc/x", 1, "x", ErrInvalidPlan},
		"a negative size":                {"ok", -1, "x", ErrInvalidPlan},
		"fewer bytes than the size says": {"ok", 5, "abc", io.ErrUnexpectedEOF},
		"more bytes than the size says":  {"ok", 1, "abc", ErrInvalidPlan},
		"a path already added":           {"m/z.zip", 1, "x", ErrInvalidPlan},
		"a path below a file added":      {"m/z.zip/x", 1, "x", ErrInvalidPlan},
		"the directory of a file added":  {"m", 1, "x", ErrInvalidPlan},
	} {
		t.Run(name, func(t *testing.T) {
			c := &composition{head: []member{file("a.go", "1")}, tree: []execution.TreeFile{treeFile("a.go", "1", execution.FromHead)}}
			_, app, err := c.compose(t)
			if err != nil {
				t.Fatalf("Compose: %v", err)
			}
			if err := app.Add("m/z.zip", 1, strings.NewReader("x")); err != nil {
				t.Fatalf("the first Add: %v", err)
			}
			if err := app.Add(add.name, add.size, strings.NewReader(add.body)); !errors.Is(err, add.err) {
				t.Errorf("Add(%q) = %v, want %v", add.name, err, add.err)
			}
		})
	}
}

func TestComposeRefusesALinkToTheNeighborsOfTheTreeDirectory(t *testing.T) {
	// Go's own state is beside the tree, and a link of the tree must not reach it: the root of the tree is the boundary of its links.
	m := symlink("l", "../.suiteward/proxy")
	c := &composition{head: []member{m}, tree: []execution.TreeFile{treeFile("l", "../.suiteward/proxy", execution.FromHead)}}
	_, _, err := c.compose(t)
	onlyError(t, err, ErrSourceMismatch)
	if !errors.Is(err, execution.ErrUnsafeSymlink) {
		t.Errorf("want the domain's ErrUnsafeSymlink inside the error, got %v", err)
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

func TestComposeGivesAManifestFileTheModeOfTheHeadsFileAtItsPath(t *testing.T) {
	// The canonical manifest records no mode, so a protected script keeps the mode that the head has for it.
	sha := strings.Repeat("4", 40)
	headEntry := func(mode execution.GitMode) map[Side][]execution.TreeEntry {
		return map[Side][]execution.TreeEntry{SideHead: {{Path: "scripts/run.sh", Mode: mode, BlobSHA1: sha}}}
	}
	for name, tc := range map[string]struct {
		entries map[Side][]execution.TreeEntry
		want    int64
	}{
		"an executable file":             {headEntry(execution.GitExecutable), 0o755},
		"a regular file":                 {headEntry(execution.GitRegular), 0o644},
		"git's legacy regular mode":      {headEntry("100664"), 0o644},
		"a symlink":                      {headEntry(execution.GitSymlink), 0o644},
		"no entry of the head at a path": {map[Side][]execution.TreeEntry{SideHead: {{Path: "other.sh", Mode: execution.GitExecutable, BlobSHA1: sha}}}, 0o644},
		"no head tree at all":            {nil, 0o644},
	} {
		t.Run(name, func(t *testing.T) {
			files := &manifestFiles{}
			c := &composition{
				tree:        []execution.TreeFile{{Path: "scripts/run.sh", Content: files.serve("#!/bin/sh\n"), Source: execution.FromManifest}},
				files:       files,
				treeEntries: tc.entries,
			}
			out, app, err := c.compose(t)
			if err != nil || app == nil {
				t.Fatalf("Compose: %v", err)
			}
			if found := readTar(t, out.Bytes()); found["src/scripts/run.sh"].mode != tc.want {
				t.Errorf("mode %o, want %o", found["src/scripts/run.sh"].mode, tc.want)
			}
		})
	}
}

func TestAppenderSpendChargesTheBudgetWithoutWriting(t *testing.T) {
	// A budget of 512 KiB, of which the tree takes 300 KiB.
	zeros := strings.Repeat("0", 300<<10)
	compose := func(t *testing.T) (*bytes.Buffer, *Appender) {
		c := &composition{
			head:   []member{file("a.bin", zeros)},
			tree:   []execution.TreeFile{treeFile("a.bin", zeros, execution.FromHead)},
			limits: Limits{ArchiveBytes: 1 << 20, FileBytes: 1 << 20, TmpfsMiB: 1},
		}
		out, app, err := c.compose(t)
		if err != nil {
			t.Fatalf("Compose: %v", err)
		}
		return out, app
	}
	t.Run("what is spent is not left for a file", func(t *testing.T) {
		out, app := compose(t)
		before := out.Len()
		if err := app.Spend(150 << 10); err != nil {
			t.Fatalf("Spend: %v", err)
		}
		if out.Len() != before {
			t.Errorf("Spend must write nothing")
		}
		more := strings.Repeat("m", 100<<10)
		onlyError(t, app.Add("m.zip", int64(len(more)), strings.NewReader(more)), ErrTooLarge)
	})
	t.Run("a file fits what is left", func(t *testing.T) {
		_, app := compose(t)
		if err := app.Spend(50 << 10); err != nil {
			t.Fatalf("Spend: %v", err)
		}
		small := strings.Repeat("m", 100<<10)
		if err := app.Add("m.zip", int64(len(small)), strings.NewReader(small)); err != nil {
			t.Errorf("Add: %v", err)
		}
	})
	t.Run("past the budget it is refused and charges nothing", func(t *testing.T) {
		_, app := compose(t)
		onlyError(t, app.Spend(300<<10), ErrTooLarge)
		small := strings.Repeat("m", 100<<10)
		if err := app.Add("m.zip", int64(len(small)), strings.NewReader(small)); err != nil {
			t.Errorf("a refused Spend must not take budget: %v", err)
		}
	})
	t.Run("a negative amount", func(t *testing.T) {
		_, app := compose(t)
		onlyError(t, app.Spend(-1), ErrInvalidPlan)
	})
}

// passedOn fails unless err is the error of an opener as it came: it is want, and the package has added no sentinel of its own to
// it, which would make a caller map one failure to two reasons.
func passedOn(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("want the error of the opener %v, got %v", want, err)
	}
	for _, sentinel := range allErrors {
		if errors.Is(err, sentinel) && !errors.Is(want, sentinel) {
			t.Errorf("the package added %v to the error of the opener: %v", sentinel, err)
		}
	}
}

type failingArchives struct{ err error }

func (a failingArchives) Open(context.Context, Side) (io.ReadCloser, error) { return nil, a.err }

type failingFiles struct{ err error }

func (f failingFiles) Open(context.Context, artifact.Digest) (io.ReadCloser, error) {
	return nil, f.err
}

func TestComposePassesTheErrorOfAnOpenerOn(t *testing.T) {
	github := fmt.Errorf("%w: the service answered 502", ErrGitHubUnavailable)
	for name, tc := range map[string]struct {
		err    error
		source execution.Source
	}{
		"an archive, and the sentinel that says why": {github, execution.FromHead},
		"an archive, and no sentinel":                {errNoArchive, execution.FromHead},
		"a manifest file, and the sentinel":          {github, execution.FromManifest},
		"a manifest file, and no sentinel":           {errNoFile, execution.FromManifest},
	} {
		t.Run(name, func(t *testing.T) {
			tree := []execution.TreeFile{treeFile("a.go", "1", tc.source)}
			entries := map[Side][]execution.TreeEntry{SideHead: treeOf(file("a.go", "1"))}
			_, err := Compose(context.Background(), io.Discard, tree, failingArchives{tc.err}, entries, failingFiles{tc.err}, generous)
			passedOn(t, err, tc.err)
		})
	}
}

// tarNames are the names of a tar in the order it holds them.
func tarNames(t *testing.T, data []byte) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatalf("the tar is unreadable: %v", err)
		}
		names = append(names, h.Name)
	}
}

func composeOne(t *testing.T) (*bytes.Buffer, *Appender) {
	t.Helper()
	c := &composition{head: []member{file("a.go", "1")}, tree: []execution.TreeFile{treeFile("a.go", "1", execution.FromHead)}}
	out, app, err := c.compose(t)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	return out, app
}

func TestCloseEndsTheTarWithTheCompletionMarker(t *testing.T) {
	out, app := composeOne(t)
	if err := app.Add(".suiteward/proxy/m.zip", 3, strings.NewReader("abc")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	names := tarNames(t, out.Bytes())
	if last := names[len(names)-1]; last != sandbox.CompleteMarker {
		t.Errorf("the last entry is %q, want the marker %q: %q", last, sandbox.CompleteMarker, names)
	}
	if marker := readTar(t, out.Bytes())[sandbox.CompleteMarker]; marker.typeflag != tar.TypeReg || marker.body != "" {
		t.Errorf("the marker is an empty regular file: %+v", marker)
	}
	if !bytes.HasSuffix(out.Bytes(), make([]byte, 1024)) {
		t.Errorf("the footer follows the marker")
	}
	length := out.Len()
	if err := app.Close(); err != nil || out.Len() != length {
		t.Errorf("a second Close writes nothing: %v, %d more bytes", err, out.Len()-length)
	}
}

func TestAddRefusesTheNamesOfTheCompletionMarker(t *testing.T) {
	for name, add := range map[string]string{
		"the marker":      sandbox.CompleteMarker,
		"a path below it": sandbox.CompleteMarker + "/x",
	} {
		t.Run(name, func(t *testing.T) {
			_, app := composeOne(t)
			onlyError(t, app.Add(add, 1, strings.NewReader("x")), ErrInvalidPlan)
		})
	}
	_, app := composeOne(t)
	if err := app.Add(path.Dir(sandbox.CompleteMarker)+"/proxy/m.zip", 1, strings.NewReader("x")); err != nil {
		t.Errorf("the directory of the marker is where the module proxy lives: %v", err)
	}
}

func TestAnAppenderThatFailedAfterWritingWritesNoMarker(t *testing.T) {
	boom := errors.New("the module stream broke")
	for name, reader := range map[string]io.Reader{
		"fewer bytes than declared":     strings.NewReader("ab"),
		"more bytes than declared":      strings.NewReader("abcd"),
		"a read error after the bytes":  io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(boom)),
		"a read error before the bytes": io.MultiReader(strings.NewReader("a"), iotest.ErrReader(boom)),
	} {
		t.Run(name, func(t *testing.T) {
			out, app := composeOne(t)
			if err := app.Add(".suiteward/proxy/m.zip", 3, reader); err == nil {
				t.Fatalf("Add must fail")
			}
			length := out.Len()
			if err := app.Close(); err == nil {
				t.Errorf("an Appender that wrote a damaged file must not end the tar as complete")
			}
			if out.Len() != length {
				t.Errorf("neither the marker nor the footer may follow a damaged file: %d bytes were written", out.Len()-length)
			}
		})
	}
}

func TestAddTakesOnlyTheEndOfInputForTheEndOfAFile(t *testing.T) {
	boom := errors.New("the module stream broke")
	_, app := composeOne(t)
	if err := app.Add("m.zip", 3, io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(boom))); !errors.Is(err, boom) {
		t.Errorf("an error after the declared bytes is the stream's, not an end: %v", err)
	}
	_, app = composeOne(t)
	if err := app.Add("m.zip", 3, strings.NewReader("abc")); err != nil {
		t.Errorf("exactly the declared bytes and then the end: %v", err)
	}
	_, app = composeOne(t)
	onlyError(t, app.Add("m.zip", 3, strings.NewReader("abcd")), ErrInvalidPlan)
}

func TestAFailedAddChargesNothing(t *testing.T) {
	// A budget of 512 KiB, of which the tree takes 300 KiB: 200 KiB can be spent after a failed Add of 100 KiB.
	zeros := strings.Repeat("0", 300<<10)
	c := &composition{
		head:   []member{file("a.bin", zeros)},
		tree:   []execution.TreeFile{treeFile("a.bin", zeros, execution.FromHead)},
		limits: Limits{ArchiveBytes: 1 << 20, FileBytes: 1 << 20, TmpfsMiB: 1},
	}
	_, app, err := c.compose(t)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if err := app.Add("m.zip", 100<<10, strings.NewReader(strings.Repeat("m", 100<<10+1))); err == nil {
		t.Fatalf("Add must fail: it has a byte too many")
	}
	if err := app.Spend(200 << 10); err != nil {
		t.Errorf("a failed Add must not take budget: %v", err)
	}
}
