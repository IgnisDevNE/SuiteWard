# Task brief template

The orchestrator writes one brief per task and sends it to the `sw-implementer` subagent. The same brief is given to `sw-reviewer`. Keep it short: a brief that needs more than a page usually describes more than one task.

```markdown
# <Phase>-<Task>: <title>

**Worktree:** D:/Repos/SuiteWard-worktrees/<phase>-<task> (branch `task/<Phase>-<Task>`, based on `<base sha>`)

## Goal
One paragraph: the outcome and why it matters.

## Owns
Files and directories this task may create, change, or delete.

## Consumes
Interfaces, documents, and decisions this task relies on (with paths). Do not change them; report if they are wrong.

## Acceptance
- Behavior scenarios that must hold, each expressed as a test that must exist and pass.
- Removals or documentation outcomes, stated so a reviewer can check them.

## Out of scope
What not to touch or decide.

## Verification
Exact commands to run (for example `./scripts/dev.ps1 check`, `./scripts/dev.ps1 persistence`).
Nil-check rule: the implementer lists every non-error nil comparison it adds or keeps with a boundary-or-not verdict, and the reviewer re-reads the touched Go code against the [nil-check rule](../development-guide.md#nil-checks) and says in its report that it did. `golangci-lint` in `check` covers only the root module, so code in another module (such as a spike) is linted by hand with the root `.golangci.yml`.

## Notes
Known pitfalls, related files, and the order of commits when it matters.
```

The implementer's final report follows the format in `.claude/agents/sw-implementer.md`. The reviewer reports with the format in `.claude/agents/sw-reviewer.md`.
