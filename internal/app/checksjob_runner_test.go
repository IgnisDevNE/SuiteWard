package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// A runner file outside the scope blocks the check (docs/contracts/runtime.md, "Check reason runner-uncovered"; D-SCOPE-DISCOVERY,
// "Uncovered runner files block"): after V1 a pull request that adds or changes one is never success until the owner protects or ignores it.

const cjJest = "jest.config.js"

func added(paths ...string) []github.PullFile {
	files := make([]github.PullFile, len(paths))
	for i, path := range paths {
		files[i] = github.PullFile{Path: path, Status: "added"}
	}
	return files
}

// runnerPull opens pull 5 at head, whose tree is headFiles over the base commit cjBase (the canonical's tree), changes diff, and checks once.
func (w *cjJobWorld) runnerPull(head string, headFiles map[string]string, diff ...github.PullFile) {
	w.t.Helper()
	w.reader.files = map[int][]github.PullFile{5: diff}
	w.scopePull(head, headFiles, bootFiles("v1"))
}

// declarations returns the canonical's declaration with the runner patterns of paths added, and with paths excluded.
func runnerDeclarations(t *testing.T, base string, paths ...string) (protect, ignore string) {
	t.Helper()
	runner := map[string][]string{cjJest: {"jest.config.*"}, "conftest.py": {".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini", "pytest.toml"}}
	var patterns []string
	for _, p := range paths {
		patterns = append(patterns, runner[p]...)
	}
	slices.Sort(patterns)
	p, err := scope.AddPatterns([]byte(base), nil, slices.Compact(patterns))
	if err != nil {
		t.Fatal(err)
	}
	i, err := scope.AddExcludes([]byte(base), anchored(paths))
	if err != nil {
		t.Fatal(err)
	}
	return string(p), string(i)
}

func runnerDigest(head string, paths ...string) string {
	return wantReviewDigest(head, paths...)
}

func (w *cjJobWorld) runnerPayload(index int) map[string]any {
	w.t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(w.scopeMessages("runner-uncovered:")[index].Payload, &payload); err != nil {
		w.t.Fatal(err)
	}
	return payload
}

func (w *cjJobWorld) wantRunnerKeys(keys ...string) {
	w.t.Helper()
	if got := w.scopeKeys("runner-uncovered:"); !slices.Equal(got, keys) {
		w.t.Fatalf("runner-uncovered messages %v, want %v\nlogs: %s", got, keys, w.logs)
	}
}

