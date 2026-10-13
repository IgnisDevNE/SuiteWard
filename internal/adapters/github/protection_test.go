package github_test

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

const (
	bearer     = "Bearer fake-token"
	rulesURI   = "/repos/octo-org/octo-repo/rules/branches/main?per_page=100&page="
	classicURI = "/repos/octo-org/octo-repo/branches/main/protection"
	contract   = "SuiteWard Spike / Contract"
	appID      = 5257122
	rulesetID  = 9120451
)

var (
	forbidden = reply{status: http.StatusForbidden, body: []byte(`{"message":"Resource not accessible by integration"}`)}
	missing   = reply{status: http.StatusNotFound, body: []byte(`{"message":"Not Found"}`)}
)

func protectionRoutes(rules, classic reply) map[string]reply {
	return map[string]reply{rulesURI + "1": rules, classicURI: classic}
}

func readProtection(t *testing.T, routes map[string]reply) (github.Protection, *fakeGitHub, error) {
	t.Helper()
	reader, gh := newReader(t, routes)
	got, err := reader.Protection(t.Context(), repo, "main")
	return got, gh, err
}

func TestUserReadsTheNumericIdentity(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{"/users/magalz": ok(fixture(t, "user.json"))})
	got, err := reader.User(t.Context(), "magalz")
	if err != nil {
		t.Fatalf("User: %v", err)
	}
	if want := (github.User{ID: 884695, Login: "magalz", Type: "User"}); got != want {
		t.Errorf("User = %+v, want %+v", got, want)
	}
	if reqs := gh.requests(); len(reqs) != 1 || reqs[0].authorization != bearer {
		t.Errorf("requests = %+v, want one carrying %q", reqs, bearer)
	}
}

func TestUserAcceptsA39ByteLogin(t *testing.T) {
	login := strings.Repeat("a", 39)
	reader, _ := newReader(t, map[string]reply{"/users/" + login: ok(fixture(t, "user.json"))})
	if _, err := reader.User(t.Context(), login); err != nil {
		t.Errorf("User(39 bytes): %v", err)
	}
}

func TestRepositoryReadsTheNumericIdentity(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{"/repos/" + repo: ok(fixture(t, "repository.json"))})
	got, err := reader.Repository(t.Context(), repo)
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	want := github.Repository{ID: 1081726354, FullName: "IgnisDevNE/SuiteWardQ", Private: false, DefaultBranch: "main"}
	if got != want {
		t.Errorf("Repository = %+v, want %+v", got, want)
	}
	if reqs := gh.requests(); len(reqs) != 1 || reqs[0].authorization != bearer {
		t.Errorf("requests = %+v, want one carrying %q", reqs, bearer)
	}
}

func TestOwnerReadsRejectInvalidArgumentsBeforeAnyRequest(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{})
	logins := []string{
		"", strings.Repeat("a", 40), "a_b", "a b", "a/b", "a.b", "..", "a?x=1", "a#x", "a%2Fb", "ôcto", "a\n",
	}
	for _, bad := range logins {
		if _, err := reader.User(t.Context(), bad); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("User(%q) = %v, want ErrInvalidArgument", bad, err)
		}
	}
	repos := []string{"", "octo-org", "octo-org/", "../octo-repo", "octo-org/..", "octo-org/octo-repo/extra", "octo-org/octo-repo?x=1"}
	for _, bad := range repos {
		if _, err := reader.Repository(t.Context(), bad); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("Repository(%q) = %v, want ErrInvalidArgument", bad, err)
		}
		if _, err := reader.Protection(t.Context(), bad, "main"); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("Protection(%q) = %v, want ErrInvalidArgument", bad, err)
		}
	}
	for _, bad := range []string{"", ".", ".."} {
		if _, err := reader.Protection(t.Context(), repo, bad); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("Protection(branch %q) = %v, want ErrInvalidArgument", bad, err)
		}
	}
	if got := gh.uris(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
}

