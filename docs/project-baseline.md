# SuiteWard project baseline

- **Updated:** 2026-09-26
- **Stage:** Product and architecture planning; implementation has not started.

## Product identity and audience

| Item | Agreed direction |
| --- | --- |
| Name | SuiteWard. Test Vault is the earlier working name. |
| Project language | English for code-facing identifiers, documentation, and product-facing text. Planning conversations may remain in Portuguese. |
| Initial audience | An individual developer working with AI agents. Agents implement and propose; an authorized human approves contract changes. |
| Initial experience | GitHub PRs, comments, and checks, with a small setup/administration surface. A full dashboard is not required for the MVP. |
| Distribution | Open source and self-hosted first; possible managed hosting later. |
| PoC #0 | CircuitoNE / CircuitoNE-QA. It informs the product but does not need to be migrated before SuiteWard planning proceeds. |

These product definitions belong here rather than in separate ADRs merely for naming or wording choices.

## Architecture baseline

The accepted stack is Go, Chi, PostgreSQL, pgx, sqlc, and River. One local/self-hosted instance can manage multiple repositories. Canonical artifacts use a dedicated persistent local volume and content-addressed storage.

The default integration uses a GitHub App with outbound API access. A local MCP call accelerates PR synchronization; independent periodic reconciliation recovers when the call is omitted. Public webhooks and an S3-compatible service are optional future adapters.

The core rule is unchanged: a repository may contain and propose tests, but it cannot redefine its own canonical test contract.

Bootstrap uses the first real monitored PR for explicit human approval. Existing tests are imported from a pinned principal-branch commit; tests introduced by the first relevant PR become canonical only after integration and final validation. Initial authority is established by the instance administrator. See [ADR 0010](decisions/0010-repository-bootstrap.md).

Approval withdrawal uses an explicit revision-bound revoke command. SuiteWard publishes the processed approval or revocation outcome in the PR; editing or deleting a processed command does not rewrite its recorded effect. See [ADR 0011](decisions/0011-approval-revocation-and-acknowledgments.md).

The MVP has a human project owner and restricted agents. One eligible human approval is sufficient, including when that authorized human authored the PR. Approval credentials remain outside agent access, and policy changes use the governing policy. See [ADR 0012](decisions/0012-mvp-authorization-policy.md).

Protected scope uses assisted discovery and a `.suiteward.yml` declaration reviewed in the PR. The agent reads the proposal, discusses adjustments with the user, and edits the declaration; SuiteWard publishes the resulting exact inventory and approval command. The human approves explicitly. Candidate configuration cannot remove existing canonical protection, and a bot comment does not automatically wake an agent. See [ADR 0013](decisions/0013-protected-scope-and-agent-assisted-review.md).

M1 relies on the project's existing review and CI process for test acceptance. A confirmed PR merge triggers promotion assessment: SuiteWard retains exact human approval, validates the integrated contract, and conditionally promotes it without executing tests. Merge alone cannot authorize an unapproved contract. See [ADR 0014](decisions/0014-merge-triggered-canonical-promotion.md).

The `SuiteWard / Contract` check is a mandatory merge requirement. Changes to protected tests or scope remain blocked until an eligible exact approval and integrity assessment establish readiness for integration. GitHub protection must require the check from the expected SuiteWard App; connection alone does not establish enforcement. Canonical promotion remains a separate post-merge step. See [ADR 0015](decisions/0015-required-contract-check.md).

Installation and protection activation form one guided flow. The verified owner reviews and confirms the proposed settings; SuiteWard uses administrative write access to apply the required rule, preserve existing protections, and verify activation. Agent MCP access remains limited to synchronization and status. Canonical bootstrap remains a separate approval. See [ADR 0016](decisions/0016-integrated-protection-onboarding.md).

Later protection changes are detected automatically and reported locally and in reachable affected PRs without repetitive polling notices. Confirmed protection gaps and unavailable verification have distinct states. Repair requires a specific confirmation from the responsible human, after which SuiteWard applies and verifies the change through the existing setup interface. See [ADR 0017](decisions/0017-protection-change-detection-and-confirmed-repair.md).

Ordinary protected changes are reviewed and accepted in the PR that contains them. Correcting a change that was already promoted uses a corrective PR, fresh exact approval, integration, and a new canonical version. All historical canonical versions, their referenced artifacts, and associated audit records remain available and immutable. The MVP has no separate pointer-only rollback command. See [ADR 0018](decisions/0018-corrective-pr-rollback-and-audit-history.md).

Backups use a separate local backup service/volume and encrypted assets in dedicated GitHub backup releases, published independently of product releases. One external recovery kit serves all repositories in the instance; the runtime needs only its public encryption key. Recovery receipts bind the completed encrypted archive to its actual recovery point. Each archive contains a version ledger/manifests, required governance records, and one copy of each distinct referenced file content. Unchanged files are reused across versions; modifications add new content and preserve the old content, and removals preserve bytes needed for history. The physical archive format remains open. See [ADR 0019](decisions/0019-encrypted-backups-and-instance-recovery-key.md) and its destination update, [ADR 0021](decisions/0021-release-backups-and-health-reconciliation.md).

