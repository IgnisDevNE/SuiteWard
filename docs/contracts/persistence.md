# Governance persistence contract

- **Status:** frozen for phase R1 (replaces the M1.01 contract `m1-01.md`).
- **Consumers:** R1-C (domain reconstitution), R1-D1/D2 (PostgreSQL adapter), R1-E (application port, use cases, in-memory fake, conformance suite).

## Principles

1. **One decision point.** Use cases load state under a per-Suite lock, decide in the domain, and hand the store explicit facts to write. The store never re-runs a domain decision and never compares decisions with `reflect.DeepEqual`.
2. **Facts, not snapshots.** Mutable governance state is stored as normalized rows. JSON is used only for immutable payloads (version manifests, covered inputs). Domain values are rebuilt with public constructors (`New*`, `Proposal.Revise`, `AssessIntegrity`) and the `Reconstitute*` functions below.
3. **Stable encodings.** Enumerations are stored as stable text codes owned by the adapter (`approved`, `revoked`, ...), never as Go `iota` values or Go field names. JSON payloads use explicit `json` tags.
4. **Every write advances the Suite revision by exactly one,** including receipt aliases. The revision is an `int64` counter; no exhaustion handling.
5. **Idempotency is global.** Operation ids are unique across all Suites and kinds. Reusing an operation id for a different Suite, kind, or request is `ErrOperationConflict`.
6. **Artifact bytes are verified when a version is written,** for that version's manifest only. Loads never re-hash stored artifacts. Periodic integrity verification is a later runtime job. (Previously every load and replay re-verified all canonical bytes; ADR 0007 records this change.)
7. **One transaction per unit of work,** reusable later for River job insertion and an outbox (M1.2) without changing the port.

## Application port (normative; R1-E creates `internal/application/governance/uow.go` with exactly these declarations, replacing `transaction.go`)

