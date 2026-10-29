# SuiteWard engineering plan

- **Updated:** 2026-10-29 (phase [M2.1b](plan/phases/M2.1b.md), results communication, is merged ([#62](https://github.com/IgnisDevNE/SuiteWard/pull/62)) and released as the pre-release `v0.5.0-alpha.2`, the latest); 2026-10-28 (phase M2.1, canonical execution and the receipt with the Go profile, is merged ([#60](https://github.com/IgnisDevNE/SuiteWard/pull/60)) and released as the pre-release `v0.5.0-alpha.1`, and verified live on both tiers)
- **Status:** The MVP phases through M1.11 are delivered and released as `v0.2.0` (2026-10-22), after the security hardening phase [SH](plan/phases/SH.md). The installer phases M1.12 to M1.15 are released as alphas. [M2.1](plan/phases/M2.1.md), canonical execution and the receipt with the Go profile, is merged ([#60](https://github.com/IgnisDevNE/SuiteWard/pull/60)) and released as the pre-release `v0.5.0-alpha.1`, verified live on both tiers.

This plan builds on the [project baseline](project-baseline.md) and the [accepted ADRs](decisions/README.md). It concerns how SuiteWard itself is developed; the product's rules for governing customer repositories are separate.

## Working direction

- Agents implement, test, review, and prepare pull requests. The user authorizes each integration into `main`; passing automation does not grant merge authorization.
- GitHub writes use the authorized bot (`ignisdevne[bot]`) within its permissions. Never fall back to the user's personal credentials after a bot access denial.
- Each task uses its own branch and worktree. One phase produces one integration PR, merged with a merge commit so the test-first commits stay in history.
- Versioned ADRs and project documents are canonical. Memtrace is supplementary and scoped to this repository; report unavailable or inconsistent results and continue with targeted local reads.
- Develop natively on Windows; build and test on Windows and Linux in CI; use Podman for local PostgreSQL. Pinned tools are installed once into a shared, checksum-verified per-user cache; Go caches, state, and the database stay per checkout. See [ADR 0023](decisions/0023-development-environment-and-project-local-tooling.md) and [local development](local-development.md).
- Use one Go module, entry points in `cmd/`, and implementation in `internal/` separating domain, application, adapters, and composition. See [ADR 0024](decisions/0024-single-module-project-structure.md).
- Use standard Go `testing`, structured `log/slog`, explicit errors, and manual dependency composition. See the [development guide](development-guide.md).
- Test-first for authority, consent, promotion, idempotency, concurrency, and adapter behavior; the `sw-reviewer` subagent checks the order. See [ADR 0025](decisions/0025-test-driven-development.md) and the [TDD rule](tdd.md).
- Required checks cover Windows/Linux build and tests, formatting and static analysis, Linux race and vulnerability checks, and real PostgreSQL checks for the persistence adapter. Patch coverage is 90% and required; project coverage is informational. See the [CI and coverage guide](ci-and-coverage.md).
- Preserve the roadmap: M0 proves the domain; M1 delivers integrity governance ("tamper-evident, human-approved test contract"); M2 is canonical execution (M2.1 the engine, the receipt and the Go profile; M2.1b results communication; then Python and TypeScript; M2.4 execution maturity); M3 is security and trust (the TOTP secret at rest, the forced resource limit, an egress allow-list, the fork merge command, owner recovery, the backup shortcuts, an evaluation of remote attestation); a verification phase ends with the first beta, and the managed service comes after it. The order is in the [roadmap](plan/README.md#roadmap) and [D-ROADMAP](plan/decisions.md#d-roadmap-the-order-after-m21).

## Task design rules

1. Separate start dependencies from integration dependencies: a task can begin once the interfaces it consumes are agreed.
2. Agree small shared contracts before splitting dependent work, and return any change to a shared contract to the coordinator. The [persistence contract](contracts/persistence.md) is the current example.
3. Give each task a demonstrable result with its own tests.
4. Use fakes at agreed boundaries, but real adapters need real-adapter evidence before a capability is complete.
5. Assign shared files (dependency manifests, migrations, generators, CI) explicitly.
6. Keep resources isolated: concurrent integration tests use separate databases, ports, and temporary data, and never share credentials that grant human approval authority.
7. Integrate in a controlled sequence: reconcile with the base, rerun affected checks, obtain the user's authorization, and verify the combined result.
8. Do not invent missing product policy to unblock a task. Record the decision needed in the [decision register](plan/decisions.md) and take independent ready work.

A task states its outcome, applicable ADRs and contracts, dependencies, owned files, exclusions, acceptance scenarios, and verification.

## Environment

- GitHub organization `IgnisDevNE` is on the Free plan. The public `SuiteWard` repository uses branch protection requiring an up-to-date PR and `CI / Gate`, with administrator enforcement and force pushes and deletion disabled.
- Codecov is configured through the organization's existing GitHub App installation.
- Bot identity: `ignisdevne[bot]` (App 5028495, installation 163660443), with Workflows write access for SuiteWard.
- Go 1.27.3, sqlc 1.31.1, actionlint 1.7.12, golangci-lint 2.14.0, zizmor 1.31.1, gitleaks 8.30.1, trivy 0.76.0, govulncheck 1.8.0, and gopls 0.23.0 install once per user in a shared tool cache. cosign 3.1.4 is pinned in `dev/tools.json` too, for the release workflow only. PostgreSQL 18.6 runs from a digest-pinned image with isolated volumes, credentials, and loopback ports per checkout. Windows uses the rootless Podman connection per checkout; the host default connection is never changed.
- Memtrace is scoped to SuiteWard; versioned documents remain canonical.

## Decisions still required

See the [decision register](plan/decisions.md). Open gates: D-MANAGED-EXECUTION and D-HARDENING-CONTROLS. D-EXECUTION and D-RELEASE are decided, and D-EXECUTION-LATER lists what M2.1 defers. Backups are encrypted and restored with `suiteward restore` since M1.13 (D-ARCHIVE, D-RESTORE); Release-asset backups, scheduled backups, the priority queue and automatic protection repair stay post-MVP.
