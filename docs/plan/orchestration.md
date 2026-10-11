# Orchestration

The main Claude session orchestrates SuiteWard work. It delegates exploration, implementation and review to subagents defined in `.claude/agents/`, keeps an Opus advisor for critical decisions, and leaves mechanical decisions to deterministic hooks and scripts. The process is intentionally light: no evidence files and no generated plan pages. Models and the advisor are set in `.claude/settings.json`.

| Role | Model | Responsibility |
| --- | --- | --- |
| Orchestrator (main session) | Sonnet, high effort | Plans, briefs, integrates, verifies, publishes, reports. |
| Advisor (`advisorModel`) | Opus, on demand | Reviews the plan before it locks, a repeated failure, and the diff before done or publication. Each call re-reads the whole transcript, so keep one session per phase. |
| `sw-scout` | Haiku, medium effort | Read-only discovery: Memtrace, then gopls, then targeted reads; structured summaries. |
| `sw-implementer` | Sonnet, high effort | One task per worktree, test-first. |
| `sw-reviewer` | Sonnet, high effort | Independent review of each task and of the phase diff. |
| Orbit executor (optional) | per prompt complexity | Runs queued prompts built from briefs in isolated worktrees, through MCP. Its output is evidence, not authority. See [Orbit executor](orbit.md). |
| Hooks and `./scripts/dev.ps1` | none | Formatting after edits, repeated-failure reminders, checks, lint. |

Subagents inherit the advisor. The ponytail plugin injects its keep-it-small rules into the orchestrator, `sw-implementer` and `sw-reviewer` (`PONYTAIL_SUBAGENT_MATCHER`). It is declared in `.claude/settings.json` from `DietrichGebert/ponytail` at tag `v5.1.0` with automatic updates off; the audited commit is `9cc65d03aa2da1db7121b912d03596409ee340b8` (a tag pin, not a cryptographic one: re-audit before changing the tag). Each machine installs it once with `claude plugin install ponytail@ponytail --scope project` after trusting the project. The gopls MCP server in `.mcp.json` is pre-approved through `enabledMcpjsonServers` in `.claude/settings.json`; it needs the pinned tools (`./scripts/dev.ps1 tools`).

Two consequences of the configuration:

- Memtrace and gopls index the orchestrator's checkout, so it stays on the phase branch while a phase runs (integration happens there; the phase branch is not checked out in another worktree). Treat their answers as phase-branch state and read the file in your own worktree before editing it. For a task's own changes the orchestrator indexes the task worktree as a Memtrace overlay and subagents pass `worktree` to `find_code` (see `AGENTS.md`).
- `CLAUDE_CODE_SUBAGENT_MODEL=haiku` also applies to built-in agents that inherit the model (such as `general-purpose` and `Plan`). Pass `model: "sonnet"` (or `"opus"`) explicitly when using them for design or substantive work.

## Roles

- **Orchestrator** (main session): plans phases, writes one [brief](task-brief.md) per task, creates worktrees, dispatches subagents, integrates reviewed work, runs phase checks, publishes through the bot, and reports to the user. It owns shared files and decides conflicts. It sends discovery to `sw-scout` instead of reading broadly itself.
- **Advisor**: consulted by the orchestrator (and subagents) at the checkpoints above. The model decides when to call it; instructions and the repeated-failure hook make the calls expected, not guaranteed.
- **`sw-scout`**: answers "where, what calls, what breaks" questions with references and short summaries; never edits.
- **`sw-implementer`**: implements exactly one brief inside its worktree, test-first per the [TDD rule](../tdd.md), and returns a report. It never pushes, opens PRs, or touches files it does not own; if the brief is wrong it stops and reports.
- **`sw-reviewer`**: given the same brief, independently checks the branch for correctness, scope, invariants, and test-before-implementation commit order. It reports findings but does not fix them.

## Task lifecycle

1. Brief: goal, owned files, consumed contracts, acceptance, verification.
2. Worktree and branch `task/<Phase>-<Task>` created from the phase branch.
3. `sw-implementer` works test-first and runs `./scripts/dev.ps1 check` (and `persistence` when applicable). A task routed to Orbit runs this step through the [Orbit lifecycle](orbit.md#lifecycle); the orchestrator's own verification, the review and the merge are unchanged.
4. `sw-reviewer` reviews. Blocking findings go back to the implementer for at most two fix rounds; then the orchestrator decides, or escalates to the user.
5. The orchestrator merges the task branch into the phase branch with `--no-ff`.
6. When the phase's tasks are merged, the orchestrator runs the phase checks on the final revision.
7. The orchestrator publishes one PR for the phase using the `ignisdevne[bot]` App.
8. The user authorizes the merge; it is a merge commit, so test-first commits are preserved.

## Parallel waves

Tasks without shared files and with satisfied dependencies run in parallel in one wave. Shared declarations, module files, migrations, generators, CI, and composition have exactly one owner per wave; a task needing such a file it does not own stops and reports. A later wave starts only after its dependencies are merged into the phase branch, or when it depends only on a frozen contract document.

## Worktrees, databases, and ports

Worktrees live at `D:/Repos/SuiteWard-worktrees/<phase>-<task>`, for example `D:/Repos/SuiteWard-worktrees/r1-b1`. Each checkout has its own caches, credentials, database volume, and ports, created through `./scripts/dev.ps1`. No task resets another checkout's database or changes the host Podman default.

## Reporting to the user

The orchestrator reports, per phase and honestly: behavior changed, commits and their test-first order, checks run and the exact revision tested, remaining limitations and open decisions, and any blocked publication. It asks before any merge into `main` and before any message to other people.
