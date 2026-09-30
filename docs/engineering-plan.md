# SuiteWard engineering preparation

- **Updated:** 2026-09-30
- **Status:** Engineering foundation prepared for bot publication; authentication and required permissions are verified. The phase/task backlog is defined and application implementation has not started.
- **Purpose:** Define how SuiteWard itself will be developed before application implementation starts.

This plan builds on the [project baseline](project-baseline.md) and [accepted ADRs](decisions/README.md). It concerns our development workflow and infrastructure. The product's rules for governing customer repositories remain separate.

## Planning scope

| Workstream | Required outcome |
| --- | --- |
| Development practices | Complementary tools, conventions, testing, review, documentation, and a definition of done built around the accepted Go, Chi, pgx, sqlc, River, and PostgreSQL stack. |
| Project structure | Module and package organization, component responsibilities, allowed dependencies, and composition boundaries. |
| Engineering infrastructure | GitHub repository settings, local development, CI, Codecov, and Memtrace scoped exclusively to SuiteWard. |
| Delivery phases | An engineering foundation before M0, followed by the existing M0-M3 milestones with entry conditions and observable completion criteria. |
| Task design | Bounded outcomes, dependencies, ownership, acceptance criteria, and verification evidence. |
| Multiagent execution | Isolated work, opportunities for parallel execution, shared contracts, review, and ordered integration. |

## Accepted working direction

- Agents implement, test, review, and prepare pull requests. The user authorizes each integration into `main`; automation passing does not grant merge authorization.
- GitHub writes use the authorized bot within its permissions. Do not bypass a bot access denial using the user's personal credentials.
- Use a separate branch and working copy for each implementation task, with an integrating agent coordinating dependencies and integration.
- Design tasks to minimize unnecessary blocking and allow independent tasks to run concurrently.
- Develop in the public `IgnisDevNE/SuiteWard` repository. The user corrected its visibility, and public access was confirmed on 2026-09-26. Branch protections and the required CI gate are active as recorded below.
- Keep Memtrace configuration and repository membership specific to SuiteWard. Do not change sibling projects or enable a broader workspace implicitly.
- Versioned ADRs and project documents remain canonical. Memtrace is supplementary; report unavailable/inconsistent results and continue with targeted local reads.
- Develop natively on Windows, build and test in Windows and Linux CI, and use the existing Podman environment for local PostgreSQL tests. Install tools inside the project wherever practical, including the Go toolchain; reserve global installation for genuine host-level requirements. See [ADR 0023](decisions/0023-development-environment-and-project-local-tooling.md).
- Use one application Go module, entry points in `cmd/`, and implementation in `internal/`, separating domain rules, application use cases, adapters, and composition. Group related capabilities without creating a package per entity or agent. See [ADR 0024](decisions/0024-single-module-project-structure.md).
- Use standard Go `testing`, table-driven cases where useful, and small boundary fakes; structured `log/slog` logging; explicit errors inspected by identity/type; and manual dependency composition through constructors or parameters. See the [development guide](development-guide.md).
- Require applicable Windows/Linux build and tests, formatting/static analysis, Linux race and vulnerability checks, and later real PostgreSQL and generation checks. Codecov patch coverage is 90%, while total coverage is informational during M0. Configuration and staged activation are described in the [CI and coverage guide](ci-and-coverage.md).
- Preserve the accepted product roadmap: M0 proves the domain; M1 delivers integrity governance; M2 adds canonical execution; M3 adds justified supply-chain hardening.
- Complete engineering preparation before starting product implementation. Infrastructure planning does not reopen the accepted application stack.

## Delivery plan and task-authoring rules

The [delivery plan](plan/README.md) contains the canonical phase/task graph, concrete outcomes, ownership, start and integration dependencies, acceptance, and verification. Each phase produces one PR. Tasks use separate branches/worktrees and contribute reviewed commits to that phase. [Parallel delivery](plan/parallel-delivery.md) defines dispatch, review, integration, and escalation. Runtime signatures are frozen at each phase's contract checkpoint, not guessed by parallel workers.

1. **Separate start dependencies from integration dependencies.** A task may begin once its required behavior and interfaces are agreed, even if another independent implementation must merge before the final combined verification. Record both kinds of dependency.
2. **Define small shared contracts before splitting dependent work.** Agree only the types, behavior, errors, and invariants needed by the next tasks. Avoid designing all future adapters up front. A change to a shared contract returns to the coordinator and affected tasks.
3. **Give each task a demonstrable result.** Include its implementation and tests. Avoid assigning all tests to a final task or making one agent responsible for every entity while another waits to implement all behavior.
4. **Use fakes at agreed boundaries where useful.** They allow independent development, but tests against the actual adapters and integrated behavior remain necessary before the affected capability is complete.
5. **Assign shared-file ownership explicitly.** Coordinate dependency manifests, migration ordering, generators, and CI configuration. Each task owns its needed changes; the coordinator resolves overlapping edits instead of becoming the author of every shared-file change.
6. **Keep resources isolated as well as files.** Concurrent integration tests must use separate databases or equivalent isolated namespaces, ports, and temporary data. They must not share development credentials that grant human approval authority.
7. **Integrate in a controlled sequence.** Reconcile each PR with the current base, rerun affected checks, obtain the user's authorization, and verify the combined result. Parallel development does not imply concurrent unchecked merges.
8. **Keep useful work available.** The backlog should expose tasks ready to start and explain actual blockers. A blocked agent can take an independent ready task; it must not silently invent missing product policy or widen its current scope.

### Task template

Every executable task should state:

