// Package inventory resolves the exact protected inventory of one pinned
// source tree and classifies a candidate tree against the governing contract
// (D-SCOPE, D-SCOPE-DEFAULTS and D-APPROVAL-CONTEXT in docs/plan/decisions.md,
// ADR 0013).
package inventory

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// EntryKind is the kind of a source tree entry; its zero value is invalid.
type EntryKind uint8

const (
	File EntryKind = iota + 1
	Symlink
	Submodule
)

// Entry is one non-directory entry of a source tree.
type Entry struct {
	Path string
	Kind EntryKind
}

// Tree is one pinned source revision; directories are implicit.
type Tree interface {
	Entries(context.Context) ([]Entry, error)
	Read(ctx context.Context, path string) ([]byte, error)
}

var (
	ErrInvalidTree      = errors.New("invalid source tree")
	ErrUnsafeEntry      = errors.New("unsafe entry in the protected scope")
	ErrInvalidGoverning = errors.New("invalid governing contract")
)

// UnsafeEntryError is the ErrUnsafeEntry of one entry: Path is the link, or the declaration that is no regular file, that the
// protected scope cannot take. Its text is that of the sentinel and the quoted path.
type UnsafeEntryError struct{ Path string }

func (e *UnsafeEntryError) Error() string { return fmt.Sprintf("%v: %q", ErrUnsafeEntry, e.Path) }

func (e *UnsafeEntryError) Unwrap() error { return ErrUnsafeEntry }

// Inventory is the protected inventory of a tree under its own declaration.
type Inventory struct {
	manifest artifact.Manifest
	scope    scope.Scope
}

// Build resolves the protected inventory of tree under its own declaration,
// or scope.NoDeclaration when the tree has no .suiteward.yml. An invalid
// declaration returns scope.ErrInvalidDeclaration, so the caller keeps the
// governing scope in force (D-SCOPE).
func Build(ctx context.Context, tree Tree) (Inventory, error) {
	entries, err := ListEntries(ctx, tree)
	if err != nil {
		return Inventory{}, err
	}
	declaration := scope.NoDeclaration()
	known := map[string][]byte{}
	if i := slices.IndexFunc(entries, func(e Entry) bool { return e.Path == scope.DeclarationPath }); i >= 0 {
		if entries[i].Kind != File {
			return Inventory{}, &UnsafeEntryError{Path: scope.DeclarationPath}
		}
		raw, err := tree.Read(ctx, scope.DeclarationPath)
		if err != nil {
			return Inventory{}, fmt.Errorf("read %s: %w", scope.DeclarationPath, err)
		}
		if declaration, err = scope.ParseDeclaration(raw); err != nil {
			return Inventory{}, fmt.Errorf("%s: %w", scope.DeclarationPath, err)
		}
		// The manifest binds the same bytes the declaration was parsed from.
		known[scope.DeclarationPath] = raw
	}
	effective := scope.NewScope(declaration)
	manifest, err := manifestOf(ctx, tree, entries, effective, effective, known)
	if err != nil {
		return Inventory{}, err
	}
	return Inventory{manifest: manifest, scope: effective}, nil
}

// Manifest returns the exact protected inventory.
func (i Inventory) Manifest() artifact.Manifest {
	return i.manifest
}

// Scope returns the effective scope of the tree's declaration.
func (i Inventory) Scope() scope.Scope {
	return i.scope
}

// Contract returns the protected contract the inventory proposes. Covered
// inputs beyond the scope and the manifest are added in later phases.
func (i Inventory) Contract() (contract.ProtectedContract, error) {
	return contract.NewProtectedContract(i.manifest, i.scope.Digest(), map[string]string{})
}

// evaluate resolves the manifest of tree under the governing scope instead of the tree's own declaration, so that the governing
// rules decide which existing protection is checked (ADR 0013). The links of tree are also accepted by the exact excludes of
// accepting: the candidate's own scope, whose exclude is the reduction the owner approves (D-SCOPE, an accepted link).
func evaluate(ctx context.Context, tree Tree, governing, accepting scope.Scope) (artifact.Manifest, error) {
	entries, err := ListEntries(ctx, tree)
	if err != nil {
		return artifact.Manifest{}, err
	}
	return manifestOf(ctx, tree, entries, governing, accepting, map[string][]byte{})
}

