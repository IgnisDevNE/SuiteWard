package github_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

const (
	archiveRepo = "octo-org/octo-repo"
	archiveSHA  = "0123456789abcdef0123456789abcdef01234567"
	archiveURI  = "/repos/octo-org/octo-repo/tarball/" + archiveSHA
	// archiveToken is the bearer value of the fake installation token; it must reach the API and nobody else.
	archiveToken = "installation-token-5150"
)

type archiveRequest struct{ method, uri, authorization string }

// archiveHost is a test server that records what it is asked.
type archiveHost struct {
	*httptest.Server

	mu   sync.Mutex
	seen []archiveRequest
}

func (h *archiveHost) requests() []archiveRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]archiveRequest(nil), h.seen...)
}

func startArchiveHost(t *testing.T, handler http.Handler) *archiveHost {
	t.Helper()
	h := &archiveHost{}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.seen = append(h.seen, archiveRequest{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization")})
		h.mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(h.Close)
	return h
}

// redirectTo answers with a 302 to location and the rate limit headers of GitHub's API: a window of 10 requests,
// one more used by each answer.
func redirectTo(location string) http.Handler {
	var mu sync.Mutex
	answered := 0
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		answered++
		remaining := 10 - answered
		mu.Unlock()
		w.Header().Set("X-RateLimit-Limit", "10")
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusFound)
	})
}

// sends answers 200 with body in two chunks; it announces the length only when declared.
func sends(body []byte, declared bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if declared {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		_, _ = w.Write(body[:10])
		w.(http.Flusher).Flush()
		_, _ = w.Write(body[10:])
	})
}

// archiveReader is a reader over api whose budget allows fraction of a window.
func archiveReader(t *testing.T, api *archiveHost, fraction float64) *github.Reader {
	t.Helper()
	budget, err := rest.NewBudget(fraction, time.Now)
	if err != nil {
		t.Fatalf("NewBudget: %v", err)
	}
	token := func(context.Context) (string, string, error) { return archiveToken, "archive-identity", nil }
	client, err := rest.NewClient(token, http.DefaultClient, api.URL, budget)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	reader, err := github.NewReader(client)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return reader
}

func archiveBody(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i + i>>8)
	}
	return b
}

func TestArchiveStreamsTheTarballOfTheCommitWithTheInstallationToken(t *testing.T) {
	body := archiveBody(100 << 10)
	target := startArchiveHost(t, sends(body, true))
	api := startArchiveHost(t, redirectTo(target.URL+"/legacy.tar.gz/refs/heads/main?token=short-lived"))
	var dst bytes.Buffer

	n, err := archiveReader(t, api, 0.5).Archive(t.Context(), archiveRepo, archiveSHA, &dst, github.DefaultArchiveLimit)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if n != int64(len(body)) || !bytes.Equal(dst.Bytes(), body) {
		t.Errorf("Archive wrote %d bytes (%d reported), want the %d bytes of the body", dst.Len(), n, len(body))
	}
	if got := api.requests(); len(got) != 1 || got[0] != (archiveRequest{http.MethodGet, archiveURI, "Bearer " + archiveToken}) {
		t.Errorf("the API saw %+v, want one GET of the commit's tarball with the installation token", got)
	}
	if got := target.requests(); len(got) != 1 || got[0].authorization != "" || got[0].uri != "/legacy.tar.gz/refs/heads/main?token=short-lived" {
		t.Errorf("the redirect target saw %+v, want one GET of the Location and no Authorization", got)
	}
}

func TestArchiveRefusesWhatIsNotAFullCommitIDOfARepositoryBeforeAnyRequest(t *testing.T) {
	api := startArchiveHost(t, redirectTo("http://127.0.0.1:1/x"))
	reader := archiveReader(t, api, 0.5)
	for name, tc := range map[string]struct{ repo, sha string }{
		"empty SHA":          {archiveRepo, ""},
		"branch name":        {archiveRepo, "main"},
		"abbreviated SHA":    {archiveRepo, archiveSHA[:7]},
		"41 characters":      {archiveRepo, archiveSHA + "a"},
		"uppercase":          {archiveRepo, strings.ToUpper(archiveSHA)},
		"path trick":         {archiveRepo, "../../" + archiveSHA[:34]},
		"repo without owner": {"octo-repo", archiveSHA},
		"repo with a path":   {archiveRepo + "/extra", archiveSHA},
		"repo dot-dot":       {"../octo-repo", archiveSHA},
	} {
		t.Run(name, func(t *testing.T) {
			var dst bytes.Buffer
			if _, err := reader.Archive(t.Context(), tc.repo, tc.sha, &dst, github.DefaultArchiveLimit); !errors.Is(err, github.ErrInvalidArgument) {
				t.Errorf("err = %v, want ErrInvalidArgument", err)
			}
			if n := len(api.requests()); n != 0 || dst.Len() != 0 {
				t.Errorf("%d requests and %d bytes written, want none", n, dst.Len())
			}
		})
	}
	if _, err := reader.Archive(t.Context(), archiveRepo, archiveSHA, io.Discard, 0); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("err = %v, want a refusal of a limit that is not positive", err)
	}
	if n := len(api.requests()); n != 0 {
		t.Errorf("%d requests after a limit that is not positive, want none", n)
	}
}

