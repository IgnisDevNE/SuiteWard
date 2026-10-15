package commandtext_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

const mergeSHA = "9c8b7a6f5e4d3c2b1a0f9e8d7c6b5a4f3e2d1c0b"

// blockedReasons are the blocked reasons of the persistence contract, each with a phrase only its text holds.
var blockedReasons = []struct{ reason, phrase string }{
	{"unapproved-change", "never saw a contract change proposal"},
	{"invalid-source", "is invalid"},
	{"assessment-conflict", "different integrity assessment"},
	{"approval-missing", "approval of this exact revision was not current"},
	{"differs-from-approved", "differ from the exact revision the owner approved"},
	{"not-current", "was no longer the current revision"},
	{"canonical-changed", "canonical contract changed after"},
	{"policy-changed", "policy changed after"},
	{"integrity-not-passed", "integrity assessment of the merge commit did not pass"},
	{"integration-mismatch", "does not match the approved change"},
	{"correction-context-reused", "already used for another correction"},
	{"fork-unsupported", "from forks are not supported"},
	{"shared-merge-commit", "another merged pull request has the same merge commit"},
	{"integration-unreadable", "could not read the merge commit"},
	{"integrity-failed", "failed its integrity verification"},
}

func blocked(reason string) commandtext.Merge {
	return commandtext.Merge{Kind: commandtext.MergeBlocked, Reason: reason, Source: mergeSHA, Version: 3, Proposal: 2, Revision: 1, Tier: commandtext.Isolated}
}

// mergeText reports a panic as a test error so every case shows its own failure.
func mergeText(t *testing.T, m commandtext.Merge) string {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: panicked: %v", m.Reason, r)
		}
	}()
	return commandtext.MergeComment(m)
}

func TestMergeCommentStatesEveryBlockedReasonAndTheRecovery(t *testing.T) {
	seen := map[string]string{}
	for _, tc := range blockedReasons {
		text := mergeText(t, blocked(tc.reason))
		if !strings.Contains(text, tc.phrase) {
			t.Errorf("%s: the comment lacks %q:\n%s", tc.reason, tc.phrase, text)
		}
		if !strings.Contains(text, "stays at V3") {
			t.Errorf("%s: the comment does not say that the canonical stays at V3:\n%s", tc.reason, text)
		}
		if !strings.Contains(text, "restores the protected files of V3") || !strings.Contains(text, "new exact approval") || !strings.Contains(text, "no reset command") {
			t.Errorf("%s: the comment does not state the recovery:\n%s", tc.reason, text)
		}
		if other, duplicate := seen[text]; duplicate {
			t.Errorf("%s and %s render the same comment", tc.reason, other)
		}
		seen[text] = tc.reason
		if tc.reason != "integration-unreadable" && !strings.Contains(text, mergeSHA) {
			t.Errorf("%s: the comment does not name the merge commit:\n%s", tc.reason, text)
		}
	}
}

func TestMergeCommentForAnUnreadableMergeCommitNeedsNoCommit(t *testing.T) {
	m := blocked("integration-unreadable")
	m.Source = ""
	text := mergeText(t, m)
	if text == "" || strings.Contains(text, "`"+mergeSHA) || strings.Contains(text, "Merge commit:") {
		t.Errorf("the comment is empty or names a commit that does not exist:\n%s", text)
	}
}

func TestMergeCommentForAFailedIntegrityVerificationSendsTheOperatorToTheArtifactStore(t *testing.T) {
	text := mergeText(t, blocked("integrity-failed"))
	if !strings.Contains(text, "restore the artifact store from a backup") || strings.Contains(text, "push a corrected") {
		t.Errorf("the comment does not tell the operator to restore the store:\n%s", text)
	}
}

