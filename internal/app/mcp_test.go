package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/mcp"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
)

var mcpNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// fakeInserter records the sync jobs the backend asks for.
type fakeInserter struct {
	mu       sync.Mutex
	inserted []string
	result   bool
	err      error
}

func (f *fakeInserter) InsertSyncRepo(_ context.Context, repo string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserted = append(f.inserted, repo)
	return f.result, f.err
}

func (f *fakeInserter) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.inserted...)
}

// failingObservations fails every read with cause, or waits for the context when cause is nil.
type failingObservations struct{ cause error }

func (f failingObservations) fail(ctx context.Context) error {
	if f.cause != nil {
		return f.cause
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f failingObservations) Repository(ctx context.Context, _ string) (githubsync.RepositoryState, bool, error) {
	return githubsync.RepositoryState{}, false, f.fail(ctx)
}

func (f failingObservations) Pulls(ctx context.Context, _ string) ([]githubsync.PullState, error) {
	return nil, f.fail(ctx)
}

func testBackend(store observations, inserter syncInserter, logs *bytes.Buffer, repos ...string) *mcpBackend {
	return &mcpBackend{repos: repos, owners: ownertest.NewMemory(), store: store, inserter: inserter, now: func() time.Time { return mcpNow }, timeout: time.Second, logger: newLogger(logs, slog.LevelDebug), ids: &repositoryIDs{}}
}

func recordCycle(t *testing.T, store *githubsynctest.Memory, repo string, pulls ...int) {
	t.Helper()
	cycle := githubsync.Cycle{Repo: repo, InstallationID: 2, At: mcpNow, Comments: map[int][]githubsync.ObservedComment{}}
	for _, number := range pulls {
		cycle.OpenPulls = append(cycle.OpenPulls, githubsync.ObservedPull{
			Number: number, HeadSHA: "sha-" + repo, HeadRef: "feature", BaseRef: "main", BaseSHA: "base", UpdatedAt: mcpNow.Add(-time.Hour),
		})
		cycle.Comments[number] = []githubsync.ObservedComment{{CommentID: int64(number), AuthorID: 9, AuthorLogin: "octo", CreatedAt: mcpNow, UpdatedAt: mcpNow, Body: "COMMENT-BODY-OF-" + repo}}
	}
	if err := store.RecordCycle(t.Context(), cycle); err != nil {
		t.Fatal(err)
	}
}

func TestMCPStatusWithoutGitHubSaysNotConfigured(t *testing.T) {
	var logs bytes.Buffer
	b := testBackend(nil, &fakeInserter{}, &logs)

	view, err := b.Status(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	if view.GitHubConfigured || view.Repositories == nil || len(view.Repositories) != 0 {
		t.Fatalf("Status = %+v; want not configured and an empty, non-nil repository list", view)
	}
}

func TestMCPStatusReportsTheConfiguredTierWithAndWithoutGitHub(t *testing.T) {
	for name, repos := range map[string][]string{"without GitHub": nil, "with GitHub": {"acme/ok"}} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			b := testBackend(githubsynctest.NewMemory(), &fakeInserter{}, &logs, repos...)
			b.tier = "isolated"

			view, err := b.Status(t.Context())

			if err != nil {
				t.Fatal(err)
			}
			if view.Tier != "isolated" {
				t.Fatalf("Status tier = %q; want isolated", view.Tier)
			}
		})
	}
}

