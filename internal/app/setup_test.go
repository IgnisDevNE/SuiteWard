package app

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/totp"
)

func TestSetupUsageErrorsExitWithTwoBeforeAnythingIsCalled(t *testing.T) {
	fake := newSetupGitHub(t)
	githubVars := []string{"SUITEWARD_GITHUB_APP_ID", "SUITEWARD_GITHUB_APP_KEY", "SUITEWARD_GITHUB_INSTALLATION_ID", "SUITEWARD_GITHUB_REPOSITORIES", "SUITEWARD_GITHUB_API_URL"}
	for name, test := range map[string]struct {
		args  []string
		unset []string
		want  string
	}{
		"no owner":                   {args: []string{"--test-check", "acme/widgets=ci"}, want: "--owner"},
		"empty owner":                {args: []string{"--owner", "", "--test-check", "acme/widgets=ci"}, want: "--owner"},
		"no test check":              {args: []string{"--owner", "octo"}, want: "acme/widgets"},
		"unknown repository":         {args: []string{"--owner", "octo", "--test-check", "acme/widgets=ci", "--test-check", "acme/other=ci"}, want: "acme/other"},
		"only an unknown repository": {args: []string{"--owner", "octo", "--test-check", "acme/other=ci"}, want: "acme/other"},
		"duplicate":                  {args: []string{"--owner", "octo", "--test-check", "acme/widgets=ci", "--test-check", "acme/widgets=ci2"}, want: "acme/widgets"},
		"empty check":                {args: []string{"--owner", "octo", "--test-check", "acme/widgets="}, want: "acme/widgets"},
		"blank check":                {args: []string{"--owner", "octo", "--test-check", "acme/widgets=  "}, want: "acme/widgets"},
		"no separator":               {args: []string{"--owner", "octo", "--test-check", "acme/widgets"}, want: "acme/widgets"},
		"check too long":             {args: []string{"--owner", "octo", "--test-check", "acme/widgets=" + strings.Repeat("c", owner.MaxTestCheckBytes+1)}, want: "acme/widgets"},
		"unknown flag":               {args: []string{"--owner", "octo", "--nope"}, want: "nope"},
		"stray argument":             {args: []string{"--owner", "octo", "--test-check", "acme/widgets=ci", "stray"}, want: "stray"},
		"GitHub settings unset":      {args: []string{"--owner", "octo", "--test-check", "acme/widgets=ci"}, unset: githubVars, want: "GitHub"},
	} {
		t.Run(name, func(t *testing.T) {
			vars := fake.vars()
			vars["SUITEWARD_DATABASE_URL"] = "postgres://suiteward:s3cret-pw@127.0.0.1:1/suiteward?sslmode=disable"
			vars["SUITEWARD_ARTIFACT_DIR"] = t.TempDir()
			for _, name := range test.unset {
				delete(vars, name)
			}

			code, stdout, stderr := run(append([]string{"setup"}, test.args...), testEnv(vars))

			// The database is unreachable: a usage error is found before it is tried.
			if code != 2 || stdout != "" || !strings.Contains(stderr, "usage") || !strings.Contains(stderr, test.want) {
				t.Fatalf("setup %q = %d, stdout %q, stderr %q; want 2, nothing on stdout and a usage message naming %q", test.args, code, stdout, stderr, test.want)
			}
			if fake.count() != 0 {
				t.Fatalf("GitHub was called %d times by a usage error", fake.count())
			}
		})
	}
}

func TestSetupTestChecksMayContainSlashesSpacesAndEquals(t *testing.T) {
	flags := setupFlags{owner: "octo", checks: []string{"acme/widgets=ci / test = unit"}}

	options, err := flags.options([]string{"acme/widgets"})

	if err != nil || options.owner != "octo" || options.testChecks["acme/widgets"] != "ci / test = unit" {
		t.Fatalf("options = %+v, %v; want the check cut at the first =", options, err)
	}
}

