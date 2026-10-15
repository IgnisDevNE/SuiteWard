# Governance persistence contract

- **Status:** frozen for phase R1 (replaces the M1.01 contract `m1-01.md`); extended for phase M1.2 by "Jobs and outbox" below (frozen by M1.2-C0) and for phase M1.3 by "Proposal revisions" (frozen by M1.3-C0) and "Baseline proposals" (frozen before M1.3-C2) below, and for phase M1.4 by "GitHub observations" (frozen by M1.4-B0).
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

	// Enqueue and Outbox are written in the same transaction as the facts
	// above (M1.2); see "Jobs and outbox".
	Enqueue(context.Context, Job) error
	Outbox(context.Context, OutboxMessage) error
}

// Seeder writes trusted initial state: policies, assessments and historical
// versions. It is no longer their only writer: the owner enrollment writes
// policies (Enroll), CreateSuite creates Suites and RecordAssessment records
// assessments (M1.6a); proposals also have AppendProposalRevision (M1.3).
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

Use cases keep their names: `ProcessConsent(ctx, UnitOfWork, ConsentRequest)`, `Promote(ctx, UnitOfWork, PromoteRequest)`, `Bootstrap(ctx, UnitOfWork, PromoteRequest)` (existing-baseline only), `Correct(ctx, UnitOfWork, CorrectionRequest)`, and since M1.3 `ReviseProposal(ctx, UnitOfWork, ReviseRequest)` (see "Proposal revisions"). Errors from the domain or the store are wrapped with `%w`, preserving the cause.

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
- A job's terminal state is River's `discarded` (retries exhausted) or `completed`; both stay in `river_job` and are counted by the runtime status endpoint until River's cleaner removes them. The runtime sets no retention, so River v0.49.0's defaults apply: completed and cancelled jobs are removed after 24 hours and discarded jobs after 7 days. The job counts of `/status` therefore describe the retained jobs, not the history.
- `outbox.project_id` and `outbox.suite_id` are both set (a message written through `Tx.Outbox`) or both null (a system message, for example the diagnostic `probe` written through the non-port `Store.EnqueueSystem`, which reuses the same insert statements without taking a Suite lock); a `CHECK` enforces it.
- The outbox row is the single owner of delivery state (it is not mirrored by a River job per message). A relay, itself a periodic River job that runs at startup and then on `OUTBOX_POLL_INTERVAL`, claims due rows with `FOR UPDATE SKIP LOCKED`, increments `attempts`, sets `next_attempt_at` to now plus `OUTBOX_LEASE` and commits; it then calls the publisher registered for the row's kind outside any transaction. An outcome update is conditional on the claim (`WHERE key = $1 AND state = 'pending' AND attempts = <attempts at claim>`): when a publish outlasts the lease and another relay has reclaimed the row, the late outcome updates nothing and is logged, never an error and never overwriting the newer claim. Success sets `state = delivered`. An error sets `last_error`; at `OUTBOX_MAX_ATTEMPTS` the state becomes `failed` (terminal, visible), otherwise `next_attempt_at` is now plus a capped exponential backoff. A crash between claim and outcome leaves the row pending until the lease expires, so it is redelivered. A row whose kind has no registered publisher stays pending. Until M1.7 it failed at once; M1.7 changes that:
- the relay releases the claim, restoring the attempt, and defers the row by the backoff cap;
- it logs a Warn naming the kind, once per kind per process;
- so a target switched off GitHub, or a publisher registered later, never loses a message.

Retried delivery never reverses a committed fact.
- River schema: River's own migrations are vendored into goose migrations (one migrator and one readiness check, ADR 0026), so `migrations.SupportedVersion` is the highest goose version and covers the River tables and the outbox. A test checks that a database migrated by goose passes River's own migrator validation. Upgrading River means adding a goose migration.

The conformance suite gains: rollback removes the job and the outbox row together with the facts; a duplicate key conflicts; a job and a message written in a unit of work that writes nothing else are committed; neither changes the revision.

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

type ReviseRequest struct {
	Revision contract.ProposalRevision // its binding's reference names the proposal and the new revision id
}

