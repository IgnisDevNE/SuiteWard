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

func TestMergeCommentForTheFirstCanonicalVersionNamesTheCommitAndSaysTestsWereNotRun(t *testing.T) {
	text := mergeText(t, commandtext.Merge{Kind: commandtext.MergePromoted, Source: mergeSHA, Version: 1, Proposal: 1, Revision: 2, Tier: commandtext.Isolated})
	for _, phrase := range []string{"promoted the canonical contract to V1", "first canonical version", mergeSHA, "P1-R2", "did not run the tests"} {
		if !strings.Contains(text, phrase) {
			t.Errorf("the comment lacks %q:\n%s", phrase, text)
		}
	}
	if strings.Contains(text, "tests passed") || strings.Contains(text, "correction") {
		t.Errorf("the comment claims tests or a correction:\n%s", text)
	}
	// A later promotion is not the first canonical version.
	later := mergeText(t, commandtext.Merge{Kind: commandtext.MergePromoted, Source: mergeSHA, Version: 2, Proposal: 1, Revision: 2, Tier: commandtext.Isolated})
	if strings.Contains(later, "first canonical version") {
		t.Errorf("a later promotion says it is the first:\n%s", later)
	}
}

func TestMergeCommentWithoutACanonicalSaysSoGivesTheReasonAndTheRecovery(t *testing.T) {
	for _, tc := range blockedReasons {
		if tc.reason == "integrity-failed" { // a canonical that fails its verification exists
			continue
		}
		m := blocked(tc.reason)
		m.Version = 0
		text := mergeText(t, m)
		for _, phrase := range []string{
			tc.phrase, "SuiteWard has no canonical contract yet", "default branch now holds the merged files",
			"proposes the default branch's contract for approval on an open pull request",
		} {
			if !strings.Contains(text, phrase) {
				t.Errorf("%s: the comment lacks %q:\n%s", tc.reason, phrase, text)
			}
		}
		for _, phrase := range []string{"stays at", "restores the protected files of", "V0", "no reset command"} {
			if strings.Contains(text, phrase) {
				t.Errorf("%s: the comment contains %q:\n%s", tc.reason, phrase, text)
			}
		}
		if tc.reason != "integration-unreadable" && !strings.Contains(text, mergeSHA) {
			t.Errorf("%s: the comment does not name the merge commit:\n%s", tc.reason, text)
		}
	}
	m := blocked("canonical-changed")
	m.Version, m.Proposal, m.Revision, m.Tier = 0, 0, 0, commandtext.CoLocated
	if text := mergeText(t, m); !strings.Contains(text, "assurance is reduced") || strings.Contains(text, "Proposal revision") {
		t.Errorf("the co-located comment lacks the reduced assurance or names a revision that does not exist:\n%s", text)
	}
}

func TestMergeCommentWithoutACanonicalStaysFarBelowGitHubsLimit(t *testing.T) {
	for _, tc := range blockedReasons {
		if tc.reason == "integrity-failed" {
			continue
		}
		m := blocked(tc.reason)
		m.Version, m.Proposal, m.Revision, m.Tier = 0, 999999999, 999999999, commandtext.CoLocated
		if text := mergeText(t, m); len(text) == 0 || len(text) > 4000 {
			t.Errorf("%s: %d bytes", tc.reason, len(text))
		}
	}
}

func TestMergeCommentPanicsOnAVersionThatCannotBeMissing(t *testing.T) {
	failedIntegrity := blocked("integrity-failed")
	failedIntegrity.Version = 0
	for name, m := range map[string]commandtext.Merge{
		"a promotion without a version":          {Kind: commandtext.MergePromoted, Source: mergeSHA, Tier: commandtext.Isolated},
		"a correction without a version":         {Kind: commandtext.MergeCorrected, Source: mergeSHA, Corrects: 1, Tier: commandtext.Isolated},
		"a failed integrity without a canonical": failedIntegrity,
	} {
		t.Run(name, func(t *testing.T) {
			mustPanic(t, func() { commandtext.MergeComment(m) })
		})
	}
}

// The merge texts of M1.6 and M1.7 are pinned: M1.7b adds the first canonical version and a merge without a
// canonical, and every other text stays as it was.
func TestMergeCommentKeepsItsExistingTexts(t *testing.T) {
	const (
		facts    = "- Merge commit: `" + mergeSHA + "`\n- Proposal revision: P2-R1\n- Installation tier: `isolated`\n\n"
		checked  = "SuiteWard checked the approval and the integrity of the protected files at that commit. It did not run the tests.\n"
		recovery = "open a new pull request that restores the protected files of V3, or one that carries the integrated files for a new exact approval. There is no reset command.\n"
	)
	unreadable := blocked("integration-unreadable")
	unreadable.Source = ""
	for name, tc := range map[string]struct {
		merge commandtext.Merge
		want  string
	}{
		"promoted": {
			commandtext.Merge{Kind: commandtext.MergePromoted, Source: mergeSHA, Version: 2, Proposal: 2, Revision: 1, Tier: commandtext.Isolated},
			"**SuiteWard promoted the canonical contract to V2**\n\n" +
				"This merge integrated the contract change the owner approved, and SuiteWard promoted V2 from the merge commit.\n\n" + facts + checked,
		},
		"corrected": {
			commandtext.Merge{Kind: commandtext.MergeCorrected, Source: mergeSHA, Version: 3, Corrects: 2, Proposal: 2, Revision: 1, Tier: commandtext.Isolated},
			"**SuiteWard promoted the canonical contract to V3 as a correction**\n\n" +
				"This merge restored the protected files of an earlier version, and SuiteWard promoted V3 as a correction of V2. The history is kept.\n\n" + facts + checked,
		},
		"blocked": {
			blocked("canonical-changed"),
			"**SuiteWard did not promote this merge: the canonical contract stays at V3**\n\n" +
				"The canonical contract changed after the owner approved this revision, so the approval no longer applies to what was merged.\n\n" + facts +
				"To recover, " + recovery,
		},
		"blocked with its own next action": {
			blocked("integrity-failed"),
			"**SuiteWard did not promote this merge: the canonical contract stays at V3**\n\n" +
				"SuiteWard's stored canonical contract failed its integrity verification, so SuiteWard refuses to promote anything on top of it.\n\n" + facts +
				"Next: the operator must restore the artifact store from a backup or re-bootstrap the repository. A pull request cannot fix this. After that, to recover, " + recovery,
		},
		"blocked without a commit": {
			unreadable,
			"**SuiteWard did not promote this merge: the canonical contract stays at V3**\n\n" +
				"SuiteWard could not read the merge commit from GitHub within 30 minutes of seeing the pull request leave the open list, or GitHub reported no merge commit for it.\n\n" +
				"- Proposal revision: P2-R1\n- Installation tier: `isolated`\n\n" + "To recover, " + recovery,
		},
	} {
		if got := mergeText(t, tc.merge); got != tc.want {
			t.Errorf("%s: the text changed:\n%s", name, got)
		}
	}
}
