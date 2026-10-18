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
	"net/url"
	"regexp"
	"slices"
	"strconv"
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
	// ErrTreeTruncated reports a recursive tree GitHub cut at its size limit;
	// the error also matches inventory.ErrInvalidTree.
	ErrTreeTruncated = errors.New("github: tree truncated")
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

// deletedHeadRepo is HeadRepo when GitHub answers head.repo: null (a deleted
// fork); a full_name always holds a "/", so it cannot collide.
const deletedHeadRepo = "(deleted)"

// Pull is one pull request. HeadRepo is the full_name of the head repository,
// or "(deleted)": a PR is a fork PR when it differs from the repository read
// (case-insensitively).
type Pull struct {
	Number           int
	HeadSHA, HeadRef string
	HeadRepo         string
	BaseRef, BaseSHA string
	UpdatedAt        time.Time
}

// PullDetail is a Pull with the facts that tell a merged PR from a closed one.
// MergedAt and MergeCommitSHA are empty unless Merged; a merged PR's
// MergeCommitSHA may still be empty right after the merge.
type PullDetail struct {
	Pull
	State          string // "open" or "closed"
	Merged         bool
	MergedAt       time.Time
	MergeCommitSHA string
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
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo *struct {
		FullName string `json:"full_name"`
	} `json:"repo"` // null when the repository was deleted
}

type pullJSON struct {
	Number    int       `json:"number"`
	Head      gitRef    `json:"head"`
	Base      gitRef    `json:"base"`
	UpdatedAt time.Time `json:"updated_at"`
}

// pull validates p as a pull request of repo.
func (p pullJSON) pull(repo string) (Pull, error) {
	switch {
	case p.Number <= 0:
		return Pull{}, fmt.Errorf("%w: pull request of %s without a number", ErrInvalidResponse, repo)
	case !hexSHA.MatchString(p.Head.SHA) || !hexSHA.MatchString(p.Base.SHA):
		return Pull{}, fmt.Errorf("%w: pull request %d of %s lacks a head or base commit SHA", ErrInvalidResponse, p.Number, repo)
	case p.Head.Ref == "" || p.Base.Ref == "":
		return Pull{}, fmt.Errorf("%w: pull request %d of %s lacks a head or base ref", ErrInvalidResponse, p.Number, repo)
	case p.UpdatedAt.IsZero():
		return Pull{}, fmt.Errorf("%w: pull request %d of %s lacks updated_at", ErrInvalidResponse, p.Number, repo)
	}
	headRepo := deletedHeadRepo
	if p.Head.Repo != nil {
		headRepo = p.Head.Repo.FullName
		if !strings.Contains(headRepo, "/") {
			return Pull{}, fmt.Errorf("%w: pull request %d of %s has a head repository that is not owner/name", ErrInvalidResponse, p.Number, repo)
		}
	}
	return Pull{
		Number: p.Number, HeadSHA: p.Head.SHA, HeadRef: p.Head.Ref, HeadRepo: headRepo,
		BaseRef: p.Base.Ref, BaseSHA: p.Base.SHA, UpdatedAt: p.UpdatedAt,
	}, nil
}

type pullDetailJSON struct {
	pullJSON
	State          string     `json:"state"`
	Merged         *bool      `json:"merged"` // absent from listings
	MergedAt       *time.Time `json:"merged_at"`
	MergeCommitSHA *string    `json:"merge_commit_sha"`
}