- Outcome and milestone.
- Applicable ADRs and agreed interfaces or contracts.
- Dependencies required to start, and dependencies required to integrate or finish.
- Owned area and shared files that require coordination.
- Explicit exclusions and decisions that remain outside the agent's authority.
- Acceptance scenarios and required verification.
- PR completion evidence and integration notes.

A task is ready when its recorded start dependencies and task-level gates are satisfied, consumed contracts are reviewed, and ownership is assigned. Each task includes behavior and tests; the phase, rather than each task, is the PR-sized delivery unit. Fixed duration estimates are deliberately omitted until implementation provides evidence.

### Initial parallel work

After M0.01-C0 reviews the value contracts, M0.01-A, B, and C cover artifact identity, authority, and immutable snapshots concurrently. M0.02-C0 can prepare proposal/consent contracts from those reviewed signatures before M0.01 merges. Subsequent consumers reconcile any contract change before integration.

Combined stale-approval and concurrent-promotion scenarios remain required completion checks. Passing isolated task tests alone does not complete M0. See the [M0 contract brief](plan/m0-contracts.md) and phase pages for the complete task breakdown.

## Environment observations

Initial read-only observations made on 2026-09-26, before the CI preparation described below:

- The local SuiteWard directory contains planning documents and has no Git metadata or application module yet.
- `IgnisDevNE/SuiteWard` is an empty public repository with `main` as its configured default branch. It was initially observed as private; the user corrected the visibility, and a subsequent API check confirmed it is public. The current GitHub credentials have repository administration access.
- The GitHub organization reports the Free plan. Branch protections are available for this public repository. Required checks and protection rules still need to be planned and configured.
- Go was not found on the current Windows PATH. This does not establish whether a separate Linux environment has Go installed.
- Ubuntu 24.04 is listed in WSL. Podman's WSL machine is running, and the Windows `docker` command resolves to Podman's compatibility wrapper.
- The installed Memtrace CLI reports version 1.2.8. SuiteWard indexing, workspace isolation, and integration have not been configured or validated by this planning work.

GitHub plan constraints: [protected branches documentation](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches).

Memtrace documents repository-scoped storage and worktree overlays. Graph worktree selection and Fleet coordination scope are separate; their concrete configuration must be validated before relying on them for agent coordination. See [workspaces](https://memtrace.io/docs/concepts/workspaces) and [Fleet coordination](https://memtrace.io/docs/features/fleet).

## CI preparation update

Git is initialized, and the user-authorized documentation bootstrap `a4be88c` is published on `main`. The user merged CI/Codecov [PR #1](https://github.com/IgnisDevNE/SuiteWard/pull/1) as `650fb81`. Its foundation checks passed on GitHub-hosted Windows and Linux runners both in the PR and in [the main-branch run](https://github.com/IgnisDevNE/SuiteWard/actions/runs/36287285580).

`main` requires an up-to-date PR and the observed `CI / Gate` check from GitHub Actions, with administrator enforcement and force pushes/deletion disabled. The current personal CLI account is not authorization for agent publication: subsequent writes require the designated bot. Human merge authorization remains a separate requirement.

Codecov already recognizes SuiteWard through the organization's existing GitHub App installation. No new upload token or organization-wide permission change was needed. The first real coverage report remains pending application code and tests.

## Decisions still required

- Production support matrix and migration tooling. Local bootstrap, cache/resource isolation, and Go/PostgreSQL/sqlc/actionlint/govulncheck versions are implemented in the [local development workflow](local-development.md).
- Configuration loading, migrations, detailed test allocation, and operational log settings. Core test, logging, error, and dependency-composition conventions are settled in the development guide. The module and package-boundary direction is settled in ADR 0024; concrete interfaces and aggregate definitions remain implementation work.
- First real Go/coverage execution and validation of the Codecov patch requirement, separate credentials for technically enforced human merge authority, persistence/generation jobs when those capabilities exist, and M0's final project-wide coverage non-regression policy. Foundation CI and its branch requirement are active; initial quality policy and upload authentication are settled in the CI guide.
- Memtrace worktree/Fleet validation and resolution of the code-search scope error. The live daemon and watcher are scoped to SuiteWard, Cortex lookup works, and local versioned documents remain canonical.
- Bot identity, authentication, and publication permissions are verified as `ignisdevne[bot]` (App 5028495, installation 163660443). The owner-authorized Workflows write grant is active for the shared installation; publication tokens are restricted to SuiteWard. Backlog location, readiness/completion, reviewer roles, initial worker capacity, and escalation are defined in the delivery plan.
- Product choices identified in the [decision register](plan/decisions.md). The plan defines the M0-M3 delivery graph without treating open policy as an implementation default.

## Local development preparation update

The checkout now has a checksum-verified Go 1.27.1 SDK, sqlc 1.31.1, actionlint 1.7.12, and govulncheck 1.8.0 under ignored project-local paths. PostgreSQL 18.6 uses a digest-pinned official image and isolated named volumes, credentials, and dynamically assigned loopback ports per checkout.

The PowerShell entry point provides setup, verification, tool execution, and database lifecycle commands. Windows uses the existing rootless Podman connection per checkout because the current rootful 6.0.2 connection fails localhost forwarding. The host default connection and unrelated workloads are preserved. See the [local development guide](local-development.md) for verification boundaries and recovery.

The environment is prepared for M0. This does not establish application behavior, real Codecov coverage, persistence integration, or production deployment readiness. Publication uses the verified bot and its granted permissions; there is no fallback to personal credentials. Product implementation follows the foundation's authorized integration.

The current [readiness snapshot](plan/readiness.md) distinguishes verified local work, existing hosted configuration, and remaining publication/activation steps. F0 additionally makes script syntax, actionlint, and plan graph/document freshness part of foundation CI.
