package app

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
)

func TestInventoryInputsCarryTheRunnerRules(t *testing.T) {
	files := bootFiles("v1")
	files[".suiteward.yml"] = "version: 1\ninclude: [/tests/**]\nrunner:\n  - pytest.ini\n  - conftest.py\n"
	inv, err := inventory.Build(t.Context(), &bootTree{files: files})
	if err != nil {
		t.Fatal(err)
	}
	inputs := inventoryInputs(bootstrapSummaryInputs{}, inv)
	if got := strings.Join(inputs.Runner, ","); got != "pytest.ini,conftest.py" {
		t.Fatalf("runner rules %q, want the declaration's, in order", got)
	}
}

func TestBootstrapSummaryNamesTheRunnerRulesTheDeclarationHas(t *testing.T) {
	long := strings.Repeat("r", 400)
	inputs := cmtSummaryInputs()
	inputs.Runner = []string{long}
	for i := 1; i < 25; i++ {
		inputs.Runner = append(inputs.Runner, fmt.Sprintf("runner%d.toml", i))
	}
	message := cmtMust(newSummaryMessage(cmtTarget(), inputs))
	var decoded bootstrapSummaryPayload
	if err := json.Unmarshal(message.Payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if s := decoded.Summary; len(s.Runner) != 20 || s.RunnerTotal != 25 || !strings.HasSuffix(s.Runner[0], "\u2026") || len(s.Runner[0]) > maxRunnerText {
		t.Fatalf("runner holds %d rules of %d, first %q; want 20 of 25, the first cut to the limit", len(s.Runner), s.RunnerTotal, s.Runner[0])
	}
	rendered := cmtMust(decoded.Summary.summary())
	if body := commandtext.BootstrapSummary(rendered); !strings.Contains(body, "Scope runner rules (20 listed; 5 more are not listed)") {
		t.Fatalf("the rendered comment does not count the runner rules it left out:\n%s", body)
	}
}

// A declaration with no runner rules says so, and so does a payload written before M1.9-E4, which holds no runner list.
func TestBootstrapSummaryStatesNoRunnerRulesWhenTheDeclarationHasNone(t *testing.T) {
	message := cmtMust(newSummaryMessage(cmtTarget(), cmtSummaryInputs()))
	var payload map[string]any
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	summary := payload["summary"].(map[string]any)
	if runner, present := summary["runner"]; !present || len(runner.([]any)) != 0 {
		t.Fatalf("summary %v, want a runner list that is present and empty", summary)
	}
	if _, present := summary["runnerTotal"]; present {
		t.Fatalf("summary %v holds a runner total of none", summary)
	}
	render := func(payload map[string]any) string {
		raw := cmtMust(json.Marshal(payload))
		var decoded bootstrapSummaryPayload
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		return commandtext.BootstrapSummary(cmtMust(decoded.Summary.summary()))
	}
	if body := render(payload); !strings.Contains(body, "Scope runner rules: none.") {
		t.Fatalf("a declaration without runner rules does not say so:\n%s", body)
	}

	want := render(payload)
	delete(summary, "runner") // a payload written before M1.9-E4 holds no runner list, which reads as none
	if got := render(payload); got != want {
		t.Fatalf("a payload without runner rules renders\n%s\nwant\n%s", got, want)
	}
}

// The longest summary: every list entry at its limit made of text that JSON writes as six bytes a character, a repository name of the 140 characters GitHub allows.
// It must stay 2 KiB under maxOutboxPayload, whatever the repository holds.
func TestBootstrapSummaryFitsTheOutboxWhenEveryByteIsEscaped(t *testing.T) {
	entries := func(size int) []string { return slices.Repeat([]string{strings.Repeat("<", size)}, maxSummaryRun) }
	inputs := cmtSummaryInputs()
	inputs.Branch, inputs.Paths, inputs.Include, inputs.Runner, inputs.Exclude = strings.Repeat("<", maxSummaryText), entries(maxSummaryText), entries(maxSummaryText), entries(maxRunnerText), entries(maxSummaryText)
	target := commentTarget{RepositoryID: cmtRepoID, Repo: strings.Repeat("a", 39) + "/" + strings.Repeat("a", 100), Pull: cmtPull}

	message := cmtMust(newSummaryMessage(target, inputs))

	if limit := maxOutboxPayload - 2<<10; len(message.Payload) > limit {
		t.Fatalf("payload of %d bytes, over %d: less than 2 KiB under the limit of %d", len(message.Payload), limit, maxOutboxPayload)
	}
}

// The same for the bootstrap summary, which carries the longest repository name and four lists at their limits.
func TestBootstrapSummaryCommentFitsGitHubWhateverTheTextIsMadeOf(t *testing.T) {
	const marker = len("<!-- suiteward:") + 32 + len(":bootstrap-summary:1001:P1-R1") + len(" -->")
	for name, unit := range map[string]string{"plain": "a", "escaped by JSON": "<", "invalid UTF-8": "\xff", "backtick": "`", "bidi control": "\u202e", "newline": "\n", "control": "\x01", "3-byte": "\u20ac"} {
		t.Run(name, func(t *testing.T) {
			entries := func() []string { return slices.Repeat([]string{strings.Repeat(unit, 400)}, 25) }
			inputs := cmtSummaryInputs()
			inputs.Branch, inputs.Paths, inputs.Include, inputs.Runner, inputs.Exclude = strings.Repeat(unit, 400), entries(), entries(), entries(), entries()
			inputs.Protected = 1000
			target := commentTarget{RepositoryID: cmtRepoID, Repo: strings.Repeat("a", 39) + "/" + strings.Repeat("a", 100), Pull: cmtPull}
			message := cmtMust(newSummaryMessage(target, inputs))
			var decoded bootstrapSummaryPayload
			if err := json.Unmarshal(message.Payload, &decoded); err != nil {
				t.Fatal(err)
			}
			body := commandtext.BootstrapSummary(cmtMust(decoded.Summary.summary()))
			if n := len(body) + marker; n >= 1<<16-4<<10 {
				t.Fatalf("the comment is %d bytes with its marker, want under GitHub's 65536 characters with 4 KiB to spare", n)
			}
		})
	}
}