// ListEntries returns the tree's entries once each has a valid path, a valid
// kind and a distinct path.
func ListEntries(ctx context.Context, tree Tree) ([]Entry, error) {
	entries, err := tree.Entries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list source entries: %w", err)
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		switch {
		case !artifact.ValidPath(entry.Path):
			return nil, fmt.Errorf("%w: invalid path %q", ErrInvalidTree, entry.Path)
		case entry.Kind < File || entry.Kind > Submodule:
			return nil, fmt.Errorf("%w: invalid kind %d for %q", ErrInvalidTree, entry.Kind, entry.Path)
		case seen[entry.Path]:
			return nil, fmt.Errorf("%w: duplicate path %q", ErrInvalidTree, entry.Path)
		}
		seen[entry.Path] = true
	}
	return entries, nil
}

// maxLinkHops bounds the links followed to resolve one symlink (D-SCOPE).
const maxLinkHops = 8

// manifestOf rejects a symlink inside effective that may hide protected content, unless accepting excludes it exactly, then hashes every covered file,
// reading from the tree only what known lacks. A submodule is outside the scope: it is never rejected and never read.
func manifestOf(ctx context.Context, tree Tree, entries []Entry, effective, accepting scope.Scope, known map[string][]byte) (artifact.Manifest, error) {
	index := newLayout(entries)
	targets := map[string]string{} // the blobs of the links read so far
	collisions, err := newCollider(entries, effective)
	if err != nil {
		return artifact.Manifest{}, err
	}
	covered := make([]string, 0, len(entries))
	for _, entry := range entries {
		covers, err := collisions.covers(effective, entry.Path)
		if err != nil {
			return artifact.Manifest{}, err
		}
		switch entry.Kind {
		case File:
			if covers {
				covered = append(covered, entry.Path)
			}
			continue
		case Submodule:
			continue
		}
		// D-SCOPE, an accepted link: an exclude that names exactly this link skips it. Nothing reached through it is protected.
		if accepting.ExcludesExactly(entry.Path) {
			continue
		}
		unsafe, err := unsafeLink(ctx, tree, index, targets, effective, collisions, entry.Path, covers)
		if err != nil {
			return artifact.Manifest{}, err
		}
		if unsafe {
			return artifact.Manifest{}, &UnsafeEntryError{Path: entry.Path}
		}
	}

	return manifestOfFiles(ctx, tree, covered, known)
}

// unsafeLink reports whether the link makes the declaration invalid (D-SCOPE): the scope covers it, or a pattern may reach below it
// unless it resolves to a regular file of the tree, which behaves as a file. The same test holds for the path the link would have in
// a case-twin directory of the tree (collider.rewrites), which a case-insensitive checkout could put it on.
func unsafeLink(ctx context.Context, tree Tree, index layout, targets map[string]string, effective scope.Scope, collisions collider, link string, covers bool) (bool, error) {
	if covers {
		return true, nil
	}
	resolved, known := false, false
	for _, candidate := range append([]string{link}, collisions.rewrites(link, true)...) {
		covered, err := effective.Covers(candidate)
		if err != nil || covered {
			return covered, err
		}
		below, err := effective.MayCoverBelow(candidate)
		if err != nil {
			return false, err
		}
		if !below {
			continue
		}
		if !known {
			if resolved, err = resolvesToFile(ctx, tree, index, targets, link); err != nil {
				return false, err
			}
			known = true
		}
		if !resolved {
			return true, nil
		}
	}
	return false, nil
}

// manifestOfFiles hashes the covered files, reading from the tree only what known lacks.
func manifestOfFiles(ctx context.Context, tree Tree, covered []string, known map[string][]byte) (artifact.Manifest, error) {
	files := make([]artifact.Entry, 0, len(covered))
	for _, path := range covered {
		content, found := known[path]
		if !found {
			var err error
			if content, err = tree.Read(ctx, path); err != nil {
				return artifact.Manifest{}, fmt.Errorf("read %q: %w", path, err)
			}
		}
		files = append(files, artifact.Entry{Path: path, Content: artifact.Hash(content)})
	}
	return artifact.NewManifest(files)
}

// layout indexes the entries of a tree for link resolution: the kind of each entry, and every directory, which has an
// entry somewhere below it and none of its own.
type layout struct {
	kinds map[string]EntryKind
	dirs  map[string]bool
}

