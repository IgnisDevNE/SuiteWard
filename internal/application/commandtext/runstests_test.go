package commandtext_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// What a text says about running tests (docs/contracts/runtime.md, "What a text says about running tests"; owner, 2026-10-27): no text says that SuiteWard does
// not run tests, or that it ran none, as a property of SuiteWard, of a command or of an outcome. Only a text computed from a run says what ran.

// notRunPhrases are the four phrases the owner's rule removes from every text.
var notRunPhrases = []string{"does not run tests", "runs no tests", "did not run any tests", "did not run the tests"}

// everyBody renders every comment kind, every check summary and every acknowledgment of the package, by name. A renderer that panics is a test error.
func everyBody(t *testing.T) map[string]string {
	t.Helper()
	bodies := map[string]string{}
	add := func(name string, f func() string) {
		if _, dup := bodies[name]; dup {
			t.Fatalf("two bodies are named %q", name)
		}
		bodies[name] = render(t, f)
	}
	tiers := map[string]commandtext.Tier{"isolated": commandtext.Isolated, "co-located": commandtext.CoLocated}
	addAcks(t, add)
	for tier, value := range tiers {
		add("bootstrap summary "+tier, func() string { return commandtext.BootstrapSummary(summary(1, pathList(1), value)) })
		add("bootstrap summary of a pull "+tier, func() string {
			s := summary(1, pathList(1), value)
			s.Source, s.Branch = commandtext.SourcePull, ""
			return commandtext.BootstrapSummary(s)
		})
		for _, tc := range checkReasons {
			add("check "+tc.reason+" "+tier, func() string {
				c := check(tc.state, tc.reason)
				c.Tier = value
				_, s := commandtext.CheckSummary(c)
				return s
			})
		}
		for _, tc := range preCanonical {
			add("check "+tc.reason+" "+tier, func() string {
				c := noCanonical(tc.state, tc.reason)
				c.Tier = value
				_, s := commandtext.CheckSummary(c)
				return s
			})
		}
		add("check bootstrap-ready "+tier, func() string { c := bootstrapReady(); c.Tier = value; _, s := commandtext.CheckSummary(c); return s })
		for _, tc := range requiredReasons {
			add("check "+tc.reason+" "+tier, func() string {
				c := requiredCheck(tc.reason)
				c.Tier = value
				_, s := commandtext.CheckSummary(c)
				return s
			})
		}
		add("check ready with counts "+tier, func() string {
			c := check(commandtext.CheckSuccess, "ready")
			c.Tier, c.Execution = value, passed()
			_, s := commandtext.CheckSummary(c)
			return s
		})
		add("check unchanged unblocked "+tier, func() string {
			c := check(commandtext.CheckSuccess, "unchanged")
			c.Tier, c.Unblocked = value, true
			_, s := commandtext.CheckSummary(c)
			return s
		})
		for _, tc := range receiptReasons {
			add("receipt "+tc.reason+" "+tier, func() string {
				c := receiptOf(tc.reason)
				c.Tier = value
				_, s := commandtext.ReceiptCheckSummary(c)
				return s
			})
		}
		add("receipt not run, unblocked "+tier, func() string {
			c := receiptOf("receipt-not-run")
			c.Tier, c.Unblocked = value, true
			_, s := commandtext.ReceiptCheckSummary(c)
			return s
		})
		add("revision "+tier, func() string { return commandtext.RevisionComment(revision(value)) })
		add("revision that reduces the scope "+tier, func() string {
			r := revision(value)
			r.Reduced, r.ReducedRules, r.ReducedPaths, r.ReducedPathsTotal = true, []string{"rule tests/**", "execution canonical: required -> advisory"}, []string{"tests/a.go"}, 1
			return commandtext.RevisionComment(r)
		})
		for _, level := range []string{"advisory", "before-approval", "required"} {
			add("revision with a "+level+" receipt "+tier, func() string { r := receiptRevision(t, level); r.Tier = value; return commandtext.RevisionComment(r) })
			add("revision with a "+level+" receipt and no link "+tier, func() string {
				r := receiptRevision(t, level)
				r.Tier, r.ReceiptURL = value, ""
				return commandtext.RevisionComment(r)
			})
		}
		add("revision that lowers the policy, detail omitted "+tier, func() string {
			r := revision(value)
			r.Reduced, r.ReducedOmitted, r.LowersPolicy = true, true, true
			return commandtext.RevisionComment(r)
		})
		add("merge promoted "+tier, func() string {
			return commandtext.MergeComment(commandtext.Merge{Kind: commandtext.MergePromoted, Source: mergeSHA, Version: 2, Proposal: 2, Revision: 1, Tier: value})
		})
		add("merge corrected "+tier, func() string {
			return commandtext.MergeComment(commandtext.Merge{Kind: commandtext.MergeCorrected, Source: mergeSHA, Version: 3, Corrects: 2, Proposal: 4, Revision: 2, Tier: value})
		})
		for _, tc := range blockedReasons {
			add("merge blocked "+tc.reason+" "+tier, func() string { m := blocked(tc.reason); m.Tier = value; return commandtext.MergeComment(m) })
		}
	}
	add("bootstrap notice", commandtext.BootstrapNotice)
	add("bootstrap cancelled", func() string { return commandtext.BootstrapCancelled(1, 2) })
	add("declaration suggestion", func() string { return commandtext.SuggestDeclaration(declarationMissing("*_test.go")) })
	add("scope notice", func() string { return commandtext.ScopeNoticeComment(notice()) })
	add("scope notice without a declaration", func() string { return commandtext.ScopeNoticeComment(patternsOnly()) })
	for name, base := range map[string]commandtext.ScopeBase{"pull": commandtext.BasePull, "contract": commandtext.BaseContract, "default branch": commandtext.BaseDefaultBranch} {
		add("scope reply "+name, func() string { return commandtext.ScopeNoticeComment(reply(base)) })
	}
	add("runner uncovered", func() string { return commandtext.RunnerUncoveredComment(runnerNotice()) })
	add("runner uncovered without a protect declaration", func() string { return commandtext.RunnerUncoveredComment(protectOnly()) })
	add("invalid source declaration", func() string {
		return commandtext.InvalidSourceComment(commandtext.InvalidSourceNotice{Problem: commandtext.DeclarationProblem})
	})
	add("invalid source link", func() string {
		return commandtext.InvalidSourceComment(commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "docs/link"})
	})
	return bodies
}

