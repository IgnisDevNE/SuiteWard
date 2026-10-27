package app

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// tests of a body, as the maps that the encoder takes.
func bodyTests(b body) []body { return b["tests"].([]body) }

func oneTest(b body) body { return bodyTests(b)[0] }

func many(n int, build func(i int) body) []body {
	tests := make([]body, n)
	for i := range tests {
		tests[i] = build(i)
	}
	return tests
}

// refused is a change that makes a result break a rule of the contract.
type refused map[string]func(job execution.Job, b body, e *execTestEnv)

// canonicalRules are the rules every complete canonical result must keep.
var canonicalRules = refused{
	"an unknown status":   func(_ execution.Job, b body, _ *execTestEnv) { b["status"] = "done" },
	"no status":           func(_ execution.Job, b body, _ *execTestEnv) { delete(b, "status") },
	"no executor":         func(_ execution.Job, b body, _ *execTestEnv) { delete(b, "executor") },
	"no executor version": func(_ execution.Job, b body, _ *execTestEnv) { b["executor"] = body{"binarySha256": execBinary} },
	"a version that starts with -": func(_ execution.Job, b body, _ *execTestEnv) {
		b["executor"] = body{"version": "-1", "binarySha256": execBinary}
	},
	"a version with a space": func(_ execution.Job, b body, _ *execTestEnv) {
		b["executor"] = body{"version": "1 0", "binarySha256": execBinary}
	},
	"a version of 65 bytes": func(_ execution.Job, b body, _ *execTestEnv) {
		b["executor"] = body{"version": strings.Repeat("v", 65), "binarySha256": execBinary}
	},
	"no binary digest": func(_ execution.Job, b body, _ *execTestEnv) { b["executor"] = body{"version": "1.0"} },
	"a short binary digest": func(_ execution.Job, b body, _ *execTestEnv) {
		b["executor"] = body{"version": "1.0", "binarySha256": execBinary[:63]}
	},
	"an upper-case binary digest": func(_ execution.Job, b body, _ *execTestEnv) {
		b["executor"] = body{"version": "1.0", "binarySha256": strings.ToUpper(strings.Repeat("a", 64))}
	},
	"a prefixed binary digest": func(_ execution.Job, b body, _ *execTestEnv) {
		b["executor"] = body{"version": "1.0", "binarySha256": "sha256:" + execBinary}
	},
	"no sandbox": func(_ execution.Job, b body, _ *execTestEnv) { delete(b, "sandbox") },
	"no image digest": func(_ execution.Job, b body, _ *execTestEnv) {
		b["sandbox"] = body{"toolchain": "go1.27.3", "runtime": "podman 5.5.0"}
	},
	"an image digest of a tag": func(_ execution.Job, b body, _ *execTestEnv) {
		b["sandbox"] = body{"imageDigest": "golang:1.27", "toolchain": "go1.27.3", "runtime": "podman 5.5.0"}
	},
	"an image digest without the prefix": func(_ execution.Job, b body, _ *execTestEnv) {
		b["sandbox"] = body{"imageDigest": strings.Repeat("b", 64), "toolchain": "go1.27.3", "runtime": "podman 5.5.0"}
	},
	"no toolchain": func(_ execution.Job, b body, _ *execTestEnv) {
		b["sandbox"] = body{"imageDigest": execImage, "runtime": "podman 5.5.0"}
	},
	"a toolchain of 65 bytes": func(_ execution.Job, b body, _ *execTestEnv) {
		b["sandbox"] = body{"imageDigest": execImage, "toolchain": strings.Repeat("g", 65), "runtime": "podman 5.5.0"}
	},
	"a toolchain that is not ASCII": func(_ execution.Job, b body, _ *execTestEnv) {
		b["sandbox"] = body{"imageDigest": execImage, "toolchain": "gö1", "runtime": "podman 5.5.0"}
	},
	"a toolchain with a tab": func(_ execution.Job, b body, _ *execTestEnv) {
		b["sandbox"] = body{"imageDigest": execImage, "toolchain": "go\t1", "runtime": "podman 5.5.0"}
	},
	"no runtime": func(_ execution.Job, b body, _ *execTestEnv) {
		b["sandbox"] = body{"imageDigest": execImage, "toolchain": "go1.27.3"}
	},
	"a runtime of 65 bytes": func(_ execution.Job, b body, _ *execTestEnv) {
		b["sandbox"] = body{"imageDigest": execImage, "toolchain": "go1.27.3", "runtime": strings.Repeat("r", 65)}
	},
	"a runtime that is not ASCII": func(_ execution.Job, b body, _ *execTestEnv) {
		b["sandbox"] = body{"imageDigest": execImage, "toolchain": "go1.27.3", "runtime": "podmán 5"}
	},
	"a runtime with a newline": func(_ execution.Job, b body, _ *execTestEnv) {
		b["sandbox"] = body{"imageDigest": execImage, "toolchain": "go1.27.3", "runtime": "podman\n5"}
	},
	"a time that is no time":  func(_ execution.Job, b body, _ *execTestEnv) { b["startedAt"] = "yesterday" },
	"a time without a zone":   func(_ execution.Job, b body, _ *execTestEnv) { b["startedAt"] = "2026-10-27T11:58:00" },
	"no start":                func(_ execution.Job, b body, _ *execTestEnv) { delete(b, "startedAt") },
	"no end":                  func(_ execution.Job, b body, _ *execTestEnv) { delete(b, "endedAt") },
	"an end before the start": func(_ execution.Job, b body, e *execTestEnv) { b["startedAt"], b["endedAt"] = e.window(0, time.Minute) },
	"a start over five minutes ahead": func(_ execution.Job, b body, e *execTestEnv) {
		b["startedAt"], b["endedAt"] = e.window(-5*time.Minute-time.Second, -10*time.Minute)
	},
	"an end over five minutes ahead": func(_ execution.Job, b body, e *execTestEnv) {
		b["startedAt"], b["endedAt"] = e.window(time.Minute, -5*time.Minute-time.Second)
	},
	"a test of kind added in a canonical result": func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["kind"] = "added" },
	"a test of no kind":                          func(_ execution.Job, b body, _ *execTestEnv) { delete(oneTest(b), "kind") },
	"a test with no name":                        func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["name"] = "" },
	"a name that is not Test":                    func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["name"] = "Contract" },
	"a lower-case name":                          func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["name"] = "testContract" },
	"a name with a dash":                         func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["name"] = "Test-Contract" },
	"a name with a space":                        func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["name"] = "Test Contract" },
	"a name with a slash":                        func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["name"] = "TestA/b" },
	"a name of 201 bytes":                        func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["name"] = "Test" + strings.Repeat("a", 197) },
	"no package":                                 func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["package"] = "" },
	"an absolute package":                        func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["package"] = "/pkg" },
	"a package with ..":                          func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["package"] = "../pkg" },
	"a package through ..":                       func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["package"] = "a/../b" },
	"a package with //":                          func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["package"] = "a//b" },
	"a package with /./":                         func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["package"] = "a/./b" },
	"a package with a trailing slash":            func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["package"] = "pkg/" },
	"a package of 513 bytes":                     func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["package"] = strings.Repeat("p", 513) },
	"no file":                                    func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["file"] = "" },
	"a file that is not a test file":             func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["file"] = "contract.go" },
	"an absolute file":                           func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["file"] = "/contract_test.go" },
	"a file with ..":                             func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["file"] = "../contract_test.go" },
	"a file through ..":                          func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["file"] = "a/../contract_test.go" },
	"a file with //":                             func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["file"] = "a//contract_test.go" },
	"a file of 1025 bytes": func(_ execution.Job, b body, _ *execTestEnv) {
		oneTest(b)["file"] = strings.Repeat("f", 1025-8) + "_test.go"
	},
	"no head runs":    func(_ execution.Job, b body, _ *execTestEnv) { delete(oneTest(b), "head") },
	"empty head runs": func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["head"] = []string{} },
	"four head runs": func(_ execution.Job, b body, _ *execTestEnv) {
		oneTest(b)["head"] = []string{"fail", "fail", "fail", "pass"}
	},
	"an unknown observation":          func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["head"] = []string{"passed"} },
	"the observation excluded":        func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["head"] = []string{"excluded"} },
	"an empty observation":            func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["head"] = []string{""} },
	"an upper-case observation":       func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["head"] = []string{"PASS"} },
	"base runs in a canonical result": func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["base"] = []string{"pass"} },
	"an unknown excluded reason": func(_ execution.Job, b body, _ *execTestEnv) {
		delete(oneTest(b), "head")
		oneTest(b)["excluded"] = "integration"
	},
	"an empty excluded reason": func(_ execution.Job, b body, _ *execTestEnv) {
		delete(oneTest(b), "head")
		oneTest(b)["excluded"] = ""
	},
	"an excluded test with runs": func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["excluded"] = "build-constraint" },
	"an unknown field of a test": func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["output"] = "PASS" },
	"the same test twice": func(_ execution.Job, b body, _ *execTestEnv) {
		b["tests"] = []body{oneTest(b), oneTest(b)}
	},
	"10001 tests": func(_ execution.Job, b body, _ *execTestEnv) {
		b["tests"] = many(10001, func(i int) body {
			return body{"package": ".", "name": fmt.Sprintf("Test%d", i), "file": "a_test.go", "kind": "declared", "head": []string{"pass"}}
		})
	},
	"markers in a canonical result": func(_ execution.Job, b body, _ *execTestEnv) {
		b["markers"] = []body{{"kind": "env-read", "path": "a.go", "line": 1}}
	},
	"unparsed files in a canonical result": func(_ execution.Job, b body, _ *execTestEnv) { b["unparsed"] = []string{"a_test.go"} },
	"101 submodules": func(_ execution.Job, b body, _ *execTestEnv) {
		paths := make([]string, 101)
		for i := range paths {
			paths[i] = fmt.Sprintf("sub%d", i)
		}
		b["submodules"] = paths
	},
	"a submodule with ..":       func(_ execution.Job, b body, _ *execTestEnv) { b["submodules"] = []string{"../sub"} },
	"an absolute submodule":     func(_ execution.Job, b body, _ *execTestEnv) { b["submodules"] = []string{"/sub"} },
	"an empty submodule":        func(_ execution.Job, b body, _ *execTestEnv) { b["submodules"] = []string{""} },
	"a submodule of 4097 bytes": func(_ execution.Job, b body, _ *execTestEnv) { b["submodules"] = []string{strings.Repeat("s", 4097)} },
}