func newLayout(entries []Entry) layout {
	l := layout{kinds: make(map[string]EntryKind, len(entries)), dirs: map[string]bool{}}
	for _, entry := range entries {
		l.kinds[entry.Path] = entry.Kind
		for dir := path.Dir(entry.Path); dir != "."; dir = path.Dir(dir) {
			l.dirs[dir] = true
		}
	}
	return l
}

func (l layout) isDir(p string) bool { return p == "" || (l.dirs[p] && l.kinds[p] == 0) }

// resolve returns the path that target names from the directory dir, the way a filesystem walks it: "" and "." stay,
// ".." leaves the last directory, and every directory passed through must be a real directory of the tree. A link, a file
// or a submodule in the middle, a missing directory, a ".." past the root and a target that ends in a directory form
// resolve to nothing, because a lexical join would send ".." through a link somewhere the tree does not show.
func (l layout) resolve(dir, target string) (string, bool) {
	segments := strings.Split(target, "/")
	if last := segments[len(segments)-1]; target == "" || target[0] == '/' || last == "" || last == "." || last == ".." {
		return "", false
	}
	var parts []string
	if dir != "." {
		parts = strings.Split(dir, "/")
	}
	for _, segment := range segments {
		if segment == "" || segment == "." {
			continue
		}
		if !l.isDir(strings.Join(parts, "/")) {
			return "", false
		}
		if segment != ".." {
			parts = append(parts, segment)
		} else if len(parts) == 0 {
			return "", false
		} else {
			parts = parts[:len(parts)-1]
		}
	}
	return strings.Join(parts, "/"), true
}

// resolvesToFile follows the symlink at link, in the same tree, through at most maxLinkHops links and reports whether
// it ends at a regular file. A link's blob is its target, relative to the link's directory (layout.resolve). Every
// target that resolves to nothing, a submodule, a directory and a loop end at no file. A failed read is an error, not
// an answer.
func resolvesToFile(ctx context.Context, tree Tree, l layout, targets map[string]string, link string) (bool, error) {
	for range maxLinkHops {
		target, read := targets[link]
		if !read {
			blob, err := tree.Read(ctx, link)
			if err != nil {
				return false, fmt.Errorf("read link %q: %w", link, err)
			}
			target = string(blob)
			targets[link] = target
		}
		next, ok := l.resolve(path.Dir(link), target)
		if !ok {
			return false, nil
		}
		switch l.kinds[next] { // a directory has no entry: the zero kind
		case File:
			return true, nil
		case Symlink:
			link = next
		default:
			return false, nil
		}
	}
	return false, nil
}

// Change classifies a candidate against the governing contract; its zero
// value is invalid.
type Change uint8

const (
	Unchanged Change = iota + 1
	ProtectedChange
	ScopeReduction
)

// Classification describes how a candidate differs from the governing
// contract. Every list is sorted and never nil.
type Classification struct {
	Change                                               Change
	Added, Removed, Modified, ReducedPaths, ReducedRules []string
}

// Governing is the approved scope and exact inventory of the canonical contract.
type Governing struct {
	Scope    scope.Scope
	Manifest artifact.Manifest
}

// Classify compares candidate with the governing contract. ScopeReduction
// wins over ProtectedChange; an implementation-only push is Unchanged here,
// since the caller knows the source revision changed (D-APPROVAL-CONTEXT).
func Classify(ctx context.Context, governing Governing, candidate Tree) (Classification, Inventory, error) {
	if governing.Manifest.IsZero() {
		return Classification{}, Inventory{}, fmt.Errorf("%w: absent manifest", ErrInvalidGoverning)
	}
	proposed, err := Build(ctx, candidate)
	if err != nil {
		return Classification{}, Inventory{}, err
	}
	underGoverning, err := evaluate(ctx, candidate, governing.Scope, proposed.scope)
	if err != nil {
		return Classification{}, Inventory{}, err
	}

	reduced := []string{}
	// A path of the governing inventory that the governing scope does not cover is a case collision, protected with its twin: its removal
	// is Removed, and the twin carries any reduction.
	for _, entry := range slices.Concat(underGoverning.Entries(), governing.Manifest.Entries()) {
		inGoverning, err := governing.Scope.Covers(entry.Path)
		if err != nil {
			return Classification{}, Inventory{}, err
		}
		if !inGoverning {
			continue
		}
		covers, err := proposed.scope.Covers(entry.Path)
		if err != nil {
			return Classification{}, Inventory{}, err
		}
		if !covers {
			reduced = append(reduced, entry.Path)
		}
	}
	slices.Sort(reduced)

	c := Classification{ReducedPaths: slices.Compact(reduced), ReducedRules: scope.Reductions(governing.Scope, proposed.scope)}
	c.Added, c.Removed, c.Modified = compare(governing.Manifest, proposed.manifest)
	switch {
	case len(c.ReducedRules) > 0 || len(c.ReducedPaths) > 0:
		c.Change = ScopeReduction
	case len(c.Added) > 0 || len(c.Removed) > 0 || len(c.Modified) > 0 || governing.Scope.Digest() != proposed.scope.Digest():
		c.Change = ProtectedChange
	default:
		c.Change = Unchanged
	}
	return c, proposed, nil
}