// addAcks renders the acknowledgment of every outcome, with every reason, refusal, rejection and malformed reason that changes its text.
func addAcks(t *testing.T, add func(name string, f func() string)) {
	t.Helper()
	// These outcomes name the proposal revision of the command; the others take no reference.
	named := []commandtext.Outcome{commandtext.Approved, commandtext.Revoked, commandtext.NoActiveApproval, commandtext.RevokedAfterPromotion, commandtext.ApprovedAfterPromotion,
		commandtext.InvalidCode, commandtext.ReplayedCode, commandtext.NoSuite, commandtext.OtherPullRequest, commandtext.AlreadyProposed, commandtext.Rejected, commandtext.ReceiptRefused}
	ack := func(name string, a commandtext.Ack) {
		a.Owner = owner
		if a.Outcome != commandtext.BootstrapPromoted { // a promotion has no command to reply to
			a.Source = source
		}
		if slices.Contains(named, a.Outcome) {
			a.Proposal, a.Revision = 4, 2
		}
		add("ack "+name, func() string { return commandtext.Acknowledge(a) })
	}
	for _, o := range []commandtext.Outcome{commandtext.Approved, commandtext.Revoked, commandtext.NoActiveApproval, commandtext.RevokedAfterPromotion, commandtext.ApprovedAfterPromotion} {
		ack("consent "+fmt.Sprint(o), commandtext.Ack{Outcome: o})
		ack("consent eligible "+fmt.Sprint(o), commandtext.Ack{Outcome: o, Eligible: o == commandtext.Approved || o == commandtext.Revoked || o == commandtext.NoActiveApproval})
	}
	for _, o := range []commandtext.Outcome{commandtext.InvalidCode, commandtext.ReplayedCode, commandtext.NoSuite, commandtext.OtherPullRequest, commandtext.AlreadyProposed} {
		ack("command "+fmt.Sprint(o), commandtext.Ack{Outcome: o})
	}
	for rejection := commandtext.Unauthorized; rejection <= commandtext.ObsoleteCommand; rejection++ {
		ack("rejected "+fmt.Sprint(rejection), commandtext.Ack{Outcome: commandtext.Rejected, Rejection: rejection})
	}
	ack("malformed", commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.ExtraText})
	ack("edited first", commandtext.Ack{Outcome: commandtext.EditedFirst})
	ack("propose failed", commandtext.Ack{Outcome: commandtext.ProposeFailed})
	ack("skipped", commandtext.Ack{Outcome: commandtext.Skipped})
	ack("bootstrap promoted", commandtext.Ack{Outcome: commandtext.BootstrapPromoted, Version: 1, Proposal: 4, Revision: 2})
	for _, base := range []commandtext.ScopeBase{commandtext.BasePull, commandtext.BaseContract, commandtext.BaseDefaultBranch} {
		ack("nothing uncovered "+fmt.Sprint(base), commandtext.Ack{Outcome: commandtext.NothingUncovered, Base: base})
	}
	// Every refusal that an outcome takes, whatever the set is: ValidateAck says which fit.
	for _, o := range []commandtext.Outcome{commandtext.ProposeRefused, commandtext.SkipRefused, commandtext.SuggestRefused, commandtext.ModeRefused, commandtext.UnblockRefused} {
		for refusal := commandtext.CanonicalPresent; refusal <= commandtext.NotUnavailable; refusal++ {
			if commandtext.ValidateAck(commandtext.Ack{Outcome: o, Refusal: refusal, Source: source, Owner: owner}) == nil {
				ack(fmt.Sprintf("refused %d %d", o, refusal), commandtext.Ack{Outcome: o, Refusal: refusal})
			}
		}
	}
	for _, gate := range []execution.Reason{execution.ReasonReceiptPending, execution.ReasonReceiptUnavailable, execution.ReasonModeRequired} {
		ack("receipt refused "+string(gate), commandtext.Ack{Outcome: commandtext.ReceiptRefused, Gate: gate})
	}
	for _, mode := range []execution.Mode{execution.ModeFix, execution.ModeFeature, execution.ModeRefactor} {
		ack("receipt refused, mode "+mode.String()+" not met", commandtext.Ack{Outcome: commandtext.ReceiptRefused, Gate: execution.ReasonModeNotMet, Mode: mode,
			Counts: execution.ReceiptCounts{Inert: 1}, Markers: 1})
		ack("mode set "+mode.String(), commandtext.Ack{Outcome: commandtext.ModeSet, Mode: mode})
	}
	for _, plans := range []commandtext.Plans{{Canonical: true}, {Receipt: true}, {Canonical: true, Receipt: true}} {
		ack("unblocked "+boolName(plans.Canonical)+boolName(plans.Receipt), commandtext.Ack{Outcome: commandtext.Unblocked, Plans: plans})
	}
}

func boolName(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// No text says that SuiteWard does not run tests, or that it ran none: the rule holds for every comment kind, check summary and acknowledgment, whatever its
// tier, level or reason.
func TestNoTextSaysThatSuiteWardDoesNotRunTests(t *testing.T) {
	bodies := everyBody(t)
	if len(bodies) < 120 {
		t.Fatalf("only %d bodies were rendered: the test no longer reaches every text", len(bodies))
	}
	for name, body := range bodies {
		if body == "" {
			t.Errorf("%s: the body is empty", name)
		}
		for _, phrase := range notRunPhrases {
			if strings.Contains(strings.ToLower(body), phrase) {
				t.Errorf("%s: the body says %q:\n%s", name, phrase, body)
			}
		}
	}
}
