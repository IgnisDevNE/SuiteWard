// Package client is the executor's side of the executor API (docs/contracts/runtime.md, "The executor API"): it leases a job, and
// with the lease it heartbeats, downloads the commit archives and the manifest files, and posts the result. It holds the executor
// token and nothing else, never reads a proxy setting from the environment and never follows a redirect, so the token goes to the
// service's origin and nowhere else.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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

const (
	apiPath             = "/executor/v1"
	leaseHeader         = "Suiteward-Lease"
	defaultCallTimeout  = 30 * time.Second // the listener's own read and write bound
	defaultDownloadWait = 10 * time.Minute // the longest the service lets a download take
	maxLeaseBytes       = 16 << 20         // the largest lease answer the service sends
	maxAnswerBytes      = 64 << 10         // any other answer
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
// The error never echoes the URL, which may carry what the operator should not have written there.
func New(origin string, token config.Secret) (*Client, error) {
	base, ok := normalizeOrigin(origin)
	if !ok {
		return nil, errors.New("the executor URL must be an https URL, or an http URL for a loopback host, with no path, query or user info")
	}
	if token == "" {
		return nil, errors.New("the executor token is required")
	}
	// The transport has no Proxy, which is how net/http says that the environment's proxy settings are not read: the egress goes direct.
	// It has no overall timeout either, which would cut a download that streams for as long as the sandbox takes to read it.
	hc := &http.Client{
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     30 * time.Second,
		},
		// The token goes to the service's origin and nowhere else: a redirect is an error, not a place to send it.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Client{origin: base, token: token, hc: hc, callTimeout: defaultCallTimeout, downloadTimeout: defaultDownloadWait}, nil
}

// ValidOrigin reports whether origin is one that New accepts.
func ValidOrigin(origin string) bool {
	_, ok := normalizeOrigin(origin)
	return ok
}

// normalizeOrigin returns scheme://host[:port] for an https origin, or an http origin of a loopback host. A trailing slash is the only
// path that is allowed.
func normalizeOrigin(origin string) (string, bool) {
	u, err := url.Parse(origin)
	if err != nil || strings.ContainsAny(origin, "?#") || u.Opaque != "" || u.User != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") {
		return "", false
	}
	switch u.Scheme {
	case "https":
	case "http":
		if addr, err := netip.ParseAddr(u.Hostname()); !strings.EqualFold(u.Hostname(), "localhost") && (err != nil || !addr.IsLoopback()) {
			return "", false
		}
	default:
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

// send makes one request, bounded by timeout from now. The caller closes the body of the response and calls cancel, which ends the
// bound, when it is done with the body.
func (c *Client) send(ctx context.Context, timeout time.Duration, method, path, leaseID string, body []byte) (*http.Response, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+apiPath+path, reader)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token.Reveal())
	if leaseID != "" {
		req.Header.Set(leaseHeader, leaseID)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, cancel, nil
}

// statusError names a status that is not the answer the call wanted. It carries the status and never the body, which is the service's
// to word and may echo the request.
func statusError(op string, resp *http.Response) error {
	var sentinel error
	switch resp.StatusCode {
	case http.StatusBadRequest:
		sentinel = ErrInvalid
	case http.StatusUnauthorized:
		sentinel = ErrUnauthorized
	case http.StatusNotFound:
		sentinel = ErrNotFound
	case http.StatusConflict:
		sentinel = ErrLeaseLost
	case http.StatusRequestEntityTooLarge:
		sentinel = ErrTooLarge
	case http.StatusBadGateway:
		sentinel = ErrGitHub
	case http.StatusServiceUnavailable:
		sentinel = ErrUnavailable
	default:
		sentinel = ErrServer
	}
	return fmt.Errorf("%s: %w (status %d)", op, sentinel, resp.StatusCode)
}

// decode reads a JSON answer of at most limit bytes.
func decode(op string, r io.Reader, limit int64, into any) error {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	switch {
	case err != nil:
		return fmt.Errorf("%s: read the answer: %w", op, err)
	case int64(len(data)) > limit:
		return fmt.Errorf("%s: %w: the answer is over %d bytes", op, ErrTooLarge, limit)
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("%s: %w: %w", op, ErrBadAnswer, err)
	}
	return nil
}

// Lease asks for a job. It returns ErrNoJob when none is ready, and ErrGitHub when the service could not read GitHub for the job it
// would have granted.
func (c *Client) Lease(ctx context.Context) (*Lease, error) {
	resp, cancel, err := c.send(ctx, c.callTimeout, http.MethodPost, "/lease", "", nil)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent:
		return nil, ErrNoJob
	default:
		return nil, statusError("lease", resp)
	}
	var job Job
	if err := decode("lease", resp.Body, maxLeaseBytes, &job); err != nil {
		return nil, err
	}
	if err := job.validate(); err != nil {
		return nil, fmt.Errorf("lease: %w: %w", ErrBadAnswer, err)
	}
	return &Lease{Job: job, c: c}, nil
}