func rightCode(t *testing.T) string {
	t.Helper()
	code, err := totp.Code(fixedSecret, totp.Step(setupNow))
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// wrongCode is a well-formed code that no step of the tolerance window has.
func wrongCode(t *testing.T) string {
	t.Helper()
	for n := 0; ; n++ {
		candidate := fmt.Sprintf("%06d", n)
		if _, err := totp.Verify(fixedSecret, candidate, setupNow, 2); err != nil {
			return candidate
		}
	}
}

// setupRun is one setup over a fake GitHub and an in-memory store.
type setupRun struct {
	fake                 *setupGitHub
	store                *ownertest.Memory
	wrap                 func(owner.Store) owner.Store
	stdin                string
	reader               io.Reader // replaces stdin when set
	stdout, stderr, logs bytes.Buffer
	tier                 config.Tier
}

// newSetupRun answers the confirmation with Enter, then sends codes.
func newSetupRun(fake *setupGitHub, codes string) *setupRun {
	return &setupRun{fake: fake, store: ownertest.NewMemory(), stdin: "\n" + codes, tier: config.TierCoLocated}
}

func (r *setupRun) run(t *testing.T) int {
	t.Helper()
	return r.runWith(t, t.Context())
}

func (r *setupRun) runWith(t *testing.T, ctx context.Context) int {
	t.Helper()
	settings := r.fake.settings()
	readers, err := composeSetupReaders(settings)
	if err != nil {
		t.Fatal(err)
	}
	var store owner.Store = r.store
	if r.wrap != nil {
		store = r.wrap(r.store)
	}
	s := &setup{
		cfg: config.Config{Tier: r.tier, GitHub: &settings}, options: setupOptions{owner: "octo", testChecks: map[string]string{setupWidgetsRepo: setupTestCheck}},
		store: store, readers: readers, stdin: r.input(), stdout: &r.stdout, stderr: &r.stderr,
		logger: newLogger(&r.logs, slog.LevelDebug), now: func() time.Time { return setupNow }, random: fixedRandom(),
	}
	return s.run(ctx)
}

func (r *setupRun) enrolled(t *testing.T) (owner.State, bool) {
	t.Helper()
	state, found, err := r.store.Current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return state, found
}

func (r *setupRun) protection(t *testing.T) []owner.ProtectionResult {
	t.Helper()
	results, err := r.store.Protection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return results
}

// requireNothingSecret fails when the logs or stderr carry the secret, a code, a token or text GitHub sent.
func (r *setupRun) requireNothingSecret(t *testing.T, codes ...string) {
	t.Helper()
	base32Secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(fixedSecret)
	keyLine := strings.Split(setupKey(), "\n")[1]
	for what, text := range map[string]string{"the logs": r.logs.String(), "stderr": r.stderr.String()} {
		for _, secret := range append([]string{base32Secret, setupReadToken, setupProtToken, setupGitHubBody, keyLine}, codes...) {
			if strings.Contains(text, secret) {
				t.Errorf("%s contain %q:\n%s", what, secret, text)
			}
		}
	}
}

func TestSetupEnrollsOnTheSecondAttemptAndPrintsTheSecretOnce(t *testing.T) {
	r := newSetupRun(newSetupGitHub(t), wrongCode(t)+"\n"+rightCode(t)+"\n")

	if code := r.run(t); code != 0 {
		t.Fatalf("setup exited %d:\n%s\n%s", code, r.stderr.String(), r.logs.String())
	}

	uri, err := totp.URI(fixedSecret, "SuiteWard", "octo")
	if err != nil {
		t.Fatal(err)
	}
	base32Secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(fixedSecret)
	if want := uri + "\n" + base32Secret + "\n"; r.stdout.String() != want {
		t.Fatalf("stdout = %q; want exactly the URI and the secret %q", r.stdout.String(), want)
	}
	if parsed, err := url.Parse(uri); err != nil || parsed.Query().Get("issuer") != "SuiteWard" || !strings.HasPrefix(parsed.Path, "/SuiteWard:octo") {
		t.Fatalf("URI %q has the wrong issuer or account", uri)
	}
	state, found := r.enrolled(t)
	want := owner.State{
		Owner: owner.Owner{AccountID: setupOwnerID, Login: "octo"}, Secret: fixedSecret, Tier: owner.TierCoLocated, PolicyRevision: owner.RevisionID(1), EnrolledAt: setupNow,
		Projects: []owner.ProjectPolicy{{RepositoryID: setupRepoID, Repo: setupWidgetsRepo, TestCheck: setupTestCheck}},
	}
	if !found || !reflect.DeepEqual(state, want) {
		t.Fatalf("enrolled = %+v, %v; want %+v", state, found, want)
	}
	// The verified step is consumed by the enrollment.
	if err := r.store.ConsumeStep(t.Context(), setupOwnerID, totp.Step(setupNow), setupNow, "github-comment:1/1"); !errors.Is(err, owner.ErrStepUsed) {
		t.Fatalf("ConsumeStep of the verified step = %v; want ErrStepUsed", err)
	}
	if !strings.Contains(r.stderr.String(), "Session Manager") {
		t.Fatalf("stderr = %q; want the warning about recorded terminal sessions", r.stderr.String())
	}
	r.requireNothingSecret(t, rightCode(t), wrongCode(t))
}

func TestSetupRecordsTheTierOfTheSettings(t *testing.T) {
	r := newSetupRun(newSetupGitHub(t), rightCode(t)+"\n")
	r.tier = config.TierIsolated

	if code := r.run(t); code != 0 {
		t.Fatalf("setup exited %d:\n%s", code, r.stderr.String())
	}
	if state, _ := r.enrolled(t); state.Tier != owner.TierIsolated {
		t.Fatalf("enrolled tier = %q; want isolated", state.Tier)
	}
}

func TestSetupWritesNothingAfterThreeWrongCodes(t *testing.T) {
	wrong := wrongCode(t)
	for name, stdin := range map[string]string{
		"three wrong codes":       strings.Repeat(wrong+"\n", 3),
		"a fourth line is unread": strings.Repeat(wrong+"\n", 3) + rightCode(t) + "\n",
		"malformed codes":         "\nabc\n1234567\n",
		"end of input":            wrong + "\n",
		"no input":                "",
	} {
		t.Run(name, func(t *testing.T) {
			r := newSetupRun(newSetupGitHub(t), stdin)

			if code := r.run(t); code != 1 {
				t.Fatalf("setup exited %d; want 1", code)
			}

			if state, found := r.enrolled(t); found {
				t.Fatalf("enrolled %+v after failed codes", state)
			}
			if results := r.protection(t); len(results) != 0 {
				t.Fatalf("recorded protection %v after failed codes", results)
			}
			r.requireNothingSecret(t, rightCode(t), wrong)
		})
	}
}

func TestSetupRefusesAnOwnerThatIsNotAUser(t *testing.T) {
	fake := newSetupGitHub(t)
	fake.userType = "Organization"
	r := newSetupRun(fake, rightCode(t)+"\n")

	if code := r.run(t); code != 1 {
		t.Fatalf("setup exited %d; want 1", code)
	}

	if r.stdout.String() != "" {
		t.Fatalf("stdout = %q; want nothing printed before the owner is accepted", r.stdout.String())
	}
	if _, found := r.enrolled(t); found {
		t.Fatal("an Organization was enrolled as the owner")
	}
	if !strings.Contains(r.stderr.String(), "not a GitHub user") {
		t.Fatalf("stderr = %q; want the fixed refusal", r.stderr.String())
	}
}

func TestSetupRefusesWhenAnyLookupFailsBeforeAnythingIsWritten(t *testing.T) {
	for name, test := range map[string]struct {
		mutate func(*setupGitHub)
		repo   string
		status string
	}{
		"owner not found":        {func(f *setupGitHub) { f.userStatus = 404 }, "", "404"},
		"owner lookup forbidden": {func(f *setupGitHub) { f.userStatus = 403 }, "", "403"},
		"repository not found":   {func(f *setupGitHub) { f.repoStatus = 404 }, setupWidgetsRepo, "404"},
		"repository server":      {func(f *setupGitHub) { f.repoStatus = 502 }, setupWidgetsRepo, "502"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newSetupGitHub(t)
			test.mutate(fake)
			r := newSetupRun(fake, rightCode(t)+"\n")

			if code := r.run(t); code != 1 {
				t.Fatalf("setup exited %d; want 1", code)
			}

			if r.stdout.String() != "" {
				t.Fatalf("stdout = %q; the secret was printed although a lookup failed", r.stdout.String())
			}
			if _, found := r.enrolled(t); found || len(r.protection(t)) != 0 {
				t.Fatal("something was written although a lookup failed")
			}
			// The log has the repository and the status, never text GitHub sent.
			var entry map[string]any
			for _, line := range strings.Split(r.logs.String(), "\n") {
				if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == "github lookup failed" {
					break
				}
				entry = nil
			}
			if entry == nil || fmt.Sprint(entry["status"]) != test.status || (test.repo != "" && entry["repo"] != test.repo) {
				t.Fatalf("logs = %s; want one lookup failure with status %s and repo %q", r.logs.String(), test.status, test.repo)
			}
			r.requireNothingSecret(t)
		})
	}
}