func TestMergeCommentForAPromotionNamesTheVersionAndTheCommitAndSaysTestsWereNotRun(t *testing.T) {
	for name, m := range map[string]commandtext.Merge{
		"promoted":  {Kind: commandtext.MergePromoted, Source: mergeSHA, Version: 2, Proposal: 2, Revision: 1, Tier: commandtext.Isolated},
		"corrected": {Kind: commandtext.MergeCorrected, Source: mergeSHA, Version: 3, Corrects: 2, Proposal: 4, Revision: 2, Tier: commandtext.Isolated},
	} {
		text := mergeText(t, m)
		want := []string{"V" + strconv.Itoa(m.Version), mergeSHA, commandtext.Reference(m.Proposal, m.Revision), "did not run the tests",
			"checked the approval and the integrity of the protected files"}
		if m.Corrects != 0 {
			want = append(want, "correction of V2")
		}
		for _, phrase := range want {
			if !strings.Contains(text, phrase) {
				t.Errorf("%s: the comment lacks %q:\n%s", name, phrase, text)
			}
		}
		for _, claim := range []string{"tests passed", "tests ran", "tests were run", "tests succeeded"} {
			if strings.Contains(strings.ToLower(text), claim) {
				t.Errorf("%s: the comment claims %q:\n%s", name, claim, text)
			}
		}
	}
}

func TestMergeCommentStatesTheCoLocatedTierOnly(t *testing.T) {
	for _, m := range []commandtext.Merge{blocked("canonical-changed"), {Kind: commandtext.MergePromoted, Source: mergeSHA, Version: 2, Tier: commandtext.Isolated}} {
		isolated := mergeText(t, m)
		m.Tier = commandtext.CoLocated
		colocated := mergeText(t, m)
		if !strings.Contains(isolated, "- Installation tier: `isolated`") || strings.Contains(isolated, "assurance is reduced") {
			t.Errorf("the isolated comment does not name its tier or states a reduced assurance:\n%s", isolated)
		}
		if !strings.Contains(colocated, "assurance is reduced") || !strings.Contains(colocated, "- Installation tier: `co-located`") {
			t.Errorf("the co-located comment does not state the reduced assurance:\n%s", colocated)
		}
	}
}

func TestMergeCommentStaysFarBelowGitHubsLimitWhateverItStates(t *testing.T) {
	longest := 0
	for _, tc := range blockedReasons {
		m := blocked(tc.reason)
		m.Version, m.Proposal, m.Revision, m.Tier = 999999999, 999999999, 999999999, commandtext.CoLocated
		longest = max(longest, len(mergeText(t, m)))
	}
	corrected := commandtext.Merge{Kind: commandtext.MergeCorrected, Source: mergeSHA, Version: 999999999, Corrects: 999999998, Proposal: 999999999, Revision: 999999999, Tier: commandtext.CoLocated}
	longest = max(longest, len(mergeText(t, corrected)))
	if longest == 0 || longest > 4000 {
		t.Errorf("the longest comment has %d bytes, want fixed text well below GitHub's limit of 65,536", longest)
	}
}

func TestMergeCommentPanicsOnAStateThatCannotBeStored(t *testing.T) {
	noCommit := blocked("fork-unsupported")
	noCommit.Source = ""
	for name, m := range map[string]commandtext.Merge{
		"an unknown kind":                    {Kind: 0, Source: mergeSHA, Version: 1, Tier: commandtext.Isolated},
		"an unknown reason":                  blocked("something-else"),
		"a blocked merge without a reason":   blocked(""),
		"a promotion with a reason":          {Kind: commandtext.MergePromoted, Reason: "canonical-changed", Source: mergeSHA, Version: 2, Tier: commandtext.Isolated},
		"a promotion that corrects":          {Kind: commandtext.MergePromoted, Source: mergeSHA, Version: 2, Corrects: 1, Tier: commandtext.Isolated},
		"a correction that corrects nothing": {Kind: commandtext.MergeCorrected, Source: mergeSHA, Version: 2, Tier: commandtext.Isolated},
		"a source that is not a commit":      {Kind: commandtext.MergePromoted, Source: "SECRET", Version: 2, Tier: commandtext.Isolated},
		"a promotion without a commit":       {Kind: commandtext.MergePromoted, Version: 2, Tier: commandtext.Isolated},
		"no version":                         {Kind: commandtext.MergePromoted, Source: mergeSHA, Tier: commandtext.Isolated},
		"half a reference":                   {Kind: commandtext.MergePromoted, Source: mergeSHA, Version: 2, Proposal: 1, Tier: commandtext.Isolated},
		"an unknown tier":                    {Kind: commandtext.MergePromoted, Source: mergeSHA, Version: 2},
		"no commit for another reason":       noCommit,
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("MergeComment did not panic")
				}
			}()
			commandtext.MergeComment(m)
		})
	}
}
