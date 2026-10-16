package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
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
		{"a cancelled comment whose reference is not P<n>-R<m>", cmtRaw(cmtBootCancelledKey("P1-R1"), with(map[string]any{"reference": cmtSecretText}))},
		{"a cancelled comment under another repository", cmtRaw(fmt.Sprintf("bootstrap-cancelled:%d:P1-R1", cmtRepoID+1), with(map[string]any{"reference": "P1-R1"}))},
		{"a cancelled comment with a field it does not have", cmtRaw(cmtBootCancelledKey("P1-R1"), with(map[string]any{"reference": "P1-R1", "body": cmtSecretText}))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)

			err := w.deliver(tc.message)

			if err == nil {
				t.Fatal("publish succeeded")
			}
			for _, leak := range []string{cmtSecretText, cmtRepo} {
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