func TestSetupMintsTheTwoTokensWithTheirOwnPermissions(t *testing.T) {
	fake := newSetupGitHub(t)
	r := newSetupRun(fake, rightCode(t)+"\n")

	if code := r.run(t); code != 0 {
		t.Fatalf("setup exited %d:\n%s", code, r.stderr.String())
	}

	readMap := map[string]any{"repositories": []any{"widgets"}, "permissions": map[string]any{"metadata": "read", "pull_requests": "read", "contents": "read"}}
	protectionMap := map[string]any{"repositories": []any{"widgets"}, "permissions": map[string]any{"metadata": "read", "administration": "read"}}
	got := fake.tokenRequests
	if len(got) != 2 || !(slices.ContainsFunc(got, func(g map[string]any) bool { return reflect.DeepEqual(g, readMap) }) &&
		slices.ContainsFunc(got, func(g map[string]any) bool { return reflect.DeepEqual(g, protectionMap) })) {
		t.Fatalf("token requests = %v; want the read map and the protection map, one each", got)
	}
	for _, path := range []string{"/users/octo", "/repos/" + setupWidgetsRepo} {
		if !fake.requestedWith(path, setupReadToken) {
			t.Errorf("%s was not read with the synchronization token", path)
		}
	}
	for _, path := range []string{"/repos/" + setupWidgetsRepo + "/rules/branches/main", "/repos/" + setupWidgetsRepo + "/branches/main/protection"} {
		if !fake.requestedWith(path, setupProtToken) {
			t.Errorf("%s was not read with the protection token", path)
		}
	}
}