```go
package governance

// UnitOfWork runs fn while holding the lock of one Suite. Writes made through
// the Tx commit only if fn returns nil; any error rolls back every write.
// Do returns ErrNotFound when the Suite does not exist.
type UnitOfWork interface {
	Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, Tx) error) error
}

// SuiteState is the Suite-level authority read under the lock.
type SuiteState struct {
	Canonical contract.CanonicalSnapshot // Suite().Revision() is the stored counter; no current version before bootstrap
	Policy    contract.Policy            // governing policy revision
	Target    contract.IntegrationTargetID
	Schedule  contract.Schedule
}

// Tx is valid only inside fn. Reads see the transaction's own writes.
type Tx interface {
	Suite(context.Context) (SuiteState, error)
	// Proposal returns ErrNotFound when the proposal is unknown in this Suite.
	Proposal(context.Context, contract.ProposalID) (contract.Proposal, contract.Consent, error)
	Assessment(context.Context, contract.ProposalReference, contract.SourceRevision) (contract.IntegrityAssessment, bool, error)
	Version(context.Context, contract.SuiteVersionID) (contract.HistoricalCanonical, bool, error)
	PromotionFor(context.Context, contract.ProposalReference) (contract.PromotionRecord, bool, error)
	// Receipt lookups are global, not limited to the locked Suite.
	Receipt(context.Context, contract.OperationID) (OperationReceipt, bool, error)
	ReceiptBySource(context.Context, contract.SourceCommandID) (OperationReceipt, bool, error)

	AppendConsent(context.Context, ConsentWrite) error
	RecordPromotion(context.Context, PromotionWrite) error
	// M1.2 adds Enqueue (River) and Outbox writes here.
}

// Seeder writes trusted initial state. It is the only writer of policies,
// proposals, assessments, schedule entries and historical versions until the
// GitHub-facing phases add their own write paths.
type Seeder interface {
	Seed(context.Context, Seed) error
}

type Seed struct {
	Suite     SuiteState                     // Canonical may have no current version
	History   []contract.HistoricalCanonical // earlier versions, oldest first; includes the current one when present
	Proposals []SeedProposal
}

type SeedProposal struct {
	Proposal    contract.Proposal // all revisions, in order
	Assessments []contract.IntegrityAssessment
}

type OperationKind string

const (
	OperationPromote   OperationKind = "promote"
	OperationBootstrap OperationKind = "bootstrap"
	OperationCorrect   OperationKind = "correct"
	OperationConsent   OperationKind = "consent"
)

// OperationReceipt is a sum type: exactly one of Promotion or Consent is set,
// matching Kind.
type OperationReceipt struct {
	Kind      OperationKind
	ProjectID contract.ProjectID
	SuiteID   contract.SuiteID
	Promotion *PromotionReceipt
	Consent   *ConsentReceipt
}

type PromoteRequest struct {
	OperationID      contract.OperationID
	Reference        contract.ProposalReference
	Carrier          contract.ApprovalCarrierID
	Proposed         contract.ProtectedContract
	AssessmentSource contract.SourceRevision
	Integration      contract.Integration
	NewVersionID     contract.SuiteVersionID
	RecordedAt       time.Time
}

type CorrectionRequest struct {
	Promotion       PromoteRequest
	TargetVersionID contract.SuiteVersionID
}

type PromotionIdentity struct {
	Kind              OperationKind
	Request           PromoteRequest
	CorrectsVersionID contract.SuiteVersionID
	Binding           contract.ApprovalBinding
}

type PromotionReceipt struct {
	Identity PromotionIdentity
	Record   contract.PromotionRecord
}

type ConsentReceipt struct {
	Result                  contract.CommandResult
	EvaluatedReference      contract.ProposalReference
	PolicyRevisionID        contract.PolicyRevisionID
	CurrentApprovalEligible bool
	PromotedVersionID       contract.SuiteVersionID
}

// ConsentWrite appends one processed command. With Alias, Result is the
// duplicate observed for a new operation id and Receipt is the original
// receipt: the store records only the new operation id for the original
// source command.
type ConsentWrite struct {
	OperationID contract.OperationID
	Result      contract.CommandResult
	Receipt     ConsentReceipt
	Alias       bool
}

// PromotionWrite records a proposed promotion: the new version (its manifest
// bytes are verified now), the promotion record, the new current pointer, and
// the observed schedule.
type PromotionWrite struct {
	Receipt  PromotionReceipt
	Version  contract.SuiteVersion
	Schedule contract.Schedule
}

type PromoteResult struct {
	Outcome   contract.PromotionOutcome
	Reason    contract.PromotionReason
	Record    contract.PromotionRecord // set when Outcome is PromotionProposed
	Committed bool
	Duplicate bool
}

type ConsentRequest struct{ Command contract.Command }

type ConsentResponse struct {
	Receipt   ConsentReceipt
	Committed bool
	Duplicate bool
}

var (
	ErrInvalidRequest    = errors.New("invalid governance request")
	ErrInvalidState      = errors.New("invalid stored governance state")
	ErrOperationConflict = errors.New("operation identity conflict")
	ErrVersionConflict   = errors.New("version identity conflict")
	ErrNotFound          = errors.New("governance aggregate not found")
)
```

Use cases keep their names: `ProcessConsent(ctx, UnitOfWork, ConsentRequest)`, `Promote(ctx, UnitOfWork, PromoteRequest)`, `Bootstrap(ctx, UnitOfWork, PromoteRequest)` (existing-baseline only), `Correct(ctx, UnitOfWork, CorrectionRequest)`. Errors from the domain or the store are wrapped with `%w`, preserving the cause.

### Use-case semantics

- **Consent:** inside `Do`: look up `Receipt(operationID)`. A consent receipt for the same source command, actor and proposal is returned as a duplicate; anything else is `ErrOperationConflict`. Then load the proposal and consent, apply the command with the governing policy, and:
  - `ConsentReasonCommandConflict` → `ErrOperationConflict`, nothing written;
  - a duplicate result (same source command, new operation id) → `AppendConsent` with `Alias: true` and the original receipt from `ReceiptBySource`;
  - otherwise build the receipt (eligibility from `Consent.HasApproval`; `PromotedVersionID` from `PromotionFor(reference)`) and `AppendConsent`.
- **Promotion, bootstrap, correction:** inside `Do`: replay by `Receipt(operationID)` compares the stored identity with the request (same kind, reference, carrier, proposed contract, assessment source, integration, new version id, corrected version); mismatch is `ErrOperationConflict`. Otherwise load suite state, proposal, consent, assessment and (for correction) the target version; decide in the domain. Only a `PromotionProposed` decision writes: `Schedule.Observe(..., ObservePromoted)` then `RecordPromotion`. Blocked and no-change outcomes are returned without writing.

### Store write semantics