func TestChecksBlockANewRunnerFileOutsideTheScopeAndWriteOneCommentWithBothDeclarations(t *testing.T) {
	w := newCJWorld(t, true)

	w.runnerPull(cjHead2, withFiles(bootFiles("v1"), cjJest), added(cjJest)...)

	record := w.want(5, checks.InProgress, "runner-uncovered", 1)
	if record.Change != "unchanged" {
		t.Fatalf("record %+v, want the classification the head had before the block", record)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.ledger.messages[len(w.ledger.messages)-1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["state"] != "in_progress" || payload["reason"] != "runner-uncovered" || payload["headSha"] != cjHead2 {
		t.Fatalf("check payload %v", payload)
	}
	w.wantRunnerKeys("runner-uncovered:1001:5:" + runnerDigest(bootFiles("v1")[".suiteward.yml"], cjJest))
	protect, ignore := runnerDeclarations(t, cjDecl, cjJest)
	want := map[string]any{"repositoryId": float64(bootRepoID), "repo": bootRepo, "pull": float64(5), "head": cjHead2, "branch": "feature",
		"paths": []any{cjJest}, "pathsTotal": float64(1), "protect": protect, "ignore": ignore}
	if got := w.runnerPayload(0); !reflect.DeepEqual(got, want) {
		t.Fatalf("payload %v, want %v", got, want)
	}
	if !strings.Contains(protect, "runner:\n  - jest.config.*") || !strings.Contains(ignore, "exclude:\n  - /jest.config.js") {
		t.Fatalf("the declarations are not what the test means: protect %q, ignore %q", protect, ignore)
	}
	if got := w.scopeKeys("scope-review:"); len(got) != 0 {
		t.Fatalf("scope reviews %v, want none: the diff shows no .suiteward.yml", got)
	}
}

func TestChecksWriteNoSecondRunnerCommentForTheSameFilesAndTheSameDeclaration(t *testing.T) {
	w := newCJWorld(t, true)
	w.runnerPull(cjHead2, withFiles(bootFiles("v1"), cjJest), added(cjJest)...)
	files := w.reader.fileReads
	trees := w.reader.reads[cjHead2]

	w.mustCheck() // a tick that changes nothing
	if w.reader.fileReads != files || w.reader.reads[cjHead2] != trees {
		t.Fatalf("reads went from %d file lists and %d head trees to %d and %d on a tick that changes nothing", files, trees, w.reader.fileReads, w.reader.reads[cjHead2])
	}

	// A push that keeps the file and the head's .suiteward.yml: a new head and generation, the same decision.
	w.tree(cjHead3, withFiles(bootFiles("v1"), cjJest, "src/more.go"))
	w.sync(map[int]string{4: bootHead, 5: cjHead3})
	w.mustCheck()

	w.want(5, checks.InProgress, "runner-uncovered", 2)
	if got := w.scopeKeys("runner-uncovered:"); len(got) != 1 {
		t.Fatalf("runner-uncovered messages %v, want only the first", got)
	}
}

func TestChecksWriteANewRunnerCommentWhenTheHeadsDeclarationChanges(t *testing.T) {
	w := newCJWorld(t, true)
	w.runnerPull(cjHead2, withFiles(bootFiles("v1"), cjJest), added(cjJest)...)

	// The same file under another declaration (an unrelated edit, approved): the review is anchored to the new bytes, so a new comment says so.
	w.tree(cjHead3, reviewFiles(reviewDecl, cjJest))
	w.sync(map[int]string{4: bootHead, 5: cjHead3})
	w.mustCheck()
	w.want(5, checks.InProgress, "approval-missing", 2)
	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.mustCheck()

	w.want(5, checks.InProgress, "runner-uncovered", 3)
	w.wantRunnerKeys("runner-uncovered:1001:5:"+runnerDigest(bootFiles("v1")[".suiteward.yml"], cjJest), "runner-uncovered:1001:5:"+runnerDigest(reviewDecl, cjJest))
}

func TestChecksKeepApprovalMissingFirstAndBlockOnlyAfterTheApprovalOfTheSameHead(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: added(cjJest)}
	w.scopePull(cjHead2, withFiles(bootFiles("v2"), cjJest), bootFiles("v1")) // a protected change, and a runner file

	w.want(5, checks.InProgress, "approval-missing", 1)
	w.wantRunnerKeys()
	files := w.reader.fileReads

	if outcome := w.consent(contract.ApproveConsent, 201, 2, 1, 5); outcome != contract.ConsentApproved {
		t.Fatalf("approval outcome %v", outcome)
	}
	w.mustCheck()

	record := w.want(5, checks.InProgress, "runner-uncovered", 2) // never success while the owner has not decided about the file
	if record.Reference != "P2-R1" || record.Change != "protected-change" {
		t.Fatalf("record %+v, want the approved revision kept", record)
	}
	if w.reader.fileReads != files+1 {
		t.Fatalf("the files of the pull request were read %d times after the approval, want one more than %d", w.reader.fileReads, files)
	}
	if len(w.scopeKeys("runner-uncovered:")) != 1 {
		t.Fatalf("runner-uncovered messages %v, want one", w.scopeKeys("runner-uncovered:"))
	}
}

// After V1 the head's own declaration decides what is covered, but only a head with nothing to approve (unchanged) or whose declaration the owner
// approved (ready) can be success, so an exclude the owner has not approved never lets a runner file through.
func TestChecksNeverLetTheHeadsOwnDeclarationApproveItsRunnerFile(t *testing.T) {
	protecting := withDeclaration(withFiles(bootFiles("v1"), cjJest), "version: 1\ninclude: [/tests/**]\nrunner: [jest.config.*]\n")
	ignoring := withDeclaration(withFiles(bootFiles("v1"), cjJest), "version: 1\ninclude: [/tests/**]\nexclude: [/jest.config.js]\n")
	for name, files := range map[string]map[string]string{"protecting it": protecting, "ignoring it": ignoring} {
		t.Run(name, func(t *testing.T) {
			w := newCJWorld(t, true)
			w.reader.files = map[int][]github.PullFile{5: append(added(cjJest), github.PullFile{Path: ".suiteward.yml", Status: "modified"})}
			w.scopePull(cjHead2, files, bootFiles("v1"))

			w.want(5, checks.InProgress, "approval-missing", 1) // the new declaration needs the owner's approval first

			w.consent(contract.ApproveConsent, 201, 2, 1, 5)
			w.mustCheck()

			w.want(5, checks.Success, "ready", 2) // the approved declaration covers or excludes the file
			w.wantRunnerKeys()
		})
	}
}

func TestChecksDoNotBlockARunnerFileTheBaseAlreadyHoldsAndThePullRequestLeavesAlone(t *testing.T) {
	w := newCJWorld(t, true)

	w.reader.files = map[int][]github.PullFile{5: added("README.md")}
	w.scopePull(cjHead2, withFiles(bootFiles("v1"), cjJest, "pkg/conftest.py"), withFiles(bootFiles("v1"), cjJest, "pkg/conftest.py")) // the base holds both files already

	w.want(5, checks.Success, "unchanged", 1)
	w.wantRunnerKeys()
	if w.reader.fileReads != 1 { // one read, for the two candidates: both are on the head and neither is in the diff
		t.Fatalf("the files of the pull request were read %d times, want once", w.reader.fileReads)
	}
}

func TestChecksReadNoFilesOfThePullRequestWhenNoRunnerFileIsUncovered(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		reads int // the reads of the diff the case allows
	}{
		"no runner file":   {bootFiles("v1"), 0},
		"a vendored one":   {withFiles(bootFiles("v1"), "node_modules/x/jest.config.js"), 0},
		"a test file only": {withFiles(bootFiles("v1"), "test_x.py", cjUtilTst), 1}, // the notice of the test files reads the diff for its own review
	} {
		t.Run(name, func(t *testing.T) {
			w := newCJWorld(t, true)

			w.runnerPull(cjHead2, tc.files, added(cjJest, "node_modules/x/jest.config.js", "test_x.py", cjUtilTst, "tests/conftest.py")...)

			w.want(5, checks.Success, "unchanged", 1)
			w.wantRunnerKeys()
			if w.reader.fileReads != tc.reads {
				t.Fatalf("the files of the pull request were read %d times, want %d", w.reader.fileReads, tc.reads)
			}
		})
	}
}

