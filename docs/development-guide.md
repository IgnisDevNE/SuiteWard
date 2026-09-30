# SuiteWard development conventions

- **Updated:** 2026-09-30
- **Status:** Accepted conventions; M0 domain implementation has started.
- **Scope:** How contributors and agents write SuiteWard code and tests.

These conventions complement the [project structure decision](decisions/0024-single-module-project-structure.md), [development environment decision](decisions/0023-development-environment-and-project-local-tooling.md), and [engineering preparation plan](engineering-plan.md). They do not define the frameworks used by customer repositories protected by SuiteWard.

Use the [local development guide](local-development.md) to prepare tools and the database, run verification, and keep worktree resources isolated. Versioned ADRs and project documents remain canonical when Memtrace is unavailable or inconsistent.

## Tests

Follow [mandatory task-level TDD](tdd.md), accepted in [ADR 0025](decisions/0025-test-driven-development.md). Before implementing each behavior, write its test and observe a meaningful failure; record the RED revision, then implement and record the same command passing at GREEN. Refactor when useful and rerun the relevant suite. Bug fixes start with a reproducer. Include failure, security, boundary, and invariant scenarios relevant to the task; a green happy path alone is insufficient.

This rule also applies to infrastructure scripts and configuration that changes behavior. Documentation, discovery, and decision tasks can record a justified `not_applicable` only while they change no executable behavior. Each task carries its own evidence and independent review into the phase PR. Passing CI or achieving coverage does not establish that tests preceded implementation.

Use Go's standard `testing` package as the initial test framework. Use table-driven cases when several inputs exercise the same behavior; use focused standalone tests when that makes the scenario clearer. Test files live beside the package they exercise.

Tests should establish observable behavior and invariants rather than duplicate implementation steps. For the domain, cover relevant accepted and rejected transitions, exact revision binding, stale approvals, immutable history, and competing promotions. Include failure cases alongside successful paths.

Use small, purpose-built fakes at actual external boundaries. Pass controlled time and other nondeterministic inputs where a rule depends on them. A fake is not evidence that PostgreSQL transactions, filesystem behavior, or GitHub requests work correctly; adapter and integrated tests must exercise those real components as they are implemented.

Keep the M0 suite runnable without external services. Later infrastructure tests must be identifiable and runnable separately. Parallel tests must own independent mutable resources; do not use timing sleeps as a substitute for coordinating concurrent test behavior.

Coverage targets and CI checks are defined in the [CI and coverage guide](ci-and-coverage.md). Detailed persistence and generated-code checks must be implemented with those capabilities.

Reference: [Go testing documentation](https://pkg.go.dev/testing).

## Structured logging

Use the standard `log/slog` package for application and infrastructure logs. Include a meaningful event message, level, and structured attributes that identify the relevant operation. When available and appropriate, use identifiers such as project, suite, proposal revision, job, or verification rather than requiring a reader to reconstruct context from prose.

Keep domain rules independent of a logger. Report domain outcomes to their caller; the application or adapter can log the operational result. Log a handled failure at the responsible boundary rather than repeating the same error at every return site.

Do not log credentials, tokens, private recovery keys, or unnecessary protected test contents. Diagnostic logs and durable audit records have different responsibilities: a log message does not replace an AuditEvent or the transaction requirements attached to a promotion.

The log format, retention, and collection setup remain operational details to define when the runtime is prepared.

Reference: [Go structured logging documentation](https://pkg.go.dev/log/slog).

## Errors

Return explicit errors for expected failures. Use a stable sentinel or typed error where callers must distinguish outcomes, and wrap errors with useful operation context while preserving a cause when appropriate.

Inspect error identity or type with `errors.Is` and `errors.As`; do not branch on an error's text. Tests should normally assert the relevant category and behavior instead of an incidental full message.

Translate provider, persistence, and transport errors at their boundaries. Domain/application contracts must not require consumers to understand pgx, GitHub, or HTTP-specific failure types. Likewise, public responses must not expose raw internal details merely because an error was wrapped.

Operational failure, rejected governance action, and an already-completed idempotent operation must remain distinguishable where the use case requires it. Expected invalid input is an error/result path, not a reason to panic.

Reference: [Go errors documentation](https://pkg.go.dev/errors).

## Dependency composition

Connect dependencies explicitly through constructors or explicit parameters. Use manual composition in the entry point/bootstrap boundary; do not introduce a dependency-injection container or service locator as part of the initial implementation.

Define small interfaces at a consuming boundary when substitution, testing, or multiple implementations actually require them. Avoid introducing an interface merely to mirror every concrete type. Domain/application code continues to follow the import rules in ADR 0024.

Keep dependencies visible in a component's construction rather than looking them up through hidden global mutable state. Tests should be able to supply their own relevant dependencies without changing another test's environment or process-wide configuration.

## Applying these conventions

Each task includes its behavior tests and follows the same conventions, regardless of the implementing agent. Additional libraries should address a concrete limitation and be discussed with the coordinating agent when they change shared dependencies. Install selected tools locally with pinned versions as required by ADR 0023.

Configuration loading, migrations, and release automation remain separate open decisions. Formatting/static-analysis gates, vulnerability checks, and coverage policy are now defined in the [CI and coverage guide](ci-and-coverage.md).