// controlText is text that the contract refuses anywhere: a control character, a newline, a format character, a line separator.
var controlText = map[string]string{
	"a control character": "x\x01y", "a newline": "x\ny", "a carriage return": "x\ry", "a NUL": "x\x00y", "a zero-width space": "x\u200by", "a right-to-left override": "x\u202ey",
	"a line separator": "x\u2028y", "a paragraph separator": "x\u2029y", "a byte order mark": "x\ufeffy", "a delete": "x\x7fy", "a C1 control": "x\u0085y",
}

func TestResultRefusesEveryRuleBreakOfACanonicalResult(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	for name, edit := range canonicalRules {
		b := e.complete(leased)
		edit(leased, b, e)
		got := e.post(leased, leased.LeaseID, b)
		if got.status != http.StatusBadRequest || strings.TrimSpace(got.text()) != `{"error":"invalid"}` {
			t.Errorf("%s = %d %.200s; want 400 invalid", name, got.status, got.text())
		}
	}
	e.untouched(leased, leased)
}

// Every string of a result is clean text: this puts each kind of unclean text into every string that a result has.
func TestResultRefusesUncleanTextInEveryString(t *testing.T) {
	e := newExecEnv(t)
	e.receiptJob(1, defaultFiles)
	leased := e.grant()
	fields := map[string]func(b body, text string){
		"the reason": func(b body, text string) {
			b["status"], b["reason"] = "unavailable", "executor-error"+text
			delete(b, "tests")
		},
		"a test's package":   func(b body, text string) { oneTest(b)["package"] = "pkg" + text },
		"a test's name":      func(b body, text string) { oneTest(b)["name"] = "TestA" + text },
		"a test's file":      func(b body, text string) { oneTest(b)["file"] = "a" + text + "_test.go" },
		"a test's kind":      func(b body, text string) { oneTest(b)["kind"] = "added" + text },
		"an observation":     func(b body, text string) { oneTest(b)["head"] = []string{"pass" + text} },
		"a base observation": func(b body, text string) { oneTest(b)["base"] = []string{"fail" + text} },
		"an excluded reason": func(b body, text string) {
			oneTest(b)["excluded"] = "build-constraint" + text
			delete(oneTest(b), "head")
			delete(oneTest(b), "base")
		},
		"a marker kind": func(b body, text string) {
			b["markers"] = []body{{"kind": "env-read" + text, "path": "a.go", "line": 1}}
		},
		"a marker path": func(b body, text string) {
			b["markers"] = []body{{"kind": "env-read", "path": "a" + text + ".go", "line": 1}}
		},
		"a submodule":          func(b body, text string) { b["submodules"] = []string{"sub" + text} },
		"an unparsed file":     func(b body, text string) { b["unparsed"] = []string{"a" + text + "_test.go"} },
		"the executor version": func(b body, text string) { b["executor"] = body{"version": "1.0" + text, "binarySha256": execBinary} },
		"the toolchain": func(b body, text string) {
			b["sandbox"] = body{"imageDigest": execImage, "toolchain": "go" + text, "runtime": "podman"}
		},
		"the runtime": func(b body, text string) {
			b["sandbox"] = body{"imageDigest": execImage, "toolchain": "go1", "runtime": "podman" + text}
		},
		"the status":        func(b body, text string) { b["status"] = "complete" + text },
		"the head's sha":    func(b body, text string) { b["head"] = body{"sha": leased.HeadSHA + text} },
		"the base's sha":    func(b body, text string) { b["base"] = body{"sha": leased.BaseSHA + text} },
		"the start time":    func(b body, text string) { b["startedAt"] = fmt.Sprint(b["startedAt"]) + text },
		"the binary digest": func(b body, text string) { b["executor"] = body{"version": "1.0", "binarySha256": execBinary + text} },
		"the image digest": func(b body, text string) {
			b["sandbox"] = body{"imageDigest": execImage + text, "toolchain": "go1", "runtime": "podman"}
		},
	}
	for field, put := range fields {
		for kind, text := range controlText {
			b := e.complete(leased)
			put(b, text)
			got := e.post(leased, leased.LeaseID, b)
			if got.status != http.StatusBadRequest || strings.TrimSpace(got.text()) != `{"error":"invalid"}` {
				t.Errorf("%s with %s = %d %.200s; want 400 invalid", field, kind, got.status, got.text())
			}
		}
	}
	e.untouched(leased, leased)
}

