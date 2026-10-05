# ADR 0024: A single Go module with explicit internal boundaries

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Product:** SuiteWard
- **Scope:** Application source organization and dependency direction.
- **Related:** [ADR 0003](0003-go-and-chi.md), [ADR 0004](0004-postgresql-pgx-sqlc.md), [ADR 0006](0006-provider-agnostic-modular-monolith.md), and [ADR 0023](0023-development-environment-and-project-local-tooling.md).

## Context

SuiteWard is a modular monolith whose canonical-contract rules must remain independent of GitHub, HTTP, database code, jobs, and eventual test execution. Multiple agents need bounded tasks that can advance concurrently without inventing incompatible interfaces.

A package per entity or per agent would fragment tightly related invariants. A single undifferentiated application package would make responsibilities and parallel changes difficult to review. The structure should express actual dependencies while growing with implemented capabilities.

## Decision

Use one application Go module, with module path `github.com/IgnisDevNE/SuiteWard`.

- Keep executable entry points under `cmd/`.
- Keep application implementation under `internal/`.
- Separate domain rules, application use cases, integration adapters, and composition.
- Group related capabilities within those boundaries. A layer is not a permanent assignment to one agent, and an entity does not automatically require its own package.
- Add packages and entry points when their behavior is implemented. The reference layout is not an instruction to create empty directories or build M2 components during M0.

The `cmd/` and `internal/` conventions follow the [Go project-layout guidance for servers](https://go.dev/doc/modules/layout#server-project). The capability grouping below is SuiteWard's own implementation direction.

## Reference layout and growth

```text
go.mod
go.sum                         # Once dependencies require it
cmd/
  suiteward/                   # Entry point, when there is a useful command
internal/
  domain/
    artifact/                  # Content identity and manifest value rules
    contract/                  # Canonical versions and governance rules
  application/
    governance/                # Use cases and interfaces they consume
  adapters/                    # Added with their implemented capabilities
    postgres/
      internal/dbgen/          # sqlc implementation details
    filesystem/                # Local content-addressed storage
    github/                    # Provider mapping and API interaction
    river/                     # Durable job integration
    httpapi/                   # Chi transport
    cli/                       # Local command transport
    mcp/                       # Agent transport
  bootstrap/                   # Runtime composition and lifecycle setup
docs/
```

M0 begins with the domain and the application behavior needed to demonstrate its invariants. Domain tests use content values and controlled inputs rather than filesystem, HTTP, or PostgreSQL access. Application tests can substitute the declared external interfaces. Persistent adapters and transports arrive when the milestone needs them.

`artifact` owns deterministic content/manifest rules; it does not read files or connect to storage. `contract` owns the related SuiteVersion, ChangeProposal, Approval, Policy, Promotion, and audit facts required by canonical governance. Exact aggregate boundaries remain subject to the domain design; this grouping does not make all entities one aggregate or one transaction.

Domain entities such as RunnerProfile and ExecutionPlan are added when the relevant use cases require them. Their presence in the product model is not a reason to scaffold execution infrastructure early.

Amendment (2026-10-05, phase R1): `internal/application/<capability>/<capability>test` packages may hold exported test support (fakes, conformance suites); only tests import them.

Unit tests live beside the packages they verify. Multi-component tests can live with the coordinating application package; tests requiring real infrastructure must be identifiable and runnable separately from the fast domain suite. SQL, migrations, generated code, and their configuration stay versioned and assigned to the persistence workstream; their final source-directory names are implementation details.

## Dependency rules

1. **Domain depends inward.** `contract` may consume `artifact`; `artifact` does not import `contract`. Domain packages do not import application, adapters, bootstrap, or provider/framework-specific types. Time and other nondeterministic inputs enter explicitly where rules require them.
2. **Application owns use-case coordination.** It imports the domain and declares the small interfaces it consumes at external boundaries. Avoid a generic shared `ports` package or interfaces added only to mirror every concrete type.
3. **Adapters translate and implement.** PostgreSQL/sqlc types, GitHub payloads, River job types, HTTP requests, and MCP messages remain at their boundaries. Application code does not import concrete adapters. Transports invoke application use cases; they do not bypass governance with direct persistence operations.
4. **Composition connects the parts.** Entry points and bootstrap select concrete implementations and manage process lifecycle. Business rules remain in domain/application packages rather than startup code.
5. **Transactions remain explicit.** A package boundary must not split a required atomic operation into separate commits. The canonical pointer, promotion record, and associated audit update retain the atomicity required by ADR 0004; durable job scheduling uses the transaction rules in ADR 0005.
6. **No dependency cycles or catch-all shared packages.** Extract a genuinely lower-level concept or revise responsibilities when a cycle appears. Do not conceal a cycle behind global state, service lookup, or a generic `common` package.

Package boundaries aid review and dependency control. They are not a process sandbox or a substitute for separating untrusted execution and human credentials.

## Parallel task implications

- Tasks include the implementation and tests needed for a bounded behavior, even when that behavior touches more than one layer.
- Agree minimal shared types and interface semantics before starting tasks that depend on them. Record dependencies needed to start separately from dependencies needed to integrate.
- Separate files can support concurrent work in a coherent package. Agent count does not determine package count.
- Assign temporary ownership of shared declarations, `go.mod`, migrations, generators, and composition changes. The integrating agent coordinates those edits without needing to implement all of them.
- Verify components together after independent work. Fakes do not prove PostgreSQL durability, GitHub behavior, or cross-system atomicity.

## Alternatives considered

- Multiple application modules: unnecessary version and dependency coordination for the current monolith.
- Packages organized only as global entity/repository/service buckets: related behavior becomes scattered and common files attract overlapping changes.
- One package per entity: introduces extra dependencies between tightly related governance rules.
- A complete future directory tree from the start: creates placeholders and interfaces before there are implemented consumers.

## Verification and remaining details

During implementation, verify that M0 domain/application tests run without GitHub credentials, PostgreSQL, or containers; review imports for the declared boundaries; and build the module on Windows and Linux. Adapter tests must demonstrate that provider and generated types do not become public application contracts.

Exact aggregate definitions, use-case signatures, command/process packaging, SQL source locations, and future execution packages remain implementation work. They must respect these boundaries and the existing authority model. This record does not create an SDK or a public Go API commitment.