func TestSetupStoresEachProtectionResultAndExitsZero(t *testing.T) {
	const (
		rules = `[{"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_id":7,"parameters":{
			"strict_required_status_checks_policy":true,
			"required_status_checks":[{"context":"SuiteWard / Contract","integration_id":1},{"context":"ci / test"}]}}]`
		classic = `{"required_status_checks":{"strict":true,"checks":[{"context":"SuiteWard / Contract","app_id":1},{"context":"ci / test","app_id":-1}]},"enforce_admins":{"enabled":true}}`
	)
	pinned := []protection.Check{{Context: protection.ContractCheck, SourceID: setupAppID}, {Context: setupTestCheck}}
	unknown := protection.Unknown()
	for name, test := range map[string]struct {
		mutate func(*setupGitHub)
		want   protection.Result
		status string // the HTTP status the log carries when the read failed
	}{
		"enforced by a ruleset": {
			func(f *setupGitHub) { f.rules = rules },
			protection.Evaluate(protection.Facts{FromRuleset: true, Rules: []protection.Rule{{RulesetID: 7, Checks: pinned, Strict: true}}}, setupAppID, setupTestCheck), "",
		},
		"enforced by classic protection": {
			func(f *setupGitHub) { f.classic = classic },
			protection.Evaluate(protection.Facts{Classic: protection.Classic{
				State: protection.ClassicPresent, Strict: true, EnforceAdmins: true,
				Checks: []protection.Check{{Context: protection.ContractCheck, SourceID: setupAppID}, {Context: setupTestCheck, SourceID: -1}},
			}}, setupAppID, setupTestCheck), "",
		},
		"findings":                                     {func(*setupGitHub) {}, protection.Evaluate(protection.Facts{}, setupAppID, setupTestCheck), ""},
		"enforcement unavailable":                      {func(f *setupGitHub) { f.private, f.rulesStatus = true, 403 }, protection.Evaluate(protection.Facts{Private: true, RulesForbidden: true}, setupAppID, setupTestCheck), ""},
		"unknown when the rules cannot be read":        {func(f *setupGitHub) { f.rulesStatus = 500 }, unknown, "500"},
		"unknown when the protection token is refused": {func(f *setupGitHub) { f.protectionMint = 403 }, unknown, "403"},
		"unknown when the token's permissions differ":  {func(f *setupGitHub) { f.mismatch = true }, unknown, ""},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newSetupGitHub(t)
			test.mutate(fake)
			r := newSetupRun(fake, rightCode(t)+"\n")

			if code := r.run(t); code != 0 {
				t.Fatalf("setup exited %d; want 0 whatever the protection result:\n%s", code, r.stderr.String())
			}

			if _, found := r.enrolled(t); !found {
				t.Fatal("the owner was not enrolled")
			}
			want := owner.ProtectionResult{RepositoryID: setupRepoID, Result: string(test.want.Kind), Findings: wantStrings(test.want.Findings), Caveats: wantStrings(test.want.Caveats), CheckedAt: setupNow}
			if got := r.protection(t); !reflect.DeepEqual(got, []owner.ProtectionResult{want}) {
				t.Fatalf("stored %+v; want %+v", got, want)
			}
			if !strings.Contains(r.stderr.String(), setupWidgetsRepo+": "+string(test.want.Kind)) {
				t.Fatalf("stderr = %q; want the result of the repository", r.stderr.String())
			}
			for _, instruction := range test.want.Instructions {
				if !strings.Contains(r.stderr.String(), instruction) {
					t.Fatalf("stderr = %q; want the instruction %q", r.stderr.String(), instruction)
				}
			}
			if test.status != "" && !strings.Contains(r.logs.String(), `"status":`+test.status) {
				t.Fatalf("logs = %s; want the HTTP status %s", r.logs.String(), test.status)
			}
			// The only thing on stdout is the enrollment.
			if strings.Contains(r.stdout.String(), setupWidgetsRepo) {
				t.Fatalf("stdout = %q carries a protection result", r.stdout.String())
			}
			r.requireNothingSecret(t, rightCode(t))
		})
	}
}