// Uncovered test files stay a notice: they add no bypass, and their detection is heuristic.
func TestChecksKeepAnUncoveredTestFileANoticeOnly(t *testing.T) {
	w := newCJWorld(t, true)

	w.runnerPull(cjHead2, withFiles(bootFiles("v1"), cjUtilTst, "test_x.py"), added(cjUtilTst, "test_x.py")...)

	w.want(5, checks.Success, "unchanged", 1)
	w.wantRunnerKeys()
	if len(w.scopeKeys("scope-notice:")) != 1 {
		t.Fatalf("scope messages %v, want the notice", w.scopeKeys("scope-"))
	}
}

func TestChecksBlockEveryKindOfChangeThatTouchesTheFile(t *testing.T) {
	for _, status := range []string{"added", "modified", "renamed", "copied", "changed"} {
		t.Run(status, func(t *testing.T) {
			w := newCJWorld(t, true)

			w.runnerPull(cjHead2, withFiles(bootFiles("v1"), cjJest), github.PullFile{Path: cjJest, Status: status})

			w.want(5, checks.InProgress, "runner-uncovered", 1)
		})
	}
	t.Run("a pull request with too many files to list", func(t *testing.T) {
		w := newCJWorld(t, true)
		diff := make([]github.PullFile, 3000)
		for i := range diff {
			diff[i] = github.PullFile{Path: fmt.Sprintf("gen/f%04d.txt", i), Status: "added"} // GitHub lists at most 3000: the runner file may be past them
		}

		w.runnerPull(cjHead2, withFiles(bootFiles("v1"), cjJest), diff...)

		w.want(5, checks.InProgress, "runner-uncovered", 1)
	})
}

