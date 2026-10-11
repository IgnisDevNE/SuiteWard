// Package rest is SuiteWard's HTTP transport to the GitHub REST API: conditional
// requests scoped to the token identity, a process-wide request budget (D-SYNC)
// and typed errors (docs/contracts/github.md, A2).
package rest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TokenFunc returns the bearer value and a stable identity of the current
// token; the identity changes when the token rotates and is never the value.
type TokenFunc func(ctx context.Context) (value, identity string, err error)

// Response is the answer to a GET. A 304 carries the cached body with NotModified true.
type Response struct {
	Status      int
	Body        []byte
	NotModified bool
	ETag        string
}

var (
	// ErrBudgetExhausted matches a *BudgetExhaustedError.
	ErrBudgetExhausted = errors.New("github: request budget exhausted")
	// ErrUnauthorized matches a *StatusError for 401.
	ErrUnauthorized = errors.New("github: unauthorized")
	// ErrForbidden matches a *StatusError for a 403 without rate-limit signals.
	ErrForbidden = errors.New("github: forbidden")
	// ErrNotFound matches a *StatusError for 404.
	ErrNotFound = errors.New("github: not found")
	// ErrUnprocessable matches a *StatusError for 422.
	ErrUnprocessable = errors.New("github: unprocessable")
	// ErrRateLimited matches a *RateLimitedError.
	ErrRateLimited = errors.New("github: rate limited")
)

// maxErrorBody bounds the response body kept in an error.
const maxErrorBody = 512

// BudgetExhaustedError is returned by Get, without calling GitHub, while the
// requests used in the current window are at the budget.
type BudgetExhaustedError struct{ Reset time.Time }

func (e *BudgetExhaustedError) Error() string {
	return fmt.Sprintf("%v until %s", ErrBudgetExhausted, e.Reset.UTC().Format(time.RFC3339))
}

func (e *BudgetExhaustedError) Unwrap() error { return ErrBudgetExhausted }

// RateLimitedError is a 403 or 429 carrying X-RateLimit-Remaining: 0 or
// Retry-After. Until is when GitHub allows the next request: Retry-After when
// present, else X-RateLimit-Reset; it is zero when GitHub sent neither in a
// parsable form.
type RateLimitedError struct {
	Status int
	Until  time.Time
	Body   string
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("%v: status %d until %s: %s", ErrRateLimited, e.Status, e.Until.UTC().Format(time.RFC3339), e.Body)
}

func (e *RateLimitedError) Unwrap() error { return ErrRateLimited }

// StatusError is any other non-2xx answer. Body is truncated to 512 bytes.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("github: status %d: %s", e.Status, e.Body)
}

// statusKinds maps the statuses callers distinguish; any other status matches no sentinel.
var statusKinds = map[int]error{
	http.StatusUnauthorized:        ErrUnauthorized,
	http.StatusForbidden:           ErrForbidden,
	http.StatusNotFound:            ErrNotFound,
	http.StatusUnprocessableEntity: ErrUnprocessable,
}

func (e *StatusError) Unwrap() error { return statusKinds[e.Status] }

// Budget limits the requests of every client of the process to a fraction of
// the installation's rate limit. It learns the window from the X-RateLimit-*
// headers of each response; until the first response, and after a reset until
// the next one, it does not refuse. It is safe for concurrent use.
type Budget struct {
	fraction float64
	clock    func() time.Time

	mu       sync.Mutex
	known    bool      // a window has been read from GitHub and has not reset
	limit    int       // X-RateLimit-Limit of the window
	reset    time.Time // X-RateLimit-Reset of the window
	used     int       // highest limit − remaining GitHub reported in the window
	inFlight int       // requests sent and not yet answered
}

// NewBudget returns a budget using fraction (in (0, 1]) of each rate-limit window.
func NewBudget(fraction float64, clock func() time.Time) (*Budget, error) {
	if !(fraction > 0 && fraction <= 1) {
		return nil, fmt.Errorf("github budget: fraction %v is not in (0, 1]", fraction)
	}
	if clock == nil {
		return nil, errors.New("github budget: nil clock")
	}
	return &Budget{fraction: fraction, clock: clock}, nil
}

// acquire reserves one request or refuses it. In-flight requests count, so
// concurrent clients cannot overshoot the budget together.
func (b *Budget) acquire() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.known && !b.clock().Before(b.reset) {
		b.known, b.used = false, 0
	}
	if b.known && float64(b.used+b.inFlight) >= b.fraction*float64(b.limit) {
		return &BudgetExhaustedError{Reset: b.reset}
	}
	b.inFlight++
	return nil
}

// release returns a reservation whose request got no response.
func (b *Budget) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inFlight--
}

// record settles a reservation with GitHub's answer. A 304 is not counted;
// a response without parsable rate-limit headers carries no window to learn.
func (b *Budget) record(status int, h http.Header) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inFlight--
	if status == http.StatusNotModified {
		return
	}
	limit, errL := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	remaining, errR := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	resetUnix, errT := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if errL != nil || errR != nil || errT != nil || limit <= 0 {
		return
	}
	reset := time.Unix(resetUnix, 0)
	switch {
	case !b.known || reset.After(b.reset):
		b.known, b.limit, b.reset, b.used = true, limit, reset, limit-remaining
	case reset.Equal(b.reset):
		b.limit, b.used = limit, max(b.used, limit-remaining)
	}
	// A reset earlier than the current window belongs to an older window: ignored.
}

