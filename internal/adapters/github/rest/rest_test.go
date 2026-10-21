package rest_test

import (
	"context"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

var t0 = time.Unix(1_800_000_000, 0)

// clock is the budget's injected time; tests move it instead of sleeping.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// tokens is a fake token source; rotate swaps the value and the identity.
type tokens struct {
	mu              sync.Mutex
	value, identity string
	calls           int
}

func (s *tokens) Token(context.Context) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.value, s.identity, nil
}

func (s *tokens) rotate(value, identity string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value, s.identity = value, identity
}

const (
	tokenValue   = "fake-token-value-0001"
	rotatedValue = "fake-token-value-0002"
)

func newTokens() *tokens { return &tokens{value: tokenValue, identity: "identity-1"} }

func newBudget(t *testing.T, fraction float64, c *clock) *rest.Budget {
	t.Helper()
	b, err := rest.NewBudget(fraction, c.Now)
	if err != nil {
		t.Fatalf("NewBudget: %v", err)
	}
	return b
}

func newClient(t *testing.T, tok rest.TokenFunc, baseURL string, b *rest.Budget) *rest.Client {
	t.Helper()
	c, err := rest.NewClient(tok, http.DefaultClient, baseURL, b)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// setRateLimit writes GitHub's rate-limit headers and a Date of t0: the server's
// clock agrees with the injected one unless a test says otherwise.
func setRateLimit(w http.ResponseWriter, limit, remaining int, reset time.Time) {
	w.Header().Set("Date", t0.UTC().Format(http.TimeFormat))
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
}

func TestNewClientRejectsInvalidArguments(t *testing.T) {
	tok := newTokens().Token
	b := newBudget(t, 0.5, &clock{now: t0})
	cases := []struct {
		name    string
		token   rest.TokenFunc
		http    *http.Client
		baseURL string
		budget  *rest.Budget
	}{
		{"nil token", nil, http.DefaultClient, "https://api.github.com", b},
		{"nil http client", tok, nil, "https://api.github.com", b},
		{"nil budget", tok, http.DefaultClient, "https://api.github.com", nil},
		{"empty base URL", tok, http.DefaultClient, "", b},
		{"relative base URL", tok, http.DefaultClient, "/api/v3", b},
		{"base URL without scheme", tok, http.DefaultClient, "api.github.com", b},
		{"base URL without host", tok, http.DefaultClient, "https://", b},
		{"base URL with a query", tok, http.DefaultClient, "https://api.github.com?x=1", b},
		{"base URL with an empty query", tok, http.DefaultClient, "https://api.github.com?", b},
		{"base URL with a fragment", tok, http.DefaultClient, "https://api.github.com#frag", b},
		{"base URL with an empty fragment", tok, http.DefaultClient, "https://api.github.com#", b},
		{"base URL with user info", tok, http.DefaultClient, "https://user:secret@api.github.com", b},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := rest.NewClient(tc.token, tc.http, tc.baseURL, tc.budget); err == nil {
				t.Fatal("NewClient accepted invalid arguments")
			}
		})
	}
	if _, err := rest.NewClient(tok, http.DefaultClient, "https://github.example.com/api/v3", b); err != nil {
		t.Fatalf("NewClient rejected valid arguments: %v", err)
	}
}

// The base URL can carry credentials; no NewClient error may print any part of it.
func TestNewClientErrorsDoNotPrintTheBaseURL(t *testing.T) {
	tok := newTokens().Token
	b := newBudget(t, 0.5, &clock{now: t0})
	for _, baseURL := range []string{
		"user:s3cr3t@api.github.com",
		"/s3cr3t/api",
		"https://",
		"https://user:s3cr3t@api.github.com",
		"https://api.github.com?token=s3cr3t",
		"https://api.github.com#s3cr3t",
		"https://api.github.com/%zz-s3cr3t",
	} {
		_, err := rest.NewClient(tok, http.DefaultClient, baseURL, b)
		if err == nil {
			t.Errorf("NewClient accepted %q", baseURL)
			continue
		}
		if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "api.github.com") {
			t.Errorf("NewClient(%q) error prints the base URL: %v", baseURL, err)
		}
	}
}

func TestNewBudgetRejectsInvalidArguments(t *testing.T) {
	c := &clock{now: t0}
	cases := []struct {
		name     string
		fraction float64
		clock    func() time.Time
	}{
		{"zero fraction", 0, c.Now},
		{"negative fraction", -0.5, c.Now},
		{"fraction above one", 1.01, c.Now},
		{"NaN fraction", math.NaN(), c.Now},
		{"infinite fraction", math.Inf(1), c.Now},
		{"nil clock", 0.5, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := rest.NewBudget(tc.fraction, tc.clock); err == nil {
				t.Fatal("NewBudget accepted invalid arguments")
			}
		})
	}
	for _, fraction := range []float64{0.5, 1} {
		if _, err := rest.NewBudget(fraction, c.Now); err != nil {
			t.Fatalf("NewBudget(%v) rejected a valid fraction: %v", fraction, err)
		}
	}
}

func TestGetSendsGitHubHeadersAndIfNoneMatch(t *testing.T) {
	var mu sync.Mutex
	var seen []http.Header
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("ETag", `"etag-1"`)
		_, _ = w.Write([]byte(`[{"number":1}]`))
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL+"/api/v3", newBudget(t, 1, &clock{now: t0}))

	const path = "/repos/o/r/pulls?state=open&per_page=100"
	first, err := c.Get(context.Background(), path)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if first.Status != http.StatusOK || first.NotModified || string(first.Body) != `[{"number":1}]` || first.ETag != `"etag-1"` {
		t.Fatalf("first response = %+v", first)
	}
	if _, err := c.Get(context.Background(), path); err != nil {
		t.Fatalf("second Get: %v", err)
	}

	if len(seen) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(seen))
	}
	for i, h := range seen {
		if got := h.Get("Authorization"); got != "Bearer "+tokenValue {
			t.Errorf("request %d: Authorization does not carry the bearer token", i)
		}
		if got := h.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("request %d: Accept = %q", i, got)
		}
		if got := h.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("request %d: X-GitHub-Api-Version = %q", i, got)
		}
		if paths[i] != "/api/v3"+path {
			t.Errorf("request %d: URI = %q, want %q", i, paths[i], "/api/v3"+path)
		}
	}
	if _, ok := seen[0]["If-None-Match"]; ok {
		t.Error("first request sent If-None-Match without a cached ETag")
	}
	if got := seen[1].Get("If-None-Match"); got != `"etag-1"` {
		t.Errorf("second request If-None-Match = %q, want the cached ETag", got)
	}
}

