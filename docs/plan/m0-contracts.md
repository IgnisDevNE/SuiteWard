# M0 agent contract brief

This is an implementation-planning brief, not a new ADR or a claim that the product is implemented. Versioned ADRs remain canonical. M0 uses Go standard-library domain/application code and controlled values, with no filesystem, PostgreSQL, GitHub, HTTP server, River, or execution backend. Each phase results in one PR; tasks include their own behavior and tests.

## Contract checkpoints and parallel starts

Each phase has a C0 checkpoint that records reviewed consumed signatures, representative inputs/outputs, errors, invariants, and shared-file ownership. A checkpoint need not wait for the preceding phase to merge. It may consume its reviewed predecessor checkpoint, but its consumers reconcile any subsequent signature changes before integration.

C0 does not create empty production packages or a general-purpose interface framework. The first go.mod is integrated with real domain source and tests in M0.01. No artificial test package is added merely to activate coverage. Contract consumers can start implementation and tests after signatures are reviewed, then select prerequisite commits for compilation. They must not ship duplicate production declarations or fake dependency implementations to avoid integration.

Task dependencies distinguish start from merge. The phase-level merge_after sequence remains F0 -> F0.01 -> F0.02 -> M0.01 -> M0.02 -> M0.03 -> M0.04. A phase integrator combines its tasks, verifies the result, and opens one phase PR using the authorized bot. Human approval is required for each merge.

## M0-C1: values and authority

### Reviewed M0.01 checkpoint

The following consumed contract is frozen for M0.01 on 2026-09-30, following independent review by the artifact, authority, and canonical-values workers. Later checkpoints extend it through reviewed changes. Start prerequisite F0.02-I is satisfied by main `7bcec8a684ce5e24426b198506e4bb137ab14a17`, its passing Windows/Linux foundation and evidence checks, and the preserved F0.01 checkpoint ancestry.

| Task | Owner | Branch / worktree suffix | Owned declarations |
| --- | --- | --- | --- |
| M0.01-C0 | root; reviewed by all three workers | `phase/M0.01` / `m0.01` | This contract checkpoint |
| M0.01-A | m001_artifact | `task/M0.01-A` / `m001-artifact` | `artifact` digest, entry, manifest and adjacent tests |
| M0.01-B | m001_authority | `task/M0.01-B` / `m001-authority` | `ProjectID`, `PrincipalID`, principal kind/value, `PolicyRevisionID`, policy and adjacent tests |
| M0.01-C | phase_domain_plan | `task/M0.01-C` / `m001-snapshots` | `SuiteID`, `SuiteVersionID`, `StateRevision`, suite/version snapshots and adjacent tests |
| M0.01-I | root; independently reviewed | `phase/M0.01` / `m0.01` | `go.mod`, integration evidence, coordinated CI configuration only when real activation requires it |

Worktrees reside under `D:/Repos/SuiteWard-worktrees/`; each owns tooling and caches. C0 is documentation-only. A, B, C, and I require observed TDD evidence. I delegates initial `go.mod` creation to A alongside the first real test/source checkpoint, then owns its final integration. The module is `github.com/IgnisDevNE/SuiteWard`, Go `1.27.1`, standard library only; no `go.sum` is needed without module dependencies. I observes the initial real component failure through the whole-module command before combining the passing components; this verifies composition, not an invented extra production capability. Task histories are merged intact.

#### Artifact API and encoding v1

`artifact.Hash(content []byte) Digest` hashes the exact bytes with SHA-256. A `Digest` is comparable, has private state, exposes `String()` as `sha256:` plus lowercase hexadecimal, and `IsZero()` for an absent value. The zero value has no identity and its string is empty; hashing an empty byte sequence yields a valid identity.

`Entry` contains `Path string` and `Content Digest`. `NewManifest(entries []Entry) (Manifest, error)` validates and copies the inventory. `Manifest.Entries()` returns a defensive copy sorted by the raw UTF-8 path bytes; `Digest()` returns its identity and `IsZero()` distinguishes the unconstructed value from a valid empty inventory.

The manifest digest is SHA-256 over these exact concatenated bytes:

1. ASCII `suiteward.manifest.v1` followed by one NUL byte.
2. Entry count as an unsigned 64-bit big-endian integer.
3. For each sorted entry: UTF-8 path byte length as unsigned 64-bit big-endian, unchanged path bytes, and the raw 32-byte content digest.

No JSON serialization, delimiter escaping, Unicode normalization, or line-ending conversion participates. Empty/nil content is equivalent. Nil/empty inventories are equivalent valid manifests. Sorting cannot modify the caller's input. Path identity is case-sensitive.

Reject empty or invalid UTF-8 paths, NUL, backslashes, colons, absolute/leading or trailing slashes, empty slash-separated components, `.`/`..` components, exact duplicate paths, and absent content digests. Return errors recognizable with `errors.Is`: `ErrInvalidPath`, `ErrDuplicatePath`, or `ErrInvalidDigest`. Reject rather than normalize. Symlinks, device names, case collisions and filesystem extraction remain adapter work; this value validation does not claim portable filesystem safety.

Independent SHA-256 golden vectors (hex digest, excluding the display prefix):

| Input | Expected digest |
| --- | --- |
| Content `abc` | `ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad` |
| Empty manifest | `ff3f0b17ac399c212f3042c9245011cac8025a8ee5600ac4191df021339b8b15` |
| `tests/a_test.go` with content `abc` | `706968403fd8612b8bb22f79e659287097e0165687d01bdf1b54bc0f79557fb6` |
| Previous entry plus `tests/b_test.go` with empty content | `6840a6b34ebc7a3e9bd2b9b1b9437f9b23af4b8be0dffea6b284c9ef839506d4` |

#### Authority API

IDs are distinct string-based domain types, not provider IDs. Constructors reject empty or whitespace-only IDs and preserve accepted bytes without trimming or normalization. `NewPrincipal(id PrincipalID, kind PrincipalKind) (Principal, error)` accepts only `Human`, `Agent`, or `Service`, exposes `ID()` and `Kind()`, and rejects invalid input with `ErrInvalidPrincipal`.

`NewPolicy(project ProjectID, revision PolicyRevisionID, owner Principal) (Policy, error)` requires nonblank identifiers and a valid human owner; invalid input returns `ErrInvalidPolicy`. It exposes `ProjectID()`, `RevisionID()`, and `OwnerID()`. Its boolean capability methods are `CanApprove(actor Principal)`, `CanAdminister(actor Principal)`, `CanRequestPriority(actor Principal)`, `CanAuthorizePolicyChange(actor Principal)`, and `CanRevoke(actor Principal, approvalAuthor PrincipalID)`.

The governing policy receiver permits only its registered human owner. Unconstructed values, a different human, agents and services fail closed, including an agent/service carrying the same ID string as the owner. Revocation additionally requires the actor to be the approval author. Proposal authorship is not a disqualifier and repository administration is not an input. One eligible owner's consent suffices under the MVP, but these capability checks do not create consent or prove proposal eligibility.

Evaluate a proposed policy change with the current policy's `CanAuthorizePolicyChange`; the candidate policy is never substituted as the governing receiver. Tests compare a candidate whose owner would authorize themselves with the current policy that rejects them. This introduces neither policy application nor owner-transfer/recovery behavior. Trusted application/adapters resolve external authentication and choose the governing policy; arbitrary principal construction is not authentication.

#### Suite and version snapshot API

`StateRevision` is an explicit unsigned 64-bit value supplied by the caller; zero is a valid initial revision. It is concurrency context, not an inferred timestamp, and this phase does not increment or persist it.

`NewSuite(project ProjectID, id SuiteID, current SuiteVersionID, revision StateRevision) (Suite, error)` requires nonblank project/suite IDs. Empty `current` means no canonical version; a nonempty whitespace-only version ID is invalid. Getters are `ProjectID()`, `ID()`, `CurrentVersionID() (SuiteVersionID, bool)`, and `Revision()`. Invalid input returns `ErrInvalidSuite`.

`NewSuiteVersion(project ProjectID, suite SuiteID, id SuiteVersionID, manifest artifact.Manifest) (SuiteVersion, error)` requires all IDs to be nonblank and a constructed manifest. Getters are `ProjectID()`, `SuiteID()`, `ID()`, and `Manifest()`. Invalid input returns `ErrInvalidSuiteVersion`. A valid empty manifest is representable and remains different from an absent canonical pointer. Eligibility to bootstrap/promote an empty inventory belongs to later transitions, not snapshot constructors.

Snapshots expose no canonical setter, promotion, clock, random ID generation, or mutable shared inventory. Two version IDs may have the same manifest digest. Constructors represent/reconstitute supplied facts; they do not authorize their persistence as canonical state or verify that a referenced version exists. Tests preserve identity after mutating retained input entries and returned entries.

The implementation-revision approval binding decision remains open under D-APPROVAL-CONTEXT. This checkpoint does not choose it.

#### CI activation repair ownership

The first hosted run of real code exposed a coverage invocation defect: PowerShell split unquoted dotted options, so passing tests wrote `coverage` instead of the required `coverage.out`. M0.01-I therefore temporarily delegates `scripts/check-go.ps1`, its focused regression fixture `scripts/test-go.ps1`, and the foundation hook to `phase_domain_plan` in `task/M0.01-I-ci-fix` / `m001-ci-fix`. Root retains integration, backlog, and evidence ownership; `m001_authority` reviews the repair independently. The correction must preserve complete arguments and constrain coverage to the exact packages already discovered by `go list ./...`. A fake command-boundary regression checks invocation behavior; actual hosted Linux race/coverage remains required to prove the real compiler and upload path.

