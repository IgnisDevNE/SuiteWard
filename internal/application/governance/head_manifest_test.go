package governance_test

import (
	"context"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// The change report is rebuilt from the manifest of a revision, which only the code that assessed the head holds: the notice hands it on.
func TestTheRevisionNoticeCarriesTheManifestOfTheRevision(t *testing.T) {
	e := newHeadEnv(t)
	tree := headTree("v2", "package main\n")
	var notices []governance.RevisionNotice
	request := e.request(prA, "head-a", tree)
	request.Revision = func(n governance.RevisionNotice) (governance.OutboxMessage, bool, error) {
		notices = append(notices, n)
		return governance.OutboxMessage{}, false, nil
	}

	if _, err := governance.AssessHead(context.Background(), e.mem, e.reader, e.writer, request); err != nil {
		t.Fatalf("assess head: %v", err)
	}

	want := treeContract(t, tree).Manifest()
	if len(notices) != 1 || notices[0].Manifest.IsZero() || notices[0].Manifest.Digest() != want.Digest() {
		t.Fatalf("notices = %+v, want one that carries the manifest %s", notices, want.Digest())
	}
}
