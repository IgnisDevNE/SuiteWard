# ADR 0003: Go and Chi for the application and HTTP adapter

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Product:** SuiteWard
- **Scope:** Implementation language and HTTP routing.
- **Related:** [ADR 0001: Self-hosted deployment and GitHub synchronization](0001-self-hosted-github-synchronization.md).

## Context

SuiteWard initially serves individual developers working with AI agents.
Its first milestones concentrate on canonical contract governance, approvals, persistence, and GitHub synchronization.
Later execution work coordinates an isolated backend rather than requiring a specialized runtime inside the control plane.

The implementation should be straightforward to review and operate as an open-source, self-hosted application.
The project prefers a small set of focused dependencies and explicit application behavior.
Framework convenience must not determine domain authority or couple business rules to GitHub and HTTP.

## Decision

Use Go as the implementation language for the SuiteWard domain, application services, and adapters.
Use Chi for HTTP routing on top of the standard library's `net/http` interfaces.

Chi provides route grouping and middleware composition while retaining standard HTTP handlers.
Its documented compatibility with `net/http` allows the HTTP layer to use standard handler and middleware interfaces. [Chi documentation](https://github.com/go-chi/chi).

Keep the architecture as a modular monolith with explicit boundaries:

- Domain rules represent projects, canonical suites, proposals, approvals, evidence, and promotions independently of HTTP and GitHub types.
- Application services coordinate use cases and enforce authorization and state transitions.
- HTTP handlers validate transport inputs, call application services, and translate results into responses.
- The GitHub and local MCP adapters invoke the same application services through their own transport boundaries.

Do not introduce a second implementation language or a full application framework as part of this decision.
The database access choice is recorded separately in [ADR 0004](0004-postgresql-pgx-sqlc.md).

## Rationale

Go suits the service, worker, and integration work in the planned milestones while keeping the implementation approachable.
Its standard library and concurrency model support this style of application. [Go FAQ](https://go.dev/doc/faq).

Chi adds a focused routing abstraction without requiring the domain to adopt a framework-specific request context.
This is a maintainability choice; no performance benchmark or measured throughput requirement drove it.

The language and router do not establish canonical authority by themselves.
Exact approvals, immutable versions, evidence binding, and atomic promotion remain explicit domain and persistence responsibilities.

## Alternatives considered

| Alternative | Assessment |
| --- | --- |
| Go with only `net/http` routing | Viable with fewer dependencies; Chi was selected for route and middleware composition while preserving the same handler interfaces. |
| Go with a broader HTTP framework such as Gin or Echo | Viable for an HTTP adapter; the selected approach better matches the project's preference for focused dependencies. |
| Rust | A credible option for expressive state modeling and static memory guarantees; Go was selected for the expected implementation and maintenance workflow. |
| C++ | A credible option where native integration or resource control is decisive; those needs do not drive the current roadmap. |
| C#/.NET | A credible service-development stack; Go was selected as the project's single implementation language. |

## Consequences

- HTTP handlers and middleware can follow standard Go interfaces.
- The project must define its own conventions for validation, error responses, configuration, and dependency composition.
- Go types and package boundaries must be complemented by runtime validation and database constraints where invariants require them.
- Changing an HTTP adapter must not require rewriting canonical contract rules.
- Dependencies still require version selection, maintenance, and security updates.

## Open implementation details

- Supported Go version and dependency versions.
- Package layout and process entry points.
- HTTP request and response contracts, middleware selection, and error conventions.
- Configuration, logging, and test conventions.

These details do not reopen the accepted Go and Chi choices.