### Provider-neutral identity

Use distinct internal value identities for Project, Suite, SuiteVersion, ChangeProposal, ProposalRevision, Principal, PolicyRevision, Operation, and Integration as their implemented consumers require them. GitHub PR numbers, check IDs, payloads, login names, and PostgreSQL/sqlc values do not enter domain signatures.

Time, generated identifiers, source references, and other nondeterministic inputs enter explicitly. A constructor receiving an actor flag is not proof of external authentication: trusted application/adapters resolve authenticated external identity to an internal Principal before invoking domain authority rules.

### Artifact and manifest identity

Artifact code consumes bytes and content values; it does not open files. A manifest records exact protected inventory, paths, and content identity. It is immutable, deterministic across input ordering, and sensitive to path, inventory, and content changes.

M0.01-C0 freezes the versioned, unambiguous canonical encoding and independently calculated golden examples above. It uses SHA-256, a manifest domain/version envelope and unambiguous field boundaries, relative forward-slash paths, case-sensitive identity, and unchanged byte content. Do not silently normalize line endings or Unicode, rewrite dot segments, accept absolute paths, or merge duplicate entries.

This encoding is a reviewed M0.01-C0 implementation choice within the ADRs, not a previously accepted ADR decision. Extraction/materialization, symlinks, Windows filesystem collisions, wildcard declarations, and source discovery are adapter concerns with later acceptance work. M0 must not claim those are solved by deterministic hashing.

### Canonical values

Suite identifies an optional current SuiteVersion and concurrency context. Absence of a canonical pointer is distinct from a successfully established empty test contract.

A sealed SuiteVersion is immutable. Copy constructors, exposed slices/maps, and returned values cannot mutate stored identity. Distinct historical versions may reuse identical content-addressed bytes without collapsing logical version, approval, promotion, or audit identity.

Exact aggregate boundaries are implementation design work. They must not split invariants that need one commit into separately committed updates.

### MVP policy

A human project owner can approve eligible exact revisions, revoke their own approval, and request priority. An agent can propose, request synchronization, and inspect permitted results. Internal service principals are not human approvers.

One eligible owner approval satisfies the MVP threshold. An owner may approve their own PR. GitHub repository administration, PR authorship, candidate configuration, and an agent-supplied role do not grant authority.

Policy replacement is evaluated under the governing policy, never the weaker proposed policy. Broader roles, delegated revocation, owner recovery, and organization quorum are not introduced.

## M0-C2: proposals, consent, and evidence

### M0.02 consumed checkpoint

This checkpoint extends M0-C1 for M0.02. The integration base is main `808241c606daf08cf6f217fbc58e459d4f963cba`: PR #6 preserved the M0.01 checkpoint ancestry and passed all eight hosted CI jobs, with 100% reported coverage. All three consuming workers independently reviewed and accepted these signatures and examples before this C0 checkpoint was frozen. No M0.02 decision gate is open; D-APPROVAL-CONTEXT remains deferred to its named M1 consumers.

| Task | Owner | Branch / worktree suffix | Owned files |
| --- | --- | --- | --- |
| M0.02-C0 | root; reviewed by all three workers | `phase/M0.02` / `m0.02` | This contract checkpoint |
| M0.02-A | m001_artifact | `task/M0.02-A` / `m002-proposals` | `contract/proposal.go`, `binding.go`, and adjacent tests |
| M0.02-B | m001_authority | `task/M0.02-B` / `m002-consent` | `contract/command.go`, `consent.go`, and adjacent tests |
| M0.02-C | phase_domain_plan | `task/M0.02-C` / `m002-assessment` | `contract/evidence.go`, `assessment.go`, and adjacent tests |
| M0.02-I | root; independently reviewed | `phase/M0.02` / `m0.02` | Integration verification, execution records, and coordinated documentation |

All source paths above are under `internal/domain/`. Worktrees reside under `D:/Repos/SuiteWard-worktrees/`; tools and caches are checkout-local. A owns the shared proposal/binding declarations. B and C may prepare tests and independent behavior after C0, then merge A's reviewed prerequisite commits to compile their real consumers. They do not create duplicate declarations or dummy production dependencies. Their histories are merged intact. C0 and I are documentation/aggregate-verification work; A, B, and C require observed behavioral RED/GREEN cycles and independent review. I must reclassify any newly added executable behavior before implementing it.

#### Exact proposal values (A)

Distinct string identities are `ProposalID`, `ProposalRevisionID`, `SourceRevision`, and `ApprovalCarrierID`. `ProposalReference` is an input value with fields `ProjectID`, `SuiteID`, `ProposalID`, and `RevisionID`. Every consumer validates it; its zero value is never shorthand for current. Identifiers reject empty/whitespace-only input and preserve accepted bytes.

`BindingInput` has fields `Reference ProposalReference`, `ExpectedCanonical SuiteVersionID`, `Manifest artifact.Digest`, `Scope artifact.Digest`, `PolicyRevision PolicyRevisionID`, and `CoveredInputs map[string]string`. `NewApprovalBinding(BindingInput) (ApprovalBinding, error)` seals these values, copying the covered-input map. Empty `ExpectedCanonical` means explicit expected absence, never any version; nonempty blank identities are invalid. Manifest/scope must be constructed digests, policy/reference IDs nonblank, and covered-input keys/values nonblank. Nil and empty maps describe the same empty set. Accepted keys and values remain exact; their meaning is selected explicitly by the caller's governing contract.

Getters are `Reference()`, `ExpectedCanonical()`, `ManifestDigest()`, `ScopeDigest()`, `PolicyRevisionID()`, and `CoveredInputs()` (defensive copy). `IsZero()` distinguishes absence. `Equal(other ApprovalBinding) bool` compares every field and covered-input key/value exactly, independently of insertion order, and returns false when either binding is absent. Invalid construction returns `ErrInvalidBinding`.

`NewProposalRevision(binding ApprovalBinding, origin SourceRevision, carrier ApprovalCarrierID) (ProposalRevision, error)` requires a constructed binding and nonblank origin/carrier, exposing `Binding()`, `Origin()`, `Carrier()`, and `IsZero()`. Origin is the immutable source of the proposed inventory; carrier identifies the separate conversation/change hosting consent. They need not identify the same source or change. Origin is not automatically added to covered inputs or equated with every subsequent assessed source.

`NewProposal(initial ProposalRevision) (Proposal, error)` creates an immutable revision history. `Revise(next ProposalRevision) (Proposal, error)` returns a new value, preserving the receiver/history, requiring the same project/suite/proposal and carrier, and rejecting any reused revision ID. `Current() ProposalRevision` returns the current snapshot; `IsZero()` identifies absence. `Lookup(reference ProposalReference, carrier ApprovalCarrierID) (ProposalRevision, error)` addresses an exact known historical revision. `Resolve(reference, carrier)` additionally requires that revision to be current. Neither resolves a missing/stale reference to latest.

Recognizable errors are `ErrInvalidProposal` for zero/invalid construction or receivers, `ErrInvalidReference` for incomplete lookup references/carrier, `ErrProposalContextMismatch` for valid but foreign project/suite/proposal/carrier, `ErrUnknownRevision`, `ErrSupersededRevision` (Resolve only), and `ErrRevisionExists` (Revise). Check validity, context, existence, then current eligibility in that order. Local history prevents rebinding a revision ID within that proposal; durable global uniqueness and authenticated lookup are later application/persistence work. Logical revision IDs are not automatically content hashes.

The approval baseline identifies a logical canonical version or absence. Whole-authority concurrency revisions belong to M0-C4: equality of this binding alone cannot authorize a commit or prove that consent, policy, scheduling, or the canonical pointer is still current.

#### Ordered commands and immutable consent (B)

`OperationID` and `SourceCommandID` are distinct string types. `CommandOrder` is an unsigned 64-bit value, strictly positive and supplied as stable source order by a trusted caller. It is not a timestamp, observation order, or proof of authentication. The caller must normalize a consistent ordering context for commands in the aggregate; acquisition/persistence of that ordering is later adapter/application work.

`ConsentAction` has nonzero constants `ApproveConsent` and `RevokeConsent`. `CommandInput` contains `OperationID`, `SourceCommandID`, `Actor Principal`, `Reference ProposalReference`, `Carrier ApprovalCarrierID`, `Action ConsentAction`, and `Order CommandOrder`. `NewCommand(CommandInput) (Command, error)` rejects incomplete/invalid input with `ErrInvalidCommand`, seals the values, and exposes getters named after those fields. It represents previously authenticated facts; constructing a Principal or Command is not authentication.

`NewConsent(project ProjectID, suite SuiteID, proposal ProposalID) (Consent, error)` creates immutable empty state scoped to all three identities. `Consent.Apply(proposal Proposal, governing Policy, command Command) (Consent, CommandResult, error)` returns the next state without mutating the receiver. Invalid/foreign aggregate, proposal, command, or governing-project context fails without recording history (`ErrInvalidConsent` or `ErrInvalidCommand`). A well-formed in-scope business rejection is instead a recorded result, returned with its next state and nil error. The caller must commit that state and outcome together; M0.02 does not persist either.

