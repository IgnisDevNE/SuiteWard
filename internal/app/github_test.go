package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
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
		"rate limited":     {fmt.Errorf("%w: %s", &rest.RateLimitedError{Status: 403, Until: until}, secret), githubsync.ReadError{Kind: githubsync.FailureRateLimited, Status: 403, WaitUntil: until}},
		"budget exhausted": {&rest.BudgetExhaustedError{Reset: until}, githubsync.ReadError{Kind: githubsync.FailureBudgetExhausted, WaitUntil: until}},
		"unauthorized":     {fmt.Errorf("%w: %s", &rest.StatusError{Status: 401}, secret), githubsync.ReadError{Kind: githubsync.FailureUnauthorized, Status: 401}},
		"forbidden":        {fmt.Errorf("%w: %s", &rest.StatusError{Status: 403}, secret), githubsync.ReadError{Kind: githubsync.FailureForbidden, Status: 403}},
		"not found":        {fmt.Errorf("%w: %s", &rest.StatusError{Status: 404}, secret), githubsync.ReadError{Kind: githubsync.FailureNotFound, Status: 404}},
		"unprocessable":    {fmt.Errorf("%w: %s", &rest.StatusError{Status: 422}, secret), githubsync.ReadError{Kind: githubsync.FailureUnprocessable, Status: 422}},
		"other status":     {fmt.Errorf("%w: %s", &rest.StatusError{Status: 502}, secret), githubsync.ReadError{Kind: githubsync.FailureStatus, Status: 502}},
		"token request":    {&appauth.StatusError{Status: 401}, githubsync.ReadError{Kind: githubsync.FailureUnauthorized, Status: 401}},
		"app jwt refused":  {&appauth.StatusError{Endpoint: "app", Status: 401}, githubsync.ReadError{Kind: githubsync.FailureUnauthorized, Status: 401}},
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
	_, err := composeGitHub(settings, time.Minute, githubsynctest.NewMemory(), time.Now, quietLogger())
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

	if _, err := s.syncRepo(t.Context(), "someone/else"); err == nil {
		t.Fatal("syncRepo accepted a repository that is not configured")
	}
	if reader.calls != 0 {
		t.Fatal("GitHub was called for a repository that is not configured")
	}
	if _, err := s.syncRepo(t.Context(), "acme/widgets"); err != nil || reader.calls == 0 {
		t.Fatalf("syncRepo of a configured repository = %v after %d calls; want a cycle", err, reader.calls)
	}
}

type countingReader struct{ calls int }

func (c *countingReader) OpenPulls(context.Context, string) ([]githubsync.ObservedPull, githubsync.ReadInfo, error) {
	c.calls++
	return nil, githubsync.ReadInfo{Date: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), Requests: 1}, nil
}

func (c *countingReader) PullComments(context.Context, string, int) ([]githubsync.ObservedComment, githubsync.ReadInfo, error) {
	c.calls++
	return nil, githubsync.ReadInfo{Requests: 1}, nil
}

// paceReader lists no pulls and reports requests requests for its cycle, from the injected clock at, for sync
// pacing tests (D-SYNC, "Sync pacing", M1.7c).
type paceReader struct {
	calls    int
	requests int
	changed  bool  // the cycle's listing was not a 304
	err      error // fails the open pulls listing
	at       func() time.Time
}

func (r *paceReader) OpenPulls(context.Context, string) ([]githubsync.ObservedPull, githubsync.ReadInfo, error) {
	r.calls++
	return nil, githubsync.ReadInfo{Date: r.at(), Requests: r.requests, Changed: r.changed}, r.err
}

func (r *paceReader) PullComments(context.Context, string, int) ([]githubsync.ObservedComment, githubsync.ReadInfo, error) {
	return nil, githubsync.ReadInfo{}, nil
}

func paceSync(t *testing.T, reader *paceReader, now func() time.Time) *githubSync {
	t.Helper()
	reconciler, err := githubsync.NewReconciler(reader, githubsynctest.NewMemory(), time.Now, 2)
	if err != nil {
		t.Fatal(err)
	}
	return &githubSync{reconciler: reconciler, repos: []string{"acme/widgets"}, jobTimeout: time.Minute, logger: quietLogger(), now: now}
}

func TestSyncTickRunsAtOnceOnTheFirstTickAfterAStart(t *testing.T) {
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	reader := &paceReader{requests: 1, at: func() time.Time { return clock }}
	s := paceSync(t, reader, func() time.Time { return clock })

	if _, err := s.tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 {
		t.Fatalf("the first tick after a start made %d calls; want 1", reader.calls)
	}
}

