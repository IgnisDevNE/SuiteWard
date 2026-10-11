package rest_test

import (
	"context"
	"errors"
	"fmt"
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

func setRateLimit(w http.ResponseWriter, limit, remaining int, reset time.Time) {
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
}

func (s *rateLimitedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits++
	w.Header().Set("ETag", `"`+r.URL.Path+`"`)
	if r.Header.Get("If-None-Match") == `"`+r.URL.Path+`"` {
		setRateLimit(w, s.limit, s.limit-s.used, s.reset)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	s.used++
	setRateLimit(w, s.limit, s.limit-s.used, s.reset)
	_, _ = w.Write([]byte("body"))
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
	if _, err := second.Get(context.Background(), "/after"); !errors.Is(err, rest.ErrBudgetExhausted) {
		t.Fatalf("Get after the shared budget is spent: err = %v, want ErrBudgetExhausted", err)
	}
}

func TestGetReturnsTypedErrors(t *testing.T) {
	reset := t0.Add(30 * time.Minute)
	sentinels := []error{rest.ErrUnauthorized, rest.ErrForbidden, rest.ErrNotFound, rest.ErrUnprocessable, rest.ErrRateLimited, rest.ErrBudgetExhausted}
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
		{name: "403 with remaining 0", status: 403, remaining: "0", want: rest.ErrRateLimited, until: reset},
		{name: "403 with Retry-After", status: 403, remaining: "4000", retryAfter: "60", want: rest.ErrRateLimited, until: t0.Add(time.Minute)},
		{name: "429 with Retry-After", status: 429, retryAfter: "30", want: rest.ErrRateLimited, until: t0.Add(30 * time.Second)},
		{name: "429 with remaining 0", status: 429, remaining: "0", want: rest.ErrRateLimited, until: reset},
		{name: "429 without signals", status: 429, remaining: "4000"},
		{name: "500", status: 500, remaining: "4000"},
		{name: "409", status: 409},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
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
			for _, s := range sentinels {
				if got := errors.Is(err, s); got != (s == tc.want) {
					t.Errorf("errors.Is(err, %v) = %v", s, got)
				}
			}
			if tc.want == rest.ErrRateLimited {
				var rl *rest.RateLimitedError
				if !errors.As(err, &rl) || rl.Status != tc.status || !rl.Until.Equal(tc.until) {
					t.Fatalf("rate-limit error = %#v, want status %d until %v", err, tc.status, tc.until)
				}
				return
			}
			var se *rest.StatusError
			if !errors.As(err, &se) || se.Status != tc.status || se.Body != `{"message":"refused"}` {
				t.Fatalf("status error = %#v, want status %d with the body", err, tc.status)
			}
		})
	}
}

func TestErrorBodiesAreTruncated(t *testing.T) {
	long := strings.Repeat("x", 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(long))
	}))
	defer srv.Close()
	c := newClient(t, newTokens().Token, srv.URL, newBudget(t, 1, &clock{now: t0}))

	_, err := c.Get(context.Background(), "/repos/o/r")
	var se *rest.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a *StatusError", err)
	}
	if se.Body != long[:512] {
		t.Fatalf("body in error has %d bytes, want the first 512", len(se.Body))
	}
	if strings.Contains(err.Error(), long[:513]) {
		t.Fatal("error text carries more than 512 bytes of the body")
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
				_, _ = fmt.Fprintf(w, "echo %v %s", r.Header, r.Header.Get("Authorization"))
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