func wantStrings(codes []protection.Code) []string {
	out := make([]string, len(codes))
	for i, c := range codes {
		out[i] = string(c)
	}
	return out
}

// failingStore fails the call it overrides.
type failingStore struct {
	owner.Store
	enroll, record error
}

func (f failingStore) Enroll(ctx context.Context, e owner.Enrollment) error {
	if f.enroll != nil {
		return f.enroll
	}
	return f.Store.Enroll(ctx, e)
}

func (f failingStore) RecordProtection(ctx context.Context, p owner.ProtectionResult) error {
	if f.record != nil {
		return f.record
	}
	return f.Store.RecordProtection(ctx, p)
}

func TestSetupEnrollmentFailuresExitOneWithFixedMessages(t *testing.T) {
	secretCause := errors.New("dial tcp db.internal:5432: password=hunter2")
	for name, test := range map[string]struct {
		cause error
		want  string
	}{
		"step already used": {owner.ErrStepUsed, "already used"},
		"refused":           {fmt.Errorf("%w: another owner cannot take over a project that has a Suite", owner.ErrInvalidState), "another owner cannot take over"},
		"store failure":     {secretCause, "could not record the enrollment"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSetupRun(newSetupGitHub(t), rightCode(t)+"\n")
			r.wrap = func(s owner.Store) owner.Store { return failingStore{Store: s, enroll: test.cause} }

			if code := r.run(t); code != 1 {
				t.Fatalf("setup exited %d; want 1", code)
			}

			if !strings.Contains(r.stderr.String(), test.want) || strings.Contains(r.stderr.String(), "hunter2") {
				t.Fatalf("stderr = %q; want %q and not the cause of a store failure", r.stderr.String(), test.want)
			}
			if _, found := r.enrolled(t); found || len(r.protection(t)) != 0 {
				t.Fatal("something was written although the enrollment failed")
			}
		})
	}
}

func TestSetupStillSucceedsWhenAProtectionResultCannotBeStoredAfterEnrolling(t *testing.T) {
	r := newSetupRun(newSetupGitHub(t), rightCode(t)+"\n")
	r.wrap = func(s owner.Store) owner.Store {
		return failingStore{Store: s, record: errors.New("dial tcp db.internal:5432: password=hunter2")}
	}

	if code := r.run(t); code != 0 {
		t.Fatalf("setup exited %d; want 0: the enrollment succeeded, and the protection job records the result again", code)
	}

	if _, found := r.enrolled(t); !found {
		t.Fatal("the enrollment was lost")
	}
	if !strings.Contains(r.stderr.String(), setupWidgetsRepo+" could not be stored") || strings.Contains(r.stderr.String(), "hunter2") {
		t.Fatalf("stderr = %q; want the repository and not the cause", r.stderr.String())
	}
}