func TestMCPStatusReportsRepositoriesInConfigurationOrderWithOnlyOpenPulls(t *testing.T) {
	store := githubsynctest.NewMemory()
	recordCycle(t, store, "acme/second", 1, 2)
	recordCycle(t, store, "acme/second", 1) // pull 2 is no longer open
	recordCycle(t, store, "acme/first", 7)
	wait := mcpNow.Add(30 * time.Minute)
	if err := store.RecordFailure(t.Context(), githubsync.Failure{Repo: "acme/limited", InstallationID: 2, At: mcpNow, Kind: githubsync.FailureRateLimited, Status: 403, WaitUntil: wait}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	b := testBackend(store, &fakeInserter{}, &logs, "acme/second", "acme/limited", "acme/new", "acme/first")

	view, err := b.Status(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	if !view.GitHubConfigured {
		t.Fatal("GitHubConfigured is false")
	}
	var names []string
	for _, r := range view.Repositories {
		names = append(names, r.Repo)
	}
	if want := []string{"acme/second", "acme/limited", "acme/new", "acme/first"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("repositories = %v; want the configuration order %v", names, want)
	}
	second, limited, fresh, first := view.Repositories[0], view.Repositories[1], view.Repositories[2], view.Repositories[3]
	if !second.LastSuccess.Equal(mcpNow) || second.LastError != "" || !second.WaitUntil.IsZero() {
		t.Errorf("acme/second = %+v; want a success without error or wait", second)
	}
	wantPulls := []mcp.Pull{{Number: 1, HeadSHA: "sha-acme/second", BaseRef: "main", UpdatedAt: mcpNow.Add(-time.Hour)}}
	if !reflect.DeepEqual(second.Pulls, wantPulls) {
		t.Errorf("acme/second pulls = %+v; want only the open pull %+v", second.Pulls, wantPulls)
	}
	if len(first.Pulls) != 1 || first.Pulls[0].Number != 7 {
		t.Errorf("acme/first pulls = %+v; want pull 7", first.Pulls)
	}
	// The same classification text /status shows.
	if limited.LastError != "rate_limited (HTTP 403)" || !limited.WaitUntil.Equal(wait) || !limited.LastSuccess.IsZero() {
		t.Errorf("acme/limited = %+v; want the rate limit and its wait", limited)
	}
	if limited.Pulls == nil || fresh.Pulls == nil || len(limited.Pulls) != 0 || len(fresh.Pulls) != 0 {
		t.Errorf("repositories without pulls have %+v and %+v; want empty, non-nil lists", limited.Pulls, fresh.Pulls)
	}
	if !fresh.LastSuccess.IsZero() || fresh.LastError != "" || !fresh.WaitUntil.IsZero() {
		t.Errorf("acme/new = %+v; want only its name before the first cycle", fresh)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "COMMENT-BODY") || strings.Contains(string(encoded), "notOpenSince") {
		t.Errorf("the status view carries a comment body or notOpenSince: %s", encoded)
	}
}

func TestMCPStatusLastErrorMatchesTheHTTPStatusSection(t *testing.T) {
	store := githubsynctest.NewMemory()
	for _, failure := range []githubsync.Failure{
		{Repo: "acme/a", InstallationID: 2, At: mcpNow, Kind: githubsync.FailureTransport},
		{Repo: "acme/b", InstallationID: 2, At: mcpNow, Kind: githubsync.FailureStatus, Status: 502},
	} {
		if err := store.RecordFailure(t.Context(), failure); err != nil {
			t.Fatal(err)
		}
	}
	repos := []string{"acme/a", "acme/b"}
	var logs bytes.Buffer
	view, err := testBackend(store, &fakeInserter{}, &logs, repos...).Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	httpEntries, err := (&githubStatus{repos: repos, store: store, problems: &bootstrapProblems{}, checks: &bootstrapProblems{}}).report(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range repos {
		if view.Repositories[i].LastError != httpEntries[i].LastError || httpEntries[i].LastError == "" {
			t.Errorf("%s: MCP lastError %q, /status lastError %q; want the same text", repos[i], view.Repositories[i].LastError, httpEntries[i].LastError)
		}
	}
}

func TestMCPRequestSync(t *testing.T) {
	wait := mcpNow.Add(10 * time.Minute)
	for name, test := range map[string]struct {
		repos      []string // nil: GitHub is not configured
		repo       string
		failure    *githubsync.Failure
		inserter   *fakeInserter
		want       mcp.SyncResult
		wantErr    error
		wantInsert []string
	}{
		"queued": {repos: []string{"acme/widgets"}, repo: "acme/widgets", inserter: &fakeInserter{result: true},
			want: mcp.SyncResult{Queued: true}, wantInsert: []string{"acme/widgets"}},
		"coalesced": {repos: []string{"acme/widgets"}, repo: "acme/widgets", inserter: &fakeInserter{},
			want: mcp.SyncResult{Reason: mcp.ReasonAlreadyPending}, wantInsert: []string{"acme/widgets"}},
		"waiting": {repos: []string{"acme/widgets"}, repo: "acme/widgets", inserter: &fakeInserter{result: true},
			failure: &githubsync.Failure{Repo: "acme/widgets", InstallationID: 2, At: mcpNow, Kind: githubsync.FailureRateLimited, Status: 403, WaitUntil: wait},
			want:    mcp.SyncResult{Reason: mcp.ReasonWaiting, WaitUntil: wait}},
		"wait over": {repos: []string{"acme/widgets"}, repo: "acme/widgets", inserter: &fakeInserter{result: true},
			failure: &githubsync.Failure{Repo: "acme/widgets", InstallationID: 2, At: mcpNow, Kind: githubsync.FailureRateLimited, Status: 403, WaitUntil: mcpNow.Add(-time.Second)},
			want:    mcp.SyncResult{Queued: true}, wantInsert: []string{"acme/widgets"}},
		"failed without a wait": {repos: []string{"acme/widgets"}, repo: "acme/widgets", inserter: &fakeInserter{result: true},
			failure: &githubsync.Failure{Repo: "acme/widgets", InstallationID: 2, At: mcpNow, Kind: githubsync.FailureTransport},
			want:    mcp.SyncResult{Queued: true}, wantInsert: []string{"acme/widgets"}},
		"unknown":          {repos: []string{"acme/widgets"}, repo: "acme/other", inserter: &fakeInserter{result: true}, wantErr: mcp.ErrUnknownRepository},
		"exact match only": {repos: []string{"acme/widgets"}, repo: "ACME/Widgets", inserter: &fakeInserter{result: true}, wantErr: mcp.ErrUnknownRepository},
		"empty":            {repos: []string{"acme/widgets"}, repo: "", inserter: &fakeInserter{result: true}, wantErr: mcp.ErrUnknownRepository},
		"not configured":   {repo: "acme/widgets", inserter: &fakeInserter{result: true}, wantErr: mcp.ErrNotConfigured},
	} {
		t.Run(name, func(t *testing.T) {
			store := githubsynctest.NewMemory()
			if test.failure != nil {
				if err := store.RecordFailure(t.Context(), *test.failure); err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			b := testBackend(store, test.inserter, &logs, test.repos...)

			got, err := b.RequestSync(t.Context(), test.repo)

			if !errors.Is(err, test.wantErr) || (test.wantErr == nil && err != nil) {
				t.Fatalf("RequestSync error = %v; want %v", err, test.wantErr)
			}
			if err == nil && (got.Queued != test.want.Queued || got.Reason != test.want.Reason || !got.WaitUntil.Equal(test.want.WaitUntil)) {
				t.Fatalf("RequestSync = %+v; want %+v", got, test.want)
			}
			if !reflect.DeepEqual(test.inserter.requests(), test.wantInsert) {
				t.Fatalf("inserted %v; want %v", test.inserter.requests(), test.wantInsert)
			}
			if logs.Len() != 0 {
				t.Fatalf("the expected outcome %q logged: %s", name, logs.String())
			}
		})
	}
}

func TestMCPBackendFailuresAreWrappedAndLoggedWithRepositoryAndCause(t *testing.T) {
	cause := errors.New("SECRET-CAUSE db.internal")
	for name, test := range map[string]struct {
		store    observations
		inserter *fakeInserter
		call     func(b *mcpBackend) error
		inserts  int
	}{
		"status read":     {failingObservations{cause: cause}, &fakeInserter{}, func(b *mcpBackend) error { _, err := b.Status(context.Background()); return err }, 0},
		"sync state read": {failingObservations{cause: cause}, &fakeInserter{result: true}, func(b *mcpBackend) error { _, err := b.RequestSync(context.Background(), "acme/widgets"); return err }, 0},
		"sync insert":     {githubsynctest.NewMemory(), &fakeInserter{err: cause}, func(b *mcpBackend) error { _, err := b.RequestSync(context.Background(), "acme/widgets"); return err }, 1},
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			b := testBackend(test.store, test.inserter, &logs, "acme/widgets")

			err := test.call(b)

			if !errors.Is(err, cause) || errors.Is(err, mcp.ErrUnknownRepository) || errors.Is(err, mcp.ErrNotConfigured) {
				t.Fatalf("error = %v; want the cause wrapped, and not a sentinel", err)
			}
			if len(test.inserter.requests()) != test.inserts {
				t.Fatalf("inserted %v; want %d insert", test.inserter.requests(), test.inserts)
			}
			var entry map[string]any
			if lines := strings.Split(strings.TrimSpace(logs.String()), "\n"); len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &entry) != nil {
				t.Fatalf("logged %q; want exactly one entry", logs.String())
			}
			if entry["level"] != "ERROR" || entry["repo"] != "acme/widgets" || !strings.Contains(entry["error"].(string), "SECRET-CAUSE") {
				t.Fatalf("entry = %v; want an error with the repository and the cause", entry)
			}
		})
	}
}

func TestMCPBackendAppliesItsOwnTimeout(t *testing.T) {
	var logs bytes.Buffer
	b := testBackend(failingObservations{}, &fakeInserter{result: true}, &logs, "acme/widgets")
	b.timeout = 50 * time.Millisecond

	for name, call := range map[string]func() error{
		"Status":      func() error { _, err := b.Status(context.Background()); return err },
		"RequestSync": func() error { _, err := b.RequestSync(context.Background(), "acme/widgets"); return err },
	} {
		start := time.Now()
		if err := call(); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
			t.Errorf("%s = %v after %v; want the deadline, applied by the backend", name, err, time.Since(start))
		}
	}
}