`ConsentOutcome` constants are `ConsentApproved`, `ConsentRevoked`, `ConsentNoActiveApproval`, and `ConsentRejected`. `ConsentReason` is `ConsentReasonNone` or one of `ConsentReasonUnauthorized`, `ConsentReasonUnknownRevision`, `ConsentReasonSupersededRevision`, `ConsentReasonContextMismatch`, `ConsentReasonPolicyMismatch`, `ConsentReasonObsoleteCommand`, and `ConsentReasonCommandConflict`. `CommandResult` has private state and getters `Command()`, `Outcome()`, `Reason()`, and `Duplicate()`. `Results() []CommandResult` returns a defensive copy of original outcomes in processing order, without claiming that processing order equals source order.

Approve uses exact current `Resolve`, the binding's governing policy revision, and the current policy's `CanApprove`. Revoke uses exact historical `Lookup` and the current policy's `CanRevoke(actor, actor.ID())`; it can withdraw that same author's historical consent even when its old binding is no longer the current policy. There is no parameter granting withdrawal of someone else's consent. No operation changes a Suite or a canonical version.

Successful approve, revoke, and no-active-approval revoke advance a per-(exact revision, principal) watermark. A distinct command with order less than or equal to that watermark is obsolete; equal orders are not broken by arrival time. Rejected unauthorized, unknown, superseded, or mismatched commands preserve their result for replay but do not advance authority watermarks. Repeated explicit eligible approve by one owner still represents one active approval; a newer eligible approve after revoke restores consent.

Deduplication precedes reevaluation of current eligibility or order, after aggregate scope checks. An observed source-command identity with the same actor returns the original command/outcome/reason marked duplicate, even if the fetched body/action/reference/order changed; it never reapplies effects. A different operation ID for that same source also reuses the original result; retain its operation-to-source alias in the returned immutable state without adding another receipt or consent effect, so that operation ID cannot later refer to a different source. Reusing an operation ID for a different source, or changing the actor behind a source identity, yields command conflict without authority changes. Actor equality includes both ID and kind. Equal-order distinct commands within the same exact revision/actor context are ambiguous even when an earlier receipt was rejected; this does not advance the successful-command watermark. Original receipts remain immutable; a conflict must not replace them. Retry of an earlier rejected command cannot turn into an approval after policy/eligibility changes.

`HasApproval(proposal Proposal, governing Policy) bool` checks the actual current revision's exact binding, current policy revision/project and eligible human owner against retained active consent. Zero, foreign, superseded, revoked or mismatched state fails closed. It is a consent fact, not readiness, source integrity, promotion permission, or proof of canonical existence.

#### Source-bound integrity facts (C)

`IntegrityOutcome` has nonzero `IntegrityPassed`, `IntegrityFailed`, and `IntegrityUnavailable`. `NewIntegrityEvidence(emitter PrincipalID, source SourceRevision, binding ApprovalBinding, outcome IntegrityOutcome) (IntegrityEvidence, error)` requires nonblank emitter/source, a constructed binding, and a recognized outcome; otherwise it returns `ErrInvalidIntegrityEvidence`. The immutable value exposes `EmitterID()`, `Source()`, `Binding()`, `Outcome()`, and `IsZero()`. Emitter is attribution, not authentication or authority; the trusted application must establish provenance before accepting observations.

`AssessIntegrity(expectedSource SourceRevision, expectedBinding ApprovalBinding, evidence *IntegrityEvidence) (IntegrityAssessment, error)` rejects invalid expected inputs with `ErrInvalidIntegrityAssessment`. Nil or unconstructed evidence yields `IntegrityReasonMissing`. Exact source and every binding field are compared before interpreting the reported outcome; mismatched evidence yields `IntegrityReasonMismatch` even if it reports failure or unavailability. Matching outcomes yield `IntegrityReasonSatisfied`, `IntegrityReasonFailed`, or `IntegrityReasonUnavailable`, respectively. These are distinct nonzero `IntegrityReason` constants.

Assessment exposes `Source()`, `Binding()`, `Evidence() (IntegrityEvidence, bool)`, `Passed()`, `Reason()`, and `Assurance() AssuranceLevel`. The only constructed assurance level is `IntegrityOnly`; zero assessment does not pass or claim assurance. Assessment retains immutable expected inputs and a copy of the supplied evidence, so later caller mutation cannot change its meaning. `Evidence()` returns zero/false for nil or unconstructed evidence and a copied value/true for constructed evidence, including mismatched, failed, or unavailable observations. It does not create consent, execution attestations, verification engines, or canonical state.

#### Reviewed examples and integration checks

- P1/R1 in project A cannot be resolved as project B, P2, a different Suite, a different carrier, or an omitted reference. P1/R2 supersedes R1 for approval; exact historical R1 remains available for withdrawal/audit.
- A one-field change in project, Suite, proposal/revision, baseline (including absent/present), manifest, scope, policy, or covered context makes binding comparison fail. A new revision cannot inherit the old approval. Candidate policy cannot serve as its own governing authority.
- Approve at order 10, revoke at 20, and a delayed approve at 15 remain revoked. A no-active revoke at 20 also fences approve at 15. A new eligible approve at 21 can restore consent. Retrying the original order-10 command returns its historical approval result marked duplicate while present consent remains revoked.
- A rejected command remains rejected on retry. A processed command edit/delete does not rewrite recorded history. Equal-order distinct commands, cross-aggregate reuse, unauthorized revoke, and changed identities cannot create authority.
- Baseline source S0 can be proposed while PR conversation C1 carries its approval. Integrity evidence for S0 never satisfies an assessment for S1, even with identical manifests. Whether S0/S1 also changes approval-covered context remains D-APPROVAL-CONTEXT; this phase accepts explicit covered inputs without choosing that product policy.
- A green assessment can coexist with absent or revoked consent. An active approval can coexist with missing or mismatched evidence. Neither fact creates or modifies a canonical pointer.

### Explicit ApprovalBinding

The reviewed binding must identify:

- Project and Suite.
- Immutable ProposalRevision.
- Expected canonical baseline, including explicit absence for initial bootstrap.
- Proposed manifest and protected-scope identity.
- Governing PolicyRevision.
- Explicit identities of any additional covered inputs/context.

Comparison is exact. A short proposal reference cannot be recycled, resolved across projects, or redirected to a newer revision. New covered inputs require a new revision; older approval does not transfer.

The proposal's source and the external conversation/change carrying the approval are separate concepts. Existing-baseline bootstrap relies on this separation.

### Open source-approval policy

ADR 0007 leaves whether approval also binds implementation revision open. D-APPROVAL-CONTEXT blocks the real M1 proposal/approval and integrated-promotion paths (M1.03 and M1.06), not M0.

M0 accepts an explicit covered-input binding and proves that changes to included inputs invalidate the old consent. It must not quietly decide that every source push invalidates consent, or that unchanged test files always preserve consent. The later policy must specify all covered execution/source/context inputs.

Evidence always identifies the exact source and inputs it assessed. Preserving consent under a later chosen policy never permits reusing an assessment from a different source.

### Consent and command history

Approve/revoke consumes a previously authenticated, project-scoped, ordered command envelope. The interface includes stable operation/source-command identity, resolved principal, exact proposal reference, and ordering context sufficient to reject obsolete replay.

The concrete GitHub parser, ordering acquisition, and authentication proof are M1 adapter work. A caller-controlled timestamp is not an authority source.

Rules:

- Only eligible exact revisions can acquire consent.
- Repeated observation reuses an existing outcome without another approval effect.
- Revoke affects the author's approval.
- Revoke with no active approval still preserves enough history to prevent an older delayed approve from restoring consent.
- A new explicit eligible approve is needed to restore consent.
- Editing/deleting a processed comment does not edit approval history.
- Supersession invalidates eligibility without rewriting historic approval facts.
- Post-promotion revoke preserves the completed canonical version.
- Committing revoke before dependent promotion removes that promotion's approval eligibility.
- Processing outcome and required acknowledgment intent belong to the applicable atomic operation.

### Source-bound assessments

Integrity assessment records its assurance level, exact source, Suite, manifest/scope, governing policy, covered context, and structured result/reasons. Missing, mismatched, unavailable, failed, and passed evidence remain distinguishable.

A passed integrity assessment does not establish human consent, completed promotion, independent test execution, or correctness/sufficiency of tests. No placeholder RunnerProfile, ExecutionPlan, or execution Attestation is added without an M2 consumer.

## M0-C3: guarded transitions and partial state

### M0.03 consumed checkpoint

The integration base is main `49383d5644291191a02cd90e144dc5ac1b3b598c`. PR #7 retained the M0.02 RED/GREEN checkpoints and passed all eight hosted CI jobs; its main coverage report records 262 covered lines with no misses. M0.03 extends the existing values, actual consent aggregate, and exact-source integrity assessment. All three consumers independently reviewed and accepted the following technical contracts before implementation.

