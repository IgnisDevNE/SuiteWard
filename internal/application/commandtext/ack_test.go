package commandtext_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// ValidateAck is the one place that knows which fields an acknowledgment may carry: Acknowledge panics when it refuses, and a
// caller that stores the payload calls it first (ADRs 0011 and 0027).

func TestParseReference(t *testing.T) {
	for _, tc := range []struct {
		ref                bool
		in                 string
		proposal, revision int
	}{
		{true, "P1-R1", 1, 1},
		{true, "P42-R3", 42, 3},
		{true, "P999999999-R999999999", 999999999, 999999999},
		{false, "", 0, 0},
		{false, "P1R1", 0, 0},
		{false, "P0-R1", 0, 0},
		{false, "P1-R0", 0, 0},
		{false, "P01-R1", 0, 0},
		{false, "P1-R01", 0, 0},
		{false, "p1-r1", 0, 0},
		{false, "P+1-R1", 0, 0},
		{false, "P1-R1 ", 0, 0},
		{false, "P1000000000-R1", 0, 0},
		{false, "P1-R1000000000", 0, 0},
		{false, "P1-R1-R2", 0, 0},
	} {
		t.Run(tc.in, func(t *testing.T) {
			proposal, revision, ok := commandtext.ParseReference(tc.in)
			if ok != tc.ref || proposal != tc.proposal || revision != tc.revision {
				t.Errorf("ParseReference(%q) = %d, %d, %v, want %d, %d, %v", tc.in, proposal, revision, ok, tc.proposal, tc.revision, tc.ref)
			}
		})
	}
}

func TestParseReferenceReadsWhatReferenceWrites(t *testing.T) {
	proposal, revision, ok := commandtext.ParseReference(commandtext.Reference(7, 12))
	if !ok || proposal != 7 || revision != 12 {
		t.Errorf("ParseReference(Reference(7, 12)) = %d, %d, %v", proposal, revision, ok)
	}
}

func TestValidLogin(t *testing.T) {
	for login, want := range map[string]bool{
		"octo-owner":                  true,
		"o":                           true,
		"octocat_acme":                true,
		"a" + strings.Repeat("b", 38): true,
		"":                            false,
		"_octo":                       false,
		"-octo":                       false,
		"a" + strings.Repeat("b", 39): false,
		"octo owner":                  false,
		"x](https://e.test)":          false,
		"octo\n":                      false,
		"\u00e9":                      false,
	} {
		if got := commandtext.ValidLogin(login); got != want {
			t.Errorf("ValidLogin(%q) = %v, want %v", login, got, want)
		}
	}
}

func TestValidateAckRefusesAnOutcomeOrAReasonThatIsNone(t *testing.T) {
	for name, ack := range map[string]commandtext.Ack{
		"an unknown outcome":          {Outcome: 99, Proposal: 1, Revision: 1, Source: source, Owner: owner},
		"an unknown rejection":        {Outcome: commandtext.Rejected, Rejection: 99, Proposal: 1, Revision: 1, Source: source, Owner: owner},
		"an unknown malformed reason": {Outcome: commandtext.MalformedCommand, Malformed: 99, Source: source, Owner: owner},
	} {
		t.Run(name, func(t *testing.T) {
			if commandtext.ValidateAck(ack) == nil {
				t.Error("ValidateAck accepts it")
			}
			mustPanic(t, func() { commandtext.Acknowledge(ack) })
		})
	}
}

// Acknowledge panics on what ValidateAck refuses and on nothing else.
func TestValidateAckAcceptsEveryOutcomeThatAcknowledgeRenders(t *testing.T) {
	ref := commandtext.Ack{Proposal: 4, Revision: 2, Source: source, Owner: owner}
	bare := commandtext.Ack{Source: source, Owner: owner}
	for outcome, ack := range map[commandtext.Outcome]commandtext.Ack{
		commandtext.Approved:               ref,
		commandtext.Revoked:                ref,
		commandtext.NoActiveApproval:       ref,
		commandtext.RevokedAfterPromotion:  ref,
		commandtext.ApprovedAfterPromotion: ref,
		commandtext.InvalidCode:            ref,
		commandtext.ReplayedCode:           ref,
		commandtext.NoSuite:                ref,
		commandtext.AlreadyProposed:        ref,
		commandtext.OtherPullRequest:       ref,
		commandtext.Rejected:               {Rejection: commandtext.Unauthorized, Proposal: 4, Revision: 2, Source: source, Owner: owner},
		commandtext.MalformedCommand:       {Malformed: commandtext.ExtraText, Source: source, Owner: owner},
		commandtext.EditedFirst:            bare,
		commandtext.ProposeFailed:          bare,
		commandtext.Skipped:                bare,
		commandtext.NothingUncovered:       {Base: commandtext.BaseContract, Source: source, Owner: owner},
		commandtext.ProposeRefused:         {Refusal: commandtext.Fork, Source: source, Owner: owner},
		commandtext.SkipRefused:            {Refusal: commandtext.Carrier, Source: source, Owner: owner},
		commandtext.SuggestRefused:         {Refusal: commandtext.Fork, Source: source, Owner: owner},
		commandtext.BootstrapPromoted:      {Version: 1, Proposal: 4, Revision: 2, Owner: owner},
	} {
		ack.Outcome = outcome
		if err := commandtext.ValidateAck(ack); err != nil {
			t.Errorf("outcome %d: ValidateAck refuses it: %v", outcome, err)
		}
		if body := render(t, func() string { return commandtext.Acknowledge(ack) }); body == "" {
			t.Errorf("outcome %d: Acknowledge renders nothing", outcome)
		}
	}
}
