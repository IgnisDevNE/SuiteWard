// Package rest is SuiteWard's HTTP transport to the GitHub REST API: conditional
// requests scoped to the token identity, a process-wide request budget (D-SYNC)
// and typed errors (docs/contracts/github.md, A2).
package rest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
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
	Date        time.Time // this response's own HTTP Date in UTC (a 304's too, never the cache entry's); zero when missing or unparsable
}

var (
	// ErrBudgetExhausted matches a *BudgetExhaustedError.
	ErrBudgetExhausted = errors.New("github: request budget exhausted")
	// ErrNotSent matches the failure to get a token, which happens before any request is sent.
	ErrNotSent = errors.New("github: request not sent")
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
	// ErrTooLarge matches a download that exceeds its size limit.
	ErrTooLarge = errors.New("github: download exceeds the size limit")
)

// maxBody bounds any response body read into memory; a variable only so tests can lower it.
var maxBody int64 = 64 << 20

// maxCacheEntries bounds the ETag cache by paths; a variable only so tests can lower it.
var maxCacheEntries = 4096

// maxCacheBytes bounds the ETag cache by the total size of its bodies; a variable only so tests can lower it.
var maxCacheBytes = 32 << 20

// maxClockOffset bounds the offset a Date header may set: windows last at most
// an hour, so a Date further from the local clock is a stale or broken
// intermediary and is ignored.
const maxClockOffset = time.Hour

// BudgetExhaustedError is returned by Get, without calling GitHub, while the
// requests used in the current window are at the budget.
type BudgetExhaustedError struct{ Reset time.Time }

func (e *BudgetExhaustedError) Error() string {
	return fmt.Sprintf("%v until %s", ErrBudgetExhausted, e.Reset.UTC().Format(time.RFC3339))
}

func (e *BudgetExhaustedError) Unwrap() error { return ErrBudgetExhausted }

// RateLimitedError is a 403 or 429 carrying X-RateLimit-Remaining: 0 or
// Retry-After. Until is when GitHub allows the next request in local time:
// Retry-After when present, else X-RateLimit-Reset; it is zero when GitHub sent
// neither in a parsable form.
type RateLimitedError struct {
	Status int
	Until  time.Time
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("%v: status %d until %s", ErrRateLimited, e.Status, e.Until.UTC().Format(time.RFC3339))
}

func (e *RateLimitedError) Unwrap() error { return ErrRateLimited }

// StatusError is any other non-2xx answer. It carries the status only: GitHub's answer text can quote repository text.
type StatusError struct{ Status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("github: status %d", e.Status)
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
// headers of each response; until the first response, and once a window has
// ended until the next response, it does not refuse. The window ends in
// GitHub's time: the Date header of each response gives the offset between the
// local clock and GitHub's (ignored when more than an hour apart), so a skewed
// clock neither ends a window early nor disables the cap. It is safe for concurrent use.
type Budget struct {
	fraction float64
	clock    func() time.Time

	mu       sync.Mutex
	offset   time.Duration // local clock minus GitHub's, from the latest Date header; 0 until one is seen
	known    bool          // a window has been read from GitHub; reset, limit and used describe it
	limit    int           // X-RateLimit-Limit of the window
	reset    time.Time     // X-RateLimit-Reset of the window, in GitHub's time
	used     int           // highest limit − remaining GitHub reported in the window
	inFlight int           // requests sent and not yet answered
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
	active := b.known && b.clock().Add(-b.offset).Before(b.reset)
	if active && float64(b.used+b.inFlight) >= b.fraction*float64(b.limit) {
		return &BudgetExhaustedError{Reset: b.reset.Add(b.offset)} // local time, so waiting until it succeeds
	}
	b.inFlight++
	return nil
}

// local converts a time in GitHub's clock to the local clock.
func (b *Budget) local(t time.Time) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return t.Add(b.offset)
}

// Window is what the budget knows of the current rate-limit window.
type Window struct {
	Remaining int       // requests SuiteWard may still send in the window; 0 once refused
	Reset     time.Time // the end of the window, on the local clock
}

// Window returns the current window; known is false before the first response
// and once the window has ended, until the next response, as acquire does not refuse then either.
func (b *Budget) Window() (w Window, known bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.known || !b.clock().Add(-b.offset).Before(b.reset) {
		return Window{}, false
	}
	allowed := int(math.Ceil(b.fraction * float64(b.limit)))
	return Window{Remaining: max(0, allowed-b.used-b.inFlight), Reset: b.reset.Add(b.offset)}, true
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
	if date, err := http.ParseTime(h.Get("Date")); err == nil {
		if off := b.clock().Sub(date); off.Abs() <= maxClockOffset {
			b.offset = off
		}
	}
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
	used uint64 // the client's tick at its last use: the smallest is the first to go
}

