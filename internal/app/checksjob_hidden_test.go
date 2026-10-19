package app

import (
	"encoding/json"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
)

// A declaration shown for copying could carry invisible characters from a comment of the owner's .suiteward.yml (docs/contracts/runtime.md, decision 7 of
// M1.10): the comment that quotes it is refused by commandtext, so a declaration that holds one is left out where the comment can do without it.

// hiddenComment is a YAML comment with a zero-width space, a character that is not graphic.
const hiddenComment = "# a\u200bb\n"

func TestChecksShowThePatternAloneWhenTheHeadsDeclarationHoldsAHiddenCharacter(t *testing.T) {
	w := newCJWorld(t, true)
	hidden := reachable + hiddenComment
	files := bootFiles("v1")
	files[".suiteward.yml"] = hidden
	files["app/main.py"] = "print('x')\n"
	w.reader.repos[bootRepo].trees[cjHead2] = linkedTree{&bootTree{files: files}, map[string]string{"spec_link": "app"}}
	w.sync(map[int]string{4: bootHead, 5: cjHead2})

	w.mustCheck()

	w.want(5, checks.Failure, "invalid-source", 1)
	w.wantInvalidSource(invalidKeyOf("link", "spec_link", acceptingDeclaration(t, hidden, "spec_link"))) // the key digests the declaration as it did
	payload := w.invalidSourcePayload()
	if declaration, found := payload["declaration"]; found {
		t.Fatalf("declaration %q, want none: the comment shows the pattern alone", declaration)
	}
	var stored invalidSourcePayload
	if err := json.Unmarshal(w.scopeMessages("invalid-source:")[0].Payload, &stored); err != nil || stored.validate() != nil {
		t.Fatalf("the stored payload does not validate at publish: %v, %v", err, stored.validate())
	}
}

// The three comments that have no form without their declaration (the scope notice, the runner-uncovered comment and the answer of suggest) are
// never queued with a declaration that cannot be shown: the job neither fails nor loses the check, and the log says the comment was skipped.
func TestChecksNeverQueueACommentWhoseDeclarationHoldsAHiddenCharacter(t *testing.T) {
	files := withDeclaration(bootFiles("v1"), cjDecl+hiddenComment)
	t.Run("the scope notice", func(t *testing.T) {
		w := newCJWorldOf(t, files)

		w.scopePull(withFiles(files, cjUtilTst), files)

		w.wantNoScopeMessages()
		w.logged("scope notice skipped")
		w.want(5, checks.Success, "unchanged", 1)
	})
	t.Run("the runner-uncovered comment", func(t *testing.T) {
		w := newCJWorldOf(t, files)

		w.runnerPull(withFiles(files, cjJest), added(cjJest)...)

		w.want(5, checks.InProgress, "runner-uncovered", 1)
		w.wantRunnerKeys()
		w.logged("runner-uncovered comment skipped")
	})
}

func TestSuggestRefusesAsAnInvalidSourceACanonicalWhoseDeclarationHoldsAHiddenCharacter(t *testing.T) {
	w := newCjWorld(t)
	w.seedDeclared(cjDecl+hiddenComment, withFiles(bootFiles("v1"), cjUtilTst))
	w.syncAll(nil, cjSuggest(500, cjAt(10)))

	w.mustRun()

	w.requireAckPayload(cjID2, 500, commands.SuggestRefused, map[string]any{"reason": "invalid-source"})
}
