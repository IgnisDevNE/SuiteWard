package protection

import (
	"slices"
	"strings"
	"testing"
)

const (
	app  = int64(77)
	test = "unit-tests"
)

func rule(strict bool, checks ...Check) Rule { return Rule{RulesetID: 1, Strict: strict, Checks: checks} }

func contract(id int64) Check { return Check{Context: ContractCheck, SourceID: id} }

func classic(strict, admins bool, checks ...Check) Classic {
	return Classic{State: ClassicPresent, Checks: checks, Strict: strict, EnforceAdmins: admins}
}

func ruleFacts(rules ...Rule) Facts { return Facts{Rules: rules, FromRuleset: len(rules) > 0} }

func TestEvaluate(t *testing.T) {
	good := []Check{contract(app), {Context: test}}
	caveat := []Code{RulesetBypassUnverified}
	tests := []struct {
		name      string
		facts     Facts
		appID     int64
		testCheck string
		kind      Kind
		findings  []Code
		caveats   []Code
	}{
		{"enforced by a ruleset", ruleFacts(rule(true, good...)), app, test, KindEnforced, nil, caveat},
		{"enforced by classic protection alone", Facts{Classic: classic(true, true, good...)}, app, test, KindEnforced, nil, nil},
		{"missing contract check alone", ruleFacts(rule(true, Check{Context: test})), app, test, KindFindings, []Code{MissingContractCheck}, caveat},
		{"missing test check alone", ruleFacts(rule(true, contract(app))), app, test, KindFindings, []Code{MissingTestCheck}, caveat},
		{"not up to date alone", ruleFacts(rule(false, good...)), app, test, KindFindings, []Code{NotUpToDate}, caveat},
		{"admin bypass alone", Facts{Classic: classic(true, false, good...)}, app, test, KindFindings, []Code{AdminBypass}, nil},
		{"several findings at once", Facts{Rules: []Rule{rule(false, contract(app))}, FromRuleset: true, Classic: classic(false, false)},
			app, test, KindFindings, []Code{AdminBypass, MissingTestCheck, NotUpToDate}, caveat},
		{"nothing required anywhere", Facts{Classic: classic(false, false)}, app, test, KindFindings, []Code{AdminBypass, MissingContractCheck, MissingTestCheck}, nil},
		{"no protection at all", Facts{}, app, test, KindFindings, []Code{MissingContractCheck, MissingTestCheck}, nil},
		{"strict on a classic that does not require the contract check does not count",
			Facts{Rules: []Rule{rule(false, contract(app))}, FromRuleset: true, Classic: classic(true, true, Check{Context: test})},
			app, test, KindFindings, []Code{NotUpToDate}, caveat},
		{"contract by ruleset, test by classic", Facts{Rules: []Rule{rule(true, contract(app))}, FromRuleset: true, Classic: classic(false, true, Check{Context: test})},
			app, test, KindEnforced, nil, caveat},
		{"strict met only by classic that requires the contract check",
			Facts{Rules: []Rule{rule(false, good...)}, FromRuleset: true, Classic: classic(true, true, good...)},
			app, test, KindEnforced, nil, caveat},
		{"P0 SuiteWardQ as measured", ruleFacts(rule(true, Check{Context: "SuiteWard Spike / Contract", SourceID: 5257122})),
			5257122, "CI", KindFindings, []Code{MissingContractCheck, MissingTestCheck}, caveat},
		{"P0 with the context renamed and CI added",
			ruleFacts(rule(true, Check{Context: ContractCheck, SourceID: 5257122}, Check{Context: "CI"})),
			5257122, "CI", KindEnforced, nil, caveat},
		{"contract check unpinned (id 0), strict", ruleFacts(rule(true, contract(0), Check{Context: test})), app, test, KindFindings, []Code{MissingContractCheck}, caveat},
		{"contract check pinned to another App, strict", ruleFacts(rule(true, contract(999), Check{Context: test})), app, test, KindFindings, []Code{MissingContractCheck}, caveat},
		{"unpinned contract check that is not strict", ruleFacts(rule(false, contract(0), Check{Context: test})), app, test, KindFindings, []Code{MissingContractCheck, NotUpToDate}, caveat},
		{"classic contract check not pinned (0)", Facts{Classic: classic(true, true, contract(0), Check{Context: test})}, app, test, KindFindings, []Code{MissingContractCheck}, nil},
		{"classic contract check not pinned (-1)", Facts{Classic: classic(true, true, contract(-1), Check{Context: test})}, app, test, KindFindings, []Code{MissingContractCheck}, nil},
		{"contract context is case sensitive", ruleFacts(rule(true, Check{Context: "suiteward / contract", SourceID: app}, Check{Context: test})), app, test, KindFindings, []Code{MissingContractCheck}, caveat},
		{"test check is case sensitive", ruleFacts(rule(true, contract(app), Check{Context: "Unit-Tests"})), app, test, KindFindings, []Code{MissingTestCheck}, caveat},
		{"test check in any pin counts", ruleFacts(rule(true, contract(app), Check{Context: test, SourceID: 12345})), app, test, KindEnforced, nil, caveat},
		{"the right rule of several satisfies the contract", ruleFacts(rule(false, Check{Context: test}), rule(true, contract(app))), app, test, KindEnforced, nil, caveat},
		{"strictness is judged on the rule requiring the contract", ruleFacts(rule(true, Check{Context: test}), rule(false, contract(app))), app, test, KindFindings, []Code{NotUpToDate}, caveat},
		{"a ruleset of another type still carries the caveat", Facts{FromRuleset: true, Classic: classic(true, true, good...)}, app, test, KindEnforced, nil, caveat},
		{"empty test check before setup", ruleFacts(rule(true, contract(app))), app, "", KindFindings, []Code{MissingTestCheck}, caveat},
		{"empty test check never matches an empty context", ruleFacts(rule(true, contract(app), Check{})), app, "", KindFindings, []Code{MissingTestCheck}, caveat},
		{"unreadable classic and no ruleset", Facts{Classic: Classic{State: ClassicUnreadable}}, app, test, KindUnknown, nil, nil},
		{"unreadable classic, ruleset contract unpinned", Facts{Rules: []Rule{rule(true, contract(0), Check{Context: test})}, FromRuleset: true, Classic: Classic{State: ClassicUnreadable}},
			app, test, KindUnknown, nil, nil},
		{"unreadable classic, ruleset enforces: no admin-bypass", Facts{Rules: []Rule{rule(true, good...)}, FromRuleset: true, Classic: Classic{State: ClassicUnreadable}},
			app, test, KindEnforced, nil, caveat},
		{"unreadable classic, ruleset has findings", Facts{Rules: []Rule{rule(false, contract(app))}, FromRuleset: true, Classic: Classic{State: ClassicUnreadable}},
			app, test, KindFindings, []Code{MissingTestCheck, NotUpToDate}, caveat},
		{"private repository whose rules are forbidden", Facts{Private: true, RulesForbidden: true}, app, test, KindEnforcementUnavailable, nil, nil},
		{"exclusive result ignores every other fact", Facts{Private: true, RulesForbidden: true, FromRuleset: true, Rules: []Rule{rule(true, good...)}, Classic: classic(true, false, good...)},
			app, test, KindEnforcementUnavailable, nil, nil},
		{"public repository whose rules are forbidden", Facts{RulesForbidden: true}, app, test, KindUnknown, nil, nil},
		{"private repository with readable rules", Facts{Private: true, Rules: []Rule{rule(true, good...)}, FromRuleset: true}, app, test, KindEnforced, nil, caveat},
		{"app id zero", ruleFacts(rule(true, contract(0), Check{Context: test})), 0, test, KindUnknown, nil, nil},
		{"app id negative", Facts{Classic: classic(true, true, contract(-1), Check{Context: test})}, -1, test, KindUnknown, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(tt.facts, tt.appID, tt.testCheck)
			if got.Kind != tt.kind || !slices.Equal(got.Findings, tt.findings) || !slices.Equal(got.Caveats, tt.caveats) {
				t.Fatalf("got kind=%s findings=%v caveats=%v, want kind=%s findings=%v caveats=%v", got.Kind, got.Findings, got.Caveats, tt.kind, tt.findings, tt.caveats)
			}
			want := len(tt.findings) + len(tt.caveats)
			if tt.kind == KindUnknown || tt.kind == KindEnforcementUnavailable {
				want = 1
			}
			if len(got.Instructions) != want {
				t.Fatalf("got %d instructions %q, want %d", len(got.Instructions), got.Instructions, want)
			}
			for _, s := range got.Instructions {
				if strings.TrimSpace(s) == "" || strings.Contains(s, "not implemented") {
					t.Fatalf("empty or placeholder instruction %q", s)
				}
			}
			if len(slices.Compact(slices.Clone(got.Instructions))) != len(got.Instructions) {
				t.Fatalf("instructions repeat: %q", got.Instructions)
			}
		})
	}
}

