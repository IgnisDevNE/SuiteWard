# Test-first development

- **Status:** Accepted practice, referenced by [ADR 0025](decisions/0025-test-driven-development.md).

SuiteWard's value is that governance rules hold, so the tests for those rules are written before the code that satisfies them. This page states what that requires. It deliberately has no evidence files or per-task bookkeeping.

## When test-first is required

- Domain and application rules about authority, consent, approval, promotion, idempotency, and concurrency.
- Adapter behavior: PostgreSQL transactions and constraints, filesystem handling, GitHub interaction, and similar boundaries.
- Bug fixes: start from a failing reproducer.

## Procedure

1. Write a test for one behavior, including its failure, boundary, and security cases where relevant.
2. Run it and confirm it fails for the right reason: the behavior is absent or wrong, not a typo, missing import, or tooling problem.
3. Commit the test (`test: ...`) on its own.
4. Implement the minimum that passes, then commit (`feat:` or `fix:`).
5. Refactor if useful and rerun the tests.

A compilation error, missing dependency, syntax error, or tooling failure is not a behavioral RED. A test written after the implementation does not satisfy this rule.

## Review

The pull request shows each test commit before its implementation commit, and `sw-reviewer` checks that order and that the test meaningfully constrains the behavior. Phase PRs are merged with a merge commit so that history survives. Coverage and green CI do not prove the order or the quality of the tests; the commit history and the reviewer do.

## Exempt work

Documentation, deletions, mechanical refactors with unchanged behavior, and time-boxed throwaway spikes whose code is discarded. Anything a spike teaches is then implemented test-first.

## Choosing tests

Prefer observable behavior over implementation steps. Use real PostgreSQL for claims about transactions and constraints, and a fake only at a true external boundary. Conventions for naming, fakes, and parallel tests are in the [development guide](development-guide.md); coverage checks are in [CI and coverage](ci-and-coverage.md).
