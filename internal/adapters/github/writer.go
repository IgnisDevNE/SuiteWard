package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

// Writer posts conversation comments. Its client's token map is
// {metadata: read, pull_requests: write}; the composition supplies it.
type Writer struct {
	client *rest.Client
}

// NewWriter returns a writer over client.
func NewWriter(client *rest.Client) (*Writer, error) {
	if client == nil {
		return nil, errors.New("github writer: nil client")
	}
	return &Writer{client: client}, nil
}

// PostComment posts body as a comment on pull request number of repo and
// returns the new comment's id.
func (w *Writer) PostComment(ctx context.Context, repo string, number int, body string) (int64, error) {
	if err := checkRepo(repo); err != nil {
		return 0, err
	}
	if number <= 0 {
		return 0, fmt.Errorf("%w: pull request number %d", ErrInvalidArgument, number)
	}
	if body == "" {
		return 0, fmt.Errorf("%w: empty comment body", ErrInvalidArgument)
	}
	payload, err := json.Marshal(struct {
		Body string `json:"body"`
	}{body})
	if err != nil {
		return 0, fmt.Errorf("encode comment of pull request %d of %s: %w", number, repo, err)
	}
	path := fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number)
	resp, err := w.client.Post(ctx, path, payload)
	if err != nil {
		return 0, fmt.Errorf("post comment on pull request %d of %s: %w", number, repo, err)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := decode(path, resp.Body, &created); err != nil {
		return 0, err
	}
	if created.ID <= 0 {
		return 0, fmt.Errorf("%w: the answer to %s has no comment id", ErrInvalidResponse, path)
	}
	return created.ID, nil
}

// CommentsByAuthor returns the comments of pull request number of repo written
// by the account authorID, sorted by ID. The publisher looks for its marker in
// their bodies before posting.
func (w *Writer) CommentsByAuthor(ctx context.Context, repo string, number int, authorID int64) ([]Comment, error) {
	if authorID <= 0 {
		return nil, fmt.Errorf("%w: author id %d", ErrInvalidArgument, authorID)
	}
	all, err := pullComments(ctx, w.client, repo, number, time.Time{})
	if err != nil {
		return nil, err
	}
	mine := []Comment{}
	for _, c := range all {
		if c.AuthorID == authorID {
			mine = append(mine, c)
		}
	}
	return mine, nil
}

// Bot is the GitHub App's bot account.
type Bot struct {
	Login string
	ID    int64
}

// BotIdentity resolves the bot account of app: GET /app with the App JWT, over
// httpClient and baseURL, gives the slug; GET /users/<slug>[bot] with the
// reader's token gives the numeric id. The JWT call is not part of the
// installation request budget (GitHub limits App JWT calls separately).
func (r *Reader) BotIdentity(ctx context.Context, app appauth.App, httpClient *http.Client, baseURL string) (Bot, error) {
	slug, err := app.Slug(ctx, httpClient, baseURL)
	if err != nil {
		return Bot{}, fmt.Errorf("bot identity: %w", err)
	}
	login := slug + "[bot]"
	path := "/users/" + url.PathEscape(login)
	resp, err := r.client.Get(ctx, path)
	if err != nil {
		return Bot{}, fmt.Errorf("bot identity: %w", err)
	}
	var user struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
		Type  string `json:"type"`
	}
	if err := decode(path, resp.Body, &user); err != nil {
		return Bot{}, err
	}
	if user.Type != "Bot" || user.Login != login || user.ID <= 0 {
		return Bot{}, fmt.Errorf("%w: %s is not the bot account %s", ErrInvalidResponse, path, login)
	}
	return Bot{Login: user.Login, ID: user.ID}, nil
}
