package rest_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

// redirectSecret stands for the short-lived credential GitHub puts in the query of an archive's redirect.
const redirectSecret = "redirect-credential-7731"

// archivePath is the path of a tarball read, as the github adapter builds it.
const archivePath = "/repos/o/r/tarball/0123456789abcdef0123456789abcdef01234567"

// payload is deterministic data in which no two neighbouring chunks look alike, so a copy that drops or reorders one shows.
func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i + i>>8 + i>>16)
	}
	return b
}

// recordingWriter keeps what it is given and the size of each write; firstWrite is closed by the first one.
// It has no ReadFrom, so io.Copy hands it chunks instead of growing a buffer.
type recordingWriter struct {
	firstWrite chan struct{}

	mu     sync.Mutex
	once   sync.Once
	data   bytes.Buffer
	writes []int
}

func newRecordingWriter() *recordingWriter { return &recordingWriter{firstWrite: make(chan struct{})} }

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.data.Write(p)
	w.writes = append(w.writes, len(p))
	w.mu.Unlock()
	w.once.Do(func() { close(w.firstWrite) })
	return len(p), nil
}

func (w *recordingWriter) written() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return bytes.Clone(w.data.Bytes())
}

func (w *recordingWriter) sizes() []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.writes)
}

type seenRequest struct{ method, uri, authorization string }

// requestLog records every request a test server receives.
type requestLog struct {
	mu   sync.Mutex
	seen []seenRequest
}

func (l *requestLog) all() []seenRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.seen)
}

// startLogged runs h on a test server and logs its requests.
func startLogged(t *testing.T, h http.Handler) (*httptest.Server, *requestLog) {
	t.Helper()
	log := &requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.mu.Lock()
		log.seen = append(log.seen, seenRequest{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization")})
		log.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

// found answers every request with a 302 to location.
func found(location string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusFound)
	})
}

// serveBody answers 200 with body; declared says whether it announces the length, otherwise the body goes out chunked.
func serveBody(body []byte, declared bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if declared {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write(body[:10])
		w.(http.Flusher).Flush()
		_, _ = w.Write(body[10:])
	})
}

// downloadClient is a client over the API at apiURL with a budget that does not interfere.
func downloadClient(t *testing.T, apiURL string) *rest.Client {
	t.Helper()
	return newClient(t, newTokens().Token, apiURL, newBudget(t, 0.5, &clock{now: t0}))
}

func TestDownloadSendsTheTokenToTheAPIOnlyAndCopiesWhatTheRedirectServes(t *testing.T) {
	body := payload(100 << 10)
	target, targetLog := startLogged(t, serveBody(body, true))
	// Both servers answer on 127.0.0.1 and differ in the port only: net/http's own redirect rule compares hostnames, so it would forward the token here.
	api, apiLog := startLogged(t, found(target.URL+"/legacy.tar.gz/abc?token="+redirectSecret))
	dst := newRecordingWriter()

	n, err := downloadClient(t, api.URL).Download(t.Context(), archivePath, dst, 1<<20)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if n != int64(len(body)) || !bytes.Equal(dst.written(), body) {
		t.Errorf("Download copied %d bytes (%d reported), want the %d bytes of the body", len(dst.written()), n, len(body))
	}
	if got := apiLog.all(); len(got) != 1 || got[0] != (seenRequest{http.MethodGet, archivePath, "Bearer " + tokenValue}) {
		t.Errorf("the API saw %+v, want one GET of the path with the bearer token", got)
	}
	if got := targetLog.all(); len(got) != 1 || got[0] != (seenRequest{http.MethodGet, "/legacy.tar.gz/abc?token=" + redirectSecret, ""}) {
		t.Errorf("the redirect target saw %+v, want one GET of the Location with no Authorization", got)
	}
}

func TestDownloadHandsTheBodyToTheWriterAsItArrives(t *testing.T) {
	body := payload(200 << 10)
	dst := newRecordingWriter()
	var streamed atomic.Bool
	target, _ := startLogged(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body[:64<<10])
		w.(http.Flusher).Flush()
		select {
		case <-dst.firstWrite: // the first chunk reached the writer while the rest of the body was still unsent
			streamed.Store(true)
		case <-time.After(5 * time.Second): // a Download that buffers the body never writes before the body ends
		}
		_, _ = w.Write(body[64<<10:])
	}))
	api, _ := startLogged(t, found(target.URL+"/a.tar.gz"))

	if _, err := downloadClient(t, api.URL).Download(t.Context(), archivePath, dst, 1<<20); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !streamed.Load() {
		t.Error("the writer got nothing until the whole body had been sent: the body is buffered")
	}
	if !bytes.Equal(dst.written(), body) {
		t.Error("the writer did not get the body")
	}
	if sizes := dst.sizes(); len(sizes) < 3 || slices.Max(sizes) > 64<<10 {
		t.Errorf("writes of %v bytes, want the 200 KiB body in several chunks of at most 64 KiB", sizes)
	}
}

