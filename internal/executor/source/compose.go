package source

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// Modes of the files in the composed tar. The sandbox extracts it as nobody, and the tree records only whether a file is executable.
const (
	regularMode    = 0o644
	executableMode = 0o755
	symlinkMode    = 0o777
)

// TreeDir is the directory of the sandbox's /work that holds the tree: Compose writes every tree entry below it, so that go's own state (the
// module proxy, the caches) lives beside the tree and not in it. The Appender's names are relative to /work and never lie in it. A caller
// that runs the tests of a package of the tree takes TreeDir + "/" + the directory of the package as its work directory.
const TreeDir = "src"

func inTree(name string) string { return TreeDir + "/" + name }

// plan is the files of a composed tree by where their bytes come from.
type plan struct {
	archives map[Side]map[string]execution.TreeFile // the files of each side's archive, by path
	manifest []execution.TreeFile                   // the files of the governing manifest, in the order of the tree
}

// newPlan groups the tree by source, and refuses a tree that names a source with nothing behind it, a path twice or a path that is
// not a path of a tree. These are mistakes of the caller, and the writer has written nothing yet.
func newPlan(tree []execution.TreeFile, entries map[Side][]execution.TreeEntry) (plan, error) {
	p := plan{archives: map[Side]map[string]execution.TreeFile{}}
	seen := map[string]bool{}
	for _, f := range tree {
		if !artifact.ValidPath(f.Path) || f.Content.IsZero() || seen[f.Path] {
			return plan{}, fmt.Errorf("%w: %q is not a path of a tree with a digest, or is listed twice", ErrInvalidPlan, f.Path)
		}
		seen[f.Path] = true
		switch f.Source {
		case execution.FromManifest:
			p.manifest = append(p.manifest, f)
		case execution.FromHead, execution.FromBase:
			side := SideHead
			if f.Source == execution.FromBase {
				side = SideBase
			}
			if _, ok := entries[side]; !ok {
				return plan{}, fmt.Errorf("%w: %q comes from the %s commit, whose tree entries were not given", ErrInvalidPlan, f.Path, side)
			}
			if p.archives[side] == nil {
				p.archives[side] = map[string]execution.TreeFile{}
			}
			p.archives[side][f.Path] = f
		default:
			return plan{}, fmt.Errorf("%w: %q has the source %d, which is none of the three", ErrInvalidPlan, f.Path, f.Source)
		}
	}
	return p, nil
}

// composer writes the tar and counts what it holds against the budget.
type composer struct {
	tw     *tar.Writer
	limits Limits
	total  int64
	files  []execution.ExtractedFile // what was written, for the check of the composed tree
}

// Compose writes to w the uncompressed tar of exactly the files of tree, which the domain's tree functions produce, each below
// TreeDir (the links of the tree stay inside it: its root is their boundary), and returns the
// Appender that adds the module files to the same tar. The files of the head and the base come from their archive streams, which
// are opened for the sides the tree uses only; each is checked against the tree entries of its side (the blob SHA-1, from the size
// in the tar header) and against the SHA-256 that the plan expects, while it is copied. The files of the manifest come from files,
// whole and checked by digest before a byte of them is written, and each is executable when the head has an executable file at its path. Nothing else of an archive is written, and a file of the tree that
// an archive lacks, or has twice, is a mismatch.
//
// Bytes reach w before a file is fully verified, so a failure (an error that is returned with no Appender) means the tar is not to
// be used: the caller must stop the sandbox that reads it, and Compose never writes the footer that ends a tar. The budget is half of
// l.TmpfsMiB: a file that would pass it is ErrTooLarge and is not written. Once every file is in, the symlinks of the composed tree
// are checked as a whole, because links that are safe in their own commit can reach beyond the root together.
//
// ErrSourceMismatch, ErrTooLarge, ErrGitHubUnavailable, ErrArchiveUnreadable and ErrInvalidPlan are as in Inspect. An error of w
// is returned as it is.
func Compose(ctx context.Context, w io.Writer, tree []execution.TreeFile, archives ArchiveOpener,
	entries map[Side][]execution.TreeEntry, files FileOpener, l Limits) (*Appender, error) {
	p, err := newPlan(tree, entries)
	if err != nil {
		return nil, err
	}
	c := &composer{tw: tar.NewWriter(w), limits: l}
	for _, side := range []Side{SideHead, SideBase} {
		if want := p.archives[side]; len(want) > 0 {
			if err := c.fromArchive(ctx, archives, side, want, entries[side]); err != nil {
				return nil, err
			}
		}
	}
	headModes := make(map[string]execution.GitMode, len(entries[SideHead]))
	for _, e := range entries[SideHead] {
		headModes[e.Path] = e.Mode
	}
	for _, f := range p.manifest {
		if err := c.fromManifest(ctx, files, f, headModes[f.Path]); err != nil {
			return nil, err
		}
	}
	if err := c.checkLinks(); err != nil {
		return nil, err
	}
	return newAppender(c), nil
}

