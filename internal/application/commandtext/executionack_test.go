package commandtext_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// refused is the acknowledgment of an approve of P4-R2 that the receipt gate refused for gate.
func refused(gate execution.Reason, mode execution.Mode) commandtext.Ack {
	return commandtext.Ack{Outcome: commandtext.ReceiptRefused, Proposal: 4, Revision: 2, Source: source, Owner: owner, Gate: gate, Mode: mode}
}

// summaryNotMet is the acknowledgment of an approve with a mode whose summary the receipt does not meet.
func summaryNotMet(mode execution.Mode) commandtext.Ack {
	a := refused(execution.ReasonModeNotMet, mode)
	a.Counts = execution.ReceiptCounts{Detects: 0, Inert: 2, NewAPI: 1, Fails: 1, Missing: 0, Flaky: 0, Skipped: 3, Excluded: 4}
	a.Markers = 5
	return a
}

func bare(outcome commandtext.Outcome) commandtext.Ack {
	return commandtext.Ack{Outcome: outcome, Source: source, Owner: owner}
}

const (
	codeUsed     = "The code was used and nothing was approved."
	nextCode     = "The next attempt needs the next code"
	countsLine   = "Receipt of this head: 0 DETECTS, 2 INERT, 1 NEW API, 1 FAILS, 0 MISSING, 0 FLAKY, 3 SKIPPED. Excluded tests: 4. Marker lines seen: 5."
	forkText     = "this pull request comes from a fork, and pull requests from forks are not supported yet. Nothing changed."
	notOpenText  = "this pull request is no longer open. Nothing changed."
	codeWasUsed  = "The code was used"
	unblockLifts = "The checks will state that the owner unblocked this head and that the tests did not run."
)

func TestAcknowledgeTheExecutionOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		ack     commandtext.Ack
		want    []string
		notWant []string
	}{
		{
			"receipt pending",
			refused(execution.ReasonReceiptPending, execution.ModeNone),
			[]string{"Approval of P4-R2 by @octo-owner was refused by the receipt gate: the receipt of this head is not complete yet. " + codeUsed, "`SuiteWard / Receipt`", nextCode},
			[]string{"unblock", "mode", "eligible"},
		},
		{
			"receipt unavailable",
			refused(execution.ReasonReceiptUnavailable, execution.ModeNone),
			[]string{"Approval of P4-R2 by @octo-owner was refused by the receipt gate: the receipt of this head is unavailable. " + codeUsed, "```\n/suiteward unblock <code>\n```", "Then comment the approve again, with the next code.", "lifts only a run that is unavailable", nextCode},
			[]string{"mode", "eligible"},
		},
		{
			"mode required",
			refused(execution.ReasonModeRequired, execution.ModeNone),
			[]string{"was refused by the receipt gate: this repository requires a mode with the approve, and the comment named none. " + codeUsed, "```\n/suiteward approve P4-R2 <fix|feature|refactor> <code>\n```", "`<fix|feature|refactor>`", nextCode},
			[]string{"unblock", "DETECTS"},
		},
		{
			"fix not met",
			summaryNotMet(execution.ModeFix),
			[]string{"was refused by the receipt gate: the receipt of this head does not meet the `fix` mode. " + codeUsed, "needs at least one test that detects the change (DETECTS) and no test that FAILS, is MISSING or is FLAKY", countsLine, nextCode},
			[]string{"unblock", "<fix|feature|refactor>"},
		},
		{
			"feature not met",
			summaryNotMet(execution.ModeFeature),
			[]string{"does not meet the `feature` mode", "needs at least one test that detects the change or covers a new API (DETECTS or NEW API) and no test that FAILS, is MISSING or is FLAKY", countsLine},
			[]string{"`fix`"},
		},
		{
			"refactor not met",
			summaryNotMet(execution.ModeRefactor),
			[]string{"does not meet the `refactor` mode", "needs every evaluated test to be INERT", countsLine},
			[]string{"`fix`", "`feature`"},
		},
		{
			"mode set",
			commandtext.Ack{Outcome: commandtext.ModeSet, Source: source, Owner: owner, Mode: execution.ModeFeature},
			[]string{"Preview mode recorded for this pull request by @octo-owner: `feature`.", "The receipt page shows it.", "no check and no approval reads it", "travels in the approve command"},
			[]string{"eligible", "/suiteward approve", "<code>"},
		},
		{
			"mode refused for a fork",
			commandtext.Ack{Outcome: commandtext.ModeRefused, Source: source, Owner: owner, Refusal: commandtext.Fork},
			[]string{"`/suiteward mode` was not processed: " + forkText, "`/suiteward mode <fix|feature|refactor>`"},
			[]string{"code", "approv"},
		},
		{
			"mode refused for a pull request that is not open",
			commandtext.Ack{Outcome: commandtext.ModeRefused, Source: source, Owner: owner, Refusal: commandtext.NotOpen},
			[]string{"`/suiteward mode` was not processed: " + notOpenText, "`/suiteward mode <fix|feature|refactor>`"},
			[]string{"code", "approv"},
		},
		{
			"unblocked, canonical run",
			commandtext.Ack{Outcome: commandtext.Unblocked, Source: source, Owner: owner, Plans: commandtext.Plans{Canonical: true}},
			[]string{"Unblock recorded by @octo-owner for the current head of this pull request. SuiteWard lifted what was unavailable on it: its canonical run.", unblockLifts, "stays as it is", "A new push is a new head and needs a new unblock if its run is unavailable too."},
			[]string{"receipt", "eligible"},
		},
		{
			"unblocked, receipt",
			commandtext.Ack{Outcome: commandtext.Unblocked, Source: source, Owner: owner, Plans: commandtext.Plans{Receipt: true}},
			[]string{"SuiteWard lifted what was unavailable on it: its receipt.", unblockLifts, "A new push is a new head"},
			[]string{"canonical"},
		},
		{
			"unblocked, both",
			commandtext.Ack{Outcome: commandtext.Unblocked, Source: source, Owner: owner, Plans: commandtext.Plans{Canonical: true, Receipt: true}},
			[]string{"SuiteWard lifted what was unavailable on it: its canonical run and its receipt.", unblockLifts},
			nil,
		},
		{
			"unblock refused for a fork",
			commandtext.Ack{Outcome: commandtext.UnblockRefused, Source: source, Owner: owner, Refusal: commandtext.Fork},
			[]string{"`/suiteward unblock` was not processed: " + forkText, codeWasUsed, "next code"},
			[]string{"eligible"},
		},
		{
			"unblock refused for a pull request that is not open",
			commandtext.Ack{Outcome: commandtext.UnblockRefused, Source: source, Owner: owner, Refusal: commandtext.NotOpen},
			[]string{"`/suiteward unblock` was not processed: " + notOpenText, codeWasUsed, "next code"},
			nil,
		},
		{
			"unblock refused where nothing is unavailable",
			commandtext.Ack{Outcome: commandtext.UnblockRefused, Source: source, Owner: owner, Refusal: commandtext.NotUnavailable},
			[]string{"`/suiteward unblock` was not processed: no run of this head is unavailable, so there is nothing to unblock. Nothing changed.", "a pending, failing or complete run is never lifted", codeWasUsed, "next code"},
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := commandtext.ValidateAck(tc.ack); err != nil {
				t.Errorf("ValidateAck refuses it: %v", err)
			}
			body := render(t, func() string { return commandtext.Acknowledge(tc.ack) })
			if body == "" {
				t.Fatal("empty body")
			}
			checkWantedText(t, body, tc.want, tc.notWant)
			checkReplyLink(t, tc.ack.Outcome, body)
			checkBody(t, body)
			checkBodyInert(t, body)
			if strings.Contains(body, "http") {
				t.Errorf("body holds a host name or URL:\n%s", body)
			}
		})
	}
}

// The text of a refusal that does not depend on the mode is the same with or without the mode that the approve carried.
func TestAcknowledgeAReceiptRefusalThatDoesNotDependOnTheMode(t *testing.T) {
	for _, gate := range []execution.Reason{execution.ReasonReceiptPending, execution.ReasonReceiptUnavailable} {
		without := render(t, func() string { return commandtext.Acknowledge(refused(gate, execution.ModeNone)) })
		with := render(t, func() string { return commandtext.Acknowledge(refused(gate, execution.ModeFix)) })
		if without == "" || without != with {
			t.Errorf("the text of %q depends on the mode:\n%s\nagainst\n%s", gate, without, with)
		}
	}
}