| Task | Owner | Branch / worktree suffix | Owned files |
| --- | --- | --- | --- |
| M0.03-C0 | root; reviewed by all three consumers | `phase/M0.03` / `m0.03` | This contract checkpoint |
| M0.03-A | m001_artifact | `task/M0.03-A` / `m003-promotion` | `contract/promotion.go`, `audit.go`, and adjacent tests |
| M0.03-B | m001_authority | `task/M0.03-B` / `m003-lifecycle` | `contract/bootstrap.go`, `correction.go`, and adjacent tests |
| M0.03-C | phase_domain_plan | `task/M0.03-C` / `m003-scheduling` | `contract/scheduling.go` and adjacent tests |
| M0.03-I | root; independently reviewed | `phase/M0.03` / `m0.03` | Backlog, generated phase page, execution record, and aggregate verification |

Source paths are under `internal/domain/`; all worktrees are under `D:/Repos/SuiteWard-worktrees/`. Each checkout owns its tools and caches. C releases reviewed admission/eligibility behavior early for A; A releases reviewed values/readiness/promotion behavior for B. Consumers merge real prerequisite commits, preserving their history, rather than creating duplicate declarations. Each implementation task owns meaningful behavioral RED/GREEN cycles and its tests. C0 and I are documentation/aggregate verification only; any new executable integration behavior must first receive a task assignment and TDD evidence.

M0 is a pure local model. A constructed principal, integration observation, scheduling resolution, canonical snapshot, or historical record represents trusted caller-supplied facts; none authenticates those facts. State revisions and scheduling generations are explicit concurrency context. The later application boundary must compare and commit all relevant authority together; equality of only the canonical pointer does not establish that a decision is still valid.

#### Promotion values and guards (A)

All values below have private fields. Constructors validate and copy caller maps; getters return copies. Reconstitution constructors seal supplied facts, never authenticate their provenance or authorize adoption. These declarations belong to A's four owned files only.

```go
type ContractChange uint8
const (
    ContractUnchanged ContractChange = iota + 1
    ContractChanged
)
func NewProtectedContract(manifest artifact.Manifest, scope artifact.Digest, coveredInputs map[string]string) (ProtectedContract, error)
func (ProtectedContract) Manifest() artifact.Manifest
func (ProtectedContract) ScopeDigest() artifact.Digest
func (ProtectedContract) CoveredInputs() map[string]string
func (ProtectedContract) Equal(ProtectedContract) bool
func (ProtectedContract) IsZero() bool

func NewCanonicalSnapshot(suite Suite, version SuiteVersion, protected ProtectedContract, record PromotionRecord) (CanonicalSnapshot, error)
func (CanonicalSnapshot) Suite() Suite
func (CanonicalSnapshot) Version() SuiteVersion
func (CanonicalSnapshot) Contract() ProtectedContract
func (CanonicalSnapshot) Record() PromotionRecord
func (CanonicalSnapshot) IsZero() bool
func ClassifyContractChange(current CanonicalSnapshot, proposed ProtectedContract) (ContractChange, error)

type IntegrationTargetID string
type IntegrationKind uint8
const (
    IntegrationMergedChange IntegrationKind = iota + 1
    IntegrationExistingBaseline
)
func NewIntegration(project ProjectID, target IntegrationTargetID, source SourceRevision, carrier ApprovalCarrierID, kind IntegrationKind) (Integration, error)
func (Integration) ProjectID() ProjectID
func (Integration) Target() IntegrationTargetID
func (Integration) Source() SourceRevision
func (Integration) Carrier() ApprovalCarrierID
func (Integration) Kind() IntegrationKind
func (Integration) IsZero() bool

type PromotionContext struct {
    Canonical CanonicalSnapshot
    Proposed ProtectedContract
    Proposal Proposal
    Reference ProposalReference
    Carrier ApprovalCarrierID
    Policy Policy
    Consent Consent
    Assessment IntegrityAssessment
    Scheduling Schedule
    ExpectedStateRevision StateRevision
    ExpectedSchedulingGeneration ScheduleGeneration
}
type PromotionInput struct {
    Context PromotionContext
    Integration Integration
    Target IntegrationTargetID
    OperationID OperationID
    NewVersionID SuiteVersionID
    RecordedAt time.Time
    CorrectsVersionID SuiteVersionID
}
type PromotionOutcome uint8
const (
    PromotionBlocked PromotionOutcome = iota + 1
    PromotionReady
    PromotionNoChange
    PromotionProposed
)
type PromotionReason uint8
const (
    PromotionReasonNone PromotionReason = iota
    PromotionReasonContextMismatch
    PromotionReasonProposalNotCurrent
    PromotionReasonCanonicalChanged
    PromotionReasonStateChanged
    PromotionReasonPolicyChanged
    PromotionReasonApprovalMissing
    PromotionReasonAssessmentMismatch
    PromotionReasonIntegrityNotPassed
    PromotionReasonSchedulingBlocked
    PromotionReasonIntegrationMissing
    PromotionReasonIntegrationMismatch
    PromotionReasonCanonicalPresent
    PromotionReasonEmptyInventory
    PromotionReasonCorrectionContextReused
)
func CheckPromotionReadiness(context PromotionContext, requiredSource SourceRevision) (PromotionDecision, error)
func DecidePromotion(input PromotionInput) (PromotionDecision, error)
func (PromotionDecision) Outcome() PromotionOutcome
func (PromotionDecision) Reason() PromotionReason
func (PromotionDecision) Effect() (PromotionEffect, bool)
func blockedPromotion(reason PromotionReason) PromotionDecision

type PromotionRecordInput struct {
    OperationID OperationID
    VersionID SuiteVersionID
    Binding ApprovalBinding
    Carrier ApprovalCarrierID
    Source SourceRevision
    Target IntegrationTargetID
    RecordedAt time.Time
    CorrectsVersionID SuiteVersionID
}
func NewPromotionRecord(input PromotionRecordInput) (PromotionRecord, error)
func (PromotionRecord) OperationID() OperationID
func (PromotionRecord) VersionID() SuiteVersionID
func (PromotionRecord) Binding() ApprovalBinding
func (PromotionRecord) Carrier() ApprovalCarrierID
func (PromotionRecord) Source() SourceRevision
func (PromotionRecord) Target() IntegrationTargetID
func (PromotionRecord) RecordedAt() time.Time
func (PromotionRecord) CorrectsVersionID() SuiteVersionID
func (PromotionRecord) IsZero() bool

func (PromotionEffect) ExpectedCanonicalID() SuiteVersionID
func (PromotionEffect) ExpectedStateRevision() StateRevision
func (PromotionEffect) ExpectedSchedulingGeneration() ScheduleGeneration
func (PromotionEffect) Suite() Suite
func (PromotionEffect) Version() SuiteVersion
func (PromotionEffect) Promotion() PromotionRecord
func (PromotionEffect) Audit() AuditEvent
func (PromotionEffect) Publication() PublicationIntent
func (PromotionEffect) IsZero() bool
func (AuditEvent) Promotion() PromotionRecord
func (AuditEvent) IsZero() bool
func (PublicationIntent) Promotion() PromotionRecord
func (PublicationIntent) IsZero() bool

var ErrInvalidProtectedContract error
var ErrInvalidCanonicalSnapshot error
var ErrInvalidIntegration error
var ErrInvalidPromotion error
var ErrInvalidPromotionRecord error
```

##### Required semantics

- A `ProtectedContract` uses an actual constructed manifest, nonzero scope digest, and exact nonblank context keys/values. Nil and empty maps compare equal. Equality compares manifest digest, scope, and explicit context only; proposal identities, baseline addressing, and governing policy authority do not imply protected-content changes. The trusted caller must supply the actual selected protected context, with D-APPROVAL-CONTEXT unresolved.
- A constructed absent `CanonicalSnapshot` has a valid suite with no current pointer plus zero version/contract/record. An established snapshot has all three present: suite/current/version/project/suite identities match; version manifest equals protected manifest; record version/reference scope/manifest/scope/context agree. The record's previous baseline is historical and need not equal the current pointer. A zero snapshot is invalid. Absent canonical always classifies as changed, including an empty manifest; bootstrap separately forbids empty inventory.
- `NewIntegration` requires nonblank project/target/source and a recognized kind. Merged-change requires a nonblank carrier; existing-baseline requires an empty carrier because its approval carrier is independent. These are trusted integration facts supplied by an adapter, not proof of GitHub state. A target argument supplies the expected configured integration target independently.
- Readiness has no effects and requires nonblank requiredSource. It resolves the exact current proposal/reference/carrier; requires suite scope, proposed protected inputs, expected canonical, expected suite state revision, current governing policy revision, real current `Consent.HasApproval`, passed IntegrityOnly assessment with the exact source and complete binding, and `Schedule.CanPromote(proposal, expectedGeneration)`. Absent canonical plus empty proposed inventory is blocked with PromotionReasonEmptyInventory even when directly calling A rather than the bootstrap wrapper; an explicitly approved empty established contract is not prohibited by this rule. Valid gate rejections return PromotionBlocked with a reason and nil error. Invalid or missing structural input returns ErrInvalidPromotion; missing consent/evidence/scheduling authority is a valid rejection. Readiness never authenticates the adapter's supplied facts.
- `DecidePromotion` first permits a structurally valid pure contract classification to return PromotionNoChange with no effect; this outcome is never approval, merge readiness, or scheduling authority. For changed contracts it requires exact confirmed integration plus every readiness gate. Merged-change carrier must match the candidate; existing-baseline is allowed only with absent canonical and source equal to proposal Origin. A merge source may differ from Origin when it is not part of explicitly covered inputs; evidence must still match that exact integrated source.
- Successful proposed promotion needs a nonblank operation ID, a fresh new version ID different from the current version, a nonzero timestamp, and a state revision that can advance without overflow. It groups the immutable new version, proposed Suite pointer with revision +1, PromotionRecord, AuditEvent, and PublicationIntent. Both audit/publication contain the identical immutable promotion record and do not imply publication already occurred. CorrectsVersionID is optional provenance; B validates the supplied historical relation. Record constructors validate shape only, including new version differing from the binding's expected baseline and an optional nonblank corrected-version identity different from the new version; they do not authorize a promotion.
- `ExpectedStateRevision` is the caller-supplied whole-authority concurrency fence carried by this phase's Suite snapshot. The application must bump/fence it for relevant proposal, policy, consent, and canonical changes and must fence the separate scheduling generation too. C4 must atomically recheck this complete authority snapshot, current pointer, scheduling fence, and conditional absence of new version/operation IDs before inserting all effects. This phase cannot prove globally unused IDs, current provider state, persistence atomicity, or remote publication atomicity.
- Missing integration yields PromotionReasonIntegrationMissing; wrong project/target/carrier/kind semantics yield PromotionReasonIntegrationMismatch. A malformed nonzero integration cannot be constructed. Required effect identity/timestamp validation is not imposed on a readiness-only or no-change result.
- B uses the shared package-private `blockedPromotion` helper for its additional valid business rejections. It enforces bootstrap mode matching: existing baseline requires IntegrationExistingBaseline, and final first-test bootstrap requires IntegrationMergedChange.