func TestInstructionContent(t *testing.T) {
	has := func(t *testing.T, r Result, parts ...string) {
		t.Helper()
		all := strings.Join(r.Instructions, "\n")
		for _, p := range parts {
			if !strings.Contains(all, p) {
				t.Fatalf("instructions %q lack %q", r.Instructions, p)
			}
		}
	}
	has(t, Evaluate(Facts{Private: true, RulesForbidden: true}, app, test), "GitHub Free", "private", "public", "Administration read")
	has(t, Unknown(), "installation", "Administration read")
	has(t, Evaluate(Facts{Classic: Classic{State: ClassicUnreadable}}, app, test), "Administration read")
	has(t, Evaluate(Facts{}, 0, test), "App id")
	has(t, Evaluate(ruleFacts(rule(true, contract(app))), app, ""), "suiteward setup")
	has(t, Evaluate(ruleFacts(rule(true, contract(app), Check{Context: test})), app, test), "bypass list")
	has(t, Evaluate(Facts{}, app, test), ContractCheck)
}

func TestInstructionsCarryNoInput(t *testing.T) {
	const marker = "zz-secret-check-name"
	facts := Facts{
		Rules:       []Rule{{RulesetID: 424242, Checks: []Check{{Context: marker + "-ctx", SourceID: 987654}}}},
		FromRuleset: true,
		Classic:     classic(false, false, Check{Context: marker + "-classic", SourceID: 987655}),
	}
	for _, f := range []Facts{facts, {}, {Private: true, RulesForbidden: true}, {Classic: Classic{State: ClassicUnreadable}}} {
		for _, tc := range []string{marker + "-test", ""} {
			r := Evaluate(f, app, tc)
			for _, s := range r.Instructions {
				for _, bad := range []string{marker, "424242", "987654", "987655", "77"} {
					if strings.Contains(s, bad) {
						t.Fatalf("instruction %q contains input %q", s, bad)
					}
				}
				if strings.Contains(strings.ReplaceAll(s, ContractCheck, ""), "/") {
					t.Fatalf("instruction %q carries a path-like string beyond the fixed check name", s)
				}
			}
		}
	}
}

