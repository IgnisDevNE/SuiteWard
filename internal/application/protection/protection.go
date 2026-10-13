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
	MissingContractCheck    Code = "missing-contract-check"
	MissingTestCheck        Code = "missing-test-check"
	NotUpToDate             Code = "not-up-to-date"
	AdminBypass             Code = "admin-bypass"
	RulesetBypassUnverified Code = "ruleset-bypass-unverified"
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

// Unknown is the result for a caller that could not read the facts.
func Unknown() Result {
	return Result{Kind: KindUnknown, Instructions: []string{"not implemented"}}
}

// Evaluate applies D-PROTECTION to f for the App appID and the repository's
// configured test check (empty before setup).
func Evaluate(f Facts, appID int64, testCheck string) Result {
	return Unknown()
}
