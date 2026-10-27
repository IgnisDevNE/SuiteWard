# Architecture decision records

- **Updated:** 2026-10-27
- **Product:** SuiteWard
- **State:** Accepted decisions. M1.2 to M1.11 are implemented and released, and `v0.2.0` (2026-10-22) is the current release; the installer phases M1.12 to M1.15 are implemented and released as the pre-releases `v0.3.0-alpha.1` to `v0.4.0-alpha.3`, the latest. The security hardening phase SH amended several records for `v0.2.0`. [ADR 0028](0028-canonical-execution-and-the-receipt.md) records the design of phase M2.1 (canonical execution, the receipt and the Go profile), which is being implemented on the phase branch and is not released yet.

## Accepted records

| ADR | Decision | Scope |
| --- | --- | --- |
| [0001](0001-self-hosted-github-synchronization.md) | Self-hosted deployment and GitHub synchronization | One instance for multiple repositories, local content-addressed storage, GitHub App, local MCP, independent polling, optional future adapters. |
| [0002](0002-exact-revision-approval.md) | Human approval of an exact proposal revision | Copyable PR command, exact binding, stale-reference handling, and idempotent approval processing. Amended (R1): the command carries a TOTP code. |
| [0003](0003-go-and-chi.md) | Go and Chi | Product implementation language and minimal HTTP adapter. |
| [0004](0004-postgresql-pgx-sqlc.md) | PostgreSQL, pgx, and sqlc | Explicit SQL, generated access code, and transaction responsibilities. |
| [0005](0005-river-background-jobs.md) | River background jobs | Transactional enqueueing, repeatable processing, periodic triggers, and restart recovery. Amended (M1.10): the outbox carries the transactional guarantee; `Tx.Enqueue` removed. |
| [0006](0006-provider-agnostic-modular-monolith.md) | Provider-agnostic modular monolith | Control plane, execution plane, SCM adapter, and GitHub-first delivery. |
| [0007](0007-canonical-contract-authority.md) | Canonical contract authority | Immutable versions, candidate non-authority, exact approval, bound evidence, and atomic promotion. Amended (R1): verify bytes at write; normalized state. Amended (SH): the service's database role. |
| [0008](0008-progressive-integrity-and-execution.md) | Integrity before canonical execution | M0-M3 sequence, distinct assurance levels, and DockerExecutionBackend in M2. Amended (SH): release provenance, SBOM and keyless signing ahead of M3. Amended (M2.1): M2 is delivered as M2.1 to M2.3, through a separate executor (ADR 0028). |
| [0009](0009-agpl-3.0-distribution.md) | AGPLv3 distribution | The grant is `AGPL-3.0-only`; D-RELEASE (2026-10-17) settles the release notices and the source offer. |
| [0010](0010-repository-bootstrap.md) | Repository bootstrap | Initial authority, existing baseline import, first-test PR, and conditional first promotion. Amended (R1): first-test-PR path deferred; restored by M1.7b and made precise in the amendments of 2026-10-14 to 2026-10-16. |
| [0011](0011-approval-revocation-and-acknowledgments.md) | Approval revocation and PR acknowledgments | Explicit withdrawal, processed comment semantics, promotion races, and durable visible outcomes. |
| [0012](0012-mvp-authorization-policy.md) | MVP authorization policy | Human owner and agent profiles, one eligible approval, approval by the PR author, and separated credentials. Hardened by SH through ADR 0027. |
| [0013](0013-protected-scope-and-agent-assisted-review.md) | Protected scope and agent-assisted review | Assisted discovery, `.suiteward.yml`, exact inventory review in the PR, and explicit human approval. Amended (R1): default scope includes workflows and test runner configuration. |
| [0014](0014-merge-triggered-canonical-promotion.md) | Merge-triggered canonical promotion | Project-owned test acceptance, exact approval, integrated-revision integrity checks, and automatic conditional promotion in M1. |
| [0015](0015-required-contract-check.md) | Required contract check | Mandatory App-sourced governance check, exact approval before integration, bootstrap readiness, and explicit enforcement boundaries. |
| [0016](0016-integrated-protection-onboarding.md) | Integrated protection onboarding | Owner-confirmed automatic rule setup, administrative permission, preservation of existing protections, and verified activation. Amended (R1): verify, do not apply; support matrix; automation post-MVP. |
| [0017](0017-protection-change-detection-and-confirmed-repair.md) | Protection-change detection and confirmed repair | Automatic detection and notices, distinct unknown state, and owner-confirmed repair through the existing setup interface. Amended (R1): repair post-MVP. |
| [0018](0018-corrective-pr-rollback-and-audit-history.md) | Corrective PR rollback and audit history | Same-PR acceptance, fresh approval for corrective rollback, and retention of all historical canonical versions and associated evidence. |
| [0019](0019-encrypted-backups-and-instance-recovery-key.md) | Encrypted backups and an instance recovery key | Local recovery volume, versioned encrypted archives, one external kit per instance, version ledger with deduplicated file contents, recovery receipts, and restore boundaries; remote destination updated by ADR 0021. Deferred to post-MVP (R1); the MVP backup is `pg_dump` plus an artifact tarball. |
| [0020](0020-guided-local-cli-recovery.md) | Guided reconstruction through a local CLI | `suiteward restore`, project discovery, isolated import, owner and GitHub reconnection, and controlled resumption without requiring a working primary service. Deferred to post-MVP (R1). |
| [0021](0021-release-backups-and-health-reconciliation.md) | Dedicated backup releases and health reconciliation | Release assets independent of product releases, periodic health checks, and reviewable Git contingency proposals after 29 days of unresolved first-time remote coverage. Deferred to post-MVP (R1). |
| [0022](0022-contract-change-pr-priority.md) | Contract-change PR priority | One active contract-changing PR per suite, waiting checks, human `/suiteward prioritize`, confirmed demotion before replacement readiness, and post-integration reconciliation. Deferred to post-MVP (R1). |
| [0023](0023-development-environment-and-project-local-tooling.md) | Development environment and project-local tooling | Native Windows development, Windows/Linux CI, local PostgreSQL in Podman, pinned project-local tools, and isolated worktree resources. Amended (R1): shared per-user tool cache. |
| [0024](0024-single-module-project-structure.md) | Single-module project structure | `cmd/` entry points, internal domain/application/adapter boundaries, capability grouping, and dependency rules for parallel implementation. |
| [0025](0025-test-driven-development.md) | Mandatory task-level test-driven development | Test-first for authority, consent, promotion, idempotency, concurrency and adapter behavior; reviewed by `sw-reviewer`; amended (R1): no evidence JSON or ancestry validator. |
| [0026](0026-versioned-postgresql-migrations.md) | Versioned PostgreSQL migrations | Embedded Goose Provider, pinned dependencies, sequential forward-only SQL, session locking and real upgrade evidence. Amended (R1): single normalized migration `00001`; amended (M1.2): River's schema vendored into goose; amended (SH): the least-privilege role and `suiteward migrate`. |
| [0027](0027-agent-human-trust-separation.md) | Agent/human trust separation | TOTP code on every approve and revoke command, isolated and co-located installation tiers, and separate agent identity. Amended (SH): the code is consumed by its comment; commands apply to their own pull request. |
| [0028](0028-canonical-execution-and-the-receipt.md) | Canonical execution and the receipt | Opt-in execution policy in `.suiteward.yml`, the canonical plan and the receipt, the receipt and canonical gates, the `SuiteWard / Receipt` check, the `mode` and `unblock` commands, the separate executor and its API, the sandbox and the Go profile, the trust layers and the evidence binding. Accepted for phase M2.1; being implemented on the phase branch, not released yet. |

