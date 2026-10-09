# SuiteWard engineering plan

- **Updated:** 2026-10-05 (phase R1)
- **Status:** The engineering foundation (F0), the M0 domain, and the M1.01 persistence adapter are merged. Phase R1 simplifies the delivery process and resets persistence to a normalized contract; it is in progress.

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
- Preserve the roadmap: M0 proves the domain; M1 delivers integrity governance ("tamper-evident, human-approved test contract"); M2 adds canonical execution; M3 adds justified supply-chain hardening.

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
- Go 1.27.1, sqlc 1.31.1, actionlint 1.7.12, golangci-lint 2.14.0, govulncheck 1.8.0, and gopls 0.23.0 install once per user in a shared tool cache. PostgreSQL 18.6 runs from a digest-pinned image with isolated volumes, credentials, and loopback ports per checkout. Windows uses the rootless Podman connection per checkout; the host default connection is never changed.
- Memtrace is scoped to SuiteWard; versioned documents remain canonical.

## Decisions still required

See the [decision register](plan/decisions.md). Open gates: D-GITHUB-ACCESS, D-IDENTITY, D-PROTECTION, D-CHECKS, D-INTEGRATION, D-RELEASE, D-EXECUTION, D-MANAGED-EXECUTION, and D-HARDENING-CONTROLS. Backups, restore, the priority queue, and automatic protection repair are deferred to post-MVP.