func TestDownloadStopsAtTheLimit(t *testing.T) {
	const limit = 1000
	for _, tc := range []struct {
		name        string
		size        int
		declared    bool
		wantErr     bool
		wantWritten int
	}{
		{"declared length over the limit is refused before a byte is copied", limit + 1, true, true, 0},
		{"chunked body over the limit stops at the limit", limit + 1, false, true, limit},
		{"declared length at the limit", limit, true, false, limit},
		{"chunked body at the limit", limit, false, false, limit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := payload(tc.size)
			target, _ := startLogged(t, serveBody(body, tc.declared))
			api, _ := startLogged(t, found(target.URL+"/a.tar.gz"))
			dst := newRecordingWriter()

			n, err := downloadClient(t, api.URL).Download(t.Context(), archivePath, dst, limit)
			if tc.wantErr != errors.Is(err, rest.ErrTooLarge) || (!tc.wantErr && err != nil) {
				t.Fatalf("err = %v, want ErrTooLarge: %v", err, tc.wantErr)
			}
			if got := dst.written(); len(got) != tc.wantWritten || !bytes.Equal(got, body[:tc.wantWritten]) {
				t.Errorf("the writer got %d bytes, want the first %d of the body", len(got), tc.wantWritten)
			}
			if !tc.wantErr && n != limit {
				t.Errorf("Download reported %d bytes, want %d", n, limit)
			}
		})
	}
}

func TestDownloadReportsABrokenBodyAndNeverSucceeds(t *testing.T) {
	for name, broken := range map[string]http.HandlerFunc{
		"fewer bytes than the declared length": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "10000")
			_, _ = w.Write(payload(4000)) // the server closes the connection: it cannot pad the answer
		},
		"chunked body that never ends": func(w http.ResponseWriter, _ *http.Request) {
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n")
			_ = rw.Flush()
			_ = conn.Close()
		},
	} {
		t.Run(name, func(t *testing.T) {
			target, _ := startLogged(t, broken)
			api, _ := startLogged(t, found(target.URL+"/a.tar.gz"))

			_, err := downloadClient(t, api.URL).Download(t.Context(), archivePath, newRecordingWriter(), 1<<20)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("err = %v, want io.ErrUnexpectedEOF", err)
			}
		})
	}
}