func TestChecksKeepEveryOtherReasonOverTheRunnerBlock(t *testing.T) {
	runner := withFiles(bootFiles("v1"), cjJest)
	t.Run("invalid-source", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.runnerPull(cjHead2, withDeclaration(runner, "version: 2\n"), added(cjJest)...)
		w.want(5, checks.Failure, "invalid-source", 1)
		w.wantRunnerKeys()
	})
	t.Run("integrity-failed", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.verdict("V1", integrity.Failed)
		w.runnerPull(cjHead2, runner, added(cjJest)...)
		w.want(5, checks.Failure, "integrity-failed", 1)
		w.wantRunnerKeys()
	})
	t.Run("a fork", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.headRepo = map[int]string{5: "evil/widgets"}
		w.runnerPull(cjHead2, runner, added(cjJest)...)
		w.want(5, checks.InProgress, "fork-unsupported", 1)
		w.wantRunnerKeys()
	})
	t.Run("an owner command that was rejected", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.runnerPull(cjHead2, runner, added(cjJest)...)
		w.want(5, checks.InProgress, "runner-uncovered", 1)
		w.entry(5, 301, bootOwnerID, "invalid-code", "")
		w.mustCheck()
		w.want(5, checks.Failure, "owner-command-rejected", 2)
	})
	t.Run("before the first contract", func(t *testing.T) {
		w := newCJWorld(t, false)
		w.reader.files = map[int][]github.PullFile{5: added(cjJest)}
		w.tree(cjHead2, runner)
		w.sync(map[int]string{4: bootHead, 5: cjHead2})
		w.mustCheck()
		if record, found := w.record(5); found && record.Reason == "runner-uncovered" {
			t.Fatalf("the check is %s/%s before V1", record.State, record.Reason)
		}
		w.wantRunnerKeys()
	})
}

// A read that fails must never leave a success: the head's runner files are unknown, so the check says it is pending.
func TestChecksNeverKeepASuccessWhenTheFilesOfThePullRequestCannotBeRead(t *testing.T) {
	w := newCJWorld(t, true)
	w.runnerPull(cjHead2, withFiles(bootFiles("v1"), "pkg/conftest.py"), added("README.md")...) // a runner file the pull request does not touch
	w.want(5, checks.Success, "unchanged", 1)

	w.check.runners = nil // a restart forgets what was decided
	w.reader.filesErr = &rest.StatusError{Status: 500, Body: bootSecret}
	w.mustCheck()

	w.want(5, checks.InProgress, "assessment-pending", 2)
	if strings.Contains(w.logs.String(), bootSecret) {
		t.Fatalf("logs hold GitHub's text: %s", w.logs)
	}
	w.reader.filesErr = nil
	w.mustCheck()
	w.want(5, checks.Success, "unchanged", 3)
}

func TestChecksNeverKeepABlockWhenTheFilesCannotBeReadAfterARestart(t *testing.T) {
	w := newCJWorld(t, true)
	w.runnerPull(cjHead2, withFiles(bootFiles("v1"), cjJest), added(cjJest)...)
	w.want(5, checks.InProgress, "runner-uncovered", 1)

	w.check.runners = nil
	w.reader.filesErr = &rest.RateLimitedError{Status: 403}
	w.mustCheck()

	w.want(5, checks.InProgress, "assessment-pending", 2)
	w.reader.filesErr = nil
	w.mustCheck()
	w.want(5, checks.InProgress, "runner-uncovered", 3)
	w.wantRunnerKeys("runner-uncovered:1001:5:" + runnerDigest(bootFiles("v1")[".suiteward.yml"], cjJest)) // the comment of the first decision stands
}

func TestChecksDecideAgainAfterARestartWithoutAnotherComment(t *testing.T) {
	w := newCJWorld(t, true)
	w.runnerPull(cjHead2, withFiles(bootFiles("v1"), cjJest), added(cjJest)...)

	w.check.runners, w.check.announced, w.check.scoped = nil, nil, nil
	w.mustCheck()

	w.want(5, checks.InProgress, "runner-uncovered", 1)
	if len(w.scopeKeys("runner-uncovered:")) != 1 {
		t.Fatalf("runner-uncovered messages %v, want one", w.scopeKeys("runner-uncovered:"))
	}
}

func TestChecksResolveARunnerBlockWhenTheNextHeadProtectsOrIgnoresTheFile(t *testing.T) {
	for name, tc := range map[string]struct{ declaration, change string }{
		"protect": {"version: 1\ninclude: [/tests/**]\nrunner: [jest.config.*]\n", "protected-change"},
		"ignore":  {"version: 1\ninclude: [/tests/**]\nexclude: [/jest.config.js]\n", "scope-reduction"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCJWorld(t, true)
			w.runnerPull(cjHead2, withFiles(bootFiles("v1"), cjJest), added(cjJest)...)
			w.want(5, checks.InProgress, "runner-uncovered", 1)

			w.reader.files = map[int][]github.PullFile{5: append(added(cjJest), github.PullFile{Path: ".suiteward.yml", Status: "modified"})}
			w.tree(cjHead3, withDeclaration(withFiles(bootFiles("v1"), cjJest), tc.declaration))
			w.sync(map[int]string{4: bootHead, 5: cjHead3})
			w.mustCheck()
			if record := w.want(5, checks.InProgress, "approval-missing", 2); record.Change != tc.change { // the owner's decision is a change like any other
				t.Fatalf("the declaration is classified %q, want %q", record.Change, tc.change)
			}

			w.consent(contract.ApproveConsent, 201, 2, 1, 5)
			w.mustCheck()
			w.want(5, checks.Success, "ready", 3)
		})
	}
}

