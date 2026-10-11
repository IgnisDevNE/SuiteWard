// Package github reads pull requests, their comments and pinned source trees
// from GitHub over the rest transport (docs/contracts/github.md, A3).
package github

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
)

var (
	// ErrInvalidArgument reports a repository, pull request number or commit
	// SHA that is malformed; no request is sent.
	ErrInvalidArgument = errors.New("github: invalid argument")
	// ErrInvalidResponse reports an answer that does not decode or lacks a
	// required field.
	ErrInvalidResponse = errors.New("github: invalid response")
)

var errNotImplemented = errors.New("github: not implemented")

// Pull is one open pull request.
type Pull struct {
	Number           int
	HeadSHA, HeadRef string
	BaseRef, BaseSHA string
	UpdatedAt        time.Time
}

// Comment is one conversation comment of a pull request.
type Comment struct {
	ID                   int64
	AuthorLogin          string
	AuthorID             int64
	Body                 string
	CreatedAt, UpdatedAt time.Time
}

// Reader performs the typed reads of one GitHub installation.
type Reader struct {
	client *rest.Client
}

// NewReader returns a reader over client.
func NewReader(client *rest.Client) (*Reader, error) {
	return &Reader{client: client}, nil
}

// OpenPulls returns the open pull requests of repo, sorted by number.
func (r *Reader) OpenPulls(ctx context.Context, repo string) ([]Pull, error) {
	return nil, errNotImplemented
}

// PullComments returns the comments of pull request number of repo updated at
// or after since (every comment when since is zero), sorted by ID.
func (r *Reader) PullComments(ctx context.Context, repo string, number int, since time.Time) ([]Comment, error) {
	return nil, errNotImplemented
}

// Tree returns the source tree of repo at commitSHA.
func (r *Reader) Tree(ctx context.Context, repo, commitSHA string) (inventory.Tree, error) {
	return nil, errNotImplemented
}