func TestGetServesCachedBodyOn304(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("ETag", `"etag-1"`)
		if r.Header.Get("If-None-Match") == `"etag-1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte(`{"sha":"abc"}`))
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))

	if _, err := c.Get(context.Background(), "/repos/o/r/git/trees/abc"); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	got, err := c.Get(context.Background(), "/repos/o/r/git/trees/abc")
	if err != nil {
		t.Fatalf("conditional Get: %v", err)
	}
	if !got.NotModified || got.Status != http.StatusNotModified || string(got.Body) != `{"sha":"abc"}` || got.ETag != `"etag-1"` {
		t.Fatalf("304 response = %+v, want the cached body with NotModified", got)
	}
	if hits.Load() != 2 {
		t.Fatalf("server saw %d requests, want 2", hits.Load())
	}
}

func TestGetRejects304WithoutCachedBody(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))

	got, err := c.Get(context.Background(), "/repos/o/r/pulls")
	if err == nil {
		t.Fatalf("Get returned %+v for a 304 with nothing cached", got)
	}
	if hits.Load() != 1 {
		t.Fatalf("server saw %d requests, want 1", hits.Load())
	}
}

// S1: GitHub answers Vary: Authorization, so an ETag does not survive a token
// rotation; the whole cache is dropped when the identity changes.
func TestTokenRotationDropsETagCache(t *testing.T) {
	var mu sync.Mutex
	var inm, auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inm = append(inm, r.Header.Get("If-None-Match"))
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("ETag", `"`+r.URL.Path+`"`)
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	tok := newTokens()
	c := newClient(t, tok.Token, srv.URL, newBudget(t, 1, &clock{now: t0}))
	get := func(path string) {
		t.Helper()
		if _, err := c.Get(context.Background(), path); err != nil {
			t.Fatalf("Get %s: %v", path, err)
		}
	}

	get("/a")
	get("/b")
	tok.rotate(rotatedValue, "identity-2")
	get("/a")
	get("/a")
	tok.rotate(tokenValue, "identity-1")
	get("/b")

	want := []string{"", "", "", `"/a"`, ""}
	if fmt.Sprint(inm) != fmt.Sprint(want) {
		t.Fatalf("If-None-Match per request = %q, want %q", inm, want)
	}
	if auth[2] != "Bearer "+rotatedValue {
		t.Fatal("the request after the rotation did not use the new token")
	}
}

// rateLimitedServer counts every request against a window of limit requests;
// a 304 does not count, as S1 measured.
type rateLimitedServer struct {
	mu    sync.Mutex
	limit int
	used  int
	reset time.Time
	hits  int
	date  func() time.Time // the server's clock for Date; nil means t0
}

func (s *rateLimitedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits++
	w.Header().Set("ETag", `"`+r.URL.Path+`"`)
	if r.Header.Get("If-None-Match") == `"`+r.URL.Path+`"` {
		s.rateLimit(w)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	s.used++
	s.rateLimit(w)
	_, _ = w.Write([]byte("body"))
}

func (s *rateLimitedServer) rateLimit(w http.ResponseWriter) {
	setRateLimit(w, s.limit, s.limit-s.used, s.reset)
	if s.date != nil {
		w.Header().Set("Date", s.date().UTC().Format(http.TimeFormat))
	}
}

func (s *rateLimitedServer) newWindow(reset time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used, s.reset = 0, reset
}

func (s *rateLimitedServer) Hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func TestBudgetAllowsFractionThenRefusesUntilReset(t *testing.T) {
	gh := &rateLimitedServer{limit: 10, reset: t0.Add(time.Hour)}
	srv := httptest.NewServer(gh)
	defer srv.Close()
	clk := &clock{now: t0}
	tok := newTokens()
	c := newClient(t, tok.Token, srv.URL, newBudget(t, 0.5, clk))

	for i := range 5 {
		if _, err := c.Get(context.Background(), fmt.Sprintf("/p/%d", i)); err != nil {
			t.Fatalf("Get %d within the budget: %v", i, err)
		}
	}
	refused := func() {
		t.Helper()
		_, err := c.Get(context.Background(), "/p/next")
		if !errors.Is(err, rest.ErrBudgetExhausted) {
			t.Fatalf("Get over the budget: err = %v, want ErrBudgetExhausted", err)
		}
		var be *rest.BudgetExhaustedError
		if !errors.As(err, &be) || !be.Reset.Equal(t0.Add(time.Hour)) {
			t.Fatalf("budget error does not carry the reset time %v: %#v", t0.Add(time.Hour), err)
		}
	}
	refused()
	clk.Set(t0.Add(time.Hour - time.Second))
	refused()
	if gh.Hits() != 5 {
		t.Fatalf("server saw %d requests, want 5: a refused Get must not call GitHub", gh.Hits())
	}

	clk.Set(t0.Add(time.Hour))
	gh.newWindow(t0.Add(2 * time.Hour))
	if _, err := c.Get(context.Background(), "/p/next"); err != nil {
		t.Fatalf("Get after the reset: %v", err)
	}
	if gh.Hits() != 6 {
		t.Fatalf("server saw %d requests, want 6", gh.Hits())
	}
}

func TestBudgetDoesNotCount304(t *testing.T) {
	gh := &rateLimitedServer{limit: 4, reset: t0.Add(time.Hour)}
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.5, &clock{now: t0}))

	for i := range 11 {
		got, err := c.Get(context.Background(), "/a")
		if err != nil {
			t.Fatalf("Get /a #%d: %v", i, err)
		}
		if (i > 0) != got.NotModified {
			t.Fatalf("Get /a #%d NotModified = %v", i, got.NotModified)
		}
	}
	if _, err := c.Get(context.Background(), "/b"); err != nil {
		t.Fatalf("second counted request: %v", err)
	}
	if _, err := c.Get(context.Background(), "/c"); !errors.Is(err, rest.ErrBudgetExhausted) {
		t.Fatalf("third counted request: err = %v, want ErrBudgetExhausted", err)
	}
	if gh.Hits() != 12 {
		t.Fatalf("server saw %d requests, want 12", gh.Hits())
	}
}

func TestBudgetIsSharedByClients(t *testing.T) {
	gh := &rateLimitedServer{limit: 20, reset: t0.Add(time.Hour)}
	srv := httptest.NewServer(gh)
	defer srv.Close()
	b := newBudget(t, 0.5, &clock{now: t0})
	first := newClient(t, newTokens().Token, srv.URL, b)
	second := newClient(t, newTokens().Token, srv.URL, b)

	// The budget learns the limit from the first response.
	if _, err := first.Get(context.Background(), "/warm"); err != nil {
		t.Fatalf("warm-up Get: %v", err)
	}
	var ok, exhausted atomic.Int32
	var wg sync.WaitGroup
	for i := range 40 {
		c := first
		if i%2 == 1 {
			c = second
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Get(context.Background(), fmt.Sprintf("/p/%d", i))
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, rest.ErrBudgetExhausted):
				exhausted.Add(1)
			default:
				t.Errorf("Get /p/%d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	if hits := gh.Hits(); hits > 10 || int(ok.Load()) != hits-1 {
		t.Fatalf("server saw %d requests and %d Gets succeeded; want at most 10 requests in total", hits, ok.Load())
	}
	if ok.Load()+exhausted.Load() != 40 || exhausted.Load() == 0 {
		t.Fatalf("succeeded %d, exhausted %d of 40", ok.Load(), exhausted.Load())
	}
	// A reservation can overlap a response that already counts it, so the burst may stop short of the 10th request;
	// sequential Gets then use the rest and are refused before the server sees an 11th.
	for n := 0; ; n++ {
		_, err := second.Get(context.Background(), fmt.Sprintf("/after/%d", n))
		if errors.Is(err, rest.ErrBudgetExhausted) {
			break
		}
		if err != nil || gh.Hits() > 10 {
			t.Fatalf("Get /after/%d once the burst ended: err = %v, server saw %d requests; want ErrBudgetExhausted within 10", n, err, gh.Hits())
		}
	}
	if hits := gh.Hits(); hits != 10 {
		t.Fatalf("server saw %d requests before the shared budget refused; want 10", hits)
	}
}

// GitHub's clock is two minutes behind the local one: every Until is local time,
// whether it comes from X-RateLimit-Reset, an HTTP-date Retry-After (both in
// GitHub's time) or a Retry-After in seconds (relative to now).
func TestGetReturnsTypedErrors(t *testing.T) {
	const skew = 2 * time.Minute
	reset := t0.Add(30 * time.Minute)
	cases := []struct {
		name       string
		status     int
		remaining  string
		retryAfter string
		want       error // nil: a *StatusError matching no sentinel
		until      time.Time
	}{
		{name: "401", status: 401, remaining: "4000", want: rest.ErrUnauthorized},
		{name: "403 without rate-limit signals", status: 403, remaining: "4000", want: rest.ErrForbidden},
		{name: "404", status: 404, remaining: "4000", want: rest.ErrNotFound},
		{name: "422", status: 422, remaining: "4000", want: rest.ErrUnprocessable},
		{name: "403 with remaining 0", status: 403, remaining: "0", want: rest.ErrRateLimited, until: reset.Add(skew)},
		{name: "403 with Retry-After", status: 403, remaining: "4000", retryAfter: "60", want: rest.ErrRateLimited, until: t0.Add(time.Minute)},
		{name: "429 with Retry-After", status: 429, retryAfter: "30", want: rest.ErrRateLimited, until: t0.Add(30 * time.Second)},
		{name: "429 with a Retry-After past an hour", status: 429, retryAfter: "9223372036854775807", want: rest.ErrRateLimited, until: t0.Add(time.Hour)},
		{name: "429 with remaining 0", status: 429, remaining: "0", want: rest.ErrRateLimited, until: reset.Add(skew)},
		{name: "429 with an HTTP-date Retry-After", status: 429, retryAfter: t0.Add(-skew + 90*time.Second).UTC().Format(http.TimeFormat), want: rest.ErrRateLimited, until: t0.Add(90 * time.Second)},
		{name: "429 without signals", status: 429, remaining: "4000"},
		{name: "500", status: 500, remaining: "4000"},
		{name: "409", status: 409},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Date", t0.Add(-skew).UTC().Format(http.TimeFormat))
				if tc.remaining != "" {
					w.Header().Set("X-RateLimit-Limit", "5000")
					w.Header().Set("X-RateLimit-Remaining", tc.remaining)
					w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
				}
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"refused"}`))
			}))
			defer srv.Close()
			c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))

			_, err := c.Get(context.Background(), "/repos/o/r")
			if err == nil || hits.Load() != 1 {
				t.Fatalf("err = %v after %d requests; want an error after one request", err, hits.Load())
			}
			assertTypedError(t, err, tc.status, tc.want, tc.until)
		})
	}
}

