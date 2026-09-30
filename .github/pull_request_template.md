## Result

<!-- Concrete trigger and resulting behavior. One phase, one PR. -->

Phase: <!-- F0, M0.01, ...; link docs/plan/phases/<ID>.md -->

Merge method: **Create a merge commit**. Preserve the RED/GREEN checkpoint commits; do not squash or rebase this phase.
Tasks completed: <!-- Stable task IDs; describe partial/excluded work explicitly. -->

## Review and evidence

- Contracts/ADRs changed:
- Start and integration dependencies satisfied:
- Acceptance scenarios and negative cases:
- TDD record: <!-- Link docs/plan/executions/<phase>.json; every task and changed file accounted for. -->
- RED/GREEN revisions and independent review: <!-- Behavioral failures observed before implementation, matching passing runs, optional refactor checks. -->
- Non-applicable tasks and reasons: <!-- Documentation/discovery/decision only; never an implementation waiver. -->
- Commands, outcomes, and exact tested revision:
- Windows/Linux and real adapter evidence, where applicable:
- Migration, recovery, and compatibility impact:
- Remaining limitations or decisions:

<!-- Preserve recorded RED/GREEN commits; the final revision must pass all checks. CI validates evidence consistency, not the truth or quality of a claimed TDD sequence. Independent review is required. Publish using the authorized bot. Merge requires the user's authorization. -->
