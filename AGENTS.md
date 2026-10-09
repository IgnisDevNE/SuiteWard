# Working on SuiteWard

Use English for source, documentation, commits, and product text. Versioned [ADRs](docs/decisions/README.md) and the [development guide](docs/development-guide.md) are canonical.

## How work is organized

- The main Claude session (Sonnet) orchestrates with an Opus advisor. Exploration goes to the `sw-scout` subagent (Haiku), tasks to `sw-implementer`, and independent checks to `sw-reviewer`, as described in [orchestration](docs/plan/orchestration.md). Briefs follow the [task brief template](docs/plan/task-brief.md).
- Consult the advisor before locking a phase plan or contract, when the same failure happens twice, before declaring a task or phase done, and before publishing or asking for a merge. A hook reminds you after a repeated failure.
- Use Memtrace first for code discovery, impact and history when its tools are connected (`repo_id: SuiteWard`); fall back to gopls (`mcp__gopls__*`), then targeted Grep and ranged reads. If Memtrace is unavailable or inconsistent, say so and continue. Versioned documents stay canonical; Memtrace results are not verification evidence.
- One worktree and one branch per task. Work only in the worktree and on the files the brief names.
- Use project-local tooling through `./scripts/dev.ps1`. Each checkout owns its caches, credentials, database volume, and ports. Never touch another checkout's database or change the host Podman default.
- Follow the [TDD rule](docs/tdd.md): for governance and adapter behavior, commit the observed failing test before the implementation.
- One phase is one integration PR, merged with **Create a merge commit** so the test-first commits stay in history.

## Invariants

Preserve canonical immutability, candidate non-authority, exact-revision approval, atomic promotion, and the distinction between evidence and authority. Do not choose an unresolved product policy to unblock yourself; record the decision needed in [open decisions](docs/plan/decisions.md) and take independent ready work.

## Writing code

- Write the least code that does the job (the project enables the ponytail plugin): standard library first, no speculative abstractions, but never drop validation, error handling, or security.
- Nil checks belong only at trust boundaries and must return an explicit error. Never hide an impossible state with a zero value, `return nil, nil`, a swallowed error, or an empty `default`. See the [nil-check rule](docs/development-guide.md#nil-checks).

## Verification

Run `./scripts/dev.ps1 check` before finishing, plus `./scripts/dev.ps1 persistence` when touching the PostgreSQL adapter or migrations. Domain tests need no services; a claim about a real adapter needs real adapter evidence.

## GitHub and merges

All GitHub writes go through the `ignisdevne[bot]` GitHub App, within its granted permissions; obtain its token for `gh` and API calls with `scripts/bot-token.ps1`. Verify the identity before publishing. Never fall back to personal credentials after a missing token or an access denial, and never print tokens or keys.

The user authorizes every merge into `main` and every message to other people. Passing CI, a prepared PR, or bot capability is not that authorization.

Report honestly: behavior changed, checks run and the exact revision tested, remaining limitations, and any blocked publication.