`AuditEvent` and `PublicationIntent` are intentionally limited to a canonical-promotion record payload in this phase; no unimplemented generic event or transport subsystem is introduced.

#### Bootstrap and corrections (B)

Owned files: `bootstrap.go`, `bootstrap_test.go`, `correction.go`, `correction_test.go`. Reuse A promotion/readiness rules and C scheduling; no duplicate consent, policy, source-assessment, current-baseline, or scheduling guards.

##### Bootstrap

- `type BootstrapMode uint8`; nonzero constants `ExistingBaselineBootstrap`, `FirstTestBootstrap`.
- `type BootstrapInput struct { Mode BootstrapMode; Promotion PromotionInput }`.
- `DecideBootstrap(BootstrapInput) (PromotionDecision, error)`.
- `ErrInvalidBootstrap` identifies malformed mode/context or correction attribution supplied to a bootstrap operation. Valid business failures use the shared `PromotionBlocked` decision.
- Require canonical absence in `Promotion.Context.Canonical` and explicit expected absence in the current proposal binding. A still validates exact caller reference, carrier, proposal, authority snapshot, consent, assessment, and schedule.
- Require a constructed nonempty proposed protected inventory. This proves inventory presence only; test discovery and actual test-presence eligibility remain M1 concerns.
- Existing baseline: require `IntegrationExistingBaseline` when integration is supplied. A verifies the observed confirmed integration source equals the proposal revision's pinned `Origin()` exactly. The approval carrier remains independent; no hosting-PR integration is required or inferred. Missing integration cannot yield a baseline promotion or readiness claim. Delegate final decision to `DecidePromotion`.
- First tests before integration: call `CheckPromotionReadiness(context, context.Proposal.Current().Origin())`. A passing decision is `PromotionReady`, with no promotion effect/version/pointer mutation. No operation/version/time metadata is needed for this readiness-only path. After integration, require `IntegrationMergedChange`, then call `DecidePromotion` using its exact confirmed integrated source and final assessment. A wrong integration kind returns `PromotionReasonIntegrationMismatch`.
- B-specific shared reasons: `PromotionReasonCanonicalPresent`, `PromotionReasonEmptyInventory`, `PromotionReasonCorrectionContextReused`. A's existing expected-baseline/integration reasons apply to its gates, including exact pinned origin. B uses A's package-private `blockedPromotion(reason)` helper.

##### Historical correction

- `NewHistoricalCanonical(version SuiteVersion, record PromotionRecord) (HistoricalCanonical, error)`.
- Immutable `HistoricalCanonical` getters: `Version() SuiteVersion`, `Record() PromotionRecord`, `IsZero() bool`.
- `ErrInvalidHistoricalCanonical` rejects absent version/record, project/Suite mismatch, `version.ID() != record.VersionID()`, or version manifest digest differing from `record.Binding().ManifestDigest()`. The record constructor establishes its other required fields. This is consistency validation of supplied history, not authentication or durable-history completeness.
- `type CorrectionInput struct { Promotion PromotionInput; Target HistoricalCanonical }`.
- `DecideCorrection(CorrectionInput) (PromotionDecision, error)`.
- `ErrInvalidCorrection` identifies missing/malformed target/current historical context, foreign target scope, or a conflicting supplied `CorrectsVersionID`.
- Current history comes from `Promotion.Context.Canonical.Version()` and `.Record()`; do not duplicate it in the input. Established current canonical is required. Target may equal the current historical version and must have the same project/Suite. If target and current share a version ID, their manifest and all exposed promotion-record facts must agree (timestamps compare as instants); contradictory same-ID history is ErrInvalidCorrection.
- Candidate current `ProposalID` and `Carrier()` must each differ from both supplied current and target provenance. A changed revision of the same historical proposal/carrier is insufficient. Valid reuse is a blocked decision, with a structured correction-context reason.
- Any requested new logical version must differ from both supplied current and target version IDs; reuse of the target identity yields `ErrInvalidCorrection`, consistent with A rejecting invalid effect identity. Global version/proposal/carrier uniqueness and completeness of supplied history are later application/persistence obligations; do not claim this finite comparison checks all history.
- Copy the promotion request, set `CorrectsVersionID` to the target version ID (accept an already matching value; reject a conflicting one), then delegate to `DecidePromotion`. A produces the grouped effect and preserves the historical target relationship in the promotion/audit facts.
- A owns exact current-baseline binding, current governing policy, eligible fresh consent, integration, final source-bound assessment, scheduling, and no-change behavior. A no-change outcome does not create a gratuitous version.
- No supplied historical object is edited. No pointer reset exists. Previously promoted effects remain intact after consent revocation. Historical artifact bytes may be reused in a new logical version.
- Later additions cannot disappear implicitly through resetting to the historical pointer. An exact fresh proposal may explicitly remove such additions after current-policy approval; do not introduce a permanent append-only inventory policy.

##### Required A getters / construction dependencies

`CanonicalSnapshot`: `Suite`, `Version`, `Contract`, `Record`, `IsZero` (already proposed).

`PromotionRecord`: `VersionID`, `Binding`, `Carrier`, `Source`, `Target`, `OperationID`, `RecordedAt`, `CorrectsVersionID`, `IsZero` (A proposed).

`Integration`: constructed/zero distinction and exact integrated `Source` getter (A to freeze).

A provides package-private `blockedPromotion(reason) PromotionDecision`; B does not build effects. Integration getters are `IsZero()`, `Source()`, and `Kind()` with the kind constants above. The shared reasons are frozen above.

##### Parallel implementation order

Prepare tests and fixtures after C0; compile only against real A declarations. Begin bootstrap absent/nonempty/readiness cycles when A has a reviewed working readiness evaluator. Add integrated-bootstrap cycles after A's promotion effect is working. HistoricalCanonical construction and correction tests require real PromotionRecord/CanonicalSnapshot; do not add fake prerequisite domain APIs. Use actual C schedule values and actual M0.02 proposal/consent/evidence in acceptance tests.


#### Scheduling and priority (C)

All declarations below live in `internal/domain/contract/scheduling.go`. Values are immutable; returned slices are copies. Facts supplied to admission and observation methods are trusted application observations, not authenticated remote evidence. M0 proves no durable concurrency or GitHub demotion safety.

```go
type ScheduleGeneration uint64 // positive; starts at 1; never wraps
func NewSchedule(project ProjectID, suite SuiteID) (Schedule, error)
func (s Schedule) IsZero() bool
func (s Schedule) ProjectID() ProjectID
func (s Schedule) SuiteID() SuiteID
func (s Schedule) Generation() ScheduleGeneration
func (s Schedule) Admit(proposal Proposal, contractChanging bool) (Schedule, error)
func (s Schedule) CanPromote(proposal Proposal, expectedGeneration ScheduleGeneration) bool
func (s Schedule) Active() (ScheduleEntry, bool)
func (s Schedule) Entries() []ScheduleEntry // immutable admission order
func (s Schedule) PendingTransfer() (PriorityCommand, bool)

type ScheduleEntryState uint8
const (ScheduleWaiting ScheduleEntryState = iota + 1; ScheduleActive; ScheduleIntegratedPending; ScheduleClosed; SchedulePromoted)
func (e ScheduleEntry) ProposalID() ProposalID
func (e ScheduleEntry) Carrier() ApprovalCarrierID
func (e ScheduleEntry) State() ScheduleEntryState

type PriorityCommandInput struct {
    OperationID OperationID
    SourceCommandID SourceCommandID
    Actor Principal
    ProjectID ProjectID
    SuiteID SuiteID
    ProposalID ProposalID
    Carrier ApprovalCarrierID
    Order CommandOrder
}
func NewPriorityCommand(input PriorityCommandInput) (PriorityCommand, error)
// PriorityCommand has a getter named for each input field.
func (s Schedule) RequestPriority(governing Policy, command PriorityCommand) (Schedule, PriorityResult, error)
type PriorityOutcome uint8
const (PriorityRequested PriorityOutcome = iota + 1; PriorityAlreadyActive; PriorityRejected)
type PriorityReason uint8
const (PriorityReasonNone PriorityReason = iota; PriorityReasonUnauthorized; PriorityReasonUnknownTarget; PriorityReasonClosedTarget; PriorityReasonMergedTarget; PriorityReasonTransferPending; PriorityReasonObsoleteCommand; PriorityReasonCommandConflict)
func (r PriorityResult) Command() PriorityCommand
func (r PriorityResult) Outcome() PriorityOutcome
func (r PriorityResult) Reason() PriorityReason
func (r PriorityResult) Duplicate() bool
func (s Schedule) Results() []PriorityResult // original outcomes, processing order

type TransferResolution uint8
const (TransferUnresolved TransferResolution = iota + 1; FormerUnmergedWithdrawn; FormerMerged)
func (s Schedule) ResolveTransfer(request OperationID, expectedGeneration ScheduleGeneration, resolution TransferResolution) (Schedule, error)

type ScheduleObservation uint8
const (ObserveIntegrated ScheduleObservation = iota + 1; ObserveClosedUnmerged; ObservePromoted)
func (s Schedule) Observe(proposal Proposal, expectedGeneration ScheduleGeneration, observation ScheduleObservation) (Schedule, error)
```