// A refusal that consumed a code says so, and one that did not never speaks of a code.
func TestAcknowledgeAReceiptRefusalAndAnUnblockRefusalSayThatTheCodeWasUsed(t *testing.T) {
	for name, ack := range map[string]commandtext.Ack{
		"receipt pending":     refused(execution.ReasonReceiptPending, execution.ModeNone),
		"receipt unavailable": refused(execution.ReasonReceiptUnavailable, execution.ModeNone),
		"mode required":       refused(execution.ReasonModeRequired, execution.ModeNone),
		"mode not met":        summaryNotMet(execution.ModeFix),
		"unblock fork":        {Outcome: commandtext.UnblockRefused, Source: source, Owner: owner, Refusal: commandtext.Fork},
		"unblock not open":    {Outcome: commandtext.UnblockRefused, Source: source, Owner: owner, Refusal: commandtext.NotOpen},
		"unblock nothing":     {Outcome: commandtext.UnblockRefused, Source: source, Owner: owner, Refusal: commandtext.NotUnavailable},
	} {
		body := render(t, func() string { return commandtext.Acknowledge(ack) })
		if !strings.Contains(body, "The code was used") || !strings.Contains(body, "next code") {
			t.Errorf("%s: body does not say that the code was used and the next attempt needs the next code:\n%s", name, body)
		}
	}
	for name, ack := range map[string]commandtext.Ack{
		"mode set":      {Outcome: commandtext.ModeSet, Source: source, Owner: owner, Mode: execution.ModeFix},
		"mode fork":     {Outcome: commandtext.ModeRefused, Source: source, Owner: owner, Refusal: commandtext.Fork},
		"mode not open": {Outcome: commandtext.ModeRefused, Source: source, Owner: owner, Refusal: commandtext.NotOpen},
	} {
		if body := render(t, func() string { return commandtext.Acknowledge(ack) }); strings.Contains(strings.ToLower(body), "code was") || strings.Contains(body, "next code") {
			t.Errorf("%s: a command that takes no code speaks of one:\n%s", name, body)
		}
	}
}

// The Ack names the facts of the gate, so the counts are stated whatever they hold, zero included.
func TestAcknowledgeAnUnmetModeStatesEveryCount(t *testing.T) {
	a := refused(execution.ReasonModeNotMet, execution.ModeFix)
	a.Counts = execution.ReceiptCounts{Detects: 12, Inert: 34, NewAPI: 5, Fails: 6, Missing: 7, Flaky: 8, Skipped: 9, Excluded: 10}
	a.Markers = 1
	body := render(t, func() string { return commandtext.Acknowledge(a) })
	want := "Receipt of this head: 12 DETECTS, 34 INERT, 5 NEW API, 6 FAILS, 7 MISSING, 8 FLAKY, 9 SKIPPED. Excluded tests: 10. Marker lines seen: 1."
	if !strings.Contains(body, want) {
		t.Errorf("body lacks %q:\n%s", want, body)
	}
}

