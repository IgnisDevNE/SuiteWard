package run

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/source"
)

// archives opens the commit archives of a lease for the source package, and wraps what the service answers in the sentinel that says why
// a stream could not be had: a 502 is GitHub's failure and consumes no attempt, a 413 is an archive over its limit, and anything else that
// is not the lease is an archive that cannot be read. A lost lease passes through as it is, for classify to test first.
type archives struct{ lease *client.Lease }

func (a archives) Open(ctx context.Context, side source.Side) (io.ReadCloser, error) {
	rc, err := a.lease.Archive(ctx, string(side))
	switch {
	case err == nil:
		return rc, nil
	case errors.Is(err, client.ErrLeaseLost):
		return nil, err
	case errors.Is(err, client.ErrGitHub):
		return nil, fmt.Errorf("the %s archive: %w: %w", side, source.ErrGitHubUnavailable, err)
	case errors.Is(err, client.ErrTooLarge):
		return nil, fmt.Errorf("the %s archive: %w: %w", side, source.ErrTooLarge, err)
	default:
		return nil, fmt.Errorf("the %s archive: %w: %w", side, source.ErrArchiveUnreadable, err)
	}
}

// manifestFiles opens the files of the manifest of a lease. The source package passes an opener's error on as it comes, so the 404 and
// the 500 of the service are wrapped here as a file that cannot be read.
type manifestFiles struct{ lease *client.Lease }

func (m manifestFiles) Open(ctx context.Context, d artifact.Digest) (io.ReadCloser, error) {
	rc, err := m.lease.File(ctx, d.String())
	switch {
	case err == nil:
		return rc, nil
	case errors.Is(err, client.ErrLeaseLost):
		return nil, err
	default:
		return nil, fmt.Errorf("the manifest file %s: %w: %w", d, source.ErrArchiveUnreadable, err)
	}
}