Errors: `ErrInvalidSchedule`, `ErrInvalidPriorityCommand`, `ErrScheduleContextMismatch`, `ErrScheduleConflict`, `ErrStaleSchedule`, `ErrScheduleGenerationExhausted`. Invalid values/context return errors and unchanged input; well-formed priority business rejections return a recorded result and nil error.

##### State and fences

- Entries use stable ProposalID plus carrier, not proposal revision. A different carrier for an admitted ProposalID is a conflict; distinct proposals cannot share an admitted carrier. First admitted changing proposal becomes active, later ones wait. Duplicate admission is a no-op; a proposal revision does not create another entry. Invalid/foreign proposals reject even when the supplied classification is false. False classification is an otherwise valid no-op, never a withdrawal or re-entry instruction.
- Active selection, transfer begin/completion/suspension, active integration, and active release advance generation. Waiting admission/closure does not invalidate active eligibility; generation fences active eligibility, not all aggregate changes. Zero or mismatched generation is ineligible. Any increment at max rejects without mutation. M0.04 must serialize the whole authority aggregate, including waiting entries and receipts.
- CanPromote checks exact project/Suite/proposal/carrier, current generation, active or integrated-pending state, and absence of a pending transfer. It grants neither consent nor promotion and does not establish integration. The proposal's exact revision remains A's independent gate.
- ObserveIntegrated retains the active entry as integrated-pending. ObservePromoted accepts an active or integrated-pending entry and records a trusted committed-promotion observation; it does not authorize or perform canonical promotion. A separately requires exact integrated-source facts. This permits existing-baseline bootstrap without adding a requirement for the approval-hosting PR to merge or for an intermediate scheduling observation. ObserveClosedUnmerged rejects integrated/promoted entries. Closing an open waiting entry records closed; successful promotion or active closure selects the earliest remaining waiting entry. No approval operation advances this queue.
- Repeating the same observation at the current generation is a no-op; stale observations reject. Unknown/foreign entries or impossible transitions reject. Ordinary observations of the active entry reject during transfer; use transfer reconciliation. Closing a waiting transfer target may be recorded, but then transfer completion rejects and remains pending.

##### Priority and reconciliation

- RequestPriority checks scope, then stable receipts before current eligibility; source/operation aliases, actor conflicts, equal-order conflicts, and immutable original results follow M0.02 command semantics. The successful request/already-active order watermark is suite-wide, so an earlier distinct command cannot undo a later decision. Rejections are remembered but do not advance the successful-order watermark. Same source plus same actor returns its original outcome and reserves an alternate operation alias; changed actor or reused operation for another source conflicts.
- After current governing project policy authority, unknown/closed/integrated/promoted targets reject. For a new command, any pending transfer rejects before the already-active check, including a command targeting the former active entry; stable receipt replay still happens first. With no pending transfer, an already-active open target is a truthful no-op (no generation advance). A pending request is never silently replaced. Target must be an admitted open waiting entry.
- Accepted request records former active and desired target as pending and advances generation. Neither entry can promote while pending. Consent is untouched.
- ResolveTransfer must match the original request OperationID and current generation. TransferUnresolved is a no-op. FormerUnmergedWithdrawn is a trusted application statement that the required prior-check/publication reconciliation has been completed; the enum itself proves none of that. It changes former active to waiting, makes the still-open target active, clears pending and advances generation. Original admission order remains unchanged.
- FormerMerged suspends/clears the requested transfer, retains former active as integrated-pending, leaves target waiting, and advances generation. The former entry can then be assessed under the new generation. No automatic target activation follows the observation; only later successful promotion advances ordinary queue order. Merged-but-unpromotable remains pending indefinitely until a separately specified recovery process. The original PriorityRequested receipt is historical acknowledgment of recording the request; it never claims that transfer completed, and remains historical on command replay.
- Transfer replay after pending was cleared rejects as conflict (cannot move queue again); command replay still returns original request receipt. Remote adapter reconciliation remains M1 work, including changed exact inputs, former closure while transferring, target closure, stale remote publications and an in-flight merge.

##### Observable tests and delivery

Early real GREEN provides NewSchedule, Admit, Generation, CanPromote using actual proposals, so A may consume C without waiting for commands. Remaining small TDD cycles prove transitions, command authority/replay, and transfer fences. Tests cover isolated immutable forks without claiming one persisted winner; unknown/foreign/zero values; revised proposal stable admission; waiting/implementation-only; distinct carrier conflicts; generation overflow; approval does not mutate schedule; merge retains slot; closed-unmerged/promotion advance; priority no-op/unauthorized/closed/merged; duplicate source aliases and old order; pending disallows both; unresolved stays pending; stale resolution rejects; former-merged suspends and retains ownership. Draft/re-entry/changed-scope/multi-suite/recovery policy remain open.

#### Reviewed transition examples

| Input or event | Local result | What it does not establish |
| --- | --- | --- |
| Actual protected manifest, scope, and explicit covered context are unchanged | No new canonical effect | Consent, merge readiness, or assessment of another source |
| Green integrated assessment, but absent/revoked consent | Blocked; canonical preserved | Evidence cannot supply authority |
| Exact approval with an old baseline or scheduling generation | Blocked; reconcile current state | A last-writer-wins update |
| Existing integrated baseline with approval in a separate open PR | Initial promotion can be proposed for the pinned baseline | The hosting PR's candidate is canonical |
| Approved first-test candidate before integration | Ready for integration, no canonical effect | Completed bootstrap |
| First-test merge with only evidence from its pre-merge source | Blocked until exact final assessment | Identical file bytes permit evidence reuse |
| Correction reuses selected old bytes and preserves later additions | Fresh exact consent and integrated validation can propose a new version | Historical approval transfers or history is rewritten |
| Correction explicitly includes deletion of a later addition | The exact current-baseline approval rules still apply | A blanket append-only inventory policy |
| Priority requested while prior eligibility withdrawal is unresolved | Transfer pending; replacement cannot promote | A local flag proves a remote check was withdrawn |
| Former active change merged during transfer | Retain that change for integration/promotion reconciliation; fence old work | Automatic release of a merged-but-unpromotable change |
| Consent revoked after a proposed effect was actually committed | Later consent state changes; completed version/history remain | Pointer-only rollback |

Nonempty manifest entries prove nonempty protected inventory, not that a file is semantically a test. Trusted discovery and approved scope establish test presence in M1. Permanent history and conditional insert requirements must be implemented by persistence; immutable domain values alone are not durable storage.

D-APPROVAL-CONTEXT remains open. This phase accepts explicit covered inputs and checks them exactly; it does not decide whether every implementation push changes approval coverage. Draft/re-entry behavior, multi-suite coordination, remote check demotion and publication reconciliation, and merged-but-unpromotable recovery remain with their named M1 consumers. No timeout or automatic reassignment policy is added.

Use orthogonal facts instead of a single status that incorrectly conflates consent, integration, evidence, scheduling, and canonical existence. Exact enum names are not prescribed.

| Fact | Meaning and invariant |
| --- | --- |
| Canonical absent | Project/Suite is awaiting an initial contract; detection or App installation alone does not establish one. |
| Current proposal revision | Immutable covered inputs eligible for assessment; a later revision does not edit it. |
| Consent active/revoked | Historic consent outcome and present eligibility; neither directly changes canonical state. |
| Awaiting integration | Pre-merge readiness can be true while canonical promotion remains impossible. |
| Evidence unavailable/mismatched | Cannot be treated as passed or replaced by unrelated evidence. |
| Active/waiting | Scheduling eligibility is separate from approval. Only one local active contract-changing position exists per Suite. |
| Transfer pending | Replacement cannot receive eligible readiness while prior eligibility withdrawal is unresolved. |
| Merged but unpromotable | Preserve canonical state and report pending governance; do not release competing work as if promotion succeeded. |
| Promoted | Immutable version and atomic promotion outcome exist; later withdrawal does not erase them. |

### Promotion inputs and effect

