package app

import (
	"net/http"
	"strings"
	"testing"
)

// A test name is Test followed by Unicode letters, digits and _ (what a Go identifier allows), at most 200 bytes: the service takes what the executor reads
// from go test.
func TestResultAcceptsTheTestNamesThatAGoIdentifierAllows(t *testing.T) {
	e := newExecEnv(t)
	e.receiptJob(1, defaultFiles)
	leased := e.grant()
	b := e.complete(leased)
	names := []string{"Test", "TestÄ_1", "Test日本語", "Test１２", "Test٣", "Test_", "Test" + strings.Repeat("Ä", 98)} // the last is 200 bytes
	tests := make([]body, len(names))
	for i, name := range names {
		tests[i] = body{"package": ".", "name": name, "file": "a_test.go", "kind": "added", "head": []string{"pass"}, "base": []string{"fail"}}
	}
	b["tests"] = tests

	e.wantState(e.post(leased, leased.LeaseID, b), "complete")
}

func TestResultRefusesTheTestNamesThatAGoIdentifierDoesNotAllow(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	for _, name := range []string{"Test²", "Test½", "Test x", "Test.", "Test-1", "Test/Sub", "TestÄ" + strings.Repeat("Ä", 98), "test_1", "Tes"} { // the third from last is 201 bytes
		b := e.complete(leased)
		oneTest(b)["name"] = name
		if got := e.post(leased, leased.LeaseID, b); got.status != http.StatusBadRequest {
			t.Errorf("name %q = %d %s; want 400", name, got.status, got.text())
		}
	}
	e.untouched(leased, leased)
}

// The paths of a result are clean and relative, and bounded at the size of what they name: a marker or a skipped submodule is a path of a tree, at most
// 4096 bytes; an unparsed file is a test file, at most 1024.
func TestResultBoundsTheMarkerSubmoduleAndUnparsedPaths(t *testing.T) {
	for _, test := range []struct {
		name  string
		edit  func(b body, size int)
		limit int
	}{
		{"a marker path", func(b body, size int) {
			b["markers"] = []body{{"kind": "env-read", "path": strings.Repeat("m", size), "line": 1}}
		}, 4096},
		{"a submodule path", func(b body, size int) { b["submodules"] = []string{strings.Repeat("s", size)} }, 4096},
		{"an unparsed path", func(b body, size int) {
			b["unparsed"] = []string{strings.Repeat("u", size-len("_test.go")) + "_test.go"}
		}, 1024},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := newExecEnv(t)
			e.receiptJob(1, defaultFiles)
			leased := e.grant()
			over := e.complete(leased)
			test.edit(over, test.limit+1)
			if got := e.post(leased, leased.LeaseID, over); got.status != http.StatusBadRequest {
				t.Errorf("%d bytes = %d %s; want 400", test.limit+1, got.status, got.text())
			}
			e.untouched(leased, leased)

			at := e.complete(leased)
			test.edit(at, test.limit)
			e.wantState(e.post(leased, leased.LeaseID, at), "complete")
		})
	}
}
