package github_test

import (
	"reflect"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
)

func TestProtectionFactsMapsEveryFieldAndClassicState(t *testing.T) {
	for _, state := range []struct {
		from github.ClassicState
		to   protection.ClassicState
	}{
		{github.ClassicAbsent, protection.ClassicAbsent},
		{github.ClassicUnreadable, protection.ClassicUnreadable},
		{github.ClassicPresent, protection.ClassicPresent},
	} {
		got, err := github.Protection{Classic: github.Classic{State: state.from}}.Facts(false)
		if err != nil || got.Classic.State != state.to {
			t.Errorf("classic state %v mapped to %v, %v; want %v", state.from, got.Classic.State, err, state.to)
		}
	}
	if _, err := (github.Protection{Classic: github.Classic{State: github.ClassicState(9)}}).Facts(false); err == nil {
		t.Error("an unknown classic state was mapped")
	}

	got, err := github.Protection{
		RulesForbidden: true, FromRuleset: true,
		Rules:   []github.Rule{{RulesetID: 7, Strict: true, Checks: []github.Check{{Context: "a", SourceID: 1}, {Context: "b", SourceID: 2}}}, {RulesetID: 8}},
		Classic: github.Classic{State: github.ClassicPresent, Strict: true, EnforceAdmins: true, Checks: []github.Check{{Context: "c", SourceID: -1}}},
	}.Facts(true)
	want := protection.Facts{
		Private: true, RulesForbidden: true, FromRuleset: true,
		Rules:   []protection.Rule{{RulesetID: 7, Strict: true, Checks: []protection.Check{{Context: "a", SourceID: 1}, {Context: "b", SourceID: 2}}}, {RulesetID: 8}},
		Classic: protection.Classic{State: protection.ClassicPresent, Strict: true, EnforceAdmins: true, Checks: []protection.Check{{Context: "c", SourceID: -1}}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Facts = %+v, %v; want %+v", got, err, want)
	}
}
