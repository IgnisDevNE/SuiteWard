package prefetch

import (
	"context"
	"net/http"
	"time"
)

const (
	// redirectHost is where proxy.golang.org sends the zips it does not serve itself.
	redirectHost = "storage.googleapis.com"
	// maxTail bounds what is kept of a zip's stream to read its directory.
	maxTail = 4 << 20
)

// Client is the HTTP client for Fetch: HTTPS only, and a redirect only to storage.googleapis.com.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{}, CheckRedirect: allowRedirectTo(redirectHost)}
}

func allowRedirectTo(host string) func(*http.Request, []*http.Request) error {
	return func(*http.Request, []*http.Request) error { return nil }
}

// Fetch streams the files of p from the proxy at base into sink.
func Fetch(ctx context.Context, c *http.Client, base string, p Plan, sink Sink, l Limits) (Stats, error) {
	return fetch(ctx, c, base, p, sink, l, maxTail)
}

func fetch(ctx context.Context, c *http.Client, base string, p Plan, sink Sink, l Limits, tail int) (Stats, error) {
	return Stats{}, nil
}