// assertTypedError checks that err matches only the sentinel want (nil: none) and carries status; a rate limit also carries until.
func assertTypedError(t *testing.T, err error, status int, want error, until time.Time) {
	t.Helper()
	for _, s := range []error{rest.ErrUnauthorized, rest.ErrForbidden, rest.ErrNotFound, rest.ErrUnprocessable, rest.ErrRateLimited, rest.ErrBudgetExhausted} {
		if got := errors.Is(err, s); got != (s == want) {
			t.Errorf("errors.Is(err, %v) = %v", s, got)
		}
	}
	if want == rest.ErrRateLimited {
		var rl *rest.RateLimitedError
		if !errors.As(err, &rl) || rl.Status != status || !rl.Until.Equal(until) {
			t.Fatalf("rate-limit error = %#v, want status %d until %v", err, status, until)
		}
		return
	}
	var se *rest.StatusError
	if !errors.As(err, &se) || se.Status != status {
		t.Fatalf("status error = %#v, want status %d", err, status)
	}
}

// GitHub's answer text can quote repository text, so no error of a read carries it, whatever the status.
func TestGetErrorsNeverCarryTheAnswerText(t *testing.T) {
	for _, tc := range []struct {
		status int
		header map[string]string
	}{
		{401, nil}, {403, nil}, {404, nil}, {422, nil}, {500, nil},
		{403, map[string]string{"Retry-After": "30"}}, {429, map[string]string{"X-RateLimit-Remaining": "0"}},
	} {
		t.Run(strconv.Itoa(tc.status)+fmt.Sprint(tc.header), func(t *testing.T) {
			client, _ := newPoster(t, tc.status, tc.header, `{"message":"`+secretAnswer+" "+tokenValue+`"}`)

			_, err := client.Get(t.Context(), "/repos/o/r")
			if err == nil {
				t.Fatal("Get succeeded")
			}
			if strings.Contains(err.Error(), secretAnswer) || strings.Contains(fmt.Sprintf("%#v", errors.Unwrap(err)), secretAnswer) {
				t.Fatalf("error %q carries the answer text", err)
			}
		})
	}
}