func TestOwnerReadsRejectAnIncompleteAnswer(t *testing.T) {
	cases := []struct{ name, body string }{
		{"user without id", `{"login":"magalz","type":"User"}`},
		{"user without login", `{"id":884695,"type":"User"}`},
		{"user without type", `{"id":884695,"login":"magalz"}`},
		{"user null", `null`},
		{"user list", `[]`},
		{"repository without id", `{"full_name":"octo-org/octo-repo","default_branch":"main"}`},
		{"repository without full name", `{"id":1,"default_branch":"main"}`},
		{"repository without branch", `{"id":1,"full_name":"octo-org/octo-repo"}`},
		{"repository null", `null`},
		{"repository list", `[]`},
	}
	for _, c := range cases {
		reader, _ := newReader(t, map[string]reply{"/users/magalz": ok([]byte(c.body)), "/repos/" + repo: ok([]byte(c.body))})
		var err error
		if strings.HasPrefix(c.name, "user") {
			_, err = reader.User(t.Context(), "magalz")
		} else {
			_, err = reader.Repository(t.Context(), repo)
		}
		if !errors.Is(err, github.ErrInvalidResponse) {
			t.Errorf("%s: error = %v, want ErrInvalidResponse", c.name, err)
		}
	}
}

func TestProtectionReadsTheMeasuredRulesetShape(t *testing.T) {
	for name, classic := range map[string]reply{"absent": missing, "unreadable": forbidden} {
		t.Run(name, func(t *testing.T) {
			got, gh, err := readProtection(t, protectionRoutes(ok(fixture(t, "rules_suitewardq.json")), classic))
			if err != nil {
				t.Fatalf("Protection: %v", err)
			}
			want := github.Protection{
				Rules:       []github.Rule{{RulesetID: rulesetID, Checks: []github.Check{{Context: contract, SourceID: appID}}, Strict: true}},
				FromRuleset: true,
				Classic:     github.Classic{State: github.ClassicAbsent},
			}
			if name == "unreadable" {
				want.Classic.State = github.ClassicUnreadable
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Protection = %+v, want %+v", got, want)
			}
			if uris := gh.uris(); !reflect.DeepEqual(uris, []string{rulesURI + "1", classicURI}) {
				t.Errorf("requests = %v, want the rules page 1 then the classic protection", uris)
			}
			for _, r := range gh.requests() {
				if r.authorization != bearer {
					t.Errorf("request %s carried %q, want %q", r.uri, r.authorization, bearer)
				}
			}
		})
	}
}

func TestProtectionRulesForbiddenCarriesNoRulesAndNoError(t *testing.T) {
	got, _, err := readProtection(t, protectionRoutes(forbidden, forbidden))
	if err != nil {
		t.Fatalf("Protection: %v", err)
	}
	want := github.Protection{RulesForbidden: true, Classic: github.Classic{State: github.ClassicUnreadable}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Protection = %+v, want %+v", got, want)
	}
}