// detail validates p as a pull request of repo. merged is GitHub's merge
// signal, which must agree with merged_at; merge_commit_sha alone never means
// merged, since GitHub sets it on open pull requests too.
func (p pullDetailJSON) detail(repo string, merged bool) (PullDetail, error) {
	pull, err := p.pull(repo)
	if err != nil {
		return PullDetail{}, err
	}
	switch {
	case p.State != "open" && p.State != "closed":
		return PullDetail{}, fmt.Errorf("%w: pull request %d of %s has an unknown state", ErrInvalidResponse, p.Number, repo)
	case p.MergedAt != nil && p.MergedAt.IsZero():
		return PullDetail{}, fmt.Errorf("%w: pull request %d of %s has a zero merged_at", ErrInvalidResponse, p.Number, repo)
	case merged != (p.MergedAt != nil):
		return PullDetail{}, fmt.Errorf("%w: pull request %d of %s disagrees between merged and merged_at", ErrInvalidResponse, p.Number, repo)
	case merged && p.State != "closed":
		return PullDetail{}, fmt.Errorf("%w: merged pull request %d of %s is not closed", ErrInvalidResponse, p.Number, repo)
	}
	detail := PullDetail{Pull: pull, State: p.State, Merged: merged}
	if !merged {
		return detail, nil
	}
	detail.MergedAt = *p.MergedAt
	if p.MergeCommitSHA != nil && *p.MergeCommitSHA != "" {
		if !hexSHA.MatchString(*p.MergeCommitSHA) {
			return PullDetail{}, fmt.Errorf("%w: merged pull request %d of %s has an invalid merge commit SHA", ErrInvalidResponse, p.Number, repo)
		}
		detail.MergeCommitSHA = *p.MergeCommitSHA
	}
	return detail, nil
}

// Pull returns pull request number of repo. It is read without the ETag
// cache: the answer changes, and a closed pull is read once per resolution.
func (r *Reader) Pull(ctx context.Context, repo string, number int) (PullDetail, error) {
	if err := checkRepo(repo); err != nil {
		return PullDetail{}, err
	}
	if number <= 0 {
		return PullDetail{}, fmt.Errorf("%w: pull request number %d", ErrInvalidArgument, number)
	}
	path := "/repos/" + repo + "/pulls/" + strconv.Itoa(number)
	resp, err := r.client.GetImmutable(ctx, path)
	if err != nil {
		return PullDetail{}, fmt.Errorf("pull request %d of %s: %w", number, repo, err)
	}
	var raw pullDetailJSON
	if err := decode(path, resp.Body, &raw); err != nil {
		return PullDetail{}, err
	}
	if raw.Number != number {
		return PullDetail{}, fmt.Errorf("%w: the answer to %s is another pull request", ErrInvalidResponse, path)
	}
	if raw.Merged == nil { // a trust boundary: GitHub always sends merged on one pull
		return PullDetail{}, fmt.Errorf("%w: the answer to %s lacks merged", ErrInvalidResponse, path)
	}
	return raw.detail(repo, *raw.Merged)
}

// ClosedPulls returns one page, the 30 most recently updated, of the closed
// pull requests of repo, in GitHub's order. A burst of more than 30 closed
// between two reads can still miss one. merged is derived from merged_at, as
// the listing has no merged field.
func (r *Reader) ClosedPulls(ctx context.Context, repo string) ([]PullDetail, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	path := "/repos/" + repo + "/pulls?state=closed&sort=updated&direction=desc&per_page=30"
	resp, err := r.client.Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("closed pull requests of %s: %w", repo, err)
	}
	var raw []pullDetailJSON
	if err := decode(path, resp.Body, &raw); err != nil {
		return nil, err
	}
	if raw == nil { // null decodes to nil; an empty page is []
		return nil, fmt.Errorf("%w: the answer to %s is not a list", ErrInvalidResponse, path)
	}
	pulls := make([]PullDetail, 0, len(raw))
	for _, p := range raw {
		if p.State != "closed" {
			return nil, fmt.Errorf("%w: pull request %d in the closed listing of %s is not closed", ErrInvalidResponse, p.Number, repo)
		}
		detail, err := p.detail(repo, p.MergedAt != nil)
		if err != nil {
			return nil, err
		}
		pulls = append(pulls, detail)
	}
	return pulls, nil
}

// ReadInfo describes what one listing cost and saw: the Date of its first page, the requests it sent and whether
// any page was not a 304. A failed listing still reports the requests it sent.
type ReadInfo struct {
	Date     time.Time
	Requests int
	Changed  bool
}