type ReviseResult struct {
	Current   contract.ProposalReference // the proposal's current revision after the call
	Committed bool                       // false when nothing was written
}
```

Use case `ReviseProposal(ctx, UnitOfWork, ReviseRequest) (ReviseResult, error)`, inside `Do`:

1. The reference's project and Suite must be the locked ones, the binding's policy revision the governing one, and its expected canonical the Suite's current version (no version before bootstrap): a revision is always bound to the state it would be approved against. Otherwise `ErrInvalidRequest`, nothing written.
2. Load the proposal. `ErrNotFound` → `contract.NewProposal(revision)` and `AppendProposalRevision`.
3. When the current revision has the same proposal context (project, Suite, proposal) and carrier, and the same coverage (manifest digest, scope digest, covered inputs, expected canonical and policy revision; the origin and the revision id may differ), nothing is written and `Current` is the existing revision: repeated observation and implementation-only pushes create no revision (ADR 0013, D-APPROVAL-CONTEXT). Another carrier never takes this path.
4. Otherwise `Proposal.Revise(revision)`; its errors are wrapped with `%w` and nothing is written (another carrier is `contract.ErrProposalContextMismatch`, a reused revision id is `contract.ErrRevisionExists`). Then `AppendProposalRevision`.

A revision carries no operation id: identity is the revision id inside its proposal, and a retried request either matches step 3 or fails in step 4.

Store write semantics of `AppendProposalRevision`: insert the proposal row when it is absent, then the revision with the next sequence number of its proposal; bump the Suite revision once. A reference outside the locked Suite is `ErrInvalidRequest`. A revision id that already exists, or a carrier that differs from the stored proposal's, is `ErrProposalConflict` (the use case rules both out first, so either means a broken caller). Reads of the proposal inside the same unit of work see the new revision.

The conformance suite gains: the first revision creates the proposal; a second revision becomes current and older ones stay resolvable; a duplicate revision id and another carrier conflict; a foreign Suite is rejected; each append advances the revision by exactly one; rollback removes the proposal and its revisions. The use-case tests add: an unchanged coverage writes nothing; the same coverage under another carrier is rejected, not ignored; a policy or canonical mismatch is rejected; and a proposal blocked by `PromotionReasonCanonicalChanged` is revised against the new canonical and promotes only after a fresh exact approval of the new revision (R1 used a stand-in proposal for this).

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
3. Inside one `Do`: the Suite must have no current canonical (otherwise `ErrInvalidRequest`: a baseline is only for bootstrap). Build the binding from the reference, `ExpectedCanonical` empty, the inventory's manifest digest and scope digest, the governing policy revision, and covered inputs `{}`; `contract.NewProposalRevision(binding, Origin, Carrier)`. Then apply exactly the logic of `ReviseProposal` steps 1–4, through one unexported function shared by both use cases, so a repeated baseline with the same coverage writes nothing.

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

- **Outbox inside the use case.** `ConsentRequest`, `BaselineRequest` and `PromoteRequest` gain `Outbox []OutboxMessage`. `ProcessConsent`, `ProposeBaseline`, and `Promote`/`Bootstrap`/`Correct` write it inside their own transaction, after their facts. Nothing is written when there are no new facts: a duplicate consent (replay by operation id or source), a baseline with unchanged coverage, or a promotion replay. Because the message is written in the same transaction as its facts, an acknowledgment never reports what did not commit (ADR 0011). The payloads are defined in the [runtime contract](runtime.md#command-and-bootstrap-jobs-m16a) (outbox kind `github_comment`).
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
	NotCommand      Disposition = "not-command"     // no /suiteward line, or SuiteWard's own comment
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

  A crash between any two steps repeats the earlier steps harmlessly on the next run. Rejected dispositions (`malformed`, `invalid-code`, `replayed-code`, `no-suite`, `unknown-proposal`, `edited-first`) go through `Record` with the acknowledgment. `not-command`, `ignored-author`, `before-enrollment` and `failed` record nothing to publish.
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
	// message ended `failed` in the outbox, so a lost publication is retried. It returns the stored record and
	// whether a message was written.
	Publish(ctx context.Context, r Result, at time.Time, message func(generation int64) (governance.OutboxMessage, error)) (Record, bool, error)
}
```

- **Table `pr_checks`.** Primary key (repository_id, pull_number). The row is current state, updated in place; the publications themselves are the outbox rows. `generation` only grows; `state` is a CHECK on the three codes; `head_sha` is 40 lowercase hex.
- **Owner commands.** `commands.Store` gains `LatestOwnerCommand(ctx, repositoryID int64, pull int, ownerID int64) (Entry, bool, error)`: the owner-authored ledger entry with the greatest comment id on that pull among the command dispositions (`malformed`, `invalid-code`, `replayed-code`, `unknown-proposal`, `edited-first`, `no-suite`, `processed`). `not-command`, `failed` and `before-enrollment` never count, so an ordinary comment never clears an owner-command `failure`.
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
  - `RecordClosed` keeps a per-repository watermark (`github_repositories.closed_watermark`), which is GitHub's greatest `updated_at` seen in the listing.
  - The first call for a repository only sets the watermark.
  - A later call inserts each merged item with `updated_at` above the watermark and no `github_pulls` row: a new row with `first_seen = last_seen = not_open_since = at` and no resolution, which the merges job then resolves with `Reader.Pull`. Then it raises the watermark.
  - Existing rows and unmerged items are left alone.
  - Times from GitHub are compared only with times from GitHub.
- **Processed.** `MarkProcessed` sets `merge_processed`.
  - It is a read optimization. The merge outcome in the governance store (below) is the authority, and a repeated outcome is a replay.
  - A failure to mark only costs a replay on the next run.