func TestProtectionFactsMapsEveryFieldAndClassicState(t *testing.T) {
	for _, state := range []struct {
		from github.ClassicState
		to   protection.ClassicState
	}{
		{github.ClassicAbsent, protection.ClassicAbsent},
		{github.ClassicUnreadable, protection.ClassicUnreadable},
		{github.ClassicPresent, protection.ClassicPresent},
	} {
		got, err := protectionFacts(github.Protection{Classic: github.Classic{State: state.from}}, false)
		if err != nil || got.Classic.State != state.to {
			t.Errorf("classic state %v mapped to %v, %v; want %v", state.from, got.Classic.State, err, state.to)
		}
	}
	if _, err := protectionFacts(github.Protection{Classic: github.Classic{State: github.ClassicState(9)}}, false); err == nil {
		t.Error("an unknown classic state was mapped")
	}

	got, err := protectionFacts(github.Protection{
		RulesForbidden: true, FromRuleset: true,
		Rules:   []github.Rule{{RulesetID: 7, Strict: true, Checks: []github.Check{{Context: "a", SourceID: 1}, {Context: "b", SourceID: 2}}}, {RulesetID: 8}},
		Classic: github.Classic{State: github.ClassicPresent, Strict: true, EnforceAdmins: true, Checks: []github.Check{{Context: "c", SourceID: -1}}},
	}, true)
	want := protection.Facts{
		Private: true, RulesForbidden: true, FromRuleset: true,
		Rules:   []protection.Rule{{RulesetID: 7, Strict: true, Checks: []protection.Check{{Context: "a", SourceID: 1}, {Context: "b", SourceID: 2}}}, {RulesetID: 8}},
		Classic: protection.Classic{State: protection.ClassicPresent, Strict: true, EnforceAdmins: true, Checks: []protection.Check{{Context: "c", SourceID: -1}}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("protectionFacts = %+v, %v; want %+v", got, err, want)
	}
}

// input is the stdin of the run: the text, or the reader that replaces it.
func (r *setupRun) input() io.Reader {
	if r.reader != nil {
		return r.reader
	}
	return strings.NewReader(r.stdin)
}

func TestSetupAbortsBeforeTheSecretIsGeneratedUnlessTheOwnerConfirms(t *testing.T) {
	for name, stdin := range map[string]string{
		"end of input":           "",
		"a non-empty line":       "no\n",
		"a code instead":         rightCode(t) + "\n" + rightCode(t) + "\n",
		"yes is not a confirmer": "yes\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := newSetupRun(newSetupGitHub(t), "")
			r.stdin = stdin

			if code := r.run(t); code != 1 {
				t.Fatalf("setup exited %d; want 1", code)
			}

			if r.stdout.String() != "" {
				t.Fatalf("stdout = %q; the secret was printed without a confirmation", r.stdout.String())
			}
			if _, found := r.enrolled(t); found || len(r.protection(t)) != 0 {
				t.Fatal("something was written without a confirmation")
			}
			if !strings.Contains(r.stderr.String(), "Session Manager") {
				t.Fatalf("stderr = %q; want the logging warning before the confirmation", r.stderr.String())
			}
			r.requireNothingSecret(t, rightCode(t))
		})
	}
}

func TestSetupWarnsBeforeTheSecretAndPromisesNoAbort(t *testing.T) {
	r := newSetupRun(newSetupGitHub(t), rightCode(t)+"\n")

	if code := r.run(t); code != 0 {
		t.Fatalf("setup exited %d:\n%s", code, r.stderr.String())
	}

	if strings.Contains(r.stderr.String(), "Ctrl-C") {
		t.Fatalf("stderr = %q; the warning must not promise an abort that cannot happen", r.stderr.String())
	}
	if !strings.Contains(r.stderr.String(), "Session Manager") {
		t.Fatalf("stderr = %q; want the logging warning", r.stderr.String())
	}
}

// cancelAtTheCodePrompt answers the confirmation, then cancels the context and
// blocks, as a terminal does while the owner reads the secret.
type cancelAtTheCodePrompt struct {
	cancel  context.CancelFunc
	release chan struct{}
	reads   int
}

func (c *cancelAtTheCodePrompt) Read(p []byte) (int, error) {
	c.reads++
	if c.reads == 1 {
		return copy(p, "\n"), nil
	}
	c.cancel()
	<-c.release
	return 0, io.EOF
}

func TestSetupStopsAtTheCodePromptWhenTheContextIsCancelled(t *testing.T) {
	r := newSetupRun(newSetupGitHub(t), "")
	ctx, cancel := context.WithCancel(t.Context())
	reader := &cancelAtTheCodePrompt{cancel: cancel, release: make(chan struct{})}
	t.Cleanup(func() { close(reader.release) })
	r.reader = reader
	done := make(chan int, 1)

	go func() { done <- r.runWith(t, ctx) }()

	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("setup exited %d; want 1", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("setup did not return after the context was cancelled")
	}
	if _, found := r.enrolled(t); found || len(r.protection(t)) != 0 {
		t.Fatal("something was written after the context was cancelled")
	}
}