// Each field belongs to its outcome: Acknowledge panics on what ValidateAck refuses, and ValidateAck refuses a field that does not fit.
func TestAcknowledgePanicsOnAnExecutionFieldThatDoesNotFit(t *testing.T) {
	withMode := func(a commandtext.Ack, mode execution.Mode) commandtext.Ack { a.Mode = mode; return a }
	notMet := func() commandtext.Ack { return summaryNotMet(execution.ModeFix) }
	counts := func(change func(*execution.ReceiptCounts)) commandtext.Ack {
		a := notMet()
		change(&a.Counts)
		return a
	}
	approved := commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 1, Source: source, Owner: owner}
	for name, ack := range map[string]commandtext.Ack{
		"a receipt refusal without a gate reason":              refused("", execution.ModeNone),
		"a receipt refusal for an unknown gate reason":         refused("no-such-reason", execution.ModeNone),
		"a receipt refusal for a reason of the canonical gate": refused(execution.ReasonCanonicalPending, execution.ModeNone),
		"a receipt refusal for a reason of a summary":          refused(execution.ReasonBlockingTests, execution.ModeNone),
		"a receipt refusal without a reference":                {Outcome: commandtext.ReceiptRefused, Source: source, Owner: owner, Gate: execution.ReasonReceiptPending},
		"a receipt refusal without a source":                   {Outcome: commandtext.ReceiptRefused, Proposal: 4, Revision: 2, Owner: owner, Gate: execution.ReasonReceiptPending},
		"a receipt refusal with eligibility":                   {Outcome: commandtext.ReceiptRefused, Proposal: 4, Revision: 2, Source: source, Owner: owner, Gate: execution.ReasonReceiptPending, Eligible: true},
		"a mode required with a mode":                          refused(execution.ReasonModeRequired, execution.ModeFix),
		"an unmet mode without a mode":                         withMode(notMet(), execution.ModeNone),
		"an unknown mode":                                      withMode(notMet(), 99),
		"a negative count":                                     counts(func(c *execution.ReceiptCounts) { c.Fails = -1 }),
		"a negative excluded count":                            counts(func(c *execution.ReceiptCounts) { c.Excluded = -1 }),
		"a negative count of files that do not parse":          counts(func(c *execution.ReceiptCounts) { c.Unparsed = -1 }),
		"a negative marker count":                              func() commandtext.Ack { a := notMet(); a.Markers = -1; return a }(),
		"counts on a pending receipt": func() commandtext.Ack {
			a := refused(execution.ReasonReceiptPending, 0)
			a.Counts.Detects = 1
			return a
		}(),
		"markers on a mode that is required":        func() commandtext.Ack { a := refused(execution.ReasonModeRequired, 0); a.Markers = 1; return a }(),
		"a gate reason on an approval":              func() commandtext.Ack { a := approved; a.Gate = execution.ReasonModeRequired; return a }(),
		"a mode on an approval":                     withMode(approved, execution.ModeFix),
		"counts on an approval":                     func() commandtext.Ack { a := approved; a.Counts.Inert = 1; return a }(),
		"plans on an approval":                      func() commandtext.Ack { a := approved; a.Plans.Receipt = true; return a }(),
		"a mode set without a mode":                 bare(commandtext.ModeSet),
		"a mode set with an unknown mode":           withMode(bare(commandtext.ModeSet), 99),
		"a mode set with a reference":               {Outcome: commandtext.ModeSet, Proposal: 1, Revision: 1, Source: source, Owner: owner, Mode: execution.ModeFix},
		"a mode set with eligibility":               {Outcome: commandtext.ModeSet, Source: source, Owner: owner, Mode: execution.ModeFix, Eligible: true},
		"a mode set without a source":               {Outcome: commandtext.ModeSet, Owner: owner, Mode: execution.ModeFix},
		"a mode set with a refusal":                 {Outcome: commandtext.ModeSet, Source: source, Owner: owner, Mode: execution.ModeFix, Refusal: commandtext.Fork},
		"a mode refusal without a refusal":          bare(commandtext.ModeRefused),
		"a mode refusal for an unavailable run":     {Outcome: commandtext.ModeRefused, Source: source, Owner: owner, Refusal: commandtext.NotUnavailable},
		"a mode refusal of a propose":               {Outcome: commandtext.ModeRefused, Source: source, Owner: owner, Refusal: commandtext.EmptyInventory},
		"a mode refusal with a reference":           {Outcome: commandtext.ModeRefused, Proposal: 1, Revision: 1, Source: source, Owner: owner, Refusal: commandtext.Fork},
		"a mode refusal with a mode":                {Outcome: commandtext.ModeRefused, Source: source, Owner: owner, Refusal: commandtext.Fork, Mode: execution.ModeFix},
		"an unblock refusal without a refusal":      bare(commandtext.UnblockRefused),
		"an unblock refusal of a skip":              {Outcome: commandtext.UnblockRefused, Source: source, Owner: owner, Refusal: commandtext.Carrier},
		"an unblock refusal with a reference":       {Outcome: commandtext.UnblockRefused, Proposal: 1, Revision: 1, Source: source, Owner: owner, Refusal: commandtext.Fork},
		"an unblock refusal with plans":             {Outcome: commandtext.UnblockRefused, Source: source, Owner: owner, Refusal: commandtext.Fork, Plans: commandtext.Plans{Canonical: true}},
		"an unblock without plans":                  bare(commandtext.Unblocked),
		"an unblock with a refusal":                 {Outcome: commandtext.Unblocked, Source: source, Owner: owner, Plans: commandtext.Plans{Receipt: true}, Refusal: commandtext.Fork},
		"an unblock with a reference":               {Outcome: commandtext.Unblocked, Proposal: 1, Revision: 1, Source: source, Owner: owner, Plans: commandtext.Plans{Receipt: true}},
		"an unblock with a mode":                    {Outcome: commandtext.Unblocked, Source: source, Owner: owner, Plans: commandtext.Plans{Receipt: true}, Mode: execution.ModeFix},
		"an unblock with eligibility":               {Outcome: commandtext.Unblocked, Source: source, Owner: owner, Plans: commandtext.Plans{Receipt: true}, Eligible: true},
		"an unblock with an owner that is no login": {Outcome: commandtext.Unblocked, Source: source, Owner: "x](https://e.test)", Plans: commandtext.Plans{Receipt: true}},
	} {
		t.Run(name, func(t *testing.T) {
			mustPanic(t, func() { commandtext.Acknowledge(ack) })
			if commandtext.ValidateAck(ack) == nil {
				t.Error("ValidateAck accepts it")
			}
		})
	}
}