- **Serialization.** These writes lock the repository row like `RecordCycle`.
- **Conformance.**
  - Absence, then resolution, then reopen, then a new absence that is unresolved.
  - A stale resolution writes nothing.
  - The closed listing: first call sets the watermark; an unseen merged item is inserted unresolved; a seen pull and an older item are ignored.
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
   - a truncated tree or a read error is returned wrapped, and nothing is written;
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
      - when an earlier version (`V1`..`V<n-1>`, read with `Tx.Version`) has the integrated manifest digest, decide a correction with target the current version (the version being corrected); otherwise decide a promotion;
      - use operation id `merge:<carrier>` and an `IntegrationMergedChange` integration with source `Source`, carrier `Carrier` and the Suite's target;
      - decide inside this same `Do`, sharing `runPromotion`'s decision and write logic, which is factored into a function over `Tx`. The behavior of `Promote` and `Correct` is unchanged.

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
3. **Write.** `Tx.RecordMergeOutcome` and, when `Message` returns true, `Tx.Outbox`, in the same transaction. A blocked or no-change outcome does not advance the Suite revision; a promotion does, through `RecordPromotion`.

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

## Tables (`00001_governance.sql` from R1; `00002`–`00009` vendor River, `00010` adds the outbox in M1.2, `00011` the GitHub observations in M1.4, `00012` the owner and protection results in M1.5, `00013` the PR command ledger in M1.6a, `00014` the PR check ledger in M1.6b, `00015` merges and canonical integrity in M1.7)

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
| `github_repositories` (M1.4) | repo (`owner/name`) | observation state; the row is the per-repository lock of a cycle | installation_id, last_success, last_error (enumerated `FailureKind`), last_status, wait_until, closed_watermark (M1.7, GitHub's time) |
| `github_pulls` (M1.4) | (repo, number) | observation state; no delete | head_sha, head_ref, base_ref, base_sha, updated_at, first_seen, last_seen, not_open_since; M1.7: head_repo, resolved_for, merged, merged_at, merge_commit_sha, resolved_at, merge_processed |
| `owner_enrollment` (M1.5) | singleton | replaced by `setup` | generation, account_id, login, secret bytea (20 bytes), tier, policy_revision_id, enrolled_at |
| `owner_test_checks` (M1.5) | (repository_id, policy_revision_id) | insert-only | project_id, repo, test_check; FK to `policies` |
| `totp_consumed_steps` (M1.5) | (account_id, step) | insert-only, no delete | consumed_at, source_command_id (M1.6a: the command that used the step; `setup:<generation>` for setup) |
| `pr_commands` (M1.6a) | (repository_id, comment_id) | insert-only | pull_number, author_id, created_at, body_digest, disposition (codes), operation_id (exactly when `processed`), decided_at |
| `suiteward_instance` (M1.6a) | singleton | immutable | instance_id (32 lowercase hex, random) |
| `pr_checks` (M1.6b) | (repository_id, pull_number) | current state, updated in place; no delete, no truncate | head_sha, head_since, state (`success`, `in_progress`, `failure`), reason, reference, change, revision, command_id, generation, message_key, decided_at |
| `pr_merges` (M1.7) | (project_id, suite_id, carrier) | immutable | kind, reason, source, version_id, corrects_version_id, proposal_id, revision_id, recorded_at |
| `canonical_integrity` (M1.7) | (project_id, suite_id) | latest verdict, replaced (`unavailable` never replaces `failed` for one version) | version_id, verdict (`passed`, `failed`, `unavailable`), checked_at |
| `github_protection_results` (M1.5) | repository_id | latest result, replaced | result, findings text[], caveats text[], checked_at |
| `github_comment_versions` (M1.4) | (repo, comment_id, updated_at, body_digest) | insert-only except `not_seen_since`, by trigger; no delete | pull_number, author_id, author_login, created_at, body bytea, body_digest (checked against the bytes), first_seen, not_seen_since |

River's own tables (`river_job` and the rest) are created by vendored goose migrations and owned by River.

All enumerations are `text` with `CHECK` constraints. Immutable and append-only tables reject `UPDATE`, `DELETE` and `TRUNCATE` by trigger (`github_comment_versions` accepts an `UPDATE` of `not_seen_since` only). `migrations.SupportedVersion` is the single exported schema version used by both the migrator and the schema-readiness check.

## Behaviors that must stay covered

The conformance suite (fake and PostgreSQL) names each of these: replay by operation id and by source command id; the same operation id in another Suite or kind → conflict; a source command replayed by another actor → conflict; an alias advances the revision; rejected results do not consume command order; full rollback when `fn` fails; canceled context; version conflict. PostgreSQL integration tests add: concurrent promotions (exactly one wins), rollback when any statement fails, triggers rejecting history mutation, and missing or corrupt content rejected when a version is written.