// The review suggestions of the two choices (one-click form). The head's file is the only place that starts from the PR head's lines.

func decodeSuggestions(t *testing.T, message []byte) []scopeSuggestion {
	t.Helper()
	var payload scopeReviewPayload
	if err := json.Unmarshal(message, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Suggestions
}

var suggestionContent = regexp.MustCompile("(?s)```suggestion\n(.*)\n```")

// apply replaces the anchor line of s in file with the content of its suggestion block, as GitHub's Commit suggestion does.
func apply(t *testing.T, file string, s scopeSuggestion) string {
	t.Helper()
	block := suggestionContent.FindStringSubmatch(s.body())
	if block == nil {
		t.Fatalf("no suggestion block in %q", s.body())
	}
	lines := strings.Split(file, "\n")
	if got := strings.TrimSuffix(lines[s.Line-1], "\r"); got != s.Anchor {
		t.Fatalf("line %d is %q, the anchor is %q", s.Line, got, s.Anchor)
	}
	ending := ""
	if strings.HasSuffix(lines[s.Line-1], "\r") {
		ending = "\r"
	}
	lines[s.Line-1] = strings.ReplaceAll(block[1], "\n", ending+"\n") + ending
	return strings.Join(lines, "\n")
}

// runnerReviewOf returns the suggestions of the review message with key, which must exist: the scope notice may write a review of its own.
func (w *cjJobWorld) runnerReviewOf(key string) []scopeSuggestion {
	w.t.Helper()
	for _, m := range w.scopeMessages("scope-review:") {
		if m.Key == key {
			return decodeSuggestions(w.t, m.Payload)
		}
	}
	w.t.Fatalf("review messages %v, want %s\nlogs: %s", w.scopeKeys("scope-review:"), key, w.logs)
	return nil
}

// runnerReviewKey is the key of the review of the choices for paths under the head's file.
func runnerReviewKey(head string, paths ...string) string {
	sum := sha256.Sum256([]byte("runner-uncovered:" + scopeDigest(paths...) + "\n" + head))
	return "scope-review:1001:5:" + hex.EncodeToString(sum[:])
}

func (w *cjJobWorld) wantRunnerReview(head string, paths []string, suggestions ...scopeSuggestion) []scopeSuggestion {
	w.t.Helper()
	got := w.runnerReviewOf(runnerReviewKey(head, paths...))
	if !reflect.DeepEqual(got, suggestions) {
		w.t.Fatalf("suggestions %+v, want %+v", got, suggestions)
	}
	return got
}

// approvedRunnerPull opens pull 5 with a head whose declaration is edited (so the owner approves it first) and a runner file outside it.
func (w *cjJobWorld) approvedRunnerPull(declaration, hunk string) {
	w.t.Helper()
	w.reader.files = map[int][]github.PullFile{5: append(added(cjJest), github.PullFile{Path: ".suiteward.yml", Status: "modified", Patch: hunk + "\n context\n-old\n+new"})}
	w.scopePull(cjHead2, reviewFiles(declaration, cjJest), bootFiles("v1"))
	w.want(5, checks.InProgress, "approval-missing", 1)
	w.wantRunnerKeys()
	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.mustCheck()
	w.want(5, checks.InProgress, "runner-uncovered", 2)
}

func TestChecksSuggestBothChoicesThatCreateTheirListAfterTheLastItemInTheDiff(t *testing.T) {
	w := newCJWorld(t, true)

	w.approvedRunnerPull(reviewDecl, "@@ -1,3 +1,3 @@")

	got := w.wantRunnerReview(reviewDecl, []string{cjJest},
		scopeSuggestion{List: "protect", Line: 3, Anchor: "  - /tests/**", Patterns: []string{"jest.config.*"}, NewList: "runner"},
		scopeSuggestion{List: "ignore", Line: 3, Anchor: "  - /tests/**", Patterns: []string{"/" + cjJest}, NewList: "exclude"})
	protect, ignore := runnerDeclarations(t, cjDecl, cjJest)
	for i, want := range []string{protect, ignore} {
		applied, err := scope.ParseDeclaration([]byte(apply(t, reviewDecl, got[i])))
		if err != nil {
			t.Fatalf("%s applied does not parse: %v", got[i].List, err)
		}
		wantDeclaration, err := scope.ParseDeclaration([]byte(want))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(patternsOf(applied), patternsOf(wantDeclaration)) {
			t.Errorf("%s applied gives %v, want %v", got[i].List, patternsOf(applied), patternsOf(wantDeclaration))
		}
	}
}

func patternsOf(d scope.Declaration) [3][]string {
	var lists [3][]string
	for i, ps := range [][]scope.Pattern{d.Include(), d.Exclude(), d.Runner()} {
		for _, p := range ps {
			lists[i] = append(lists[i], p.String())
		}
	}
	return lists
}

func TestChecksSuggestBothChoicesOnTheItemsOfTheListsThatExist(t *testing.T) {
	const declaration = "version: 1\ninclude:\n  - /tests/**\nrunner:\n  - pytest.toml\nexclude:\n  - gen/\n"
	w := newCJWorld(t, true)

	w.approvedRunnerPull(declaration, "@@ -1,7 +1,7 @@")

	got := w.wantRunnerReview(declaration, []string{cjJest},
		scopeSuggestion{List: "protect", Line: 5, Anchor: "  - pytest.toml", Patterns: []string{"jest.config.*"}},
		scopeSuggestion{List: "ignore", Line: 7, Anchor: "  - gen/", Patterns: []string{"/" + cjJest}})
	if applied := apply(t, declaration, got[0]); applied != "version: 1\ninclude:\n  - /tests/**\nrunner:\n  - pytest.toml\n  - \"jest.config.*\"\nexclude:\n  - gen/\n" {
		t.Errorf("protect applied gives %q", applied)
	}
	if applied := apply(t, declaration, got[1]); applied != "version: 1\ninclude:\n  - /tests/**\nrunner:\n  - pytest.toml\nexclude:\n  - gen/\n  - \"/jest.config.js\"\n" {
		t.Errorf("ignore applied gives %q", applied)
	}
	for _, s := range got {
		if _, err := scope.ParseDeclaration([]byte(apply(t, declaration, s))); err != nil {
			t.Errorf("%s applied does not parse: %v", s.List, err)
		}
	}
}

// Each anchor sits on the last item of a list, so an item that follows it never ends up after the created key; and the file's own style survives.
func TestChecksAnchorACreatedListOnlyOnTheLastItemOfALastLine(t *testing.T) {
	for name, tc := range map[string]struct {
		declaration string
		hunk        string
		line        int
		anchor      string
		keyIndent   string
	}{
		"an indentless list":               {"version: 1\ninclude:\n- /tests/**\n", "@@ -1,3 +1,3 @@", 3, "- /tests/**", ""},
		"a list indented by four":          {"version: 1\ninclude:\n    - /tests/**\n", "@@ -1,3 +1,3 @@", 3, "    - /tests/**", ""},
		"the last item, not an earlier":    {"version: 1\ninclude:\n  - /tests/**\n  - /e2e/**\n", "@@ -1,4 +1,4 @@", 4, "  - /e2e/**", ""},
		"a Windows file":                   {"version: 1\r\ninclude:\r\n  - /tests/**\r\n", "@@ -1,3 +1,3 @@", 3, "  - /tests/**", ""},
		"a list in the middle of the file": {"version: 1\ninclude:\n  - /tests/**\nexclude:\n  - gen/\n", "@@ -3,1 +3,1 @@", 0, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCJWorld(t, true)

			w.approvedRunnerPull(tc.declaration, tc.hunk)

			got := w.runnerReviewOf(runnerReviewKey(tc.declaration, cjJest))
			if tc.line == 0 { // an exclude list exists but its item is outside the diff: only protect can be created, after the include item
				if len(got) != 1 || got[0].List != "protect" || got[0].NewList != "runner" || got[0].Line != 3 {
					t.Fatalf("suggestions %+v, want only protect, created after the include item on line 3", got)
				}
				return
			}
			if len(got) != 2 {
				t.Fatalf("suggestions %+v, want protect and ignore", got)
			}
			for _, s := range got {
				if s.Line != tc.line || s.Anchor != tc.anchor || s.NewList == "" || s.KeyIndent != tc.keyIndent {
					t.Errorf("suggestion %+v, want line %d, anchor %q and a created list at indent %q", s, tc.line, tc.anchor, tc.keyIndent)
				}
				if _, err := scope.ParseDeclaration([]byte(apply(t, tc.declaration, s))); err != nil {
					t.Errorf("%s applied does not parse: %v", s.List, err)
				}
			}
		})
	}
}

