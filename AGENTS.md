# Working on SuiteWard

Use English for source, documentation, commits, and product text. Versioned [ADRs](docs/decisions/README.md) and [development conventions](docs/development-guide.md) are canonical. Memtrace is supplementary and scoped to this repository; report missing or inconsistent results and continue with targeted local reads.

## Before implementation

1. Select a task from [the delivery plan](docs/plan/README.md). Check its start dependencies, decision gates, reviewed contracts, and owned files.
2. Agree ownership with the phase integrator. Use a task branch and separate worktree. Shared declarations, module files, migrations, generators, CI, and composition require coordinated ownership.
3. Use project-local tooling through `./scripts/dev.ps1`. Each checkout owns its caches, credentials, database volume, and ports. Never reset another task's database or change the host Podman default.

One phase produces one integration PR. Tasks include their own tests and contribute reviewed commits to that phase; they do not require separate task PRs. Later phases may start on reviewed contracts, but integrate in the plan's dependency order. Follow [parallel delivery](docs/plan/parallel-delivery.md).

## Implementation and verification

- Follow the [module boundaries](docs/decisions/0024-single-module-project-structure.md). Add packages with implemented consumers, not empty scaffolding.
- Preserve canonical immutability, candidate non-authority, exact approval, atomic promotion, evidence binding, and the distinction between evidence and authority.
- Run `./scripts/dev.ps1 check` and the task's applicable checks. Domain tests require no services. Real adapter claims require real adapter evidence.
- Update `docs/plan/backlog.json` when task contracts or dependencies change, then run `./scripts/check-plan.ps1 -WriteDocs`. Generated phase pages must match the backlog.
- Do not choose an unresolved product policy to unblock yourself. Record the concrete decision needed and take independent ready work.

## GitHub identity and integration

All GitHub writes use the authorized bot, within its granted permissions. Verify identity before publication. Never fall back to personal credentials after missing bot authentication or an access denial. Do not print tokens or keys.

The user authorizes each merge into `main`. Passing CI, a prepared PR, or a bot capability does not grant that authorization. Comments and messages to other people require explicit authorization beyond preparing a PR.

Report behavior changed, checks and exact revision tested, remaining limitations, and any blocked publication honestly. Follow [task evidence](docs/plan/task-evidence-template.md).
