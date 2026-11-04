package app

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
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
