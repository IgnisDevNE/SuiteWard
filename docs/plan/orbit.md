# Orbit executor

Orbit is a prompt queue that runs coding agents in isolated worktrees and exposes itself through an MCP server. The orchestrator can send it tasks instead of dispatching `sw-implementer`: the orchestrator plans, writes and queues the prompts and reads what comes back; Orbit executes. Nothing Orbit returns is authority. Its completion notes and review verdicts are evidence, like Memtrace results: the orchestrator verifies the exact revision, the independent `sw-reviewer` reviews it, the advisor is consulted at the usual checkpoints, and merges, GitHub writes and publication stay as in [orchestration](orchestration.md).

This page rests on a read-only exploration of the server (Orbit 2.163.64, 25 direct tools, 273 in total) and on the pilot runs recorded in [Pilot results](#pilot-results).

## Setup (owner, once)

1. **A dedicated clone.** The Orbit project's `path` is a separate clone of this repository, `D:\Repos\SuiteWard-orbit`, created from `main` with the [safeguards](#clone-safeguards) below. Not the orchestrator's checkout (Orbit would write into the working tree and branch the orchestrator is using) and not a subfolder of it (agents work only inside the registered path, so a subfolder hides the product code). `path` is a protected setting: register the project with the clone's path.
2. **Project settings.** `gitPublish.mode` is `commit-only`, `dispatchWithoutHumanApproval` is `false` (the owner approves every prompt in the dashboard before it runs) and worktree isolation is required. The project's runtime shows as `node` with no provisioning; that is enough, because the Go toolchain comes from the shared tool cache of `./scripts/dev.ps1`.
3. **The agent runtime is logged in.** Orbit launches Claude Code for each run. Before the login, runs end as `failed` with no notes, and every failed attempt takes a number suffix (#1, #1a, #1b). Check the login before queueing.
4. **MCP connection, local scope.** The URL and the token never enter a committed file of this repository. Once `.mcp.json` is untracked in the clone (see the safeguards), Orbit writes its entry there. In the owner's terminal, **from the orchestrator's checkout** `D:\Repos\SuiteWard` (the folder decides the scope), with the clone's file as the source (nothing is printed):

   ```powershell
   cd D:\Repos\SuiteWard
   $o = (Get-Content -Raw D:\Repos\SuiteWard-orbit\.mcp.json | ConvertFrom-Json).mcpServers.orbit
   claude mcp add --transport http --scope local orbit $o.url --header "Authorization: $($o.headers.Authorization)" --header "X-Orbit-Project: suiteward"
   ```

   The tools appear as `orbit_*` in the next session opened in that folder. Local scope binds the entry, and its `X-Orbit-Project` header, to that folder only: with user scope every Claude Code session on the machine would inherit the binding to this project, including sessions for other Orbit projects. `X-Orbit-Project` must be the slug of the project registered in step 1. `--header` takes several values, so the name and the URL come first.
5. **Known issue of Orbit 2.163.64: Node fails inside runs.** The app unpacks `agent-browser-preload.cjs` to `app.asar.unpacked\src` but not the two modules it requires, `managed-child-process.cjs` and `test-process-termination-guard.cjs`, which stay inside `app.asar`. Runs put that preload in `NODE_OPTIONS`, so every Node process of a run fails with `Cannot find module './managed-child-process.cjs'`, including Orbit's own `commit-msg` hook and its report delivery: no commit, no report. The fix on the host is to extract the two files from `app.asar` (the archive header carries their SHA-256) into `app.asar.unpacked\src` next to the preload, or to install an Orbit version that does it. Check: with `NODE_OPTIONS` set to `--require "<path of the preload>"` (the path has spaces, keep the quotes, and write it with forward slashes: Node drops the backslashes of a Windows path in `NODE_OPTIONS`), `node -e "console.log('ok')"` must print `ok`. An Orbit update can overwrite that folder: repeat the check after updating.

## Clone safeguards

Orbit scaffolds its own files into the project folder, writes its MCP entry (with the token) into `.mcp.json`, may rewrite `.claude/settings.json` and `.gitignore`, and its agents run with permissions bypassed. The repository is public. These settings of the clone (none is versioned) keep Orbit's files and the owner's identity out of commits and keep the clone from reaching GitHub:

```powershell
$d = 'D:\Repos\SuiteWard-orbit'
git clone https://github.com/IgnisDevNE/SuiteWard.git $d
git -C $d remote set-url --push origin DISABLED-the-orbit-clone-never-pushes
git -C $d config credential.helper ''
git -C $d config user.name 'ignisdevne[bot]'
git -C $d config user.email '332310975+ignisdevne[bot]@users.noreply.github.com'
git -C $d update-index --skip-worktree .claude/settings.json
git -C $d rm --cached .mcp.json
```

- **No push.** A push from the clone fails and a fetch works. Orbit's own `pre-push` hook only records pushes and does not block them, and the agents use the owner's credentials unless told otherwise, so two settings are the barrier: the disabled push URL of `origin` and the empty `credential.helper` (a push to an explicit GitHub URL then fails with "could not read Username"). The prompt clauses and the review of every fetched diff are the other layers. The dashboard shows "Branch is ahead of origin, Push Now" after a shipped run: use "Dismiss", never "Push Now".
- **Identity.** Orbit commits with the clone's git identity. It is the bot's, as for every commit of this repository, so the owner's personal name and e-mail never reach this public history. Do not merge a commit whose author is anyone else.
- **`.mcp.json`.** Orbit refuses to write its credential into a tracked file ("`.mcp.json` is already tracked"), and here it is tracked (it carries the `gopls` server). `git rm --cached` untracks it in the clone's index only: the file stays on disk, nothing is committed, the repository and `main` are unchanged. Orbit can track it again after some operations, and a `git reset --hard` also does: repeat the command, or flag it with `git update-index --skip-worktree .mcp.json`.
- **`.claude/settings.json`** may be rewritten by Orbit without dirtying the clone or entering `git add -A`. If a merge into the clone complains about it, clear the flag (`--no-skip-worktree`), update, and set it again. Orbit also appends a managed block to the tracked `.gitignore`, which shows as a modified file in the clone.
- **`.git/info/exclude`** lists the scaffold Orbit creates, which is untracked in this repository: `/ORBIT.md`, `/CLAUDE.md`, `/GEMINI.md`, `/COMPLETION_LOG.md`, `/COMPLETION_LOG.archive.md`, `/context/`, `/.orbit/`, `/.sweep/`, `/.codex/`, `/.orbit-run-owner.json`, plus the entries Orbit adds itself (`.worktrees/`, `.orbit-tmp/`, `.orbit/runs/`), and `/.memtraceignore` (a clone-local file listing `.worktrees/`, `.orbit-tmp/` and `.orbit/`, so the clone's Memtrace index never takes in run worktrees).
- **Hooks.** For each run Orbit sets `core.hooksPath` to a directory of its own (`pre-commit`, `commit-msg`, `post-commit`, `pre-push`). Its `pre-commit` delegates to the clone's `.git/hooks/pre-commit`, so the guard below runs; its `commit-msg` validates the subject against the prompt; `post-commit` and `pre-push` only record.
- `.git/hooks/pre-commit` refuses a commit that stages a managed file or adds a bearer token or the host name of the Orbit instance (fill in `orbit_host` with the instance's domain; the real domain stays out of this repository):

```sh
#!/bin/sh
managed='^(\.mcp\.json|\.claude/settings\.json|\.codex/|\.orbit/|\.sweep/|context/|ORBIT\.md|CLAUDE\.md|GEMINI\.md|COMPLETION_LOG)'
staged=$(git diff --cached --name-only | grep -E "$managed")
if [ -n "$staged" ]; then
	echo "pre-commit: Orbit-managed files are staged, commit refused:" >&2
	echo "$staged" >&2
	exit 1
fi
orbit_host='[a-z0-9]{12,}\.orbit\.<domain of the Orbit instance, dots escaped>'
if git diff --cached -U0 | grep '^+' | grep -Eq "Bearer [A-Za-z0-9._~+/=-]{20,}|$orbit_host"; then
	echo "pre-commit: a credential or an Orbit endpoint is in the staged changes, commit refused" >&2
	exit 1
fi
exit 0
```

## Tools

| Use | Tools |
| --- | --- |
| Read | `orbit_queue_list`, `orbit_get_completions` (its `nextNumber` is the next prompt number; the notes are cut at about 110 characters; pass `number` to read one entry, because the list head drops entries to fit the transport limit), `orbit_get_completion_full`, `orbit_get_review_detail` (Apollo review: raw verdict and parsed JSON), `orbit_get_semantic_diffs`, `orbit_findings_search`, `orbit_get_project_settings`, `orbit_list_locks` |
| Queue and control | `orbit_queue_prompt`, `orbit_edit_prompt` (pending rows only), `orbit_cancel_prompt`, `orbit_lock_file` |
| Not used | Everything else. `orbit_call` reaches all 273 tools, including Orbit's deploy, payment, database, security and vault tools, which have nothing to do with this project: do not call one without the owner's explicit yes. The Bridge (`relay_*`) has no channel on this account. |

Since 2026-10-11 the MCP server exposes the queue, completion, settings and lock tools only through `orbit_call` (`orbit_list_tools` finds them). The owner allowed calling the tools of this table that way; the rest of the rule is unchanged.

## Prompt format

```text
[SuiteWard] Prompt #<nextNumber> — <outcome in one line>
complexity: low|medium|high|ludicrous
depends_on: #<n>

## Context
## Task
## Verification
## FAILURE CONDITIONS
```

`depends_on` is optional and must be a standalone line in the first 10 lines with only the number (prose is rejected). The number is Orbit's and authoritative: read `nextNumber` first (Orbit skips a number already taken and says so). A [task brief](task-brief.md) maps onto the prompt: Goal, Consumes and Out of scope go to Context (with a pointer to `AGENTS.md` and the contract documents the task reads), Owns, Acceptance and Notes to Task, Verification to Verification. The branch and worktree lines of the brief do not apply: Orbit isolates the run itself.

**Orbit wraps the prompt.** The agent is told to read the prompt from a file in its worktree, and Orbit's own steps surround ours: a commit whose subject is tagged `[Project] Prompt #N — <short description>` (validated by its `commit-msg` hook) and an integration of the commit into `main` of the clone. The run ends with a `<completion_report>` (STATUS, IMPLEMENTATION, MISSING_SCOPE, VERIFICATION_STATUS, SUMMARY, FILES, COMMIT, NOTES, VERIFICATION, TOKENS) that Orbit stores as the completion.

- **Commit subject:** `[SuiteWard] Prompt #N — <type>: <what>`, with N the number Orbit assigned, in English, one commit per test-first step (`test:` first, then `feat:` or `fix:`), and the `Co-Authored-By` trailer of the model.
- **Standard clauses in Context**, in every prompt: `AGENTS.md` is the source of the project rules (Orbit's `context/rules.md` is not in the worktree); Memtrace is the first choice when it answers, through `repo_id: SuiteWard-orbit`, with its paths mapped to the run worktree and never read or written in the clone's main checkout or in `D:\Repos\SuiteWard`; otherwise gopls, Grep and Read (see [Memtrace in runs](#memtrace-in-runs)); do not perform the integration step, the commits stay on the run branch (Orbit integrates by itself afterwards, see the lifecycle); run `./scripts/dev.ps1 check`, never `persistence` or `setup` (the orchestrator runs `persistence`, a database per run worktree would leak resources); the completion report is short (at most 15 lines) and the commits are the artifact.
- **FAILURE CONDITIONS**, in every prompt:
  - Touches a file the task does not own, or a locked file.
  - Pushes, opens or edits a pull request, merges, or writes to GitHub in any way.
  - Puts a secret, a token or personal data in a file, a log or a message.
  - Changes behavior without its failing test committed first (the [TDD rule](../tdd.md)), or weakens a test to make it pass.
  - Hides an impossible state (a zero value, `return nil, nil`, a swallowed error) or adds a nil check away from a trust boundary (the [nil-check rule](../development-guide.md#nil-checks)).
  - Uses `--no-verify`, overrides `core.hooksPath`, or changes or unsets environment variables to get a hook through: it reports the failure instead.
  - Reports success without the exact commands of Verification, their results and the revision they ran on.

Queue parameters: `project`, `content`, `complexity`, `expected_outcome` (`commit`; for documents-only work `file_write` with `outcome_paths`). Files that must have one owner per wave (`go.mod`, `go.sum`, migrations, generators, CI, composition) get an `orbit_lock_file` lock with a reason, tied to the prompt that owns them through `queueRowId` so Orbit releases it when that prompt ends.

| Complexity | Use for |
| --- | --- |
| `low` | Documentation and mechanical changes without logic. Observed: runs on Haiku 5.5 at maximum effort. |
| `medium` | An ordinary task inside one package. Observed: runs on `claude-sonnet-5`. |
| `high` | Contracts, migrations, security-relevant or cross-package work. Observed: runs on `claude-opus-5-5`. |
| `ludicrous` | Rare: large or novel work where a wrong answer is costly. State the goal and the definition of done, not the method. |

`local` (the operator's local model) is not used.

A prompt that depends on code another prompt adds starts with a precondition step: confirm the code it consumes exists (not a stub) and its tests pass at HEAD, otherwise stop and report without changes. A dependent run starts from the clone's `main` as Orbit integrated the previous run, which is not yet reviewed; the orchestrator reviews the runs in order and fixes on top.

## Approval

For `medium`, `high` and `ludicrous` the orchestrator shows the complete prompt in chat and queues it only after the owner's explicit yes, which is Orbit's own convention for those tiers; a `low` prompt may be queued directly. In every case the owner's approval in the Orbit dashboard is the gate before a run starts. Orbit may also ask for its own confirmations (a shared-worktree or coordinator confirmation, a prompt left in `needs_input`): those are the owner's decisions, so the orchestrator shows the question and does not answer it through `orbit_queue_prompt` (`confirm_id`). A queued prompt is never a message to other people, but it carries only what the task needs: no secret, no token, nothing from `.local/`.

## Lifecycle

Before queueing:

1. **Orbit is healthy.** The agent runtime is logged in, `orbit_get_project_settings` answers, no run is `running` or stuck, and the Node check of the known issue passes.
2. **Realign the clone.** Orbit launches a run from the clone's `main` (recorded as `baselineCommit`) and integrates a shipped commit into that `main` by itself, even when the agent skips its integration step, so `main` there drifts ahead with work nobody reviewed. Make it equal to the reviewed base: `git -C <clone> fetch origin`, then `git -C <clone> reset --hard <base>`, with `<base>` the reviewed state (`origin/main`, or a phase branch fetched from the orchestrator's checkout). Then confirm the safeguards still hold (push URL, `credential.helper`, identity, `.mcp.json`).

Then:

1. Write the brief, turn it into a prompt, show it when the tier requires it, queue it.
2. The owner approves it in the dashboard; Orbit runs it in an isolated worktree of the clone.
3. Before the run starts and once it is running, compare the row's `contentHash` and `contentRevision` in `orbit_queue_list` with what was queued: the content can change after queueing (an accepted Apollo patch, a dashboard edit, or an Orbit rewrite nobody announced, see [M1.3 results](#m13-results)), and an `orbit_edit_prompt` is possible only while the row is pending. Then the orchestrator reads `orbit_queue_list` and `orbit_get_completions` (`finalState` `SHIPPED` means a commit exists). It checks when asked or at a natural pause, never in a polling loop. When the owner is away and has agreed, the orchestrator may wait on the clone's local refs instead (a low-frequency check of `run/q<id>`, the clone's `main` and its worktree list, with no call to Orbit). `failed` with no notes means the run never started properly (the login); `failed` with a report and no commit is usually a precondition the prompt defines, and Orbit may retry it automatically as `<N>a` with the same result: report it, do not let it retry silently; `failed` with no notes means the run never started properly (the login); `partial` with `WORK_NOT_COMMITTED` means the work is done but the commit was blocked, and it sits staged in the run worktree.
4. Fetch the commits into the orchestrator's checkout (`git remote add orbit <clone path>` once, fetch-only, then `git fetch orbit`). A run is the branch `orbit/run/q<queue id>`; the queue id is not the prompt number, and a retry takes a letter suffix. An `[Orbit] ... preserve failed-run work` commit by "Orbit Recovery" on a failed run's branch is Orbit's own: never merge it.
5. Check the commits first: the author is the bot, the subject carries the tag, only owned files changed, nothing secret is in the diff, and the test-before-implementation order holds. Then verify the exact revision: `./scripts/dev.ps1 check`, and `persistence` when the task touches the PostgreSQL adapter or migrations. Then `sw-reviewer` with the same brief. The agent may use a subagent for a pre-review; the independent review stays ours.
6. Fix rounds are new prompts that `depends_on` the previous one (or `orbit_edit_prompt` before it starts), at most two rounds as for any task.
7. Merge `--no-ff` the run branch into the phase branch in the orchestrator's checkout, so the test-first commits stay in history; phase checks, publication and the merge into `main` are unchanged. Realign the clone to the new base before the next task.

## Pilot results

One `low`, documents-only prompt, five attempts on 2026-10-10 and 2026-10-11. The first two failed without notes (the agent runtime was not logged in). The third reached the end and wrote its report but could not commit, and the fourth was stopped, both because of the Node failure of the known issue. After the host fix the fifth run shipped and was verified: one commit, one file, the tagged subject, the model's trailer, nothing secret, and the report delivered. What the runs showed:

- **Where commits land.** On `run/q<queue id>` in `<clone>\.worktrees\run-q<id>`, on top of the clone's `main` at launch. Orbit then fast-forwards the clone's `main` through its own integration worktree.
- **Subject tag.** Required and validated by Orbit; the format above is accepted.
- **The agent.** Claude Code with permissions bypassed, Haiku 5.5 at maximum effort for `low`, about 8 minutes for the pilot. It read `AGENTS.md` and this page, our `.claude/settings.json` applied (the advisor, Opus 5.5, was consulted; the `PostToolUse` hooks ran), and it refused to bypass a failing hook. A subagent is available inside a run: the `Agent` tool exists and `sw-reviewer` was used on the agent's own diff (the agent's report, not independently verified).
- **Go toolchain.** `./scripts/dev.ps1 check` passes in a run worktree in about 70 seconds from the shared tool cache, with no download; no database is provisioned.
- **Identity.** The first pilot commit carried the owner's global git identity; the clone-local identity of the safeguards fixes that.
- **Orbit findings.** Agents store findings in Orbit's project wiki, and later runs read them. Some were wrong or stale (about the hooks and about the run-owner marker): treat them as leads.
- **Not exercised in the pilot.** File locks, the models of the higher tiers, and a Go task with two test-first commits; the first real tasks below covered them.

## Memtrace in runs

The clone has its own Memtrace store: the owner runs `memtrace start --headless --no-workspace` in `D:\Repos\SuiteWard-orbit`, which indexes and watches the clone as `repo_id` `SuiteWard-orbit` (the folder name). A distinct id means that a session attached to the wrong store fails with `repo_not_in_store` instead of returning paths of the orchestrator's checkout. Set the Orbit project's `memtraceRepoId` to the same id for `orbit_code_search`. The clone's `main` is realigned to the reviewed base before each task, so the index matches the base of the next run.

Observed in M1.3: the agents of the runs had no `mcp__memtrace__*` tools and no `memtrace-first` skill (Orbit's agent configuration does not load them), so they used `orbit_code_search`, which returned nothing, and fell back to Grep. They still reported "Memtrace answered". Until the Orbit agent configuration loads the Memtrace MCP server, treat a run's Memtrace claim as unverified and expect Grep.

## M1.3 results

The first real tasks, 2026-10-11, M1.3-A1 (#6, `medium`), M1.3-A2 (#7, `high`) and M1.3-B (#9, `high`), each shipped two commits in test-first order with the bot as author, the tagged subjects and the model's trailer; RED was behavioral at every test commit, and `check` passed at every head. A1 and A2 needed review fixes, done by `sw-implementer` on the phase branch; B had only non-blocking findings.

- **Models.** `medium` ran on `claude-sonnet-5`, `high` on `claude-opus-5-5`.
- **Locks.** `go.mod` and `go.sum` were locked for #6 with `queueRowId` and released when the row ended (`orbit_list_locks` showed none afterwards). The lock reason did not appear in the run's transcript, so the preamble injection was not observed, and no run tried to touch a locked file, so the post-execution diff check was not exercised. A lock blocks every queued prompt from the moment it exists, whatever `queueRowId` says: never lock a file that a later prompt in the same chain owns.
- **Dependent chains.** `depends_on` worked: each run started after the previous one shipped, from the clone's `main` as Orbit integrated it.
- **Content changed after queueing.** Prompt #8 ran with text that differed from what was queued (`contentRevision` 1, no Apollo review on the row): an added precondition the code could not meet, so the agent stopped without changes. Orbit then retried it automatically as #8a with the same result. Prompt #9 replaced it after the missing domain function was added; its hash was unchanged when it ran. A report for the vendor proposes that Orbit notify the agent that queued a prompt through MCP of any later change to it.
- **Apollo reviews.** `READY` reviews came with optional patches; accepting one goes through `orbit_edit_prompt` and clears the review. `high` rows were queued straight to `pending_approval` without a review.