func TestChecksWriteTheRunnerCommentButNoReviewWhenTheDiffGivesNoLineToSuggestOn(t *testing.T) {
	for name, tc := range map[string]struct {
		declaration string
		hunk        string
	}{
		"no list line inside a hunk": {reviewDecl, "@@ -1,2 +1,2 @@"},
		"lists in flow style":        {"version: 1\ninclude: [/tests/**, /e2e/**]\n", "@@ -1,2 +1,2 @@"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCJWorld(t, true)

			w.approvedRunnerPull(tc.declaration, tc.hunk)

			if got := w.scopeKeys("scope-review:"); len(got) != 0 {
				t.Fatalf("reviews %v, want none", got)
			}
			if len(w.scopeKeys("runner-uncovered:")) != 1 {
				t.Fatalf("runner-uncovered messages %v, want the comment with both declarations", w.scopeKeys("runner-uncovered:"))
			}
		})
	}
}

// runnerNoticeDecl has a runner list, so the scope notice for jest.config.js has a review of its own on line 5.
const runnerNoticeDecl = "version: 1\ninclude:\n  - /tests/**\nrunner:\n  - pytest.toml\n"

// The review of the choices shares the scope-review message kind with the notice's, not its key: a collision would drop the comment written with it.
func TestChecksKeepTheRunnerReviewApartFromTheScopeNoticesReview(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: append(added(cjJest), github.PullFile{Path: ".suiteward.yml", Status: "modified", Patch: "@@ -1,5 +1,5 @@\n context\n-old\n+new"})}
	w.scopePull(cjHead2, reviewFiles(runnerNoticeDecl, cjJest), bootFiles("v1")) // the notice's set is [jest.config.js] too, and its review has a runner list to suggest on
	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.mustCheck()

	w.want(5, checks.InProgress, "runner-uncovered", 2)
	keys := w.scopeKeys("scope-review:")
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("review keys %v, want the notice's and the runner's, which differ", keys)
	}
	if len(w.scopeKeys("runner-uncovered:")) != 1 || len(w.scopeKeys("scope-notice:")) != 1 {
		t.Fatalf("messages %v, want one notice and one runner-uncovered comment", w.scopeKeys(""))
	}
}

