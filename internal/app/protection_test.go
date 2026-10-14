package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
)

// pinnedRules require the contract check pinned to the App and the test check, strictly.
const pinnedRules = `[{"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_id":7,"parameters":{
	"strict_required_status_checks_policy":true,
	"required_status_checks":[{"context":"SuiteWard / Contract","integration_id":1},{"context":"ci / test"}]}}]`

// protectionRun is one protection check over a fake GitHub and an in-memory store.
type protectionRun struct {
	fake  *setupGitHub
	store *ownertest.Memory
	ids   *repositoryIDs
	logs  bytes.Buffer
	check *protectionCheck
}

func newProtectionRun(t *testing.T, fake *setupGitHub) *protectionRun {
	t.Helper()
	settings := fake.settings()
	readers, err := composeSetupReaders(settings)
	if err != nil {
		t.Fatal(err)
	}
	r := &protectionRun{fake: fake, store: ownertest.NewMemory(), ids: &repositoryIDs{}}
	r.check = &protectionCheck{
		repos: settings.Repositories, appID: setupAppID, readers: readers, store: r.store, ids: r.ids,
		logger: newLogger(&r.logs, slog.LevelDebug), now: func() time.Time { return setupNow }, within: time.Hour,
	}
	return r
}

// enroll records an owner and the test check of the repository, as setup does.
func (r *protectionRun) enroll(t *testing.T) {
	t.Helper()
	err := r.store.Enroll(t.Context(), owner.Enrollment{
		Owner: owner.Owner{AccountID: setupOwnerID, Login: "octo"}, Secret: fixedSecret, Step: 1, Tier: owner.TierCoLocated, EnrolledAt: setupNow,
		Projects: []owner.ProjectPolicy{{RepositoryID: setupRepoID, Repo: setupWidgetsRepo, TestCheck: setupTestCheck}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (r *protectionRun) results(t *testing.T) []owner.ProtectionResult {
	t.Helper()
	results, err := r.store.Protection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return results
}

func storedResult(result protection.Result) owner.ProtectionResult {
	return owner.ProtectionResult{RepositoryID: setupRepoID, Result: string(result.Kind), Findings: wantStrings(result.Findings), Caveats: wantStrings(result.Caveats), CheckedAt: setupNow}
}

func (r *protectionRun) statusBody(t *testing.T) string {
	t.Helper()
	a := &api{
		owners: r.store, logger: quietLogger(), tier: "co-located", store: fakeStore{version: 1},
		github: &githubStatus{repos: []string{setupWidgetsRepo}, store: githubsynctest.NewMemory(), ids: r.ids, problems: &bootstrapProblems{}},
	}
	code, body := call(t, a, http.MethodGet, "/status")
	if code != http.StatusOK {
		t.Fatalf("GET /status = %d %s", code, body)
	}
	return body
}

func TestProtectionCheckRecordsTheFindingsOfAnUnprotectedBranch(t *testing.T) {
	r := newProtectionRun(t, newSetupGitHub(t))
	r.enroll(t)

	if err := r.check.run(t.Context()); err != nil {
		t.Fatalf("run = %v; want a nil error: a GitHub answer never fails the job", err)
	}

	want := storedResult(protection.Evaluate(protection.Facts{}, setupAppID, setupTestCheck))
	if want.Result != string(protection.KindFindings) {
		t.Fatalf("test setup: %+v is not a findings result", want)
	}
	if got := r.results(t); !reflect.DeepEqual(got, []owner.ProtectionResult{want}) {
		t.Fatalf("stored %+v; want %+v", got, want)
	}
	if !r.fake.requestedWith("/repos/"+setupWidgetsRepo, setupReadToken) || !r.fake.requestedWith("/repos/"+setupWidgetsRepo+"/rules/branches/main", setupProtToken) {
		t.Fatal("the repository was not read with the synchronization token and its rules with the protection token")
	}
}

func TestProtectionCheckRecordsAnUnenforceablePrivateRepository(t *testing.T) {
	fake := newSetupGitHub(t)
	fake.private, fake.rulesStatus = true, http.StatusForbidden
	r := newProtectionRun(t, fake)
	r.enroll(t)

	if err := r.check.run(t.Context()); err != nil {
		t.Fatal(err)
	}

	want := storedResult(protection.Evaluate(protection.Facts{Private: true, RulesForbidden: true}, setupAppID, setupTestCheck))
	if want.Result != string(protection.KindEnforcementUnavailable) {
		t.Fatalf("test setup: %+v is not enforcement-unavailable", want)
	}
	if got := r.results(t); !reflect.DeepEqual(got, []owner.ProtectionResult{want}) {
		t.Fatalf("stored %+v; want %+v", got, want)
	}
}

func TestProtectionCheckRecordsUnknownForAReadThatFailsAndNeverKeepsTheErrorText(t *testing.T) {
	for name, mutate := range map[string]func(*setupGitHub){
		"the protection token is refused":   func(f *setupGitHub) { f.protectionMint = http.StatusForbidden },
		"the rules cannot be read":          func(f *setupGitHub) { f.rulesStatus = http.StatusInternalServerError },
		"the token's permissions differ":    func(f *setupGitHub) { f.mismatch = true },
		"the protection answer is unusable": func(f *setupGitHub) { f.classic = "{" },
	} {
		t.Run(name, func(t *testing.T) {
			fake := newSetupGitHub(t)
			mutate(fake)
			r := newProtectionRun(t, fake)
			r.enroll(t)

			if err := r.check.run(t.Context()); err != nil {
				t.Fatalf("run = %v; want a nil error", err)
			}

			want := storedResult(protection.Unknown())
			got := r.results(t)
			if !reflect.DeepEqual(got, []owner.ProtectionResult{want}) {
				t.Fatalf("stored %+v; want %+v", got, want)
			}
			stored, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			for where, text := range map[string]string{"the logs": r.logs.String(), "the stored result": string(stored), "/status": r.statusBody(t)} {
				for _, secret := range []string{setupGitHubBody, setupReadToken, setupProtToken} {
					if strings.Contains(text, secret) {
						t.Errorf("%s contain %q:\n%s", where, secret, text)
					}
				}
			}
			if !strings.Contains(r.logs.String(), setupWidgetsRepo) {
				t.Errorf("logs = %s; want the repository", r.logs.String())
			}
		})
	}
}

func TestProtectionCheckBeforeSetupHasNoTestCheck(t *testing.T) {
	fake := newSetupGitHub(t)
	fake.rules = pinnedRules
	r := newProtectionRun(t, fake)

	if err := r.check.run(t.Context()); err != nil {
		t.Fatal(err)
	}

	want := storedResult(protection.Evaluate(protection.Facts{
		FromRuleset: true,
		Rules:       []protection.Rule{{RulesetID: 7, Strict: true, Checks: []protection.Check{{Context: protection.ContractCheck, SourceID: setupAppID}, {Context: setupTestCheck}}}},
	}, setupAppID, ""))
	if want.Result != string(protection.KindFindings) || !reflect.DeepEqual(want.Findings, []string{string(protection.MissingTestCheck)}) {
		t.Fatalf("test setup: %+v is not a lone missing-test-check", want)
	}
	if got := r.results(t); !reflect.DeepEqual(got, []owner.ProtectionResult{want}) {
		t.Fatalf("stored %+v; want %+v before setup", got, want)
	}

	// Once setup recorded the test check, the same branch is enforced.
	r.enroll(t)
	if err := r.check.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := r.results(t); len(got) != 1 || got[0].Result != string(protection.KindEnforced) {
		t.Fatalf("stored %+v; want enforced once the test check is configured", got)
	}
}

func TestProtectionCheckSkipsARepositoryThatWaitsForTheRateLimit(t *testing.T) {
	for name, mutate := range map[string]func(*setupGitHub, bool){
		"the rules read": func(f *setupGitHub, limited bool) { f.rulesLimited = limited },
		"the repository": func(f *setupGitHub, limited bool) { f.repoLimited = limited },
	} {
		t.Run(name, func(t *testing.T) {
			fake := newSetupGitHub(t)
			r := newProtectionRun(t, fake)
			r.enroll(t)
			mutate(fake, true)

			if err := r.check.run(t.Context()); err != nil {
				t.Fatalf("run = %v; want a nil error: a wait is not a failure", err)
			}

			if got := r.results(t); len(got) != 0 {
				t.Fatalf("stored %+v; want nothing recorded until the next run", got)
			}
			mutate(fake, false)
			if err := r.check.run(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := r.results(t); len(got) != 1 {
				t.Fatalf("stored %+v after the wait; want the repository verified", got)
			}
		})
	}
}

func TestProtectionCheckRecordsUnknownForARepositoryItCannotReadOnlyOnceItKnowsTheId(t *testing.T) {
	fake := newSetupGitHub(t)
	r := newProtectionRun(t, fake)
	fake.repoStatus = http.StatusInternalServerError

	if err := r.check.run(t.Context()); err != nil {
		t.Fatalf("run = %v; want a nil error", err)
	}

	if got := r.results(t); len(got) != 0 {
		t.Fatalf("stored %+v; want nothing: without the repository id there is nothing to record against", got)
	}
	if !strings.Contains(r.logs.String(), setupWidgetsRepo) || strings.Contains(r.logs.String(), setupGitHubBody) {
		t.Fatalf("logs = %s; want the repository and no text GitHub sent", r.logs.String())
	}

	fake.repoStatus = 0
	if err := r.check.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	fake.repoStatus = http.StatusInternalServerError
	if err := r.check.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := r.results(t); len(got) != 1 || got[0].RepositoryID != setupRepoID || got[0].Result != string(protection.KindUnknown) {
		t.Fatalf("stored %+v; want unknown for the repository identified by an earlier read", got)
	}
}

// brokenOwners fails the calls it overrides.
type brokenOwners struct {
	owner.Store
	current, record, protection error
}

func (b brokenOwners) Current(ctx context.Context) (owner.State, bool, error) {
	if b.current != nil {
		return owner.State{}, false, b.current
	}
	return b.Store.Current(ctx)
}

func (b brokenOwners) RecordProtection(ctx context.Context, p owner.ProtectionResult) error {
	if b.record != nil {
		return b.record
	}
	return b.Store.RecordProtection(ctx, p)
}

func (b brokenOwners) Protection(ctx context.Context) ([]owner.ProtectionResult, error) {
	if b.protection != nil {
		return nil, b.protection
	}
	return b.Store.Protection(ctx)
}

func TestProtectionCheckFailsOnlyForAStoreError(t *testing.T) {
	cause := errors.New("store is down")
	for name, broken := range map[string]func(owner.Store) owner.Store{
		"reading the enrollment": func(s owner.Store) owner.Store { return brokenOwners{Store: s, current: cause} },
		"recording the result":   func(s owner.Store) owner.Store { return brokenOwners{Store: s, record: cause} },
	} {
		t.Run(name, func(t *testing.T) {
			r := newProtectionRun(t, newSetupGitHub(t))
			r.check.store = broken(r.store)

			if err := r.check.run(t.Context()); !errors.Is(err, cause) {
				t.Fatalf("run = %v; want the store error", err)
			}
		})
	}
}

func TestProtectionCheckStopsWhenTheJobIsCancelled(t *testing.T) {
	r := newProtectionRun(t, newSetupGitHub(t))
	r.enroll(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := r.check.run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run = %v; want the cancellation, not a recorded unknown", err)
	}

	if got := r.results(t); len(got) != 0 {
		t.Fatalf("stored %+v; want nothing from a cancelled job", got)
	}
}

func TestStatusShowsTheLatestProtectionOfEachRepositoryAndNothingBeforeTheFirstVerification(t *testing.T) {
	r := newProtectionRun(t, newSetupGitHub(t))
	r.enroll(t)

	var before struct {
		GitHub []map[string]any `json:"github"`
	}
	if err := json.Unmarshal([]byte(r.statusBody(t)), &before); err != nil || len(before.GitHub) != 1 {
		t.Fatalf("/status = %v, %v", before, err)
	}
	if _, listed := before.GitHub[0]["protection"]; listed {
		t.Fatalf("/status github entry = %v; want no protection before the first verification", before.GitHub[0])
	}

	if err := r.check.run(t.Context()); err != nil {
		t.Fatal(err)
	}

	var after struct {
		GitHub []struct {
			Repo       string `json:"repo"`
			Protection struct {
				Result    string   `json:"result"`
				Findings  []string `json:"findings"`
				Caveats   []string `json:"caveats"`
				CheckedAt string   `json:"checkedAt"`
			} `json:"protection"`
		} `json:"github"`
	}
	if err := json.Unmarshal([]byte(r.statusBody(t)), &after); err != nil || len(after.GitHub) != 1 {
		t.Fatalf("/status = %v, %v", after, err)
	}
	want := protection.Evaluate(protection.Facts{}, setupAppID, setupTestCheck)
	got := after.GitHub[0].Protection
	if got.Result != string(want.Kind) || !reflect.DeepEqual(got.Findings, wantStrings(want.Findings)) || got.Caveats == nil || len(got.Caveats) != 0 || got.CheckedAt != "2026-10-13T12:00:10Z" {
		t.Fatalf("/status protection = %+v; want %+v with the check time", got, want)
	}
}

func TestStatusIsUnavailableWhenTheProtectionCannotBeRead(t *testing.T) {
	r := newProtectionRun(t, newSetupGitHub(t))
	r.ids.set(setupWidgetsRepo, setupRepoID)
	a := &api{
		owners: brokenOwners{Store: r.store, protection: errors.New("dial tcp db.internal:5432: password=hunter2")}, logger: quietLogger(), store: fakeStore{version: 1},
		github: &githubStatus{repos: []string{setupWidgetsRepo}, store: githubsynctest.NewMemory(), ids: r.ids, problems: &bootstrapProblems{}},
	}

	code, body := call(t, a, http.MethodGet, "/status")

	if code != http.StatusServiceUnavailable || strings.Contains(body, "hunter2") || !strings.Contains(body, "database unavailable") {
		t.Fatalf("GET /status = %d %s; want 503 database unavailable without the cause", code, body)
	}
}

func TestMCPStatusShowsTheLatestProtectionOfEachRepository(t *testing.T) {
	r := newProtectionRun(t, newSetupGitHub(t))
	r.enroll(t)
	var logs bytes.Buffer
	b := testBackend(githubsynctest.NewMemory(), &fakeInserter{}, &logs, setupWidgetsRepo, "acme/other")
	b.owners, b.ids = r.store, r.ids

	view, err := b.Status(t.Context())
	if err != nil || view.Repositories[0].Protection != nil {
		t.Fatalf("Status = %+v, %v; want no protection before the first verification", view, err)
	}
	if err := r.check.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	view, err = b.Status(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	want := protection.Evaluate(protection.Facts{}, setupAppID, setupTestCheck)
	got := view.Repositories[0].Protection
	if got == nil || got.Result != string(want.Kind) || !reflect.DeepEqual(got.Findings, wantStrings(want.Findings)) || got.Caveats == nil || len(got.Caveats) != 0 || !got.CheckedAt.Equal(setupNow) {
		t.Fatalf("protection = %+v; want %+v checked at %v", got, want, setupNow)
	}
	if view.Repositories[1].Protection != nil {
		t.Fatalf("protection of an unverified repository = %+v; want none", view.Repositories[1].Protection)
	}

	b.owners = brokenOwners{Store: r.store, protection: errors.New("SECRET-CAUSE")}
	if _, err := b.Status(t.Context()); err == nil {
		t.Fatal("Status succeeded although the protection results cannot be read")
	}
}

func TestProtectionCheckSkipsARepositoryThatTheSharedBudgetRefuses(t *testing.T) {
	fake := newSetupGitHub(t)
	fake.repoBudget = true
	r := newProtectionRun(t, fake)
	r.enroll(t)

	if err := r.check.run(t.Context()); err != nil {
		t.Fatalf("run = %v; want a nil error: an exhausted budget is a wait, not a failure", err)
	}

	if got := r.results(t); len(got) != 0 {
		t.Fatalf("stored %+v; want nothing recorded until the next run", got)
	}
	// The repository read taught the budget its window; the next request is refused without calling GitHub.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, request := range fake.requests {
		if strings.Contains(request, "/rules/") || strings.Contains(request, "/protection ") {
			t.Fatalf("GitHub was asked %q although the budget was exhausted", request)
		}
	}
}

func TestProtectionCheckRecordsUnknownForAnEnrolledRepositoryItCannotReadAfterARestart(t *testing.T) {
	fake := newSetupGitHub(t)
	fake.repoStatus = http.StatusInternalServerError
	r := newProtectionRun(t, fake) // a fresh process: it has read no repository yet, but setup stored the enrolled ids
	r.enroll(t)

	if err := r.check.run(t.Context()); err != nil {
		t.Fatalf("run = %v; want a nil error", err)
	}

	want := storedResult(protection.Unknown())
	if got := r.results(t); !reflect.DeepEqual(got, []owner.ProtectionResult{want}) {
		t.Fatalf("stored %+v; want %+v: the enrollment names the repository", got, want)
	}
	if id, known := r.ids.lookup(setupWidgetsRepo); !known || id != setupRepoID {
		t.Fatalf("lookup = %d, %v; want /status to find the stored result too", id, known)
	}
}

// afterRecord runs a hook once a result has been stored: the first repository is finished.
type afterRecord struct {
	owner.Store
	hook func()
}

func (a afterRecord) RecordProtection(ctx context.Context, p owner.ProtectionResult) error {
	err := a.Store.RecordProtection(ctx, p)
	if err == nil {
		a.hook()
	}
	return err
}

// requestedAny reports whether any request reached path.
func (f *setupGitHub) requestedAny(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, request := range f.requests {
		if strings.Contains(request, " "+path+" ") {
			return true
		}
	}
	return false
}

func TestProtectionCheckStartsNoRepositoryAfterItsDeadline(t *testing.T) {
	r := newProtectionRun(t, newSetupGitHub(t))
	r.enroll(t)
	r.check.repos = []string{setupWidgetsRepo, "acme/other"}
	r.check.within = time.Minute
	now := setupNow
	r.check.now = func() time.Time { return now }
	r.check.store = afterRecord{Store: r.store, hook: func() { now = now.Add(time.Hour) }} // the first repository takes longer than the deadline

	if err := r.check.run(t.Context()); err != nil {
		t.Fatalf("run = %v; want a nil error: reaching the deadline is not a failure", err)
	}

	if got := r.results(t); len(got) != 1 || got[0].RepositoryID != setupRepoID {
		t.Fatalf("stored %+v; want only the repository that finished", got)
	}
	if r.fake.requestedAny("/repos/acme/other") {
		t.Fatal("the second repository was read after the deadline")
	}
	if !strings.Contains(r.logs.String(), `"skipped":1`) {
		t.Fatalf("logs = %s; want a line naming the one skipped repository", r.logs.String())
	}
}

func TestProtectionCheckKeepsWhatItFinishedWhenCancelledBetweenRepositories(t *testing.T) {
	r := newProtectionRun(t, newSetupGitHub(t))
	r.enroll(t)
	r.check.repos = []string{setupWidgetsRepo, "acme/other"}
	ctx, cancel := context.WithCancel(t.Context())
	r.check.store = afterRecord{Store: r.store, hook: cancel}

	if err := r.check.run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run = %v; want the cancellation", err)
	}

	if got := r.results(t); len(got) != 1 || got[0].RepositoryID != setupRepoID {
		t.Fatalf("stored %+v; want the finished repository only, nothing for the unfinished one", got)
	}
	if r.fake.requestedAny("/repos/acme/other") {
		t.Fatal("the second repository was read after the cancellation")
	}
}