// fromArchive copies the files of the plan from one side's archive.
func (c *composer) fromArchive(ctx context.Context, archives ArchiveOpener, side Side, want map[string]execution.TreeFile, tree []execution.TreeEntry) error {
	entries := make(map[string]execution.TreeEntry, len(tree))
	for _, e := range tree {
		entries[e.Path] = e
	}
	rc, err := archives.Open(ctx, side)
	if err != nil {
		return fmt.Errorf("open the %s archive: %w", side, err)
	}
	defer func() { _ = rc.Close() }()
	done := map[string]bool{}
	err = walk(ctx, rc, c.limits, func(e entry) error {
		f, ok := want[e.path]
		if !ok {
			return nil // not a file of this plan
		}
		te, ok := entries[e.path]
		if !ok {
			return mismatch("%q is in the plan but not in the %s tree", e.path, side)
		}
		if done[e.path] {
			return mismatch("%q is in the %s archive twice", e.path, side)
		}
		done[e.path] = true
		return c.copyEntry(e, f, te)
	})
	if err != nil {
		return err
	}
	for _, p := range slices.Sorted(maps.Keys(want)) {
		if !done[p] {
			return mismatch("%q is in the plan but not in the %s archive", p, side)
		}
	}
	return nil
}

// kindOf reads a mode of a tree entry as the archive would show it. 100664 is git's legacy mode of a regular file.
func kindOf(m execution.GitMode) (k kind, exec, ok bool) {
	switch m {
	case execution.GitRegular, "100664":
		return regular, false, true
	case execution.GitExecutable:
		return regular, true, true
	case execution.GitSymlink:
		return symbolic, false, true
	default:
		return 0, false, false
	}
}

// copyEntry writes one file of an archive and verifies it against its tree entry and the plan.
func (c *composer) copyEntry(e entry, f execution.TreeFile, te execution.TreeEntry) error {
	if k, exec, ok := kindOf(te.Mode); !ok || k != e.kind || exec != e.exec {
		return mismatch("%q is not the %s of the tree", e.path, te.Mode)
	}
	if e.kind == regular && e.size > c.limits.FileBytes {
		return fmt.Errorf("%w: %q is %d bytes, over the cap of %d", ErrTooLarge, e.path, e.size, c.limits.FileBytes)
	}
	if err := c.reserve(e.path, e.size); err != nil {
		return err
	}
	if e.kind == symbolic {
		if execution.BlobSHA1([]byte(e.target)) != te.BlobSHA1 || artifact.Hash([]byte(e.target)) != f.Content {
			return mismatch("the symlink %q is not the tree's", e.path)
		}
		if err := c.tw.WriteHeader(&tar.Header{Name: inTree(e.path), Typeflag: tar.TypeSymlink, Linkname: e.target, Mode: symlinkMode}); err != nil {
			return err
		}
		c.record(execution.ExtractedFile{Path: e.path, Mode: execution.GitSymlink, Target: e.target}, 0)
		return nil
	}
	mode, gitMode := int64(regularMode), execution.GitRegular
	if e.exec {
		mode, gitMode = executableMode, execution.GitExecutable
	}
	if err := c.tw.WriteHeader(&tar.Header{Name: inTree(e.path), Typeflag: tar.TypeReg, Mode: mode, Size: e.size}); err != nil {
		return err
	}
	blob, content, err := digests(c.tw, e)
	if err != nil {
		return err
	}
	if blob != te.BlobSHA1 || content != f.Content {
		return mismatch("the bytes of %q are not the tree's", e.path)
	}
	c.record(execution.ExtractedFile{Path: e.path, Mode: gitMode, BlobSHA1: blob}, e.size)
	return nil
}

// fromManifest writes one file of the governing manifest. The file is read whole, within the cap of a file, and checked by digest
// before a byte of it reaches the tar. The manifest records no mode, so the file is executable when the head has an executable file
// at its path (headMode, empty when it has none), and a regular file otherwise.
func (c *composer) fromManifest(ctx context.Context, files FileOpener, f execution.TreeFile, headMode execution.GitMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rc, err := files.Open(ctx, f.Content)
	if err != nil {
		return fmt.Errorf("%w: open the manifest file %q: %w", ErrArchiveUnreadable, f.Path, err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, c.limits.FileBytes+1))
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		return fmt.Errorf("%w: read the manifest file %q: %w", ErrArchiveUnreadable, f.Path, err)
	case int64(len(data)) > c.limits.FileBytes:
		return fmt.Errorf("%w: the manifest file %q is over the cap of %d bytes", ErrTooLarge, f.Path, c.limits.FileBytes)
	case artifact.Hash(data) != f.Content:
		return fmt.Errorf("%w: the manifest file %q is not the bytes of %s", ErrArchiveUnreadable, f.Path, f.Content)
	}
	if err := c.reserve(f.Path, int64(len(data))); err != nil {
		return err
	}
	mode, gitMode := int64(regularMode), execution.GitRegular
	if headMode == execution.GitExecutable {
		mode, gitMode = executableMode, execution.GitExecutable
	}
	if err := c.tw.WriteHeader(&tar.Header{Name: inTree(f.Path), Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(data))}); err != nil {
		return err
	}
	if _, err := c.tw.Write(data); err != nil {
		return err
	}
	c.record(execution.ExtractedFile{Path: f.Path, Mode: gitMode, BlobSHA1: execution.BlobSHA1(data)}, int64(len(data)))
	return nil
}