// Client performs conditional GETs against one GitHub API base URL. It is
// safe for concurrent use and never retries.
type Client struct {
	token  TokenFunc
	http   *http.Client
	base   string
	budget *Budget

	mu         sync.Mutex
	identity   string            // token identity the cache belongs to
	cache      map[string]cached // by request path, at most maxCacheEntries
	cacheBytes int               // total size of the cached bodies, at most maxCacheBytes
	tick       uint64
}

// NewClient returns a client; baseURL must be absolute and carry no query,
// fragment or user info (the request path is appended to it).
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
		return nil, errors.New("github client: base URL is not an absolute URL")
	}
	if strings.ContainsAny(baseURL, "?#") || u.User != nil {
		return nil, errors.New("github client: base URL must not carry a query, a fragment or user info")
	}
	return &Client{
		token:  token,
		http:   httpClient,
		base:   strings.TrimSuffix(baseURL, "/"),
		budget: budget,
		cache:  map[string]cached{},
	}, nil
}

// Get performs a conditional GET. A 304 returns a copy of the cached body with
// NotModified true; a returned body is never shared with the cache.
func (c *Client) Get(ctx context.Context, path string) (Response, error) {
	return c.get(ctx, path, true)
}

// GetImmutable performs a GET for a SHA-addressed path (git trees and blobs):
// same headers, budget and errors as Get, but it sends no If-None-Match, never
// reads or writes the ETag cache and treats a 304 as an error.
func (c *Client) GetImmutable(ctx context.Context, path string) (Response, error) {
	return c.get(ctx, path, false)
}

// MaxPostBody bounds a POST body; comments are far smaller.
const MaxPostBody = 64 << 10

// Post sends a JSON body and expects 201. It shares the budget and the rate-limit
// errors of Get but never uses the ETag cache. The request body never reaches an error: a comment can quote repository text.
func (c *Client) Post(ctx context.Context, path string, body []byte) (Response, error) {
	if !strings.HasPrefix(path, "/") {
		return Response{}, fmt.Errorf("github POST %q: path must start with /", path)
	}
	if len(body) > MaxPostBody {
		return Response{}, fmt.Errorf("github POST %s: body of %d bytes exceeds the limit of %d", path, len(body), MaxPostBody)
	}
	if err := c.budget.acquire(); err != nil {
		return Response{}, fmt.Errorf("github POST %s: %w", path, err)
	}
	resp, err := c.send(ctx, http.MethodPost, path, body, false)
	if err != nil {
		c.budget.release()
		return Response{}, fmt.Errorf("github POST %s: %w", path, err)
	}
	c.budget.record(resp.status, resp.header)
	out, err := c.interpret(resp)
	if err != nil {
		return Response{}, fmt.Errorf("github POST %s: %w", path, err)
	}
	if out.Status != http.StatusCreated {
		return Response{}, fmt.Errorf("github POST %s: answered %d, want 201", path, out.Status)
	}
	return out, nil
}

// Download reads an archive: it sends a GET of path with the token, follows the one redirect that answers it
// without the token and copies the body to dst in chunks, so a large archive is never held in memory. It counts
// once in the budget, from the API's answer; the archive host's answer is not an API request. It never retries.
// The copy stops at limit bytes with ErrTooLarge (a declared length over the limit before a byte is copied) and
// a body that ends before its declared length is an error. Both requests go through the client's transport and
// not through the client, which would follow the redirect with its own rules, bound the whole exchange (the
// body included) by its Timeout, and quote the redirect's address in its errors. The Timeout bounds the API's
// answer only: ctx, which must carry a deadline, bounds the archive host and the body. No error repeats the
// redirect's address, whose query carries a short-lived credential for a private repository.
func (c *Client) Download(ctx context.Context, path string, dst io.Writer, limit int64) (int64, error) {
	if !strings.HasPrefix(path, "/") {
		return 0, fmt.Errorf("github download %q: path must start with /", path)
	}
	if limit <= 0 {
		return 0, fmt.Errorf("github download %s: limit %d is not positive", path, limit)
	}
	if err := c.budget.acquire(); err != nil {
		return 0, fmt.Errorf("github download %s: %w", path, err)
	}
	transport := c.http.Transport
	if transport == nil { // the optional field: a nil Transport is http.DefaultTransport
		transport = http.DefaultTransport
	}
	location, err := c.redirect(ctx, transport, path)
	if err != nil {
		return 0, fmt.Errorf("github download %s: %w", path, err)
	}
	n, err := c.copyBody(ctx, transport, location, dst, limit)
	if err != nil {
		return n, fmt.Errorf("github download %s: %w", path, err)
	}
	return n, nil
}

