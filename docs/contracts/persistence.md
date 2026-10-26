# Governance persistence contract

- **Status:** frozen for phase R1 (replaces the M1.01 contract `m1-01.md`); extended for phase M1.2 by "Jobs and outbox" below (frozen by M1.2-C0) and for phase M1.3 by "Proposal revisions" (frozen by M1.3-C0) and "Baseline proposals" (frozen before M1.3-C2) below, and for phase M1.4 by "GitHub observations" (frozen by M1.4-B0), and by each later phase through M1.11 (`v0.2.0`, released 2026-10-22), including phase SH, and by phase M2.1 ("Execution", [ADR 0028](../decisions/0028-canonical-execution-and-the-receipt.md)).
- **Consumers:** R1-C (domain reconstitution), R1-D1/D2 (PostgreSQL adapter), R1-E (application port, use cases, in-memory fake, conformance suite), M1.3-C1 (proposal revision write), M1.3-C2 (baseline proposals), M1.4-B1 (observation store), M1.4-B2 (reconciliation).

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
	// AppendProposalRevision (M1.3); see "Proposal revisions".
	AppendProposalRevision(context.Context, ProposalWrite) error

	// Outbox is written in the same transaction as the facts above (M1.2); see
	// "Jobs and outbox". It is the transactional guarantee of an external effect.
	Outbox(context.Context, OutboxMessage) error
}

// Seeder writes trusted initial state: policies, assessments and historical
// versions. Only tests use it: the PostgreSQL store implements it in
// `internal/adapters/postgres/seed_test.go`, so no production path can seed.
// Enrollment writes policies (Enroll), CreateSuite creates Suites and
// RecordAssessment records assessments (M1.6a); proposals also have
// AppendProposalRevision (M1.3).
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
	Duplicate bool // the bootstrap was already recorded
}

type ConsentRequest struct{ Command contract.Command }