// Lease is a job and the lease on it: every call under the job carries the lease.
type Lease struct {
	Job
	c *Client
}

func (l *Lease) path(suffix string) string { return "/jobs/" + strconv.FormatInt(l.JobID, 10) + suffix }

// Heartbeat extends the lease. ErrLeaseLost means the lease is gone; ErrNotFound, that the job is.
func (l *Lease) Heartbeat(ctx context.Context) error {
	resp, cancel, err := l.c.send(ctx, l.c.callTimeout, http.MethodPost, l.path("/heartbeat"), l.LeaseID, nil)
	if err != nil {
		return err
	}
	defer cancel()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return statusError("heartbeat", resp)
	}
	return nil
}

// Result posts the result body and returns the state of the job that the service answered: StateComplete, StateUnavailable, or
// StatePending when the attempt is queued again. It posts once, and does not retry.
func (l *Lease) Result(ctx context.Context, body []byte) (string, error) {
	resp, cancel, err := l.c.send(ctx, l.c.callTimeout, http.MethodPost, l.path("/result"), l.LeaseID, body)
	if err != nil {
		return "", err
	}
	defer cancel()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", statusError("result", resp)
	}
	var answer struct {
		State string `json:"state"`
	}
	if err := decode("result", resp.Body, maxAnswerBytes, &answer); err != nil {
		return "", err
	}
	switch answer.State {
	case StateComplete, StateUnavailable, StatePending:
		return answer.State, nil
	default:
		return "", fmt.Errorf("result: %w: the state is none of complete, unavailable and pending", ErrBadAnswer)
	}
}

// Archive opens the tar.gz of the head or the base commit. The caller closes it; a download is bounded in time, and a stream that
// stops short ends with the error of its body.
func (l *Lease) Archive(ctx context.Context, side string) (io.ReadCloser, error) {
	if side != "head" && side != "base" {
		return nil, fmt.Errorf("archive: %q is neither the head nor the base", side)
	}
	return l.download(ctx, "archive "+side, l.path("/archive/"+side))
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// File opens the manifest file with the digest (as manifest.files names it). The caller closes it.
func (l *Lease) File(ctx context.Context, digest string) (io.ReadCloser, error) {
	if !digestPattern.MatchString(digest) {
		return nil, fmt.Errorf("file: %q is not a sha256 digest", digest)
	}
	return l.download(ctx, "file", l.path("/files/"+url.PathEscape(digest)))
}

// download opens a stream. The bound of the request runs until the caller closes the stream, so a stalled stream ends.
func (l *Lease) download(ctx context.Context, op, path string) (io.ReadCloser, error) {
	resp, cancel, err := l.c.send(ctx, l.c.downloadTimeout, http.MethodGet, path, l.LeaseID, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		err := statusError(op, resp)
		_ = resp.Body.Close()
		cancel()
		return nil, err
	}
	return &stream{ReadCloser: resp.Body, cancel: cancel}, nil
}

type stream struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (s *stream) Close() error {
	err := s.ReadCloser.Close()
	s.cancel()
	return err
}