func TestChecksLeaveTheIgnoreChoiceOutWhenAPathCannotBeNamedInExclude(t *testing.T) {
	// A bracket is a character class in a pattern, so the path would never match itself; a star or a question mark makes the pattern match the path but
	// also other files, and an ignore choice must name exactly one file.
	for _, odd := range []string{"app/[id]/jest.config.js", "docs/a*/conftest.py", "docs/a?/conftest.py", "!app/conftest.py"} {
		t.Run(odd, func(t *testing.T) {
			w := newCJWorld(t, true)

			w.reader.files = map[int][]github.PullFile{5: added(odd)}
			w.scopePull(cjHead2, withFiles(bootFiles("v1"), odd), bootFiles("v1"))

			w.want(5, checks.InProgress, "runner-uncovered", 1)
			payload := w.runnerPayload(0)
			if _, hasIgnore := payload["ignore"]; hasIgnore || payload["protect"] == "" {
				t.Fatalf("payload %v, want a protect declaration and no ignore declaration", payload)
			}
			w.mustCheck() // a decided head is not decided again, and never fails the job
			w.mustCheck()
			if keys := w.scopeKeys("runner-uncovered:"); len(keys) != 1 {
				t.Fatalf("runner comments %v after two more runs, want the one of the first", keys)
			}
		})
	}
}