// receiptRules are the rules of a complete receipt that a canonical result does not have.
var receiptRules = refused{
	"a test of kind declared in a receipt": func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["kind"] = "declared" },
	"no base runs":                         func(_ execution.Job, b body, _ *execTestEnv) { delete(oneTest(b), "base") },
	"empty base runs":                      func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["base"] = []string{} },
	"four base runs": func(_ execution.Job, b body, _ *execTestEnv) {
		oneTest(b)["base"] = []string{"fail", "fail", "fail", "pass"}
	},
	"an unknown base observation":   func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["base"] = []string{"failed"} },
	"the base observation excluded": func(_ execution.Job, b body, _ *execTestEnv) { oneTest(b)["base"] = []string{"excluded"} },
	"an excluded test with base runs": func(_ execution.Job, b body, _ *execTestEnv) {
		delete(oneTest(b), "head")
		oneTest(b)["excluded"] = "nested-module"
	},
	"a marker of an unknown kind": func(_ execution.Job, b body, _ *execTestEnv) {
		b["markers"] = []body{{"kind": "panic", "path": "a.go", "line": 1}}
	},
	"a marker of no kind": func(_ execution.Job, b body, _ *execTestEnv) { b["markers"] = []body{{"path": "a.go", "line": 1}} },
	"a marker at line 0": func(_ execution.Job, b body, _ *execTestEnv) {
		b["markers"] = []body{{"kind": "env-read", "path": "a.go", "line": 0}}
	},
	"a marker at a negative line": func(_ execution.Job, b body, _ *execTestEnv) {
		b["markers"] = []body{{"kind": "env-read", "path": "a.go", "line": -1}}
	},
	"a marker past line 10000000": func(_ execution.Job, b body, _ *execTestEnv) {
		b["markers"] = []body{{"kind": "env-read", "path": "a.go", "line": 10000001}}
	},
	"a marker at a fractional line": func(_ execution.Job, b body, _ *execTestEnv) {
		b["markers"] = []body{{"kind": "env-read", "path": "a.go", "line": 1.5}}
	},
	"a marker with no path": func(_ execution.Job, b body, _ *execTestEnv) {
		b["markers"] = []body{{"kind": "env-read", "path": "", "line": 1}}
	},
	"a marker with a path through ..": func(_ execution.Job, b body, _ *execTestEnv) {
		b["markers"] = []body{{"kind": "env-read", "path": "a/../../b.go", "line": 1}}
	},
	"a marker with an absolute path": func(_ execution.Job, b body, _ *execTestEnv) {
		b["markers"] = []body{{"kind": "env-read", "path": "/a.go", "line": 1}}
	},
	"201 markers": func(_ execution.Job, b body, _ *execTestEnv) {
		b["markers"] = many(201, func(i int) body { return body{"kind": "env-read", "path": "a.go", "line": i + 1} })
	},
	"an unparsed file that is not a test file": func(_ execution.Job, b body, _ *execTestEnv) { b["unparsed"] = []string{"a.go"} },
	"an unparsed file with ..":                 func(_ execution.Job, b body, _ *execTestEnv) { b["unparsed"] = []string{"../a_test.go"} },
	"101 unparsed files": func(_ execution.Job, b body, _ *execTestEnv) {
		paths := make([]string, 101)
		for i := range paths {
			paths[i] = fmt.Sprintf("a%d_test.go", i)
		}
		b["unparsed"] = paths
	},
	"an unavailable receipt with tests": func(_ execution.Job, b body, _ *execTestEnv) {
		b["status"], b["reason"] = "unavailable", "executor-error"
	},
}