// A changed test file that go cannot parse blocks every mode, so the acknowledgment of an unmet mode says how many there are when there are some, and nothing of the
// subject when there are none.
func TestAcknowledgeAnUnmetModeStatesTheChangedTestFilesThatDoNotParseOnlyWhenThereAreSome(t *testing.T) {
	const unparsed = "Changed test files that do not parse: 3."
	a := refused(execution.ReasonModeNotMet, execution.ModeFix)
	a.Counts = execution.ReceiptCounts{Detects: 1, Unparsed: 3}
	body := render(t, func() string { return commandtext.Acknowledge(a) })
	if !strings.Contains(body, unparsed) {
		t.Errorf("body lacks %q:\n%s", unparsed, body)
	}

	a.Counts.Unparsed = 0
	body = render(t, func() string { return commandtext.Acknowledge(a) })
	if strings.Contains(body, "do not parse") {
		t.Errorf("body speaks of files that do not parse though there are none:\n%s", body)
	}
}

const (
	stoppedWaiting = "SuiteWard stopped waiting for the test runner, which was not responding."
	runnerResponds = "The test runner is responding, so the tests of this commit will run. Nothing was unblocked."
)

// An unblock that ended an open job says that SuiteWard stopped waiting for the test runner, which was not responding; one that only lifted what was unavailable says
// nothing of it.
func TestAcknowledgeAnUnblockThatEndedAJobSaysThatSuiteWardStoppedWaiting(t *testing.T) {
	both := commandtext.Plans{Canonical: true, Receipt: true}
	for name, tc := range map[string]struct {
		ack  commandtext.Ack
		want bool
	}{
		"the canonical run ended": {commandtext.Ack{Outcome: commandtext.Unblocked, Source: source, Owner: owner, Plans: both, Ended: commandtext.Plans{Canonical: true}}, true},
		"the receipt ended":       {commandtext.Ack{Outcome: commandtext.Unblocked, Source: source, Owner: owner, Plans: both, Ended: commandtext.Plans{Receipt: true}}, true},
		"both ended":              {commandtext.Ack{Outcome: commandtext.Unblocked, Source: source, Owner: owner, Plans: both, Ended: both}, true},
		"none ended":              {commandtext.Ack{Outcome: commandtext.Unblocked, Source: source, Owner: owner, Plans: both}, false},
	} {
		if err := commandtext.ValidateAck(tc.ack); err != nil {
			t.Errorf("%s: ValidateAck refuses it: %v", name, err)
		}
		body := render(t, func() string { return commandtext.Acknowledge(tc.ack) })
		if has := strings.Contains(body, stoppedWaiting); has != tc.want {
			t.Errorf("%s: the body says that SuiteWard stopped waiting = %v, want %v:\n%s", name, has, tc.want, body)
		}
	}
}

func TestAcknowledgeAnUnblockRefusedBecauseTheRunnerRespondsSaysThatTheTestsWillRunAndThatTheCodeWasUsed(t *testing.T) {
	ack := commandtext.Ack{Outcome: commandtext.UnblockRefused, Source: source, Owner: owner, Refusal: commandtext.RunnerResponding}
	if err := commandtext.ValidateAck(ack); err != nil {
		t.Fatalf("ValidateAck refuses it: %v", err)
	}
	body := render(t, func() string { return commandtext.Acknowledge(ack) })
	checkWantedText(t, body, []string{runnerResponds, "The code was used", "next code"}, []string{"eligible", "nothing to unblock"})
	checkBody(t, body)
	checkBodyInert(t, body)
}

func TestValidateAckRefusesEndedPlansThatDoNotFit(t *testing.T) {
	both := commandtext.Plans{Canonical: true, Receipt: true}
	for name, ack := range map[string]commandtext.Ack{
		"an ended plan that was not lifted":  {Outcome: commandtext.Unblocked, Source: source, Owner: owner, Plans: commandtext.Plans{Canonical: true}, Ended: commandtext.Plans{Receipt: true}},
		"ended plans on a refusal":           {Outcome: commandtext.UnblockRefused, Source: source, Owner: owner, Refusal: commandtext.RunnerResponding, Ended: both},
		"ended plans on a mode that was set": {Outcome: commandtext.ModeSet, Source: source, Owner: owner, Mode: execution.ModeFix, Ended: both},
		"ended plans on a receipt refusal":   {Outcome: commandtext.ReceiptRefused, Source: source, Owner: owner, Proposal: 4, Revision: 2, Gate: execution.ReasonReceiptPending, Ended: both},
	} {
		if err := commandtext.ValidateAck(ack); err == nil {
			t.Errorf("%s: ValidateAck accepts it", name)
		}
	}
}
