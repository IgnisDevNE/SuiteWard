// Package source checks a commit archive against the commit's tree and composes the tar that the sandbox reads on its standard
// input (ADR 0028 section 3). Every byte is verified while it streams: the blob SHA-1 of a file is computed from the size in the
// tar header while the file is copied, so no whole entry is held in memory and nothing of the tree is written to a host
// filesystem, where Windows or macOS would alias names.
//
// The errors are the shared vocabulary of the failure reasons of the runtime contract. An opener that cannot supply a stream wraps
// the one that says why (a 502 of the service is ErrGitHubUnavailable), and the package passes it on.
package source

import (
	"context"
	"errors"
	"io"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

var (
	// ErrSourceMismatch reports a source that is not the commit's tree: a missing, extra or mismatched file, an unsafe symlink, a
	// name that aliases another, or an archive that is not shaped like GitHub's (reason source-mismatch).
	ErrSourceMismatch = errors.New("the source does not match the commit's tree")
	// ErrTooLarge reports an archive stream that passed its limit, a file over its cap, or a tree that passed half the tmpfs (reason too-large).
	ErrTooLarge = errors.New("the source passes its size limit")
	// ErrGitHubUnavailable reports an archive stream that broke short of its limit: the service could not read GitHub to the end
	// (reason github-unavailable, which consumes no attempt).
	ErrGitHubUnavailable = errors.New("the archive stream broke before its end")
	// ErrArchiveUnreadable reports an archive that is not a readable tar.gz, or a manifest file that is not the bytes of its digest
	// (reason archive-unavailable).
	ErrArchiveUnreadable = errors.New("the archive or the manifest file cannot be read")
	// ErrInvalidPlan reports a composition that cannot be built from what the caller gave: a file with a source the tree has no
	// entries for, a path that is listed twice, or a file that conflicts with another. It is the caller's mistake.
	ErrInvalidPlan = errors.New("invalid composition")
)

// Side is a side of a plan: the head commit, or for a receipt the base commit.
type Side string

// The sides.
const (
	SideHead Side = "head"
	SideBase Side = "base"
)

// Limits are the limits of a job that bear on the source. Every one is required: a limit of zero refuses everything.
type Limits struct {
	ArchiveBytes int64 // the compressed bytes of one archive stream
	FileBytes    int64 // one file, and one manifest file
	TmpfsMiB     int   // the size of the sandbox's /work; the tree and the module files may take half of it
}

// Inspection is what a checked archive says of its tree.
type Inspection struct {
	Files      artifact.Manifest // every file and symlink, with the SHA-256 of its bytes (of its target, for a link)
	Submodules []string          // skipped
	Bytes      int64             // the uncompressed size of the files
}

// ArchiveOpener opens the tar.gz of a side's commit. The caller closes it. It may be opened again: the same commit gives the same
// archive.
type ArchiveOpener interface {
	Open(ctx context.Context, side Side) (io.ReadCloser, error)
}

// FileOpener opens one file of the governing manifest by its digest. The caller closes it.
type FileOpener interface {
	Open(ctx context.Context, d artifact.Digest) (io.ReadCloser, error)
}

var errNotImplemented = errors.New("not implemented")

// Inspect streams a commit archive and checks it against the commit's tree.
func Inspect(ctx context.Context, archive io.Reader, entries []execution.TreeEntry, l Limits) (Inspection, error) {
	return Inspection{}, errNotImplemented
}

// Compose writes to w the uncompressed tar of exactly the files of tree.
func Compose(ctx context.Context, w io.Writer, tree []execution.TreeFile, archives ArchiveOpener,
	entries map[Side][]execution.TreeEntry, files FileOpener, l Limits) (*Appender, error) {
	return nil, errNotImplemented
}

// Appender adds extra regular files to the tar that Compose started.
type Appender struct{}

// Add writes one file of size bytes, read from r.
func (a *Appender) Add(name string, size int64, r io.Reader) error { return errNotImplemented }

// Close ends the tar.
func (a *Appender) Close() error { return errNotImplemented }
