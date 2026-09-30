# Test-driven development

The user accepted [ADR 0025](decisions/0025-test-driven-development.md) on 2026-09-30. Every implementation task follows this rule, including domain/application code, adapters, infrastructure scripts, generators, migrations, and configuration that changes behavior. It governs SuiteWard development, not the development process of customer repositories.

## Task applicability

Every task in [the backlog](plan/backlog.json) declares `tdd.mode` and `tdd.reason`:

| Mode | Meaning |
| --- | --- |
| `required` | Implementation or behavioral change. Record observed RED/GREEN cycles and independent review. |
| `not_applicable` | Documentation, discovery, or decision work with no executable behavior changes. State the concrete reason; the reviewer checks its scope. |
| `historical` | Completed F0 work from before adoption only. Preserve its real verification without asserting retrospective TDD. New work cannot use this mode. |

A behavior-changing task cannot waive TDD because it is small, difficult to test, configuration-only, or already written. Applicability follows the actual change, not the task's `kind` label: a documentation-only task may be non-applicable even when grouped as implementation work in the plan. Update a discovery task's scope and mode before it begins implementing behavior. Documentation accompanying behavioral implementation belongs to that task's evidence; it does not exempt the implementation.

## The worker cycle

1. **Choose an observable scenario.** Use the task's accepted contract and invariant. Include relevant rejection, boundary, security, and operational failure cases. A bug fix begins with a test that reproduces the bug. Assert behavior rather than mirroring private implementation steps.
2. **RED.** Write the test before implementing its behavior. Run it and observe the expected behavioral failure. Save a Git checkpoint and record the exact command, nonzero exit code, and meaningful diagnostic. Installation errors, missing dependencies, syntax errors, or failure to compile do not qualify. Minimal temporary API declarations can make the test executable, but the behavior must still be absent and the assertion must fail for that reason.
3. **GREEN.** Implement the behavior, rerun the same command, and record the passing checkpoint, zero exit code, and result. Run the relevant regression checks as well. Do not weaken or remove a valid assertion merely to obtain a passing run; review an incorrect test against the contract and record any correction.
4. **REFACTOR, when useful.** Improve the implementation while retaining behavior, then rerun the applicable tests. State when no refactoring was needed. The final GREEN checkpoint must contain the final task behavioral files; rerun and record a final passing checkpoint after any later changes.
5. **Review.** A reviewer other than the author examines the real failure and success evidence, test meaning, relevant negative cases, final files, and regression results. Resolve material findings before marking the task complete.

Use small cycles instead of writing every test for a milestone up front. Workers perform their cycles in their own task worktrees; no central testing lane is needed. Record the full Git revision for each checkpoint. Commit recorded evidence after the observed run so the record does not need to contain its own commit hash.

A standalone refactor with no intended behavior change needs a separately reviewed characterization/regression plan before dispatch. Do not manufacture a failing behavior, mutate production code merely to create RED, or label executable changes as documentation. The optional refactor step of an existing cycle is already covered above; this rule does not create a blanket exemption for unrelated code refactoring.

Preserve referenced task commits when integrating. Merge or otherwise preserve their ancestry; do not squash away RED/GREEN checkpoints referenced by the record. The final phase PR must pass all checks. A historical failing test checkpoint is evidence of the cycle, not an acceptable failing final revision.

On GitHub choose **Create a merge commit**. A green PR check evaluates the PR history; it cannot guarantee which merge method will later be chosen. Inspect the resulting main revision as well.

PR #4 was squash-integrated as `c0f173deb6d2bd7ef577b6e8ce5d6acbee391a82`: its files match the reviewed phase, but that commit omitted the original checkpoint ancestry and the main evidence gate correctly failed. F0.02 restores the actual original history through a merge, leaving the implementation, validator and F0.01 evidence unchanged. The recovery must preserve those commits when merged; its documentation-only evidence check cannot detect a second squash by itself. Product work waits for the repaired main check and explicit ancestry verification.

