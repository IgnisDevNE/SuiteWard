package execution

import "errors"

var (
	// ErrInvalidSource reports a commit tree or an extraction the check cannot read: a malformed path, mode or blob
	// SHA-1, a path listed twice, or an extracted file whose fields do not fit its mode.
	ErrInvalidSource = errors.New("invalid source tree")
	// ErrUnsafeSymlink reports a symlink whose target leaves the tree, or does not resolve inside it.
	ErrUnsafeSymlink = errors.New("symlink target leaves the tree")
)

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
func BlobSHA1(content []byte) string { return "" }

// SourceIntegrity is how an extraction differs from its commit's tree. Each list is sorted.
type SourceIntegrity struct {
	Missing    []string // in the tree, not extracted
	Extra      []string // extracted, not in the tree
	Mismatched []string // in both, with another mode or another blob
	Submodules []string // skipped: a submodule is outside the commit's own files, and its content is never read
}

// Intact reports whether the extraction is the commit's tree: nothing missing, extra or mismatched. Skipped submodules
// do not break it.
func (SourceIntegrity) Intact() bool { return false }

// CheckSource compares the files of an extracted archive with the entries of the commit's tree. Git archives can drop
// files (export-ignore), rewrite them (export-subst) and replace LFS content with pointers, so an archive is not the
// tree; any difference makes the run unavailable. A symlink must resolve inside the tree, following the links of the
// extraction and at most eight hops, or the check fails with ErrUnsafeSymlink.
func CheckSource(entries []TreeEntry, files []ExtractedFile) (SourceIntegrity, error) {
	return SourceIntegrity{}, errNotImplemented
}