func TestProtectionReadsClassicProtection(t *testing.T) {
	got, _, err := readProtection(t, protectionRoutes(ok([]byte(`[]`)), ok(fixture(t, "classic_protection.json"))))
	if err != nil {
		t.Fatalf("Protection: %v", err)
	}
	want := github.Protection{
		Classic: github.Classic{
			State:         github.ClassicPresent,
			Checks:        []github.Check{{Context: "SuiteWard / Contract", SourceID: appID}, {Context: "ci / test", SourceID: 0}},
			Strict:        true,
			EnforceAdmins: true,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Protection = %+v, want %+v", got, want)
	}
}

func TestProtectionClassicSourceIdsAndMissingParts(t *testing.T) {
	cases := []struct {
		name, body string
		want       github.Classic
	}{
		{
			"minus one stays minus one, absent is zero",
			`{"required_status_checks":{"strict":false,"checks":[{"context":"a","app_id":-1},{"context":"b"}]},"enforce_admins":{"enabled":false}}`,
			github.Classic{State: github.ClassicPresent, Checks: []github.Check{{Context: "a", SourceID: -1}, {Context: "b"}}},
		},
		{
			"no required checks and no enforce_admins",
			`{"required_linear_history":{"enabled":true}}`,
			github.Classic{State: github.ClassicPresent},
		},
	}
	for _, c := range cases {
		got, _, err := readProtection(t, protectionRoutes(ok([]byte(`[]`)), ok([]byte(c.body))))
		if err != nil {
			t.Fatalf("%s: Protection: %v", c.name, err)
		}
		if !reflect.DeepEqual(got.Classic, c.want) {
			t.Errorf("%s: Classic = %+v, want %+v", c.name, got.Classic, c.want)
		}
	}
}

func TestProtectionRuleShapes(t *testing.T) {
	cases := []struct {
		name, body  string
		wantRules   []github.Rule
		fromRuleset bool
	}{
		{
			"integration id absent is zero, strict absent is false",
			`[{"type":"required_status_checks","ruleset_source_type":"Repository","ruleset_id":7,"parameters":{"required_status_checks":[{"context":"ci"}]}}]`,
			[]github.Rule{{RulesetID: 7, Checks: []github.Check{{Context: "ci"}}}}, true,
		},
		{
			"a ruleset id alone marks a ruleset, a null integration id is zero",
			`[{"type":"required_status_checks","ruleset_id":7,"parameters":{"strict_required_status_checks_policy":true,"required_status_checks":[{"context":"ci","integration_id":null}]}}]`,
			[]github.Rule{{RulesetID: 7, Checks: []github.Check{{Context: "ci"}}, Strict: true}}, true,
		},
		{
			"a source type alone marks a ruleset",
			`[{"type":"deletion","ruleset_source_type":"Organization"}]`,
			nil, true,
		},
		{
			"no ruleset marker",
			`[{"type":"deletion"}]`,
			nil, false,
		},
		{
			"two rules of two rulesets",
			`[{"type":"required_status_checks","ruleset_id":1,"parameters":{"required_status_checks":[{"context":"a","integration_id":3}]}},
			  {"type":"required_status_checks","ruleset_id":2,"parameters":{"strict_required_status_checks_policy":true,"required_status_checks":[{"context":"b","integration_id":4},{"context":"c"}]}}]`,
			[]github.Rule{
				{RulesetID: 1, Checks: []github.Check{{Context: "a", SourceID: 3}}},
				{RulesetID: 2, Checks: []github.Check{{Context: "b", SourceID: 4}, {Context: "c"}}, Strict: true},
			}, true,
		},
	}
	for _, c := range cases {
		got, _, err := readProtection(t, protectionRoutes(ok([]byte(c.body)), missing))
		if err != nil {
			t.Fatalf("%s: Protection: %v", c.name, err)
		}
		if !reflect.DeepEqual(got.Rules, c.wantRules) || got.FromRuleset != c.fromRuleset {
			t.Errorf("%s: Rules = %+v FromRuleset = %t, want %+v %t", c.name, got.Rules, got.FromRuleset, c.wantRules, c.fromRuleset)
		}
	}
}

func TestProtectionPathEscapesTheBranch(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{
		"/repos/octo-org/octo-repo/rules/branches/release%2F1.x?per_page=100&page=1": ok([]byte(`[]`)),
		"/repos/octo-org/octo-repo/branches/release%2F1.x/protection":                missing,
	})
	if _, err := reader.Protection(t.Context(), repo, "release/1.x"); err != nil {
		t.Fatalf("Protection: %v", err)
	}
	if got := len(gh.uris()); got != 2 {
		t.Errorf("sent %d requests, want 2", got)
	}
}

// ruleRows returns n required_status_checks rules of distinct rulesets, ids from first.
func ruleRows(first, n int) []byte {
	rows := make([]string, 0, n)
	for i := first; i < first+n; i++ {
		rows = append(rows, fmt.Sprintf(`{"type":"required_status_checks","ruleset_id":%d,"parameters":{"required_status_checks":[{"context":"ci-%d"}]}}`, i, i))
	}
	return []byte("[" + strings.Join(rows, ",") + "]")
}

func TestProtectionFollowsFullPagesUntilAShortOne(t *testing.T) {
	routes := protectionRoutes(ok(ruleRows(1, 100)), missing)
	routes[rulesURI+"2"] = ok(ruleRows(101, 100))
	routes[rulesURI+"3"] = ok(fixture(t, "rules_suitewardq.json"))
	got, gh, err := readProtection(t, routes)
	if err != nil {
		t.Fatalf("Protection: %v", err)
	}
	if len(got.Rules) != 201 || got.Rules[0].RulesetID != 1 || got.Rules[199].RulesetID != 200 || got.Rules[200].RulesetID != rulesetID {
		t.Fatalf("got %d rules, want 201 in page order", len(got.Rules))
	}
	want := []string{rulesURI + "1", rulesURI + "2", rulesURI + "3", classicURI}
	if uris := gh.uris(); !reflect.DeepEqual(uris, want) {
		t.Errorf("requests = %v, want %v", uris, want)
	}
}

