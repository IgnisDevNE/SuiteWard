package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// apiError is a non-2xx, non-304 GitHub answer.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("github %d: %s", e.Status, e.Message) }

type call struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
}

type client struct {
	base, appID, installationID string
	key                         *rsa.PrivateKey
	permissions                 map[string]string // narrowing for the installation token; empty means all
	http                        *http.Client
	log                         *logger
	etags                       map[string]string // shared with state.Etags
	cache                       map[string][]byte // last 200 body per GET path, memory only
	token                       string
	tokenExpires                time.Time
	calls                       []call // every call since the last reset, for the poll event
}

func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("key file holds no PEM block")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("key file is neither PKCS#1 nor PKCS#8")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("key file is not an RSA key")
	}
	return rk, nil
}

// appJWT builds the RS256 JWT that authenticates as the App.
func (c *client) appJWT(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	claims, err := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": c.appID})
	if err != nil {
		return "", err
	}
	signing := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(nil, c.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// installationToken returns the cached token until five minutes before it expires.
func (c *client) installationToken(ctx context.Context) (string, error) {
	if c.token != "" && time.Now().Before(c.tokenExpires.Add(-5*time.Minute)) {
		return c.token, nil
	}
	jwt, err := c.appJWT(time.Now())
	if err != nil {
		return "", err
	}
	var body []byte
	if len(c.permissions) > 0 {
		if body, err = json.Marshal(map[string]any{"permissions": c.permissions}); err != nil {
			return "", err
		}
	}
	r, err := c.call(ctx, http.MethodPost, "/app/installations/"+c.installationID+"/access_tokens", jwt, body, "")
	if err != nil {
		return "", err
	}
	var out struct {
		Token               string            `json:"token"`
		Permissions         map[string]string `json:"permissions"`
		RepositorySelection string            `json:"repository_selection"`
		ExpiresAt           time.Time         `json:"expires_at"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil || out.Token == "" {
		return "", errors.New("access_tokens response has no token")
	}
	c.token, c.tokenExpires = out.Token, out.ExpiresAt
	c.log.emit("token", map[string]any{"action": "minted", "expires_at": out.ExpiresAt, "requested_permissions": c.permissions, "granted_permissions": out.Permissions, "repository_selection": out.RepositorySelection})
	return c.token, nil
}

type response struct {
	status int
	header http.Header
	body   []byte
}

// call performs one logged request. A non-2xx, non-304 status is an *apiError.
func (c *client) call(ctx context.Context, method, path, bearer string, body []byte, etag string) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "suiteward-s1-spike")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	began := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		// The transport error names the URL but never a header.
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }() // the body is read to the end below; a close error cannot change the outcome
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("%s %s: read body: %w", method, path, err)
	}
	r := &response{status: resp.StatusCode, header: resp.Header, body: b}
	c.calls = append(c.calls, call{method, path, r.status})
	if r.status == http.StatusUnauthorized && bearer == c.token {
		c.token = "" // revoked or expired early: the next call re-mints it
	}
	if r.status >= 300 && r.status != http.StatusNotModified {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &e) // a non-JSON error body just leaves the message empty
		msg := e.Message
		if len(msg) > 200 {
			msg = msg[:200]
		}
		c.log.http(began, method, path, r.status, etag != "", r.header, msg)
		return nil, &apiError{Status: r.status, Message: msg}
	}
	c.log.http(began, method, path, r.status, etag != "", r.header, "")
	return r, nil
}

// get performs an authenticated GET with If-None-Match when a cached body
// exists. A 304 returns the cached body; after a restart the cache is empty,
// so the first request of each URL is unconditional.
func (c *client) get(ctx context.Context, path string) ([]byte, error) {
	tok, err := c.installationToken(ctx)
	if err != nil {
		return nil, err
	}
	etag := ""
	if _, ok := c.cache[path]; ok {
		etag = c.etags[path]
	}
	r, err := c.call(ctx, http.MethodGet, path, tok, nil, etag)
	if err != nil {
		return nil, err
	}
	if r.status == http.StatusNotModified {
		return c.cache[path], nil
	}
	c.cache[path] = r.body
	if e := r.header.Get("ETag"); e != "" {
		c.etags[path] = e
	}
	return r.body, nil
}

type pull struct {
	Number int `json:"number"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

// listPath is the polled open PR list. shortcut: one page of 100, the throwaway repo has a handful of PRs.
func listPath(repo string) string { return "/repos/" + repo + "/pulls?state=open&per_page=100" }

func treePath(repo, sha string) string {
	return "/repos/" + repo + "/git/trees/" + sha + "?recursive=1"
}

func (c *client) openPulls(ctx context.Context, repo string) ([]pull, error) {
	b, err := c.get(ctx, listPath(repo))
	if err != nil {
		return nil, err
	}
	var ps []pull
	if err := json.Unmarshal(b, &ps); err != nil {
		return nil, fmt.Errorf("parse pull list: %w", err)
	}
	return ps, nil
}

func (c *client) tree(ctx context.Context, repo, sha string) ([]treeEntry, error) {
	b, err := c.get(ctx, treePath(repo, sha))
	if err != nil {
		return nil, err
	}
	var t struct {
		Tree      []treeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("parse tree: %w", err)
	}
	if t.Truncated {
		return nil, errors.New("tree response is truncated; the spike does not page it")
	}
	return t.Tree, nil
}

type comment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

func commentsPath(repo string, number int) string {
	return fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", repo, number)
}

// getJSON is a conditional GET decoded into v.
func (c *client) getJSON(ctx context.Context, path string, v any) error {
	b, err := c.get(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// comments returns the first page of a PR's comments. shortcut: no paging, a spike PR has a handful.
func (c *client) comments(ctx context.Context, repo string, number int) ([]comment, error) {
	var cs []comment
	if err := c.getJSON(ctx, commentsPath(repo, number), &cs); err != nil {
		return nil, err
	}
	return cs, nil
}

// pullDetail is the part of GET /pulls/{n} the merge pass uses.
type pullDetail struct {
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	MergedAt       string `json:"merged_at"`
	MergedBy       struct {
		Login string `json:"login"`
	} `json:"merged_by"` // null while unmerged: decodes to the zero value
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
}

// gitCommit is the part of GET /git/commits/{sha} the merge pass uses.
type gitCommit struct {
	Message string `json:"message"`
	Tree    struct {
		SHA string `json:"sha"`
	} `json:"tree"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
}

// putCheck creates (id == 0) or updates a check run and returns its id.
// state is in_progress or a conclusion (success, failure, neutral, action_required).
func (c *client) putCheck(ctx context.Context, repo, name, headSHA string, id int64, state, summary string) (int64, error) {
	tok, err := c.installationToken(ctx)
	if err != nil {
		return 0, err
	}
	body := map[string]any{"name": name, "status": "in_progress", "output": map[string]string{"title": name, "summary": summary}}
	if state != "in_progress" {
		body["status"], body["conclusion"] = "completed", state
	}
	method, path := http.MethodPatch, fmt.Sprintf("/repos/%s/check-runs/%d", repo, id)
	if id == 0 {
		method, path = http.MethodPost, "/repos/"+repo+"/check-runs"
		body["head_sha"] = headSHA
	}
	b, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	r, err := c.call(ctx, method, path, tok, b, "")
	if err != nil {
		return 0, err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil || out.ID == 0 {
		return 0, errors.New("check run response has no id")
	}
	return out.ID, nil
}

// parsePermissions turns "name:level,name:level" into the access_tokens permissions object.
func parsePermissions(s string) (map[string]string, error) {
	m := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if kv = strings.TrimSpace(kv); kv == "" {
			continue
		}
		name, level, ok := strings.Cut(kv, ":")
		if !ok || name == "" || level == "" {
			return nil, fmt.Errorf("S1_TOKEN_PERMISSIONS entry %q is not name:level", kv)
		}
		m[name] = level
	}
	return m, nil
}
