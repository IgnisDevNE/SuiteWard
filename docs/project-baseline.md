# SuiteWard project baseline

- **Updated:** 2026-10-29 (phase [M2.1b](plan/phases/M2.1b.md), results communication, is merged ([#62](https://github.com/IgnisDevNE/SuiteWard/pull/62)) and released as the pre-release `v0.5.0-alpha.2`, the latest); 2026-10-28 (phase M2.1, canonical execution and the receipt with the Go profile, is merged ([#60](https://github.com/IgnisDevNE/SuiteWard/pull/60)) and released as the pre-release `v0.5.0-alpha.1`, and verified live on both tiers)
- **Stage:** the MVP `v0.2.0` is released (2026-10-22) and the installer phases M1.12 to M1.15 are released as alphas. [M2.1](plan/phases/M2.1.md) is merged ([#60](https://github.com/IgnisDevNE/SuiteWard/pull/60)) and released as the pre-release `v0.5.0-alpha.1`: SuiteWard runs the approved tests, and the tests a pull request adds or changes, in sandboxes driven by a separate executor, where the canonical declaration names a profile. The [delivery plan](plan/README.md) lists each phase.

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

## Positioning

GitHub already offers CODEOWNERS, branch protection and rulesets, required reviews, and "dismiss stale approvals". They answer "who must review a change to these files". SuiteWard answers a narrower question: is the test contract that gates the code still the contract a human approved.

SuiteWard differs from the GitHub-native controls in these ways:

- **Approval survives implementation-only pushes.** Consent binds the protected inventory, the declaration, the governing policy, and the canonical baseline. A push that changes only implementation keeps consent and triggers a fresh integrity assessment, instead of dismissing the approval.
- **Detection of protected-scope reduction.** A candidate cannot shrink what is protected by editing its own declaration; a reduction needs approval like any other change.
- **Immutable canonical history.** Every approved version and its artifacts are kept, and the canonical pointer never rewrites an earlier version.
- **Owner confirmation the agent cannot forge, on an isolated installation.** Approval needs a TOTP code from the verified owner, not only a GitHub account that an agent might control (ADR 0027). Co-located installations are supported but labeled with reduced assurance, because the agent can reach the secret.
- **Canonical execution (M2.1, opt-in).** Where the canonical declaration names a profile (`execution: {profile: go}`), SuiteWard runs the approved tests on each head, and the tests a revision adds or changes on the head and on the code before the change (the receipt), in disposable sandboxes driven by a separate executor. A passing result does not come from candidate-controlled runners.

M1's honest claim is a **tamper-evident, human-approved test contract**, and it stays the core claim. M2.1 adds evidence to it: what an executor observed when it ran the tests on one head. That evidence is never authority and never enters the approval, and it cannot rule out code that behaves differently under test ([ADR 0028](decisions/0028-canonical-execution-and-the-receipt.md)). Assurance is labeled by installation tier: isolated installations give the full assurance, co-located installations report that the agent can reach the App key, the database and the executor.

## Architecture baseline

The accepted stack is Go, Chi, PostgreSQL, pgx, sqlc, and River. One local/self-hosted instance runs as a single `suiteward serve` binary (API and worker) and can manage multiple repositories, with a stated supported limit of 20 in the MVP. Canonical artifacts use a dedicated persistent local volume and content-addressed storage. Governance state is stored as normalized facts; see the [persistence contract](contracts/persistence.md).

The default integration uses a GitHub App with outbound API access. A local MCP call accelerates PR synchronization; independent periodic reconciliation (a 20 s paced poll with conditional requests, D-SYNC) recovers when the call is omitted. Public webhooks and an S3-compatible service are optional future adapters.

The core rule is unchanged: a repository may contain and propose tests, but it cannot redefine its own canonical test contract.

Bootstrap imports the existing tests from a pinned principal-branch commit, approved explicitly in the first real monitored PR. The path for a repository without an initial test contract (first-test PR) was delivered in M1.7b. Initial authority is established by the instance administrator. See [ADR 0010](decisions/0010-repository-bootstrap.md).

Approval and withdrawal use explicit revision-bound commands that carry a current owner TOTP code: `/suiteward approve <ref> <code>` and `/suiteward revoke <ref> <code>`. SuiteWard publishes the processed outcome in the PR; editing or deleting a processed command does not rewrite its recorded effect. See [ADR 0002](decisions/0002-exact-revision-approval.md), [ADR 0011](decisions/0011-approval-revocation-and-acknowledgments.md), and [ADR 0027](decisions/0027-agent-human-trust-separation.md).

The MVP has a human project owner and restricted agents. One eligible human approval is sufficient, including when that authorized human authored the PR. The agent uses its own GitHub identity, and the TOTP secret stays inside the instance (out of the agent's reach on isolated installations). Installations are isolated (full assurance) or co-located (supported, labeled). See [ADR 0012](decisions/0012-mvp-authorization-policy.md) and [ADR 0027](decisions/0027-agent-human-trust-separation.md).

Protected scope uses assisted discovery and a `.suiteward.yml` declaration reviewed in the PR. The default scope includes `.github/workflows/**`, the test runner configuration, and the scripts that run tests. The agent edits the declaration; SuiteWard publishes the exact inventory and approval command; the human approves. Candidate configuration cannot remove existing canonical protection. See [ADR 0013](decisions/0013-protected-scope-and-agent-assisted-review.md) and D-SCOPE in the [decision register](plan/decisions.md).

SuiteWard does not replace the project's own review and CI process for test acceptance: the ruleset still requires the project's test check. A confirmed PR merge triggers promotion assessment: SuiteWard retains exact human approval, validates the integrated contract, and conditionally promotes it. Promotion rests on the approval and the integrity of the protected files, not on a test run. Merge alone cannot authorize an unapproved contract. See [ADR 0014](decisions/0014-merge-triggered-canonical-promotion.md), whose note of 2026-10-27 says what M2.1 changes.

The `SuiteWard / Contract` check is a mandatory merge requirement, and setup also verifies that at least one project test check is required. Changes to protected tests or scope remain blocked until an eligible exact approval and integrity assessment establish readiness. GitHub protection must require the check from the expected SuiteWard App; connection alone does not establish enforcement. On private repositories on GitHub Free, branch protection and rulesets are unavailable, and SuiteWard reports "enforcement unavailable". The MVP verifies protection and gives manual instructions; automatic application and repair move post-MVP, while drift detection stays. See [ADR 0015](decisions/0015-required-contract-check.md), [ADR 0016](decisions/0016-integrated-protection-onboarding.md), and [ADR 0017](decisions/0017-protection-change-detection-and-confirmed-repair.md).

Ordinary protected changes are reviewed and accepted in the PR that contains them. Correcting a change that was already promoted uses a corrective PR, fresh exact approval, integration, and a new canonical version. All historical canonical versions, their artifacts, and audit records remain available and immutable. See [ADR 0018](decisions/0018-corrective-pr-rollback-and-audit-history.md).

Artifact bytes are verified when a version is written and again every hour. See [ADR 0007](decisions/0007-canonical-contract-authority.md).

A backup is `pg_dump` plus a tarball of the artifact volume, encrypted to the owner's recovery key, and `suiteward restore` loads it (M1.13); the runbook still re-bootstraps from the protected main branch when no backup can be used. Release-asset backups with the 29-day contingency, scheduled backups and the contract-change priority queue (`/suiteward prioritize`) are accepted but deferred to post-MVP. See ADRs [0019](decisions/0019-encrypted-backups-and-instance-recovery-key.md) through [0022](decisions/0022-contract-change-pr-priority.md).

The [ADR index](decisions/README.md) is the authoritative map of the decisions and their consequences. Each record distinguishes accepted direction from implementation details that remain open.

## Delivery and boundaries

M0 proves the domain. M1 delivers integrity governance. M2 is canonical execution: M2.1 is the engine, the receipt and the Go profile, M2.1b is the results comment and page of every pull request, M2.2 and M2.3 add Python and TypeScript, and M2.4 is execution maturity. The executor is a separate process that drives a container runtime CLI ([D-EXECUTION](plan/decisions.md#d-execution-first-executable-profile-and-execution-trust-model); it replaces the `DockerExecutionBackend` that ADR 0008 planned). M3 is security and trust, including justified supply-chain hardening. A verification phase ends with the first beta, and the managed service comes after it (the [roadmap](plan/README.md#roadmap), [D-ROADMAP](plan/decisions.md#d-roadmap-the-order-after-m21)).

Choosing Go for SuiteWard does not select the languages or testing frameworks of the repositories it will protect. The first executable test profile is Go (D-EXECUTION, accepted 2026-10-26), then Python and TypeScript.

SuiteWard's own development environment is settled separately from product deployment: native Windows development, Windows/Linux CI, local PostgreSQL in Podman, and project-local tool installation wherever practical. See [ADR 0023](decisions/0023-development-environment-and-project-local-tooling.md) and the [engineering plan](engineering-plan.md).

The codebase uses one application Go module, `cmd/` entry points, and internal domain, application, adapter, and composition boundaries. See [ADR 0024](decisions/0024-single-module-project-structure.md).

Development is test-first for authority, consent, promotion, idempotency, concurrency, and adapter behavior, reviewed by the `sw-reviewer` subagent. See [ADR 0025](decisions/0025-test-driven-development.md), the [development guide](development-guide.md), and the [CI and coverage guide](ci-and-coverage.md).

## Items still open

- Known limit: strict GitHub gate-to-merge-to-promotion coordination is not proved by database compare-and-set alone.
- The future managed execution location and trust model (D-MANAGED-EXECUTION). D-EXECUTION is decided: the first profile is Go, and what M2.1 defers is listed in the [decision register](plan/decisions.md#open) (D-EXECUTION-LATER).
- D-HARDENING-CONTROLS ([decision register](plan/decisions.md#open)).
- Post-MVP: Release backups, scheduled backups, the recovery mode of the restore, the priority queue, and automatic protection setup and repair.

An earlier system-design proposal may discuss candidates for these items. That discussion alone does not make them accepted decisions.
