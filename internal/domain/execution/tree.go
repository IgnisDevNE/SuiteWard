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

// CanonicalTree is the tree of the canonical plan: the head with the governing manifest overlaid. Every head file matched
// by the selection is replaced by the manifest file, and removed when the manifest lacks it; other head files are kept;
// a manifest file the head lacks is added, whether or not the selection matches it. The files are in path order.
func CanonicalTree(head, governing artifact.Manifest, sel Selection) ([]TreeFile, error) {
	if err := refuseAbsent(head, governing); err != nil {
		return nil, err
	}
	manifest := digestsOf(governing)
	inHead := map[string]bool{}
	var tree []TreeFile
	for _, entry := range head.Entries() {
		inHead[entry.Path] = true
		matched, err := sel.Matches(entry.Path)
		if err != nil {
			return nil, err
		}
		if !matched {
			tree = append(tree, TreeFile{Path: entry.Path, Content: entry.Content, Source: FromHead})
		} else if digest, ok := manifest[entry.Path]; ok {
			tree = append(tree, TreeFile{Path: entry.Path, Content: digest, Source: FromManifest})
		}
	}
	for _, entry := range governing.Entries() {
		if !inHead[entry.Path] {
			tree = append(tree, TreeFile{Path: entry.Path, Content: entry.Content, Source: FromManifest})
		}
	}
	sortTree(tree)
	return tree, nil
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

// BaseSideTree is the tree of the receipt's base side: the base commit, with the head version of each file that holds an
// evaluated test and nothing else from the head, so source, go.mod, go.sum, runner files and .suiteward.yml stay the
// base's. A carried file must exist in the head and be matched by the canonical selection, or the tree is refused. The
// files are in path order.
func BaseSideTree(base, head artifact.Manifest, sel Selection, carried []string) ([]TreeFile, error) {
	if err := refuseAbsent(base, head); err != nil {
		return nil, err
	}
	headDigests := digestsOf(head)
	fromHead := map[string]artifact.Digest{}
	for _, path := range carried {
		digest, ok := headDigests[path]
		if !ok {
			return nil, fmt.Errorf("%w: %q is not in the head", ErrInvalidTree, path)
		}
		matched, err := sel.Matches(path)
		if err != nil {
			return nil, err
		}
		if !matched {
			return nil, fmt.Errorf("%w: %q is not matched by the canonical include", ErrInvalidTree, path)
		}
		fromHead[path] = digest
	}
	var tree []TreeFile
	for _, entry := range base.Entries() {
		if _, ok := fromHead[entry.Path]; !ok {
			tree = append(tree, TreeFile{Path: entry.Path, Content: entry.Content, Source: FromBase})
		}
	}
	for path, digest := range fromHead {
		tree = append(tree, TreeFile{Path: path, Content: digest, Source: FromHead})
	}
	sortTree(tree)
	return tree, nil
}