- `AppendConsent` (not alias): insert the command result and an operation row carrying the receipt; bump the revision.
- `AppendConsent` (alias): insert only an operation row for the new operation id pointing at the original source command, with a copy of the original receipt; bump the revision.
- `RecordPromotion`: verify the version's manifest bytes in the content store; insert the version, the promotion, and the operation row; set the Suite's current version; persist schedule entry states and generation; bump the revision. A duplicate version id or promoted reference is `ErrVersionConflict`.
- Reads reconstitute: proposals via `NewProposal` + `Revise` in sequence order; consent via `ReconstituteConsent` from stored results and aliases; assessments via `AssessIntegrity` over stored evidence; schedule via `ReconstituteSchedule`; canonical and history via `NewSuite`, `NewSuiteVersion`, `NewPromotionRecord`, `NewCanonicalSnapshot`, `NewHistoricalCanonical`.

## Domain reconstitution (R1-C implements in `internal/domain/contract`)

```go
// ReconstituteCommandResult rebuilds a stored, non-duplicate command result.
func ReconstituteCommandResult(command Command, outcome ConsentOutcome, reason ConsentReason) (CommandResult, error)

// ReconstituteConsent rebuilds consent for proposal's aggregate from stored
// results in their original order plus operation aliases. It derives the
// per-revision, per-actor state exactly as Apply produced it and rejects
// inconsistent input (foreign aggregate, duplicate or conflict results,
// non-increasing order for an actor and revision, unknown revisions, aliases
// pointing at unknown source commands).
func ReconstituteConsent(proposal Proposal, results []CommandResult, aliases map[OperationID]SourceCommandID) (Consent, error)

type ScheduleEntryInput struct {
	ProposalID ProposalID
	Carrier    ApprovalCarrierID
	State      ScheduleEntryState
}

// ReconstituteSchedule rebuilds a schedule; at most one entry is active and
// proposal ids and carriers are unique.
func ReconstituteSchedule(project ProjectID, suite SuiteID, generation ScheduleGeneration, entries []ScheduleEntryInput) (Schedule, error)
```

Property every reconstitution test must check: state produced by domain operations, written as facts and reconstituted, behaves identically (`HasApproval`, `Results`, `Active`, `CanPromote`, and the outcome of the next `Apply`/`Observe`).

## Tables (R1-D1 creates one migration `00001_governance.sql`)

| Table | Key | Mutability | Main columns |
| --- | --- | --- | --- |
| `suites` | (project_id, suite_id) | update only via revision +1 trigger; no delete | revision bigint, current_version_id (deferred FK), target_id, policy_revision_id, schedule_generation bigint |
| `policies` | (project_id, revision_id) | immutable | owner principal id and kind |
| `suite_versions` | (project_id, suite_id, version_id) | immutable | manifest_digest, manifest jsonb |
| `proposals` | (project_id, suite_id, proposal_id) | immutable | carrier_id |
| `proposal_revisions` | (project_id, suite_id, proposal_id, revision_id); unique seq | immutable | seq, origin, carrier, manifest/scope digests, covered_inputs jsonb, expected_version_id, policy_revision_id |
| `assessments` | (project_id, suite_id, proposal_id, revision_id, source) | immutable | evidence emitter, evidence source, outcome (null = missing evidence) |
| `schedule_entries` | (project_id, suite_id, proposal_id); unique carrier | only `state` and `position`-preserving updates | position, state text |
| `consent_results` | source_command_id | append-only | operation_id, proposal, revision, actor, carrier, action, command_order, outcome, reason, seq |
| `operations` | operation_id (global) | append-only | project_id, suite_id, kind, source_command_id (consent), consent receipt columns, promotion identity columns |
| `promotions` | operation_id → operations | immutable | version_id, proposal/revision, carrier, source, target, recorded_at, corrects_version_id; unique version and reference |

All enumerations are `text` with `CHECK` constraints. Immutable and append-only tables reject `UPDATE`, `DELETE` and `TRUNCATE` by trigger. `migrations.SupportedVersion` is the single exported schema version used by both the migrator and the schema-readiness check.

## Behaviors that must stay covered

The conformance suite (fake and PostgreSQL) names each of these: replay by operation id and by source command id; the same operation id in another Suite or kind → conflict; a source command replayed by another actor → conflict; an alias advances the revision; rejected results do not consume command order; full rollback when `fn` fails; canceled context; version conflict. PostgreSQL integration tests add: concurrent promotions (exactly one wins), rollback when any statement fails, triggers rejecting history mutation, and missing or corrupt content rejected when a version is written.
