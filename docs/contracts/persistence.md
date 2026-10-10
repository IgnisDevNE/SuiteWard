# Governance persistence contract

- **Status:** frozen for phase R1 (replaces the M1.01 contract `m1-01.md`); extended for phase M1.2 by "Jobs and outbox" below (frozen by M1.2-C0).
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

	// Enqueue and Outbox are written in the same transaction as the facts
	// above (M1.2); see "Jobs and outbox".
	Enqueue(context.Context, Job) error
	Outbox(context.Context, OutboxMessage) error
}

// Seeder writes trusted initial state. It is the only writer of policies,
// proposals, assessments and historical versions until the
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

// ConsentWrite appends one processed command. OperationID is the operation
// being processed. With Alias, Result is the domain's duplicate result, whose
// Command() is the original command (including its original operation id), and
// Receipt is the original receipt: the store records only OperationID for
// Result.Command().SourceCommandID().
type ConsentWrite struct {
	OperationID contract.OperationID
	Result      contract.CommandResult
	Receipt     ConsentReceipt
	Alias       bool
}

// PromotionWrite records a proposed promotion: the new version (its manifest
// bytes are verified now), the promotion record, and the new current pointer.
type PromotionWrite struct {
	Receipt PromotionReceipt
	Version contract.SuiteVersion
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

- **Consent:** inside `Do`: look up `Receipt(operationID)`. A consent receipt for the same Suite, proposal, source command and actor is returned as a duplicate; anything else is `ErrOperationConflict`. Then look up `ReceiptBySource(sourceCommandID)`: source command ids are global and the domain sees only one proposal's history, so a receipt there that is not a consent receipt for the same Suite, proposal and actor is `ErrOperationConflict`. Then load the proposal and consent, apply the command with the governing policy, and:
  - `ConsentReasonCommandConflict` → `ErrOperationConflict`, nothing written;
  - a duplicate result (same source command, new operation id) → `AppendConsent` with `Alias: true` and the original receipt from `ReceiptBySource`;
  - otherwise build the receipt (eligibility from `Consent.HasApproval`; `PromotedVersionID` from `PromotionFor(reference)`) and `AppendConsent`.
- **Promotion, bootstrap, correction:** inside `Do`: replay by `Receipt(operationID)` compares the stored identity with the request (same kind, reference, carrier, proposed contract, assessment source, integration, new version id, corrected version); mismatch is `ErrOperationConflict`. Otherwise load suite state, proposal, consent, assessment and (for correction) the target version; decide in the domain. Only a `PromotionProposed` decision writes, through `RecordPromotion`. Blocked and no-change outcomes are returned without writing. There is no admission gate: proposals based on the same canonical version are all promotable, the first promotion wins, and the canonical compare-and-set blocks the others with `PromotionReasonCanonicalChanged` until they have a new revision against the new canonical and a fresh exact approval.

### Store write semantics

- `AppendConsent` (not alias): insert the command result and an operation row carrying the receipt; bump the revision.
- `AppendConsent` (alias): insert only an operation row keyed by `ConsentWrite.OperationID` pointing at `Result.Command().SourceCommandID()` (never at `Result.Command().OperationID()`, which is the original operation), with a copy of the original receipt; bump the revision.
- `RecordPromotion`: verify the version's manifest bytes in the content store; insert the version, the promotion, and the operation row; set the Suite's current version; bump the revision. A duplicate version id or promoted reference is `ErrVersionConflict`.
- Time values are stored with microsecond precision (PostgreSQL `timestamptz`). Stores reject a `PromotionRecord.RecordedAt` with finer precision rather than truncating it, so callers building a `PromoteRequest` must truncate their clock to microseconds (`time.Now().UTC().Truncate(time.Microsecond)`).
- `Seed` stores the given state as is, including the Suite revision carried by `Canonical.Suite().Revision()`; it does not reset counters.
- `Do` must not commit when its context is canceled, even if `fn` returned nil; a `Tx` is unusable after `fn` returns.
- Reads reconstitute: proposals via `NewProposal` + `Revise` in sequence order; consent via `ReconstituteConsent` from stored results and aliases; assessments via `AssessIntegrity` over stored evidence; canonical and history via `NewSuite`, `NewSuiteVersion`, `NewPromotionRecord`, `NewCanonicalSnapshot`, `NewHistoricalCanonical`.

## Jobs and outbox (M1.2; normative)

Declared in `internal/application/governance/uow.go` next to `Tx`. River types never appear in `governance`.

```go
// Job is follow-up work for a background worker. Args reference scoped
// records and immutable revisions; they never carry credentials.
type Job struct {
	Kind        string          // ^[a-z][a-z0-9_.]*$, at most 64 bytes, for example "probe"
	Args        json.RawMessage // a JSON object, at most 64 KiB
	ScheduledAt time.Time       // zero means as soon as possible
}

// OutboxMessage records an external effect (for example publishing a check)
// together with the fact that requires it. Delivery is at least once, so a
// publisher must be idempotent by Key.
type OutboxMessage struct {
	Key     string          // globally unique idempotency key, at most 200 bytes
	Kind    string          // same syntax as Job.Kind; selects the publisher
	Payload json.RawMessage // a JSON object, at most 64 KiB
}
```

Semantics:

- `Job.Validate()` and `OutboxMessage.Validate()` (in `internal/application/governance/queue.go`) apply the limits above and are shared by every implementation of `Tx`, so the fake and the adapter reject the same inputs.
- `Enqueue` inserts the job and `Outbox` inserts the message through the unit of work's own `pgx.Tx`. They commit or roll back with the facts; there is no enqueue after commit. Neither advances the Suite revision (the revision counts governance facts only) and neither takes a lock beyond the Suite's.
- A malformed kind, a non-object or oversized `Args`/`Payload`, or an empty key is `ErrInvalidRequest`. A repeated `Key` is `ErrOperationConflict`; use cases replay by receipt before they write, so a repeated key means a genuine conflict.
- Retry limits, timeouts and backoff are runtime settings per kind (see [runtime](runtime.md)), not part of the port.
- A job's terminal state is River's `discarded` (retries exhausted) or `completed`; both stay in `river_job` and are counted by the runtime status endpoint.
- `outbox.project_id` and `outbox.suite_id` are both set (a message written through `Tx.Outbox`) or both null (a system message, for example the diagnostic `probe` written through the non-port `Store.EnqueueSystem`, which reuses the same insert statements without taking a Suite lock); a `CHECK` enforces it.
- The outbox row is the single owner of delivery state (it is not mirrored by a River job per message). A relay, itself a periodic River job that runs at startup and then on `OUTBOX_POLL_INTERVAL`, claims due rows with `FOR UPDATE SKIP LOCKED`, increments `attempts`, sets `next_attempt_at` to now plus `OUTBOX_LEASE` and commits; it then calls the publisher registered for the row's kind outside any transaction. An outcome update is conditional on the claim (`WHERE key = $1 AND state = 'pending' AND attempts = <attempts at claim>`): when a publish outlasts the lease and another relay has reclaimed the row, the late outcome updates nothing and is logged, never an error and never overwriting the newer claim. Success sets `state = delivered`. An error sets `last_error`; at `OUTBOX_MAX_ATTEMPTS` the state becomes `failed` (terminal, visible), otherwise `next_attempt_at` is now plus a capped exponential backoff. A crash between claim and outcome leaves the row pending until the lease expires, so it is redelivered. An unknown kind fails the row at once. Retried delivery never reverses a committed fact.
- River schema: River's own migrations are vendored into goose migrations (one migrator and one readiness check, ADR 0026), so `migrations.SupportedVersion` is the highest goose version and covers the River tables and the outbox. A test checks that a database migrated by goose passes River's own migrator validation. Upgrading River means adding a goose migration.

The conformance suite gains: rollback removes the job and the outbox row together with the facts; a duplicate key conflicts; a job and a message written in a unit of work that writes nothing else are committed; neither changes the revision.

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
```

Property every reconstitution test must check: state produced by domain operations, written as facts and reconstituted, behaves identically (`HasApproval`, `Results`, and the outcome of the next `Apply`).

## Tables (`00001_governance.sql` from R1; `00002`–`00009` vendor River, `00010` adds the outbox in M1.2)

| Table | Key | Mutability | Main columns |
| --- | --- | --- | --- |
| `suites` | (project_id, suite_id) | update only via revision +1 trigger; no delete | revision bigint, current_version_id (deferred FK), target_id, policy_revision_id |
| `policies` | (project_id, revision_id) | immutable | owner principal id and kind |
| `suite_versions` | (project_id, suite_id, version_id) | immutable | manifest_digest, manifest jsonb |
| `proposals` | (project_id, suite_id, proposal_id) | immutable | carrier_id |
| `proposal_revisions` | (project_id, suite_id, proposal_id, revision_id); unique seq | immutable | seq, origin, carrier, manifest/scope digests, covered_inputs jsonb, expected_version_id, policy_revision_id |
| `assessments` | (project_id, suite_id, proposal_id, revision_id, source) | immutable | evidence emitter, evidence source, evidence revision id (FK to `proposal_revisions` of the same proposal; the evidence binding is rebuilt from it, so evidence bound to another proposal is not representable and is rejected by the writer), outcome; all four null = missing evidence, otherwise all non-null |
| `consent_results` | source_command_id | append-only | operation_id, proposal, revision, actor, carrier, action, command_order, outcome, reason, seq |
| `operations` | operation_id (global) | append-only | project_id, suite_id, kind, source_command_id (consent kinds, FK to `consent_results`), receipt jsonb (immutable receipt payload with explicit json tags) |
| `promotions` | operation_id (unique; no FK, because seeded history has no receipt) | immutable | version_id (FK to `suite_versions`), proposal_id + revision_id (FK to `proposal_revisions`, which holds the binding), carrier, source, target, recorded_at, corrects_version_id; unique version and unique reference |
| `outbox` (M1.2) | key (global) | immutable except delivery state: `state` (`pending`, `delivered`, `failed`), `attempts`, `next_attempt_at`, `last_error`, `finished_at`; terminal states never change; no delete | project_id, suite_id, kind, payload jsonb, created_at |

River's own tables (`river_job` and the rest) are created by vendored goose migrations and owned by River.

All enumerations are `text` with `CHECK` constraints. Immutable and append-only tables reject `UPDATE`, `DELETE` and `TRUNCATE` by trigger. `migrations.SupportedVersion` is the single exported schema version used by both the migrator and the schema-readiness check.

## Behaviors that must stay covered

The conformance suite (fake and PostgreSQL) names each of these: replay by operation id and by source command id; the same operation id in another Suite or kind → conflict; a source command replayed by another actor → conflict; an alias advances the revision; rejected results do not consume command order; full rollback when `fn` fails; canceled context; version conflict. PostgreSQL integration tests add: concurrent promotions (exactly one wins), rollback when any statement fails, triggers rejecting history mutation, and missing or corrupt content rejected when a version is written.
