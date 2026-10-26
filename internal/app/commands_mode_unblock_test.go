package app

import (
	"fmt"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// runRecovering runs the job and reports a panic as an error: before the verbs were routed away, the job panicked in consent on them.
func (w *cjWorld) runRecovering() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the job panicked: %v", r)
		}
	}()
	return w.run()
}

// The parser reads the mode and the unblock verbs, but the commands job does not run them yet: until it does, they are acknowledged as an unknown
// verb, as before the parser knew them. They never reach consent, which would ask for a reference they do not carry, and no code is consumed.
func TestCommandsJobAcknowledgesTheModeAndUnblockVerbsAsUnknownAndNeverConsumesACode(t *testing.T) {
	created := cjAt(10)
	for name, body := range map[string]string{
		"mode":                      cjMarker + "\n/suiteward mode fix",
		"mode in capitals":          "/SUITEWARD MODE Refactor",
		"unblock":                   cjMarker + "\n/suiteward unblock " + cjCode(created, 0),
		"unblock in capitals":       "/SUITEWARD UNBLOCK " + cjCode(created, 0),
		"unblock with another code": "/suiteward unblock " + cjCode(created, 1),
	} {
		t.Run(name, func(t *testing.T) {
			w := newCjWorld(t)
			w.syncAll([]cjComment{{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: created, body: body}})

			if err := w.runRecovering(); err != nil {
				t.Fatalf("run: %v\nlogs: %s", err, w.logs)
			}

			w.requireRejected(cjID1, 500, commands.Malformed, commandtext.UnknownVerb, "")
			if len(w.owners.consumed) != 0 {
				t.Fatalf("the job consumed %d steps; want none, since the command never reaches consent", len(w.owners.consumed))
			}
			w.requireNothingLeaked(cjCode(created, 0), cjCode(created, 1))
			w.requireNoEntryAfterSecondRun(500)
		})
	}
}

// The two malformed reasons of the mode verb have their codes: the job acknowledges the comment and does not fail on it.
func TestCommandsJobAcknowledgesAMalformedModeCommand(t *testing.T) {
	created := cjAt(10)
	for name, tc := range map[string]struct {
		body   string
		reason commandtext.Reason
	}{
		"no mode":            {cjMarker + "\n/suiteward mode", commandtext.MissingMode},
		"a bad mode":         {cjMarker + "\n/suiteward mode loud", commandtext.BadMode},
		"extra text":         {cjMarker + "\n/suiteward mode fix now", commandtext.ExtraText},
		"a code as the mode": {"/suiteward mode " + cjCode(created, 0), commandtext.BadMode},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCjWorld(t)
			w.syncAll([]cjComment{{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: created, body: tc.body}})

			if err := w.runRecovering(); err != nil {
				t.Fatalf("run: %v\nlogs: %s", err, w.logs)
			}

			w.requireRejected(cjID1, 500, commands.Malformed, tc.reason, "")
			w.requireNothingLeaked(cjCode(created, 0))
		})
	}
}

// The payload of the acknowledgment of each of the new reasons is stored and rendered.
func TestMalformedCodesHoldTheReasonsOfTheMode(t *testing.T) {
	for reason, code := range map[commandtext.Reason]string{commandtext.MissingMode: "missing-mode", commandtext.BadMode: "bad-mode"} {
		if got := reasonCode(reason); got != code {
			t.Errorf("reasonCode(%d) = %q, want %q", reason, got, code)
		}
		if got, ok := codeReason(code); !ok || got != reason {
			t.Errorf("codeReason(%q) = %d, %v, want %d", code, got, ok, reason)
		}
	}
}

func TestCommentPublisherRendersTheMalformedReasonsOfTheMode(t *testing.T) {
	for _, reason := range []commandtext.Reason{commandtext.MissingMode, commandtext.BadMode} {
		t.Run(fmt.Sprint(reason), func(t *testing.T) {
			w := newCmtWorld(t)
			message, err := newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.Malformed, reason, "")
			if err != nil {
				t.Fatalf("newRejectedAck: %v", err)
			}

			w.mustDeliver(message)

			w.wantPost(message.Key, commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: reason, Owner: cmtOwner, Source: cmtComment}))
		})
	}
}
