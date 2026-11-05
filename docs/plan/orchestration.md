# Orchestration

The shared agent workflow (roles, task lifecycle, parallel waves, unattended commands, worktree cleanup, reporting and the itsaplan flow) is in the global `~/.claude/AGENTS.md`. This guide keeps only what is specific to SuiteWard.

The main Claude session orchestrates SuiteWard work. It delegates exploration, implementation and review to subagents defined in `.claude/agents/`, keeps an Opus advisor for critical decisions, and leaves mechanical decisions to deterministic hooks and scripts. The process is intentionally light: no evidence files in the repository and no generated plan pages. Models and the advisor are set in `.claude/settings.json`.

| Role | Model | Responsibility |
| --- | --- | --- |
| Orchestrator (main session) | Sonnet, high effort | Plans, briefs, integrates, verifies, publishes, reports. |
| Advisor (`advisorModel`) | Opus, on demand | Reviews the plan before it locks, a repeated failure, and the diff before done or publication. Each call re-reads the whole transcript, so keep one session per phase. |
| `sw-scout` | Haiku, max effort | Read-only discovery: Memtrace, then gopls, then targeted reads; structured summaries. |
| `sw-implementer` | Sonnet, very high (`xhigh`) effort | One task per worktree, test-first. |
| `sw-reviewer` | Opus 5.5, high effort | Independent review of each task and of the phase diff. |
| Orbit executor (optional) | per prompt complexity | Runs queued prompts built from briefs in isolated worktrees, through MCP. Its output is evidence, not authority. See [Orbit executor](orbit.md). |
| Hooks and `./scripts/dev.ps1` | none | Formatting after edits, repeated-failure reminders, checks, lint. |

Subagents inherit the advisor. The ponytail plugin injects its keep-it-small rules into the orchestrator, `sw-implementer` and `sw-reviewer` (`PONYTAIL_SUBAGENT_MATCHER`). It is declared in `.claude/settings.json` from `DietrichGebert/ponytail` at tag `v5.1.0` with automatic updates off; the audited commit is `9cc65d03aa2da1db7121b912d03596409ee340b8` (a tag pin, not a cryptographic one: re-audit before changing the tag). Each machine installs it once with `claude plugin install ponytail@ponytail --scope project` after trusting the project. The gopls MCP server in `.mcp.json` is pre-approved through `enabledMcpjsonServers` in `.claude/settings.json`; it needs the pinned tools (`./scripts/dev.ps1 tools`).

Two consequences of the configuration:

- Memtrace and gopls index the orchestrator's checkout, so it stays on the phase branch while a phase runs (integration happens there; the phase branch is not checked out in another worktree). Treat their answers as phase-branch state and read the file in your own worktree before editing it. Memtrace labels that checkout with the branch it is on, so during a phase queries pass the phase branch. Task worktrees are not indexed as overlays; the Memtrace rules and known limits are in the project's [AGENTS.md](../../AGENTS.md).
- `CLAUDE_CODE_SUBAGENT_MODEL=haiku` also applies to built-in agents that inherit the model (such as `general-purpose` and `Plan`). Pass `model: "sonnet"` (or `"opus"`) explicitly when using them for design or substantive work.

## Checks and publishing

The project's checks are `./scripts/dev.ps1 check`, plus `./scripts/dev.ps1 persistence` when a change touches the PostgreSQL adapter or migrations. A task routed to Orbit is implemented through the [Orbit lifecycle](orbit.md#lifecycle); the orchestrator's own verification, the review and the merge are unchanged. Every GitHub write, the phase PR included, goes through the `ignisdevne[bot]` GitHub App; `scripts/bot-token.ps1` gives its token to `gh` and API calls ([GitHub and merges](../../AGENTS.md#github-and-merges)).

## SuiteWard governs this repository

Since M1.8, the host instance governs IgnisDevNE/SuiteWard. Its scope is in [`.suiteward.yml`](../../.suiteward.yml): the tests, the testdata, the CI scripts and their configuration, plus `.suiteward.yml` and `.github/workflows/**`, which are always protected. `main` requires `SuiteWard / Contract` from the host's App and `CI / Gate`.

- A PR that changes a protected file shows `approval-missing` until the owner posts `/suiteward approve <ref> <code>` on its current revision. Every push that changes a protected file needs a new approval. The orchestrator says which protected files a PR changes when it asks for the merge.
- The agent never posts a `/suiteward` command, never generates a TOTP code, and never proposes, skips or revokes. Those are the owner's.
- The agent never reaches the host, its App key or its database. If one of them becomes reachable, it tells the owner instead of using it.
- Keep test changes in as few PRs and pushes as possible, since each protected revision costs the owner one approval.

## Worktrees, databases, and ports

Worktrees live at `D:/Repos/SuiteWard-worktrees/<phase>-<task>`, for example `D:/Repos/SuiteWard-worktrees/r1-b1`. Each checkout has its own caches, credentials, database volume, and ports, created through `./scripts/dev.ps1`. No task resets another checkout's database or changes the host Podman default.

**After a task branch is merged** into the phase branch, the orchestrator removes its worktree. `git worktree remove` stops at the read-only Go module cache that `./scripts/dev.ps1` keeps inside each checkout (`.cache/go-mod`), so delete that folder by its exact path with `Remove-Item -Recurse -Force` once git no longer lists it.

**After a phase's PR is merged**, the orchestrator:
- resets the Memtrace store and indexes `main` afresh (owner, 2026-10-21). Stale branch copies and records then never pile up, and a fresh index is much faster than cleaning a large store record by record:
  1. In PowerShell, from the main checkout, set `$env:MEMTRACE_MEMDB_DATA_DIR = 'D:\Repos\SuiteWard\.memdb'` and `$env:MEMTRACE_ALLOW_DESTRUCTIVE_DATA_DIR = '\\?\D:\Repos\SuiteWard\.memdb'`, then run `memtrace reset`.
     - The reset stops the Memtrace runtime.
     - It moves the old store to `.memdb.history/`, which the owner purges with `memtrace history purge --store D:\Repos\SuiteWard\.memdb --delete-forever`.
  2. Start the runtime again as a background command, with no startup watch: `$env:MEMTRACE_NO_STARTUP_WATCH = '1'; memtrace start --headless`.
     - Without the variable the nightly registers a `wip` overlay at startup.
  3. Index through the MCP tool `index_directory`, with `path` the main checkout, `repo_id: SuiteWard`, `branch: main` and `defer_replay: true` as the only option.
     - Embeddings stay on, so every graph and search feature works.
     - Only the git-history replay is deferred. The project does not use it, and it is the slow part.
     - If the call answers `peer_indexing`, the runtime is already indexing: wait for its job (`list_jobs`, `check_job_status`) instead of starting another.
     - A job can stay `running` after the graph is complete. Check `get_repository_stats` before waiting longer.

**At a phase start**, after checking out the phase branch in the main checkout, index it once with `branch` set to the phase branch and `defer_replay: true`. The nightly refuses any other label for that checkout. The label change re-embeds every symbol, so do it once per phase.