// A server that echoes the request headers must not get the token into an error.
func TestErrorsNeverContainTheToken(t *testing.T) {
	for _, status := range []int{304, 401, 403, 404, 422, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if status == 429 {
					w.Header().Set("Retry-After", "1")
				}
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, html.EscapeString(fmt.Sprintf("echo %v %s", r.Header, r.Header.Get("Authorization"))))
			}))
			defer srv.Close()
			c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))

			_, err := c.Get(context.Background(), "/repos/o/r")
			if err == nil || hits.Load() != 1 {
				t.Fatalf("err = %v after %d requests; want an error after one request", err, hits.Load())
			}
			if strings.Contains(err.Error(), tokenValue) || strings.Contains(fmt.Sprintf("%#v", err), tokenValue) {
				t.Fatal("error contains the token value")
			}
		})
	}
	t.Run("transport error", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		srv.Close()
		tok := newTokens()
		c := newClient(t, tok.Token, srv.URL, newBudget(t, 1, &clock{now: t0}))
		_, err := c.Get(context.Background(), "/repos/o/r")
		if err == nil || tok.calls != 1 {
			t.Fatalf("err = %v after %d token calls; want a transport error", err, tok.calls)
		}
		if strings.Contains(err.Error(), tokenValue) {
			t.Fatal("error contains the token value")
		}
	})
}

func TestTokenSourceErrorIsWrapped(t *testing.T) {
	errSource := errors.New("token source down")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	failing := func(context.Context) (string, string, error) { return "", "", errSource }
	c := newClient(t, failing, srv.URL, newBudget(t, 1, &clock{now: t0}))

	if _, err := c.Get(context.Background(), "/repos/o/r"); !errors.Is(err, errSource) {
		t.Fatalf("err = %v, want the token source error wrapped", err)
	}
	if hits.Load() != 0 {
		t.Fatal("Get called GitHub without a token")
	}
}

// The path is joined to the base URL; one without a leading slash could change
// the host that receives the token.
func TestGetRejectsPathWithoutLeadingSlash(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))

	if _, err := c.Get(context.Background(), "/repos/o/r"); err != nil {
		t.Fatalf("valid path: %v", err)
	}
	for _, path := range []string{"", "repos/o/r", ".evil.example/x", "@evil.example/x"} {
		if _, err := c.Get(context.Background(), path); err == nil {
			t.Errorf("Get(%q) accepted a path without a leading slash", path)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("server saw %d requests, want 1", hits.Load())
	}
}

// The window ends in the server's time: each response's Date gives the offset
// between the local clock and GitHub's, so a host whose clock runs ahead keeps
// the cap until GitHub's reset, and the error reports the reset in local time.
func TestBudgetHoldsUntilTheServersResetWhenTheLocalClockRunsAhead(t *testing.T) {
	const skew = 2 * time.Minute
	reset := t0.Add(time.Hour)
	clk := &clock{now: t0.Add(skew)}
	gh := &rateLimitedServer{limit: 10, reset: reset, date: func() time.Time { return clk.Now().Add(-skew) }}
	srv := httptest.NewServer(gh)
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.5, clk))

	for i := range 5 {
		if _, err := c.Get(context.Background(), fmt.Sprintf("/p/%d", i)); err != nil {
			t.Fatalf("Get %d within the budget: %v", i, err)
		}
	}
	refused := func() {
		t.Helper()
		_, err := c.Get(context.Background(), "/p/next")
		var be *rest.BudgetExhaustedError
		if !errors.As(err, &be) || !be.Reset.Equal(reset.Add(skew)) {
			t.Fatalf("err = %#v, want ErrBudgetExhausted until the reset in local time %v", err, reset.Add(skew))
		}
	}
	refused()
	clk.Set(reset) // past the reset on the local clock, before it on GitHub's
	refused()
	clk.Set(reset.Add(skew - time.Second))
	refused()
	if gh.Hits() != 5 {
		t.Fatalf("server saw %d requests, want 5", gh.Hits())
	}

	clk.Set(reset.Add(skew)) // the error's Reset: a caller that waits until it is let through
	gh.newWindow(reset.Add(time.Hour))
	if _, err := c.Get(context.Background(), "/p/after"); err != nil {
		t.Fatalf("Get at the reported reset: %v", err)
	}
}

// A request answered just after the reset can carry the old reset with nothing
// remaining; the budget must not stop for good because of it.
func TestBudgetDoesNotStopForGoodOnALateAnswerFromTheOldWindow(t *testing.T) {
	reset := t0.Add(time.Hour)
	clk := &clock{now: t0}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch hits.Add(1) {
		case 1:
			setRateLimit(w, 10, 9, reset)
		case 2: // sent inside the window, answered after it with the old reset
			clk.Set(reset.Add(time.Second))
			setRateLimit(w, 10, 0, reset)
			w.Header().Set("Date", clk.Now().UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusForbidden)
		default:
			setRateLimit(w, 10, 10, reset.Add(time.Hour))
			w.Header().Set("Date", clk.Now().UTC().Format(http.TimeFormat))
		}
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.5, clk))
	ctx := context.Background()

	if _, err := c.Get(ctx, "/a"); err != nil {
		t.Fatalf("Get /a: %v", err)
	}
	if _, err := c.Get(ctx, "/b"); !errors.Is(err, rest.ErrRateLimited) {
		t.Fatalf("Get /b: err = %v, want ErrRateLimited", err)
	}
	if _, err := c.Get(ctx, "/c"); err != nil {
		t.Fatalf("Get /c after the reset: %v (the budget stopped for good)", err)
	}
}

