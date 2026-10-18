package app

import (
	"encoding/json"
	"fmt"
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

// A declaration with no runner rules says so; a payload written before M1.9-E4 never read the runner rules, so it says nothing.
func TestBootstrapSummaryStatesNoRunnerRulesOnlyWhenTheDeclarationHasNone(t *testing.T) {
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

	delete(summary, "runner") // a payload written before M1.9-E4
	pre := cmtSummaryInputs()
	want := commandtext.BootstrapSummary(commandtext.Summary{Proposal: 1, Revision: 1, Origin: bootHead, Branch: "main", Protected: 2, Paths: pre.Paths,
		Include: pre.Include, Exclude: pre.Exclude, IncludeTotal: len(pre.Include), ExcludeTotal: len(pre.Exclude), Tier: commandtext.CoLocated})
	if got := render(payload); got != want || strings.Contains(got, "runner") {
		t.Fatalf("a payload without runner rules renders\n%s\nwant\n%s", got, want)
	}
}
