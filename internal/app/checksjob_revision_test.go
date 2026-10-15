package app

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// revisionMessages returns the revision comments the job wrote, in order.
func (w *cjJobWorld) revisionMessages() []governance.OutboxMessage {
	var found []governance.OutboxMessage
	for _, m := range w.suites.messages {
		if strings.HasPrefix(m.Key, "revision:") {
			found = append(found, m)
		}
	}
	return found
}

func (w *cjJobWorld) revisionKeys() []string {
	var keys []string
	for _, m := range w.revisionMessages() {
		keys = append(keys, m.Key)
	}
	return keys
}

func TestChecksWriteOneRevisionCommentForEveryNewRevisionAndNoneForAnUnchangedHead(t *testing.T) {
	w := newCJWorld(t, true)
	w.protectedPull()

	if got := w.revisionKeys(); !slices.Equal(got, []string{"revision:1001:P2-R1"}) {
		t.Fatalf("revision comments %v, want exactly the one of P2-R1", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.revisionMessages()[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["pull"] != float64(5) || payload["canonical"] != "V1" || payload["reference"] != "P2-R1" || payload["tier"] != "isolated" || payload["previous"] != nil ||
		!slices.Equal(anyStrings(payload["modified"]), []string{"tests/a_test.go"}) {
		t.Fatalf("payload %v, want pull 5, base V1, P2-R1 with tests/a_test.go modified and no previous revision", payload)
	}

	w.mustCheck() // nothing moved
	files := bootFiles("v2")
	files["src/main.go"] = "package main // changed\n"
	w.tree(cjHead3, files)
	w.sync(map[int]string{4: bootHead, 5: cjHead3}) // an implementation-only push keeps the revision
	w.mustCheck()
	if got := w.revisionKeys(); !slices.Equal(got, []string{"revision:1001:P2-R1"}) {
		t.Fatalf("revision comments %v after an unchanged head and an implementation-only push, want still one", got)
	}

	w.tree(cjHead4, bootFiles("v3"))
	w.sync(map[int]string{4: bootHead, 5: cjHead4}) // a protected push is a new revision
	w.mustCheck()
	w.mustCheck()

	if got := w.revisionKeys(); !slices.Equal(got, []string{"revision:1001:P2-R1", "revision:1001:P2-R2"}) {
		t.Fatalf("revision comments %v, want one per revision", got)
	}
	if err := json.Unmarshal(w.revisionMessages()[1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["previous"] != "P2-R1" {
		t.Fatalf("payload %v, want the superseded revision P2-R1", payload)
	}
	w.want(5, checks.InProgress, "approval-missing", 4)
}

func anyStrings(value any) []string {
	var strs []string
	list, _ := value.([]any)
	for _, item := range list {
		strs = append(strs, item.(string))
	}
	return strs
}