// statusReader fails a cycle with a classified error, as the GitHub reader does.
type statusReader struct{ err error }

func (r statusReader) OpenPulls(context.Context, string) ([]githubsync.ObservedPull, error) {
	return nil, r.err
}

func (r statusReader) PullComments(context.Context, string, int) ([]githubsync.ObservedComment, error) {
	return nil, r.err
}

func TestAFailedCycleLogsOneWarnLineWithRepositoryKindAndStatus(t *testing.T) {
	var logs bytes.Buffer
	store := failureLog{Store: githubsynctest.NewMemory(), logger: newLogger(&logs, slog.LevelDebug)}
	reconciler, err := githubsync.NewReconciler(statusReader{&githubsync.ReadError{Kind: githubsync.FailureStatus, Status: 502}}, store, func() time.Time { return mcpNow }, 2)
	if err != nil {
		t.Fatal(err)
	}

	if err := reconciler.SyncRepo(t.Context(), "acme/widgets"); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	var entry map[string]any
	if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &entry) != nil {
		t.Fatalf("logged %q; want exactly one line", logs.String())
	}
	if entry["level"] != "WARN" || entry["repo"] != "acme/widgets" || entry["kind"] != "status" || entry["status"] != 502.0 {
		t.Fatalf("entry = %v; want a warning with repo, kind and status", entry)
	}
	delete(entry, "time")
	delete(entry, "msg")
	if len(entry) != 4 { // level, repo, kind, status: nothing GitHub sent
		t.Fatalf("entry = %v; want only level, repo, kind and status besides the message", entry)
	}
}

func TestACycleThatSucceedsOrCannotBeRecordedLogsNoFailure(t *testing.T) {
	var logs bytes.Buffer
	memory := githubsynctest.NewMemory()
	store := failureLog{Store: memory, logger: newLogger(&logs, slog.LevelDebug)}
	ok, err := githubsync.NewReconciler(statusReader{}, store, func() time.Time { return mcpNow }, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ok.SyncRepo(t.Context(), "acme/widgets"); err != nil {
		t.Fatal(err)
	}
	// A failure the store refuses is returned to the caller, not logged as recorded.
	invalid := githubsync.Failure{Repo: "acme/widgets", InstallationID: 2, At: mcpNow, Kind: "no such kind"}
	if err := store.RecordFailure(t.Context(), invalid); err == nil {
		t.Fatal("the store accepted an invalid failure")
	}
	if logs.Len() != 0 {
		t.Fatalf("logged %s; want nothing", logs.String())
	}
}