Naming, English as the project language, initial audience, and product positioning are recorded in the [project baseline](../project-baseline.md). They do not each need an architecture decision record.

## How to read this set

ADRs 0001 and 0002 capture the recent hosting and approval decisions. The additional records consolidate choices already made in the conversation or recovered from the original system design. Their numbering is a documentation sequence, not the original decision chronology.

An accepted architecture direction is not a claim that a feature is implemented, a schema migrated, a license applied, or an operational guarantee proven.

Details explicitly marked open remain open. In particular:

- Strict GitHub gate-to-merge-to-promotion coordination is not proved by database CAS alone.
- The first execution profile (Go) and the execution trust model are decided in ADR 0028. Hosted execution versus customer infrastructure stays open ([D-MANAGED-EXECUTION](../plan/decisions.md#open)).
- The license grant is decided: `AGPL-3.0-only`, with the release notices and the source offer settled by D-RELEASE (2026-10-17).

## Relation to the earlier system design

The earlier Test Vault System Design v0.1 is historical planning input. These records preserve its core authority model and delivery sequence, while the explicit SuiteWard decisions resolve or replace these earlier assumptions:

| Earlier assumption or open item | Current decision |
| --- | --- |
| Test Vault working name | SuiteWard; project baseline. |
| Language and framework undecided | Go and Chi; ADR 0003. |
| Persistence access undecided | PostgreSQL with pgx and sqlc; ADR 0004. |
| Queue implementation not fixed | River OSS on PostgreSQL; ADR 0005. |
| S3-compatible service in the default deployment | Dedicated local content-addressed volume; ADR 0001. S3 remains an optional extension. |
| Inbound GitHub webhooks as the default trigger | Local MCP acceleration plus independent periodic API reconciliation; ADR 0001. |
| Short approval command or extra challenge still under discussion | Explicit proposal revision in `/suiteward approve P42-R3`; ADR 0002, plus an owner TOTP code; ADR 0027. |
| Open-source license undecided | `AGPL-3.0-only`; ADR 0009 and D-RELEASE. |

This consolidation does not turn every proposal in the old design into an accepted requirement.

## Phase R1 course correction

A review found overengineering and gaps in the threat model and plan. Phase R1 simplifies the plan and records these decisions: ADR 0027 (TOTP owner confirmation and installation tiers), amendments to ADRs 0007, 0013, 0016, 0017, 0023, 0025 and 0026, and the post-MVP deferral of ADRs 0019 through 0022 and the first-test-PR path of ADR 0010. Open and accepted gates are in the [decision register](../plan/decisions.md).

## Phase SH

The security assessment of 2026-10-20 led to the hardening phase [SH](../plan/phases/SH.md). It amends ADRs 0007, 0008, 0012, 0026 and 0027 and adds the [threat model](../security/threat-model.md) and [SECURITY.md](../../SECURITY.md).

## Next product decision

None is blocking. Strict GitHub gate-to-merge-to-promotion coordination is not proved by database compare-and-set alone. Accepted and open product gates are in the [decision register](../plan/decisions.md).
