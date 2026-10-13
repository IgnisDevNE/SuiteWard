// Package owner is the port of the owner, TOTP and protection-result store
// (docs/contracts/persistence.md, "Owner, TOTP and protection results"). These
// are trusted local writes made by setup and the protection job; the policy
// revisions written by Enroll are the policies governance reads.
package owner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Tiers an enrollment can record: the INSTALLATION_TIER at enrollment.
const (
	TierIsolated  = "isolated"
	TierCoLocated = "co-located"
)

// Results of a protection verification (D-PROTECTION).
const (
	ResultEnforced               = "enforced"
	ResultFindings               = "findings"
	ResultEnforcementUnavailable = "enforcement-unavailable"
	ResultUnknown                = "unknown"
)

// Finding and caveat codes of a protection verification (D-PROTECTION).
const (
	FindingMissingContractCheck = "missing-contract-check"
	FindingMissingTestCheck     = "missing-test-check"
	FindingNotUpToDate          = "not-up-to-date"
	FindingAdminBypass          = "admin-bypass"
	CaveatRulesetBypass         = "ruleset-bypass-unverified"
)

// MaxTestCheckBytes bounds a project's required test check name.
const MaxTestCheckBytes = 200

// secretBytes is the TOTP secret length; it must equal totp.SecretBytes once
// that package lands.
const secretBytes = 20

var (
	ErrStepUsed     = errors.New("owner: TOTP step already used")
	ErrInvalidState = errors.New("owner: invalid enrollment or result")
	ErrNotEnrolled  = errors.New("owner: no owner enrolled")
)

// Secret is a TOTP secret. It never renders: every fmt verb, JSON and slog
// show a placeholder, so no struct holding one can print it. A []byte is
// assignable to it and back.
type Secret []byte

// Owner is the instance's owner.
type Owner struct {
	AccountID int64  // GitHub numeric account id, > 0
	Login     string // display only
}

// ProjectPolicy is one configured repository.
type ProjectPolicy struct {
	RepositoryID int64  // GitHub numeric repository id, > 0; the project is ProjectID(RepositoryID)
	Repo         string // owner/name, display only
	TestCheck    string // the repository's required project test check, non-empty, at most MaxTestCheckBytes
}

// Enrollment is what setup records.
type Enrollment struct {
	Owner      Owner
	Secret     Secret          // the TOTP secret, exactly 20 bytes
	Projects   []ProjectPolicy // one per configured repository, distinct ids, at least one
	Step       int64           // the TOTP step verified during setup; consumed in the same transaction
	Tier       string          // TierIsolated or TierCoLocated
	EnrolledAt time.Time
}

// State is the enrolled state.
type State struct {
	Owner          Owner
	Secret         Secret
	Projects       []ProjectPolicy // ordered by repository id
	Tier           string
	PolicyRevision contract.PolicyRevisionID // the governing revision of every project, "setup-<n>"
	EnrolledAt     time.Time
}

// ProtectionResult is the latest protection verification of one repository.
type ProtectionResult struct {
	RepositoryID int64
	Result       string   // one of the Result constants
	Findings     []string // D-PROTECTION finding codes, sorted, empty unless Result is ResultFindings
	Caveats      []string // D-PROTECTION caveat codes, sorted
	CheckedAt    time.Time
}

var errNotImplemented = errors.New("owner: not implemented")

// ProjectID is the project of a repository.
func ProjectID(repositoryID int64) contract.ProjectID { return "" }

// PrincipalID is the principal of an owner account.
func PrincipalID(accountID int64) contract.PrincipalID { return "" }

// RevisionID is the policy revision written by the enrollment of a generation.
func RevisionID(generation int64) contract.PolicyRevisionID { return "" }

// Validate reports why the enrollment cannot be recorded, as ErrInvalidState.
func (Enrollment) Validate() error { return fmt.Errorf("%w", errNotImplemented) }

// Validate reports why the result cannot be recorded, as ErrInvalidState.
func (ProtectionResult) Validate() error { return fmt.Errorf("%w", errNotImplemented) }

// Store is the owner, TOTP and protection-result store.
type Store interface {
	// Enroll replaces the owner and the secret and appends, per project, a policy revision owned by
	// PrincipalID(AccountID) of kind Human with revision id RevisionID(n) (n is the enrollment generation: 1,
	// then previous+1), records each project's test check and the tier, points the enrollment at the new
	// revision, and consumes Step for the new owner, in one transaction. An invalid enrollment, or a project
	// that already has a Suite while the owner changes, is ErrInvalidState and writes nothing; a used Step is
	// ErrStepUsed and writes nothing.
	Enroll(ctx context.Context, e Enrollment) error
	// Current returns the enrolled state, or found false before setup.
	Current(ctx context.Context) (State, bool, error)
	// ConsumeStep records that the owner used the TOTP step: ErrStepUsed if it was used before (global single use
	// per owner and step, across all commands, ADR 0027), ErrNotEnrolled without an owner or when accountID is not
	// the enrolled owner.
	ConsumeStep(ctx context.Context, accountID int64, step int64, at time.Time) error
	// RecordProtection stores the latest result of one repository, replacing the previous one.
	RecordProtection(ctx context.Context, r ProtectionResult) error
	// Protection returns the latest result per repository, ordered by repository id.
	Protection(ctx context.Context) ([]ProtectionResult, error)
}
