// Package rest is SuiteWard's HTTP transport to the GitHub REST API: conditional
// requests scoped to the token identity, a process-wide request budget (D-SYNC)
// and typed errors (docs/contracts/github.md, A2).
package rest

import (
	"context"
	"errors"
	"net/http"
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

var errNotImplemented = errors.New("rest: not implemented")

// BudgetExhaustedError is returned by Get, without calling GitHub, while the
// requests used in the current window are at the budget.
type BudgetExhaustedError struct{ Reset time.Time }

func (e *BudgetExhaustedError) Error() string { panic("not implemented") }

// RateLimitedError is a 403 or 429 carrying X-RateLimit-Remaining: 0 or
// Retry-After. Until is when GitHub allows the next request.
type RateLimitedError struct {
	Status int
	Until  time.Time
	Body   string
}

func (e *RateLimitedError) Error() string { panic("not implemented") }

// StatusError is any other non-2xx answer. Body is truncated to 512 bytes.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string { panic("not implemented") }

// Budget limits the requests of every client of the process to a fraction of
// the installation's rate limit. It is safe for concurrent use.
type Budget struct{}

// NewBudget returns a budget using fraction (in (0, 1]) of each rate-limit window.
func NewBudget(fraction float64, clock func() time.Time) (*Budget, error) {
	return nil, errNotImplemented
}

// Client performs conditional GETs against one GitHub API base URL.
type Client struct{}

// NewClient returns a client; baseURL must be absolute.
func NewClient(token TokenFunc, httpClient *http.Client, baseURL string, budget *Budget) (*Client, error) {
	return nil, errNotImplemented
}

// Get performs a conditional GET. A 304 returns the cached body with NotModified true.
func (c *Client) Get(ctx context.Context, path string) (Response, error) {
	return Response{}, errNotImplemented
}
