package github

import (
	"context"
	"errors"
	"io"
)

// DefaultArchiveLimit is a stub that the archive tests are written against.
const DefaultArchiveLimit int64 = 0

// Archive is a stub that the archive tests are written against.
func (r *Reader) Archive(ctx context.Context, repo, commitSHA string, dst io.Writer, maxBytes int64) (int64, error) {
	return 0, errors.New("not implemented")
}
