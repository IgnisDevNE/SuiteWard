package app

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

// The log of a skipped declaration says what failed, never what GitHub or the head's file said.
func TestChecksLogNothingTheDeclarationReadOrTheHeadsFileSaid(t *testing.T) {
	t.Run("a failed read of the declaration", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.linkHeadReadFailing(fmt.Errorf("%w: %s", &rest.StatusError{Status: 502}, bootSecret))

		w.mustCheck()

		fields := w.logged("declaration skipped")
		if fields["kind"] != "status" || fields["status"] != float64(502) || fields["repo"] != bootRepo {
			t.Fatalf("log fields %v, want the kind, the status and the repository of the failed read", fields)
		}
		if strings.Contains(w.logs.String(), bootSecret) {
			t.Fatalf("a log line carries what GitHub sent: %s", w.logs)
		}
	})
	t.Run("an exclude that cannot be added", func(t *testing.T) {
		var logs bytes.Buffer
		j := &checksJob{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
		tree := &bootTree{files: map[string]string{".suiteward.yml": "version: 1\n" + bootSecret + ": 1\n"}}

		declaration, noFile, err := j.acceptingDeclaration(t.Context(), bootRepo, tree, "spec_link")

		if declaration != nil || noFile || err != nil {
			t.Fatalf("declaration %q, no file %v, error %v, want the pattern alone", declaration, noFile, err)
		}
		if !strings.Contains(logs.String(), "the exclude cannot be added") || strings.Contains(logs.String(), bootSecret) {
			t.Fatalf("log %s, want the fixed message and none of the head's file", logs.String())
		}
	})
}
