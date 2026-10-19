package rest_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

const (
	secretAnswer  = "distinctive-answer-text-5150"
	secretRequest = "distinctive-request-text-8675"
)

type postRequest struct {
	method, uri, contentType, authorization, ifNoneMatch string
	body                                                 string
}

// postRecorder answers every request with the same status, headers and body and records what it received.
type postRecorder struct {
	status int
	header map[string]string
	answer string

	mu   sync.Mutex
	seen []postRequest
}

func (p *postRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body) // a short read shows up as a body mismatch in the assertions
	p.mu.Lock()
	p.seen = append(p.seen, postRequest{r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), r.Header.Get("Authorization"), r.Header.Get("If-None-Match"), string(body)})
	p.mu.Unlock()
	for k, v := range p.header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(p.status)
	_, _ = io.WriteString(w, p.answer)
}

func (p *postRecorder) requests() []postRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]postRequest(nil), p.seen...)
}

func newPoster(t *testing.T, status int, header map[string]string, answer string) (*rest.Client, *postRecorder) {
	t.Helper()
	rec := &postRecorder{status: status, header: header, answer: answer}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	return newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.5, &clock{now: t0})), rec
}

func TestPostSendsTheJSONBodyAndReturns201(t *testing.T) {
	client, rec := newPoster(t, http.StatusCreated, map[string]string{"ETag": `"abc"`}, `{"id":7}`)
	const body = `{"body":"hello"}`

	resp, err := client.Post(t.Context(), "/repos/o/r/issues/1/comments", []byte(body))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if resp.Status != http.StatusCreated || string(resp.Body) != `{"id":7}` {
		t.Errorf("response = %d %q, want 201 and the answer body", resp.Status, resp.Body)
	}
	// A second POST of the same path must not become conditional on the first answer's ETag.
	if _, err := client.Post(t.Context(), "/repos/o/r/issues/1/comments", []byte(body)); err != nil {
		t.Fatalf("second Post: %v", err)
	}
	reqs := rec.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want 2", len(reqs))
	}
	for i, r := range reqs {
		if r.method != http.MethodPost || r.uri != "/repos/o/r/issues/1/comments" || r.body != body {
			t.Errorf("request %d = %s %s %q, want POST with the path and body", i, r.method, r.uri, r.body)
		}
		if r.contentType != "application/json" {
			t.Errorf("request %d Content-Type = %q, want application/json", i, r.contentType)
		}
		if r.authorization != "Bearer "+tokenValue {
			t.Errorf("request %d Authorization is not the bearer token", i)
		}
		if r.ifNoneMatch != "" {
			t.Errorf("request %d sent If-None-Match %q", i, r.ifNoneMatch)
		}
	}
}

func TestPostBodyLimitIsCheckedBeforeAnyRequest(t *testing.T) {
	client, rec := newPoster(t, http.StatusCreated, nil, `{"id":1}`)
	const limit = 64 << 10

	if _, err := client.Post(t.Context(), "/p", make([]byte, limit+1)); err == nil {
		t.Error("a body of 64 KiB + 1 byte was accepted")
	}
	if n := len(rec.requests()); n != 0 {
		t.Errorf("%d requests after an oversized body, want none", n)
	}
	if _, err := client.Post(t.Context(), "/p", make([]byte, limit)); err != nil {
		t.Errorf("a body of exactly 64 KiB was refused: %v", err)
	}
}

func TestPostRejectsAPathWithoutLeadingSlash(t *testing.T) {
	client, rec := newPoster(t, http.StatusCreated, nil, `{}`)
	if _, err := client.Post(t.Context(), "repos/o/r", []byte(`{}`)); err == nil {
		t.Error("Post accepted a path without a leading slash")
	}
	if n := len(rec.requests()); n != 0 {
		t.Errorf("%d requests, want none", n)
	}
}

