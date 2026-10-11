// Package github reads pull requests, their comments and pinned source trees
// from GitHub over the rest transport (docs/contracts/github.md, A3).
package github

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
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

const (
	// pageSize is per_page of every listing; a shorter page is the last one.
	pageSize = 100
	// maxPages bounds a listing at 10,000 items; a server that never ends a
	// listing is answering wrongly, not holding more.
	maxPages = 100
)

var (
	repoPart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	hexSHA   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

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
	if client == nil {
		return nil, errors.New("github reader: nil client")
	}
	return &Reader{client: client}, nil
}

// checkRepo accepts an owner/name repository whose parts can only name
// themselves in a URL path.
func checkRepo(repo string) error {
	owner, name, _ := strings.Cut(repo, "/")
	for _, part := range []string{owner, name} {
		if !repoPart.MatchString(part) || part == "." || part == ".." {
			return fmt.Errorf("%w: repository %q is not owner/name", ErrInvalidArgument, repo)
		}
	}
	return nil
}

type gitRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type pullJSON struct {
	Number    int       `json:"number"`
	Head      gitRef    `json:"head"`
	Base      gitRef    `json:"base"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OpenPulls returns the open pull requests of repo, sorted by number.
func (r *Reader) OpenPulls(ctx context.Context, repo string) ([]Pull, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	raw, err := readPages[pullJSON](ctx, r.client, func(page int) string {
		return fmt.Sprintf("/repos/%s/pulls?state=open&per_page=%d&page=%d", repo, pageSize, page)
	})
	if err != nil {
		return nil, fmt.Errorf("open pull requests of %s: %w", repo, err)
	}
	pulls := make([]Pull, 0, len(raw))
	for _, p := range latest(raw, func(p pullJSON) int { return p.Number }) {
		switch {
		case p.Number <= 0:
			return nil, fmt.Errorf("%w: pull request of %s without a number", ErrInvalidResponse, repo)
		case !hexSHA.MatchString(p.Head.SHA) || !hexSHA.MatchString(p.Base.SHA):
			return nil, fmt.Errorf("%w: pull request %d of %s lacks a head or base commit SHA", ErrInvalidResponse, p.Number, repo)
		case p.Head.Ref == "" || p.Base.Ref == "":
			return nil, fmt.Errorf("%w: pull request %d of %s lacks a head or base ref", ErrInvalidResponse, p.Number, repo)
		case p.UpdatedAt.IsZero():
			return nil, fmt.Errorf("%w: pull request %d of %s lacks updated_at", ErrInvalidResponse, p.Number, repo)
		}
		pulls = append(pulls, Pull{
			Number: p.Number, HeadSHA: p.Head.SHA, HeadRef: p.Head.Ref,
			BaseRef: p.Base.Ref, BaseSHA: p.Base.SHA, UpdatedAt: p.UpdatedAt,
		})
	}
	slices.SortFunc(pulls, func(a, b Pull) int { return cmp.Compare(a.Number, b.Number) })
	return pulls, nil
}

type commentJSON struct {
	ID   int64 `json:"id"`
	User struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	} `json:"user"` // a null or missing user decodes to zero values, rejected below
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PullComments returns the comments of pull request number of repo updated at
// or after since (every comment when since is zero), sorted by ID.
func (r *Reader) PullComments(ctx context.Context, repo string, number int, since time.Time) ([]Comment, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("%w: pull request number %d", ErrInvalidArgument, number)
	}
	var sinceParam string
	if !since.IsZero() {
		sinceParam = "&since=" + since.UTC().Format(time.RFC3339)
	}
	raw, err := readPages[commentJSON](ctx, r.client, func(page int) string {
		return fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=%d&page=%d%s", repo, number, pageSize, page, sinceParam)
	})
	if err != nil {
		return nil, fmt.Errorf("comments of pull request %d of %s: %w", number, repo, err)
	}
	comments := make([]Comment, 0, len(raw))
	for _, c := range latest(raw, func(c commentJSON) int64 { return c.ID }) {
		switch {
		case c.ID <= 0:
			return nil, fmt.Errorf("%w: comment of pull request %d of %s without an id", ErrInvalidResponse, number, repo)
		case c.User.Login == "" || c.User.ID <= 0:
			return nil, fmt.Errorf("%w: comment %d of %s lacks its author", ErrInvalidResponse, c.ID, repo)
		case c.CreatedAt.IsZero() || c.UpdatedAt.IsZero():
			return nil, fmt.Errorf("%w: comment %d of %s lacks created_at or updated_at", ErrInvalidResponse, c.ID, repo)
		}
		comments = append(comments, Comment{
			ID: c.ID, AuthorLogin: c.User.Login, AuthorID: c.User.ID, Body: c.Body,
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		})
	}
	slices.SortFunc(comments, func(a, b Comment) int { return cmp.Compare(a.ID, b.ID) })
	return comments, nil
}

// latest keeps one item per key, the last one listed: an item shifted onto
// the next page while listing appears twice, and the later read is newer.
func latest[T any, K comparable](items []T, key func(T) K) []T {
	byKey := make(map[K]T, len(items))
	for _, item := range items {
		byKey[key(item)] = item
	}
	return slices.Collect(maps.Values(byKey))
}

// readPages reads pages 1, 2, … of a listing until one holds fewer than
// pageSize items, or fails after maxPages; the result is never nil.
func readPages[T any](ctx context.Context, client *rest.Client, path func(page int) string) ([]T, error) {
	all := []T{}
	for page := 1; page <= maxPages; page++ {
		p := path(page)
		resp, err := client.Get(ctx, p)
		if err != nil {
			return nil, err
		}
		var items []T
		if err := decode(p, resp.Body, &items); err != nil {
			return nil, err
		}
		if items == nil { // null decodes to nil; an empty page is []
			return nil, fmt.Errorf("%w: the answer to %s is not a list", ErrInvalidResponse, p)
		}
		all = append(all, items...)
		if len(items) < pageSize {
			return all, nil
		}
	}
	return nil, fmt.Errorf("%w: the listing at %s runs past %d pages", ErrInvalidResponse, path(maxPages), maxPages)
}

// decode unmarshals body; its error names the path only, since the decoder's
// message can quote the body.
func decode(path string, body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%w: the answer to %s is not the expected JSON", ErrInvalidResponse, path)
	}
	return nil
}

type treeEntry struct {
	kind inventory.EntryKind
	sha  string
}

// entryKinds types each git mode and object type pair GitHub lists;
// directories (040000, tree) are implicit in an inventory.Tree.
var entryKinds = map[[2]string]inventory.EntryKind{
	{"100644", "blob"}:   inventory.File,
	{"100755", "blob"}:   inventory.File,
	{"120000", "blob"}:   inventory.Symlink,
	{"160000", "commit"}: inventory.Submodule,
}

// Tree returns the source tree of repo at commitSHA. Its entries are read at
// once; a file's content is fetched only when Read is called.
func (r *Reader) Tree(ctx context.Context, repo, commitSHA string) (inventory.Tree, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	if !hexSHA.MatchString(commitSHA) {
		return nil, fmt.Errorf("%w: commit SHA %q is not 40 lowercase hex characters", ErrInvalidArgument, commitSHA)
	}
	path := "/repos/" + repo + "/git/trees/" + commitSHA + "?recursive=1"
	resp, err := r.client.GetImmutable(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("tree of %s at %s: %w", repo, commitSHA, err)
	}
	var raw struct {
		Tree []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := decode(path, resp.Body, &raw); err != nil {
		return nil, err
	}
	if raw.Tree == nil { // a missing or null list decodes to nil; an empty tree is []
		return nil, fmt.Errorf("%w: the answer to %s lists no tree", ErrInvalidResponse, path)
	}
	if raw.Truncated {
		return nil, fmt.Errorf("%w: GitHub truncated the tree of %s at %s", inventory.ErrInvalidTree, repo, commitSHA)
	}
	t := &tree{client: r.client, repo: repo, byPath: make(map[string]treeEntry, len(raw.Tree))}
	for _, e := range raw.Tree {
		if e.Mode == "040000" && e.Type == "tree" {
			continue
		}
		kind, known := entryKinds[[2]string{e.Mode, e.Type}]
		switch {
		case !known:
			return nil, fmt.Errorf("%w: %q has mode %q and type %q", inventory.ErrInvalidTree, e.Path, e.Mode, e.Type)
		case !hexSHA.MatchString(e.SHA):
			return nil, fmt.Errorf("%w: %q has an invalid object SHA", inventory.ErrInvalidTree, e.Path)
		}
		t.entries = append(t.entries, inventory.Entry{Path: e.Path, Kind: kind})
		t.byPath[e.Path] = treeEntry{kind: kind, sha: e.SHA}
	}
	return t, nil
}

// tree is an inventory.Tree over one listed git tree. Duplicate paths are
// listed as GitHub sent them and rejected by inventory.
type tree struct {
	client  *rest.Client
	repo    string
	entries []inventory.Entry
	byPath  map[string]treeEntry
}

func (t *tree) Entries(context.Context) ([]inventory.Entry, error) {
	return slices.Clone(t.entries), nil
}

// Read fetches and decodes the blob of a listed regular file.
func (t *tree) Read(ctx context.Context, path string) ([]byte, error) {
	entry, listed := t.byPath[path]
	switch {
	case !listed:
		return nil, fmt.Errorf("github tree: %q is not listed", path)
	case entry.kind != inventory.File:
		return nil, fmt.Errorf("github tree: %q is not a regular file", path)
	}
	blobPath := "/repos/" + t.repo + "/git/blobs/" + entry.sha
	resp, err := t.client.GetImmutable(ctx, blobPath)
	if err != nil {
		return nil, fmt.Errorf("blob of %q: %w", path, err)
	}
	var blob struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := decode(blobPath, resp.Body, &blob); err != nil {
		return nil, err
	}
	if blob.Encoding != "base64" {
		return nil, fmt.Errorf("%w: blob of %q has encoding %q", ErrInvalidResponse, path, blob.Encoding)
	}
	content, err := base64.StdEncoding.DecodeString(blob.Content) // the decoder skips GitHub's line breaks
	if err != nil {
		return nil, fmt.Errorf("%w: blob of %q is not valid base64: %v", ErrInvalidResponse, path, err)
	}
	return content, nil
}
