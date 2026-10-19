// Package integrity is the port of the canonical integrity verdict: the latest result of re-verifying the stored
// bytes of each Suite's current canonical (docs/contracts/persistence.md, "Canonical integrity"; D-INTEGRATION).
// A verdict is evidence about the stored bytes, never authority: governance still decides.
package integrity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// ErrInvalid marks a record the store cannot keep (it fails Validate).
var ErrInvalid = errors.New("integrity: invalid record")

// Verdict is the result of one verification.
type Verdict string

const (
	// Passed means every content digest of the canonical's manifest verified.
	Passed Verdict = "passed"
	// Failed means a digest is missing or does not match its bytes.
	Failed Verdict = "failed"
	// Unavailable means the bytes could not be read for another reason.
	Unavailable Verdict = "unavailable"
)

// Record is the latest verdict of one Suite.
type Record struct {
	Project   contract.ProjectID
	Suite     contract.SuiteID
	Version   contract.SuiteVersionID // the canonical version verified
	Verdict   Verdict
	CheckedAt time.Time
}

// Store keeps the latest verdict of each Suite. Implementations run the shared conformance suite in integritytest.
type Store interface {
	// Record replaces the Suite's verdict, except that "unavailable" never replaces "failed" for the same version
	// (only CheckedAt moves), so an unreadable store never clears a confirmed failure.
	Record(ctx context.Context, r Record) error
	Latest(ctx context.Context, project contract.ProjectID, suite contract.SuiteID) (Record, bool, error)
}

// Validate reports why the record cannot be kept, as ErrInvalid.
func (r Record) Validate() error {
	switch {
	case strings.TrimSpace(string(r.Project)) == "" || strings.TrimSpace(string(r.Suite)) == "" || strings.TrimSpace(string(r.Version)) == "":
		return fmt.Errorf("%w: the project, the suite and the version are required", ErrInvalid)
	case r.Verdict != Passed && r.Verdict != Failed && r.Verdict != Unavailable:
		return fmt.Errorf("%w: unknown verdict %q", ErrInvalid, r.Verdict)
	case r.CheckedAt.IsZero():
		return fmt.Errorf("%w: the check time is required", ErrInvalid)
	}
	return nil
}