func TestPostMapsStatusesAndNeverKeepsText(t *testing.T) {
	reset := "1800000060" // a minute after t0
	cases := []struct {
		name        string
		status      int
		header      map[string]string
		wantLimited bool
		wantStatus  int // the StatusError status; 0 when none is expected
		sentinel    error
	}{
		{"403 with Retry-After", 403, map[string]string{"Retry-After": "30"}, true, 0, rest.ErrRateLimited},
		{"429 with Retry-After", 429, map[string]string{"Retry-After": "30"}, true, 0, rest.ErrRateLimited},
		{"403 with remaining 0", 403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset}, true, 0, rest.ErrRateLimited},
		{"403 without limit signals", 403, nil, false, 403, rest.ErrForbidden},
		{"401", 401, nil, false, 401, rest.ErrUnauthorized},
		{"404", 404, nil, false, 404, rest.ErrNotFound},
		{"422", 422, nil, false, 422, rest.ErrUnprocessable},
		{"500", 500, nil, false, 500, nil},
		{"200 is not 201", 200, nil, false, 0, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, _ := newPoster(t, c.status, c.header, `{"message":"`+secretAnswer+" "+tokenValue+`"}`)

			_, err := client.Post(t.Context(), "/repos/o/r/issues/1/comments", []byte(`{"body":"`+secretRequest+`"}`))
			if err == nil {
				t.Fatal("Post succeeded")
			}
			assertPostErrorType(t, err, c.status, c.wantLimited, c.wantStatus)
			if c.sentinel != nil && !errors.Is(err, c.sentinel) {
				t.Errorf("err = %v, want it to match %v", err, c.sentinel)
			}
			for _, secret := range []string{secretAnswer, secretRequest, tokenValue} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error %q contains %q", err, secret)
				}
			}
		})
	}
}

func TestPostIsCountedInTheSharedBudget(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		used := hits
		mu.Unlock()
		setRateLimit(w, 10, 10-used, t0.Add(time.Hour))
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":1}`)
	}))
	t.Cleanup(srv.Close)
	// 20% of 10 is two requests: a POST and a GET use them, the next POST is refused.
	client := newClient(t, newTokens().Token, srv.URL, newBudget(t, 0.2, &clock{now: t0}))

	if _, err := client.Post(t.Context(), "/a", []byte(`{}`)); err != nil {
		t.Fatalf("first Post: %v", err)
	}
	if _, err := client.Get(t.Context(), "/b"); err != nil {
		t.Fatalf("Get after a Post: %v", err)
	}
	_, err := client.Post(t.Context(), "/a", []byte(`{}`))
	var exhausted *rest.BudgetExhaustedError
	if !errors.As(err, &exhausted) || !errors.Is(err, rest.ErrBudgetExhausted) {
		t.Fatalf("third request err = %v, want *BudgetExhaustedError", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 2 {
		t.Errorf("server saw %d requests, want 2 (the refusal must not call GitHub)", hits)
	}
}

func TestPostTokenSourceErrorIsWrappedAndReleasesItsReservation(t *testing.T) {
	boom := errors.New("token source down")
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		used := hits
		mu.Unlock()
		setRateLimit(w, 10, 10-used, t0.Add(time.Hour))
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	var failing atomic.Bool
	token := func(context.Context) (string, string, error) {
		if failing.Load() {
			return "", "", boom
		}
		return tokenValue, "identity-1", nil
	}
	// 20% of 10 is two requests. One answered request makes the window known; the
	// failed attempt must then give its reservation back, or the last Post is refused.
	client := newClient(t, token, srv.URL, newBudget(t, 0.2, &clock{now: t0}))

	if _, err := client.Post(t.Context(), "/p", []byte(`{}`)); err != nil {
		t.Fatalf("first Post: %v", err)
	}
	failing.Store(true)
	if _, err := client.Post(t.Context(), "/p", []byte(`{}`)); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the token source error", err)
	}
	failing.Store(false)
	if _, err := client.Post(t.Context(), "/p", []byte(`{}`)); err != nil {
		t.Errorf("Post after a failed attempt: %v", err)
	}
}

// assertPostErrorType checks the type of a Post error: a rate limit with a time to wait until, or a status error (wantStatus 0: neither).
func assertPostErrorType(t *testing.T, err error, status int, wantLimited bool, wantStatus int) {
	t.Helper()
	var limited *rest.RateLimitedError
	var statusErr *rest.StatusError
	switch {
	case wantLimited:
		if !errors.As(err, &limited) || limited.Status != status {
			t.Fatalf("err = %v, want *RateLimitedError with status %d", err, status)
		}
		if limited.Until.IsZero() {
			t.Error("RateLimitedError.Until is zero")
		}
	case wantStatus != 0:
		if !errors.As(err, &statusErr) || statusErr.Status != wantStatus {
			t.Fatalf("err = %v, want *StatusError with status %d", err, wantStatus)
		}
	}
}