// A response from an older window that arrives after the window was replaced
// must not roll it back.
func TestBudgetIgnoresALaggingResponseFromAnOlderWindow(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch hits.Add(1) {
		case 1:
			setRateLimit(w, 10, 9, t0.Add(time.Hour))
		case 2:
			setRateLimit(w, 10, 9, t0.Add(2*time.Hour))
		case 3: // the lagging answer of the first window, nothing remaining
			setRateLimit(w, 10, 0, t0.Add(time.Hour))
		default:
			setRateLimit(w, 10, 8, t0.Add(2*time.Hour))
		}
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.5, &clock{now: t0}))

	for i := range 4 {
		if _, err := c.Get(context.Background(), fmt.Sprintf("/p/%d", i)); err != nil {
			t.Fatalf("Get %d: %v (a lagging answer rolled the window back)", i, err)
		}
	}
}

func TestGetImmutableBypassesTheETagCache(t *testing.T) {
	var mu sync.Mutex
	var inm []string
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		inm = append(inm, r.Header.Get("If-None-Match"))
		if r.Header.Get("If-None-Match") == `"etag-1"` {
			w.Header().Set("ETag", `"etag-1"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		n++
		w.Header().Set("ETag", fmt.Sprintf(`"etag-%d"`, n))
		_, _ = fmt.Fprintf(w, "body-%d", n)
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))
	ctx := context.Background()

	if got, err := c.Get(ctx, "/p"); err != nil || string(got.Body) != "body-1" {
		t.Fatalf("Get: %+v, %v", got, err)
	}
	got, err := c.GetImmutable(ctx, "/p")
	if err != nil || got.Status != http.StatusOK || got.NotModified || string(got.Body) != "body-2" || got.ETag != `"etag-2"` {
		t.Fatalf("GetImmutable = %+v, %v; want a fresh 200 with the new body", got, err)
	}
	if got, err := c.GetImmutable(ctx, "/fresh"); err != nil || string(got.Body) != "body-3" {
		t.Fatalf("GetImmutable on a new path: %+v, %v", got, err)
	}
	// The cache holds what Get stored, untouched by GetImmutable, and nothing for /fresh.
	if got, err := c.Get(ctx, "/p"); err != nil || !got.NotModified || string(got.Body) != "body-1" {
		t.Fatalf("Get after GetImmutable = %+v, %v; want the 304 answered from the original cache entry", got, err)
	}
	if _, err := c.Get(ctx, "/fresh"); err != nil {
		t.Fatalf("Get /fresh: %v", err)
	}
	want := []string{"", "", "", `"etag-1"`, ""}
	if fmt.Sprint(inm) != fmt.Sprint(want) {
		t.Fatalf("If-None-Match per request = %q, want %q", inm, want)
	}
}

func TestGetImmutableTreats304AsAnError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("ETag", `"etag-1"`)
			_, _ = w.Write([]byte("cached"))
			return
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))

	if _, err := c.Get(context.Background(), "/p"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got, err := c.GetImmutable(context.Background(), "/p"); err == nil {
		t.Fatalf("GetImmutable returned %+v for a 304", got)
	}
}

func TestGetImmutableSharesHeadersBudgetAndTypedErrors(t *testing.T) {
	var mu sync.Mutex
	var seen []http.Header
	gh := &rateLimitedServer{limit: 4, reset: t0.Add(time.Hour)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gh.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.5, &clock{now: t0}))
	ctx := context.Background()

	if _, err := c.GetImmutable(ctx, "/missing"); !errors.Is(err, rest.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	for _, path := range []string{"/a", "/b"} {
		if _, err := c.GetImmutable(ctx, path); err != nil {
			t.Fatalf("GetImmutable %s: %v", path, err)
		}
	}
	if _, err := c.GetImmutable(ctx, "/c"); !errors.Is(err, rest.ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted", err)
	}
	if _, err := c.Get(ctx, "/d"); !errors.Is(err, rest.ErrBudgetExhausted) {
		t.Fatalf("Get after GetImmutable spent the shared budget: err = %v", err)
	}
	if _, err := c.GetImmutable(ctx, "relative"); err == nil {
		t.Fatal("GetImmutable accepted a path without a leading slash")
	}
	for i, h := range seen {
		if h.Get("Authorization") != "Bearer "+tokenValue || h.Get("Accept") != "application/vnd.github+json" || h.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("request %d headers = %v", i, h)
		}
	}
}

func TestBodiesOverTheLimitAreAnErrorNotTruncated(t *testing.T) {
	const limit = 8
	rest.LimitBodiesTo(t, limit)
	for _, tc := range []struct {
		name   string
		status int
		body   int
		ok     bool
	}{
		{"2xx at the limit", 200, limit, true},
		{"2xx over the limit", 200, limit + 1, false},
		{"error status over the limit", 500, limit + 1, false},
		{"error status at the limit", 500, limit, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(strings.Repeat("x", tc.body)))
			}))
			defer srv.Close()
			c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))
			for name, get := range map[string]func(context.Context, string) (rest.Response, error){"Get": c.Get, "GetImmutable": c.GetImmutable} {
				got, err := get(context.Background(), "/p")
				var se *rest.StatusError
				switch {
				case tc.ok && tc.status == 200:
					if err != nil || len(got.Body) != limit {
						t.Errorf("%s: %+v, %v; want the full body", name, got, err)
					}
				case tc.ok:
					if !errors.As(err, &se) {
						t.Errorf("%s: err = %v, want a StatusError", name, err)
					}
				default:
					if err == nil || errors.As(err, &se) || !strings.Contains(err.Error(), "8 bytes") {
						t.Errorf("%s: err = %v, want an error naming the 8-byte limit", name, err)
					}
				}
			}
		})
	}
}

// A body that never ends must stop at the limit instead of filling memory.
func TestStreamingBodyStopsAtTheLimit(t *testing.T) {
	rest.LimitBodiesTo(t, 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := []byte(strings.Repeat("x", 512))
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.Get(ctx, "/p"); err == nil || !strings.Contains(err.Error(), "1024 bytes") {
		t.Fatalf("err = %v, want an error naming the 1024-byte limit", err)
	}
}

func TestReturnedBodiesAreCopies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"etag-1"`)
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("original"))
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))
	ctx := context.Background()

	first, err := c.Get(ctx, "/p")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	first.Body[0] = 'X' // the 200 body must not alias the cache
	second, err := c.Get(ctx, "/p")
	if err != nil || string(second.Body) != "original" {
		t.Fatalf("304 after mutating the 200 body = %q, %v", second.Body, err)
	}
	second.Body[0] = 'Y' // the 304 body must not alias the cache either
	third, err := c.Get(ctx, "/p")
	if err != nil || string(third.Body) != "original" {
		t.Fatalf("304 after mutating a 304 body = %q, %v", third.Body, err)
	}
}