Promotion receives a consistent snapshot of current authority: canonical baseline, proposal eligibility, governing policy, consent, exact integration assessment, and scheduling generation/transfer state.

A successful decision produces one grouped effect containing the new immutable version, conditional canonical update, promotion record, audit facts, and follow-up publication intent. The domain does not save any part of it. A no-contract-change integration does not create a gratuitous version.

An intervening change to covered authority must be revalidated. Comparing only current_version is insufficient if consent, policy, proposal, or scheduling changed.

### Bootstrap

Existing contract:

- Pin the integrated principal-branch source.
- The first real monitored PR may carry its approval without supplying its source.
- Approved, validated baseline import need not wait for that hosting PR's merge.
- Changes in the hosting PR require their own applicable proposal.

First-test PR:

- Without tests, canonical remains absent.
- Approved pre-integration assessment can establish bootstrap readiness.
- Only confirmed integration and exact final validation permit first promotion.

Both paths require expected canonical absence; concurrent bootstrap attempts cannot both establish the initial pointer.

### Correction and history

A corrective PR is a fresh TEST_SUITE proposal relative to current canonical state. It requires current-policy exact consent, integration, final assessment, and a new immutable version. Historical bytes can be reused, but historical consent cannot authorize a different proposal/context. Preserve every previous logical version and its referenced audit/artifact history.

Do not introduce pointer-only rollback or rewrite history after revocation. A correction must not silently discard later valid additions.

### Scheduling boundary

M0 models local active/waiting/transfer state and generation fences. Approval does not advance the position. Successful promotion or closure without merge can advance it. Implementation-only changes do not occupy the contract-change position.

A stale generation or replayed older priority command cannot reinstate obsolete eligibility. A transfer request does not confer consent.

M0 cannot prove prior GitHub success has been withdrawn, no remote publication remains in flight, or no merge has started. A fake remote acknowledgment is not evidence of real merge safety. The M1 adapter must validate delayed publication, same-SHA PRs, merge-method/queue behavior, and reconciliation.

Do not choose draft/re-entry behavior, multi-suite scheduling, or automatic recovery of a merged-but-unpromotable active PR inside M0. Preserve a truthful pending state until the relevant policy/protocol is defined.

## M0-C4: consumed atomic application boundary

Application owns the small transaction interfaces its implemented use cases consume. Avoid a generic ORM/repository framework, catch-all ports/common package, or interfaces without consumers.

The boundary must provide a consistent view and conditional commit across all authority used by the operation. An opaque concurrency token is acceptable only if its documented semantics cover canonical, proposal, policy, consent, and scheduling changes relevant to the outcome. A database-specific isolation/locking strategy is not selected by the M0 fake.

Promotion atomically records:

1. Conditional canonical pointer update.
2. New immutable SuiteVersion when there is a contract change.
3. Promotion record.
4. Associated audit.
5. Required publication intent.
6. Stable operation receipt/outcome where needed for replay.

Consent operations atomically record their command receipt, consent change/no-op outcome, audit, and required acknowledgment intent. Application publication failures cannot roll back a committed revoke or permit a blocked promotion.

### Consumed signatures and authority fence

The application package `internal/application/governance` owns one small boundary consumed by the implemented use cases. The I task authors the shared declarations in `transaction.go`; A and B consume those declarations and own their own use-case code. The reviewed shape is:

```go
type AuthorityFence struct {
    ProjectID contract.ProjectID
    SuiteID contract.SuiteID
    Revision contract.StateRevision
}
type OperationKind uint8
const (
    OperationPromote OperationKind = iota + 1
    OperationBootstrap
    OperationCorrect
    OperationConsent
)
type ReadRequest struct {
    Reference contract.ProposalReference
    OperationID contract.OperationID
    SourceCommandID contract.SourceCommandID // consent only
    AssessmentSource contract.SourceRevision // exact stored assessment selection
    HistoricalVersionID contract.SuiteVersionID // correction only
}
type Snapshot struct {
    Fence AuthorityFence
    Canonical contract.CanonicalSnapshot
    Proposal contract.Proposal
    Policy contract.Policy
    Consent contract.Consent
    Scheduling contract.Schedule
    Assessment contract.IntegrityAssessment
    Target contract.IntegrationTargetID
    History contract.HistoricalCanonical // correction target, if requested
    HistoricalPromotion contract.PromotionRecord // exact ReadRequest.Reference
    Operation OperationReceipt // Suite-wide lookup by operation ID
    Source OperationReceipt // Suite-wide original consent lookup by source command ID
}
type PromoteRequest struct {
    OperationID contract.OperationID
    Reference contract.ProposalReference
    Carrier contract.ApprovalCarrierID
    Proposed contract.ProtectedContract
    AssessmentSource contract.SourceRevision
    Integration contract.Integration // zero only for first-test pre-merge readiness
    NewVersionID contract.SuiteVersionID
    RecordedAt time.Time
}
type BootstrapRequest struct {
    Mode contract.BootstrapMode
    Promotion PromoteRequest
}
type CorrectionRequest struct {
    Promotion PromoteRequest
    TargetVersionID contract.SuiteVersionID
}
type PromotionIdentity struct {
    Kind OperationKind // ordinary, bootstrap, or correction
    Request PromoteRequest
    BootstrapMode contract.BootstrapMode // otherwise zero
    CorrectsVersionID contract.SuiteVersionID // otherwise empty
    Binding contract.ApprovalBinding // original exact approved binding
}
type PromotionReceipt struct {
    Identity PromotionIdentity
    Decision contract.PromotionDecision // committed PromotionProposed only
}
type ConsentReceipt struct {
    Result contract.CommandResult // original, never replaced on replay
    EvaluatedReference contract.ProposalReference // current revision when processed
    PolicyRevisionID contract.PolicyRevisionID
    CurrentApprovalEligible bool
    PromotedVersionID contract.SuiteVersionID // exact command revision, if promoted
}
type OperationReceipt struct {
    Kind OperationKind // zero means absent; exactly one payload populated
    Promotion PromotionReceipt
    Consent ConsentReceipt
}
type PromotionWrite struct {
    Receipt PromotionReceipt
    Scheduling contract.Schedule // ObservePromoted result
}
type ConsentWrite struct {
    Command contract.Command // observed command, including a possible new alias
    Consent contract.Consent // returned aggregate, or original on source replay
    Receipt ConsentReceipt // new original or original reused by alias
    Alias bool // new operation for existing source; no new audit/ack
}
type PromoteResult struct {
    Decision contract.PromotionDecision
    Committed bool
    Duplicate bool
}
type ConsentRequest struct { Command contract.Command }
type ConsentResponse struct {
    Receipt ConsentReceipt
    Committed bool
    Duplicate bool
}
type Store interface {
    Load(context.Context, ReadRequest) (Snapshot, error)
    CommitPromotion(context.Context, AuthorityFence, PromotionWrite) error
    CommitConsent(context.Context, AuthorityFence, ConsentWrite) error
}

func Promote(context.Context, Store, PromoteRequest) (PromoteResult, error)
func Bootstrap(context.Context, Store, BootstrapRequest) (PromoteResult, error)
func Correct(context.Context, Store, CorrectionRequest) (PromoteResult, error)
func ProcessConsent(context.Context, Store, ConsentRequest) (ConsentResponse, error)
var (
    ErrInvalidRequest error
    ErrInvalidSnapshot error
    ErrAuthorityConflict error
    ErrOperationConflict error
    ErrVersionConflict error
    ErrAuthorityExhausted error
    ErrNotFound error
)
```

`ReadRequest` identifies one project and Suite, the relevant proposal, a stable operation identity, and an optional historical correction target. `Snapshot` is one coherent observation of the canonical pointer and protected contract, current proposal and policy, consent, scheduling, stored integrity assessment, configured integration target, applicable historical promotion, and existing operation/source receipts. It also carries the `AuthorityFence`. Request values may carry trusted integration observations, exact source references, proposed content, and authenticated command facts, but cannot substitute their own policy, canonical, consent, schedule, stored assessment, or historical promotion for the loaded authority. M0 tests control those trusted inputs; GitHub authentication remains M1 work.

The `AuthorityFence` is an opaque, Suite-scoped whole-authority revision. Every committed change to canonical, proposal, policy, consent, scheduling entries or receipts, and operation/source aliases advances it, even when the canonical ID and scheduling generation do not change. A successful `Load` cannot promise a lock across the caller's decision. Each conditional commit must compare the loaded fence against the current entire authority state and reject a stale or mismatched scope. The promotion write additionally checks the domain effect's expected canonical version, Suite revision, and scheduling generation. An implementation that compares only the pointer or generation fails the contract. Initial absence has an explicit fence value; a racing first bootstrap cannot both win. The M0 reference store models this contract without choosing PostgreSQL isolation or locking.

`PromotionWrite` contains only an actual `PromotionProposed` effect and its matching new schedule from `Schedule.Observe(..., ObservePromoted)`, plus a stable committed outcome. The commit reserves its operation and new logical version identities, stores the immutable version and promotion history, advances the pointer and whole-authority fence, records audit, and persists one publication intent as one all-or-nothing change. If scheduling observation fails, including generation exhaustion, nothing commits. A stored promotion receipt binds its operation to the exact project, Suite, proposal revision/binding, carrier, integrated source and kind, target, new version, correction target if any, and resulting effect. An operation replay with the same immutable request identity returns that recorded result; a changed identity conflicts and cannot adopt the old result. `PromotionBlocked`, pre-merge readiness, and `PromotionNoChange` are provisional decisions with no committed promotion receipt or queue advancement. In particular, no-change does not release an active contract-changing position or certify merge readiness.

