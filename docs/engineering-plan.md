# SuiteWard engineering preparation

- **Updated:** 2026-09-26
- **Status:** Planning in progress. Accepted directions are distinguished from proposals and open decisions below.
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
- Use a separate branch and working copy for each implementation task, with an integrating agent coordinating dependencies and integration.
- Design tasks to minimize unnecessary blocking and allow independent tasks to run concurrently.
- Develop in the public `IgnisDevNE/SuiteWard` repository. The user corrected its visibility, and public access was confirmed on 2026-09-26. The organization's Free plan supports branch protections for this public repository; their configuration is still pending.
- Keep Memtrace configuration and repository membership specific to SuiteWard. Do not change sibling projects or enable a broader workspace implicitly.
- Develop natively on Windows, build and test in Windows and Linux CI, and use the existing Podman environment for local PostgreSQL tests. Install tools inside the project wherever practical, including the Go toolchain; reserve global installation for genuine host-level requirements. See [ADR 0023](decisions/0023-development-environment-and-project-local-tooling.md).
- Use one application Go module, entry points in `cmd/`, and implementation in `internal/`, separating domain rules, application use cases, adapters, and composition. Group related capabilities without creating a package per entity or agent. See [ADR 0024](decisions/0024-single-module-project-structure.md).
- Use standard Go `testing`, table-driven cases where useful, and small boundary fakes; structured `log/slog` logging; explicit errors inspected by identity/type; and manual dependency composition through constructors or parameters. See the [development guide](development-guide.md).
- Preserve the accepted product roadmap: M0 proves the domain; M1 delivers integrity governance; M2 adds canonical execution; M3 adds justified supply-chain hardening.
- Complete engineering preparation before starting product implementation. Infrastructure planning does not reopen the accepted application stack.

## Proposed task-authoring rules

These rules translate the accepted parallelism requirement into a proposed workflow. Concrete interfaces, task assignments, and tooling still need to be defined.

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

A task is ready when its author has enough agreed information to implement it without selecting an unresolved product policy. Task size should support a focused, reviewable PR; fixed duration and task-count targets are not yet selected.

### Illustrative parallel work

After agreeing the minimal domain vocabulary and relevant interface contracts, separate M0 tasks could cover manifest/content identity, approval-revision behavior, and canonical version transitions. Each task includes tests. Shared identifiers and the links between an approval, a proposal revision, and a canonical version require an explicit contract before those tasks start.

Their combined stale-approval and concurrent-promotion scenarios are required completion checks. Passing isolated task tests alone does not complete M0. This example is not the final task breakdown.

## Environment observations

Read-only observations made on 2026-09-26:

- The local SuiteWard directory contains planning documents and has no Git metadata or application module yet.
- `IgnisDevNE/SuiteWard` is an empty public repository with `main` as its configured default branch. It was initially observed as private; the user corrected the visibility, and a subsequent API check confirmed it is public. The current GitHub credentials have repository administration access.
- The GitHub organization reports the Free plan. Branch protections are available for this public repository. Required checks and protection rules still need to be planned and configured.
- Go was not found on the current Windows PATH. This does not establish whether a separate Linux environment has Go installed.
- Ubuntu 24.04 is listed in WSL. Podman's WSL machine is running, and the Windows `docker` command resolves to Podman's compatibility wrapper.
- The installed Memtrace CLI reports version 1.2.8. SuiteWard indexing, workspace isolation, and integration have not been configured or validated by this planning work.

GitHub plan constraints: [protected branches documentation](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches).

Memtrace documents repository-scoped storage and worktree overlays. Graph worktree selection and Fleet coordination scope are separate; their concrete configuration must be validated before relying on them for agent coordination. See [workspaces](https://memtrace.io/docs/concepts/workspaces) and [Fleet coordination](https://memtrace.io/docs/features/fleet).

## Decisions still required

- Exact tool versions, project-local bootstrap and cache layout, shared local/CI commands, and the production support matrix. Windows development, Windows/Linux CI, Podman PostgreSQL, and the local-installation policy are settled in ADR 0023.
- Configuration loading, migrations, detailed test allocation, and operational log settings. Core test, logging, error, and dependency-composition conventions are settled in the development guide. The module and package-boundary direction is settled in ADR 0024; concrete interfaces and aggregate definitions remain implementation work.
- CI jobs, security checks, coverage policy, Codecov authentication, and staged activation of required checks.
- SuiteWard-only Memtrace configuration, worktree handling, and decision-memory conventions.
- Backlog location, task readiness/completion rules, reviewer roles, concurrency limits, and escalation behavior.
- Engineering-foundation acceptance criteria and the concrete M0 task dependency graph. M1-M3 should retain milestone outcomes without prematurely fixing details explicitly deferred by the product ADRs.

This document does not claim that repository protections, CI, Codecov, Go installation, Memtrace, or application code are operational.