func TestRetryAfterAsHTTPDate(t *testing.T) {
	until := t0.Add(90 * time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Date", t0.UTC().Format(http.TimeFormat))
		w.Header().Set("Retry-After", until.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))

	_, err := c.Get(context.Background(), "/p")
	var rl *rest.RateLimitedError
	if !errors.As(err, &rl) || !rl.Until.Equal(until) {
		t.Fatalf("err = %#v, want a RateLimitedError until %v", err, until)
	}
}

// The tests below pin behavior that already holds.

func TestResponseWithoutRateLimitHeadersLeavesTheBudgetUnchanged(t *testing.T) {
	var headerless atomic.Bool
	var remaining atomic.Int32
	remaining.Store(3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !headerless.Load() {
			setRateLimit(w, 4, int(remaining.Add(-1)+1), t0.Add(time.Hour))
		}
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.5, &clock{now: t0})) // cap of 2
	ctx := context.Background()

	if _, err := c.Get(ctx, "/learn"); err != nil { // reports 3 remaining: used 1
		t.Fatalf("Get: %v", err)
	}
	headerless.Store(true)
	for i := range 5 {
		if _, err := c.Get(ctx, fmt.Sprintf("/headerless/%d", i)); err != nil {
			t.Fatalf("headerless Get %d: %v", i, err)
		}
	}
	headerless.Store(false)
	if _, err := c.Get(ctx, "/second"); err != nil { // used 1 < 2; reports 2 remaining: used 2
		t.Fatalf("Get after headerless answers: %v", err)
	}
	if _, err := c.Get(ctx, "/third"); !errors.Is(err, rest.ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted at the same count as before", err)
	}
}

func TestCancelledContextReleasesItsReservation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		setRateLimit(w, 2, 2, t0.Add(time.Hour))
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.5, &clock{now: t0})) // cap of 1
	if _, err := c.Get(context.Background(), "/learn"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Get(ctx, "/cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := c.Get(context.Background(), "/after"); err != nil {
		t.Fatalf("Get after a cancelled one: %v (the reservation leaked)", err)
	}
}

func TestETagCacheEntryLifecycle(t *testing.T) {
	var mu sync.Mutex
	var inm []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inm = append(inm, r.Header.Get("If-None-Match"))
		step := len(inm)
		mu.Unlock()
		switch step {
		case 1, 4:
			w.Header().Set("ETag", `"etag-1"`)
		case 3:
			w.Header().Set("ETag", `"etag-error"`)
			w.WriteHeader(http.StatusInternalServerError)
		}
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))
	get := func() { _, _ = c.Get(context.Background(), "/p") }

	get() // 1: stores etag-1
	get() // 2: a 200 without an ETag removes the entry
	get() // 3: no entry; an error status with an ETag stores nothing
	get() // 4: no entry; stores etag-1
	get() // 5: the entry is back
	want := []string{"", `"etag-1"`, "", "", `"etag-1"`}
	if fmt.Sprint(inm) != fmt.Sprint(want) {
		t.Fatalf("If-None-Match per request = %q, want %q", inm, want)
	}
}

func TestConcurrentGetsAndTokenRotationAreRaceFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"`+r.URL.Path+`"`)
		setRateLimit(w, 1_000_000, 1_000_000, t0.Add(time.Hour))
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	tok := newTokens()
	c := newClient(t, tok.Token, srv.URL, newBudget(t, 1, &clock{now: t0}))

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 20 {
				if _, err := c.Get(context.Background(), fmt.Sprintf("/p/%d", (i+j)%4)); err != nil {
					t.Errorf("Get: %v", err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := range 40 {
			tok.rotate(tokenValue, fmt.Sprintf("identity-%d", j%3))
		}
	}()
	wg.Wait()
}

// One response with a wildly wrong Date (a stale intermediary) must not move the
// budget's window: GitHub's windows last at most an hour, so a Date further from
// the local clock than that is ignored.
func TestBudgetIgnoresADateFarFromTheLocalClock(t *testing.T) {
	reset := t0.Add(time.Hour)
	for _, tc := range []struct {
		name string
		date time.Duration // from the local clock
	}{
		{"stale intermediary, 5 hours behind", -5 * time.Hour},
		{"5 hours ahead", 5 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := &clock{now: t0}
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if hits.Add(1) == 1 {
					setRateLimit(w, 10, 5, reset) // used 5: at the cap of 5
					w.Header().Set("Date", t0.Add(tc.date).UTC().Format(http.TimeFormat))
				} else {
					setRateLimit(w, 10, 10, reset.Add(time.Hour))
					w.Header().Set("Date", clk.Now().UTC().Format(http.TimeFormat))
				}
				_, _ = w.Write([]byte("body"))
			}))
			defer srv.Close()
			c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.5, clk))
			ctx := context.Background()

			if _, err := c.Get(ctx, "/a"); err != nil {
				t.Fatalf("Get /a: %v", err)
			}
			_, err := c.Get(ctx, "/b")
			var be *rest.BudgetExhaustedError
			if !errors.As(err, &be) || !be.Reset.Equal(reset) {
				t.Fatalf("Get /b: err = %#v, want ErrBudgetExhausted until the reset %v", err, reset)
			}
			clk.Set(reset)
			if _, err := c.Get(ctx, "/b"); err != nil {
				t.Fatalf("Get /b right after the reset: %v", err)
			}
		})
	}
}

// A 304 costs nothing on GitHub's side: its headers neither change the count nor
// establish a window.
func TestBudgetIgnoresRateLimitHeadersOf304(t *testing.T) {
	reset := t0.Add(time.Hour)
	for _, tc := range []struct {
		name     string
		okLimits bool // the 200 carries rate-limit headers (a window is known)
	}{
		{"window known", true},
		{"no window yet", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"a"`)
				if r.Header.Get("If-None-Match") != "" {
					setRateLimit(w, 10, 0, reset) // a 304 claiming everything is used
					w.WriteHeader(http.StatusNotModified)
					return
				}
				if tc.okLimits {
					setRateLimit(w, 10, 9, reset)
				}
				_, _ = w.Write([]byte("body"))
			}))
			defer srv.Close()
			c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.5, &clock{now: t0}))
			ctx := context.Background()

			if _, err := c.Get(ctx, "/a"); err != nil {
				t.Fatalf("Get /a: %v", err)
			}
			if got, err := c.Get(ctx, "/a"); err != nil || !got.NotModified {
				t.Fatalf("conditional Get /a = %+v, %v", got, err)
			}
			if _, err := c.Get(ctx, "/b"); err != nil {
				t.Fatalf("Get /b after a 304: %v (the 304's headers were counted)", err)
			}
		})
	}
}