// reserve refuses a file that would take the tree past the budget, before any of it is written.
func (c *composer) reserve(name string, size int64) error {
	if c.total+size > c.limits.budget() {
		return fmt.Errorf("%w: %q would take the tree past half of the %d MiB tmpfs", ErrTooLarge, name, c.limits.TmpfsMiB)
	}
	return nil
}

func (c *composer) record(f execution.ExtractedFile, size int64) {
	c.files = append(c.files, f)
	c.total += size
}

// checkLinks runs the rules of CheckSource over the composed tree against itself: the files are what they are, so only the rules
// that look at the whole tree can fail (a symlink that resolves outside it, names that alias).
func (c *composer) checkLinks() error {
	tree := make([]execution.TreeEntry, len(c.files))
	for i, f := range c.files {
		tree[i] = execution.TreeEntry{Path: f.Path, Mode: f.Mode, BlobSHA1: f.BlobSHA1}
		if f.Mode == execution.GitSymlink {
			tree[i].BlobSHA1 = execution.BlobSHA1([]byte(f.Target))
		}
	}
	if _, err := execution.CheckSource(tree, c.files); err != nil {
		return fmt.Errorf("%w: the composed tree: %w", ErrSourceMismatch, err)
	}
	return nil
}

// Appender adds extra regular files, the module files of the dependency prefetch, to the tar that Compose started. They count
// against the same budget as the tree. An Appender is for one goroutine.
type Appender struct {
	c      *composer
	paths  map[string]bool // the files added so far
	dirs   map[string]bool // and their directories
	closed bool
}

func newAppender(c *composer) *Appender {
	return &Appender{c: c, paths: map[string]bool{}, dirs: map[string]bool{}}
}

func (a *Appender) markDirs(name string) {
	for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
		a.dirs[dir] = true
	}
}

// conflicts reports a name that is the tree directory or lies in it, or that is a file or a directory of the files added already, or
// lies below one of them. The files of the tree are all below TreeDir, so no other name can meet them.
func (a *Appender) conflicts(name string) bool {
	if name == TreeDir || strings.HasPrefix(name, TreeDir+"/") || a.paths[name] || a.dirs[name] {
		return true
	}
	for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
		if a.paths[dir] {
			return true
		}
	}
	return false
}

// Add writes the file name of exactly size bytes, read from r. The name is a clean relative path to /work that is not TreeDir or
// below it, and that no file added before has, is below or is the directory of. A file that would pass the budget is ErrTooLarge and is not written, and a name or a size that
// cannot be written is ErrInvalidPlan, also before anything is written. After any other error the tar is not to be used.
func (a *Appender) Add(name string, size int64, r io.Reader) error {
	switch {
	case a.closed:
		return fmt.Errorf("%w: the tar is closed", ErrInvalidPlan)
	case !artifact.ValidPath(name) || size < 0 || a.conflicts(name):
		return fmt.Errorf("%w: %q cannot be added with %d bytes: it is not a new clean path", ErrInvalidPlan, name, size)
	}
	if err := a.c.reserve(name, size); err != nil {
		return err
	}
	if err := a.c.tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: regularMode, Size: size}); err != nil {
		return err
	}
	n, err := io.Copy(a.c.tw, io.LimitReader(r, size))
	switch {
	case err != nil:
		return fmt.Errorf("copy %q: %w", name, err)
	case n < size:
		return fmt.Errorf("%q has %d of the %d bytes it declared: %w", name, n, size, io.ErrUnexpectedEOF)
	}
	if _, err := io.CopyN(io.Discard, r, 1); err == nil {
		return fmt.Errorf("%w: %q has more than the %d bytes it declared", ErrInvalidPlan, name, size)
	}
	a.paths[name] = true
	a.c.total += size
	a.markDirs(name)
	return nil
}

// Close ends the tar with its footer. Nothing can be added after it.
func (a *Appender) Close() error {
	a.closed = true
	return a.c.tw.Close()
}

// Spend charges n bytes against the budget without writing anything, for what the tar will hold once it is unpacked and does not
// carry itself: the unpacked size of the module zips that Add wrote. Past the budget it is ErrTooLarge and charges nothing, and a
// negative n is ErrInvalidPlan.
func (a *Appender) Spend(n int64) error {
	if n < 0 {
		return fmt.Errorf("%w: cannot spend %d bytes", ErrInvalidPlan, n)
	}
	if err := a.c.reserve("the unpacked module files", n); err != nil {
		return err
	}
	a.c.total += n
	return nil
}