## Execution record

Commit one record at `docs/plan/executions/<phase>.json`. It contains `schema_version: 1`, the exact phase ID, and a `tasks` entry for every task in the phase. Planned work stays in the backlog; the record states what actually happened. Use the [task evidence template](plan/task-evidence-template.md) for supplementary execution notes.

Each task entry contains:

| Field | Requirement |
| --- | --- |
| `task_id` | Exact task ID from the phase. |
| `mode` | `required` or `not_applicable`, matching the task's plan declaration. Historical F0 is not a new execution mode. |
| `files` | Exact repository-relative changed paths assigned to the task, including its tests, supporting documentation, and removed files. Account for every changed file across the phase. |
| `cycles` | For `required`, one or more scenarios with `red`, `green`, and `refactor` notes. |
| `reason` | For `not_applicable`, a concrete explanation of why no executable behavior changed. |
| `review` | `author`, different `reviewer`, `outcome: "accepted"`, and substantive `notes`. These are review declarations, not machine-authenticated identities. |

A cycle has a `scenario` and two observations. Each observation has a full 40-character Git `revision`, `command`, numeric `exit_code`, and diagnostic `output`. RED has a nonzero exit code; GREEN has zero. Both use the same command and are ancestors of the checked head, with RED preceding GREEN. Keep output excerpts sufficient to assess the claimed result and exclude secrets.

The following fragment illustrates a required task's cycle. Placeholder revisions are explanatory and must be replaced by actual observed checkpoints in a real record:

```json
{
  "scenario": "A stale approval cannot authorize a changed proposal",
  "red": {
    "revision": "<full RED commit>",
    "command": "./scripts/dev.ps1 go test ./internal/domain/contract -run TestRejectStaleApproval",
    "exit_code": 1,
    "output": "TestRejectStaleApproval: expected stale approval rejection; promotion was accepted"
  },
  "green": {
    "revision": "<full GREEN commit>",
    "command": "./scripts/dev.ps1 go test ./internal/domain/contract -run TestRejectStaleApproval",
    "exit_code": 0,
    "output": "PASS"
  },
  "refactor": "No refactoring needed; the applicable regression suite also passed."
}
```

Non-applicable records list their files and reason with an accepted independent review. They do not invent cycles. Automated non-applicability is limited to Markdown (`.md` or `.markdown`), the plan backlog, and phase execution JSON; executable scripts or behavioral configuration require implementation evidence. Scope broader than these allowed documentation paths must be classified before dispatch rather than silently exempted.

The final GREEN for each implementation task must cover its final behavioral files. If integration changes those files after the recorded GREEN, rerun the applicable tests and update the evidence. Add a new RED/GREEN cycle before implementing any newly identified behavior. The integrator owns resolving shared-file assignments and complete phase accounting; workers must not edit each other's evidence concurrently.

## Integration checks and their limits

Before integration, run:

```powershell
./scripts/dev.ps1 check
./scripts/check-tdd.ps1 -BaseRevision <base> -HeadRevision HEAD
```

Use the phase's actual comparison base and complete Git history. The evidence check validates task modes, record fields, changed-file accounting, distinct declared author/reviewer, checkpoint ancestry, RED/GREEN ordering, matching commands/results, and final task-file binding. F0.01 adds the same check for PRs and `main` pushes as a prerequisite of `CI / Gate`; operational activation requires that delivery's hosted checks and authorized integration.

The validator never executes a command supplied by an execution record. It cannot prove that a command actually ran before code was written, that an output excerpt is authentic, that different names identify independent people/agents, or that assertions protect the intended invariant. Reviewers must inspect and, where needed, reproduce the claimed behavior. A schema-valid claim is insufficient review evidence.

Normal CI verifies the final revision on the required platforms and with applicable real adapters. Coverage remains a separate check. Record remaining limits honestly; do not manufacture missing runs or label behavior-changing files as documentation. The user authorizes merging into `main`; evidence and a green gate do not grant that authority.
