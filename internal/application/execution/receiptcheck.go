package execution

import (
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
)

// NextReceipt is the record stored when r is published at time at over current (found false: there is none), and whether it carries
// a new generation, hence a new message. It is checks.Next with unblocked in the identity: a new head, state, reason, reference or
// unblocked advances the generation, and so does a current message that ended failed (failedAt, its finished_at; zero when it did
// not) at least checks.RepublishCooldown before at. Otherwise only the job and the decision time move.
func NextReceipt(current ReceiptRecord, found bool, r ReceiptCheck, at, failedAt time.Time) (next ReceiptRecord, advance bool) {
	at = Stored(at)
	if !found {
		return ReceiptRecord{ReceiptCheck: r, HeadSince: at, Generation: 1, DecidedAt: at}, true
	}
	next = ReceiptRecord{ReceiptCheck: r, HeadSince: current.HeadSince, Generation: current.Generation, DecidedAt: at}
	if r.HeadSHA != current.HeadSHA {
		next.HeadSince = at
	}
	lost := !failedAt.IsZero() && !at.Before(failedAt.Add(checks.RepublishCooldown))
	changed := r.HeadSHA != current.HeadSHA || r.State != current.State || r.Reason != current.Reason || r.Reference != current.Reference || r.Unblocked != current.Unblocked
	if lost || changed {
		next.Generation++
		advance = true
	}
	return next, advance
}