func TestArchiveSizeLimitDefaultsToHalfAGibibyte(t *testing.T) {
	if github.DefaultArchiveLimit != 512<<20 {
		t.Errorf("DefaultArchiveLimit = %d, want 512 MiB", github.DefaultArchiveLimit)
	}
}

func TestArchiveStopsAtTheLimitAndNeverSucceedsOnABrokenBody(t *testing.T) {
	const limit = 1000
	for _, tc := range []struct {
		name         string
		handler      http.Handler
		want         error // nil: the archive is copied whole
		wantWritten  int
		writtenIsMax bool // the writer may have got fewer bytes than wantWritten
	}{
		{"declared length over the limit", sends(archiveBody(limit+1), true), rest.ErrTooLarge, 0, false},
		{"chunked body over the limit", sends(archiveBody(limit+1), false), rest.ErrTooLarge, limit, false},
		{"chunked body at the limit", sends(archiveBody(limit), false), nil, limit, false},
		{"fewer bytes than the declared length", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "500")
			_, _ = w.Write(archiveBody(100))
		}), io.ErrUnexpectedEOF, 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := startArchiveHost(t, tc.handler)
			api := startArchiveHost(t, redirectTo(target.URL+"/a.tar.gz"))
			var dst bytes.Buffer

			_, err := archiveReader(t, api, 0.5).Archive(t.Context(), archiveRepo, archiveSHA, &dst, limit)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if dst.Len() > tc.wantWritten || !tc.writtenIsMax && dst.Len() != tc.wantWritten {
				t.Errorf("Archive wrote %d bytes, want %d", dst.Len(), tc.wantWritten)
			}
		})
	}
}

func TestArchiveNamesTheStatusOfAFailedReadAndDoesNotRetry(t *testing.T) {
	failing := func(status int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
	}
	t.Run("the API answers 404", func(t *testing.T) {
		api := startArchiveHost(t, failing(http.StatusNotFound))

		_, err := archiveReader(t, api, 0.5).Archive(t.Context(), archiveRepo, archiveSHA, io.Discard, github.DefaultArchiveLimit)
		var status *rest.StatusError
		if !errors.As(err, &status) || status.Status != 404 || !errors.Is(err, rest.ErrNotFound) || !strings.Contains(err.Error(), "status 404") {
			t.Errorf("err = %v, want one that names status 404 and matches ErrNotFound", err)
		}
		if n := len(api.requests()); n != 1 {
			t.Errorf("the API saw %d requests, want 1", n)
		}
	})
	t.Run("the archive host answers 502", func(t *testing.T) {
		target := startArchiveHost(t, failing(http.StatusBadGateway))
		api := startArchiveHost(t, redirectTo(target.URL+"/a.tar.gz"))

		_, err := archiveReader(t, api, 0.5).Archive(t.Context(), archiveRepo, archiveSHA, io.Discard, github.DefaultArchiveLimit)
		var status *rest.StatusError
		if !errors.As(err, &status) || status.Status != 502 || !strings.Contains(err.Error(), "status 502") {
			t.Errorf("err = %v, want one that names status 502", err)
		}
		if n := len(target.requests()); n != 1 {
			t.Errorf("the archive host saw %d requests, want 1", n)
		}
	})
}

func TestArchiveCountsOnceAgainstTheBudgetAndHonoursItsRefusal(t *testing.T) {
	target := startArchiveHost(t, sends(archiveBody(100), true))
	api := startArchiveHost(t, redirectTo(target.URL+"/a.tar.gz"))
	// The window is 10 requests and the API reports one more used by each answer: 20% allows two downloads.
	reader := archiveReader(t, api, 0.2)

	for i := 1; i <= 2; i++ {
		if _, err := reader.Archive(t.Context(), archiveRepo, archiveSHA, io.Discard, github.DefaultArchiveLimit); err != nil {
			t.Fatalf("download %d: %v", i, err)
		}
	}
	_, err := reader.Archive(t.Context(), archiveRepo, archiveSHA, io.Discard, github.DefaultArchiveLimit)
	if !errors.Is(err, rest.ErrBudgetExhausted) {
		t.Fatalf("third download err = %v, want the budget's refusal", err)
	}
	if a, b := len(api.requests()), len(target.requests()); a != 2 || b != 2 {
		t.Errorf("the API saw %d requests and the archive host %d, want 2 each (the refusal calls nobody)", a, b)
	}
}