func TestSyncTickIsDueEighteenSecondsAfterThePreviousStart(t *testing.T) {
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	reader := &paceReader{requests: 1, at: func() time.Time { return clock }}
	s := paceSync(t, reader, func() time.Time { return clock })
	if _, err := s.tick(t.Context()); err != nil { // the first tick, always due
		t.Fatal(err)
	}

	start := clock
	clock = start.Add(17900 * time.Millisecond)
	if _, err := s.tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 {
		t.Fatal("a tick 17.9 s after the previous start called GitHub; want it to wait")
	}

	// the 20 s periodic grid fires a little late, so a tick 19.9 s after the previous start must run
	clock = start.Add(19900 * time.Millisecond)
	if _, err := s.tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 2 {
		t.Fatal("a tick 19.9 s after the previous start did not run")
	}
}

func TestSyncTickWaitsLongerAfterAnExpensiveTick(t *testing.T) {
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	clock := start
	reader := &paceReader{requests: 420, at: func() time.Time { return clock }}
	s := paceSync(t, reader, func() time.Time { return clock })
	if _, err := s.tick(t.Context()); err != nil { // the first tick: 420 requests, which paces the next one to 56 s
		t.Fatal(err)
	}

	for _, elapsed := range []time.Duration{20 * time.Second, 55900 * time.Millisecond} {
		clock = start.Add(elapsed)
		if _, err := s.tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if reader.calls != 1 {
			t.Fatalf("a tick %v after a 420-request tick called GitHub; want it to wait until 56 s", elapsed)
		}
	}

	clock = start.Add(56 * time.Second)
	if _, err := s.tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 2 {
		t.Fatal("a tick 56 s after a 420-request tick did not run")
	}
}

