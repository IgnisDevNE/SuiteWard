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
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"

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

// budget is what the tree and the module files may take together: half of the tmpfs, because the other half is left to the Go
// build cache (ADR 0028 section 11).
func (l Limits) budget() int64 { return int64(l.TmpfsMiB) << 20 / 2 }

// Inspect streams a commit archive, a tar.gz in GitHub's layout, and checks it against the entries of the commit's tree with the
// rules of execution.CheckSource: a missing, extra or mismatched file, an unsafe symlink and a name that aliases another are
// ErrSourceMismatch. The blob SHA-1 of each file is computed from the size of its tar header while the file streams, with the
// SHA-256 that the tree functions of the domain take, so no whole entry is held in memory.
//
// A stream that passes l.ArchiveBytes, a file over l.FileBytes and a tree over half of l.TmpfsMiB are ErrTooLarge; a stream that
// breaks short of its limit is ErrGitHubUnavailable and bytes that are no tar.gz are ErrArchiveUnreadable.
func Inspect(ctx context.Context, archive io.Reader, entries []execution.TreeEntry, l Limits) (Inspection, error) {
	in := &inspector{limits: l, most: len(entries)}
	if err := walk(ctx, archive, l, in.visit); err != nil {
		return Inspection{}, err
	}
	integrity, err := execution.CheckSource(entries, in.files)
	if err != nil {
		return Inspection{}, fmt.Errorf("%w: %w", ErrSourceMismatch, err)
	}
	if !integrity.Intact() {
		return Inspection{}, mismatch("%s", describe(integrity))
	}
	manifest, err := artifact.NewManifest(in.manifest)
	if err != nil {
		return Inspection{}, fmt.Errorf("build the manifest of the archive: %w", err)
	}
	return Inspection{Files: manifest, Submodules: integrity.Submodules, Bytes: in.bytes}, nil
}

type inspector struct {
	limits   Limits
	most     int // the entries of the tree: an archive cannot hold more files than that and match it
	files    []execution.ExtractedFile
	manifest []artifact.Entry
	bytes    int64
}

func (in *inspector) visit(e entry) error {
	if len(in.files) >= in.most {
		return mismatch("the archive holds more files than the tree, beginning with %q", e.path)
	}
	if in.bytes+e.size > in.limits.budget() {
		return fmt.Errorf("%w: the tree passes half of the %d MiB tmpfs at %q", ErrTooLarge, in.limits.TmpfsMiB, e.path)
	}
	file, content, err := inspect(e, in.limits.FileBytes)
	if err != nil {
		return err
	}
	in.files = append(in.files, file)
	in.manifest = append(in.manifest, artifact.Entry{Path: e.path, Content: content})
	in.bytes += e.size
	return nil
}

// inspect reads a file as it streams and returns what CheckSource takes of it and the SHA-256 of its bytes.
func inspect(e entry, fileBytes int64) (execution.ExtractedFile, artifact.Digest, error) {
	if e.kind == symbolic {
		return execution.ExtractedFile{Path: e.path, Mode: execution.GitSymlink, Target: e.target}, artifact.Hash([]byte(e.target)), nil
	}
	if e.size > fileBytes {
		return execution.ExtractedFile{}, artifact.Digest{}, fmt.Errorf("%w: %q is %d bytes, over the cap of %d", ErrTooLarge, e.path, e.size, fileBytes)
	}
	blob, content, err := digests(io.Discard, e)
	if err != nil {
		return execution.ExtractedFile{}, artifact.Digest{}, err
	}
	mode := execution.GitRegular
	if e.exec {
		mode = execution.GitExecutable
	}
	return execution.ExtractedFile{Path: e.path, Mode: mode, BlobSHA1: blob}, content, nil
}

// digests copies a file to w while it computes the git blob SHA-1, which hashes "blob <size>" and a NUL before the bytes (the size
// is that of the tar header, known before the first byte), and the SHA-256 of the bytes.
func digests(w io.Writer, e entry) (blob string, content artifact.Digest, err error) {
	blobHash, contentHash := sha1.New(), sha256.New()
	blobHash.Write([]byte("blob " + strconv.FormatInt(e.size, 10) + "\x00"))
	if _, err := io.Copy(io.MultiWriter(w, blobHash, contentHash), e.body); err != nil {
		return "", artifact.Digest{}, err
	}
	content, err = artifact.ParseDigest("sha256:" + hex.EncodeToString(contentHash.Sum(nil)))
	return hex.EncodeToString(blobHash.Sum(nil)), content, err
}

// describe says how an extraction differs from its tree, with a few paths of each kind.
func describe(i execution.SourceIntegrity) string {
	return fmt.Sprintf("%d files missing%s, %d extra%s, %d mismatched%s",
		len(i.Missing), sample(i.Missing), len(i.Extra), sample(i.Extra), len(i.Mismatched), sample(i.Mismatched))
}

func sample(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	if len(paths) > 3 {
		paths = paths[:3]
	}
	return fmt.Sprintf(" (%q...)", paths)
}
