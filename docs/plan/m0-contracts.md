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
