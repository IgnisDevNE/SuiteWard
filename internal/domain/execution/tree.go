package execution

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// ErrInvalidTree reports an input the tree rules refuse: an absent manifest, or a composed tree with a path below another path of it.
var ErrInvalidTree = errors.New("invalid tree composition")

// Source says whose bytes a file of a composed tree holds.
type Source uint8

// The sources of a file of a composed tree.
const (
	FromHead     Source = iota + 1 // the head commit
	FromBase                       // the base commit
	FromManifest                   // the governing manifest, fetched by Content and verified against it
)

// TreeFile is one file of a composed tree: where its bytes come from, and their digest.
type TreeFile struct {
	Path    string
	Content artifact.Digest
	Source  Source
}

// Selection is the canonical include and exclude patterns. They alone decide which files are tests: a pattern only the
// head declares never reaches it, so the head can add tests to a run but never silence one or move code between sides.
type Selection struct {
	Include, Exclude []scope.Pattern
}

// Matches reports whether path is matched by an include pattern and by no exclude pattern.
func (s Selection) Matches(path string) (bool, error) {
	excluded, err := matchesAny(s.Exclude, path)
	if err != nil || excluded {
		return false, err
	}
	return matchesAny(s.Include, path)
}

func matchesAny(list []scope.Pattern, path string) (bool, error) {
	for _, pattern := range list {
		if matched, err := pattern.Match(path); err != nil || matched {
			return matched, err
		}
	}
	return false, nil
}

// digestsOf indexes the files of a manifest by path.
func digestsOf(m artifact.Manifest) map[string]artifact.Digest {
	digests := map[string]artifact.Digest{}
	for _, entry := range m.Entries() {
		digests[entry.Path] = entry.Content
	}
	return digests
}

func refuseAbsent(manifests ...artifact.Manifest) error {
	if slices.ContainsFunc(manifests, artifact.Manifest.IsZero) {
		return fmt.Errorf("%w: a manifest is absent", ErrInvalidTree)
	}
	return nil
}

// finishTree puts the files of a composed tree in path order, and refuses a tree that holds a path below another path of it,
// as the head's file x and the manifest's x/y would make: a file and a directory on one name. The check is by set, because
// the sort order does not put a file next to its descendants ("a", "a.txt", "a/b").
func finishTree(tree []TreeFile) ([]TreeFile, error) {
	slices.SortFunc(tree, func(a, b TreeFile) int { return strings.Compare(a.Path, b.Path) })
	paths := map[string]bool{}
	for _, f := range tree {
		paths[f.Path] = true
	}
	if err := refuseBelowFile(paths, paths, ErrInvalidTree); err != nil {
		return nil, err
	}
	return tree, nil
}

// CanonicalTree is the tree of the canonical plan: the head with the whole governing manifest overlaid. Every manifest file
// is in the tree with the manifest's bytes, whether it is a test, a runner file, the declaration or a workflow. A head file
// that the manifest lacks is removed when the governing scope covers it, and kept otherwise, so the head loses no file
// outside the scope. The files are in path order.
func CanonicalTree(head, governing artifact.Manifest, governingScope scope.Scope) ([]TreeFile, error) {
	if err := refuseAbsent(head, governing); err != nil {
		return nil, err
	}
	manifest := digestsOf(governing)
	var tree []TreeFile
	for _, entry := range governing.Entries() {
		tree = append(tree, TreeFile{Path: entry.Path, Content: entry.Content, Source: FromManifest})
	}
	for _, entry := range head.Entries() {
		if _, inManifest := manifest[entry.Path]; inManifest {
			continue
		}
		covered, err := governingScope.Covers(entry.Path)
		if err != nil {
			return nil, err
		}
		if !covered {
			tree = append(tree, TreeFile{Path: entry.Path, Content: entry.Content, Source: FromHead})
		}
	}
	return finishTree(tree)
}

// EvaluatedFiles lists the head files the receipt evaluates: those matched by the canonical selection whose bytes differ
// from expected, the canonical version the revision expects, or that expected lacks. They are added or changed relative to
// that canonical, not to the merge base. The Go profile picks the tests within them. The paths are in path order.
func EvaluatedFiles(head, expected artifact.Manifest, sel Selection) ([]string, error) {
	if err := refuseAbsent(head, expected); err != nil {
		return nil, err
	}
	canonical := digestsOf(expected)
	var files []string
	for _, entry := range head.Entries() {
		matched, err := sel.Matches(entry.Path)
		if err != nil {
			return nil, err
		}
		if digest, ok := canonical[entry.Path]; matched && (!ok || digest != entry.Content) {
			files = append(files, entry.Path)
		}
	}
	return files, nil
}

// BaseSideTree is the tree of the receipt's base side: the base commit, with the head version of every file that
// EvaluatedFiles lists (test files, testdata and helpers alike) and nothing else from the head, so source, go.mod, go.sum,
// runner files and .suiteward.yml stay the base's. Only the canonical selection decides what crosses, so a pattern that only
// the head declares moves no code. The files are in path order.
func BaseSideTree(base, head, expected artifact.Manifest, sel Selection) ([]TreeFile, error) {
	if err := refuseAbsent(base, head, expected); err != nil {
		return nil, err
	}
	carried, err := EvaluatedFiles(head, expected, sel)
	if err != nil {
		return nil, err
	}
	headDigests := digestsOf(head)
	fromHead := map[string]bool{}
	var tree []TreeFile
	for _, path := range carried {
		fromHead[path] = true
		tree = append(tree, TreeFile{Path: path, Content: headDigests[path], Source: FromHead})
	}
	for _, entry := range base.Entries() {
		if !fromHead[entry.Path] {
			tree = append(tree, TreeFile{Path: entry.Path, Content: entry.Content, Source: FromBase})
		}
	}
	return finishTree(tree)
}
