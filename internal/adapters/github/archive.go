package github

import (
	"context"
	"fmt"
	"io"
)

// DefaultArchiveLimit is the size, 512 MiB, past which a caller that has no limit of its own stops an archive.
const DefaultArchiveLimit int64 = 512 << 20

// Archive streams the tar.gz of the source of repo at commitSHA to dst and returns the bytes written, at most
// maxBytes: a longer archive stops the copy with rest.ErrTooLarge, and a short or broken one is an error, never
// a success. It is one budgeted request. dst may hold part of the archive after an error.
//
// The archive is what GitHub builds with git archive, not the commit's tree: .gitattributes can drop or rewrite
// files in it and a file stored in LFS is a pointer. A caller that needs the tree checks the files against Tree.
func (r *Reader) Archive(ctx context.Context, repo, commitSHA string, dst io.Writer, maxBytes int64) (int64, error) {
	if err := checkRepo(repo); err != nil {
		return 0, err
	}
	if !hexSHA.MatchString(commitSHA) {
		return 0, fmt.Errorf("%w: commit SHA %q is not 40 lowercase hex characters", ErrInvalidArgument, commitSHA)
	}
	n, err := r.client.Download(ctx, "/repos/"+repo+"/tarball/"+commitSHA, dst, maxBytes)
	if err != nil {
		return n, fmt.Errorf("archive of %s at %s: %w", repo, commitSHA, err)
	}
	return n, nil
}
