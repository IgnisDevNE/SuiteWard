# Task execution record

Copy this template into the phase's execution notes when a task is assigned. The backlog describes planned work; this record describes observed completion. Commit machine-readable evidence to `docs/plan/executions/<phase>.json` using [the TDD record format](../tdd.md#execution-record). Prose notes supplement that record.

- Task / phase:
- Assignee / reviewer / integrator:
- Branch / worktree:
- Contract checkpoint revision:
- Start dependencies and evidence:
- Decision gates and accepted decision links:
- Owned files / temporarily delegated shared files:
- TDD mode / concrete non-applicability reason:
- Resulting behavior:
- Acceptance and negative scenarios verified:
- RED checkpoint(s): scenario, full revision, command, nonzero exit code, and behavioral failure diagnostic:
- GREEN checkpoint(s): corresponding full revision, same command, zero exit code, and result:
- Refactoring performed / no refactoring needed / tests rerun:
- Commands and outcomes:
- Exact tested commit:
- Real adapters exercised / remaining limits:
- Integration dependencies and combined verification:
- Review findings and resolution:
- Independent review: author, different reviewer, accepted outcome, and verification of RED/GREEN meaning, scope, and final file binding:
- Phase PR / merged revision, when available:

Use actual observations. Tool installation errors, missing dependencies, syntax errors, and failure to compile are not RED evidence. A bug fix must include its reproducer. Record relevant negative and security/invariant scenarios, not only a successful path. If no refactoring is needed, state that directly.

For documentation, discovery, or decision-only tasks, explain why no executable behavior changes and list the exact changed files. Do not invent a RED/GREEN cycle. Existing F0 work predates adoption and has historical status in the plan; new changes cannot reuse that exception.

Do not include tokens, connection strings, private recovery keys, or customer protected test content. Keep diagnostic excerpts sufficient to assess the failure without copying sensitive data. An empty item is not evidence that a requirement passed. CI checks record consistency and Git bindings; the reviewer must establish that the observations and tests support the claimed behavior.
