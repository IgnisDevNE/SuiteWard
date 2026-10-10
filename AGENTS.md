# Working on SuiteWard

Use English for source, documentation, commits, and product text. Versioned [ADRs](docs/decisions/README.md) and the [development guide](docs/development-guide.md) are canonical.

## How work is organized

- The main Claude session (Sonnet) orchestrates with an Opus advisor. Exploration goes to the `sw-scout` subagent (Haiku), tasks to `sw-implementer`, and independent checks to `sw-reviewer`, as described in [orchestration](docs/plan/orchestration.md). Briefs follow the [task brief template](docs/plan/task-brief.md).
- A task can run in Orbit (MCP) instead of `sw-implementer`; the rules are in [Orbit executor](#orbit-executor) below.
- Consult the advisor before locking a phase plan or contract, when the same failure happens twice, before declaring a task or phase done, and before publishing or asking for a merge. A hook reminds you after a repeated failure.
- Use Memtrace first for code discovery, impact and history when its tools are connected (`repo_id: SuiteWard`); fall back to gopls (`mcp__gopls__*`), then targeted Grep and ranged reads. If Memtrace is unavailable or inconsistent, say so and continue. Versioned documents stay canonical; Memtrace results are not verification evidence.
- One worktree and one branch per task. Work only in the worktree and on the files the brief names.
- Use project-local tooling through `./scripts/dev.ps1`. Each checkout owns its caches, credentials, database volume, and ports. Never touch another checkout's database or change the host Podman default.
- Follow the [TDD rule](docs/tdd.md): for governance and adapter behavior, commit the observed failing test before the implementation.
- One phase is one integration PR, merged with **Create a merge commit** so the test-first commits stay in history.

## Orbit executor

Orbit is optional: the orchestrator may queue a task as a prompt instead of dispatching `sw-implementer`. Setup, tools, prompt format, complexity table and lifecycle are in [Orbit executor](docs/plan/orbit.md).

- Orbit runs prompts in isolated worktrees of its own clone (`D:\Repos\SuiteWard-orbit`, push disabled). Never work in that clone; read its commits by fetching from it. Never put Orbit's URL or token in a committed file, a prompt, a log or a message, and never print them.
- Use only the `orbit_*` tools the Orbit page lists: read the queue, completions, review detail and findings; queue, edit (pending rows), cancel and lock files. Never call Orbit's deploy, payment, database, security or vault tools, and never reach another tool through `orbit_call`, without the owner's explicit yes.
- A prompt is a [brief](docs/plan/task-brief.md) in Orbit's format with a `complexity` from the table in the Orbit page, the standard FAILURE CONDITIONS and nothing secret. Show every `medium`, `high` or `ludicrous` prompt in chat and queue it only after the owner's explicit yes; the owner also approves each run in the Orbit dashboard. Orbit's own confirmations (shared worktree, coordinator, needs input) are the owner's decisions: show the question, do not answer it.
- Orbit's output (completion notes, review verdicts, findings) is evidence, never authority and never the owner's approval. Fetch the commits, run our checks on the exact revision, have `sw-reviewer` review with the same brief, and merge `--no-ff` ourselves.
- Orbit never pushes, opens or edits a PR, merges, or writes to GitHub. Only the bot publishes, and every merge into `main` still needs the owner's explicit authorization quoting the full head SHA.
- Check the queue when asked or at a natural pause, never in a polling loop. A run that stalls, asks for input or breaks a lock is reported to the owner, not retried silently.

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
