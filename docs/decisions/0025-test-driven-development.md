# ADR 0025: Mandatory task-level test-driven development

- **Date:** 2026-09-30
- **Status:** Accepted by the user; amended 2026-10-05 (phase R1), which replaces the evidence and enforcement machinery described below.
- **Product:** SuiteWard
- **Scope:** Development of this repository, including application behavior, infrastructure scripts, and behavioral configuration.
- **Related:** [ADR 0023](0023-development-environment-and-project-local-tooling.md), [ADR 0024](0024-single-module-project-structure.md), and [development conventions](../development-guide.md).

## Context

The delivery plan required tests and a passing final revision, but neither established that contributors wrote and observed failing behavior tests before implementation. Multiple agents can satisfy a coverage target while missing the failure or authority boundary a task is intended to protect. This development rule concerns SuiteWard's own engineering process; it does not add a TDD requirement to customer repositories.

## Decision

Every implementation task follows RED -> GREEN -> REFACTOR independently in its task worktree:

1. Define the observable scenario, including relevant rejection, security, failure, and invariant cases. Write its test before implementing the behavior and observe a meaningful failure.
2. Preserve a RED Git checkpoint, command, nonzero exit code, and diagnostic that demonstrates the missing behavior. Missing tooling, dependencies, syntax, or compilation is not a behavioral failure. Minimal declarations may make a test compile, but must not implement the behavior being demonstrated.
3. Implement the smallest coherent behavior that satisfies the test. Preserve a GREEN checkpoint and the same command passing, then run the applicable regression checks.
4. Refactor when useful and rerun the affected tests. Refactoring is optional; a fabricated refactoring step or failure is not required.
5. Obtain review from someone other than the task author. The reviewer checks the observed failure, test meaning, matching success, revision/file bindings, and applicable final verification.

Bug fixes start with a reproducer. Infrastructure scripts and configuration that changes behavior follow the same rule. Tasks exclusively changing documentation, discovery results, or decisions may declare `not_applicable` with a concrete reason and independent review. A task that starts as discovery must change its plan and evidence mode before introducing implementation.

The completed F0 baseline predates this decision. Preserve its existing verification as historical evidence without claiming or inventing retrospective TDD. That historical designation is closed to new work, including later changes to F0 files.

## Evidence and enforcement

The [TDD rule](../tdd.md) defines the plan declarations, execution-record format, and local check. Each phase records its tasks and exact changed-file ownership. Referenced RED and GREEN checkpoints remain reachable in the integration history; the final PR revision passes all applicable checks. This preserves one integration PR per phase and independent parallel worker cycles.

CI validates evidence structure, task applicability, changed-file coverage, Git ancestry, and whether the final recorded GREEN covers the task's final behavioral files. It never executes arbitrary commands from evidence. The evidence gate feeds the existing required `CI / Gate`.

Machine validation cannot establish command execution chronology, diagnostic authenticity, independent reviewer identity, or semantic test quality from declared JSON. Independent review must inspect the actual evidence and relevant behavior. The review record and CI result are evidence, not permission to merge; the user still authorizes each integration into `main`.

## Consequences

- Workers remain free to run concurrently after their reviewed contracts are ready. Tests and evidence belong to their tasks, not to a later testing phase.
- Reviewable RED checkpoints increase history and evidence work. Preserve them through integration; squashing away a referenced checkpoint requires rebuilding honest evidence, not rewriting revision strings.
- Missing or stale evidence blocks integration even when the final suite is green. A passing final test is necessary but does not repair a missing earlier observation.
- Non-implementation work does not manufacture tests to satisfy a form. Its concrete reason and changed-file scope remain reviewable.
- This rule is prospective. The first enforcement delivery must itself use TDD for new validator and CI behavior, with documentation tasks explicitly non-applicable.

## Alternatives considered

- Require only coverage and green CI: proves final results but not a test-first development process.
- Require TDD only for application Go code: leaves behavioral infrastructure and enforcement changes outside the rule.
- Serialize all testing behind one agent: adds unnecessary blocking and separates behavior ownership from its tests.
- Treat a RED/GREEN JSON claim as proof of TDD: overstates what a validator can know and allows meaningless or fabricated evidence to appear authoritative.

## Amendment (2026-10-05, phase R1): test-first without evidence machinery

This amendment supersedes the evidence and enforcement parts of this record (the RED/GREEN checkpoint declarations, the per-task execution records, and CI validation of evidence and Git ancestry). The test-first discipline itself stays.

- **Required:** test-first is required for domain and application rules about authority, consent, promotion, idempotency and concurrency, and for adapter behavior. Write the failing behavior test, observe it fail for the right reason, commit it, then implement.
- **Review:** the PR shows the test commit before the implementation commit, and the `sw-reviewer` subagent checks that order and that the test fails for the right reason.
- **Exempt:** documentation, deletions, mechanical refactors, and time-boxed throwaway spikes.
- **Removed:** per-task evidence JSON, generated execution records, and the ancestry validator. They checked structure, not whether a test was meaningful, and cost more than they protected.
- **Unchanged:** phases still integrate with merge commits, so the RED and GREEN commits stay in history. The user still authorizes each merge into `main`.