func TestDownloadMapsTheAnswerOfTheAPIAndNeverRetries(t *testing.T) {
	reset := "1800000060" // a minute after t0
	for _, tc := range []struct {
		name        string
		status      int
		header      map[string]string
		wantLimited bool
		wantStatus  int // the StatusError status; 0 when none is expected
		sentinel    error
		text        string // a part the error must name
	}{
		{"404", 404, nil, false, 404, rest.ErrNotFound, ""},
		{"401", 401, nil, false, 401, rest.ErrUnauthorized, ""},
		{"403 without limit signals", 403, nil, false, 403, rest.ErrForbidden, ""},
		{"403 with Retry-After", 403, map[string]string{"Retry-After": "30"}, true, 0, rest.ErrRateLimited, ""},
		{"429 with remaining 0", 429, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset}, true, 0, rest.ErrRateLimited, ""},
		{"500", 500, nil, false, 500, nil, ""},
		{"301 is not followed", 301, nil, false, 301, nil, ""},
		{"200 is not a redirect", 200, nil, false, 0, nil, "answered 200, want 302"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, targetLog := startLogged(t, serveBody(payload(100), true))
			api, apiLog := startLogged(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				if tc.status == http.StatusMovedPermanently {
					w.Header().Set("Location", target.URL+"/moved")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"message":"`+secretAnswer+" "+tokenValue+`"}`)
			}))
			dst := newRecordingWriter()

			_, err := downloadClient(t, api.URL).Download(t.Context(), archivePath, dst, 1<<20)
			if err == nil {
				t.Fatal("Download succeeded")
			}
			assertPostErrorType(t, err, tc.status, tc.wantLimited, tc.wantStatus)
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Errorf("err = %v, want it to match %v", err, tc.sentinel)
			}
			if !strings.Contains(err.Error(), tc.text) {
				t.Errorf("err = %v, want it to say %q", err, tc.text)
			}
			for _, secret := range []string{secretAnswer, tokenValue} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error %q contains %q", err, secret)
				}
			}
			if n := len(apiLog.all()); n != 1 {
				t.Errorf("the API saw %d requests, want 1 (no retry)", n)
			}
			if n := len(targetLog.all()); n != 0 || len(dst.written()) != 0 {
				t.Errorf("the redirect target saw %d requests and the writer %d bytes, want none", n, len(dst.written()))
			}
		})
	}
}

func TestDownloadMapsTheAnswerOfTheRedirectTargetAndNeverRetries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		wantStatus int
		sentinel   error
		text       string
	}{
		{"404", 404, 404, rest.ErrNotFound, ""},
		{"500", 500, 500, nil, ""},
		{"204 is not an archive", 204, 0, nil, "answered 204, want 200"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, targetLog := startLogged(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				if tc.status != http.StatusNoContent {
					_, _ = io.WriteString(w, secretAnswer)
				}
			}))
			api, _ := startLogged(t, found(target.URL+"/a.tar.gz?token="+redirectSecret))
			dst := newRecordingWriter()

			_, err := downloadClient(t, api.URL).Download(t.Context(), archivePath, dst, 1<<20)
			if err == nil {
				t.Fatal("Download succeeded")
			}
			assertPostErrorType(t, err, tc.status, false, tc.wantStatus)
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Errorf("err = %v, want it to match %v", err, tc.sentinel)
			}
			if !strings.Contains(err.Error(), tc.text) {
				t.Errorf("err = %v, want it to say %q", err, tc.text)
			}
			for _, secret := range []string{secretAnswer, redirectSecret, tokenValue} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error %q contains %q", err, secret)
				}
			}
			if n := len(targetLog.all()); n != 1 || len(dst.written()) != 0 {
				t.Errorf("the target saw %d requests and the writer %d bytes, want 1 and none (no retry)", n, len(dst.written()))
			}
		})
	}
}

// The Location of a private repository's archive carries a credential; no error may repeat it.
func TestDownloadErrorsNeverCarryTheRedirectURL(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close() // a closed server: connecting to it is refused

	for name, tc := range map[string]struct {
		location string
		text     string
	}{
		"target is down":      {downURL + "/a.tar.gz?token=" + redirectSecret, strings.TrimPrefix(downURL, "http://")}, // the error names the host it could not reach
		"missing Location":    {"", "Location"},
		"unparsable Location": {"http://bad host/a.tar.gz?token=" + redirectSecret, "Location"},
	} {
		t.Run(name, func(t *testing.T) {
			api, _ := startLogged(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				w.WriteHeader(http.StatusFound)
			}))

			_, err := downloadClient(t, api.URL).Download(t.Context(), archivePath, newRecordingWriter(), 1<<20)
			if err == nil {
				t.Fatal("Download succeeded")
			}
			if strings.Contains(err.Error(), redirectSecret) || strings.Contains(err.Error(), tokenValue) {
				t.Errorf("error %q repeats the redirect credential or the token", err)
			}
			if !strings.Contains(err.Error(), tc.text) {
				t.Errorf("err = %v, want it to say %q", err, tc.text)
			}
		})
	}
}

func TestDownloadFollowsOneRedirectOnly(t *testing.T) {
	third, thirdLog := startLogged(t, serveBody(payload(100), true))
	second, _ := startLogged(t, found(third.URL+"/c.tar.gz"))
	api, _ := startLogged(t, found(second.URL+"/b.tar.gz"))

	_, err := downloadClient(t, api.URL).Download(t.Context(), archivePath, newRecordingWriter(), 1<<20)
	var status *rest.StatusError
	if !errors.As(err, &status) || status.Status != http.StatusFound {
		t.Errorf("err = %v, want *StatusError 302 for the second redirect", err)
	}
	if n := len(thirdLog.all()); n != 0 {
		t.Errorf("the third host saw %d requests, want none", n)
	}
}

func TestDownloadCountsOnceInTheSharedBudgetFromTheAnswerOfTheAPI(t *testing.T) {
	target, targetLog := startLogged(t, serveBody(payload(100), true))
	var mu sync.Mutex
	hits := 0
	api, apiLog := startLogged(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		used := hits
		mu.Unlock()
		setRateLimit(w, 10, 10-used, t0.Add(time.Hour)) // only the API's answer carries the rate limit, never the archive host's
		w.Header().Set("Location", target.URL+"/a.tar.gz")
		w.WriteHeader(http.StatusFound)
	}))
	// 20% of 10 is two requests: two downloads use them, the third is refused without calling GitHub.
	client := newClient(t, newTokens().Token, api.URL, newBudget(t, 0.2, &clock{now: t0}))

	for i := 1; i <= 2; i++ {
		if _, err := client.Download(t.Context(), archivePath, newRecordingWriter(), 1<<20); err != nil {
			t.Fatalf("download %d: %v", i, err)
		}
	}
	_, err := client.Download(t.Context(), archivePath, newRecordingWriter(), 1<<20)
	var exhausted *rest.BudgetExhaustedError
	if !errors.As(err, &exhausted) || !errors.Is(err, rest.ErrBudgetExhausted) {
		t.Fatalf("third download err = %v, want *BudgetExhaustedError", err)
	}
	if a, b := len(apiLog.all()), len(targetLog.all()); a != 2 || b != 2 {
		t.Errorf("the API saw %d requests and the archive host %d, want 2 each (the refusal calls nobody)", a, b)
	}
}

func TestDownloadTokenSourceErrorIsWrappedAndReleasesItsReservation(t *testing.T) {
	boom := errors.New("token source down")
	target, _ := startLogged(t, serveBody(payload(100), true))
	var mu sync.Mutex
	hits := 0
	api, apiLog := startLogged(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		used := hits
		mu.Unlock()
		setRateLimit(w, 10, 10-used, t0.Add(time.Hour))
		w.Header().Set("Location", target.URL+"/a.tar.gz")
		w.WriteHeader(http.StatusFound)
	}))
	var failing atomic.Bool
	token := func(context.Context) (string, string, error) {
		if failing.Load() {
			return "", "", boom
		}
		return tokenValue, "identity-1", nil
	}
	// 20% of 10 is two requests. One answered download makes the window known; the failed attempt must give its reservation back or the last one is refused.
	client := newClient(t, token, api.URL, newBudget(t, 0.2, &clock{now: t0}))

	if _, err := client.Download(t.Context(), archivePath, newRecordingWriter(), 1<<20); err != nil {
		t.Fatalf("first download: %v", err)
	}
	failing.Store(true)
	_, err := client.Download(t.Context(), archivePath, newRecordingWriter(), 1<<20)
	if !errors.Is(err, boom) || !errors.Is(err, rest.ErrNotSent) {
		t.Fatalf("err = %v, want the token source error, not sent", err)
	}
	failing.Store(false)
	if _, err := client.Download(t.Context(), archivePath, newRecordingWriter(), 1<<20); err != nil {
		t.Errorf("download after a failed attempt: %v", err)
	}
	if n := len(apiLog.all()); n != 2 {
		t.Errorf("the API saw %d requests, want 2 (the failed attempt sends nothing)", n)
	}
}

func TestDownloadRejectsInvalidArgumentsBeforeAnyRequest(t *testing.T) {
	api, apiLog := startLogged(t, found("http://127.0.0.1:1/a.tar.gz"))
	client := downloadClient(t, api.URL)
	for name, tc := range map[string]struct {
		path  string
		limit int64
		text  string
	}{
		"path without a leading slash": {"repos/o/r/tarball/x", 1 << 20, "path must start with /"},
		"zero limit":                   {archivePath, 0, "limit"},
		"negative limit":               {archivePath, -1, "limit"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.Download(t.Context(), tc.path, newRecordingWriter(), tc.limit); err == nil || !strings.Contains(err.Error(), tc.text) {
				t.Errorf("err = %v, want a refusal that says %q", err, tc.text)
			}
			if n := len(apiLog.all()); n != 0 {
				t.Errorf("the API saw %d requests, want none", n)
			}
		})
	}
}

// The client's Timeout bounds a whole exchange, the body included: it is meant for small reads, not for a large archive.
func TestDownloadIsNotCutByTheTimeoutOfTheHTTPClient(t *testing.T) {
	body := payload(100 << 10)
	target, _ := startLogged(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body[:1000])
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond) // six times the client's Timeout
		_, _ = w.Write(body[1000:])
	}))
	api, _ := startLogged(t, found(target.URL+"/a.tar.gz"))
	client, err := rest.NewClient(newTokens().Token, &http.Client{Timeout: 50 * time.Millisecond}, api.URL, newBudget(t, 0.5, &clock{now: t0}))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	dst := newRecordingWriter()

	if _, err := client.Download(t.Context(), archivePath, dst, 1<<20); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !bytes.Equal(dst.written(), body) {
		t.Errorf("the writer got %d bytes, want the %d bytes of the body", len(dst.written()), len(body))
	}
}

// The bytes are the archive as GitHub sends it: a Content-Encoding on the answer must not make the transport unpack them.
func TestDownloadCopiesTheBytesOnTheWire(t *testing.T) {
	var packed bytes.Buffer
	zw := gzip.NewWriter(&packed)
	_, _ = zw.Write(payload(10 << 10))
	_ = zw.Close()
	target, _ := startLogged(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(packed.Bytes())
	}))
	api, _ := startLogged(t, found(target.URL+"/a.tar.gz"))
	dst := newRecordingWriter()

	if _, err := downloadClient(t, api.URL).Download(t.Context(), archivePath, dst, 1<<20); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !bytes.Equal(dst.written(), packed.Bytes()) {
		t.Errorf("the writer got %d bytes, want the %d bytes the server sent", len(dst.written()), packed.Len())
	}
}
