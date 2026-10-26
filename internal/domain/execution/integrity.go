package execution

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

var (
	// ErrInvalidSource reports a commit tree or an extraction the check cannot read: a malformed path, mode or blob
	// SHA-1, a path listed twice, or an extracted file whose fields do not fit its mode.
	ErrInvalidSource = errors.New("invalid source tree")
	// ErrUnsafeSymlink reports a symlink whose target leaves the tree, or does not resolve inside it.
	ErrUnsafeSymlink = errors.New("symlink target leaves the tree")
)

// maxLinkHops is how many symlinks resolving one target may follow, as in the D-SCOPE resolution of links.
const maxLinkHops = 8

// GitMode is the mode of a git tree entry, as git spells it in octal.
type GitMode string

// The modes of a git tree entry.
const (
	GitRegular    GitMode = "100644"
	GitExecutable GitMode = "100755"
	GitSymlink    GitMode = "120000"
	GitSubmodule  GitMode = "160000"
	GitTree       GitMode = "040000"
)

// gitLegacyRegular is git's old mode for a regular file with group write; a tree may still hold it, and it reads as GitRegular.
const gitLegacyRegular GitMode = "100664"

// TreeEntry is one entry of a commit's tree, as the service reads it from GitHub with a recursive tree request: its path,
// its mode and the SHA-1 of its object. A directory entry (GitTree) names no file and is ignored.
type TreeEntry struct {
	Path     string
	Mode     GitMode
	BlobSHA1 string
}

// ExtractedFile is one file the executor found in the extracted archive. A regular or executable file carries the blob
// SHA-1 of its bytes (BlobSHA1) and no Target; a symlink carries its Target, the text of its blob, and no BlobSHA1: the
// check derives it.
type ExtractedFile struct {
	Path     string
	Mode     GitMode
	BlobSHA1 string
	Target   string
}

// BlobSHA1 is the SHA-1 git gives a blob: that of "blob <len>\0" followed by the bytes, as lowercase hexadecimal.
func BlobSHA1(content []byte) string {
	sum := sha1.Sum(slices.Concat([]byte("blob "+strconv.Itoa(len(content))+"\x00"), content))
	return hex.EncodeToString(sum[:])
}

// SourceIntegrity is how an extraction differs from its commit's tree. Each list is sorted.
type SourceIntegrity struct {
	Missing    []string // in the tree, not extracted
	Extra      []string // extracted, not in the tree
	Mismatched []string // in both, with another mode or another blob
	Submodules []string // skipped: a submodule is outside the commit's own files, and its content is never read
}

// Intact reports whether the extraction is the commit's tree: nothing missing, extra or mismatched. Skipped submodules
// do not break it.
func (i SourceIntegrity) Intact() bool {
	return len(i.Missing)+len(i.Extra)+len(i.Mismatched) == 0
}

// blob is what identifies a file: its mode and the SHA-1 of its object.
type blob struct {
	mode GitMode
	sha1 string
}

// CheckSource compares the files of an extracted archive with the entries of the commit's tree. Git archives can drop
// files (export-ignore), rewrite them (export-subst) and replace LFS content with pointers, so an archive is not the
// tree; any difference makes the run unavailable. A symlink must resolve inside the tree, following the links of the
// extraction and at most eight hops, or the check fails with ErrUnsafeSymlink.
//
// The check is lexical. It folds case and trailing dots and spaces, as NTFS and APFS do, but it cannot model 8.3 short
// names or Unicode normalization, so the executor must also materialize the tree with OS-level containment (os.Root, or
// EvalSymlinks against the root) and never trust this check alone.
func CheckSource(entries []TreeEntry, files []ExtractedFile) (SourceIntegrity, error) {
	tree, submodules, err := indexTree(entries)
	if err != nil {
		return SourceIntegrity{}, err
	}
	extracted, links, err := indexExtraction(files)
	if err != nil {
		return SourceIntegrity{}, err
	}
	if err := checkLinks(links); err != nil {
		return SourceIntegrity{}, err
	}
	report := SourceIntegrity{Submodules: submodules}
	for _, p := range slices.Sorted(maps.Keys(tree)) {
		if got, ok := extracted[p]; !ok {
			report.Missing = append(report.Missing, p)
		} else if got != tree[p] {
			report.Mismatched = append(report.Mismatched, p)
		}
	}
	for _, p := range slices.Sorted(maps.Keys(extracted)) {
		if _, ok := tree[p]; !ok {
			report.Extra = append(report.Extra, p)
		}
	}
	return report, nil
}

func validSHA1(s string) bool {
	decoded, err := hex.DecodeString(s)
	return err == nil && len(decoded) == sha1.Size && hex.EncodeToString(decoded) == s
}

// checkPath refuses an invalid path, and one already seen.
func checkPath(p string, seen map[string]bool) error {
	if !artifact.ValidPath(p) {
		return fmt.Errorf("%w: invalid path %q", ErrInvalidSource, p)
	}
	if seen[p] {
		return fmt.Errorf("%w: %q is listed twice", ErrInvalidSource, p)
	}
	seen[p] = true
	return nil
}

// refuseBelowFile fails with invalid when a path of paths sits below another path that is in files. A tree holds no such
// path, and an extraction that does would write below a link or through a file.
func refuseBelowFile(paths, files map[string]bool, invalid error) error {
	for _, p := range slices.Sorted(maps.Keys(paths)) {
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			if files[dir] {
				return fmt.Errorf("%w: %q is below the file %q", invalid, p, dir)
			}
		}
	}
	return nil
}

