# ADR 0007: Canonical contract authority and promotion invariants

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Scope:** Domain authority, versioning, approval, and promotion.
- **Origin:** Product thesis derived from CircuitoNE / CircuitoNE-QA as PoC #0.

## Context

Implementation code can modify repository tests, configuration, execution commands, and reported results. A successful run cannot establish that those modified criteria are authorized.

SuiteWard therefore maintains the canonical verification contract outside the candidate repository's write authority. A second user-managed QA repository is not a product requirement.

## Decision

The primary contract is a Suite with immutable SuiteVersions. Manifest and trusted runner/policy inputs define the content and conditions being governed. The mutable canonical pointer selects a version; it does not change that version's bytes.

A repository may contain and propose tests, but it cannot redefine its own canonical test contract.

Enforce these invariants:

| Invariant | Required behavior |
| --- | --- |
| Canonical immutability | A sealed version is never edited in place. Content changes create another version. |
| Candidate non-authority | Candidate files, configuration, reports, and MCP arguments cannot select or rewrite the canonical contract or grant permissions. |
| Exact approval | Approval binds an identified immutable proposal revision and its covered inputs. It cannot move silently to changed content. |
| Atomic promotion | Changing the canonical pointer requires authorized conditional state transition, conflict detection, and durable promotion/audit records. |
| Evidence binding | Evidence identifies the exact source revision, suite, runner, policy context, and execution plan or integrity evaluation it describes. |
| Green is evidence, not authority | A passing check supplies evidence. It does not approve a contract change or authorize promotion by itself. |

ChangeProposal is a generic governance concept with TEST_SUITE, RUNNER, and POLICY change types. Their permissions are distinct. A policy change is judged under the currently governing policy; a candidate cannot authorize itself by proposing a weaker policy.

Keep proposal approval, verification, and promotion as separate operations. Principals and policies define who may perform them. [ADR 0012](0012-mvp-authorization-policy.md) defines the MVP's human owner, restricted agents, and one-approval threshold. Future organizational policies remain open.

Use content-addressed storage for protected artifacts. [ADR 0001](0001-self-hosted-github-synchronization.md) selects the initial local volume; its location does not change content identity or authority.

Compare the expected canonical state before promotion. If another valid promotion has changed that state, re-evaluate the proposal against the new baseline instead of overwriting it. Update the pointer and its promotion/audit records together. Record required follow-up publication durably so a failed GitHub update can be retried.

The human-facing approval command is fixed by [ADR 0002](0002-exact-revision-approval.md). Neither an agent-triggered synchronization nor periodic discovery relaxes these invariants.

## Consequences

The product can explain which contract was approved and evaluated, and concurrent proposals cannot silently erase an accepted regression.

The repository's own CI may contribute evidence, but evidence from candidate-controlled execution does not establish independent execution assurance. Required assurance levels are covered by [ADR 0008](0008-progressive-integrity-and-execution.md).

The threat model trusts the instance's authorized administrators to operate SuiteWard. It does not claim to defeat a malicious host administrator with direct database, storage, and credential control.

A database transaction does not make GitHub merge and SuiteWard promotion a single atomic operation. Strict gate-to-merge-to-promotion coordination must be demonstrated separately before that guarantee is advertised.

## Alternatives

- Let the candidate repository define its own final acceptance contract.
- Treat a passing CI result as sufficient authorization.
- Overwrite an existing canonical version or accept last-writer-wins promotion.

These alternatives conflict with the product's core purpose.

## Open implementation details

- Canonical serialization, digest envelopes, and exact aggregate boundaries.
- Concrete schema constraints, transaction isolation, and concurrency protocol.
- Whether a policy also binds approval to the implementation revision.
- Bootstrap implementation details following [ADR 0010](0010-repository-bootstrap.md), revocation details following [ADR 0011](0011-approval-revocation-and-acknowledgments.md), corrective-PR rollback and history tooling under [ADR 0018](0018-corrective-pr-rollback-and-audit-history.md), and backup/restore details under [ADR 0019](0019-encrypted-backups-and-instance-recovery-key.md); administrative-access recovery remains open.
- Strict GitHub integration coordination beyond the explicitly limited assisted mode.

The earlier draft SQL schema is a design input, not a completed production migration.


## Amendment (2026-10-05, phase R1): verification point and storage shape

- Artifact bytes are verified when a version is written, against that version's manifest only. Loads and replays do not re-hash stored artifacts. Periodic integrity verification becomes a runtime job.
- Governance state is stored as normalized facts rather than serialized aggregate snapshots. See the [persistence contract](../contracts/persistence.md).

The invariants above are unchanged.
