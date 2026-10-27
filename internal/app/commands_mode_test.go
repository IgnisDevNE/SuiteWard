package app

import (
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
)

// /suiteward mode <fix|feature|refactor> (docs/contracts/runtime.md, "Commands"): the owner sets the preview mode of a pull request. It carries no code, so
// it takes no step and no operation, and the closed-pull guard does not cover it: on a pull request that is not open, or a fork, it is refused and acknowledged.

func TestModeRecordsThePreviewModeAndTakesNoStep(t *testing.T) {
	for name, tc := range map[string]struct{ body, mode string }{
		"fix":              {cjMarker + "\n/suiteward mode fix", "fix"},
		"feature":          {cjMarker + "\n/suiteward mode feature", "feature"},
		"in capitals":      {"/SUITEWARD MODE Refactor", "refactor"},
		"after other text": {"Thanks!\n\n  /suiteward mode feature  \n", "feature"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCjWorld(t)
			created := cjAt(10)
			w.syncAll([]cjComment{{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: created, body: tc.body}})

			w.mustRun()

			entry, _ := w.requireDisposition(cjID1, 500, commands.ModeSet)
			if entry.Mode != tc.mode || entry.Operation != "" {
				t.Errorf("entry has mode %q and operation %q; want %q and none", entry.Mode, entry.Operation, tc.mode)
			}
			w.requireAckPayload(cjID1, 500, commands.ModeSet, map[string]any{"mode": tc.mode})
			if len(w.owners.consumed) != 0 {
				t.Errorf("the job consumed %d steps; a mode carries no code", len(w.owners.consumed))
			}
			if got, found, err := w.ledger.PreviewMode(t.Context(), cjID1, cjPull, cjOrigin); err != nil || !found || got != tc.mode {
				t.Errorf("PreviewMode = %q, %v, %v; want %q", got, found, err, tc.mode)
			}
			w.requireNothingLeaked()
		})
	}
}

// The preview mode of a pull request is its latest mode-set; it lasts across heads, and another pull request has its own.
func TestThePreviewModeIsTheLatestModeSet(t *testing.T) {
	w := newCjWorld(t)
	mode := func(id int64, minute int, body string) cjComment {
		created := cjAt(minute)
		return cjComment{pull: cjPull, id: id, author: cjOwnerID, created: created, updated: created, body: body}
	}
	w.syncAll([]cjComment{mode(500, 10, "/suiteward mode fix"), mode(501, 11, "/suiteward mode refactor"), mode(502, 12, "/suiteward mode feature").onPull(9)})

	w.mustRun()

	for pull, want := range map[int]string{cjPull: "refactor", 9: "feature"} {
		if got, found, err := w.ledger.PreviewMode(t.Context(), cjID1, pull, cjOrigin); err != nil || !found || got != want {
			t.Errorf("PreviewMode of pull %d = %q, %v, %v; want %q", pull, got, found, err, want)
		}
	}
	if _, found, err := w.ledger.PreviewMode(t.Context(), cjID1, cjPull, cjAt(11).Add(time.Second)); err != nil || found {
		t.Errorf("PreviewMode since after both modes found %v, error %v; want none: the preview mode is that of the current enrollment", found, err)
	}
}

// A mode is refused, and acknowledged, on a fork and on a pull request that is not open; nothing else is recorded and no step is used.
func TestModeIsRefusedOnAForkAndOnAPullRequestThatIsNotOpen(t *testing.T) {
	closedAt := cjSync.Add(10 * time.Minute)
	for _, tc := range []struct {
		name   string
		reason string
		setup  func(w *cjWorld, mode cjComment)
	}{
		{"a fork", "fork", func(w *cjWorld, mode cjComment) {
			w.headRepo = "evil/widgets"
			w.syncAll([]cjComment{mode})
		}},
		{"a pull request that closed after the comment", "not-open", func(w *cjWorld, mode cjComment) {
			w.syncAll([]cjComment{mode})
			w.closePull(cjID1, cjSync.Add(time.Minute))
		}},
		// The guard for a closed pull request covers commands that carry a code: a mode first observed after the close is refused all the same.
		{"a pull request that was closed when the comment was first observed", "not-open", func(w *cjWorld, mode cjComment) {
			w.observe(cjID1, closedAt.Add(time.Minute), mode)
			w.observe(cjID2, cjSync.Add(20*time.Minute))
			w.closePull(cjID1, closedAt)
			w.closePull(cjID1, cjSync.Add(20*time.Minute))
		}},
		{"a fork that is also closed", "fork", func(w *cjWorld, mode cjComment) {
			w.headRepo = "evil/widgets"
			w.syncAll([]cjComment{mode})
			w.closePull(cjID1, cjSync.Add(time.Minute))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			created := cjAt(10)
			tc.setup(w, cjComment{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: created, body: "/suiteward mode fix"})

			w.mustRun()

			w.requireDisposition(cjID1, 500, commands.ModeRefused)
			w.requireAckPayload(cjID1, 500, commands.ModeRefused, map[string]any{"reason": tc.reason})
			if len(w.owners.consumed) != 0 {
				t.Errorf("the job consumed %d steps; a mode carries no code", len(w.owners.consumed))
			}
			if _, found, err := w.ledger.PreviewMode(t.Context(), cjID1, cjPull, cjOrigin); err != nil || found {
				t.Errorf("PreviewMode found %v, error %v; a refused mode records no preview", found, err)
			}
		})
	}
}

// A mode needs no Suite: it only sets a preview.
func TestModeNeedsNoSuite(t *testing.T) {
	w := newCjWorld(t)
	created := cjAt(10)
	w.syncAll(nil, cjComment{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: created, body: "/suiteward mode feature"})

	w.mustRun()

	w.requireDisposition(cjID2, 500, commands.ModeSet)
}

// The mode of an approve is recorded next to its disposition, whatever the disposition is, and so is that of a mode comment; a mode that is no command is not.
func TestTheModeOfAnApproveIsRecordedWhateverItsDisposition(t *testing.T) {
	approve := func(code int64) cjComment {
		created := cjAt(10)
		return cjComment{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: created, body: "/suiteward approve P1-R2 refactor " + cjCode(created, code)}
	}
	for _, tc := range []struct {
		name string
		body cjComment
		want commands.Disposition
	}{
		{"a wrong code", approve(5), commands.InvalidCode},
		{"an unknown proposal", func() cjComment {
			c := approve(0)
			c.body = "/suiteward approve P9-R1 refactor " + cjCode(c.created, 0)
			return c
		}(), commands.UnknownProposal},
		{"a processed approve", approve(0), commands.Processed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			w.syncAll([]cjComment{tc.body})

			w.mustRun()

			entry, _, found := w.recorded(cjID1, 500)
			if !found || entry.Disposition != tc.want || entry.Mode != "refactor" {
				t.Fatalf("entry %+v (found %v); want %q with the mode refactor\nlogs: %s", entry, found, tc.want, w.logs)
			}
		})
	}
}