// indexTree validates the entries of a commit's tree. It returns its files by path, and the sorted paths of its submodules.
func indexTree(entries []TreeEntry) (map[string]blob, []string, error) {
	files := map[string]blob{}
	var submodules []string
	seen, notDirs := map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		if err := checkPath(e.Path, seen); err != nil {
			return nil, nil, err
		}
		if !validSHA1(e.BlobSHA1) {
			return nil, nil, fmt.Errorf("%w: %q has no valid SHA-1", ErrInvalidSource, e.Path)
		}
		notDirs[e.Path] = e.Mode != GitTree
		switch e.Mode {
		case GitTree:
			// A directory names no file.
		case GitSubmodule:
			submodules = append(submodules, e.Path)
		case GitRegular, GitExecutable, GitSymlink:
			files[e.Path] = blob{e.Mode, e.BlobSHA1}
		case gitLegacyRegular:
			files[e.Path] = blob{GitRegular, e.BlobSHA1}
		default:
			return nil, nil, fmt.Errorf("%w: %q has the unknown mode %q", ErrInvalidSource, e.Path, e.Mode)
		}
	}
	if err := refuseBelowFile(seen, notDirs, ErrInvalidSource); err != nil {
		return nil, nil, err
	}
	slices.Sort(submodules)
	return files, submodules, nil
}

// indexExtraction validates the extracted files. It returns them by path, and the targets of the symlinks by path.
func indexExtraction(files []ExtractedFile) (map[string]blob, map[string]string, error) {
	extracted := map[string]blob{}
	links := map[string]string{}
	seen := map[string]bool{}
	for _, f := range files {
		if err := checkPath(f.Path, seen); err != nil {
			return nil, nil, err
		}
		switch f.Mode {
		case GitRegular, GitExecutable:
			if !validSHA1(f.BlobSHA1) || f.Target != "" {
				return nil, nil, fmt.Errorf("%w: %q must carry a blob SHA-1 and no target", ErrInvalidSource, f.Path)
			}
			extracted[f.Path] = blob{f.Mode, f.BlobSHA1}
		case GitSymlink:
			if f.BlobSHA1 != "" {
				return nil, nil, fmt.Errorf("%w: the symlink %q must carry a target and no blob SHA-1", ErrInvalidSource, f.Path)
			}
			extracted[f.Path] = blob{GitSymlink, BlobSHA1([]byte(f.Target))}
			links[f.Path] = f.Target
		default:
			return nil, nil, fmt.Errorf("%w: %q has the mode %q, which no extracted file has", ErrInvalidSource, f.Path, f.Mode)
		}
	}
	if err := refuseBelowFile(seen, seen, ErrInvalidSource); err != nil {
		return nil, nil, err
	}
	return extracted, links, nil
}

// unsafeTarget reports a link target no tree may hold: empty, absolute, or spelled with a character that a path of the tree
// never has. A backslash or a colon (a drive letter, a stream name) steps out of the tree on Windows, where the executor can
// run, and NUL ends a path in C.
func unsafeTarget(target string) bool {
	return target == "" || strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\\:\x00")
}

// foldRune maps a rune to the smallest of its simple-folding orbit, so two runes have one image exactly when they are equal
// but for case.
func foldRune(r rune) rune {
	smallest := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		smallest = min(smallest, f)
	}
	return smallest
}

// foldPath is the key under which a filesystem that ignores case, and drops trailing dots and spaces from a name, sees a path.
func foldPath(p string) string {
	names := strings.Split(p, "/")
	for i, name := range names {
		names[i] = strings.Map(foldRune, strings.TrimRight(name, ". "))
	}
	return strings.Join(names, "/")
}

// checkLinks resolves every link of the extraction inside the tree. Links are indexed by the folded path of their names: a
// walk that meets a name follows the link that the name spells in any case, with trailing dots or spaces. Two links that
// fold to one name are refused, as no filesystem that ignores case can hold both.
func checkLinks(links map[string]string) error {
	byFold := map[string]string{}
	named := map[string]string{}
	for _, link := range slices.Sorted(maps.Keys(links)) {
		key := foldPath(link)
		if other, taken := named[key]; taken {
			return fmt.Errorf("%w: %q and %q are one name once case and trailing dots are ignored", ErrUnsafeSymlink, other, link)
		}
		named[key], byFold[key] = link, links[link]
	}
	for _, link := range slices.Sorted(maps.Keys(links)) {
		if err := resolveInside(link, links[link], byFold); err != nil {
			return err
		}
	}
	return nil
}

// resolveInside walks target from the directory of link one name at a time, following the links of the extraction (byFold,
// by folded path), as a filesystem does, and fails when the walk steps above the root of the tree, meets an unsafe target
// or a name that ends in a dot or a space, or follows more than maxLinkHops links. A lexical clean of the target would miss
// a ".." that comes after a link, which steps above the directory the link points to and not above the one that holds its name.
func resolveInside(link, target string, byFold map[string]string) error {
	unsafe := fmt.Errorf("%w: %q -> %q", ErrUnsafeSymlink, link, target)
	var here []string
	if dir := path.Dir(link); dir != "." {
		here = strings.Split(dir, "/")
	}
	if unsafeTarget(target) {
		return unsafe
	}
	todo := strings.Split(target, "/")
	for hops := 0; len(todo) > 0; {
		name := todo[0]
		todo = todo[1:]
		switch name {
		case "", ".":
		case "..":
			if len(here) == 0 {
				return unsafe
			}
			here = here[:len(here)-1]
		default:
			if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
				return unsafe // no target spells a name that way, and Windows reads it as another name
			}
			next := slices.Concat(here, []string{name})
			linked, isLink := byFold[foldPath(strings.Join(next, "/"))]
			if !isLink {
				here = next
				continue
			}
			if hops++; hops > maxLinkHops || unsafeTarget(linked) {
				return unsafe
			}
			todo = slices.Concat(strings.Split(linked, "/"), todo)
		}
	}
	return nil
}
