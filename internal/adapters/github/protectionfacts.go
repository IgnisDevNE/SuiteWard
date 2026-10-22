package github

import (
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
)

// Facts maps what the adapter read, field by field, into the evaluator's facts; private comes from the repository read.
// The service's setup and protection job and the installer all go through it, so they evaluate the same facts.
func (p Protection) Facts(private bool) (protection.Facts, error) {
	state, err := classicState(p.Classic.State)
	if err != nil {
		return protection.Facts{}, err
	}
	facts := protection.Facts{
		Private: private, RulesForbidden: p.RulesForbidden, FromRuleset: p.FromRuleset,
		Classic: protection.Classic{State: state, Checks: protectionChecks(p.Classic.Checks), Strict: p.Classic.Strict, EnforceAdmins: p.Classic.EnforceAdmins},
	}
	for _, rule := range p.Rules {
		facts.Rules = append(facts.Rules, protection.Rule{RulesetID: rule.RulesetID, Checks: protectionChecks(rule.Checks), Strict: rule.Strict})
	}
	return facts, nil
}

func classicState(state ClassicState) (protection.ClassicState, error) {
	switch state {
	case ClassicAbsent:
		return protection.ClassicAbsent, nil
	case ClassicUnreadable:
		return protection.ClassicUnreadable, nil
	case ClassicPresent:
		return protection.ClassicPresent, nil
	}
	return 0, fmt.Errorf("unknown classic protection state %d", state)
}

func protectionChecks(checks []Check) []protection.Check {
	var out []protection.Check
	for _, c := range checks {
		out = append(out, protection.Check{Context: c.Context, SourceID: c.SourceID})
	}
	return out
}
