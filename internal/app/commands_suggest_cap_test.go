package app

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
)

// Twenty-six include patterns and a runner file the declaration lacks: the answer still names the runner pattern, holds 20 patterns in all and says
// it cut. The comment would not render, and the owner would be told nothing, if it held more.
func TestSuggestKeepsTheMissingRunnerPatternWhenTheIncludePatternsFillTheLimit(t *testing.T) {
	w := newCjWorld(t)
	w.trees(map[string]string{"README.md": "x"}, reviewFiles(reviewDecl, "jest.config.js",
		"a/testdata/x", "a/__tests__/x", "a/__snapshots__/x", "a/__mocks__/x", "a/__fixtures__/x", "a/cypress/x", "a/src/test/x", "a/src/it/x", "spec/x",
		"a/x.cy.ts", "a/x.spec.ts", "a/x.test.ts", "a/XTest.java", "a/XTest.kt", "a/XTest.php", "a/XTests.cs", "a/XTests.java", "a/x_spec.rb",
		"a/x_test.dart", "a/x_test.exs", "a/x_test.go", "a/x_test.rb"))
	w.pullFiles(changed("@@ -1,3 +1,3 @@"))
	w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
	w.mustRun()

	_, ack := w.requireDisposition(cjID1, 500, commands.Suggested)
	var got struct {
		Reason     string   `json:"reason"`
		AddInclude []string `json:"addInclude"`
		AddRunner  []string `json:"addRunner"`
		Cut        bool     `json:"cut"`
	}
	if ack == nil || json.Unmarshal(ack.Payload, &got) != nil {
		t.Fatalf("no acknowledgment to read: %+v", ack)
	}
	if got.Reason != "scope-notice" || !slices.Equal(got.AddRunner, []string{"jest.config.*"}) || len(got.AddInclude)+len(got.AddRunner) != 20 || !got.Cut {
		t.Fatalf("reason %s, %d include, runner %v, cut %v; want a scope notice with 19 include patterns, jest.config.* and a cut", got.Reason, len(got.AddInclude), got.AddRunner, got.Cut)
	}
}

// All 13 runner families bring 25 runner patterns, more than a notice holds: the answer lists 20, says it cut, and is a notice the publisher can render.
func TestSuggestAnswersARunnerSetOverTheLimitWithTheFirstTwentyAndACut(t *testing.T) {
	w := newCjWorld(t)
	w.trees(map[string]string{"README.md": "x"}, reviewFiles(reviewDecl, "conftest.py", "jest.config.js", "vitest.config.ts", ".mocharc.json", "karma.conf.js", "playwright.config.ts",
		"cypress.config.ts", ".coveragerc", "phpunit.xml", "spec_helper.rb", "test_helper.exs", "dart_test.yaml", "codecov.yml"))
	w.pullFiles(changed("@@ -1,3 +1,3 @@"))
	w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
	w.mustRun()

	_, ack := w.requireDisposition(cjID1, 500, commands.Suggested)
	var got struct {
		Reason     string   `json:"reason"`
		AddInclude []string `json:"addInclude"`
		AddRunner  []string `json:"addRunner"`
		Cut        bool     `json:"cut"`
	}
	if ack == nil || json.Unmarshal(ack.Payload, &got) != nil {
		t.Fatalf("no acknowledgment to read: %+v", ack)
	}
	if got.Reason != "scope-notice" || len(got.AddInclude)+len(got.AddRunner) != 20 || !got.Cut {
		t.Fatalf("reason %s, %d include and %d runner patterns, cut %v; want a scope notice of 20 patterns and a cut", got.Reason, len(got.AddInclude), len(got.AddRunner), got.Cut)
	}
}

// Before the first contract the runner pattern found has the same room as after it: with 24 include candidates it is in the reply of a suggest and in the
// refusal of a propose, which say the same for the same trees.
func TestBeforeTheFirstContractTheRunnerPatternKeepsItsRoomInTheSuggestionAndTheProposal(t *testing.T) {
	head := map[string]string{"src/main.go": "y", "jest.config.js": "x"}
	for _, path := range []string{"a/testdata/x", "a/__tests__/x", "a/__snapshots__/x", "a/__mocks__/x", "a/__fixtures__/x", "a/cypress/x", "a/src/test/x", "a/src/it/x", "tests/x", "test/x", "spec/x",
		"a/x.cy.ts", "a/x.spec.ts", "a/x.test.ts", "a/XTest.java", "a/XTest.kt", "a/XTest.php", "a/XTests.cs", "a/XTests.java", "a/x_spec.rb", "a/x_test.dart", "a/x_test.exs", "a/x_test.go", "a/x_test.rb"} {
		head[path] = "x"
	}
	for _, tc := range []struct {
		name        string
		comment     cjComment
		disposition commands.Disposition
	}{
		{"suggest", cjSuggest(500, cjAt(10)), commands.Suggested},
		{"propose", cjPropose(500, cjAt(10)), commands.ProposeRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			w.trees(map[string]string{"README.md": "x"}, head)
			w.syncAll([]cjComment{tc.comment})
			w.mustRun()

			_, ack := w.requireDisposition(cjID1, 500, tc.disposition)
			var got struct {
				Reason     string   `json:"reason"`
				Suggestion []string `json:"suggestion"`
				Runner     []string `json:"runner"`
				Cut        bool     `json:"cut"`
			}
			if ack == nil || json.Unmarshal(ack.Payload, &got) != nil {
				t.Fatalf("no acknowledgment to read: %+v", ack)
			}
			if got.Reason != "declaration-missing" || !slices.Equal(got.Runner, []string{"jest.config.*"}) || len(got.Suggestion)+len(got.Runner) != 20 || !got.Cut {
				t.Fatalf("reason %s, %d include patterns, runner %v, cut %v; want 19 include patterns, jest.config.* and a cut", got.Reason, len(got.Suggestion), got.Runner, got.Cut)
			}
		})
	}
}