func TestProtectionStopsAfterTheRulesPageLimit(t *testing.T) {
	// Pages 1 through 100 are routed; a 101st request fails the fake.
	routes := fullPages(rulesURI, ruleRows(1, 100), 100)
	routes[classicURI] = missing
	got, gh, err := readProtection(t, routes)
	if !errors.Is(err, github.ErrInvalidResponse) {
		t.Fatalf("Protection = %+v, %v; want ErrInvalidResponse", got, err)
	}
	if !strings.Contains(err.Error(), "/rules/branches/main") {
		t.Errorf("error %q does not name the path", err)
	}
	if n := len(gh.requests()); n != 100 {
		t.Errorf("sent %d requests, want 100", n)
	}
}

func TestProtectionServerErrorsAreErrors(t *testing.T) {
	serverError := reply{status: http.StatusInternalServerError, body: []byte(`{"message":"boom"}`)}
	cases := map[string]map[string]reply{
		"rules 500":                          protectionRoutes(serverError, missing),
		"classic 500":                        protectionRoutes(ok([]byte(`[]`)), serverError),
		"rules 404 is not a protection fact": protectionRoutes(missing, missing),
	}
	for name, routes := range cases {
		got, _, err := readProtection(t, routes)
		var se *rest.StatusError
		if !errors.As(err, &se) {
			t.Errorf("%s: Protection = %+v, %v; want a *rest.StatusError", name, got, err)
		}
	}
	reader, _ := newReader(t, map[string]reply{"/users/magalz": serverError, "/repos/" + repo: missing})
	var se *rest.StatusError
	if _, err := reader.User(t.Context(), "magalz"); !errors.As(err, &se) || se.Status != http.StatusInternalServerError {
		t.Errorf("User error = %v, want a 500 *rest.StatusError", err)
	}
	if _, err := reader.Repository(t.Context(), repo); !errors.Is(err, rest.ErrNotFound) {
		t.Errorf("Repository error = %v, want rest.ErrNotFound", err)
	}
}

func TestProtectionRateLimitedForbiddenIsNotAFact(t *testing.T) {
	limited := reply{status: http.StatusForbidden, body: forbidden.body, header: map[string]string{"Retry-After": "60"}}
	cases := map[string]map[string]reply{
		"rules":   protectionRoutes(limited, missing),
		"classic": protectionRoutes(ok([]byte(`[]`)), limited),
	}
	for name, routes := range cases {
		got, _, err := readProtection(t, routes)
		if !errors.Is(err, rest.ErrRateLimited) {
			t.Errorf("%s: Protection = %+v, %v; want rest.ErrRateLimited", name, got, err)
		}
	}
}

func TestProtectionRejectsAnAnswerItCannotRead(t *testing.T) {
	const marker = "body-marker-27182"
	garbage := ok([]byte(marker + " <html>proxy error</html>"))
	wrongShape := ok([]byte(`{"marker":"` + marker + `"}`)) // valid JSON, not a list of rules
	noContext := ok([]byte(`[{"type":"required_status_checks","ruleset_id":1,"parameters":{"required_status_checks":[{"context":"","integration_id":1}]}}]`))
	badClassic := ok([]byte(`{"enforce_admins":{"enabled":"` + marker + `"}}`))
	empty := ok([]byte(`[]`))
	check := func(what string, err error) {
		t.Helper()
		switch {
		case !errors.Is(err, github.ErrInvalidResponse):
			t.Errorf("%s: error = %v, want ErrInvalidResponse", what, err)
		case strings.Contains(err.Error(), marker):
			t.Errorf("%s: error %q includes the body", what, err)
		}
	}
	cases := map[string]map[string]reply{
		"rules garbage":     protectionRoutes(garbage, missing),
		"rules wrong shape": protectionRoutes(wrongShape, missing),
		"rules null":        protectionRoutes(ok([]byte(`null`)), missing),
		"rules no context":  protectionRoutes(noContext, missing),
		"classic garbage":   protectionRoutes(empty, garbage),
		"classic list":      protectionRoutes(empty, empty),
		"classic bad type":  protectionRoutes(empty, badClassic),
	}
	for name, routes := range cases {
		_, _, err := readProtection(t, routes)
		check(name, err)
	}
	reader, _ := newReader(t, map[string]reply{"/users/magalz": garbage, "/repos/" + repo: garbage})
	_, err := reader.User(t.Context(), "magalz")
	check("User", err)
	_, err = reader.Repository(t.Context(), repo)
	check("Repository", err)
}
