package app

import (
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// The mode of an approve is recorded next to its disposition, whatever the disposition is.
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

// A /suiteward mode line is no command (M2.1c, SW-16, text 7): the owner's line is recorded as any malformed line is, with no mode, no step and no operation, and
// it is answered on a pull request that is not open, on a fork and in a repository with no Suite too, because a malformed line is decided before those guards.
func TestAModeLineIsAnsweredAsMalformedAndRecordsNoMode(t *testing.T) {
	closedAt := cjSync.Add(10 * time.Minute)
	for _, tc := range []struct {
		name  string
		repo  int64
		setup func(w *cjWorld, mode cjComment)
	}{
		{"an open pull request", cjID1, func(w *cjWorld, mode cjComment) { w.syncAll([]cjComment{mode}) }},
		{"a pull request that closed after the comment", cjID1, func(w *cjWorld, mode cjComment) {
			w.syncAll([]cjComment{mode})
			w.closePull(cjID1, cjSync.Add(time.Minute))
		}},
		{"a pull request that was closed when the comment was first observed", cjID1, func(w *cjWorld, mode cjComment) {
			w.observe(cjID1, closedAt.Add(time.Minute), mode)
			w.observe(cjID2, cjSync.Add(20*time.Minute))
			w.closePull(cjID1, closedAt)
			w.closePull(cjID1, cjSync.Add(20*time.Minute))
		}},
		{"a fork", cjID1, func(w *cjWorld, mode cjComment) {
			w.headRepo = "evil/widgets"
			w.syncAll([]cjComment{mode})
		}},
		{"a repository with no Suite", cjID2, func(w *cjWorld, mode cjComment) { w.syncAll(nil, mode) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			created := cjAt(10)
			tc.setup(w, cjComment{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: created, body: "/suiteward mode fix"})

			w.mustRun()

			w.requireRejected(tc.repo, 500, commands.Malformed, commandtext.BadMode, "")
			if entry, _, _ := w.recorded(tc.repo, 500); entry.Mode != "" || entry.Operation != "" {
				t.Errorf("entry has mode %q and operation %q; want neither: a mode line records no mode", entry.Mode, entry.Operation)
			}
			if len(w.owners.consumed) != 0 {
				t.Errorf("the job consumed %d steps; a mode line carries no code", len(w.owners.consumed))
			}
		})
	}
}

// A row that an earlier version recorded as a mode-set still reads: the ledger takes it, and the job neither decides its comment again nor stops before the next one.
func TestAnEarlierModeSetEntryStillReadsAndItsCommentIsNotDecidedAgain(t *testing.T) {
	w := newCjWorld(t)
	earlier := cjComment{pull: cjPull, id: 500, author: cjOwnerID, created: cjAt(10), updated: cjAt(10), body: "/suiteward mode feature"}
	later := cjComment{pull: cjPull, id: 501, author: cjOwnerID, created: cjAt(11), updated: cjAt(11), body: "/suiteward mode fix"}
	w.syncAll([]cjComment{earlier, later})
	row := commands.Entry{RepositoryID: cjID1, CommentID: 500, PullNumber: cjPull, AuthorID: cjOwnerID, CreatedAt: earlier.created, BodyDigest: artifact.Hash([]byte(earlier.body)),
		Disposition: commands.ModeSet, Mode: "feature", DecidedAt: cjSync}
	if err := w.ledger.Record(t.Context(), row, nil); err != nil {
		t.Fatalf("the ledger refuses an entry of the mode-set disposition: %v", err)
	}

	w.mustRun()

	if entry, _ := w.requireDisposition(cjID1, 500, commands.ModeSet); entry.Mode != "feature" {
		t.Errorf("the earlier entry has mode %q; want feature", entry.Mode)
	}
	w.requireRejected(cjID1, 501, commands.Malformed, commandtext.BadMode, "")
	if len(w.ledger.records) != 2 {
		t.Errorf("%d entries were recorded; want the earlier mode-set and the one malformed line", len(w.ledger.records))
	}
}