func TestResultRefusesEveryRuleBreakOfAReceipt(t *testing.T) {
	e := newExecEnv(t)
	e.receiptJob(1, defaultFiles)
	leased := e.grant()
	for name, edit := range receiptRules {
		b := e.complete(leased)
		edit(leased, b, e)
		got := e.post(leased, leased.LeaseID, b)
		if got.status != http.StatusBadRequest || strings.TrimSpace(got.text()) != `{"error":"invalid"}` {
			t.Errorf("%s = %d %.200s; want 400 invalid", name, got.status, got.text())
		}
	}
	e.untouched(leased, leased)
}

// The bounds themselves are accepted: every limit of the contract, at its value.
func TestResultAcceptsEveryLimitAtItsValue(t *testing.T) {
	e := newExecEnv(t)
	e.receiptJob(1, defaultFiles)
	leased := e.grant()
	b := e.complete(leased)
	b["executor"] = body{"version": "1.0.0+build.5-" + strings.Repeat("x", 50), "binarySha256": execBinary} // 64 bytes
	b["sandbox"] = body{"imageDigest": execImage, "toolchain": strings.Repeat("g", 64), "runtime": "podman 5.5.0" + strings.Repeat(" ", 52)}
	now := e.clock.Now()
	b["startedAt"], b["endedAt"] = now.Add(5*time.Minute).Format(time.RFC3339), now.Add(5*time.Minute).Format(time.RFC3339) // five minutes ahead, not more
	tests := many(9990, func(i int) body {
		return body{"package": "p/" + strings.Repeat("q", 20), "name": fmt.Sprintf("Test%d", i), "file": "p/a_test.go", "kind": "added", "head": []string{"fail", "fail", "pass"}, "base": []string{"absent", "absent", "fail"}}
	})
	tests = append(tests,
		body{"package": ".", "name": "Test", "file": "a_test.go", "kind": "changed", "head": []string{"pass"}, "base": []string{"pass"}},
		body{"package": strings.Repeat("d", 512), "name": "TestÜber_9", "file": strings.Repeat("f", 1024-8) + "_test.go", "kind": "added", "head": []string{"pass"}, "base": []string{"fail"}},
		body{"package": "x", "name": "Test" + strings.Repeat("a", 196), "file": "x_test.go", "kind": "added", "excluded": "nested-module"},
	)
	for i := range 7 {
		tests = append(tests, body{"package": "m", "name": fmt.Sprintf("TestM%d", i), "file": "m_test.go", "kind": "added", "head": []string{"pass"}, "base": []string{"pass"}})
	}
	b["tests"] = tests // 10,000 tests
	kinds := []string{"testing-import", "testing-testing", "flag-lookup-test", "exit-in-init", "test-file-read", "env-read", "forged-event"}
	b["markers"] = many(200, func(i int) body {
		return body{"kind": kinds[i%len(kinds)], "path": "pkg/file.go", "line": []int{1, 10000000}[i%2]}
	})
	subs := make([]string, 100)
	unparsed := make([]string, 100)
	for i := range subs {
		subs[i], unparsed[i] = fmt.Sprintf("sub/%d", i), fmt.Sprintf("u/%d_test.go", i)
	}
	b["submodules"], b["unparsed"] = subs, unparsed

	got := e.post(leased, leased.LeaseID, b)

	e.wantState(got, "complete")
	if tests, err := e.store.Tests(t.Context(), leased.ID); err != nil || len(tests) != 10000 {
		t.Errorf("stored tests = %d, %v; want 10000", len(tests), err)
	}
}