type cached struct {
	etag string
	body []byte
}

// Client performs conditional GETs against one GitHub API base URL. It is
// safe for concurrent use and never retries.
type Client struct {
	token  TokenFunc
	http   *http.Client
	base   string
	budget *Budget

	mu       sync.Mutex
	identity string            // token identity the cache belongs to
	cache    map[string]cached // by request path
}

// NewClient returns a client; baseURL must be absolute.
func NewClient(token TokenFunc, httpClient *http.Client, baseURL string, budget *Budget) (*Client, error) {
	if token == nil {
		return nil, errors.New("github client: nil token func")
	}
	if httpClient == nil {
		return nil, errors.New("github client: nil http client")
	}
	if budget == nil {
		return nil, errors.New("github client: nil budget")
	}
	u, err := url.Parse(baseURL)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return nil, fmt.Errorf("github client: base URL %q is not absolute", baseURL)
	}
	return &Client{
		token:  token,
		http:   httpClient,
		base:   strings.TrimSuffix(baseURL, "/"),
		budget: budget,
		cache:  map[string]cached{},
	}, nil
}

// Get performs a conditional GET. A 304 returns the cached body with NotModified true.
func (c *Client) Get(ctx context.Context, path string) (Response, error) {
	if !strings.HasPrefix(path, "/") {
		return Response{}, fmt.Errorf("github GET %q: path must start with /", path)
	}
	if err := c.budget.acquire(); err != nil {
		return Response{}, fmt.Errorf("github GET %s: %w", path, err)
	}
	resp, value, err := c.send(ctx, path)
	if err != nil {
		c.budget.release()
		return Response{}, fmt.Errorf("github GET %s: %w", path, err)
	}
	c.budget.record(resp.status, resp.header)
	out, err := c.interpret(resp, value)
	if err != nil {
		return Response{}, fmt.Errorf("github GET %s: %w", path, err)
	}
	return out, nil
}

type answer struct {
	path     string
	identity string
	status   int
	header   http.Header
	body     []byte
	cached   cached
	hadCache bool
}

// send performs one request with the current token and, when the cache of
// that token's identity holds the path, its ETag.
func (c *Client) send(ctx context.Context, path string) (answer, string, error) {
	value, identity, err := c.token(ctx)
	if err != nil {
		return answer{}, "", fmt.Errorf("token: %w", err)
	}
	c.mu.Lock()
	if identity != c.identity {
		c.identity, c.cache = identity, map[string]cached{}
	}
	entry, hadCache := c.cache[path]
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return answer{}, "", err
	}
	req.Header.Set("Authorization", "Bearer "+value)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if hadCache {
		req.Header.Set("If-None-Match", entry.etag)
	}
	resp, err := c.http.Do(req) // the error names the URL, never a header
	if err != nil {
		return answer{}, "", err
	}
	defer func() { _ = resp.Body.Close() }() // the body is read to the end below; a close error cannot change the outcome
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return answer{}, "", fmt.Errorf("read body: %w", err)
	}
	return answer{path: path, identity: identity, status: resp.StatusCode, header: resp.Header, body: body, cached: entry, hadCache: hadCache}, value, nil
}

// interpret turns GitHub's answer into a Response or a typed error, and keeps
// the ETag of a 2xx for the identity that requested it.
func (c *Client) interpret(a answer, value string) (Response, error) {
	switch {
	case a.status == http.StatusNotModified:
		if !a.hadCache {
			return Response{}, errors.New("github answered 304 to an unconditional request")
		}
		return Response{Status: a.status, Body: a.cached.body, NotModified: true, ETag: a.cached.etag}, nil
	case a.status >= 200 && a.status < 300:
		etag := a.header.Get("ETag")
		c.mu.Lock()
		if a.identity == c.identity {
			if etag != "" {
				c.cache[a.path] = cached{etag: etag, body: a.body}
			} else {
				delete(c.cache, a.path)
			}
		}
		c.mu.Unlock()
		return Response{Status: a.status, Body: a.body, ETag: etag}, nil
	}
	body := errorBody(a.body, value)
	if a.status == http.StatusForbidden || a.status == http.StatusTooManyRequests {
		retryAfter, hasRetry := a.header["Retry-After"]
		if hasRetry || a.header.Get("X-RateLimit-Remaining") == "0" {
			return Response{}, &RateLimitedError{Status: a.status, Until: c.retryTime(retryAfter, a.header.Get("X-RateLimit-Reset")), Body: body}
		}
	}
	return Response{}, &StatusError{Status: a.status, Body: body}
}

// retryTime prefers Retry-After (seconds) over X-RateLimit-Reset (Unix seconds).
func (c *Client) retryTime(retryAfter []string, reset string) time.Time {
	if len(retryAfter) > 0 {
		if s, err := strconv.Atoi(retryAfter[0]); err == nil && s >= 0 {
			return c.budget.clock().Add(time.Duration(s) * time.Second)
		}
	}
	if r, err := strconv.ParseInt(reset, 10, 64); err == nil {
		return time.Unix(r, 0)
	}
	return time.Time{}
}

// errorBody removes the token from a body (a server could echo it) and
// truncates it to maxErrorBody bytes.
func errorBody(body []byte, value string) string {
	s := string(body)
	if value != "" {
		s = strings.ReplaceAll(s, value, "[redacted]")
	}
	if len(s) > maxErrorBody {
		s = s[:maxErrorBody]
	}
	return s
}
