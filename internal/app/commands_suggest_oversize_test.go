package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// A declaration over the size of a notice is never shown, and suggest does not refuse for it: it answers with the patterns alone, as it does for a
// declaration that holds a hidden character (decision 7 of M1.10).

// oversizeDeclaration is a valid declaration over commandtext.MaxDeclaration, and so is any declaration built from it.
func oversizeDeclaration() string {
	declaration := "version: 1\ninclude:\n"
	for i := range 400 {
		declaration += fmt.Sprintf("  - \"tests/directory%03d/**\"\n", i)
	}
	return declaration
}

// requirePatternsOnlyReply checks that the one answer of the command renders, from what is stored, as the patterns alone.
func (w *cjWorld) requirePatternsOnlyReply(repository int64, want map[string]any) {
	w.t.Helper()
	want["reason"] = "scope-notice"
	w.requireAckPayload(repository, 500, commands.Suggested, want)
	_, ack := w.requireDisposition(repository, 500, commands.Suggested)
	var p ackPayload
	if err := json.Unmarshal(ack.Payload, &p); err != nil || p.validate() != nil {
		w.t.Fatalf("the stored acknowledgment does not validate at publish: %v, %v", err, p.validate())
	}
	if text := commandtext.ScopeNoticeComment(p.notice()); !strings.Contains(text, "Patterns missing from the `include` list:\n\n```\n") || !strings.Contains(text, "*_test.go\n```") || strings.Contains(text, "tests/directory") || strings.Contains(text, "version: 1") {
		w.t.Fatalf("the reply does not show the pattern alone:\n%s", text)
	}
}

func TestSuggestOffersThePatternsAloneWhenTheCanonicalDeclarationIsOverTheLimit(t *testing.T) {
	w := newCjWorld(t)
	w.seedDeclared(oversizeDeclaration(), withFiles(bootFiles("v1"), cjUtilTst))
	w.syncAll(nil, cjSuggest(500, cjAt(10)))

	w.mustRun()

	w.requirePatternsOnlyReply(cjID2, map[string]any{"branch": cjBranch, "base": "contract", "paths": []string{cjUtilTst, "tests/a_test.go"}, "pathsTotal": 2, "addInclude": []string{"/tests/", "*_test.go"}})
}

// Before the first contract the default branch's file can be the base: the reply names it and links to the editor of the default branch.
func TestSuggestOffersThePatternsAloneWhenTheDefaultBranchDeclarationIsOverTheLimit(t *testing.T) {
	w := newCjWorld(t)
	w.trees(withDeclaration(bootFiles("v1"), oversizeDeclaration()), map[string]string{cjUtilTst: "x"})
	w.pullFiles(nil)
	w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})

	w.mustRun()

	w.requirePatternsOnlyReply(cjID1, map[string]any{"branch": cjBranch, "base": "default-branch", "defaultBranch": "main", "paths": []string{cjUtilTst}, "pathsTotal": 1, "addInclude": []string{"*_test.go"}})
}