type ConsentResponse struct {
	Receipt   ConsentReceipt
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

The use cases are `ProcessConsent(ctx, UnitOfWork, ConsentRequest)`, `Bootstrap(ctx, UnitOfWork, PromoteRequest)` (existing-baseline only), `PromoteMerge` (see "Merge outcome") and, since M1.3, `ProposeBaseline` and `AssessHead`, which share the proposal revision rule (see "Proposal revisions"). `Promote`, `Correct` and `ReviseProposal` were removed in M1.10: no production path called them, and a promotion or a correction is now only a merge. Errors from the domain or the store are wrapped with `%w`, preserving the cause.

### Use-case semantics

- **Consent:** inside `Do`: look up `Receipt(operationID)`. A consent receipt for the same Suite, proposal, source command and actor is returned as a duplicate; any other receipt is `ErrOperationConflict`, except a consent receipt without its body, which is damaged stored state and `ErrInvalidState`. Then look up `ReceiptBySource(sourceCommandID)`: source command ids are global and the domain sees only one proposal's history, so a receipt there that is not a consent receipt for the same Suite, proposal and actor is `ErrOperationConflict` (a body-less one is `ErrInvalidState`), before the Suite or the proposal is read. The steps are `consentReceipts` (with `commandReceipt`) and `processConsent`. Then load the proposal and consent, apply the command with the governing policy, and:
  - `ConsentReasonCommandConflict` → `ErrOperationConflict`, nothing written, and checked before the next case;
  - a result whose duplicate flag disagrees with whether `ReceiptBySource` knew the source command → `ErrInvalidState` (history and receipts disagree);
  - a duplicate result (same source command, new operation id) → `AppendConsent` with `Alias: true` and the original receipt from `ReceiptBySource`;
  - otherwise build the receipt (eligibility from `Consent.HasApproval`; `PromotedVersionID` from `PromotionFor(reference)`) and `AppendConsent`.
- **Bootstrap:** inside `Do`: replay by `Receipt(operationID)` compares the stored identity with the request (same kind, reference, carrier, proposed contract, assessment source, integration, new version id); mismatch, including an operation id held by a consent command or a merge, is `ErrOperationConflict`. Otherwise load suite state, proposal, consent and assessment; decide in the domain. Only a `PromotionProposed` decision writes, through `RecordPromotion`, then the request's outbox. Blocked outcomes are returned without writing. A merge decides through the same function over `Tx` (see "Merge outcome"), replays by carrier, and corrects only the current version. There is no admission gate: proposals based on the same canonical version are all promotable, the first promotion wins, and the canonical compare-and-set blocks the others with `PromotionReasonCanonicalChanged` until they have a new revision against the new canonical and a fresh exact approval.

### Impossible states are errors

A state that no valid input reaches is an error, not a decision (the [nil-check rule](../development-guide.md#nil-checks)):
- `Consent.Apply` returns an error wrapping `contract.ErrInvalidConsent` when the proposal cannot answer a command's reference (`ErrInvalidReference` or `ErrInvalidProposal` from `Resolve` or `Lookup`), which a command built by `NewCommand` never causes. It never records that as a context mismatch. A reference of another carrier is a reachable `ConsentReasonContextMismatch`.
- `AssessIntegrity` returns an error wrapping `contract.ErrInvalidIntegrityAssessment` for evidence whose outcome is none of the three, which `NewIntegrityEvidence` never builds. It no longer answers "missing".
- `promoteWithin` returns `ErrInvalidState` for an operation kind other than bootstrap, promotion or correction.
- `ProcessConsent` returns `ErrInvalidState` for a consent receipt without its body, and when the consent history and the receipts disagree about a source command.

`scope.Declaration.Present` and `Raw`, and `contract.PromotionEffect.ExpectedCanonicalID` and `Suite`, are read only by tests and live in `export_test.go` files. Production code compares declarations by `scope.NewScope(declaration).Digest()`, which covers the presence byte and the raw bytes.

### Store write semantics

- `AppendConsent` and `RecordPromotion` start with `ConsentWrite.Validate(project, suite)` and `PromotionWrite.Validate(project, suite)` (`internal/application/governance/validate.go`), called by the PostgreSQL `Tx` and by `governancetest.Memory` alike, before they read or write anything. The refusals, in this order:
  - `ConsentWrite.Validate`:
    - `ErrInvalidRequest` when the write has no operation id or its result's command has none;
    - `ErrInvalidRequest` when the command's reference is not in the locked Suite;
    - `ErrOperationConflict` when `Result.Duplicate()` differs from `Alias`, or when it is not an alias and the write's operation id is not the command's;
    - `ErrInvalidRequest` when the receipt's result belongs to another source command.
  - `PromotionWrite.Validate`, all `ErrInvalidRequest`:
    - the record is zero or the version id is empty;
    - the record's reference or the version's project or Suite is not the locked Suite;
    - the receipt's request names another operation than the record;
    - the receipt identity's `CorrectsVersionID` or binding differs from the record's;
    - `RecordedAt` has finer than microsecond precision (the rule below).
- `AppendConsent` (not alias): insert the command result and an operation row carrying the receipt; bump the revision.
- `AppendConsent` (alias): insert only an operation row keyed by `ConsentWrite.OperationID` pointing at `Result.Command().SourceCommandID()` (never at `Result.Command().OperationID()`, which is the original operation), with a copy of the original receipt; bump the revision.
- `RecordPromotion`: verify the version's manifest bytes in the content store; insert the version, the promotion, and the operation row; set the Suite's current version; bump the revision. A duplicate version id or promoted reference is `ErrVersionConflict`.
- Time values are stored with microsecond precision (PostgreSQL `timestamptz`). `PromotionWrite.Validate` (and `MergeWrite.Validate` for a merge outcome) rejects a `RecordedAt` with finer precision as `ErrInvalidRequest` rather than truncating it, in every store, so callers building a `PromoteRequest` must truncate their clock to microseconds (`time.Now().UTC().Truncate(time.Microsecond)`).
- `Seed` stores the given state as is, including the Suite revision carried by `Canonical.Suite().Revision()`; it does not reset counters.
- `Do` must not commit when its context is canceled, even if `fn` returned nil; a `Tx` is unusable after `fn` returns.
- Reads reconstitute: proposals via `NewProposal` + `Revise` in sequence order; consent via `ReconstituteConsent` from stored results and aliases; assessments via `AssessIntegrity` over stored evidence; canonical and history via `NewSuite`, `NewSuiteVersion`, `NewPromotionRecord`, `NewCanonicalSnapshot`, `NewHistoricalCanonical`.

## Jobs and outbox (M1.2; normative)

Declared in `internal/application/governance/uow.go` next to `Tx`. River types never appear in `governance`.

```go
// Job is follow-up work for a background worker, enqueued with an outbox message
// outside any Suite by the store's EnqueueSystem. Args reference scoped records
// and immutable revisions; they never carry credentials.
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

- `Job.Validate()` and `OutboxMessage.Validate()` (in `internal/application/governance/queue.go`) apply the limits above. `OutboxMessage.Validate()` is shared by every implementation of `Tx`, and so are `ConsentWrite.Validate`, `PromotionWrite.Validate`, `AssessmentWrite.Validate` and `MergeWrite.Validate` (see "Store write semantics"), so the fake and the adapter reject the same inputs; `Job.Validate()` guards the system enqueue.
- `Outbox` inserts the message through the unit of work's own `pgx.Tx`. It commits or rolls back with the facts; there is no write after commit. It does not advance the Suite revision (the revision counts governance facts only) and takes no lock beyond the Suite's. There is no `Tx.Enqueue`: a job committed with a fact had no caller, and the outbox plus its relay carry the guarantee that a committed fact has its follow-up (removed in M1.10; see the amendment to [ADR 0005](../decisions/0005-river-background-jobs.md)).
- A malformed kind, a non-object or oversized `Args`/`Payload`, or an empty key is `ErrInvalidRequest`. A repeated `Key` is `ErrOperationConflict`; use cases replay by receipt before they write, so a repeated key means a genuine conflict.
- Retry limits, timeouts and backoff are runtime settings per kind (see [runtime](runtime.md)), not part of the port.
- A job's terminal state is River's `discarded` (retries exhausted) or `completed`; both stay in `river_job` and are counted by the runtime status endpoint until River's cleaner removes them. The runtime sets no retention, so River v0.49.0's defaults apply: completed and cancelled jobs are removed after 24 hours and discarded jobs after 7 days. The job counts of `/status` therefore describe the retained jobs, not the history.
- `outbox.project_id` and `outbox.suite_id` are both set (a message written through `Tx.Outbox`) or both null (a system message, for example the diagnostic `probe` written through the non-port `Store.EnqueueSystem`, which reuses the outbox insert statement and inserts a job in the same transaction without taking a Suite lock); a `CHECK` enforces it.
- The outbox row is the single owner of delivery state (it is not mirrored by a River job per message). A relay, itself a periodic River job that runs at startup and then on `OUTBOX_POLL_INTERVAL`, claims due rows with `FOR UPDATE SKIP LOCKED`, increments `attempts`, sets `next_attempt_at` to now plus `OUTBOX_LEASE` and commits; it then calls the publisher registered for the row's kind outside any transaction. An outcome update is conditional on the claim (`WHERE key = $1 AND state = 'pending' AND attempts = <attempts at claim>`): when a publish outlasts the lease and another relay has reclaimed the row, the late outcome updates nothing and is logged, never an error and never overwriting the newer claim. Success sets `state = delivered`. An error sets `last_error`; at `OUTBOX_MAX_ATTEMPTS` the state becomes `failed` (terminal, visible), otherwise `next_attempt_at` is now plus a capped exponential backoff. A crash between claim and outcome leaves the row pending until the lease expires, so it is redelivered. The relay's periodic job is unique while unfinished: after an unclean stop its next run waits until River rescues the stuck job (up to `JOB_TIMEOUT` plus 1 minute); the rows stay durable. A row whose kind has no registered publisher stays pending. Until M1.7 it failed at once; M1.7 changes that:
- the relay releases the claim, restoring the attempt, and defers the row by the backoff cap;
- it logs a Warn naming the kind, once per kind per process;
- so a target switched off GitHub, or a publisher registered later, never loses a message.

Retried delivery never reverses a committed fact.
- River schema: River's own migrations are vendored into goose migrations (one migrator and one readiness check, ADR 0026), so `migrations.SupportedVersion` is the highest goose version and covers the River tables and the outbox. A test checks that a database migrated by goose passes River's own migrator validation. Upgrading River means adding a goose migration.

The conformance suite gains: rollback removes the outbox row together with the facts; a duplicate key conflicts; a message written in a unit of work that writes nothing else is committed; it does not change the revision. The system enqueue is tested on PostgreSQL: a job and a message commit together or not at all, and an invalid job is `ErrInvalidRequest`.

## Proposal revisions (M1.3; normative)

The first write path for proposals outside `Seed`. Declared in `internal/application/governance/uow.go`; the `Seeder` comment then says that it writes trusted initial state, and no longer that it is the only writer of policies and assessments (enrollment, `CreateSuite` and `RecordAssessment` write them too, M1.6a).

```go
// ProposalWrite appends one revision to a proposal of the locked Suite. When
// it is the proposal's first revision, the proposal is created with the
// revision's carrier.
type ProposalWrite struct {
	Revision contract.ProposalRevision
}

var ErrProposalConflict = errors.New("proposal revision conflict")

type ReviseResult struct {
	Current   contract.ProposalReference // the proposal's current revision after the call
	Committed bool                       // false when nothing was written
}
```

The revision rule is one unexported function, `reviseWithin(ctx, tx, revision) (ReviseResult, error)`, that `AssessHead` and `ProposeBaseline` share, inside their own `Do`. There is no public use case for it (`ReviseProposal` was removed in M1.10: nothing called it). Both callers bind the revision to the Suite state they have just read under the lock (the locked project and Suite, the governing policy revision, and the Suite's current version as the expected canonical, none before bootstrap), so the function does not check that binding again. Its steps:

1. Load the proposal. `ErrNotFound` → `contract.NewProposal(revision)` and `AppendProposalRevision`.
2. When the current revision has the same proposal context (project, Suite, proposal) and carrier, and the same coverage (manifest digest, scope digest, covered inputs, expected canonical and policy revision; the origin and the revision id may differ), nothing is written and `Current` is the existing revision: repeated observation and implementation-only pushes create no revision (ADR 0013, D-APPROVAL-CONTEXT). Another carrier never takes this path.
3. Otherwise `Proposal.Revise(revision)`; its errors are wrapped with `%w` and nothing is written (another carrier is `contract.ErrProposalContextMismatch`, a reused revision id is `contract.ErrRevisionExists`). Then `AppendProposalRevision`.

A revision carries no operation id: identity is the revision id inside its proposal, and a retried request either matches step 2 or fails in step 3.

Store write semantics of `AppendProposalRevision`: insert the proposal row when it is absent, then the revision with the next sequence number of its proposal; bump the Suite revision once. A reference outside the locked Suite is `ErrInvalidRequest`. A revision id that already exists, or a carrier that differs from the stored proposal's, is `ErrProposalConflict` (the use case rules both out first, so either means a broken caller). Reads of the proposal inside the same unit of work see the new revision.

The conformance suite gains: the first revision creates the proposal; a second revision becomes current and older ones stay resolvable; a duplicate revision id and another carrier conflict; a foreign Suite is rejected; each append advances the revision by exactly one; rollback removes the proposal and its revisions. The use-case tests (through `ProposeBaseline` and `AssessHead`) add: an unchanged coverage writes nothing; the same coverage under another carrier is rejected, not ignored; a reused revision id with other coverage is rejected; and a proposal blocked by `PromotionReasonCanonicalChanged` is revised against the new canonical and promotes only after a fresh exact approval of the new revision (R1 used a stand-in proposal for this).

## Baseline proposals (M1.3; normative)

The existing-baseline bootstrap gets its proposal from an inventory of the pinned principal-branch commit (ADR 0010, ADR 0013). Declared in `internal/application/governance` (for example `baseline.go`); `governance` imports `internal/application/inventory`, never the reverse.

```go
// ContentWriter stores artifact bytes under their digest. It is idempotent:
// storing the same bytes again succeeds. filesystem.Store implements it.
type ContentWriter interface {
	Put(ctx context.Context, digest artifact.Digest, content io.Reader) error
}

type BaselineRequest struct {
	Reference contract.ProposalReference // the proposal and its new revision id
	Carrier   contract.ApprovalCarrierID // the PR that hosts the approval
	Origin    contract.SourceRevision    // the pinned principal-branch commit
	Tree      inventory.Tree             // that commit's source tree
}

type BaselineResult struct {
	Revise   ReviseResult               // as returned by the shared revision logic
	Contract contract.ProtectedContract // inventory.Contract(), to pass to Bootstrap as PromoteRequest.Proposed
}
```

Use case `ProposeBaseline(ctx, UnitOfWork, ContentWriter, BaselineRequest) (BaselineResult, error)`:

1. `inventory.Build(ctx, request.Tree)`; its errors are wrapped with `%w` (an invalid declaration or an unsafe entry writes nothing).
2. For every manifest entry, read the bytes from the tree, re-hash them, and `Put` them. A digest that does not match the manifest entry fails before that entry is stored and with nothing written to the database (entries stored before it stay in the store, as orphans). Content is stored before any database write: an orphan blob after a later failure is harmless, a version row without its bytes is not.
3. Inside one `Do`: the Suite must have no current canonical (otherwise `ErrInvalidRequest`: a baseline is only for bootstrap). Build the binding from the reference, `ExpectedCanonical` empty, the inventory's manifest digest and scope digest, the governing policy revision, and covered inputs `{}`; `contract.NewProposalRevision(binding, Origin, Carrier)`. Then apply `reviseWithin` (see "Proposal revisions"), which `AssessHead` shares, so a repeated baseline with the same coverage writes nothing.

There are no new `Tx` methods and no migration. The first canonical is then created by `Bootstrap` with `Integration` of kind existing baseline and `Proposed = BaselineResult.Contract`, after approval and integrity assessment as before.

Tests: the full path on the in-memory fake (tree, `ProposeBaseline`, approval through `ProcessConsent`, a seeded assessment, `Bootstrap`, a canonical whose manifest equals the inventory's); a repeated call with the same tree writes nothing; a changed protected file creates a new revision; an existing canonical is rejected; a failing or mismatching `Put` writes nothing. On PostgreSQL with the filesystem store: the inventory's revision and assessment are seeded (`RecordAssessment` records assessments since M1.6a, but this test seeds its own), `ProposeBaseline` on the same tree returns the seeded reference with `Committed` false and still stores the bytes, and `Bootstrap` succeeds, so `RecordPromotion` verifies the bytes `ProposeBaseline` wrote.

## GitHub observations (M1.4; normative)

What synchronization saw on GitHub: evidence for later phases, never authority. These writes are not governance facts: they do not go through `Tx`, do not take a Suite lock, do not advance any Suite revision (principle 4 covers governance facts only) and are not linked to a project or Suite. Rows are keyed by repository `owner/name` and installation id; the trusted association between an installation, a repository and a Suite is D-IDENTITY's (M1.5). Migration `00011_github_observations.sql`; `SupportedVersion` becomes 11.

The port lives in `internal/application/githubsync` (not `sync`, which would shadow the standard library) with an in-memory fake and a conformance suite run against both the fake and PostgreSQL:

```go
// Cycle is one complete, successful cycle of one repository: every page of
// every listing in it succeeded.
type Cycle struct {
	Repo           string
	InstallationID int64
	At             time.Time            // observation time, from the injected clock
	OpenPulls      []ObservedPull       // the complete open listing
	Comments       map[int][]ObservedComment // for each open pull number, its complete comment listing
}

type ObservedPull struct {
	Number                   int
	HeadSHA, HeadRef         string
	BaseRef, BaseSHA         string
	UpdatedAt                time.Time
}

type ObservedComment struct {
	CommentID              int64
	AuthorID               int64
	AuthorLogin            string
	CreatedAt, UpdatedAt   time.Time
	Body                   string
}

type FailureKind string // rate_limited, budget_exhausted, unauthorized, forbidden, not_found, unprocessable, status, transport, decode

type Failure struct {
	Repo           string
	InstallationID int64
	At             time.Time
	Kind           FailureKind
	Status         int       // the HTTP status for kind status (and the typed kinds), else 0
	WaitUntil      time.Time // set for rate_limited and budget_exhausted
}

type Store interface {
	// Repository returns the stored state, or found false before its first cycle.
	Repository(ctx context.Context, repo string) (RepositoryState, bool, error)
	// RecordCycle stores one finished cycle of one repository atomically: the
	// repository state, the open pulls seen, the pulls no longer open, and the
	// comment versions seen. A repeated call with the same observations changes nothing.
	RecordCycle(ctx context.Context, cycle Cycle) error
	// RecordFailure stores a failed cycle (its classification and wait-until) without touching observations.
	RecordFailure(ctx context.Context, failure Failure) error
	Pulls(ctx context.Context, repo string) ([]PullState, error)
	Comments(ctx context.Context, repo string, number int) ([]CommentVersion, error)
}

type RepositoryState struct {
	Repo           string    // owner/name
	InstallationID int64
	LastSuccess    time.Time // zero before the first successful cycle
	LastError      FailureKind // empty after a successful cycle; a fixed classification, never text GitHub sent
	LastStatus     int
	WaitUntil      time.Time // zero when not waiting (from RateLimitedError.Until or BudgetExhaustedError.Reset)
}

type PullState struct {
	Number                    int
	HeadSHA, HeadRef          string
	BaseRef, BaseSHA          string
	UpdatedAt                 time.Time // GitHub's
	FirstSeen, LastSeen       time.Time // observation times
	NotOpenSince              time.Time // zero while open; set at the first cycle the PR was absent from the open list
}

type CommentVersion struct {
	PullNumber  int
	CommentID   int64
	AuthorID    int64
	AuthorLogin string
	CreatedAt   time.Time // GitHub's; never changes for an id
	UpdatedAt   time.Time // GitHub's; a new value is a new version (an edit)
	Body        string
	BodyDigest  artifact.Digest
	FirstSeen   time.Time
	NotSeenSince time.Time // zero while the comment appears in its PR's full listing; set at the first full listing without it
}
```

- **Complete listings only.** A cycle is recorded only when every page of every listing in it succeeded; otherwise the reconciliation records a `Failure` and no observation changes. The store computes the absence marks from the complete sets of the `Cycle`.
- **Serialized per repository.** `RecordCycle` and `RecordFailure` lock the repository's `github_repositories` row (inserting it on first use) for the whole transaction and compute the marks inside it, so two cycles of the same repository (a tick and an MCP-requested sync) never mark or clear from a stale view; the later commit wins consistently.
- **Pulls:** upserted by `(repo, number)`; the head, refs, `updated_at` and `last_seen` change, `first_seen` never does. A pull absent from the cycle's open listing gets `not_open_since` (the cycle does not tell merged from closed: since M1.7 the merges job resolves the absence, see "Merges"); a pull that reappears clears it.
- **Comments are append-only versions** keyed by `(repo, comment_id, updated_at, body_digest)`: an edit adds a version, the earlier body stays (two edits within one second of GitHub's `updated_at` are still two versions; the latest version is the one with the largest `updated_at`, ties broken by `first_seen`), so an approval comment edited after it was processed remains visible as such. `created_at` must be equal across versions of one id (a different one is an error and the cycle is not recorded). Absence is judged only within a pull listed in the cycle: a comment missing from its open pull's listing gets `not_seen_since` on its latest version, and reappearing clears it. The comments of a pull that is not open in the cycle are left untouched. A deletion is never a row removal. The body is stored as raw bytes (`bytea`), whatever they contain (a NUL or invalid UTF-8 never fails a cycle), with `body_digest` = `artifact.Hash` of those bytes.
- **Errors:** an invalid `Cycle` or `Failure` (failing `Validate`, or a pull number outside 1..2^31−1 in a read) wraps `githubsync.ErrInvalidObservation`; a `created_at` that differs across versions of one id wraps `githubsync.ErrCreationTimeChanged`; both stores return the same sentinels. `FailureKind` values are the stored codes, enumerated by a `CHECK` constraint.
- **Failure classification:** `last_error` is the `FailureKind` (from the `rest` and `github` errors: `ErrRateLimited`, `ErrBudgetExhausted`, `ErrUnauthorized`, `ErrForbidden`, `ErrNotFound`, `ErrUnprocessable`, another status, a transport error, a decode error) with the status where there is one; never error text, so nothing GitHub sent and no secret is stored.
- **Listing:** each cycle lists the open pulls and, for each open pull, its comments in full (conditional GETs, so an unchanged listing costs a 304 and nothing on GitHub's budget). No `since` and no local clock are used for what to fetch; observation times (`first_seen`, `last_seen`, `not_*_since`) come from the injected clock and are never compared with GitHub's times.
- **Failures:** a cycle that fails records `last_error` and, for a rate limit or exhausted budget, `wait_until`, without touching observations. The next cycle of that repository starts no earlier than `wait_until`, across restarts.

Tables (observation state, outside the governance immutability rules except where stated): `github_repositories` (repo, installation_id, last_success, last_error, last_status, wait_until), `github_pulls` (repo, number, head_sha, head_ref, base_ref, base_sha, updated_at, first_seen, last_seen, not_open_since), `github_comment_versions` (repo, comment_id, updated_at, pull_number, author_id, author_login, created_at, body, body_digest, first_seen, not_seen_since; a trigger rejects every `UPDATE` that changes a column other than `not_seen_since`, and `DELETE` and `TRUNCATE`).

The conformance suite (fake and PostgreSQL) covers: a first cycle creates the state; a repeated identical cycle changes nothing; a head change updates the pull and keeps `first_seen`; a pull missing from the open listing gets `not_open_since` and reappearing clears it; an edit adds a version and keeps the earlier body; a different `created_at` for an id is an error and records nothing; a comment missing from its open pull's listing gets `not_seen_since`, and the comments of a pull no longer open are untouched; a failure records its kind, status and wait-until without touching observations, and a later success clears the error and the wait; two concurrent `RecordCycle` calls of one repository leave a state equal to one of them applied last. PostgreSQL adds the trigger rejections.

## Owner, TOTP and protection results (M1.5; normative)

Package `internal/application/owner` declares the port; the fake `ownertest.NewMemory` and the conformance suite `ownertest.RunConformance` live in `internal/application/owner/ownertest`; the PostgreSQL adapter implements it in migration `00012` (`SupportedVersion` 12). These are trusted local writes made by `setup` and the protection job, not governance facts carried by `Tx`, except that the policy revisions written here are the policies governance reads.

```go
package owner

type Owner struct {
	AccountID int64  // GitHub numeric account id, > 0
	Login     string // display only
}

type ProjectPolicy struct {
	RepositoryID int64  // GitHub numeric repository id, > 0; the project is contract.ProjectID("github-repo:<id>")
	Repo         string // owner/name, display only
	TestCheck    string // the repository's required project test check, non-empty, at most 200 bytes
}

type Enrollment struct {
	Owner      Owner
	Secret     Secret          // the TOTP secret, exactly 20 bytes (totp.SecretBytes); Secret is a []byte that never renders
	Projects   []ProjectPolicy // one per configured repository, distinct ids, at least one
	Step       int64           // the TOTP step verified during setup; consumed in the same transaction
	Tier       string          // "isolated" or "co-located": the INSTALLATION_TIER at enrollment
	EnrolledAt time.Time
}

type State struct {
	Owner          Owner
	Secret         Secret
	Projects       []ProjectPolicy
	Tier           string
	PolicyRevision contract.PolicyRevisionID // the governing revision of every project, "setup-<n>"
	EnrolledAt     time.Time
}

type ProtectionResult struct {
	RepositoryID int64
	Result       string   // "enforced", "findings", "enforcement-unavailable" or "unknown"
	Findings     []string // D-PROTECTION finding codes, sorted, empty unless Result is "findings"
	Caveats      []string // D-PROTECTION caveat codes, sorted
	CheckedAt    time.Time
}

var (
	ErrStepUsed     = errors.New("owner: TOTP step already used")
	ErrInvalidState = errors.New("owner: invalid enrollment or result")
	ErrNotEnrolled  = errors.New("owner: no owner enrolled")
)

type Store interface {
	// Enroll replaces the owner and the secret and appends, per project, a policy revision owned by
	// contract.PrincipalID("github:<AccountID>") of kind Human with revision id "setup-<n>" (n is the enrollment
	// generation: 1, then previous+1), records each project's test check and the tier, points the enrollment at the
	// new revision, and consumes Step for the new owner, in one transaction. An invalid enrollment, or a project
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
```

Rules:

- **One owner per instance.** `owner_enrollment` has at most one row; `Enroll` replaces it and the secret. Earlier policy revisions stay (policies are immutable); the new revision is the governing one for each project from then on, and pending consent bound to an earlier revision no longer matches (`contract.NewApprovalBinding` includes the policy revision).
- **Consumed steps are never purged in the MVP.** A command is verified against its comment's `created_at` and may be processed long after (an outage, a wait, a restart catch-up), so a time-based purge could let a code be used twice; the rows are small.
- **The secret is stored as bytes inside the instance** and is never logged, returned by `/status`, MCP or an error. Encryption at rest is not part of the MVP; the isolated tier keeps the database out of the agent's reach (ADR 0027).
- **Protection results** are the latest per repository; they are status, not evidence, and are replaced on each verification. Findings and caveats are strictly increasing (sorted, no duplicates) and come back as empty, non-nil lists.
- **Validation** also rejects a zero `EnrolledAt`, a zero `at` in `ConsumeStep` and a zero `CheckedAt` with `ErrInvalidState`. `Secret` redacts itself in every `fmt` verb, `encoding.TextMarshaler`, JSON and `slog`. The result, finding and caveat codes are the constants of `internal/application/protection`.

Tables (`00012_owner.sql`): `owner_enrollment` (singleton key, generation, account_id, login, secret bytea of exactly 20 bytes, tier, policy_revision_id, enrolled_at), `owner_test_checks` (repository_id, project_id, repo, test_check, policy_revision_id; FK to `policies`), `totp_consumed_steps` ((account_id, step) primary key, consumed_at; insert-only, no delete), `github_protection_results` (repository_id primary key, result, findings text[], caveats text[], checked_at). Conformance names: enroll and read back; re-enroll replaces the secret, increments the generation and appends policy revisions; re-enroll with another owner while a project has a Suite is rejected; an invalid enrollment writes nothing; a used step is rejected by `Enroll` and by `ConsumeStep`; `ConsumeStep` without an owner; concurrent `ConsumeStep` of one step (exactly one wins, PostgreSQL); protection result replaced and listed.

## PR commands and bootstrap (M1.6a; normative)

Implements D-COMMANDS. Identifiers follow its table.

### Governance writes (`internal/application/governance`)

```go
// UnitOfWork gains:
// CreateSuite creates a Suite with no canonical, governed by policy (which must exist for the project) and
// targeting target. It is idempotent: an existing Suite with the same policy and target is success and writes
// nothing; an existing Suite with another policy or target is ErrConflict.
CreateSuite(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, policy contract.PolicyRevisionID, target contract.IntegrationTargetID) error

// Tx gains:
// RecordAssessment stores an integrity assessment for a proposal revision of the locked Suite at a source.
// Assessments are immutable: the same (reference, source) with the same evidence is success and writes nothing;
// with other evidence it is ErrConflict.
RecordAssessment(ctx context.Context, w AssessmentWrite) error

type AssessmentWrite struct {
	Reference contract.ProposalReference
	Source    contract.SourceRevision
	Evidence  contract.IntegrityEvidence // emitted by the service principal "suiteward" (kind Service)
}
```

- **Outbox inside the use case.** `ConsentRequest`, `BaselineRequest` and `PromoteRequest` gain `Outbox []OutboxMessage`. `ProcessConsent`, `ProposeBaseline` and `Bootstrap` write it inside their own transaction, after their facts (only `Bootstrap` uses `PromoteRequest.Outbox`; a merge writes its message through `MergeRequest.Message`). Nothing is written when there are no new facts: a duplicate consent (replay by operation id or source), a baseline with unchanged coverage, or a bootstrap replay. Because the message is written in the same transaction as its facts, an acknowledgment never reports what did not commit (ADR 0011). The payloads are defined in the [runtime contract](runtime.md#command-and-bootstrap-jobs-m16a) (outbox kind `github_comment`).
- **Outcomes.** The evidence outcome is:
  - `Passed` when the protected inventory of the exact source equals the revision's binding (manifest and scope digests);
  - `Failed` when it differs;
  - `Unavailable` when the source cannot be read or is invalid (a truncated tree, a rejected link, an invalid declaration).
- **Bootstrap.** The bootstrap records the assessment for the origin, then calls `Bootstrap`.

### Command ledger (`internal/application/commands`, migration `00013`)

The ledger records the first observed version of every PR comment SuiteWard considered (D-COMMENT-EDIT). It is the record of that decision. It is not the authority: consent lives in the governance tables.

```go
package commands

type Disposition string // the codes below

const (
	NotCommand      Disposition = "not-command"     // no /suiteward line, SuiteWard's own comment, or an approve, revoke or skip first observed after its pull request stopped being open (no step used, never acknowledged)
	BeforeEnrollment Disposition = "before-enrollment" // created before the current enrollment's enrolled_at; never acknowledged
	NoSuite         Disposition = "no-suite"        // owner-authored command for a project whose Suite does not exist yet
	EditedFirst     Disposition = "edited-first"    // owner-authored, first observed version already edited (updated_at != created_at); no step used
	Failed          Disposition = "failed"          // a candidate that kept failing (bounded retries, logged); never acknowledged
	IgnoredAuthor   Disposition = "ignored-author"  // a /suiteward command from an account other than the owner
	Malformed       Disposition = "malformed"       // owner-authored, unparseable command line
	InvalidCode     Disposition = "invalid-code"    // owner-authored, code does not verify within the tolerance
	ReplayedCode    Disposition = "replayed-code"   // owner-authored, the step was used by another command
	UnknownProposal Disposition = "unknown-proposal" // owner-authored, valid code, for a proposal that does not exist on an existing Suite; the step stays consumed
	Processed       Disposition = "processed"       // passed to ProcessConsent; Receipt holds the outcome
	OtherPull       Disposition = "other-pull"      // owner-authored approve or revoke for a proposal that another pull request carries; refused before its step is used (migration 00020); acknowledged, no operation id, never counted by LatestOwnerCommand
)

type Entry struct {
	RepositoryID int64
	CommentID    int64
	PullNumber   int
	AuthorID     int64
	CreatedAt    time.Time
	BodyDigest   artifact.Digest // of the first observed version
	Disposition  Disposition
	Operation    contract.OperationID // set when Processed
	DecidedAt    time.Time
}

type Store interface {
	// Pending returns the first observed version of every comment that has no ledger entry, across all enrolled
	// repositories, ordered by created_at then comment id, at most limit, and only comments created before
	// horizon. The caller computes horizon from the sync watermark (D-COMMANDS): the earliest last successful
	// cycle among the enrolled repositories, minus JOB_TIMEOUT and 30 s.
	Pending(ctx context.Context, horizon time.Time, limit int) ([]Candidate, error)
	// Record stores the decision for one comment and, when ack is not nil, its acknowledgment outbox message in
	// the same transaction (for dispositions that never reach ProcessConsent). That message is a system outbox
	// row with no project or Suite, written the way EnqueueSystem writes the probe's. A second Record of the same
	// (repository, comment) with the same entry is success and writes nothing; with another entry it is
	// ErrConflict. Entries are never updated or deleted.
	Record(ctx context.Context, e Entry, ack *governance.OutboxMessage) error
	// Instance returns this installation's random identifier, created by migration 00013 and never changed.
	Instance(ctx context.Context) (string, error)
}

type Candidate struct { // the first observed version of one comment
	RepositoryID int64
	Repo         string // owner/name, for the GitHub calls
	PullNumber   int
	Comment      githubsync.CommentVersion
}
```

- **Order of writes for an owner command.**
  1. `ConsumeStep`, which is idempotent for the same source.
  2. `ProcessConsent` with the acknowledgment in `Outbox`. It is idempotent by operation id, and a duplicate writes no message.
  3. `Record(processed)`.

  `edited-first` is recorded like a rejected disposition, with an acknowledgment, and uses up no step.

  A crash between any two steps repeats the earlier steps harmlessly on the next run. Rejected dispositions (`malformed`, `invalid-code`, `replayed-code`, `no-suite`, `unknown-proposal`, `other-pull`, `edited-first`) go through `Record` with the acknowledgment. `not-command`, `ignored-author`, `before-enrollment` and `failed` record nothing to publish.
- **Keys.** Observations are keyed by `owner/name` (M1.4) and projects by repository id. `Pending` joins them through the enrolled projects (`owner.State.Projects`), so a repository that is not enrolled has no candidates.

### Consumed steps bound to their command

- **Schema.** Migration `00013` adds `source_command_id` (text, not null) to `totp_consumed_steps`. Existing rows take `setup:<generation>` from the enrollment that consumed them.
- **Signature.** `owner.Store.ConsumeStep` becomes `ConsumeStep(ctx, accountID, step int64, at time.Time, source contract.SourceCommandID) error`.
- **Same command.** If the step is already consumed by the same source, the call succeeds and writes nothing. That makes a retry after a crash safe.
- **Another command.** If the step is consumed by any other source, the call returns `ErrStepUsed`.
- **Setup.** `Enroll` consumes its step with the source `setup:<generation>`.

### Tables (`00013`)
- **`pr_commands`.**
  - Primary key: (repository_id, comment_id).
  - Columns: pull_number, author_id, created_at, body_digest, disposition (CHECK on the codes), operation_id (non-null exactly when the disposition is `processed`), decided_at.
  - The table is insert-only.
- **`suiteward_instance`.** A singleton table holding the random instance id (128 bits, hex). It is created once by the migration and never updated.
- **`totp_consumed_steps.source_command_id`.**

### Conformance
- `Pending`:
  - ordering;
  - only first versions;
  - not-enrolled repositories excluded;
  - already-recorded comments excluded.
- `Record`:
  - idempotent;
  - a conflicting entry is rejected.
- `ConsumeStep`:
  - same source is idempotent;
  - another source gives `ErrStepUsed`;
  - concurrent consumption by two sources: exactly one wins.
- `CreateSuite`:
  - idempotent;
  - a conflicting Suite is rejected.
- `RecordAssessment`:
  - idempotent;
  - conflicting evidence is rejected;
  - readable by `Tx.Assessment`;
  - `Bootstrap` succeeds after it.
- `Instance`: stable across calls and restarts.

## PR checks (M1.6b; normative)

Implements the D-CHECKS amendment of 2026-10-14.

### Head assessment (`internal/application/governance`)

```go
// ContentReader reads artifact bytes by digest. filesystem.Store implements it.
type ContentReader interface {
	Read(ctx context.Context, digest artifact.Digest) ([]byte, error)
}

type HeadRequest struct {
	Project contract.ProjectID
	Suite   contract.SuiteID
	Carrier contract.ApprovalCarrierID // github-pr:<repository id>/<pull>
	Head    contract.SourceRevision    // the PR head SHA
	Tree    inventory.Tree             // that commit's source tree
}

type HeadResult struct {
	Change    inventory.Change            // Unchanged, ProtectedChange or ScopeReduction
	Reference *contract.ProposalReference // the PR's current revision; nil when Unchanged
	Ready     bool                        // CheckPromotionReadiness answered ready for Head
	Reason    contract.PromotionReason    // when not Ready
	Canonical contract.SuiteVersionID
	Revision  int64                       // the Suite revision the result was computed at
}

// AssessHead classifies Head against the canonical and, for a protected change, keeps the PR's proposal
// current, records the head's integrity assessment and evaluates readiness for Head.
func AssessHead(ctx context.Context, uow UnitOfWork, reader ContentReader, writer ContentWriter, request HeadRequest) (HeadResult, error)
```

- **Idempotent.** Repeating `AssessHead` for the same head writes nothing new: unchanged coverage creates no revision and an identical assessment is not stored twice. The check job therefore calls it whenever a governing fact may have changed; a separate verdict-only call is not needed (a revision stores only its manifest digest, so readiness needs the head's inventory anyway).
- **Stale canonical or policy.** `AssessHead` always binds the revision to the current canonical and policy, so a changed canonical or policy shows as a new revision awaiting approval (`approval-missing`).
- **No canonical.** It returns `ErrNoCanonical` (new sentinel). No proposal is created before a canonical exists (D-CHECKS).
- **Governing scope.** Rebuilt from the canonical: the `.suiteward.yml` bytes named by the canonical manifest are read through `ContentReader` (no entry means `scope.NoDeclaration()`), parsed, and their scope digest must equal the canonical binding's; a mismatch is `ErrInvalidState`.
- **Classification.** `inventory.Classify(governing, Tree)`. An invalid tree, an unsafe entry or an invalid declaration is returned wrapped (`inventory.ErrInvalidTree`, `ErrUnsafeEntry`, `scope.ErrInvalidDeclaration`) and maps to `failure`, except a tree GitHub truncated: the reader then also wraps `github.ErrTreeTruncated`, a GitHub limit rather than the owner's source, which maps to `in_progress`. `Unchanged` returns at once, with no write.
- **The PR's proposal.** Inside one `Do`:
  - The PR's change proposal is the proposal whose carrier is `Carrier`, whose revisions expect a non-empty canonical (not a baseline) and that has no promotion. `Tx.ChangeProposalFor(ctx, carrier)` finds it. The bootstrap carrier PR therefore gets a new proposal for a protected change.
  - Without one, a new id is allocated with `Tx.NextProposalID(ctx)` (`P<max+1>`), under the Suite lock.
  - Its revision is kept current with `reviseWithin`: binding (manifest digest, scope digest, `{}` covered inputs, expected canonical = the current version, policy = the governing policy), origin `Head`, carrier `Carrier`. Unchanged coverage writes nothing, so an implementation-only push keeps consent.
  - The content bytes are stored before `Do`, as in `ProposeBaseline`.
- **Assessment.** `RecordAssessment` of the current revision at `Head`, emitted by `ServiceEmitter`: `Passed` when the head's protected inventory under the revision's scope equals the binding, `Failed` otherwise. The rule is the shared `HeadEvidence` function, also used by the bootstrap.
- **Verdict.** `contract.CheckPromotionReadiness` with `requiredSource = Head`.
- **Canonical moved during the call.** `AssessHead` reads the tree outside the Suite lock, so the canonical can move before its write; it then returns `ErrCanonicalMoved` (an `ErrConflict`, so `errors.Is(err, ErrConflict)` still matches) and writes nothing. The job retries it on its next run and tells it apart from an assessment conflict.

### Check ledger (`internal/application/checks`, migration `00014`)

```go
package checks

type State string // "success", "in_progress", "failure"

type Result struct {
	RepositoryID int64
	PullNumber   int
	HeadSHA      string
	State        State
	Reason       string // a stable code, see the runtime contract
	Reference    string // "P<n>-R<m>" or empty
	Change       string // the classification code, or empty when not assessed
	Revision     int64  // the Suite revision of the verdict, 0 without a Suite
	CommandID    int64  // the latest owner command on the pull that was considered, 0 for none
}

type Record struct {
	Result
	HeadSince  time.Time // when this head was first recorded
	Generation int64     // > 0
	DecidedAt  time.Time
}

type Store interface {
	Current(ctx context.Context, repositoryID int64, pull int) (Record, bool, error)
	// Publish stores r for its pull. When the head, State, Reason or Reference differ from the current record
	// (or there is none) it advances the generation and writes message(generation) to the outbox in the same
	// transaction, as a system row. Otherwise it updates Change, Revision, CommandID and DecidedAt only and
	// writes no message. It also advances the generation and writes a message when the current generation's
	// message ended `failed` in the outbox, at least 15 minutes after that message finished (M1.7), so a lost publication is retried without unbounded republication. It returns the stored record and
	// whether a message was written.
	Publish(ctx context.Context, r Result, at time.Time, message func(generation int64) (governance.OutboxMessage, error)) (Record, bool, error)
}
```

- **Table `pr_checks`.** Primary key (repository_id, pull_number). The row is current state, updated in place; the publications themselves are the outbox rows. `generation` only grows; `state` is a CHECK on the three codes; `head_sha` is 40 lowercase hex.
- **Owner commands.** `commands.Store` gains `LatestOwnerCommand(ctx, repositoryID int64, pull int, ownerID int64, before int64) (Entry, bool, error)`: the owner-authored ledger entry with the greatest comment id below `before` (0 means no bound) on that pull among `invalid-code`, `replayed-code` and `processed` (amended 2026-10-19, D-COMMANDS; until then also `malformed`, `unknown-proposal`, `edited-first` and `no-suite`). `not-command`, `failed` and `before-enrollment` never count, so an ordinary comment never clears an owner-command `failure`. A negative `before` is invalid. The checks job walks back with `before` past a processed command whose consent result is rejected, so a wrong command neither sets nor clears the failure. No migration: 00019's header comment still names the old set.
- **Conformance.**
  - Publish: the first record writes generation 1 and one message; an identical result writes none; a new head, state, reason or reference advances the generation by exactly one; concurrent publishes for one pull never write two messages for one generation; a failing message rolls the record back.
  - Publish with an identical result after the current message failed: generation advances by one with a new message.
  - `LatestOwnerCommand`: ordering, the author filter, and a rejected command followed by an owner `not-command` comment is still the latest command.

## Merges (M1.7; normative)

Implements D-INTEGRATION (2026-10-15) and ADR 0014. Migration `00015` adds every table and column below. The three parts are written by different tasks.

### Merge facts (`internal/application/githubsync`)

The resolution of a pull's absence is observation state: evidence, not authority. The merges job is its only writer.

```go
type ObservedPull struct { /* M1.4 fields */ HeadRepo string } // full_name, or "(deleted)" when GitHub reports none

type Resolution struct {
	Number         int
	NotOpenSince   time.Time // the absence it resolves; must equal the stored not_open_since
	Merged         bool
	MergedAt       time.Time // GitHub's; set exactly when Merged
	MergeCommitSHA string    // 40 lowercase hex, or "" (only when Merged and the bound ran out)
	HeadSHA, HeadRef, BaseRef, BaseSHA, HeadRepo string // as read at resolution
	ResolvedAt     time.Time // injected clock
}

type MergedPull struct {
	PullState  // with HeadRepo and the refreshed head and base
	Resolution
	Processed bool // MarkProcessed was called
}

// Store gains:
//   AwaitingResolution(ctx, repo) ([]PullState, error)  // not open, and the current absence is unresolved
//   RecordResolution(ctx, repo string, r Resolution) error
//   RecordClosed(ctx, repo string, closed []ClosedPull, at time.Time) error
//   Merged(ctx, repo) ([]MergedPull, error)           // every pull resolved as merged, by MergedAt then Number
//   MarkProcessed(ctx, repo string, number int) error
type ClosedPull struct {
	ObservedPull
	MergedAt time.Time // zero for a closed, unmerged pull
}
```

- **Head repository.** `RecordCycle` stores `ObservedPull.HeadRepo` in `head_repo`, as it does the head. Rows from before M1.7 hold NULL, read back as `""`: not observed yet, which is not a fork. The first cycle fills it.
- **Resolution.**
  - `RecordResolution` writes only while the pull's `not_open_since` equals `r.NotOpenSince` and the pull is not open. Otherwise it returns `ErrStaleResolution` (new sentinel) and writes nothing, because the pull reopened or is gone again.
  - It sets `resolved_for = not_open_since`, the merge facts and `resolved_at`. It refreshes the head and base columns and `head_repo` from the read, because a push between the last cycle and the merge is not otherwise seen.
  - A pull that reopens is upserted by the cycle, which clears `not_open_since`. A later absence is then unresolved again (`resolved_for <> not_open_since`). A merged pull cannot reopen.
- **Closed listing.**
  - `RecordClosed` keeps a per-repository watermark (`github_repositories.closed_watermark`), which is GitHub's greatest `updated_at` seen in the listing; the first call sets it one microsecond past that value instead.
  - **The first call is exclusive.** The first call for a repository only sets the watermark, one microsecond past its newest item. What it listed is history and is never inserted later, so a merge from before enrollment is not backfilled. A merge in the exact moment of that newest item that is listed only later is history too.
  - A later call inserts each merged item with `updated_at` at or above the watermark (inclusive from the second call on: a pull merged in the same second as the item that raised the watermark is not lost, and a seen pull is left alone) and no `github_pulls` row: a new row with `first_seen = last_seen = not_open_since = at` and no resolution, which the merges job then resolves with `Reader.Pull`. Then it raises the watermark.
  - Existing rows and unmerged items are left alone.
  - Times from GitHub are compared only with times from GitHub.
- **Processed.** `MarkProcessed` sets `merge_processed`.
  - It is a read optimization. The merge outcome in the governance store (below) is the authority, and a repeated outcome is a replay.
  - A failure to mark only costs a replay on the next run.
- **Serialization.** These writes lock the repository row like `RecordCycle`.
- **Conformance.**
  - Absence, then resolution, then reopen, then a new absence that is unresolved.
  - A stale resolution writes nothing.
  - The closed listing: first call sets an exclusive watermark; an item it listed, or one at its newest moment listed only later, is never inserted; an unseen merged item above the watermark is inserted unresolved; a seen pull and an item below the watermark are ignored; on a later call an item at exactly the current watermark is inserted.
  - `Merged` ordering and the `Processed` flag.
  - The head repository round-trips.

### Merge outcome (`internal/application/governance`)

```go
type MergeKind string // "promoted", "corrected", "no-change", "not-target", "before-canonical", "blocked"

type MergeReason string // blocked only. Use case: "unapproved-change", "invalid-source", "assessment-conflict", and the mapped
                        // promotion reasons below. Caller: "fork-unsupported", "shared-merge-commit",
                        // "integration-unreadable", "integrity-failed".

type MergeOutcome struct {
	Kind      MergeKind
	Reason    MergeReason
	Source    contract.SourceRevision     // the merge commit, "" when unreadable
	Version   contract.SuiteVersionID     // promoted or corrected: the new version; otherwise the canonical kept
	Corrects  contract.SuiteVersionID     // corrected only
	Reference *contract.ProposalReference // the PR's change proposal revision, when there is one
}

type MergeRequest struct {
	Project    contract.ProjectID
	Suite      contract.SuiteID
	Carrier    contract.ApprovalCarrierID   // github-pr:<repository id>/<pull>
	Source     contract.SourceRevision      // the merge commit; "" only with Refusal integration-unreadable
	Target     contract.IntegrationTargetID // branch:<base ref at the merge>
	ObservedAt time.Time                    // the pull's not_open_since (SuiteWard's clock)
	Tree       inventory.Tree               // the merge commit's tree; nil with a Refusal
	Refusal    MergeReason                  // a caller-decided blocked reason, or ""
	RecordedAt time.Time                    // microsecond precision
	// Message builds the outcome comment for the outcome. false writes no message.
	Message func(MergeOutcome) (OutboxMessage, bool, error)
}

type MergeResult struct {
	MergeOutcome
	Duplicate bool // the outcome was already recorded
}

func PromoteMerge(ctx context.Context, uow UnitOfWork, writer ContentWriter, request MergeRequest) (MergeResult, error)
func MergeOutcomeOf(ctx context.Context, uow UnitOfWork, project contract.ProjectID, suite contract.SuiteID, carrier contract.ApprovalCarrierID) (MergeOutcome, bool, error)
```

`PromoteMerge` records exactly one outcome per merged PR (carrier). The outcome row and its message are written in the same transaction as any promotion.

1. **Before the transaction**, unless there is a `Refusal`:
   - run `inventory.Build(ctx, Tree)`;
   - an invalid tree, an unsafe entry or an invalid declaration becomes the refusal `invalid-source`;
   - a read error is returned wrapped, and nothing is written. A truncated tree must not reach `PromoteMerge`: the GitHub reader wraps `ErrInvalidTree` with `ErrTreeTruncated`, and governance cannot tell them apart, so the caller handles truncation first (the merges job reads the tree's entries before calling);
   - store the inventory's bytes through `writer`, as `ProposeBaseline` does. The unexported `storeContent` is reused.
2. **Inside one `Do(project, suite)`, in order:**
   1. **Replay.** If `Tx.MergeOutcome(carrier)` finds an outcome, return it with `Duplicate`. The request is not compared, because a PR merges once.
   2. **No canonical.** Return `ErrNoCanonical` and write nothing.
   3. **Not the target.** `Target` differs from the Suite's target: `not-target`.
   4. **Before the canonical.** `ObservedAt` is before the `RecordedAt` of V1's promotion: `before-canonical`. That merge is part of, or precedes, the bootstrap baseline.
   5. **Refusal.** `blocked` with the refusal reason.
   6. **No change.** The integrated contract equals the canonical (`ClassifyContractChange` unchanged): `no-change`.
   7. **No proposal.** `Tx.ChangeProposalFor(carrier)` finds none: `blocked`, `unapproved-change`.
   8. **Otherwise:**
      - record the assessment of the proposal's current revision at `Source` with `HeadEvidence`. An `ErrConflict` (another assessment is stored at that source) is deterministic, so it does not fail the call, which would stall the repository's later merges: the outcome is `blocked`, `assessment-conflict`, and the canonical is kept;
      - allocate the next version: the current id is `V<n>` and the next is `V<n+1>`; another form of id is `ErrInvalidState`;
      - read the Suite's versions with one `Tx.VersionsHolding(digest)` call, by manifest digest and without decoding a manifest (M1.15; it replaces one `Tx.Version` read per earlier version). Its query joins the promotion and its proposal revision as `Tx.Version` does, so a version without a promotion is missing, and a version whose promotion binds another manifest digest than the version stores is `ErrInvalidState` naming it. Walk `V1`..`V<n-1>` in order: a missing `V<i>` is `ErrInvalidState` naming it, and the first one that holds the integrated manifest digest decides a correction with target the current version (the version being corrected); with none, decide a promotion;
      - use operation id `merge:<carrier>` and an `IntegrationMergedChange` integration with source `Source`, carrier `Carrier` and the Suite's target;
      - decide inside this same `Do`, sharing the decision and write logic of `Bootstrap` (`promoteWithin`), which is a function over `Tx`.

      `PromotionProposed` gives `promoted` or `corrected`. `PromotionBlocked` maps:
      - `ApprovalMissing` to `approval-missing`;
      - `ContextMismatch` to `differs-from-approved`;
      - `ProposalNotCurrent` to `not-current`;
      - `CanonicalChanged` to `canonical-changed`;
      - `PolicyChanged` to `policy-changed`;
      - `AssessmentMismatch` and `IntegrityNotPassed` to `integrity-not-passed`;
      - `IntegrationMismatch` to `integration-mismatch`;
      - `CorrectionContextReused` to `correction-context-reused`.

      Any other reason or outcome is `ErrInvalidState`.
3. **Write.** `Tx.RecordMergeOutcome` and, when `Message` returns true, `Tx.Outbox`, in the same transaction. The outcome write itself never advances the Suite revision; the assessment recorded in step 8 does, as does a promotion, through `RecordPromotion`.

`MergeOutcomeOf` is a read in one `Do`. The job uses it to skip work that is already done, before reading any tree.

**`Tx` gains:**
- `MergeOutcome(ctx, carrier) (MergeOutcome, bool, error)`;
- `RecordMergeOutcome(ctx, MergeWrite) error`, where `MergeWrite` is `{Carrier, Outcome, RecordedAt}`. A second outcome for the same carrier is `ErrConflict`.

**Table `pr_merges`.**
- Primary key `(project_id, suite_id, carrier)`, immutable by trigger.
- Columns: `kind` and `reason` (CHECK on the codes, reason exactly when blocked); `source`; `version_id` and `corrects_version_id` (FKs to `suite_versions`); `proposal_id` and `revision_id` (nullable); `recorded_at`.

**Conformance (fake and PostgreSQL).**
- One case per kind and reason path.
- A replay.
- A second outcome conflicts.
- Two PRs approved against V1: the first merge promotes V2. The second merge blocks as `canonical-changed`, or as `approval-missing` once its revision was rebound.
- A correction restoring V1's content after V2 records `CorrectsVersionID` V2.
- Concurrent `PromoteMerge` calls for one carrier write one outcome.
- A failing message rolls everything back.

### Canonical integrity (`internal/application/integrity`)

```go
type Verdict string // "passed", "failed", "unavailable"

type Record struct {
	Project   contract.ProjectID
	Suite     contract.SuiteID
	Version   contract.SuiteVersionID // the canonical version verified
	Verdict   Verdict
	CheckedAt time.Time
}

type Store interface {
	// Record replaces the Suite's verdict, except that "unavailable" never replaces "failed" for the same version
	// (only CheckedAt moves), so an unreadable store never clears a confirmed failure.
	Record(ctx context.Context, r Record) error
	Latest(ctx context.Context, project contract.ProjectID, suite contract.SuiteID) (Record, bool, error)
}
```

- **Verdicts.** `failed` means a content digest of the current canonical's manifest is missing or does not match its bytes: `filesystem.Store.Verify` returns `ErrCorruptArtifact` or the bytes are absent. `unavailable` means another read error. `passed` means every digest verified.
- **Applies to its version only.** A verdict applies only while its `Version` is the current canonical, so a new version starts without one. A failed verdict for the current version persists across restarts, and only a later `passed` clears it.
- **Table `canonical_integrity`.** Primary key `(project_id, suite_id)`, the latest verdict replaced in place. Columns `version_id`, `verdict` (CHECK) and `checked_at`.
- **Conformance.**
  - Replace.
  - `unavailable` after `failed` keeps `failed`.
  - `passed` after `failed` clears it.
  - A new version's verdict replaces the older one.

### Change to the check ledger (M1.6b)

**Lost publication with a cooldown.** `checks.Store.Publish` re-sends a state whose current message ended `failed` only once at least 15 minutes have passed since that message's `finished_at`. Before then, an identical result writes nothing. This bounds republication under a permanent refusal to one message per 15 minutes per pull.

**Conformance.**
- A failed message re-sent after the cooldown.
- No message inside it.

## Bootstrap from a PR, and the revision comment (M1.7b; normative)

The rework of 2026-10-16 replaces `Tx.BaselineProposalFor`, the `Summary` and `Committed` shape of `AssessBootstrapHead`, and the carrier-bound `PromoteMerge` below: see [Explicit bootstrap](#explicit-bootstrap-m17b-rework-2026-10-16-normative).

Implements the ADR 0010 and D-CHECKS amendments of 2026-10-15. Migration `00016` adds every column below.

### Origin kind (`internal/domain/contract`)

```go
type OriginKind string

const (
	OriginBranch OriginKind = "branch" // a pinned default-branch commit (existing-baseline bootstrap)
	OriginPull   OriginKind = "pull"   // a PR head: the first-test bootstrap and every change proposal
)

func NewProposalRevision(binding ApprovalBinding, origin SourceRevision, kind OriginKind, carrier ApprovalCarrierID) (ProposalRevision, error)
func (r ProposalRevision) OriginKind() OriginKind
```

`NewProposalRevision` returns `ErrInvalidProposal` in these cases:
- an unknown kind;
- `OriginBranch` with a binding that expects a canonical (only a baseline comes from a branch).

`Proposal.Revise` also rejects a revision whose kind differs from the proposal's first revision, so a proposal never changes kind.

**`DecideBootstrap`.** It keeps `PromotionReasonCanonicalPresent` and the empty-inventory guard, and accepts exactly two shapes:
- an `IntegrationExistingBaseline` integration when the current revision is `OriginBranch`, with source = origin, as before;
- an `IntegrationMergedChange` integration when the current revision is `OriginPull`, with the carrier of the proposal and the merge commit as source. The source may equal the revision's origin (a fast-forward merge): the kind, not the commit, separates the paths.

Any other combination is `PromotionReasonIntegrationMismatch`. `DecidePromotion` applies the same rule wherever it decides: an `IntegrationExistingBaseline` integration requires an `OriginBranch` revision, so a bootstrap cannot make a `pull` head canonical either. A PR head therefore never becomes canonical without its merge.

Reconstitution and equality include the kind.

**Table `proposal_revisions`** gains `origin_kind` (`text NOT NULL`, `CHECK` on the two codes). Migration `00016` back-fills existing rows: `branch` where `expected_version_id` is NULL (the baselines), `pull` otherwise. If a proposal would end up with revisions of both kinds (a baseline later revised against a canonical, which no released job writes), the migration fails and rolls back rather than leave an unreadable proposal.

### Governance (`internal/application/governance`)

**`BaselineRequest`.**
- It gains `OriginKind`. The bootstrap job passes `OriginBranch`; `AssessBootstrapHead` passes `OriginPull`.
- Its `Reference` may name any next revision, not only R1. The rest of `ProposeBaseline` is unchanged: no canonical, `ExpectedCanonical` empty, the shared revision logic.

**`Tx.BaselineProposalFor`.**

```go
// BaselineProposalFor returns the bootstrap proposal of carrier in the locked Suite: the proposal carried by it
// whose revisions are OriginPull, expect no canonical, and none of which was promoted. False when there is none;
// ErrInvalidState when more than one qualifies.
BaselineProposalFor(context.Context, contract.ApprovalCarrierID) (contract.ProposalID, bool, error)
```

`ChangeProposalFor` keeps excluding every baseline.

**`AssessBootstrapHead`.**

```go
type BootstrapHeadRequest struct {
	Project contract.ProjectID
	Suite   contract.SuiteID
	Carrier contract.ApprovalCarrierID // the bootstrap PR (the caller chooses it, runtime contract)
	Head    contract.SourceRevision
	Tree    inventory.Tree
	// Summary builds the bootstrap-summary comment of a revision this call commits.
	Summary func(contract.ProposalReference, inventory.Inventory) (OutboxMessage, error)
}

type BootstrapHeadResult struct {
	Empty     bool                        // the head has no protected files: nothing was written
	Reference *contract.ProposalReference // the current revision, nil when Empty
	Committed bool                        // this call appended the revision
	Ready     bool                        // CheckPromotionReadiness answered ready for Head
	Reason    contract.PromotionReason
	Revision  int64
}

func AssessBootstrapHead(ctx context.Context, uow UnitOfWork, writer ContentWriter, request BootstrapHeadRequest) (BootstrapHeadResult, error)
```

1. **Build the inventory.** `inventory.Build(Tree)`. Its errors are wrapped as for `AssessHead`, including a truncated tree.
   - An empty manifest returns `Empty` and writes nothing.
2. **Store the content** before `Do`.
3. **Inside one `Do`:**
   1. **Canonical present.** Return `ErrCanonicalMoved`. The next run uses `AssessHead`.
   2. **Find or allocate the proposal.** Use `BaselineProposalFor(Carrier)`, or `NextProposalID` when there is none. The next revision is `R<len+1>`.
   3. **Revise.** Use `reviseWithin` with an `OriginPull` revision, origin `Head`, `ExpectedCanonical` empty and the governing policy. Unchanged coverage writes nothing, so an implementation-only push keeps consent.
   4. **Summary.** When this call committed the revision, write the `Summary` message.
   5. **Assess.** `RecordAssessment` of the current revision at `Head` with `HeadEvidence`.
   6. **Readiness.** `CheckPromotionReadiness` with `requiredSource = Head`.
4. **Idempotent.** Repeating the call writes nothing new.

**`PromoteMerge` with no canonical.** These rules replace step 2.2, "No canonical: return `ErrNoCanonical`".
- **The carrier's bootstrap.** When there is no canonical and the request has no `Refusal`, its `Target` is the Suite's target, and `BaselineProposalFor(Carrier)` finds a proposal:
  1. record the assessment of its current revision at `Source` with `HeadEvidence`; an `ErrConflict` gives `blocked` `assessment-conflict`;
  2. decide with `DecideBootstrap`, using an `IntegrationMergedChange` integration, version `V1`, operation `merge:<carrier>` and kind `OperationBootstrap`, inside the same `Do`.

  The outcome is `promoted` V1, or `blocked` with a mapped reason and **no version**.
- **A merge into another branch** with no canonical is `before-canonical`, even for the bootstrap carrier; the target is checked first, as with a canonical.
- **A refusal with a bootstrap proposal.** The outcome is `blocked` with that reason and no version. A refusal `integrity-failed` names a stored canonical, so without one it is `ErrInvalidRequest`.
- **Every other merge with no canonical.** The outcome is `before-canonical` with no version and no message.
- **The `before-canonical` rule by time.** It applies only when V1's current revision is `OriginBranch`. When V1 came from a merge, every earlier merge was already recorded in order.
- **Impossible reasons.** `CanonicalPresent` and `EmptyInventory` under the Suite lock are impossible states, `ErrInvalidState`.
- **`MergeOutcome.Version` is empty exactly for** `blocked` and `before-canonical` decided without a canonical. `MergeWrite.Validate` and `pr_merges` enforce this: `version_id` becomes nullable in `00016`, with a `CHECK`.

**The revision comment.**
- `HeadRequest` gains:

  ```go
  Revision func(RevisionNotice) (OutboxMessage, bool, error)
  ```

  In the second `Do` of `AssessHead`, when `keepProposalCurrent` committed a new revision, `AssessHead` calls it and writes the message when it returns true.

  ```go
  type RevisionNotice struct {
  	Reference contract.ProposalReference
  	Previous  *contract.ProposalReference // the revision it supersedes, nil for R1
  	Canonical contract.SuiteVersionID     // the base version
  	Added, Modified, Removed []string     // inventory.Classify against the canonical, sorted
  	Reduced                  bool         // a scope reduction
  }
  ```

- A nil `Revision` is `ErrInvalidRequest`. Callers that want no comment pass a function that returns false; A3 changes the existing `internal/app` call site that way until A4 wires the real message.

**Conformance (fake and PostgreSQL).**
- Origin kind:
  - the kind round-trips;
  - the back-fill;
  - a mixed-kind proposal is rejected;
  - `DecideBootstrap` with each of the four kind and integration combinations.
- `BaselineProposalFor`.
- `AssessBootstrapHead`:
  - R1;
  - an implementation-only push writes nothing;
  - a protected push gives R2 and a summary;
  - an empty head;
  - a canonical present;
  - readiness before and after approval.
- `PromoteMerge` with no canonical:
  - V1 from a merged bootstrap PR;
  - a differing merge tree is `blocked` with no version;
  - a non-carrier merge is `before-canonical`;
  - a merge after a merged V1 is decided against V1, never `before-canonical`;
  - the `before-canonical` time rule is skipped for a merged V1.
- The revision comment:
  - written exactly once per committed revision;
  - not written for an unchanged head;
  - rolled back with the revision.

### Explicit bootstrap (M1.7b rework, 2026-10-16; normative)

This replaces the parts of the M1.7b section above that this list names.

**Governance.**
- **`Tx.LatestBootstrapProposal`** replaces `Tx.BaselineProposalFor(carrier)`, which is removed with its query:

  ```go
  // LatestBootstrapProposal returns the locked Suite's newest proposal whose revisions expect no canonical, of
  // either origin kind, and none of which was promoted. False when there is none. Older ones are superseded.
  LatestBootstrapProposal(context.Context) (contract.ProposalID, bool, error)
  ```

  "Newest" means the highest proposal sequence, in other words the latest first revision.
- **`AssessBootstrapHead` is assess-only.**
  - The request drops `Summary` and adds nothing.
  - Inside `Do` it reads `LatestBootstrapProposal`. Neither of these is an error:
    - when the proposal is missing, or its carrier is not `Carrier`, or it is not `OriginPull`, the result is `Cancelled`;
    - when the head's coverage differs from the current revision, the result is also `Cancelled`.
  - In both cases nothing is written.
  - Otherwise it records the assessment at `Head` and evaluates `CheckPromotionReadiness`.
  - It never revises or creates a proposal. `BootstrapHeadResult` gains `Cancelled bool` and drops `Committed`.
- **`PromoteMerge` without a canonical** uses `LatestBootstrapProposal`. The bootstrap path applies only when that proposal is `OriginPull` and its carrier equals the request's carrier. Every other case is `before-canonical`.
- **`ProposeBaseline`** is unchanged. The commands job calls it with R1 and the origin kind of its source.

**Commands store (`internal/application/commands`).**
- New dispositions `proposed`, `propose-refused`, `skipped` and `skip-refused`. None carries an operation id.
- `LatestOwnerCommand` ignores them, so they never affect the owner-command `failure`.
- The new read:

  ```go
  // Skipped reports whether the pull has a `skipped` entry whose comment was created at or after since
  // (the current enrollment's enrolled_at).
  Skipped(ctx context.Context, repositoryID int64, pull int, since time.Time) (bool, error)
  ```
- Migration `00017` updates the `pr_commands` disposition CHECK.

**Inventory.** The suggestion from file paths only was `inventory.SuggestScope` when this rework was written. M1.9 replaced it with `inventory.Suggest` and removed it: [Scope discovery](#scope-discovery-m19-normative) gives the signatures, the limit of 20 and the order.

**Conformance:**
- `LatestBootstrapProposal`: the newest wins, a promoted one is excluded, and both kinds are returned.
- `AssessBootstrapHead`:
  - cancelled when the coverage differs, when the proposal belongs to another carrier, or when it is a `branch` proposal;
  - ready after approval;
  - writes nothing when cancelled.
- `PromoteMerge`: a superseded carrier's merge is `before-canonical`.
- `Skipped`: the `since` boundary, and another pull is not matched.
- The new dispositions are ignored by `LatestOwnerCommand`.

## Scope discovery (M1.9; normative)

The [runtime contract](runtime.md#scope-discovery-m19) gives the behavior. This section gives the signatures, the migration and the conformance.

- **Inventory.**

  ```go
  // Suggestion is a suggested declaration from file paths only.
  type Suggestion struct {
  	Include []string
  	Runner  []string
  	Mixed   []string
  	// Submodules are the non-vendored submodules that an Include or Runner pattern reaches; their content is not protected.
  	Submodules []string
  	// Cut is true when the limit dropped some include or runner patterns.
  	Cut bool
  }

  // Suggest proposes a declaration from the paths of the File entries of a tree. It replaced SuggestScope.
  func Suggest(entries []Entry) Suggestion

  // SuggestAgainst is Suggest for a base declaration s: a runner pattern that s lists or covers is left out
  // before the limit applies, so it neither takes room nor sets Cut.
  func SuggestAgainst(entries []Entry, s scope.Scope) (Suggestion, error)

  // Uncovered returns the test-like file paths of entries that s does not cover, sorted.
  func Uncovered(entries []Entry, s scope.Scope) ([]string, error)
  ```

  The limit of 20 covers the include and the runner patterns together. The runner patterns come first, at most 20 of them, and the include patterns take the room that is left, the directory patterns before the file patterns and each group sorted. `Cut` is set whenever an include or runner pattern is dropped, and the replies say so. `Mixed` and `Submodules` have a limit of their own, 20 paths each. `SuggestScope` no longer exists.
- **Scope.**

  ```go
  // AddPatterns returns raw with include and runner appended to their lists. It creates a list that is
  // missing, skips a pattern already present, keeps the comments yaml.v3 keeps, and never removes or
  // reorders a pattern. The result is a valid declaration.
  func AddPatterns(raw []byte, include, runner []string) ([]byte, error)
  ```
- **Commands.**
  - The new dispositions are `suggested` and `suggest-refused`. Neither carries an operation id, and neither is counted by `LatestOwnerCommand`.
  - Migration `00019` updates the `pr_commands` disposition CHECK, as `00017` did, and `SupportedVersion` becomes 19.
- **GitHub adapter.**
  - `Reader.PullFiles(ctx, repo, pull)` returns each changed file's path, status and `patch`.
  - `Writer.PostReviewComment(ctx, repo, pull, commitSHA, path, startLine, line, body)` and `Writer.ReviewCommentsByAuthor(ctx, repo, pull, authorID)` read and write review comments.

**Conformance:**
- `Suggest`:
  - the conventions;
  - directories at any depth;
  - the top-level rule for `tests/`, `test/` and `spec/`;
  - vendored paths excluded;
  - the higher-priority runner files;
  - `mixed`;
  - the shared limit and `cut`;
  - the order;
  - SuiteWard's own paths, a fixture: every `*_test.go`, all `testdata/` and `codecov.yml` covered, with `cut` false.
- `Uncovered`:
  - it returns only test-like paths;
  - it leaves out covered paths;
  - it leaves out a test-like path matched by an `exclude`;
  - it uses the same classifier as `Suggest`.
- A declaration whose runner pattern matches no file still parses, and `Covers` and the integrity check accept it.
- `AddPatterns`, by round trip: the output parses, keeps every original pattern, and adds exactly the requested new ones.
- `suggested`, `suggest-refused` and `other-pull` are ignored by `LatestOwnerCommand`: after an `approve` with a wrong code, a `suggest` keeps `owner-command-rejected`.
- Payloads: an ack or revision payload of the `v0.1.0` shape still decodes.

## Execution (M2.1; normative)

[ADR 0028](../decisions/0028-canonical-execution-and-the-receipt.md) decides the rules, and the [runtime contract](runtime.md#execution-m21-normative) gives the wire and the behavior. Migration `00021_execution.sql` adds everything below, forward only, and `SupportedVersion` becomes 21. A † marks a choice of task A0 (a name, a size or a number) that the integrator may change. The new tables follow the conventions of the earlier ones: `text` enumerations with `CHECK` constraints, no `UPDATE` of an insert-only table, and `DELETE` and `TRUNCATE` rejected by `reject_immutable_history()`. `GrantApp` needs no change: its default privileges cover the new tables, and the triggers stop what a grant would allow.

**Evidence, not authority.** Nothing here is read by `Tx`, consent or promotion. The use cases that decide a state (the executor API and the checks job) call `internal/domain/execution` and hand the store rows that already carry their states. The store checks the shape (codes, sizes, the plan against the rows) and never re-runs a decision.

**Retention: none in M2.1.** No row of these tables is deleted, as for `pr_commands` and the outbox. The bounds are in the wire contract: at most 10,000 test rows for a result, and one job for each plan and binding of a head (a new governing manifest or a changed policy is a new binding, so a head can have more than two jobs over its life). Pruning old per-test rows is deferred (ADR 0028, deferred work).

### Table `execution_jobs`

One row for each wanted run: a plan on a head, for one binding. A not-applicable receipt has no row. The row changes while the job is open and never after.

| Column | Type | Rule |
| --- | --- | --- |
| `job_id` | `bigint GENERATED ALWAYS AS IDENTITY` | primary key |
| `job_key` | `text NOT NULL` | `^[0-9a-f]{64}$`; the SHA-256 of the six lines of the [runtime contract](runtime.md#job-lifecycle); unique among the jobs that are not `cancelled` (a partial unique index) |
| `repository_id` | `bigint NOT NULL` | `> 0` |
| `pull_number` | `integer NOT NULL` | `> 0` |
| `plan` | `text NOT NULL` | `canonical` or `receipt` |
| `profile` | `text NOT NULL` | `go` |
| `head_sha` | `text NOT NULL` | `^[0-9a-f]{40}$` |
| `base_sha` | `text` | `^[0-9a-f]{40}$`; not null exactly when `plan = 'receipt'`, except for a receipt job that is created already `unavailable` with reason `no-merge-base`, `executor-not-configured` or `github-unavailable`, which has none (one `CHECK` states the exception) |
| `governing_digest` | `text NOT NULL` | `^sha256:[0-9a-f]{64}$`: the manifest the job runs against |
| `governing_source` | `text NOT NULL` | `^(V[1-9][0-9]{0,8}\|P[1-9][0-9]{0,8}-R[1-9][0-9]{0,8})$`, the pattern of `internal/domain/execution`: versions and revisions are numbered from 1 |
| `declaration_digest` | `text NOT NULL` | `^sha256:[0-9a-f]{64}$`: the canonical declaration, the source of the policy |
| `canonical_level`, `receipt_level` | `text NOT NULL` | the effective levels when the job was created. `canonical_level` is `off`, `advisory` or `required`, and not `off` for a `canonical` job. `receipt_level` is `off`, `advisory`, `before-approval` or `required`, and not `off` for a `receipt` job |
| `unapproved_paths` | `text[] NOT NULL DEFAULT '{}'` | the paths "not evaluated: pattern not approved yet", computed by the service when it creates the job: at most 100, each at most 1024 bytes; empty for a `canonical` job. Display only |
| `changed_paths` | `text[] NOT NULL DEFAULT '{}'` | the files that the canonical `include` matches and the revision adds or changes, computed by the service when it creates the job: at most 100, each at most 1024 bytes; empty for a `canonical` job. Display only |
| `changed_total` | `integer NOT NULL DEFAULT 0` | `>= 0`; how many such files there are |
| `state` | `text NOT NULL DEFAULT 'pending'` | `pending`, `leased`, `complete`, `unavailable` or `cancelled` |
| `reason` | `text` | not null when `state` is `unavailable` or `cancelled`, null when it is `complete`, and for an open job it holds the reason of the last failed attempt, or `github-unavailable` while a GitHub read is failing (so it is null until something failed). For `unavailable`, one of the reasons of the [runtime contract](runtime.md#execution-m21-normative); for `cancelled`, `head-moved`, `closed` or `obsolete` |
| `attempts` | `integer NOT NULL DEFAULT 0` | `>= 0`; the number of leases granted |
| `failures` | `integer NOT NULL DEFAULT 0` | `>= 0`; the attempts that an executor leased and lost or failed with a retried reason. The job is final at the third. A GitHub failure is not one |
| `github_since` | `timestamptz` | the time of the job's first GitHub failure; set once, and cleared by a result or by a failure that is not GitHub's. `github-unavailable` is final when `EXECUTOR_GITHUB_WAIT` has passed since it |
| `not_before` | `timestamptz NOT NULL` | the earliest time a lease may be granted; the creation time at first, and the backoff time after a failed attempt or a GitHub failure |
| `lease_id` | `text` | `^[0-9a-f]{32}$`; not null exactly when `attempts > 0`: the id of the latest lease. It stays when the lease ends, so that a result sent again for a finished job is recognized and the record shows it. Only a job that is `leased` accepts a lease |
| `lease_expires_at` | `timestamptz` | not null exactly when `state = 'leased'` |
| `leased_at` | `timestamptz` | not null exactly when `attempts > 0`: the time of the latest lease |
| `deadline_at` | `timestamptz` | not null exactly when `state = 'leased'`: the deadline of the run, the job timeout after `leased_at` |
| `created_at` | `timestamptz NOT NULL` | |
| `finished_at` | `timestamptz` | not null exactly when `state` is `complete`, `unavailable` or `cancelled` |

There is no foreign key to `github_pulls` (observation rows are state, not history), as `pr_checks` has none. Indexes: the partial unique index on `job_key`; `(created_at, job_id) WHERE state IN ('pending', 'leased')`, which the lease query reads; and `(repository_id, pull_number, head_sha)`.

**Update guard `guard_execution_job_update`.** A `BEFORE UPDATE` trigger. A row whose state is `complete`, `unavailable` or `cancelled` cannot change. The identity columns (`job_id`, `job_key`, `repository_id`, `pull_number`, `plan`, `profile`, `head_sha`, `base_sha`, `governing_digest`, `governing_source`, `declaration_digest`, `canonical_level`, `receipt_level`, `unapproved_paths`, `changed_paths`, `changed_total`, `created_at`) never change. The state moves only along the table of the [runtime contract](runtime.md#job-lifecycle): `pending` to `leased`, `unavailable` or `cancelled` (and an update of a pending row that changes its reason, `github_since` and `not_before`); `leased` to `pending`, `complete`, `unavailable` or `cancelled`. `DELETE` and `TRUNCATE` are rejected. A row is inserted `pending`, or already `unavailable` with reason `no-merge-base`, `executor-not-configured` or `github-unavailable` (`finished_at` set, `attempts` 0).

### Table `execution_results`

Insert-only. One row for each job that ended with a result from the executor, `complete` or `unavailable`: the result of the last attempt, never one of an attempt that was queued again. A job that the service made unavailable (`no-executor`, a GitHub bound, `lease-expired` on the third failure, or one created final) has none, and its `execution_jobs` row says why.

| Column | Type | Rule |
| --- | --- | --- |
| `job_id` | `bigint NOT NULL` | primary key; foreign key to `execution_jobs` |
| `status` | `text NOT NULL` | `complete` or `unavailable`; the job's state, written in the same transaction |
| `reason` | `text` | not null exactly for `unavailable`; an executor-set reason of the runtime contract |
| `executor_version` | `text NOT NULL` | `^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`, the pattern of `internal/domain/execution` |
| `executor_sha256` | `text NOT NULL` | `^[0-9a-f]{64}$` |
| `image_digest` | `text` | `^sha256:[0-9a-f]{64}$`; not null for `complete` |
| `toolchain` | `text` | at most 64 bytes; not null for `complete` |
| `started_at`, `ended_at` | `timestamptz NOT NULL` | `ended_at >= started_at` |
| `received_at` | `timestamptz NOT NULL` | the service's clock |
| `body_digest` | `text NOT NULL` | `^sha256:[0-9a-f]{64}$`; of the request body, so that the same result sent again is recognized |
| `markers` | `jsonb NOT NULL DEFAULT '[]'` | an array of at most 200 objects `{kind, path, line}`; an immutable payload |
| `submodules` | `text[] NOT NULL DEFAULT '{}'` | at most 100 |

### Table `execution_tests`

Insert-only. One row for each test of a `complete` result. The head and base observations are the facts, and `state` is the decision that was shown.

| Column | Type | Rule |
| --- | --- | --- |
| `job_id` | `bigint NOT NULL` | foreign key to `execution_results`; part of the primary key `(job_id, package, name)` |
| `package` | `text NOT NULL` | 1 to 512 bytes |
| `name` | `text NOT NULL` | 1 to 200 bytes |
| `file` | `text NOT NULL` | 1 to 1024 bytes |
| `kind` | `text NOT NULL` | `declared`, `added` or `changed` |
| `excluded_reason` | `text` | `build-constraint` †; not null exactly when `state = 'excluded'` |
| `head_runs` | `text[] NOT NULL` | each element `pass`, `fail`, `build-error`, `skip` or `absent`; 1 to 3 elements, none exactly when the test is excluded |
| `base_runs` | `text[]` | the same elements; null when excluded and for a canonical job; otherwise 1 to 3 elements |
| `state` | `text NOT NULL` | the lower-case form of a state of ADR 0028. Canonical states: `pass`, `fail`, `skipped`, `missing`, `flaky`, `excluded`. Receipt states: `excluded`, `missing`, `flaky`, `skipped`, `fails`, `new_api`, `detects`, `inert`. One `CHECK` lists the union; `fail` and `fails` are two states, as in the ADR. `state = 'excluded'` exactly when `excluded_reason` is set |

The store also checks, against the job's plan: a `canonical` job has only `declared` rows with no `base_runs`, and a `receipt` job has only `added` and `changed` rows, each with `base_runs` unless excluded, and only receipt states. A violation is `ErrInvalidRequest` and nothing is written. So is a result with more than 10,000 test rows †: the bound of a result is checked when it is written, and not only by the wire.

### Table `pr_receipt_checks`

The ledger of the `SuiteWard / Receipt` check. It mirrors `pr_checks` (migration `00014`): one row for each pull request, with the current state and the publication generation, updated in place and never deleted, and with the publications themselves in the outbox. It has the columns of `pr_checks` with the same constraints, except that `change`, `revision` and `command_id` have no meaning here and are left out, `state` also accepts `neutral`, and `job_id` and `unblocked` are added.

| Column | Type | Rule |
| --- | --- | --- |
| `repository_id`, `pull_number` | `bigint`, `integer` | primary key; both `> 0` |
| `head_sha` | `text NOT NULL` | `^[0-9a-f]{40}$` |
| `head_since` | `timestamptz NOT NULL` | when this head was first recorded |
| `state` | `text NOT NULL` | `success`, `in_progress`, `failure` or `neutral` |
| `reason` | `text NOT NULL` | not blank: `receipt-running`, `receipt-failing`, `receipt-complete` or `receipt-not-run` (the names differ from the gate reasons of the domain, `receipt-pending` and `receipt-unavailable`, which are the reasons of a refused approve) |
| `reference` | `text NOT NULL` | `^(P[0-9]+-R[0-9]+)?$` |
| `job_id` | `bigint NOT NULL` | `>= 0`; the receipt job that the state shows, `0` for none; no foreign key, as `pr_checks` has none to the outbox |
| `unblocked` | `boolean NOT NULL DEFAULT false` | true when the owner unblocked the head's receipt job and it is `unavailable` |
| `generation` | `bigint NOT NULL` | `> 0` |
| `message_key` | `text NOT NULL` | not blank: `receipt-check:<repository id>:<pull>:<generation>` |
| `decided_at` | `timestamptz NOT NULL` | |

The triggers are `pr_receipt_checks_no_delete` and `pr_receipt_checks_no_truncate`. The store has the shape of `checks.Store`: `Current(ctx, repositoryID, pull)` and `Publish(ctx, result, at, message func(generation int64) (governance.OutboxMessage, error))`, with the same rules. A new head, state, reason, reference or `unblocked` advances the generation by one and writes the `github_receipt_check` message in the same transaction. A result that changes none of them updates `job_id` and `decided_at` only and writes no message. A message that ended `failed` is sent again under the next generation, no sooner than 15 minutes after it finished. Concurrent publications for one pull never write two messages for one generation.

### Changes to `pr_checks`

`pr_checks` gains `unblocked boolean NOT NULL DEFAULT false`: true when the owner unblocked the head's canonical job and that lift decided the outcome (the canonical gate would have withheld the check). `checks.Result` and `checks.Record` gain `Unblocked`, and `Publish` advances the generation when it differs from the current record, as it does for the head, the state, the reason and the reference. The `github_check` message gains `unblocked` and `execution` (see the [runtime contract](runtime.md#the-canonical-gate-on-the-suiteward--contract-check)). Rows from before `00021` read `false`.

### Table `execution_unblocks`

Insert-only. The owner's unblock: one row for each job that an `unblocked` command lifted, written in the same transaction as its `pr_commands` entry. The command lifts the jobs of the head that were `unavailable` when it was processed, so a command can write one row or two.

| Column | Type | Rule |
| --- | --- | --- |
| `repository_id`, `comment_id` | `bigint NOT NULL` | foreign key to `pr_commands`; part of the primary key |
| `job_id` | `bigint NOT NULL` | foreign key to `execution_jobs`; part of the primary key `(repository_id, comment_id, job_id)`. The job must be of the head and the pull of the command, and `unavailable` |
| `pull_number` | `integer NOT NULL` | `> 0` |
| `head_sha` | `text NOT NULL` | `^[0-9a-f]{40}$`: the head of the job |
| `decided_at` | `timestamptz NOT NULL` | |

The store has `Unblocked(ctx, jobID, since)`, true when a row names the job and its `pr_commands` entry was created at or after `since` (the current enrollment's `enrolled_at`). The row binds to the job and not to the head: a job created later for the same head (a new governing manifest after an approval, a changed policy) is another job and needs its own unblock. Nothing else reads the table.

### Table `approval_receipts`

Insert-only. The receipt state at the moment of an `approve`, whether it was refused or not. One row for each `approve` that reached the receipt gate under a `receipt` level other than `off` and named the proposal's current revision, written in the same transaction as its `pr_commands` entry.

| Column | Type | Rule |
| --- | --- | --- |
| `repository_id`, `comment_id` | `bigint NOT NULL` | primary key; foreign key to `pr_commands` |
| `head_sha` | `text NOT NULL` | `^[0-9a-f]{40}$`: the head that the gate read |
| `receipt_level` | `text NOT NULL` | `advisory`, `before-approval` or `required` |
| `run_state` | `text NOT NULL` | `none` (no job yet), `pending`, `complete`, `unavailable` or `not-applicable` |
| `job_id` | `bigint` | foreign key to `execution_jobs`; not null for `pending`, `complete` and `unavailable`, null for `none` and `not-applicable` |
| `summary` | `text` | `met` or `not-met`; set only when `run_state = 'complete'`, the receipt is not empty and the comment carried a mode (a complete empty receipt passes the gate, so it has none) |
| `unblocked` | `boolean NOT NULL DEFAULT false` | true when the gate passed an `unavailable` job because the owner unblocked it |
| `decided_at` | `timestamptz NOT NULL` | |

The mode of the approve is in `pr_commands.mode`, next to the disposition, so that the ledger holds one record of the comment. An `approve` that named another revision than the current one is not gated and has no row. So has an `approve` that the commands job retried after a crash and that found its consent already recorded: the state at the approval was not recorded.

### Changes to `pr_commands`

- **Dispositions.** The `pr_commands_disposition_check` list is replaced, as `00020` replaced it, by the list of `00020` plus `receipt-refused`, `mode-set`, `mode-refused`, `unblocked` and `unblock-refused` †. The table is insert-only, so only the `CHECK` changes. None of the five carries an operation id (the existing rule allows one only for `processed`). `receipt-refused`, `unblocked` and `unblock-refused` carry a code, and their step stays consumed. `LatestOwnerCommand` does not list the five dispositions, so none of them sets or clears `owner-command-rejected`.
- **`mode text`**, nullable, with `CHECK (mode IS NULL OR mode IN ('fix', 'feature', 'refactor'))` and `CHECK (disposition <> 'mode-set' OR mode IS NOT NULL)`. The ledger writes it whenever the command carried a mode.
- **`commands.Entry`** gains `Mode` (empty for none). `Record` gains an optional gate record for an `approve` (written to `approval_receipts`) and optional unblock rows for an `unblock` (written to `execution_unblocks`), each in the same transaction. An entry of another verb has neither.
- **`PreviewMode(ctx, repositoryID, pull, since)`** returns the `mode` of the latest `mode-set` entry of the pull created at or after `since` (the current enrollment's `enrolled_at`), and false when there is none.

### Store operations of `execution_jobs` and the results

The behavior is in the [runtime contract](runtime.md#the-executor-api); these are the semantics that the store must guarantee. The signatures belong to the port that task P1 writes.
- **Enqueue.** Idempotent by `job_key` among the jobs that are not `cancelled`: it returns the existing job and writes nothing. It also creates a job that is already `unavailable` with reason `no-merge-base`, `executor-not-configured` or `github-unavailable`, a receipt one with no `base_sha`. A job that breaks a constraint is `ErrInvalidRequest`.
- **Lease.** In one transaction, picks the oldest job (by `created_at`, then `job_id`) that is `pending` with `not_before` reached, or `leased` with an expired lease, with `FOR UPDATE SKIP LOCKED`. A candidate that is `leased` and expired counts as a failure with reason `lease-expired`, or `timeout` when it ran past its `deadline_at`: with fewer than 3 failures it goes back to `pending` with its `not_before`, otherwise it becomes `unavailable` with that reason. The transaction goes on to the next candidate. Otherwise it increments `attempts`, sets `leased_at`, a new `lease_id`, `lease_expires_at` and `deadline_at`, and returns the job. It returns none when nothing is ready. Two concurrent leases never return one job.
- **GitHub failure.** The store records that a read for the job failed: it sets `github_since` if it is null, sets `reason` to `github-unavailable`, moves `not_before` a minute † ahead, counts no failure, and leaves the job `pending`. When `github_since` is older than the wait, the job becomes `unavailable` with `github-unavailable`. The result of an executor that gives the job back as `github-unavailable` does the same to a `leased` job, which goes back to `pending`.
- **Heartbeat.** Extends `lease_expires_at`, never beyond `deadline_at`, when the job is `leased`, the lease matches and has not expired. Anything else is a lost lease (`ErrLeaseLost`, P1's name †).
- **Complete.** Looks first at a finished job: when `body_digest` equals the stored one it returns the stored outcome, whatever the lease, and otherwise `ErrConflict`. For an open job it validates the lease as the heartbeat does. Then, for a retried reason with failures left, it queues the job again (`failures` plus one) and stores no result; for `github-unavailable` it records the GitHub failure; otherwise it writes the `execution_results` row, the `execution_tests` rows and the job's state in one transaction.
- **Cancel** and **Expire.** Cancel moves a `pending` or `leased` job to `cancelled` with its reason and does nothing to a final job. Expire applies the expiry, `no-executor` and GitHub-bound rules to every open job and returns the number it changed. It takes the time the service last saw an executor: when `EXECUTOR_LEASE_WAIT` has passed since, every `pending` job becomes `unavailable` with `no-executor`.
- **Reads.** The jobs of a head, by plan; a job by key; the tests of a job; the number of jobs in each state for `/status`.

### Conformance

The fake and PostgreSQL suites cover:
- `execution_jobs`:
  - Enqueue is idempotent, and a `cancelled` job does not block a new one with the same key;
  - the lease is exclusive under concurrency (PostgreSQL), takes the oldest first by `created_at` and `job_id`, and honors `not_before`;
  - a job whose lease expired is queued again with its backoff and is leased once `not_before` is reached, and the third failure is `unavailable`;
  - a failed attempt with a retried reason goes back to `pending` with the backoff and keeps its reason, and a reason that is not retried is `unavailable` at once;
  - a GitHub failure counts no failure and no attempt, keeps the job `pending` with `github-unavailable`, and is final only once `github_since` is older than the wait, a successful lease in between not clearing it;
  - `no-executor` reaches every `pending` job only when no executor was seen for the wait, and never because of a job's age;
  - a receipt job for `no-merge-base`, `executor-not-configured` or `github-unavailable` is created already `unavailable` with no `base_sha`, and no other job may lack one;
  - a heartbeat extends, and a heartbeat with another lease, after expiry or on a final job is `ErrLeaseLost`;
  - a final row cannot be updated or deleted, and no identity column can be updated (PostgreSQL triggers, as the owner role).
- Results:
  - `Complete` stores the result, the tests and the state together or not at all, and stores nothing for a job that is queued again;
  - the same body twice is stored once, including after the lease ended and the job is final, and another body for a finished job is `ErrConflict`;
  - a canonical job refuses a row with `base_runs`, and a receipt job refuses a row without one;
  - an invalid code, a count over its bound or a duplicate `(package, name)` writes nothing.
- `pr_receipt_checks`: the rules of the `Publish` conformance of `pr_checks`, with `neutral` as a fourth state and `unblocked` in the identity.
- `pr_checks`: a change of `unblocked` alone advances the generation, and a row from before `00021` reads `false`.
- `pr_commands`:
  - the five dispositions are accepted and none of them carries an operation id;
  - `mode-set` without a mode is refused;
  - `LatestOwnerCommand` ignores all five: after an `approve` with a wrong code, a `mode` or an `unblock` with a valid code keeps `owner-command-rejected`;
  - `PreviewMode` returns the latest `mode-set`, honors `since`, and ignores another pull.
- `execution_unblocks`: written with its `pr_commands` entry or not at all; `Unblocked` is true only for a job that a row names, honors `since`, and is false for another job of the same head; a row for a job that is not `unavailable`, or of another head, is refused.
- `approval_receipts` is written with its `pr_commands` entry or not at all, and refuses `none` or `not-applicable` with a job, and `pending`, `complete` or `unavailable` without one.

## Cycle start (M1.7c; normative)

**`internal/application/githubsync`.**
- `Cycle` gains:
  - `StartedAt time.Time`, GitHub's `Date` of the cycle's first response; required, so a zero value is `ErrInvalid` in `Validate`;
  - `Requests int`, at least 1;
  - `Changed bool`.
- `RepositoryState` gains `CycleStartedAt time.Time`, which is zero until a cycle has recorded one.
- `RecordCycle` stores `StartedAt` in the same transaction as `last_success`. A later cycle with an earlier `StartedAt` is still recorded, because the value is the cycle's own. The horizon takes the minimum over repositories, never over cycles.
- `RecordFailure` leaves `cycle_started_at` unchanged, as it leaves `last_success`.
- `Requests` and `Changed` are not stored.
- `RecordCycle(ctx, cycle) ([]int64, error)` returns the ids of the comments that this call inserted for the first time, in ascending order. A comment it already had is never returned.
- Conformance: a second identical `RecordCycle` returns no ids.

**Migration `00018_cycle_start.sql`** (forward-only):

```sql
ALTER TABLE github_repositories ADD COLUMN cycle_started_at timestamptz;
```

- `SupportedVersion` is 18.
- `UpsertRepositorySuccess` sets `cycle_started_at = excluded.cycle_started_at`.
- The repository read selects it.

**Conformance (fake and PostgreSQL):**
- `StartedAt` round-trips, microsecond-truncated as `last_success` is.
- A zero `StartedAt` is `ErrInvalid` and writes nothing.
- `RecordFailure` keeps the stored start.
- A row from before `00018` reads a zero `CycleStartedAt`.

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

## Tables (`00001_governance.sql` from R1; `00002`–`00009` vendor River, `00010` adds the outbox in M1.2, `00011` the GitHub observations in M1.4, `00012` the owner and protection results in M1.5, `00013` the PR command ledger in M1.6a, `00014` the PR check ledger in M1.6b, `00015` merges and canonical integrity in M1.7, `00016` the origin kind and outcomes without a canonical in M1.7b, `00017` the propose and skip dispositions in M1.7b, `00018` the cycle start in M1.7c, `00019` the suggest dispositions in M1.9, `00020` the other-pull disposition, `00021` execution in M2.1)

| Table | Key | Mutability | Main columns |
| --- | --- | --- | --- |
| `suites` | (project_id, suite_id) | update only via revision +1 trigger; no delete | revision bigint, current_version_id (deferred FK), target_id, policy_revision_id |
| `policies` | (project_id, revision_id) | immutable | owner principal id and kind |
| `suite_versions` | (project_id, suite_id, version_id) | immutable | manifest_digest, manifest jsonb |
| `proposals` | (project_id, suite_id, proposal_id) | immutable | carrier_id |
| `proposal_revisions` | (project_id, suite_id, proposal_id, revision_id); unique seq | immutable | seq, origin, carrier, manifest/scope digests, covered_inputs jsonb, expected_version_id, policy_revision_id, origin_kind (M1.7b: `branch`, `pull`) |
| `assessments` | (project_id, suite_id, proposal_id, revision_id, source) | immutable | evidence emitter, evidence source, evidence revision id (FK to `proposal_revisions` of the same proposal; the evidence binding is rebuilt from it, so evidence bound to another proposal is not representable and is rejected by the writer), outcome; all four null = missing evidence, otherwise all non-null |
| `consent_results` | source_command_id | append-only | operation_id, proposal, revision, actor, carrier, action, command_order, outcome, reason, seq |
| `operations` | operation_id (global) | append-only | project_id, suite_id, kind, source_command_id (consent kinds, FK to `consent_results`), receipt jsonb (immutable receipt payload with explicit json tags) |
| `promotions` | operation_id (unique; no FK, because seeded history has no receipt) | immutable | version_id (FK to `suite_versions`), proposal_id + revision_id (FK to `proposal_revisions`, which holds the binding), carrier, source, target, recorded_at, corrects_version_id; unique version and unique reference |
| `outbox` (M1.2) | key (global) | immutable except delivery state: `state` (`pending`, `delivered`, `failed`), `attempts`, `next_attempt_at`, `last_error`, `finished_at`; terminal states never change; no delete | project_id, suite_id, kind, payload jsonb, created_at |
| `github_repositories` (M1.4) | repo (`owner/name`) | observation state; the row is the per-repository lock of a cycle | installation_id, last_success, last_error (enumerated `FailureKind`), last_status, wait_until, closed_watermark (M1.7, GitHub's time) |
| `github_pulls` (M1.4) | (repo, number) | observation state; no delete | head_sha, head_ref, base_ref, base_sha, updated_at, first_seen, last_seen, not_open_since; M1.7: head_repo, resolved_for, merged, merged_at, merge_commit_sha, resolved_at, merge_processed |
| `owner_enrollment` (M1.5) | singleton | replaced by `setup` | generation, account_id, login, secret bytea (20 bytes), tier, policy_revision_id, enrolled_at |
| `owner_test_checks` (M1.5) | (repository_id, policy_revision_id) | insert-only | project_id, repo, test_check; FK to `policies` |
| `totp_consumed_steps` (M1.5) | (account_id, step) | insert-only, no delete | consumed_at, source_command_id (M1.6a: the command that used the step; `setup:<generation>` for setup) |
| `pr_commands` (M1.6a) | (repository_id, comment_id) | insert-only | pull_number, author_id, created_at, body_digest, disposition (codes), operation_id (exactly when `processed`), decided_at; M2.1: mode (`fix`, `feature`, `refactor`, when the command carried one) |
| `suiteward_instance` (M1.6a) | singleton | immutable | instance_id (32 lowercase hex, random) |
| `pr_checks` (M1.6b) | (repository_id, pull_number) | current state, updated in place; no delete, no truncate | head_sha, head_since, state (`success`, `in_progress`, `failure`), reason, reference, change, revision, command_id, generation, message_key, decided_at; M2.1: unblocked |
| `pr_merges` (M1.7) | (project_id, suite_id, carrier) | immutable | kind, reason, source, version_id (nullable since M1.7b: empty exactly for `blocked` and `before-canonical` decided without a canonical), corrects_version_id, proposal_id, revision_id, recorded_at |
| `canonical_integrity` (M1.7) | (project_id, suite_id) | latest verdict, replaced (`unavailable` never replaces `failed` for one version) | version_id, verdict (`passed`, `failed`, `unavailable`), checked_at |
| `github_protection_results` (M1.5) | repository_id | latest result, replaced | result, findings text[], caveats text[], checked_at |
| `github_comment_versions` (M1.4) | (repo, comment_id, updated_at, body_digest) | insert-only except `not_seen_since`, by trigger; no delete | pull_number, author_id, author_login, created_at, body bytea, body_digest (checked against the bytes), first_seen, not_seen_since |
| `execution_jobs` (M2.1) | job_id (identity); unique job_key among jobs that are not `cancelled` | open jobs change by guard trigger (state, reason, lease, attempts, failures, github_since, not_before); final rows never change; no delete, no truncate | repository_id, pull_number, plan (`canonical`, `receipt`), profile, head_sha, base_sha, governing_digest, governing_source, declaration_digest, canonical_level, receipt_level, unapproved_paths, changed_paths, changed_total, state, reason, attempts, failures, github_since, not_before, lease_id, lease_expires_at, leased_at, deadline_at, created_at, finished_at |
| `execution_results` (M2.1) | job_id | insert-only | status, reason, executor_version, executor_sha256, image_digest, toolchain, started_at, ended_at, received_at, body_digest, markers jsonb, submodules |
| `execution_tests` (M2.1) | (job_id, package, name) | insert-only | file, kind, excluded_reason, head_runs, base_runs, state |
| `execution_unblocks` (M2.1) | (repository_id, comment_id, job_id) | insert-only | pull_number, head_sha, decided_at |
| `pr_receipt_checks` (M2.1) | (repository_id, pull_number) | current state, updated in place; no delete, no truncate | head_sha, head_since, state (`success`, `in_progress`, `failure`, `neutral`), reason, reference, job_id, unblocked, generation, message_key, decided_at |
| `approval_receipts` (M2.1) | (repository_id, comment_id) | insert-only | head_sha, receipt_level, run_state, job_id, summary, unblocked, decided_at |

River's own tables (`river_job` and the rest) are created by vendored goose migrations and owned by River.

All enumerations are `text` with `CHECK` constraints. Immutable and append-only tables reject `UPDATE`, `DELETE` and `TRUNCATE` by trigger (`github_comment_versions` accepts an `UPDATE` of `not_seen_since` only). `migrations.SupportedVersion` is the single exported schema version used by both the migrator and the schema-readiness check.

## Database roles and grants (SH-H4)

Two roles use the database. The **owner** owns every object (today the bootstrap role `suiteward`) and is used only by `suiteward migrate`. The **application role** is what `serve`, `setup` and `probe` connect as: the user of `DATABASE_URL` (`suiteward_app` in the shipped layout), which is also the role `suiteward migrate` grants to. It must be least-privilege: `migrate` refuses, before it changes anything, the migration role or a member of it, a superuser, a role with `BYPASSRLS`, the owner of the schema or a member of its owner, and the owner of a table in the schema. Whoever holds the application credential can change data but cannot defeat the immutability triggers: that needs the owner.

`migrations.GrantApp(ctx, ownerURL, role)` gives the role, in the connection's `current_schema()` (never a hard-coded `public`), in one transaction and idempotently:

- `USAGE` on the schema;
- `SELECT, INSERT, UPDATE, DELETE` on all tables, then `INSERT, UPDATE, DELETE` revoked on `goose_db_version`, which it only reads (the schema version is the startup gate; `schema_version()` reads it as the caller);
- `USAGE, SELECT` on all sequences;
- `ALTER DEFAULT PRIVILEGES FOR ROLE <owner> IN SCHEMA <schema>` granting the same on tables and sequences the owner creates later, so a migration that was applied but not yet granted is still covered.

It grants nothing else: no `TRUNCATE`, `TRIGGER`, `REFERENCES`, `CREATE` or ownership (every run revokes `TRUNCATE`, `TRIGGER` and `REFERENCES` on the schema's tables, so the role converges to this set), so the role cannot `ALTER TABLE ... DISABLE TRIGGER`, `DROP TRIGGER`, `TRUNCATE` or create objects, and it is not a superuser, so `SET session_replication_role` fails; each is a `42501` (insufficient privilege). The statements the store runs on the enrollment and queue tables (`LOCK TABLE owner_enrollment IN SHARE ROW EXCLUSIVE MODE`, row locks, `FOR UPDATE SKIP LOCKED`) and River's own maintenance need no more. River's reindexer, which does (`REINDEX INDEX CONCURRENTLY` needs the owner), is disabled.

The role is not created by the program: `CREATE ROLE` needs a password, which the deployment holds. `GrantApp` fails, naming it, when the role does not exist, and when it is not least-privilege, as listed above (`CheckAppRole`, which `migrate` runs first). A deployment creates it once, as the owner, with the password typed or piped into `psql` on its standard input, never as a command-line argument (an argument shows in the process list and the shell history):

```sql
CREATE ROLE suiteward_app LOGIN;
\password suiteward_app
```

The grants assume PostgreSQL 15 or later, where `PUBLIC` has no `CREATE` on schema `public`. On an older server the role could create objects there through `PUBLIC`: run `REVOKE CREATE ON SCHEMA public FROM PUBLIC;` yourself (`GrantApp` does not change it). River's tables and the history tables stay owned by the owner either way.

The integration tests create one such role for the cluster and run the PostgreSQL conformance suites, the River suites and the application suites connected as it, after the owner migrated the schema and `GrantApp` ran (`internal/adapters/postgres/pgtest`); suites that tamper with stored facts or change the schema (`ALTER TABLE`, `TRUNCATE`, `DISABLE TRIGGER`) run those statements as the owner.

## Behaviors that must stay covered

The conformance suite (fake and PostgreSQL) names each of these: replay by operation id and by source command id; the same operation id in another Suite or kind → conflict; a source command replayed by another actor → conflict; an alias advances the revision; rejected results do not consume command order; full rollback when `fn` fails; canceled context; version conflict; and the write guard (`write_guard_conformance.go`), where each invalid write is refused and leaves the visible state unchanged. For `AppendConsent` that is a command of another Suite, a receipt of another source command, no operation id, a result without a command (all `ErrInvalidRequest`), and a duplicate result that is not an alias, an alias of a result that is not a duplicate and an operation id other than the command's (all `ErrOperationConflict`). For `RecordPromotion`, all `ErrInvalidRequest`, it is a promotion of another Suite, a receipt request naming another operation, a receipt identity correcting another version or bound to another revision, a record time finer than a microsecond, no record, no version id, and a version of another Suite under a record of this one. PostgreSQL integration tests add: concurrent merges and concurrent bootstraps (exactly one version is recorded), rollback when any statement fails, triggers rejecting history mutation, and missing or corrupt content rejected when a version is written.
