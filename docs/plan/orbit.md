# Orbit executor

Orbit is a prompt queue that runs coding agents in isolated worktrees and exposes itself through an MCP server. The orchestrator can send it tasks instead of dispatching `sw-implementer`: the orchestrator plans, writes and queues the prompts and reads what comes back; Orbit executes. Nothing Orbit returns is authority. Its completion notes and review verdicts are evidence, like Memtrace results: the orchestrator verifies the exact revision, the independent `sw-reviewer` reviews it, the advisor is consulted at the usual checkpoints, and merges, GitHub writes and publication stay as in [orchestration](orchestration.md).

The first version of this page rests on a read-only exploration of the server (Orbit 2.163.64, 25 direct tools, 273 in total). No prompt has run yet; the [pilot](#pilot) records what the first run teaches.

## Setup (owner, once)

1. **A dedicated clone.** The Orbit project's `path` is a separate clone of this repository, `D:\Repos\SuiteWard-orbit`, created from `main` with the [safeguards](#clone-safeguards) below. Not the orchestrator's checkout (Orbit would write into the working tree and branch the orchestrator is using) and not a subfolder of it (agents work only inside the registered path, so a subfolder hides the product code). `path` is a protected setting: register the project with the clone's path.
2. **Project settings.** `gitPublish.mode` is `commit-only` (Orbit never pushes), `dispatchWithoutHumanApproval` is `false` (the owner approves every prompt in the dashboard before it runs), worktree isolation is required, and the runtime provides PowerShell 7 and the pinned tools (`./scripts/dev.ps1 tools`; every checkout has its own caches, database and ports).
3. **MCP connection, local scope.** The URL and the token never enter a committed file of this repository. Once `.mcp.json` is untracked in the clone (see the safeguards), Orbit writes its entry there. In the owner's terminal, **from the orchestrator's checkout** `D:\Repos\SuiteWard` (the folder decides the scope), with the clone's file as the source (nothing is printed):

   ```powershell
   cd D:\Repos\SuiteWard
   $o = (Get-Content -Raw D:\Repos\SuiteWard-orbit\.mcp.json | ConvertFrom-Json).mcpServers.orbit
   claude mcp add --transport http --scope local orbit $o.url --header "Authorization: $($o.headers.Authorization)" --header "X-Orbit-Project: suiteward"
   ```

   The tools appear as `orbit_*` in the next session opened in that folder. Local scope binds the entry, and its `X-Orbit-Project` header, to that folder only: with user scope every Claude Code session on the machine would inherit the binding to this project, including sessions for other Orbit projects. `X-Orbit-Project` must be the slug of the project registered in step 1. `--header` takes several values, so the name and the URL come first.

## Clone safeguards

Orbit scaffolds its own files into the project folder, writes its MCP entry (with the token) into `.mcp.json` and may rewrite `.claude/settings.json`. Both are tracked files of this repository, and the repository is public. These settings of the clone (none is versioned) keep that out of commits and keep the clone from reaching GitHub:

```powershell
$d = 'D:\Repos\SuiteWard-orbit'
git clone https://github.com/IgnisDevNE/SuiteWard.git $d
git -C $d remote set-url --push origin DISABLED-the-orbit-clone-never-pushes
git -C $d update-index --skip-worktree .claude/settings.json
git -C $d rm --cached .mcp.json
```

- A push from the clone fails and a fetch works. The orchestrator reads the clone by fetching from it, never by working in it.
- Orbit refuses to write its credential into a tracked file ("`.mcp.json` is already tracked"), and here `.mcp.json` is tracked (it carries the `gopls` server). `git rm --cached` untracks it **in the clone's index only**: the file stays on disk, nothing is committed, the repository and `main` are unchanged, and the pre-commit guard below refuses any commit that stages it, so this deletion can never be committed from the clone. A `git reset --hard origin/main` in the clone tracks it again and drops Orbit's entry: repeat the command.
- `.claude/settings.json` may be rewritten by Orbit without dirtying the clone or entering `git add -A`. If a merge into the clone complains about it, clear the flag (`--no-skip-worktree`), update, and set it again.
- Orbit also appends a managed block to the tracked `.gitignore`, which shows as a modified file in the clone.
- `.git/info/exclude` lists the scaffold Orbit creates, which is untracked in this repository: `/ORBIT.md`, `/CLAUDE.md`, `/GEMINI.md`, `/COMPLETION_LOG.md`, `/COMPLETION_LOG.archive.md`, `/context/`, `/.orbit/`, `/.sweep/`, `/.codex/`.
- `.git/hooks/pre-commit` refuses a commit that stages a managed file or adds a bearer token or an Orbit endpoint:

```sh
#!/bin/sh
managed='^(\.mcp\.json|\.claude/settings\.json|\.codex/|\.orbit/|\.sweep/|context/|ORBIT\.md|CLAUDE\.md|GEMINI\.md|COMPLETION_LOG)'
staged=$(git diff --cached --name-only | grep -E "$managed")
if [ -n "$staged" ]; then
	echo "pre-commit: Orbit-managed files are staged, commit refused:" >&2
	echo "$staged" >&2
	exit 1
fi
if git diff --cached -U0 | grep '^+' | grep -Eq 'Bearer [A-Za-z0-9._~+/=-]{20,}|[a-z0-9]{12,}\.orbit\.sivants\.com'; then
	echo "pre-commit: a credential or an Orbit endpoint is in the staged changes, commit refused" >&2
	exit 1
fi
exit 0
```

## Tools

| Use | Tools |
| --- | --- |
| Read | `orbit_queue_list`, `orbit_get_completions` (its `nextNumber` is the next prompt number), `orbit_get_completion_full`, `orbit_get_review_detail` (Apollo review: raw verdict and parsed JSON), `orbit_findings_search` |
| Queue and control | `orbit_queue_prompt`, `orbit_edit_prompt` (pending rows only), `orbit_cancel_prompt`, `orbit_lock_file` |
| Not used | Everything else. `orbit_call` reaches all 273 tools, including Orbit's deploy, payment, database, security and vault tools, which have nothing to do with this project: do not call one without the owner's explicit yes. |

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

`depends_on` is optional and must be a standalone line in the first 10 lines with only the number (prose is rejected). The number is Orbit's and authoritative: read `nextNumber` first. A [task brief](task-brief.md) maps onto it: Goal, Consumes and Out of scope go to Context (with a pointer to `AGENTS.md` and the contract documents the task reads), Owns, Acceptance and Notes to Task, Verification to Verification. The branch and worktree lines of the brief do not apply: Orbit isolates the run itself.

Every prompt ends its FAILURE CONDITIONS with these clauses:

- Touches a file the task does not own, or a locked file.
- Pushes, opens or edits a pull request, merges, or writes to GitHub in any way.
- Puts a secret, a token or personal data in a file, a log or a message.
- Changes behavior without its failing test committed first (the [TDD rule](../tdd.md)), or weakens a test to make it pass.
- Hides an impossible state (a zero value, `return nil, nil`, a swallowed error) or adds a nil check away from a trust boundary (the [nil-check rule](../development-guide.md#nil-checks)).
- Reports success without the exact commands of Verification, their results and the revision they ran on.

Queue parameters: `project`, `content`, `complexity`, `expected_outcome` (`commit`; for documents-only work `file_write` with `outcome_paths`). Files that must have one owner per wave (`go.mod`, `go.sum`, migrations, generators, CI, composition) get an `orbit_lock_file` lock with a reason, tied to the prompt that owns them through `queueRowId` so Orbit releases it when that prompt ends.

| Complexity | Use for |
| --- | --- |
| `low` | Documentation and mechanical changes without logic. |
| `medium` | An ordinary task inside one package. |
| `high` | Contracts, migrations, security-relevant or cross-package work. |
| `ludicrous` | Rare: large or novel work where a wrong answer is costly. State the goal and the definition of done, not the method. |

`local` (the operator's local model) is not used.

## Approval

For `medium`, `high` and `ludicrous` the orchestrator shows the complete prompt in chat and queues it only after the owner's explicit yes, which is Orbit's own convention for those tiers; a `low` prompt may be queued directly. In every case the owner's approval in the Orbit dashboard is the gate before a run starts. A queued prompt is never a message to other people, but it carries only what the task needs: no secret, no token, nothing from `.local/`.

## Lifecycle

1. Write the brief, turn it into a prompt, show it when the tier requires it, queue it.
2. The owner approves it in the dashboard; Orbit runs it in an isolated worktree of the clone.
3. The orchestrator reads `orbit_queue_list`, then `orbit_get_completions` or `orbit_get_completion_full` and `orbit_get_review_detail`. It checks when asked or at a natural pause, never in a polling loop.
4. Fetch the commits into the orchestrator's checkout (`git remote add orbit <clone path>` once, then `git fetch orbit`) and read them there.
5. Verify the exact revision: `./scripts/dev.ps1 check`, `persistence` when the task touches the PostgreSQL adapter or migrations, the test-before-implementation commit order, and that only owned files changed. Then `sw-reviewer` with the same brief.
6. Fix rounds are new prompts that `depends_on` the previous one (or `orbit_edit_prompt` before it starts), at most two rounds as for any task.
7. Merge `--no-ff` into the phase branch in the orchestrator's checkout, so the test-first commits stay in history; phase checks, publication and the merge into `main` are unchanged.

## Pilot

Before real tasks, one `low`, documents-only prompt, to learn and write down here:

- where Orbit leaves its commits (branch name) and how they are fetched;
- whether commit subjects must carry the prompt tag (Orbit's completion registration requires a subject tag that agrees with the prompt label) and how that fits the [development guide](../development-guide.md);
- whether the agent runtime honours `AGENTS.md` and the hooks of `.claude/settings.json`;
- how the Go toolchain and the per-checkout database are provisioned;
- how `commit-only` and a locked-file violation behave.
