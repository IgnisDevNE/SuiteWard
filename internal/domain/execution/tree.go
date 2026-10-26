package execution

import (
	"errors"

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
func (s Selection) Matches(path string) (bool, error) { return false, errNotImplemented }

// CanonicalTree is the tree of the canonical plan: the head with the governing manifest overlaid. Every head file matched
// by the selection is replaced by the manifest file, and removed when the manifest lacks it; other head files are kept;
// a manifest file the head lacks is added, whether or not the selection matches it. The files are in path order.
func CanonicalTree(head, governing artifact.Manifest, sel Selection) ([]TreeFile, error) {
	return nil, errNotImplemented
}

// EvaluatedFiles lists the head files the receipt evaluates: those matched by the canonical selection whose bytes differ
// from the governing manifest, or that the manifest lacks. They are added or changed relative to the canonical, not to the
// merge base. The Go profile picks the tests within them. The paths are in path order.
func EvaluatedFiles(head, governing artifact.Manifest, sel Selection) ([]string, error) {
	return nil, errNotImplemented
}

// BaseSideTree is the tree of the receipt's base side: the base commit, with the head version of each file that holds an
// evaluated test and nothing else from the head, so source, go.mod, go.sum, runner files and .suiteward.yml stay the
// base's. A carried file must exist in the head and be matched by the canonical selection, or the tree is refused. The
// files are in path order.
func BaseSideTree(base, head artifact.Manifest, sel Selection, carried []string) ([]TreeFile, error) {
	return nil, errNotImplemented
}
