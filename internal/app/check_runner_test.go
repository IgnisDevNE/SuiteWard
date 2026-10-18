package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// The check of a runner-uncovered head lists its runner files in the summary (docs/contracts/runtime.md, "Check reason runner-uncovered").

func runnerCheckMessage(t *testing.T, paths []string, total int) governance.OutboxMessage {
	t.Helper()
	r := checks.Result{RepositoryID: cmtRepoID, PullNumber: cmtPull, HeadSHA: chkHead, State: checks.InProgress, Reason: "runner-uncovered"}
	m, err := newCheckMessageWithPaths(cmtRepo, r, owner.TierIsolated, "V2", 3, paths, total)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNewCheckMessageCarriesTheRunnerFilesOfARunnerUncoveredCheck(t *testing.T) {
	m := runnerCheckMessage(t, []string{"jest.config.js", "pkg/conftest.py"}, 25)

	var got map[string]any
	if err := json.Unmarshal(m.Payload, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"repositoryId": 1001.0, "repo": cmtRepo, "pull": 7.0, "headSha": chkHead, "generation": 3.0, "state": "in_progress",
		"reason": "runner-uncovered", "tier": "isolated", "version": "V2", "paths": []any{"jest.config.js", "pkg/conftest.py"}, "pathsTotal": 25.0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload %v, want %v", got, want)
	}
}

func TestNewCheckMessageWithoutPathsHasNoPathFields(t *testing.T) {
	m := chkMessage(t, checks.InProgress, "approval-missing", "P1-R2")

	var got map[string]any
	if err := json.Unmarshal(m.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if _, has := got["paths"]; has || got["pathsTotal"] != nil {
		t.Fatalf("payload %v holds paths", got)
	}
}

func TestCheckPublisherListsTheRunnerFilesInTheSummary(t *testing.T) {
	w := newChkWorld()

	if err := w.deliver(runnerCheckMessage(t, []string{"jest.config.js", "a```b\n"}, 3)); err != nil {
		t.Fatal(err)
	}

	posts := w.runner.posts()
	if len(posts) != 1 || posts[0].Title != "Runner files outside the protected scope" {
		t.Fatalf("posts %+v", posts)
	}
	for _, want := range []string{"````\njest.config.js\n\"a```b\\n\"\n````", "and 1 more"} {
		if !strings.Contains(posts[0].Summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, posts[0].Summary)
		}
	}
}

// A check message written before this field existed, and any other reason, renders as before.
func TestCheckPublisherRendersAPayloadWithoutPaths(t *testing.T) {
	for _, reason := range []string{"runner-uncovered", "unchanged"} {
		t.Run(reason, func(t *testing.T) {
			w := newChkWorld()
			state := checks.InProgress
			if reason == "unchanged" {
				state = checks.Success
			}
			m := chkMessage(t, state, reason, "")

			if err := w.deliver(m); err != nil {
				t.Fatal(err)
			}

			if posts := w.runner.posts(); len(posts) != 1 || strings.Contains(posts[0].Summary, "```\n") {
				t.Fatalf("posts %+v, want one summary without a listing", posts)
			}
		})
	}
}

func TestCheckPublisherRefusesPathsThatDoNotFit(t *testing.T) {
	long := strings.Repeat("p", 151)
	for name, change := range map[string]func(map[string]any){
		"paths of another reason": func(m map[string]any) {
			m["reason"], m["state"] = "approval-missing", "in_progress"
			m["reference"] = "P1-R2"
		},
		"21 paths": func(m map[string]any) {
			m["paths"], m["pathsTotal"] = strings.Split(strings.Repeat("a,", 21), ",")[:21], 21
		},
		"a total below the paths":  func(m map[string]any) { m["pathsTotal"] = 1 },
		"a path over the bound":    func(m map[string]any) { m["paths"], m["pathsTotal"] = []string{long}, 1 },
		"a total without paths":    func(m map[string]any) { delete(m, "paths"); m["pathsTotal"] = 2 },
		"paths of a success check": func(m map[string]any) { m["reason"], m["state"] = "unchanged", "success" },
	} {
		t.Run(name, func(t *testing.T) {
			w := newChkWorld()
			m := runnerCheckMessage(t, []string{"a", "b"}, 2)
			var payload map[string]any
			if err := json.Unmarshal(m.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			change(payload)
			raw, _ := json.Marshal(payload)

			if err := w.pub.publish(t.Context(), m.Key, raw); err == nil {
				t.Fatal("publish succeeded")
			}
			if len(w.runner.posts()) != 0 {
				t.Fatal("a check was posted for a payload that was refused")
			}
		})
	}
}

// The job passes the runner files to the check message of a runner-uncovered result and to no other.
func TestChecksPublishTheRunnerFilesWithTheRunnerUncoveredCheck(t *testing.T) {
	w := newCJWorld(t, true)

	w.runnerPull(cjHead2, withFiles(bootFiles("v1"), cjJest, "pkg/conftest.py"), added(cjJest, "pkg/conftest.py")...)

	w.want(5, checks.InProgress, "runner-uncovered", 1)
	var payload struct {
		Reason     string   `json:"reason"`
		Paths      []string `json:"paths"`
		PathsTotal int      `json:"pathsTotal"`
	}
	if err := json.Unmarshal(w.ledger.messages[len(w.ledger.messages)-1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Reason != "runner-uncovered" || !reflect.DeepEqual(payload.Paths, []string{cjJest, "pkg/conftest.py"}) || payload.PathsTotal != 2 {
		t.Fatalf("check payload %+v, want both runner files", payload)
	}
	// Another reason of the same pull request carries none.
	w.tree(cjHead3, bootFiles("v1"))
	w.sync(map[int]string{4: bootHead, 5: cjHead3})
	w.mustCheck()
	w.want(5, checks.Success, "unchanged", 2)
	if raw := string(w.ledger.messages[len(w.ledger.messages)-1].Payload); strings.Contains(raw, "paths") {
		t.Fatalf("check payload %s holds paths", raw)
	}
}
