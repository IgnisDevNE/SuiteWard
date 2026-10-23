// Package protection evaluates D-PROTECTION: whether the default branch's
// protection makes the SuiteWard check binding. It is pure: composition maps
// the GitHub adapter's protection facts into Facts, and Evaluate returns the
// result, its findings, caveats and fixed English instructions. SuiteWard
// verifies and reports; it never changes protection (ADR 0016 amendment).
//
// Evaluate never builds an instruction from input: every sentence is a fixed
// string, so no GitHub-originated text reaches /status or a log.
//
// An appID that is not positive makes Evaluate return KindUnknown with an
// instruction rather than panic: configuration validates the id, but the
// periodic job must fail safe (never claim protection) instead of crashing.
package protection

import "slices"

// ContractCheck is the required check the App publishes (D-CHECKS).
const ContractCheck = "SuiteWard / Contract"

// Kind is the overall result.
type Kind string

const (
	KindEnforced               Kind = "enforced"
	KindFindings               Kind = "findings"
	KindEnforcementUnavailable Kind = "enforcement-unavailable"
	KindUnknown                Kind = "unknown"
)

// Code names a finding or a caveat.
type Code string

const (
	MissingContractCheck      Code = "missing-contract-check"
	MissingTestCheck          Code = "missing-test-check"
	NotUpToDate               Code = "not-up-to-date"
	AdminBypass               Code = "admin-bypass"
	RulesetOverrideUnverified Code = "ruleset-bypass-unverified"
)

// Check is one required status check. SourceID is the integration id for a
// ruleset rule and the app id for classic protection; 0 means not pinned, and
// -1 does too (classic only).
type Check struct {
	Context  string
	SourceID int64
}

// Rule is one required_status_checks rule of an active ruleset.
type Rule struct {
	RulesetID int64
	Checks    []Check
	Strict    bool
}

// ClassicState tells whether classic branch protection could be read.
type ClassicState int

const (
	ClassicAbsent ClassicState = iota
	ClassicUnreadable
	ClassicPresent
)

// Classic is the classic branch protection; Checks, Strict and EnforceAdmins
// are meaningful only when State is ClassicPresent.
type Classic struct {
	State         ClassicState
	Checks        []Check
	Strict        bool
	EnforceAdmins bool
}

// Facts are what the adapter read.
type Facts struct {
	Private        bool
	RulesForbidden bool
	Rules          []Rule
	FromRuleset    bool
	Classic        Classic
}

// Result is the evaluation. Findings and Caveats are sorted; Instructions
// holds one fixed sentence per finding or caveat (findings first), or a single
// sentence for an exclusive result.
type Result struct {
	Kind         Kind
	Findings     []Code
	Caveats      []Code
	Instructions []string
}

var instructions = map[Code]string{
	MissingContractCheck:      "Require the check " + ContractCheck + " on the default branch, with the SuiteWard App as its source.",
	MissingTestCheck:          "Require the project's test check on the default branch.",
	NotUpToDate:               "Require branches to be up to date before merging on the rule that requires " + ContractCheck + ".",
	AdminBypass:               "Enable enforcement for administrators on the default branch protection.",
	RulesetOverrideUnverified: "A ruleset applies and SuiteWard cannot see its bypass list: check in GitHub that the bypass list is empty.",
}

const (
	setupInstruction = "No test check is configured for this project yet: run suiteward enroll (it asks for the check), then require that check on the default branch."
	appIDInstruction = "The GitHub App id is not configured: set it, then wait for the next protection check."
	unavailable      = "This repository is private and GitHub did not return its branch rules: GitHub Free does not enforce rules on private repositories, so upgrade the plan or make the repository public; or the App lacks the read permission, so grant it Administration read."
	unreadable       = "SuiteWard could not read the branch protection: check that the App's installation covers this repository and grants Administration read."
)

// Unknown is the result for a caller that could not read the facts.
func Unknown() Result {
	return Result{Kind: KindUnknown, Instructions: []string{unreadable}}
}

// source is one place a required check can come from: a ruleset rule or the
// classic protection.
type source struct {
	checks []Check
	strict bool
}

func (s source) requires(context string) bool {
	return slices.ContainsFunc(s.checks, func(c Check) bool { return c.Context == context })
}

func (s source) pinsContract(appID int64) bool {
	return slices.Contains(s.checks, Check{Context: ContractCheck, SourceID: appID})
}

// Evaluate applies D-PROTECTION to f for the App appID and the repository's
// configured test check (empty before setup).
func Evaluate(f Facts, appID int64, testCheck string) Result {
	switch {
	case appID <= 0:
		return Result{Kind: KindUnknown, Instructions: []string{appIDInstruction}}
	case f.RulesForbidden && f.Private:
		return Result{Kind: KindEnforcementUnavailable, Instructions: []string{unavailable}}
	case f.RulesForbidden:
		return Unknown()
	}

	sources := make([]source, 0, len(f.Rules)+1)
	for _, r := range f.Rules {
		sources = append(sources, source{r.Checks, r.Strict})
	}
	pins := func(s source) bool { return s.pinsContract(appID) }
	if f.Classic.State == ClassicUnreadable && !slices.ContainsFunc(sources, pins) {
		// The unreadable classic protection might hold the pinned check.
		return Unknown()
	}
	if f.Classic.State == ClassicPresent {
		sources = append(sources, source{f.Classic.Checks, f.Classic.Strict})
	}

	var findings, caveats []Code
	if !slices.ContainsFunc(sources, pins) {
		findings = append(findings, MissingContractCheck)
	}
	// An empty test check never matches: setup has not recorded one.
	if testCheck == "" || !slices.ContainsFunc(sources, func(s source) bool { return s.requires(testCheck) }) {
		findings = append(findings, MissingTestCheck)
	}
	// Strictness is judged on the sources that require the contract context,
	// whatever their pin; with none, missing-contract-check already says it.
	withContract := slices.DeleteFunc(slices.Clone(sources), func(s source) bool { return !s.requires(ContractCheck) })
	if len(withContract) > 0 && !slices.ContainsFunc(withContract, func(s source) bool { return s.strict }) {
		findings = append(findings, NotUpToDate)
	}
	if f.Classic.State == ClassicPresent && !f.Classic.EnforceAdmins {
		findings = append(findings, AdminBypass)
	}
	if f.FromRuleset {
		caveats = append(caveats, RulesetOverrideUnverified)
	}
	slices.Sort(findings)

	res := Result{Kind: KindEnforced, Findings: findings, Caveats: caveats}
	if len(findings) > 0 {
		res.Kind = KindFindings
	}
	for _, c := range findings {
		if c == MissingTestCheck && testCheck == "" {
			res.Instructions = append(res.Instructions, setupInstruction)
			continue
		}
		res.Instructions = append(res.Instructions, instructions[c])
	}
	for _, c := range caveats {
		res.Instructions = append(res.Instructions, instructions[c])
	}
	return res
}
