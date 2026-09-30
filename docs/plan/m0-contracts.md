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
