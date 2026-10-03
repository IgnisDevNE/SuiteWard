# ADR 0026: Versioned PostgreSQL migrations

- Status: Accepted implementation decision; migration behavior awaits M1.01 verification.
- Date: 2026-10-02.
- Related: [ADR 0004](0004-postgresql-pgx-sqlc.md), [ADR 0023](0023-development-environment-and-project-local-tooling.md), [M1-C01](../contracts/m1-01.md).

## Decision

Use Goose v3.28.0 as an embedded Go library with its Provider API, sequential SQL migrations, PostgreSQL session locking and transactional migration bodies. Pin pgx to v5.11.0; the existing project-local sqlc remains 1.31.1. The integrator resolves D-MIGRATIONS within the accepted PostgreSQL/pgx/sqlc stack; no new product authority policy or global tool installation is introduced.

The Provider keeps configuration local to the actual migration runner rather than using process-global migration registration. Authored SQL remains reviewable and versioned; sqlc generates private adapter access types from those inputs. Migration tooling does not authorize canonical promotion.

The initial supported sequence is migration 1 relational storage, then migration 2 history immutability and exact whole-authority revision guards. The complete sequence is required for adapter readiness. Test a fresh database, repeat/concurrent application, a populated 1-to-2 upgrade, and transaction rollback on migration failure. Every test uses a checkout-owned isolated database namespace. Preserve all existing canonical/history data through a supported upgrade.

Migrations move forward only in production. Do not edit an applied migration or auto-run out-of-order/nontransactional changes. Reject destructive down migration; rollback/recovery requires an explicit supported recovery procedure, defined by later restore work. A schema downgrade is not a canonical rollback or a way to erase an immutable audit.

A owns migrations, runner, SQL, sqlc configuration/generated output and the initial module dependency update. The integrator coordinates further shared-file changes. Migrations and generation changes follow mandatory TDD, real PostgreSQL evidence and independent review. CI must require integration and a clean regeneration before claiming persistence ready.

## Alternatives and consequences

Golang-migrate is a maintained alternative, but the selected Goose Provider fits embedded SQL, local configuration and the existing Go application boundary. A separate global migration CLI adds installation/configuration state without a current consumer. Neither tool proves transaction or authority correctness; M1.01 tests exercise those invariants against real PostgreSQL.

The library adds a pinned dependency and an explicit schema lifecycle. Session locking prevents competing migration runners from independently advancing the same database, while application authority still uses its own whole-Suite fence. No automated application runtime/upgrade policy is invented before M1.02.

## Sources

- [Goose v3.28.0 release](https://github.com/pressly/goose/releases/tag/v3.28.0), verified 2026-10-02.
- [Goose Provider](https://pressly.github.io/goose/blog/2023/goose-provider/).
- [pgx v5.11.0 release](https://github.com/jackc/pgx/releases/tag/v5.11.0), verified 2026-10-02.