// redirect sends the GET of path with the token and returns where GitHub redirects it. It settles the
// reservation of Download with the answer's rate-limit headers.
func (c *Client) redirect(ctx context.Context, transport http.RoundTripper, path string) (*url.URL, error) {
	value, _, err := c.token(ctx)
	if err != nil {
		c.budget.release()
		return nil, fmt.Errorf("%w: token: %w", ErrNotSent, err)
	}
	if c.http.Timeout > 0 { // the client's Timeout bounds this answer, so a hung request cannot hold its reservation
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.http.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		c.budget.release()
		return nil, err
	}
	setAPIHeaders(req, value)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		c.budget.release()
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }() // the body of a redirect is not read
	c.budget.record(resp.StatusCode, resp.Header)
	if err := c.expect(resp, http.StatusFound); err != nil {
		return nil, err
	}
	location, err := resp.Location()
	if err != nil {
		return nil, errors.New("the redirect has no usable Location") // the parse error would quote the address
	}
	return location, nil
}

// copyBody copies the body of a GET of location, sent without the token, to dst: at most limit bytes.
func (c *Client) copyBody(ctx context.Context, transport http.RoundTripper, location *url.URL, dst io.Writer, limit int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", location.Host, err)
	}
	req.Header.Set("Accept-Encoding", "identity") // the bytes of the archive, which the transport must not unpack
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", location.Host, err)
	}
	defer func() { _ = resp.Body.Close() }() // a close error cannot change the outcome of a read that ended
	if err := c.expect(resp, http.StatusOK); err != nil {
		return 0, err
	}
	if resp.ContentLength > limit {
		return 0, fmt.Errorf("%w: the answer declares %d bytes, the limit is %d", ErrTooLarge, resp.ContentLength, limit)
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, limit))
	if err != nil {
		return n, fmt.Errorf("copy the body: %w", err)
	}
	if n < limit {
		return n, nil
	}
	// limit bytes were copied: the body ends here or goes on past the limit.
	switch _, err := io.ReadFull(resp.Body, make([]byte, 1)); {
	case err == nil:
		return n, fmt.Errorf("%w: the body goes on past the limit of %d bytes", ErrTooLarge, limit)
	case !errors.Is(err, io.EOF):
		return n, fmt.Errorf("read past the limit: %w", err)
	}
	return n, nil
}

// expect is nil when resp answers want and otherwise the typed error of its status. It never reads the body:
// GitHub's answer text can quote repository text.
func (c *Client) expect(resp *http.Response, want int) error {
	if resp.StatusCode == want {
		return nil
	}
	if _, err := c.interpret(answer{status: resp.StatusCode, header: resp.Header}); err != nil {
		return err
	}
	return fmt.Errorf("answered %d, want %d", resp.StatusCode, want)
}

func (c *Client) get(ctx context.Context, path string, useCache bool) (Response, error) {
	if !strings.HasPrefix(path, "/") {
		return Response{}, fmt.Errorf("github GET %q: path must start with /", path)
	}
	if err := c.budget.acquire(); err != nil {
		return Response{}, fmt.Errorf("github GET %s: %w", path, err)
	}
	resp, err := c.send(ctx, http.MethodGet, path, nil, useCache)
	if err != nil {
		c.budget.release()
		return Response{}, fmt.Errorf("github GET %s: %w", path, err)
	}
	c.budget.record(resp.status, resp.header)
	out, err := c.interpret(resp)
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
	useCache bool
}

// send performs one request with the current token and, when useCache is set
// and the cache of that token's identity holds the path, its ETag. A request
// with a body is sent as JSON.
func (c *Client) send(ctx context.Context, method, path string, payload []byte, useCache bool) (answer, error) {
	value, identity, err := c.token(ctx)
	if err != nil {
		return answer{}, fmt.Errorf("%w: token: %w", ErrNotSent, err)
	}
	var entry cached
	var hadCache bool
	if useCache {
		c.mu.Lock()
		if identity != c.identity {
			c.identity, c.cache, c.cacheBytes = identity, map[string]cached{}, 0
		}
		entry, hadCache = c.cache[path]
		if hadCache {
			c.tick++
			entry.used = c.tick
			c.cache[path] = entry
		}
		c.mu.Unlock()
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return answer{}, err
	}
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	setAPIHeaders(req, value)
	if hadCache {
		req.Header.Set("If-None-Match", entry.etag)
	}
	resp, err := c.http.Do(req) // the error names the URL, never a header
	if err != nil {
		return answer{}, err
	}
	defer func() { _ = resp.Body.Close() }() // the body is read (up to the limit) below; a close error cannot change the outcome
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return answer{}, fmt.Errorf("read body: %w", err)
	}
	if int64(len(body)) > maxBody {
		return answer{}, fmt.Errorf("response body exceeds the limit of %d bytes", maxBody)
	}
	return answer{path: path, identity: identity, useCache: useCache, status: resp.StatusCode, header: resp.Header, body: body, cached: entry, hadCache: hadCache}, nil
}