func TestChecksLogWhyTheRunnerCommentIsSkippedAndKeepTheBlock(t *testing.T) {
	t.Run("a declaration over the limit", func(t *testing.T) {
		w := newCJWorldOf(t, withDeclaration(bootFiles("v1"), cjDecl+"# "+strings.Repeat("x", commandtext.MaxDeclaration)+"\n"))

		w.runnerPull(cjHead2, withDeclaration(withFiles(bootFiles("v1"), cjJest), cjDecl+"# "+strings.Repeat("x", commandtext.MaxDeclaration)+"\n"), added(cjJest)...)

		w.want(5, checks.InProgress, "runner-uncovered", 1)
		w.wantRunnerKeys()
		w.logged("runner-uncovered comment skipped")
	})
}

// anchored is the exclude pattern that names exactly one path: a name without a slash would match at every depth, so it gets a leading slash.
func anchored(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = p
		if !strings.Contains(p, "/") {
			out[i] = "/" + p
		}
	}
	return out
}

// Ignoring a root file excludes that file only: approved, the declaration neither lifts the protection of a runner file that the include list
// covers nor lets a later nested one of the same name through.
func TestChecksIgnoreOnlyTheFileThatWasAskedAbout(t *testing.T) {
	w := newCJWorld(t, true)

	w.runnerPull(cjHead2, withFiles(bootFiles("v1"), "conftest.py"), added("conftest.py")...)

	w.want(5, checks.InProgress, "runner-uncovered", 1)
	var payload struct {
		Ignore string `json:"ignore"`
	}
	if err := json.Unmarshal(w.scopeMessages("runner-uncovered:")[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	declaration, err := scope.ParseDeclaration([]byte(payload.Ignore))
	if err != nil {
		t.Fatalf("ignore declaration %q: %v", payload.Ignore, err)
	}
	entries := []inventory.Entry{{Path: "conftest.py", Kind: inventory.File}, {Path: "tests/conftest.py", Kind: inventory.File}, {Path: "src/x/conftest.py", Kind: inventory.File}}
	uncovered, err := inventory.UncoveredRunners(entries, scope.NewScope(declaration))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(uncovered, []string{"src/x/conftest.py"}) {
		t.Fatalf("under the ignore declaration %q the uncovered runner files are %v, want only the nested one outside the include list", payload.Ignore, uncovered)
	}
	protected, err := scope.NewScope(declaration).Covers("tests/conftest.py")
	if err != nil || !protected {
		t.Fatalf("tests/conftest.py is covered = %v (%v) under the ignore declaration, want it still protected", protected, err)
	}
}

// A link named like a runner file is one: a link the scope covers is invalid already, so this is the uncovered case.
func TestChecksBlockALinkNamedLikeARunnerFile(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: added("conftest.py")}
	w.bases = map[int]string{5: cjBase}
	w.tree(cjBase, bootFiles("v1"))
	w.reader.repos[bootRepo].trees[cjHead2] = linkedTree{&bootTree{files: withFiles(bootFiles("v1"), "evil.py")}, map[string]string{"conftest.py": "evil.py"}}
	w.sync(map[int]string{4: bootHead, 5: cjHead2})

	w.mustCheck()

	w.want(5, checks.InProgress, "runner-uncovered", 1)
	if payload := w.runnerPayload(0); !reflect.DeepEqual(payload["paths"], []any{"conftest.py"}) {
		t.Fatalf("payload %v, want the link as the runner file", payload)
	}
}

// A choice that does not fit a suggestion is dropped with a reason that quotes nothing, and the other choice is kept.
func TestChecksLogTheChoiceThatCannotBeSuggested(t *testing.T) {
	w := newCJWorld(t, true)
	const quoted = `q"x/conftest.py` // a quote cannot be written in a double-quoted suggestion item

	w.reader.files = map[int][]github.PullFile{5: append(added(quoted), github.PullFile{Path: ".suiteward.yml", Status: "modified", Patch: "@@ -1,3 +1,3 @@\n context\n-old\n+new"})}
	w.scopePull(cjHead2, reviewFiles(reviewDecl, quoted), bootFiles("v1"))
	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.mustCheck()

	w.want(5, checks.InProgress, "runner-uncovered", 2)
	got := w.runnerReviewOf(runnerReviewKey(reviewDecl, quoted))
	if len(got) != 1 || got[0].List != "protect" {
		t.Fatalf("suggestions %+v, want only protect", got)
	}
	line := w.logged("runner-uncovered choice skipped")
	if line["choice"] != "ignore" || strings.Contains(w.logs.String(), quoted) {
		t.Fatalf("log line %v, want the choice named and the path not quoted", line)
	}
}