// OpenPulls returns the open pull requests of repo, sorted by number, and what the listing read.
func (r *Reader) OpenPulls(ctx context.Context, repo string) ([]Pull, ReadInfo, error) {
	if err := checkRepo(repo); err != nil {
		return nil, ReadInfo{}, err
	}
	raw, info, err := readDecoded[pullJSON](ctx, r.client, func(page int) string {
		return fmt.Sprintf("/repos/%s/pulls?state=open&per_page=%d&page=%d", repo, pageSize, page)
	})
	if err != nil {
		return nil, info, fmt.Errorf("open pull requests of %s: %w", repo, err)
	}
	pulls := make([]Pull, 0, len(raw))
	for _, p := range latest(raw, func(p pullJSON) int { return p.Number }) {
		pull, err := p.pull(repo)
		if err != nil {
			return nil, info, err
		}
		pulls = append(pulls, pull)
	}
	slices.SortFunc(pulls, func(a, b Pull) int { return cmp.Compare(a.Number, b.Number) })
	return pulls, info, nil
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
// or after since (every comment when since is zero), sorted by ID, and what the listing read.
func (r *Reader) PullComments(ctx context.Context, repo string, number int, since time.Time) ([]Comment, ReadInfo, error) {
	return pullComments(ctx, r.client, repo, number, "issues", since)
}

// pullComments lists the comments of collection "issues" (the conversation) or "pulls" (the review comments).
func pullComments(ctx context.Context, client *rest.Client, repo string, number int, collection string, since time.Time) ([]Comment, ReadInfo, error) {
	if err := checkRepo(repo); err != nil {
		return nil, ReadInfo{}, err
	}
	if number <= 0 {
		return nil, ReadInfo{}, fmt.Errorf("%w: pull request number %d", ErrInvalidArgument, number)
	}
	var sinceParam string
	if !since.IsZero() {
		sinceParam = "&since=" + since.UTC().Format(time.RFC3339)
	}
	raw, info, err := readDecoded[commentJSON](ctx, client, func(page int) string {
		return fmt.Sprintf("/repos/%s/%s/%d/comments?per_page=%d&page=%d%s", repo, collection, number, pageSize, page, sinceParam)
	})
	if err != nil {
		return nil, info, fmt.Errorf("comments of pull request %d of %s: %w", number, repo, err)
	}
	comments := make([]Comment, 0, len(raw))
	for _, c := range latest(raw, func(c commentJSON) int64 { return c.ID }) {
		switch {
		case c.ID <= 0:
			return nil, info, fmt.Errorf("%w: comment of pull request %d of %s without an id", ErrInvalidResponse, number, repo)
		case c.User.Login == "" || c.User.ID <= 0:
			return nil, info, fmt.Errorf("%w: comment %d of %s lacks its author", ErrInvalidResponse, c.ID, repo)
		case c.CreatedAt.IsZero() || c.UpdatedAt.IsZero():
			return nil, info, fmt.Errorf("%w: comment %d of %s lacks created_at or updated_at", ErrInvalidResponse, c.ID, repo)
		}
		comments = append(comments, Comment{
			ID: c.ID, AuthorLogin: c.User.Login, AuthorID: c.User.ID, Body: c.Body,
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		})
	}
	slices.SortFunc(comments, func(a, b Comment) int { return cmp.Compare(a.ID, b.ID) })
	return comments, info, nil
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
	all, _, err := readDecoded[T](ctx, client, path)
	return all, err
}

// readDecoded is readPages that also reports what the listing read.
func readDecoded[T any](ctx context.Context, client *rest.Client, path func(page int) string) ([]T, ReadInfo, error) {
	return readListing(ctx, client, path, func(p string, body []byte) ([]T, error) {
		var items []T
		if err := decode(p, body, &items); err != nil {
			return nil, err
		}
		return items, nil
	})
}

// readPagesOf is readPages for a listing whose items sit inside the answer;
// items returns nil for an answer that is not a list.
func readPagesOf[T any](ctx context.Context, client *rest.Client, path func(page int) string, items func(path string, body []byte) ([]T, error)) ([]T, error) {
	all, _, err := readListing(ctx, client, path, items)
	return all, err
}

// readListing is readPagesOf that also reports what the listing read.
func readListing[T any](ctx context.Context, client *rest.Client, path func(page int) string, items func(path string, body []byte) ([]T, error)) ([]T, ReadInfo, error) {
	all := []T{}
	var info ReadInfo
	for page := 1; page <= maxPages; page++ {
		p := path(page)
		resp, err := client.Get(ctx, p)
		if !errors.Is(err, rest.ErrBudgetExhausted) && !errors.Is(err, rest.ErrNotSent) { // a refused request, or one with no token, was never sent
			info.Requests++
		}
		if err != nil {
			return nil, info, err
		}
		if page == 1 {
			info.Date = resp.Date
		}
		info.Changed = info.Changed || !resp.NotModified
		got, err := items(p, resp.Body)
		if err != nil {
			return nil, info, err
		}
		if got == nil { // null decodes to nil; an empty page is []
			return nil, info, fmt.Errorf("%w: the answer to %s is not a list", ErrInvalidResponse, p)
		}
		all = append(all, got...)
		if len(got) < pageSize {
			return all, info, nil
		}
	}
	return nil, info, fmt.Errorf("%w: the listing at %s runs past %d pages", ErrInvalidResponse, path(maxPages), maxPages)
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
		return nil, fmt.Errorf("%w: %w: GitHub truncated the tree of %s at %s", inventory.ErrInvalidTree, ErrTreeTruncated, repo, commitSHA)
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

// BranchHead returns the SHA of the commit branch of repo points at.
func (r *Reader) BranchHead(ctx context.Context, repo, branch string) (string, error) {
	if err := checkRepo(repo); err != nil {
		return "", err
	}
	if branch == "" {
		return "", fmt.Errorf("%w: empty branch name", ErrInvalidArgument)
	}
	path := "/repos/" + repo + "/branches/" + url.PathEscape(branch)
	resp, err := r.client.Get(ctx, path)
	if err != nil {
		return "", fmt.Errorf("head of branch %q of %s: %w", branch, repo, err)
	}
	var raw struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := decode(path, resp.Body, &raw); err != nil {
		return "", err
	}
	if !hexSHA.MatchString(raw.Commit.SHA) {
		return "", fmt.Errorf("%w: the answer to %s lacks a 40-hex commit SHA", ErrInvalidResponse, path)
	}
	return raw.Commit.SHA, nil
}

// PullFile is one file a pull request changes. Patch is GitHub's unified diff
// of the file, empty for a binary or too large one.
type PullFile struct {
	Path, Status, Patch string
}

// PullFiles returns the files pull request number of repo changes, in the order
// GitHub lists them (it lists at most 3000).
func (r *Reader) PullFiles(ctx context.Context, repo string, number int) ([]PullFile, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("%w: pull request number %d", ErrInvalidArgument, number)
	}
	raw, err := readPages[struct {
		Filename string `json:"filename"`
		Status   string `json:"status"`
		Patch    string `json:"patch"`
	}](ctx, r.client, func(page int) string {
		return fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=%d&page=%d", repo, number, pageSize, page)
	})
	if err != nil {
		return nil, fmt.Errorf("files of pull request %d of %s: %w", number, repo, err)
	}
	files := make([]PullFile, len(raw))
	for i, f := range raw {
		if f.Filename == "" || f.Status == "" {
			return nil, fmt.Errorf("%w: file of pull request %d of %s without a filename or status", ErrInvalidResponse, number, repo)
		}
		files[i] = PullFile{Path: f.Filename, Status: f.Status, Patch: f.Patch}
	}
	return files, nil
}

// LineRange is a span of lines of a file, 1-based and inclusive.
type LineRange struct{ Start, End int }

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// NewLineRanges returns the line spans the patch adds or keeps context around
// in the new version of the file, one per hunk; a pure deletion has none.
func (f PullFile) NewLineRanges() ([]LineRange, error) {
	var ranges []LineRange
	// A diff body line starts with " ", "+", "-" or "\", so "@@" starts a header.
	for _, line := range strings.Split(f.Patch, "\n") {
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		m := hunkHeader.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("%w: malformed hunk header in the patch of %s", ErrInvalidResponse, f.Path)
		}
		start, errStart := strconv.Atoi(m[1])
		count, errCount := 1, error(nil)
		if m[2] != "" {
			count, errCount = strconv.Atoi(m[2])
		}
		if err := errors.Join(errStart, errCount); err != nil {
			return nil, fmt.Errorf("%w: hunk header numbers of the patch of %s are out of range", ErrInvalidResponse, f.Path)
		}
		if count > 0 {
			ranges = append(ranges, LineRange{Start: start, End: start + count - 1})
		}
	}
	return ranges, nil
}