func TestSyncTickWakesCommandsChecksAndMergesWhenACycleChanged(t *testing.T) {
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	s := paceSync(t, &paceReader{requests: 1, changed: true, at: func() time.Time { return clock }}, func() time.Time { return clock })

	wake, err := s.tick(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := river.Wake{river.CommandsKind, river.ChecksKind, river.MergesKind}
	if !slices.Equal(wake, want) {
		t.Fatalf("a tick with a changed cycle woke %v; want %v", wake, want)
	}
}

func TestSyncTickWakesOnlyCommandsWhenEveryRequestWasA304ButACycleWasRecorded(t *testing.T) {
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	s := paceSync(t, &paceReader{requests: 1, at: func() time.Time { return clock }}, func() time.Time { return clock })

	wake, err := s.tick(t.Context())
	if want := (river.Wake{river.CommandsKind}); err != nil || !slices.Equal(wake, want) {
		t.Fatalf("an all-304 tick that recorded a cycle = %v, %v; want %v", wake, err, want)
	}
}

func TestSyncTickWhoseEveryCycleFailedWakesNothing(t *testing.T) {
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	reader := &paceReader{requests: 1, changed: true, err: &githubsync.ReadError{Kind: githubsync.FailureTransport}, at: func() time.Time { return clock }}
	s := paceSync(t, reader, func() time.Time { return clock })

	if wake, err := s.tick(t.Context()); err != nil || len(wake) != 0 {
		t.Fatalf("a tick whose every cycle failed = %v, %v; want no wake-ups", wake, err)
	}
}

func TestSyncTickThatIsNotDueWakesNothing(t *testing.T) {
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	s := paceSync(t, &paceReader{requests: 1, changed: true, at: func() time.Time { return clock }}, func() time.Time { return clock })
	if _, err := s.tick(t.Context()); err != nil { // the first tick, always due
		t.Fatal(err)
	}

	clock = clock.Add(time.Second)
	if wake, err := s.tick(t.Context()); err != nil || len(wake) != 0 {
		t.Fatalf("a tick that was not due = %v, %v; want no wake-ups", wake, err)
	}
}

func TestSyncRepoWakesTheKindsOfATick(t *testing.T) {
	clock := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	changed := paceSync(t, &paceReader{requests: 1, changed: true, at: func() time.Time { return clock }}, time.Now)
	unchanged := paceSync(t, &paceReader{requests: 1, at: func() time.Time { return clock }}, time.Now)

	wake, err := changed.syncRepo(t.Context(), "acme/widgets")
	if want := (river.Wake{river.CommandsKind, river.ChecksKind, river.MergesKind}); err != nil || !slices.Equal(wake, want) {
		t.Fatalf("syncRepo of a changed cycle = %v, %v; want %v", wake, err, want)
	}
	if wake, err := unchanged.syncRepo(t.Context(), "acme/widgets"); err != nil || !slices.Equal(wake, river.Wake{river.CommandsKind}) {
		t.Fatalf("syncRepo of an all-304 cycle = %v, %v; want only the commands woken", wake, err)
	}
	failed := paceSync(t, &paceReader{requests: 1, changed: true, err: &githubsync.ReadError{Kind: githubsync.FailureTransport}, at: func() time.Time { return clock }}, time.Now)
	if wake, err := failed.syncRepo(t.Context(), "acme/widgets"); err != nil || len(wake) != 0 {
		t.Fatalf("syncRepo of a failed cycle = %v, %v; want no wake-ups", wake, err)
	}
	if wake, err := changed.syncRepo(t.Context(), "someone/else"); err == nil || len(wake) != 0 {
		t.Fatalf("syncRepo of an unconfigured repository = %v, %v; want an error and no wake-ups", wake, err)
	}
}

// detectorReader lists one pull and, for each successive cycle in order, the comments given for it: for the
// listing-lag detector tests.
type detectorReader struct {
	cycle    int
	dates    []time.Time
	comments [][]githubsync.ObservedComment
}

func (r *detectorReader) OpenPulls(context.Context, string) ([]githubsync.ObservedPull, githubsync.ReadInfo, error) {
	at := r.dates[r.cycle]
	return []githubsync.ObservedPull{{Number: 1, HeadSHA: "h", HeadRef: "f", BaseRef: "main", BaseSHA: "b", UpdatedAt: at}}, githubsync.ReadInfo{Date: at, Requests: 1}, nil
}

func (r *detectorReader) PullComments(context.Context, string, int) ([]githubsync.ObservedComment, githubsync.ReadInfo, error) {
	comments := r.comments[r.cycle]
	r.cycle++
	return comments, githubsync.ReadInfo{Requests: 1}, nil
}

// decodeLogLines decodes a buffer of JSON log lines, without requiring any particular field order.
func decodeLogLines(t *testing.T, logs []byte) []map[string]any {
	t.Helper()
	var entries []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(logs))
	for decoder.More() {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			t.Fatalf("log is not JSON: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestSyncRepoLogsTheListingLagDetector(t *testing.T) {
	const commentMarker = "SECRET-COMMENT-BODY-TEXT"
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	reader := &detectorReader{dates: []time.Time{t0, t1}, comments: [][]githubsync.ObservedComment{
		nil, // the first cycle observes nothing new: no CycleStartedAt to miss against yet
		{
			{CommentID: 1, AuthorID: 7, CreatedAt: t0.Add(-5 * time.Second), UpdatedAt: t0.Add(-5 * time.Second), Body: commentMarker}, // created before the previous cycle's start: missed
			{CommentID: 2, AuthorID: 7, CreatedAt: t1.Add(-time.Second), UpdatedAt: t1.Add(-time.Second), Body: commentMarker},         // created after the previous start: not missed
		},
	}}
	reconciler, err := githubsync.NewReconciler(reader, githubsynctest.NewMemory(), time.Now, 2)
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	s := &githubSync{reconciler: reconciler, repos: []string{"acme/widgets"}, jobTimeout: time.Minute, logger: slog.New(slog.NewJSONHandler(logs, nil)), now: time.Now}

	if _, err := s.syncRepo(t.Context(), "acme/widgets"); err != nil { // nothing first-observed yet: no detector line
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "github comment first observed") {
		t.Fatalf("a cycle that first-observed nothing logged the detector line: %s", logs)
	}
	if _, err := s.syncRepo(t.Context(), "acme/widgets"); err != nil {
		t.Fatal(err)
	}

	var withMissed, withoutMissed int
	for _, e := range decodeLogLines(t, logs.Bytes()) {
		if e["msg"] != "github comment first observed" {
			continue
		}
		if e["repo"] != "acme/widgets" || e["previous_cycle_started_at"] == nil {
			t.Fatalf("entry missing repo or previous_cycle_started_at: %+v", e)
		}
		switch e["comment"] {
		case float64(1):
			missed, ok := e["missed_by_ms"]
			if !ok || missed != float64(5000) {
				t.Fatalf("comment 1 (created before the previous start) has missed_by_ms %v, ok %v; want 5000", missed, ok)
			}
			withMissed++
		case float64(2):
			if _, ok := e["missed_by_ms"]; ok {
				t.Fatalf("comment 2 (created after the previous start) has missed_by_ms: %+v", e)
			}
			withoutMissed++
		default:
			t.Fatalf("unexpected detector entry: %+v", e)
		}
	}
	if withMissed != 1 || withoutMissed != 1 {
		t.Fatalf("%d lines with missed_by_ms and %d without; want 1 and 1", withMissed, withoutMissed)
	}
	if strings.Contains(logs.String(), commentMarker) {
		t.Fatal("the detector logged comment text")
	}
}

func TestSyncTickLogsTheListingLagDetector(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	reader := &detectorReader{dates: []time.Time{t0, t1}, comments: [][]githubsync.ObservedComment{
		nil,
		{
			{CommentID: 9, AuthorID: 7, CreatedAt: t1.Add(-time.Second), UpdatedAt: t1.Add(-time.Second), Body: "x"},          // created after the previous start: not missed
			{CommentID: 10, AuthorID: 7, CreatedAt: t0.Add(-5 * time.Second), UpdatedAt: t0.Add(-5 * time.Second), Body: "x"}, // created before the previous start: missed
		},
	}}
	reconciler, err := githubsync.NewReconciler(reader, githubsynctest.NewMemory(), time.Now, 2)
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	clock := t0
	s := &githubSync{reconciler: reconciler, repos: []string{"acme/widgets"}, jobTimeout: time.Minute, logger: slog.New(slog.NewJSONHandler(logs, nil)), now: func() time.Time { return clock }}

	if _, err := s.tick(t.Context()); err != nil { // the first tick: nothing first-observed yet
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute) // past the pacing window
	if _, err := s.tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	var withMissed, withoutMissed int
	for _, e := range decodeLogLines(t, logs.Bytes()) {
		if e["msg"] != "github comment first observed" {
			continue
		}
		switch missed, ok := e["missed_by_ms"]; e["comment"] {
		case float64(9):
			if ok {
				t.Fatalf("comment 9 (created after the previous start) has missed_by_ms: %+v", e)
			}
			withoutMissed++
		case float64(10):
			if !ok || missed != float64(5000) {
				t.Fatalf("comment 10 (created before the previous start) has missed_by_ms %v, ok %v; want 5000", missed, ok)
			}
			withMissed++
		default:
			t.Fatalf("unexpected detector entry: %+v", e)
		}
	}
	if withMissed != 1 || withoutMissed != 1 {
		t.Fatalf("a tick logged %d detector lines with missed_by_ms and %d without; want 1 and 1: %s", withMissed, withoutMissed, logs)
	}
}

func TestStatusReportsTheConfiguredRepositories(t *testing.T) {
	store := githubsynctest.NewMemory()
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	wait := at.Add(30 * time.Minute)
	if _, err := store.RecordCycle(t.Context(), githubsync.Cycle{Repo: "acme/ok", InstallationID: 2, At: at, StartedAt: at.Add(-2 * time.Second), Requests: 1}); err != nil { // GitHub's clock, not the local one
		t.Fatal(err)
	}
	if err := store.RecordFailure(t.Context(), githubsync.Failure{Repo: "acme/limited", InstallationID: 2, At: at, Kind: githubsync.FailureRateLimited, Status: 403, WaitUntil: wait}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFailure(t.Context(), githubsync.Failure{Repo: "acme/down", InstallationID: 2, At: at, Kind: githubsync.FailureTransport}); err != nil {
		t.Fatal(err)
	}
	a := &api{owners: ownertest.NewMemory(), logger: quietLogger(), tier: "co-located", store: fakeStore{version: 11}, github: &githubStatus{repos: []string{"acme/ok", "acme/limited", "acme/down", "acme/new"}, store: store, ids: &repositoryIDs{}, problems: &bootstrapProblems{}, checks: &bootstrapProblems{}, merges: &bootstrapProblems{}, integrity: noVerdicts()}}

	code, body := call(t, a, http.MethodGet, "/status")

	want := `{"version":"` + Version + `","schemaVersion":11,"tier":"co-located",
		"jobs":{"available":0,"running":0,"retryable":0,"scheduled":0,"completed":0,"discarded":0},
		"outbox":{"pending":0,"delivered":0,"failed":0},
		"github":[
			{"repo":"acme/ok","lastSuccess":"2026-10-11T12:00:00Z","cycleStartedAt":"2026-10-11T11:59:58Z"},
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
		github: &githubStatus{repos: []string{"acme/ok", "acme/down"}, store: githubsynctest.NewMemory(), ids: &repositoryIDs{}, problems: problems, checks: &bootstrapProblems{}, merges: &bootstrapProblems{}, integrity: noVerdicts()}}

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
	a := &api{owners: ownertest.NewMemory(), logger: quietLogger(), store: fakeStore{version: 11}, github: &githubStatus{repos: []string{"acme/ok"}, store: failingRepositories{}, ids: &repositoryIDs{}, problems: &bootstrapProblems{}, checks: &bootstrapProblems{}, merges: &bootstrapProblems{}, integrity: noVerdicts()}}

	code, body := call(t, a, http.MethodGet, "/status")

	if code != http.StatusServiceUnavailable || strings.Contains(body, "hunter2") || !strings.Contains(body, "database unavailable") {
		t.Fatalf("GET /status = %d %s; want 503 database unavailable without the cause", code, body)
	}
}
