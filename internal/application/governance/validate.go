package governance

import (
	"fmt"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The checks of a write that do not need the store. Every Tx implementation calls them first (PostgreSQL, and
// governancetest.Memory for the tests), so a write that production refuses is refused by the stand-in as well.

// exactMicroseconds reports whether moment survives a round trip through PostgreSQL's timestamptz, which keeps microseconds.
func exactMicroseconds(moment time.Time) bool { return moment.Equal(moment.Truncate(time.Microsecond)) }

// Validate reports ErrInvalidRequest when the write is incomplete, belongs to another Suite than the locked one, or its
// receipt describes another source command, and ErrOperationConflict when its operation id does not match the result it records.
func (w ConsentWrite) Validate(project contract.ProjectID, suite contract.SuiteID) error {
	command := w.Result.Command()
	reference := command.Reference()
	switch {
	case w.OperationID == "" || command.OperationID() == "":
		return fmt.Errorf("%w: a consent write needs its operation and command", ErrInvalidRequest)
	case reference.ProjectID != project || reference.SuiteID != suite:
		return fmt.Errorf("%w: command %q belongs to another suite", ErrInvalidRequest, command.SourceCommandID())
	case w.Result.Duplicate() != w.Alias || (!w.Alias && w.OperationID != command.OperationID()):
		return fmt.Errorf("%w: operation %q does not match the command result it records", ErrOperationConflict, w.OperationID)
	case w.Receipt.Result.Command().SourceCommandID() != command.SourceCommandID():
		return fmt.Errorf("%w: the receipt belongs to another source command", ErrInvalidRequest)
	}
	return nil
}

// Validate reports ErrInvalidRequest when the write is incomplete, belongs to another Suite than the locked one, or its
// receipt disagrees with its record about the operation, the corrected version or the approval binding, or the record's
// time has more precision than the store keeps. Whether the version and the proposal revision it names exist is the store's to say.
func (w PromotionWrite) Validate(project contract.ProjectID, suite contract.SuiteID) error {
	receipt, record := w.Receipt, w.Receipt.Record
	reference := record.Binding().Reference()
	switch {
	case record.IsZero() || w.Version.ID() == "":
		return fmt.Errorf("%w: a promotion write needs its record and version", ErrInvalidRequest)
	case reference.ProjectID != project || reference.SuiteID != suite || w.Version.ProjectID() != project || w.Version.SuiteID() != suite:
		return fmt.Errorf("%w: promotion %q belongs to another suite", ErrInvalidRequest, record.OperationID())
	case receipt.Identity.Request.OperationID != record.OperationID():
		return fmt.Errorf("%w: the receipt request and the promotion record name different operations", ErrInvalidRequest)
	case receipt.Identity.CorrectsVersionID != record.CorrectsVersionID() || !receipt.Identity.Binding.Equal(record.Binding()):
		return fmt.Errorf("%w: the receipt identity and the promotion record disagree about the corrected version or the approval binding", ErrInvalidRequest)
	case !exactMicroseconds(record.RecordedAt()):
		return fmt.Errorf("%w: recorded time %s needs sub-microsecond precision that PostgreSQL does not keep", ErrInvalidRequest, record.RecordedAt().Format(time.RFC3339Nano))
	}
	return nil
}
