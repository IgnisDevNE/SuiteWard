package github

import (
	"context"
	"errors"
	"net/http"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

var errNotImplemented = errors.New("github: not implemented")

// Writer posts comments with a client whose token map is {metadata: read, pull_requests: write}.
type Writer struct{ client *rest.Client }

// NewWriter returns a writer over client.
func NewWriter(client *rest.Client) (*Writer, error) { return &Writer{client: client}, nil }

// PostComment posts a conversation comment and returns its id.
func (w *Writer) PostComment(ctx context.Context, repo string, number int, body string) (int64, error) {
	return 0, errNotImplemented
}

// CommentsByAuthor lists the comments of authorID on pull request number.
func (w *Writer) CommentsByAuthor(ctx context.Context, repo string, number int, authorID int64) ([]Comment, error) {
	return nil, errNotImplemented
}

// Bot is the GitHub App's bot account.
type Bot struct {
	Login string
	ID    int64
}

// BotIdentity resolves the bot account of app.
func (r *Reader) BotIdentity(ctx context.Context, app appauth.App, httpClient *http.Client, baseURL string) (Bot, error) {
	return Bot{}, errNotImplemented
}

// BranchHead returns the head commit SHA of branch.
func (r *Reader) BranchHead(ctx context.Context, repo, branch string) (string, error) {
	return "", errNotImplemented
}
