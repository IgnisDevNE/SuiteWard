package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

func TestReadErrorsAreClassifiedWithoutTheTextGitHubSent(t *testing.T) {
	until := time.Date(2026, 10, 11, 13, 0, 0, 0, time.UTC)
	const secret = "BODY-GITHUB-SENT"
	for name, test := range map[string]struct {
		err  error
		want githubsync.ReadError
	}{
		"rate limited":     {&rest.RateLimitedError{Status: 403, Until: until, Body: secret}, githubsync.ReadError{Kind: githubsync.FailureRateLimited, Status: 403, WaitUntil: until}},
		"budget exhausted": {&rest.BudgetExhaustedError{Reset: until}, githubsync.ReadError{Kind: githubsync.FailureBudgetExhausted, WaitUntil: until}},
		"unauthorized":     {&rest.StatusError{Status: 401, Body: secret}, githubsync.ReadError{Kind: githubsync.FailureUnauthorized, Status: 401}},
		"forbidden":        {&rest.StatusError{Status: 403, Body: secret}, githubsync.ReadError{Kind: githubsync.FailureForbidden, Status: 403}},
		"not found":        {&rest.StatusError{Status: 404, Body: secret}, githubsync.ReadError{Kind: githubsync.FailureNotFound, Status: 404}},
		"unprocessable":    {&rest.StatusError{Status: 422, Body: secret}, githubsync.ReadError{Kind: githubsync.FailureUnprocessable, Status: 422}},
		"other status":     {&rest.StatusError{Status: 502, Body: secret}, githubsync.ReadError{Kind: githubsync.FailureStatus, Status: 502}},
		"token request":    {&appauth.StatusError{Status: 401}, githubsync.ReadError{Kind: githubsync.FailureUnauthorized, Status: 401}},
		"token not found":  {&appauth.StatusError{Status: 404}, githubsync.ReadError{Kind: githubsync.FailureNotFound, Status: 404}},
		"token server":     {&appauth.StatusError{Status: 503}, githubsync.ReadError{Kind: githubsync.FailureStatus, Status: 503}},
		"unknown":          {errors.New("something no adapter names, with " + secret), githubsync.ReadError{Kind: githubsync.FailureTransport}},
		"token mismatch":   {fmt.Errorf("%w: requested %s", appauth.ErrPermissionsMismatch, secret), githubsync.ReadError{Kind: githubsync.FailureForbidden}},
		"decode":           {fmt.Errorf("%w: %s", github.ErrInvalidResponse, secret), githubsync.ReadError{Kind: githubsync.FailureDecode}},
		"transport":        {&url.Error{Op: "Get", URL: "https://api.example/" + secret, Err: &net.OpError{Op: "dial"}}, githubsync.ReadError{Kind: githubsync.FailureTransport}},
		"client timeout":   {fmt.Errorf("github GET /x: %w", context.DeadlineExceeded), githubsync.ReadError{Kind: githubsync.FailureTransport}},
	} {
		t.Run(name, func(t *testing.T) {
			// The adapters wrap what they return, as github.Reader does.
			err := classifyReadError(fmt.Errorf("open pull requests of acme/widgets: %w", test.err))

			var got *githubsync.ReadError
			if !errors.As(err, &got) {
				t.Fatalf("classifyReadError = %v; want a *githubsync.ReadError", err)
			}
			if got.Kind != test.want.Kind || got.Status != test.want.Status || !got.WaitUntil.Equal(test.want.WaitUntil) {
				t.Fatalf("classified as %+v; want %+v", *got, test.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("the classified error carries text GitHub sent: %v", err)
			}
		})
	}
}

func TestTickDeadlineIsBelowTheJobTimeout(t *testing.T) {
	for jobTimeout, want := range map[time.Duration]time.Duration{
		time.Minute:      55 * time.Second,
		time.Hour:        time.Hour - 5*time.Second,
		10 * time.Second: 5 * time.Second,
		5 * time.Second:  2500 * time.Millisecond, // JOB_TIMEOUT minus 5 s would leave nothing: half is the floor
		time.Second:      500 * time.Millisecond,
	} {
		if got := tickDeadline(jobTimeout); got != want {
			t.Errorf("tickDeadline(%v) = %v; want %v", jobTimeout, got, want)
		}
	}
}

func goodGitHubSettings() config.GitHub {
	return config.GitHub{AppID: 1, AppKey: "not a key", InstallationID: 2, Repositories: []string{"acme/widgets"}, APIURL: "http://127.0.0.1:1"}
}

func TestComposeGitHubRejectsAnUnusableKeyWithoutPrintingIt(t *testing.T) {
	settings := goodGitHubSettings()
	settings.AppKey = "SECRET-KEY-TEXT"
	_, err := composeGitHub(settings, time.Minute, githubsynctest.NewMemory())
	if err == nil || strings.Contains(err.Error(), "SECRET-KEY-TEXT") {
		t.Fatalf("composeGitHub = %v; want an error that does not contain the key", err)
	}
}

func TestSyncRepoRefusesARepositoryThatIsNotConfigured(t *testing.T) {
	store := githubsynctest.NewMemory()
	reader := &countingReader{}
	reconciler, err := githubsync.NewReconciler(reader, store, time.Now, 2)
	if err != nil {
		t.Fatal(err)
	}
	s := &githubSync{reconciler: reconciler, repos: []string{"acme/widgets"}, jobTimeout: time.Minute}

	if err := s.syncRepo(t.Context(), "someone/else"); err == nil {
		t.Fatal("syncRepo accepted a repository that is not configured")
	}
	if reader.calls != 0 {
		t.Fatal("GitHub was called for a repository that is not configured")
	}
	if err := s.syncRepo(t.Context(), "acme/widgets"); err != nil || reader.calls == 0 {
		t.Fatalf("syncRepo of a configured repository = %v after %d calls; want a cycle", err, reader.calls)
	}
}

type countingReader struct{ calls int }

func (c *countingReader) OpenPulls(context.Context, string) ([]githubsync.ObservedPull, error) {
	c.calls++
	return nil, nil
}

func (c *countingReader) PullComments(context.Context, string, int) ([]githubsync.ObservedComment, error) {
	c.calls++
	return nil, nil
}

func TestStatusReportsTheConfiguredRepositories(t *testing.T) {
	store := githubsynctest.NewMemory()
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	wait := at.Add(30 * time.Minute)
	if err := store.RecordCycle(t.Context(), githubsync.Cycle{Repo: "acme/ok", InstallationID: 2, At: at}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFailure(t.Context(), githubsync.Failure{Repo: "acme/limited", InstallationID: 2, At: at, Kind: githubsync.FailureRateLimited, Status: 403, WaitUntil: wait}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFailure(t.Context(), githubsync.Failure{Repo: "acme/down", InstallationID: 2, At: at, Kind: githubsync.FailureTransport}); err != nil {
		t.Fatal(err)
	}
	a := &api{owners: ownertest.NewMemory(), logger: quietLogger(), tier: "co-located", store: fakeStore{version: 11}, github: &githubStatus{repos: []string{"acme/ok", "acme/limited", "acme/down", "acme/new"}, store: store, ids: &repositoryIDs{}, problems: &bootstrapProblems{}}}

	code, body := call(t, a, http.MethodGet, "/status")

	want := `{"version":"` + Version + `","schemaVersion":11,"tier":"co-located",
		"jobs":{"available":0,"running":0,"retryable":0,"scheduled":0,"completed":0,"discarded":0},
		"outbox":{"pending":0,"delivered":0,"failed":0},
		"github":[
			{"repo":"acme/ok","lastSuccess":"2026-10-11T12:00:00Z"},
			{"repo":"acme/limited","lastError":"rate_limited (HTTP 403)","waitUntil":"2026-10-11T12:30:00Z"},
			{"repo":"acme/down","lastError":"transport"},
			{"repo":"acme/new"}]}`
	if code != http.StatusOK || !jsonEqual(t, body, want) {
		t.Fatalf("GET /status = %d %s; want 200 %s", code, body, want)
	}
}

func TestStatusShowsTheLastBootstrapProblemOfARepository(t *testing.T) {
	problems := &bootstrapProblems{}
	problems.set("acme/ok", "origin-not-passed")
	problems.set("acme/down", "read-failed")
	problems.clear("acme/down")
	a := &api{owners: ownertest.NewMemory(), logger: quietLogger(), tier: "co-located", store: fakeStore{version: 11},
		github: &githubStatus{repos: []string{"acme/ok", "acme/down"}, store: githubsynctest.NewMemory(), ids: &repositoryIDs{}, problems: problems}}

	code, body := call(t, a, http.MethodGet, "/status")

	want := `{"version":"` + Version + `","schemaVersion":11,"tier":"co-located",
		"jobs":{"available":0,"running":0,"retryable":0,"scheduled":0,"completed":0,"discarded":0},
		"outbox":{"pending":0,"delivered":0,"failed":0},
		"github":[{"repo":"acme/ok","bootstrap":"origin-not-passed"},{"repo":"acme/down"}]}`
	if code != http.StatusOK || !jsonEqual(t, body, want) {
		t.Fatalf("GET /status = %d %s; want 200 %s", code, body, want)
	}
}

type failingRepositories struct{}

func (failingRepositories) Repository(context.Context, string) (githubsync.RepositoryState, bool, error) {
	return githubsync.RepositoryState{}, false, errors.New("dial tcp db.internal:5432: password=hunter2")
}

func TestStatusIsUnavailableWhenTheObservationsCannotBeRead(t *testing.T) {
	a := &api{owners: ownertest.NewMemory(), logger: quietLogger(), store: fakeStore{version: 11}, github: &githubStatus{repos: []string{"acme/ok"}, store: failingRepositories{}, ids: &repositoryIDs{}, problems: &bootstrapProblems{}}}

	code, body := call(t, a, http.MethodGet, "/status")

	if code != http.StatusServiceUnavailable || strings.Contains(body, "hunter2") || !strings.Contains(body, "database unavailable") {
		t.Fatalf("GET /status = %d %s; want 503 database unavailable without the cause", code, body)
	}
}