// setAPIHeaders sets the token and the media type of a request to the API.
func setAPIHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

// interpret turns GitHub's answer into a Response or a typed error, and keeps
// the ETag of a 2xx for the identity that requested it.
func (c *Client) interpret(a answer) (Response, error) {
	switch {
	case a.status == http.StatusNotModified:
		if !a.hadCache {
			return Response{}, errors.New("github answered 304 to an unconditional request")
		}
		return Response{Status: a.status, Body: bytes.Clone(a.cached.body), NotModified: true, ETag: a.cached.etag, Date: responseDate(a.header)}, nil
	case a.status >= 200 && a.status < 300:
		etag := a.header.Get("ETag")
		if a.useCache {
			c.mu.Lock()
			if a.identity == c.identity {
				if etag != "" {
					c.store(a.path, etag, a.body)
				} else {
					c.drop(a.path)
				}
			}
			c.mu.Unlock()
		}
		return Response{Status: a.status, Body: a.body, ETag: etag, Date: responseDate(a.header)}, nil
	}
	if a.status == http.StatusForbidden || a.status == http.StatusTooManyRequests {
		retryAfter, hasRetry := a.header["Retry-After"]
		if hasRetry || a.header.Get("X-RateLimit-Remaining") == "0" {
			return Response{}, &RateLimitedError{Status: a.status, Until: c.retryTime(retryAfter, a.header.Get("X-RateLimit-Reset"))}
		}
	}
	return Response{}, &StatusError{Status: a.status}
}

// responseDate is the answer's own Date in UTC, zero when the header is missing or cannot be parsed. Unlike the
// budget's offset it is not filtered: the caller decides what a zero or a far Date means.
func responseDate(h http.Header) time.Time {
	date, err := http.ParseTime(h.Get("Date"))
	if err != nil {
		return time.Time{}
	}
	return date.UTC()
}

// maxRetryAfterSeconds bounds the wait a Retry-After in seconds can ask for.
const maxRetryAfterSeconds = 3600

// retryTime prefers Retry-After (seconds or an HTTP date) over X-RateLimit-Reset
// (Unix seconds) and returns local time: the date and the reset are in GitHub's
// clock and are shifted by the offset the response's Date just updated.
func (c *Client) retryTime(retryAfter []string, reset string) time.Time {
	if len(retryAfter) > 0 {
		if s, err := strconv.Atoi(retryAfter[0]); err == nil && s >= 0 {
			return c.budget.clock().Add(time.Duration(min(s, maxRetryAfterSeconds)) * time.Second) // a huge value must not overflow the Duration
		}
		if at, err := http.ParseTime(retryAfter[0]); err == nil {
			return c.budget.local(at)
		}
	}
	if r, err := strconv.ParseInt(reset, 10, 64); err == nil {
		return c.budget.local(time.Unix(r, 0))
	}
	return time.Time{}
}

// store keeps the ETag and body of path, first dropping its old entry and then the entries used least recently until the new one fits both
// maxCacheEntries and maxCacheBytes; a body larger than maxCacheBytes is not kept. The caller holds c.mu. The bound by paths holds a polling
// cycle over some thirteen hundred open pull requests (about three paths each, a few more for each repository), far more than one
// installation observes; the bound by bytes keeps large bodies from growing the cache past the container's memory. An entry dropped early
// costs one full read, never a stale answer. The scan is linear, once for each dropped entry, which follows a network round trip.
func (c *Client) store(path, etag string, body []byte) {
	c.drop(path)
	if len(body) > maxCacheBytes {
		return
	}
	for len(c.cache) > 0 && (len(c.cache) >= maxCacheEntries || c.cacheBytes+len(body) > maxCacheBytes) {
		oldest, found := "", false
		for p, e := range c.cache {
			if !found || e.used < c.cache[oldest].used {
				oldest, found = p, true
			}
		}
		c.drop(oldest)
	}
	c.tick++
	c.cache[path] = cached{etag: etag, body: bytes.Clone(body), used: c.tick}
	c.cacheBytes += len(body)
}

// drop removes path from the cache and its body from the byte total; the caller holds c.mu.
func (c *Client) drop(path string) {
	c.cacheBytes -= len(c.cache[path].body)
	delete(c.cache, path)
}
