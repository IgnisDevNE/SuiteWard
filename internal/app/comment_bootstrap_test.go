package app

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The payloads of bootstrap-notice and bootstrap-cancelled are built from raw JSON here, so the test pins their key names.

func cmtBootNoticeKey() string { return fmt.Sprintf("bootstrap-notice:%d:%d", cmtRepoID, cmtPull) }

func cmtBootCancelledKey(reference string) string {
	return fmt.Sprintf("bootstrap-cancelled:%d:%s", cmtRepoID, reference)
}

func cmtRaw(key string, payload map[string]any) governance.OutboxMessage {
	return governance.OutboxMessage{Key: key, Kind: githubCommentKind, Payload: cmtMust(json.Marshal(payload))}
}

func cmtBootNotice() governance.OutboxMessage {
	return cmtRaw(cmtBootNoticeKey(), map[string]any{"repositoryId": cmtRepoID, "repo": cmtRepo, "pull": cmtPull})
}

func cmtBootCancelled(reference string) governance.OutboxMessage {
	return cmtRaw(cmtBootCancelledKey(reference), map[string]any{"repositoryId": cmtRepoID, "repo": cmtRepo, "pull": cmtPull, "reference": reference})
}

func TestCommentPublisherRendersTheBootstrapNoticeAndTheCancelledComment(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message governance.OutboxMessage
		text    string
	}{
		{"notice", cmtBootNotice(), commandtext.BootstrapNotice()},
		{"cancelled", cmtBootCancelled("P2-R3"), commandtext.BootstrapCancelled(2, 3)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)

			w.mustDeliver(tc.message)
			w.mustDeliver(tc.message) // a redelivery finds its marker

			w.wantPost(tc.message.Key, tc.text)
		})
	}
}

func TestCommentPublisherRefusesABootstrapNoticeOrCancelledCommentThatDoesNotMatchItsKey(t *testing.T) {
	target := map[string]any{"repositoryId": cmtRepoID, "repo": cmtRepo, "pull": cmtPull}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range target {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for _, tc := range []struct {
		name    string
		message governance.OutboxMessage
	}{
		{"a notice under another pull", cmtRaw(fmt.Sprintf("bootstrap-notice:%d:%d", cmtRepoID, cmtPull+1), target)},
		{"a notice under another repository", cmtRaw(fmt.Sprintf("bootstrap-notice:%d:%d", cmtRepoID+1, cmtPull), target)},
		{"a notice with a field it does not have", cmtRaw(cmtBootNoticeKey(), with(map[string]any{"reference": "P1-R1"}))},
		{"a notice with a pull that is not a pull request", cmtRaw(cmtBootNoticeKey(), with(map[string]any{"pull": 0}))},
		{"a notice under a key that is not a pull number", cmtRaw(fmt.Sprintf("bootstrap-notice:%d:P1-R1", cmtRepoID), target)},
		{"a cancelled comment under another reference", cmtRaw(cmtBootCancelledKey("P1-R2"), with(map[string]any{"reference": "P1-R1"}))},
		{"a cancelled comment without a reference", cmtRaw(cmtBootCancelledKey("P1-R1"), target)},
		{"a cancelled comment whose reference is not P<n>-R<m>", cmtRaw(cmtBootCancelledKey("P1-R1"), with(map[string]any{"reference": cmtLeakText}))},
		{"a cancelled comment under another repository", cmtRaw(fmt.Sprintf("bootstrap-cancelled:%d:P1-R1", cmtRepoID+1), with(map[string]any{"reference": "P1-R1"}))},
		{"a cancelled comment with a field it does not have", cmtRaw(cmtBootCancelledKey("P1-R1"), with(map[string]any{"reference": "P1-R1", "body": cmtLeakText}))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)

			err := w.deliver(tc.message)

			if err == nil {
				t.Fatal("publish succeeded")
			}
			for _, leak := range []string{cmtLeakText, cmtRepo} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("the error %q holds payload text %q", err, leak)
				}
			}
			if len(w.writer.posts()) != 0 || len(w.writer.listed) != 0 {
				t.Fatal("GitHub was called for a payload that was refused")
			}
		})
	}
}

// cmtBootPromoted is a bootstrap-promoted message built from raw JSON, with the keys of extra added: the test pins the name and the values of the level.
func cmtBootPromoted(extra map[string]any) governance.OutboxMessage {
	payload := map[string]any{"repositoryId": cmtRepoID, "repo": cmtRepo, "pull": cmtPull, "reference": "P1-R1", "version": "V1"}
	maps.Copy(payload, extra)
	return cmtRaw(fmt.Sprintf("bootstrap-promoted:%d:P1-R1", cmtRepoID), payload)
}

// The V1 comment says that the approved tests run from now on where the canonical level of the promoted declaration runs them (SW-2, docs/plan/phases/M2.1c.md, text 6), and is
// what it was where the level does not and for a message queued before the level was carried, which has no such key.
func TestCommentPublisherSaysThatTheApprovedTestsRunWhereTheCanonicalLevelRunsThem(t *testing.T) {
	for _, tc := range []struct {
		name   string
		extra  map[string]any
		level  domain.CanonicalLevel // the level the comment is rendered for; none for the message without the key
		runs   bool
		checks bool // the comment says that the checks of a commit wait for its run
	}{
		{"a message from before the level was carried", nil, 0, false, false},
		{"canonical off", map[string]any{"canonical": "off"}, domain.CanonicalLevelOff, false, false},
		{"canonical advisory", map[string]any{"canonical": "advisory"}, domain.CanonicalLevelAdvisory, true, false},
		{"canonical required", map[string]any{"canonical": "required"}, domain.CanonicalLevelRequired, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			message := cmtBootPromoted(tc.extra)

			w.mustDeliver(message)

			w.wantPost(message.Key, commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.BootstrapPromoted, Proposal: 1, Revision: 1, Version: 1, Owner: cmtOwner, Canonical: tc.level}))
			body := w.writer.posts()[0].body
			if got := strings.Contains(body, "SuiteWard runs the approved tests on every open pull request and on every push."); got != tc.runs {
				t.Errorf("the comment says that the tests run: %v, want %v:\n%s", got, tc.runs, body)
			}
			if got := strings.Contains(body, "The checks of a commit wait for its run."); got != tc.checks {
				t.Errorf("the comment says that the checks wait for the run: %v, want %v:\n%s", got, tc.checks, body)
			}
		})
	}
}

// A level that is not one of the three is refused at decode, as the other fields of the payload are, and the error never quotes the payload.
func TestCommentPublisherRefusesAPromotionWhoseCanonicalLevelIsNotKnown(t *testing.T) {
	for name, level := range map[string]any{
		"a word that is not a level":   cmtLeakText,
		"a level in another spelling":  "Advisory",
		"a level of another policy":    "before-approval",
		"a number":                     2,
		"a level that is not a string": true,
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)

			err := w.deliver(cmtBootPromoted(map[string]any{"canonical": level}))

			if err == nil {
				t.Fatal("publish succeeded")
			}
			if strings.Contains(err.Error(), cmtLeakText) {
				t.Fatalf("the error %q holds payload text", err)
			}
			if len(w.writer.posts()) != 0 || len(w.writer.listed) != 0 {
				t.Fatal("GitHub was called for a payload that was refused")
			}
		})
	}
}
