// Package owner is the port of the owner, TOTP and protection-result store
// (docs/contracts/persistence.md, "Owner, TOTP and protection results"). These
// are trusted local writes made by setup and the protection job; the policy
// revisions written by Enroll are the policies governance reads.
package owner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/totp"
)

// Tiers an enrollment can record: the INSTALLATION_TIER at enrollment.
const (
	TierIsolated  = "isolated"
	TierCoLocated = "co-located"
)

// Results, findings and caveats of a protection verification (D-PROTECTION),
// as the strings the store keeps. The protection package owns the codes.
const (
	ResultEnforced               = string(protection.KindEnforced)
	ResultFindings               = string(protection.KindFindings)
	ResultEnforcementUnavailable = string(protection.KindEnforcementUnavailable)
	ResultUnknown                = string(protection.KindUnknown)

	FindingMissingContractCheck = string(protection.MissingContractCheck)
	FindingMissingTestCheck     = string(protection.MissingTestCheck)
	FindingNotUpToDate          = string(protection.NotUpToDate)
	FindingAdminBypass          = string(protection.AdminBypass)
	CaveatRulesetBypass         = string(protection.RulesetOverrideUnverified)
)

// MaxTestCheckBytes bounds a project's required test check name.
const MaxTestCheckBytes = 200

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

// redacted is what a Secret renders as.
const redacted = "[redacted]"

// Format renders every verb as a placeholder.
func (Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// MarshalText hides the value from JSON and slog encoders.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// ProjectID is the project of a repository.
func ProjectID(repositoryID int64) contract.ProjectID {
	return contract.ProjectID("github-repo:" + strconv.FormatInt(repositoryID, 10))
}

// PrincipalID is the principal of an owner account.
func PrincipalID(accountID int64) contract.PrincipalID {
	return contract.PrincipalID("github:" + strconv.FormatInt(accountID, 10))
}

// SetupSource is the source command that consumes the TOTP step verified by the setup of a generation.
func SetupSource(generation int64) contract.SourceCommandID {
	return contract.SourceCommandID("setup:" + strconv.FormatInt(generation, 10))
}

// RevisionID is the policy revision written by the enrollment of a generation.
func RevisionID(generation int64) contract.PolicyRevisionID {
	return contract.PolicyRevisionID("setup-" + strconv.FormatInt(generation, 10))
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidState, fmt.Sprintf(format, args...))
}

// Validate reports why the enrollment cannot be recorded, as ErrInvalidState.
func (e Enrollment) Validate() error {
	switch {
	case len(e.Secret) != totp.SecretBytes:
		return invalid("the TOTP secret must be %d bytes", totp.SecretBytes)
	case e.Owner.AccountID <= 0:
		return invalid("the owner account id must be positive")
	case e.Tier != TierIsolated && e.Tier != TierCoLocated:
		return invalid("the tier must be %q or %q", TierIsolated, TierCoLocated)
	case len(e.Projects) == 0:
		return invalid("at least one project is required")
	case e.EnrolledAt.IsZero():
		return invalid("the enrollment time is required")
	}
	seen := map[int64]bool{}
	for _, p := range e.Projects {
		switch {
		case p.RepositoryID <= 0:
			return invalid("the repository id must be positive")
		case seen[p.RepositoryID]:
			return invalid("repository %d is listed twice", p.RepositoryID)
		case strings.TrimSpace(p.TestCheck) == "" || len(p.TestCheck) > MaxTestCheckBytes:
			return invalid("the test check of repository %d must be 1 to %d bytes", p.RepositoryID, MaxTestCheckBytes)
		}
		seen[p.RepositoryID] = true
	}
	return nil
}

// Validate reports why the result cannot be recorded, as ErrInvalidState.
func (r ProtectionResult) Validate() error {
	switch {
	case r.RepositoryID <= 0:
		return invalid("the repository id must be positive")
	case !slices.Contains([]string{ResultEnforced, ResultFindings, ResultEnforcementUnavailable, ResultUnknown}, r.Result):
		return invalid("unknown result %q", r.Result)
	case r.CheckedAt.IsZero():
		return invalid("the check time is required")
	case (r.Result == ResultFindings) != (len(r.Findings) > 0):
		return invalid("findings are required for the result %q and only for it", ResultFindings)
	}
	if err := validCodes("finding", r.Findings, FindingMissingContractCheck, FindingMissingTestCheck, FindingNotUpToDate, FindingAdminBypass); err != nil {
		return err
	}
	return validCodes("caveat", r.Caveats, CaveatRulesetBypass)
}

// validCodes requires codes to be known and strictly increasing.
func validCodes(kind string, codes []string, known ...string) error {
	for i, code := range codes {
		if !slices.Contains(known, code) {
			return invalid("unknown %s code %q", kind, code)
		}
		if i > 0 && codes[i-1] >= code {
			return invalid("%s codes must be sorted and distinct", kind)
		}
	}
	return nil
}

// Store is the owner, TOTP and protection-result store.
type Store interface {
	// Enroll replaces the owner and the secret and appends, per project, a policy revision owned by
	// PrincipalID(AccountID) of kind Human with revision id RevisionID(n) (n is the enrollment generation: 1,
	// then previous+1), records each project's test check and the tier, points the enrollment at the new
	// revision, and consumes Step for the new owner with the source SetupSource(n), in one transaction. An invalid enrollment, or a project
	// that already has a Suite while the owner changes, is ErrInvalidState and writes nothing; a used Step is
	// ErrStepUsed and writes nothing.
	Enroll(ctx context.Context, e Enrollment) error
	// Current returns the enrolled state, or found false before setup.
	Current(ctx context.Context) (State, bool, error)
	// ConsumeStep records that the owner used the TOTP step for the command source: a step is single use per owner
	// across all commands (ADR 0027), and the row records its source. A step already consumed by the same source
	// is success and writes nothing, so a retry after a crash is not a replay; one consumed by another source is
	// ErrStepUsed. ErrNotEnrolled without an owner or when accountID is not the enrolled owner; ErrInvalidState
	// without a time or a source.
	ConsumeStep(ctx context.Context, accountID int64, step int64, at time.Time, source contract.SourceCommandID) error
	// RecordProtection stores the latest result of one repository, replacing the previous one.
	RecordProtection(ctx context.Context, r ProtectionResult) error
	// Protection returns the latest result per repository, ordered by repository id.
	Protection(ctx context.Context) ([]ProtectionResult, error)
}