// compare lists, in path order, the paths only in proposed, only in
// governing, and in both with different content.
func compare(governing, proposed artifact.Manifest) (added, removed, modified []string) {
	added, removed, modified = []string{}, []string{}, []string{}
	before := map[string]artifact.Digest{}
	for _, entry := range governing.Entries() {
		before[entry.Path] = entry.Content
	}
	after := map[string]bool{}
	for _, entry := range proposed.Entries() {
		after[entry.Path] = true
		content, found := before[entry.Path]
		switch {
		case !found:
			added = append(added, entry.Path)
		case content != entry.Content:
			modified = append(modified, entry.Path)
		}
	}
	for _, entry := range governing.Entries() {
		if !after[entry.Path] {
			removed = append(removed, entry.Path)
		}
	}
	return added, removed, modified
}

// collider finds the paths that a case-insensitive checkout could put on a covered path (D-SCOPE, case-sensitive matching). A path is
// protected too when it case-folds equal to a covered file, or when rewriting a directory prefix of it onto a real directory of the same
// tree that differs from it only in case gives a path the scope covers. Matching stays case-sensitive and a look-alike character folds
// to nothing (Unicode simple folding), so only the tree's own paths decide.
type collider struct {
	files map[string]bool     // the folded covered files
	dirs  map[string][]string // the directories of the tree, by folded path
}

// newCollider indexes the covered regular files and the directories of entries.
func newCollider(entries []Entry, effective scope.Scope) (collider, error) {
	c := collider{files: map[string]bool{}, dirs: map[string][]string{}}
	for _, entry := range entries {
		covers, err := effective.Covers(entry.Path)
		if err != nil {
			return collider{}, err
		}
		if entry.Kind == File && covers {
			c.files[foldKey(entry.Path)] = true
		}
		for dir := path.Dir(entry.Path); dir != "."; dir = path.Dir(dir) {
			key := foldKey(dir)
			if slices.Contains(c.dirs[key], dir) {
				break // its parents are in already
			}
			c.dirs[key] = append(c.dirs[key], dir)
		}
	}
	return c, nil
}

// rewrites lists the paths p becomes when a directory prefix of p is replaced by a real directory of the tree that differs from it in
// case; with self, p itself is such a prefix too (a link standing for a directory).
func (c collider) rewrites(p string, self bool) []string {
	var rewritten []string
	for end := 0; end <= len(p); end++ {
		if end < len(p) && p[end] != '/' || end == len(p) && !self {
			continue
		}
		for _, dir := range c.dirs[foldKey(p[:end])] {
			if dir != p[:end] {
				rewritten = append(rewritten, dir+p[end:])
			}
		}
	}
	return rewritten
}

// covers reports whether effective covers p or p collides with a path it covers.
func (c collider) covers(effective scope.Scope, p string) (bool, error) {
	covers, err := effective.Covers(p)
	if err != nil || covers {
		return covers, err
	}
	if c.files[foldKey(p)] {
		return true, nil
	}
	for _, rewritten := range c.rewrites(p, false) {
		if covers, err = effective.Covers(rewritten); err != nil || covers {
			return covers, err
		}
	}
	return false, nil
}

// foldKey maps every rune to the smallest of its simple-folding orbit, so two paths have one key exactly when strings.EqualFold holds for them.
func foldKey(s string) string {
	return strings.Map(func(r rune) rune {
		smallest := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			smallest = min(smallest, f)
		}
		return smallest
	}, s)
}
