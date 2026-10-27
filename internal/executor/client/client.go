// Package client is the executor's side of the executor API (docs/contracts/runtime.md, "The executor API"): it leases a job, and
// with the lease it heartbeats, downloads the commit archives and the manifest files, and posts the result. It holds the executor
// token and nothing else, never reads a proxy setting from the environment and never follows a redirect, so the token goes to the
// service's origin and nowhere else.
package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// The errors that the API answers with, by status. Any 409 under /jobs/{id} is ErrLeaseLost: stop, discard the work, post nothing.
var (
	ErrNoJob        = errors.New("no job is ready")                        // 204
	ErrInvalid      = errors.New("the service refused the request")        // 400
	ErrUnauthorized = errors.New("the service refused the token")          // 401
	ErrNotFound     = errors.New("the service does not know the job")      // 404
	ErrLeaseLost    = errors.New("the lease was lost")                     // 409
	ErrTooLarge     = errors.New("the answer or the request is too large") // 413
	ErrGitHub       = errors.New("the service could not read GitHub")      // 502
	ErrUnavailable  = errors.New("the service is not ready")               // 503
	ErrServer       = errors.New("the service failed")                     // 500 and any other status
	// ErrBadAnswer reports an answer of the service that is not what the contract says.
	ErrBadAnswer = errors.New("the service sent an answer that breaks the contract")
)

// The states of an answered result.
const (
	StateComplete    = "complete"
	StateUnavailable = "unavailable"
	StatePending     = "pending"
)

// Client talks to one service. It is safe for concurrent use.
type Client struct {
	origin          string
	token           config.Secret
	hc              *http.Client
	callTimeout     time.Duration // an API call: everything but the body of a download
	downloadTimeout time.Duration // a download, its body included
}

// New returns a client of the service at origin, an https URL, or an http URL for a loopback host, with no path, query or user info.
func New(origin string, token config.Secret) (*Client, error) {
	return nil, errors.New("not implemented")
}

// ValidOrigin reports whether origin is one that New accepts.
func ValidOrigin(origin string) bool { return false }

// Lease asks for a job. It returns ErrNoJob when none is ready, and ErrGitHub when the service could not read GitHub for the job it
// would have granted.
func (c *Client) Lease(ctx context.Context) (*Lease, error) {
	return nil, errors.New("not implemented")
}

// Lease is a job and the lease on it: every call under the job carries the lease.
type Lease struct {
	Job
	c *Client
}

// Heartbeat extends the lease. ErrLeaseLost means the lease is gone; ErrNotFound, that the job is.
func (l *Lease) Heartbeat(ctx context.Context) error {
	return errors.New("not implemented")
}

// Result posts the result body and returns the state of the job that the service answered: StateComplete, StateUnavailable, or
// StatePending when the attempt is queued again. It posts once, and does not retry.
func (l *Lease) Result(ctx context.Context, body []byte) (string, error) {
	return "", errors.New("not implemented")
}

// Archive opens the tar.gz of the head or the base commit. The caller closes it; a download is bounded in time, and a stream that
// stops short ends with the error of its body.
func (l *Lease) Archive(ctx context.Context, side string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

// File opens the manifest file with the digest (as manifest.files names it). The caller closes it.
func (l *Lease) File(ctx context.Context, digest string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