`ConsentWrite` preserves the canonical pointer, version history, and schedule. It commits the returned domain `Consent`, whole-authority fence, and, for a newly processed command, the immutable original `CommandResult`, audit, and pending acknowledgment intent together. Its acknowledgment records the authenticated source command and actor, exact revision, processing-time consent eligibility under the governing policy, and an exact historical promoted version when applicable. That eligibility is a consent fact, not overall merge or promotion readiness. The historical promotion lookup is by exact proposal revision rather than just the current canonical record. Later policy or canonical changes cannot rewrite an old acknowledgment.

An exact known command/operation replay returns its original receipt without a new audit, acknowledgment, or authority mutation. A repeated source command from the same actor with a *new* operation ID can return `Duplicate=true` while reserving a new operation-to-source alias in the returned `Consent`; that alias and the advanced whole-authority fence commit together, reusing the original receipt and acknowledgment. An operation/source/actor collision that the domain rejects leaves the original records intact and does not manufacture a competing acknowledgment. A no-op revoke, ordered rejection, or another newly processed terminal command still gets its own receipt, audit, and pending acknowledgment. Processing order uses the domain's stable source order, not arrival time. Caller response reports committed success only after commit succeeds; a conflict is surfaced for explicit fresh-load reconciliation rather than silently regenerating IDs or source order.

The first I checkpoint supplies these actual shared types and a behaviorally tested reference-store slice before A and B start executable use-case work. Later I cycles extend the same store and cross-use-case scenarios. The task branches remain separate, and neither consumer creates a duplicate production port. A correction names a historical version to load from the store and calls `DecideCorrection`; a caller-supplied historical object cannot authorize it. `Bootstrap` uses the domain's distinct existing-baseline and first-test modes. Both share the same promotion commit boundary.

| Controlled schedule | Required result |
| --- | --- |
| Two loads see absent canonical; one bootstrap commits first | The other loses the whole-authority comparison; exactly one version, pointer, record, audit, receipt, and intent exist. |
| Two incompatible promotions read the same baseline | At most one commits; the loser cannot overwrite the winner or release its queue entry. |
| Revoke commits between promotion load and commit | The whole-authority fence changes even though the pointer does not; the stale promotion cannot commit. |
| Promotion commits before a later revoke | The version and its immutable history remain; the revoke records its own outcome and truthful already-promoted acknowledgment. |
| Policy, proposal, waiting-entry, or receipt alias changes after load | The stale operation conflicts even when pointer and active scheduling generation are unchanged. |
| An injected failure occurs at any staged promotion or consent effect | No subset becomes visible; retry sees either the old complete state or the committed complete state. |
| Publication fails after commit | The canonical or consent outcome and pending intent survive for later delivery; no remote exactly-once claim follows. |
| Transfer remains pending or merged change cannot promote | Canonical state stays put and the waiting queue does not advance automatically. |

The task-only reference store uses private staged copies and one atomic state replacement. Its fault points cover receipt and alias reservation, version, pointer, promotion, audit, schedule, and publication or acknowledgment intent. Deterministic channel barriers or controllers order loads and commits; sleeps are not evidence of a race. The store is test-only and never becomes a production persistence mode. Its `_test.go` implementation is not counted in Go package coverage, so its meaningful RED/GREEN and race scenarios remain required independently of the coverage percentage.

`D-M0-COVERAGE` remains a phase integration gate. I may implement the reference boundary and collect its genuine application report while the gate is open. The project owner and integrator select and record the post-M0 project-wide non-regression policy after seeing that report; the accepted 90% patch target, critical scenarios, and existing generated-code exclusion remain in force. No coverage threshold or branch requirement is inferred from this contract.

The field shapes above are a consumed application API, not authenticated domain facts. I defines non-nil error sentinels and validates malformed store snapshots and receipt kinds. A and B validate request scope and loaded facts. ReadRequest.Reference selects the exact project, Suite, proposal, and revision; AssessmentSource selects stored assessment evidence for the integrated source or the first-test proposal origin. Neither a caller-supplied assessment nor a caller-supplied historical correction target can replace a store lookup. History is the requested immutable correction target; HistoricalPromotion is the record bound to the command's exact revision, even if a newer canonical version exists.

AuthorityFence.Revision must equal Snapshot.Canonical.Suite().Revision(). M0-C3 already uses this Suite revision as whole-authority context. Promotion, consent, source or operation aliases, policy, proposal, and scheduling writes all advance that same counter, including changes that leave the canonical pointer and active scheduling generation unchanged. A non-promotion write reconstructs a same-pointer canonical snapshot with the unchanged protected version and record and revision plus one. If the revision cannot advance, the entire write fails. The store additionally checks the promotion effect's expected pointer, revision, and scheduling generation; none replaces the whole-authority comparison.

The operation namespace is shared across consent and promotion kinds within one project and Suite. Load checks the requested operation ID and, for consent, source command ID against Suite-wide receipts before current authority is evaluated. It loads the proposal aggregate by project, Suite, and proposal ID; an unknown or superseded requested revision is not a load failure for consent. The domain must be able to record its UnknownRevision or SupersededRevision rejection, and a known-source replay with an edited revision must still return the original historical receipt first. A missing proposal aggregate may produce ErrNotFound without effects. A receipt from a different kind, scope, source, or actor conflicts without new effects. An edited observation of a known source in another proposal also conflicts rather than acquiring fresh authority; its original approval and acknowledgment remain intact. Within the same proposal, a source replay from the same actor returns the original receipt even when the edited action, revision, or order differs. A new operation alias for that source commits the global reservation and, when Consent.Apply returns an updated aggregate, that aggregate too, without a second audit or acknowledgment. The operation and source indexes are committed under the same whole-authority fence so concurrent first observations cannot produce two originals.

Promotion replay compares its immutable kind, scope, original stored binding, protected content, carrier, assessment source, integrated source and kind, target, logical version, bootstrap mode, and correction target before accepting the recorded receipt. The stored binding must be self-consistent with the recorded reference and content; a later Snapshot.Proposal.Current() is not the replay baseline. RecordedAt is original processing metadata; a retry with a later timestamp returns the original record unchanged. Successful promotion and newly processed consent outcomes are durable receipts. PromotionBlocked, pre-merge readiness, and PromotionNoChange remain provisional, so no receipt or queue advancement is inferred from them. A successful application result is returned only after conditional commit; a replay of an already committed operation returns the historical result before current policy, consent, or canonical state is reevaluated. A stale fence or identity conflict is returned for explicit reconciliation, without regenerating an ID or source order.

ConsentReceipt.CurrentApprovalEligible describes the current proposal revision under the processing-time governing policy, identified by EvaluatedReference and PolicyRevisionID. It does not assert that a historical revoked revision was eligible or that the PR could merge. PromotedVersionID names the exact historical promotion of the command reference. A no-op revoke, ordered rejection, or other new terminal command receives one receipt, audit, and pending acknowledgment; a pure replay or identity conflict does not. A missing or malformed load fails without writes.

The first I checkpoint must implement and behaviorally test these shared declarations and a usable reference-store slice. A and B then consume that reviewed checkpoint on separate task branches. I later adds the complete store failure and concurrency scenarios without replacing A/B tests. Any concrete field adjustment required by a consumer must be reviewed by all three owners and amended here before that consumer implements against it. The open D-M0-COVERAGE gate blocks phase integration, not this evidence-producing I checkpoint.

### Reference-model proof

Use a test-only in-memory store implementing actual consumers' boundary semantics. Do not ship a production nonpersistent mode. Concurrent tests use explicit barriers or scheduling controls, not sleeps.

Required proofs:

- Injected failure yields no partial effects.
- Successful commit exposes all related effects together.
- Operation replay returns the original outcome without duplicate version/promotion/intent.
- Two bootstrap attempts have one winner.
- Two incompatible promotions against one baseline have one winner and one conflict.
- Revoke committed first blocks the dependent promotion.
- Promotion committed first is retained after later revoke.
- Intervening policy/proposal/scheduling changes invalidate stale work.
- Failed publication leaves the committed outcome and retry intent.
- End-to-end model scenarios cover baseline import, first tests, ordinary changes, corrections, and merged-but-unpromotable state.

These results prove a domain/application contract and reference implementation. They do not prove PostgreSQL durability, real transaction isolation, River persistence, GitHub identity verification, exactly-once remote comments, or atomic GitHub merge plus promotion. Those require real M1 integration tests.

## Shared ownership and completion evidence

The phase integrator owns go.mod, go.sum if needed, shared declaration reconciliation, and the final combined scenario checks. Task owners own their behavior and adjacent tests. Shared file changes require coordination, not unilateral ownership expansion.

Every phase PR reports:

- Intended behavior and applicable ADRs.
- Reviewed contract version/examples.
- Implemented acceptance and negative cases.
- Windows/Linux tests and applicable Linux race/coverage/security results.
- Limits and remaining real-adapter proofs.
- Source of any technical decision added during implementation.

No unrelated product policy is settled as a convenient implementation default. No source/configuration/GitHub mutation uses personal credentials to bypass the authorized bot's permissions.
