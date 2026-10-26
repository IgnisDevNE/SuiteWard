package execution

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// ErrInvalidTree reports an input the tree rules refuse: an absent manifest, or a file that may not cross from the head.
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

func sortTree(tree []TreeFile) {
	slices.SortFunc(tree, func(a, b TreeFile) int { return strings.Compare(a.Path, b.Path) })
}

// EvaluatedFiles lists the head files the receipt evaluates: those matched by the canonical selection whose bytes differ
// from the governing manifest, or that the manifest lacks. They are added or changed relative to the canonical, not to the
// merge base. The Go profile picks the tests within them. The paths are in path order.
func EvaluatedFiles(head, governing artifact.Manifest, sel Selection) ([]string, error) {
	if err := refuseAbsent(head, governing); err != nil {
		return nil, err
	}
	manifest := digestsOf(governing)
	var files []string
	for _, entry := range head.Entries() {
		matched, err := sel.Matches(entry.Path)
		if err != nil {
			return nil, err
		}
		if digest, ok := manifest[entry.Path]; matched && (!ok || digest != entry.Content) {
			files = append(files, entry.Path)
		}
	}
	return files, nil
}

// CanonicalTree is the tree of the canonical plan.
func CanonicalTree(head, governing artifact.Manifest, governingScope scope.Scope) ([]TreeFile, error) {
	return nil, errNotImplemented
}

// BaseSideTree is the tree of the receipt's base side.
func BaseSideTree(base, head, expected artifact.Manifest, sel Selection) ([]TreeFile, error) {
	return nil, errNotImplemented
}