// A response without a usable Date says nothing about the clocks: the previous offset stays.
func TestBudgetKeepsTheOffsetWithoutAUsableDate(t *testing.T) {
	const skew = 2 * time.Minute
	reset := t0.Add(time.Hour)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(hits.Add(1))
		setRateLimit(w, 10, 10-n, reset) // used n
		switch n {
		case 1:
			w.Header().Set("Date", t0.Add(-skew).UTC().Format(http.TimeFormat))
		case 2:
			w.Header()["Date"] = nil // the server sends none
		default:
			w.Header().Set("Date", "not a date")
		}
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.5, &clock{now: t0}))

	for i := range 5 {
		if _, err := c.Get(context.Background(), fmt.Sprintf("/p/%d", i)); err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
	}
	_, err := c.Get(context.Background(), "/next")
	var be *rest.BudgetExhaustedError
	if !errors.As(err, &be) || !be.Reset.Equal(reset.Add(skew)) {
		t.Fatalf("err = %#v, want ErrBudgetExhausted until %v (the first Date's offset)", err, reset.Add(skew))
	}
}

// Each response carries the Date it was sent with, in UTC; a 304 carries its own, never the cached entry's.
func TestResponseCarriesTheDateOfItsOwnAnswer(t *testing.T) {
	var served atomic.Int32
	dates := []time.Time{t0, t0.Add(7 * time.Second)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", dates[served.Add(1)-1].UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", `"e1"`)
		if r.Header.Get("If-None-Match") == `"e1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.5, &clock{now: t0}))

	first, err := c.Get(context.Background(), "/a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Get(context.Background(), "/a")
	if err != nil {
		t.Fatal(err)
	}

	if !first.Date.Equal(dates[0]) || first.Date.Location() != time.UTC {
		t.Errorf("200 Date = %v, want %v in UTC", first.Date, dates[0])
	}
	if !second.NotModified || !second.Date.Equal(dates[1]) || second.Date.Location() != time.UTC {
		t.Errorf("304 = %+v, want its own Date %v in UTC, not the cached entry's", second, dates[1])
	}
}

// A Date that is missing or cannot be parsed is the zero time, never a guess.
func TestResponseDateIsZeroWhenMissingOrUnparsable(t *testing.T) {
	for name, tc := range map[string]struct {
		header func(http.Header)
		want   time.Time
	}{
		"valid":   {func(h http.Header) { h.Set("Date", t0.UTC().Format(http.TimeFormat)) }, t0},
		"missing": {func(h http.Header) { h["Date"] = nil }, time.Time{}}, // the server sends none
		"garbled": {func(h http.Header) { h.Set("Date", "not a date") }, time.Time{}},
		"empty":   {func(h http.Header) { h.Set("Date", "") }, time.Time{}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				tc.header(w.Header())
				_, _ = w.Write([]byte("body"))
			}))
			defer srv.Close()
			c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.5, &clock{now: t0}))

			got, err := c.Get(context.Background(), "/a")
			if err != nil {
				t.Fatal(err)
			}
			if !got.Date.Equal(tc.want) {
				t.Errorf("Date = %v, want %v", got.Date, tc.want)
			}
		})
	}
}

// The ETag cache holds at most maxCacheEntries paths: past that the path used least recently is dropped, which costs one full read
// when it is asked again, never a wrong answer. A path used just now keeps its ETag.
func TestETagCacheIsBoundedAndKeepsTheRecentlyUsed(t *testing.T) {
	rest.LimitCacheTo(t, 3)
	var mu sync.Mutex
	conditional := map[string]bool{} // the last request of each path carried If-None-Match
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		etag := `"` + r.URL.Path + `"`
		mu.Lock()
		conditional[r.URL.Path] = r.Header.Get("If-None-Match") == etag
		mu.Unlock()
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))
	get := func(path string) bool {
		t.Helper()
		if _, err := c.Get(context.Background(), path); err != nil {
			t.Fatalf("Get %s: %v", path, err)
		}
		mu.Lock()
		defer mu.Unlock()
		return conditional[path]
	}

	for _, path := range []string{"/a", "/b", "/c"} {
		get(path)
	}
	if !get("/a") { // used again: now the most recent
		t.Fatal("/a sent no If-None-Match before the bound was reached")
	}
	get("/d") // over the bound: /b, the least recently used, goes
	if got := c.CacheLen(); got != 3 {
		t.Fatalf("the cache holds %d paths after inserting past the bound of 3", got)
	}
	if !get("/a") || !get("/d") {
		t.Error("a recently used path lost its ETag")
	}
	if get("/b") {
		t.Error("/b, the least recently used, still sent If-None-Match after the bound dropped it")
	}
	if got := c.CacheLen(); got != 3 {
		t.Fatalf("the cache holds %d paths after the dropped one came back; want the bound of 3", got)
	}
}

// bodyServer answers each path with an ETag and a body of sizes[path] bytes (looked up on every request, so a test can change it);
// a request carrying the current ETag gets a 304. conditional records whether each path's last request carried If-None-Match.
type bodyServer struct {
	mu          sync.Mutex
	sizes       map[string]int
	version     int
	conditional map[string]bool
}

func (b *bodyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	etag := fmt.Sprintf(`"%s-%d-%d"`, r.URL.Path, b.sizes[r.URL.Path], b.version)
	b.conditional[r.URL.Path] = r.Header.Get("If-None-Match") == etag
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	_, _ = w.Write([]byte(strings.Repeat("x", b.sizes[r.URL.Path])))
}

func (b *bodyServer) sentConditional(path string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conditional[path]
}

func newBodyCache(t *testing.T, maxBytes int, sizes map[string]int) (*rest.Client, *bodyServer, func(string)) {
	t.Helper()
	rest.LimitCacheBytesTo(t, maxBytes)
	b := &bodyServer{sizes: sizes, conditional: map[string]bool{}}
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))
	return c, b, func(path string) {
		t.Helper()
		if _, err := c.Get(context.Background(), path); err != nil {
			t.Fatalf("Get %s: %v", path, err)
		}
	}
}

// The ETag cache also holds at most maxCacheBytes of bodies: past that the entries used least recently are dropped.
func TestETagCacheIsBoundedByBytes(t *testing.T) {
	c, b, get := newBodyCache(t, 10, map[string]int{"/a": 4, "/b": 4, "/c": 4})
	get("/a")
	get("/b")
	get("/a") // /a is now the most recent
	get("/c") // 12 bytes would pass the bound of 10: /b, the least recently used, goes
	if got := c.CacheLen(); got != 2 {
		t.Errorf("the cache holds %d paths, want 2", got)
	}
	if got := c.CacheBytes(); got != 8 {
		t.Errorf("the cache holds %d bytes, want 8", got)
	}
	get("/a")
	get("/c")
	get("/b")
	if !b.sentConditional("/a") || !b.sentConditional("/c") {
		t.Error("a recently used path lost its ETag")
	}
	if b.sentConditional("/b") {
		t.Error("/b, the least recently used, still sent If-None-Match after the byte bound dropped it")
	}
}

// Replacing a path's body swaps its size in the total instead of adding to it.
func TestETagCacheByteTotalFollowsAReplacedBody(t *testing.T) {
	c, b, get := newBodyCache(t, 100, map[string]int{"/a": 4})
	get("/a")
	b.mu.Lock()
	b.sizes["/a"] = 6
	b.mu.Unlock()
	get("/a") // a new ETag: the 200 replaces the entry
	if got, gotBytes := c.CacheLen(), c.CacheBytes(); got != 1 || gotBytes != 6 {
		t.Errorf("the cache holds %d paths and %d bytes after the replacement, want 1 and 6", got, gotBytes)
	}
}

// A body larger than the whole bound is not cached and does not push the others out.
func TestETagCacheSkipsABodyLargerThanTheByteBound(t *testing.T) {
	c, b, get := newBodyCache(t, 10, map[string]int{"/a": 4, "/big": 11})
	get("/a")
	get("/big")
	if got, gotBytes := c.CacheLen(), c.CacheBytes(); got != 1 || gotBytes != 4 {
		t.Errorf("the cache holds %d paths and %d bytes, want /a alone: 1 and 4", got, gotBytes)
	}
	get("/a")
	get("/big")
	if !b.sentConditional("/a") {
		t.Error("/a lost its ETag to a body that was never cached")
	}
	if b.sentConditional("/big") {
		t.Error("/big was cached though it exceeds the byte bound")
	}
}

// A path whose new body is larger than the bound loses its old entry: that ETag no longer describes the current answer.
func TestETagCacheDropsAPathWhoseBodyGrewPastTheByteBound(t *testing.T) {
	c, b, get := newBodyCache(t, 10, map[string]int{"/a": 4})
	get("/a")
	b.mu.Lock()
	b.sizes["/a"] = 11
	b.mu.Unlock()
	get("/a")
	if got, gotBytes := c.CacheLen(), c.CacheBytes(); got != 0 || gotBytes != 0 {
		t.Errorf("the cache holds %d paths and %d bytes, want it empty", got, gotBytes)
	}
}

func TestBudgetWindowReportsWhatIsLeftUntilTheResetInLocalTime(t *testing.T) {
	const skew = 2 * time.Minute
	reset := t0.Add(time.Hour)
	clk := &clock{now: t0.Add(skew)}
	gh := &rateLimitedServer{limit: 10, reset: reset, date: func() time.Time { return clk.Now().Add(-skew) }}
	srv := httptest.NewServer(gh)
	defer srv.Close()
	b := newBudget(t, 0.5, clk)
	c := newClient(t, newTokens().Token, srv.URL, b)

	if w, known := b.Window(); known {
		t.Fatalf("Window before any response = %+v, want unknown", w)
	}
	for i := range 2 {
		if _, err := c.Get(context.Background(), fmt.Sprintf("/p/%d", i)); err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
	}
	if w, known := b.Window(); !known || w.Remaining != 3 || !w.Reset.Equal(reset.Add(skew)) {
		t.Fatalf("Window = %+v, %v; want 3 requests left until %v", w, known, reset.Add(skew))
	}
	for i := 2; i < 5; i++ {
		if _, err := c.Get(context.Background(), fmt.Sprintf("/p/%d", i)); err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
	}
	if w, known := b.Window(); !known || w.Remaining != 0 {
		t.Fatalf("Window over the budget = %+v, %v; want 0 left, known", w, known)
	}
	clk.Set(reset.Add(skew)) // the window has ended: nothing is known until the next response
	if w, known := b.Window(); known {
		t.Fatalf("Window after the reset = %+v, want unknown", w)
	}
}
