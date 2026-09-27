# ADR 0004: PostgreSQL with pgx and sqlc

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Product:** SuiteWard
- **Scope:** Relational persistence and database access from Go.
- **Related:** [ADR 0001](0001-self-hosted-github-synchronization.md) and [ADR 0003](0003-go-and-chi.md).

## Context

SuiteWard stores canonical version pointers, immutable version metadata, proposals, approvals, verification evidence, promotions, and audit history.
Concurrent jobs and repeated synchronization must not create conflicting canonical updates or duplicate business effects.

The database layer must make transaction boundaries and conditional updates easy to inspect.
It should also avoid requiring handwritten Go mapping code for every query.
This choice is independent of the HTTP router and does not define the domain model through persistence types.

## Decision

Use PostgreSQL as the relational database.
Use pgx for PostgreSQL connectivity and sqlc to generate Go access code from explicitly authored SQL.
Keep SQL queries and schema changes in version control.

pgx provides PostgreSQL access and transaction APIs. [pgx documentation](https://pkg.go.dev/github.com/jackc/pgx/v5).
sqlc generates Go code from the schema and SQL queries and supports pgx as the driver. [sqlc PostgreSQL tutorial](https://docs.sqlc.dev/en/stable/tutorials/getting-started-postgresql.html).

Treat generated query types as persistence implementation details.
Domain and application services remain responsible for policy, authorization, approval binding, and valid state transitions.
The database enforces the constraints and atomic operations required to preserve those decisions under concurrency.

## Transactions and promotion

Canonical promotion must use a transaction and a conditional update of the canonical pointer.
The update compares the stored version with the expected version before replacing it: compare-and-swap, or CAS.
Confirm that exactly one intended row changed; a mismatch means the promotion must not be reported as successful.

The canonical pointer update, promotion record, and related audit record must commit together.
Immutable suite version records are not rewritten to implement promotion.
Policy and evidence checks must remain valid at the point of promotion; the exact locking and constraint strategy belongs to implementation design.

sqlc supports associating generated queries with a transaction through `WithTx`.
Its `:execrows` annotation exposes the affected-row count for a conditional update. [sqlc transactions](https://docs.sqlc.dev/en/stable/howto/transactions.html), [query annotations](https://docs.sqlc.dev/en/stable/reference/query-annotations.html).

Neither pgx nor sqlc automatically proves the correctness of a transaction or an approval.
Idempotency constraints, concurrent promotion behavior, and failure recovery require explicit implementation and verification.
PostgreSQL transactions do not atomically commit changes in GitHub or artifact storage; those boundaries need their own retry and reconciliation behavior.

## Alternatives considered

| Alternative | Assessment |
| --- | --- |
| pgx with handwritten Go query code | Preserves direct SQL control and avoids generation; requires more repetitive parameter, result, and mapping code. |
| An ORM such as GORM | Useful for common persistence operations and relationships; adds conventions and query abstractions that are not needed for the selected SQL-first approach. |
| Another relational database | Not selected; PostgreSQL is the agreed database for the initial deployment, so database portability is not a reason to hide its useful capabilities. |

An ORM is not inherently incompatible with the required guarantees.
For example, GORM supports explicit SQL and transactions; the preference for sqlc reflects reviewability and the desired development style. [GORM SQL support](https://gorm.io/docs/sql_builder.html), [GORM transactions](https://gorm.io/docs/transactions.html).

## Consequences

- Critical queries remain visible as SQL and can be reviewed with the schema.
- sqlc reduces repetitive Go access code while adding a generation step to development.
- SQL, generated code, and schema changes must stay consistent.
- PostgreSQL-specific behavior and pgx types remain inside the persistence adapter.
- The application still needs integration tests for transaction boundaries, constraints, retries, and concurrent state changes.
- Database operation, backup, and restore remain part of the self-hosted installation.

## Open implementation details

- Migration tool, migration execution policy, and upgrade/rollback procedure.
- Supported PostgreSQL and dependency versions.
- Final schema, constraints, indexing, locking, and transaction isolation choices.
- Query organization, generated-code location, and generation checks.
- Connection pool settings and database error translation.

These details do not reopen the accepted PostgreSQL, pgx, and sqlc choices.