func TestDeterministicOrder(t *testing.T) {
	a, b, c := rule(false, contract(app)), rule(true, Check{Context: test}), Rule{RulesetID: 3}
	var first Result
	for i, rules := range [][]Rule{{a, b, c}, {c, b, a}, {b, a, c}} {
		got := Evaluate(Facts{Rules: rules, FromRuleset: true, Classic: classic(false, false)}, app, test)
		if i == 0 {
			first = got
			if !slices.IsSorted(got.Findings) || len(got.Findings) < 2 {
				t.Fatalf("findings not sorted: %v", got.Findings)
			}
			continue
		}
		if !slices.Equal(got.Findings, first.Findings) || !slices.Equal(got.Caveats, first.Caveats) || !slices.Equal(got.Instructions, first.Instructions) {
			t.Fatalf("order of rules changed the result: %+v vs %+v", got, first)
		}
	}
}

func TestUnknown(t *testing.T) {
	r := Unknown()
	if r.Kind != KindUnknown || len(r.Findings) != 0 || len(r.Caveats) != 0 || len(r.Instructions) != 1 {
		t.Fatalf("unexpected %+v", r)
	}
	if !strings.Contains(r.Instructions[0], "installation") || !strings.Contains(r.Instructions[0], "Administration read") {
		t.Fatalf("instruction %q", r.Instructions[0])
	}
}