Periodic reconciliation checks remote backup availability, integrity, and coverage. Git fallback and cleanup are proposed through reviewable PRs, preserving existing protection and exact referenced archive bytes. The accepted fallback threshold is 29 days from the oldest recovery-relevant change still awaiting its first verified remote coverage; later PRs, promotions, and restarts do not reset that gap. Publication failures are reported promptly. The separate escalation clock after loss of a previously verified copy remains open in ADR 0021.

Recovery uses a local `suiteward restore` assistant, normally started from the repository directory. It can work while the primary service is unavailable, restore project data into an isolated target, guide instance configuration and credential reconnection, and verify ownership and GitHub protection before resumption. A full-instance archive containing administrative secrets is deferred; recovery from loss of the owner identity remains a separate open policy. See [ADR 0020](decisions/0020-guided-local-cli-recovery.md).

Contract-changing PRs use one active position per Suite. Other contract-changing PRs wait with a non-passing required check linked to the active PR; implementation-only PRs do not consume that position. An authorized human can comment `/suiteward prioritize` in another PR to request transfer. SuiteWard confirms removal of the previous PR's remote eligibility before assessing replacement readiness, and reports the processed outcome in both PRs. Priority is separate from approval; normal advancement follows successful integration and canonical promotion, or closure without merge. Only SuiteWard's check is re-evaluated, with no new test-CI orchestration in M1. See [ADR 0022](decisions/0022-contract-change-pr-priority.md).

The [ADR index](decisions/README.md) is the authoritative map of the decisions and their consequences. Each record distinguishes accepted direction from implementation details that remain open.

## Delivery and boundaries

M0 proves the domain. M1 delivers integrity governance. M2 adds canonical execution using DockerExecutionBackend. M3 adds justified supply-chain hardening.

Choosing Go for SuiteWard does not select the languages or testing frameworks of the repositories it will protect. The first executable test profile remains open and is deferred to M2 while the integrity MVP is defined and validated.

SuiteWard's own development environment is settled separately from product deployment: native Windows development, Windows/Linux CI, local PostgreSQL in Podman, and project-local tool installation wherever practical. See [ADR 0023](decisions/0023-development-environment-and-project-local-tooling.md) and the [engineering preparation plan](engineering-plan.md).

The codebase will use one application Go module, `cmd/` entry points, and internal domain, application, adapter, and composition boundaries. Capabilities are grouped to preserve cohesive invariants and support parallel tasks. See [ADR 0024](decisions/0024-single-module-project-structure.md).

The [development guide](development-guide.md) defines the accepted standard-library test/log/error conventions and explicit dependency composition. CI and coverage policy remain engineering decisions to complete.

## Items still open

- Bootstrap detection rules, concrete declaration semantics, large-inventory presentation, and initial identity-verification UI; the governing bootstrap and scope-review flows are settled in ADRs 0010 and 0013.
- M2's first supported execution ecosystem and RunnerProfile; deferred from current M1 planning.
- Policy serialization, future organizational roles, and owner recovery; MVP profiles, approval threshold, and PR-author approval are settled in ADR 0012.
- Recovery from loss of the owner identity, detailed credential-reconnection mechanisms, and history-browsing/correction tooling; revocation is settled in ADR 0011, corrective-PR rollback in ADR 0018, backup in ADR 0019, and guided instance reconstruction in ADR 0020.
- Concrete enforcement verification, detailed check/notification lifecycles, and strict GitHub merge coordination; promotion, checks, setup, and protection repair are settled in ADRs 0014 through 0017, and visible PR priority behavior in ADR 0022. Same-SHA PRs, multiple-suite queue coordination, and a merged active PR whose promotion fails still require validated handling.
- Polling intervals, API budget targets, the remaining App permission matrix, and local/headless onboarding details; administrative write capability for protection setup is accepted in ADR 0016.
- Migration tooling, concrete schemas and aggregate definitions, use-case interfaces, version pins, and supported deployment platforms; the package-boundary direction is settled in ADR 0024.
- Physical backup format, exact Release namespace and Git contingency path, escalation after loss of a previously verified backup, size budgets, detailed CLI prompts and packaging, transient-data retention, and production execution isolation; historical retention is settled in ADR 0018, the backup payload in ADR 0019, local restore in ADR 0020, and Release storage/health reconciliation in ADR 0021.
- AGPL only-versus-or-later release grant and publication notices.
- Future managed execution location and trust model.

An earlier system-design proposal may discuss candidates for these items. That discussion alone does not make them accepted decisions.

