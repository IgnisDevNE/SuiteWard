# ADR 0026: Versioned PostgreSQL migrations

- Status: Accepted and implemented; amended 2026-10-05 (phase R1) with a pre-release reset to a single migration, and 2026-10-20 (phase SH) with a separate migrate step and a least-privilege service role.
- Date: 2026-10-02.
- Related: [ADR 0004](0004-postgresql-pgx-sqlc.md), [ADR 0023](0023-development-environment-and-project-local-tooling.md), [persistence contract](../contracts/persistence.md).

## Decision

Use Goose v3.28.0 as an embedded Go library with its Provider API, sequential SQL migrations, PostgreSQL session locking and transactional migration bodies. Pin pgx to v5.11.0; the existing project-local sqlc remains 1.31.1. The integrator resolves D-MIGRATIONS within the accepted PostgreSQL/pgx/sqlc stack; no new product authority policy or global tool installation is introduced.

The Provider keeps configuration local to the actual migration runner rather than using process-global migration registration. Authored SQL remains reviewable and versioned; sqlc generates private adapter access types from those inputs. Migration tooling does not authorize canonical promotion.

R1 note: the two-step sequence and its upgrade test below are superseded by the single `00001` in the [amendment](#amendment-2026-10-05-phase-r1-pre-release-reset). The initial supported sequence was migration 1 relational storage, then migration 2 history immutability and exact whole-authority revision guards. The complete sequence is required for adapter readiness. Test a fresh database, repeat/concurrent application, a populated 1-to-2 upgrade, and transaction rollback on migration failure. Every test uses a checkout-owned isolated database namespace. Preserve all existing canonical/history data through a supported upgrade.

Migrations move forward only in production. Do not edit an applied migration or auto-run out-of-order/nontransactional changes. Reject destructive down migration; rollback/recovery requires an explicit supported recovery procedure, defined by later restore work. A schema downgrade is not a canonical rollback or a way to erase an immutable audit.

A owns migrations, runner, SQL, sqlc configuration/generated output and the initial module dependency update. The integrator coordinates further shared-file changes. Migrations and generation changes follow mandatory TDD, real PostgreSQL evidence and independent review. CI must require integration and a clean regeneration before claiming persistence ready.

## Alternatives and consequences

Golang-migrate is a maintained alternative, but the selected Goose Provider fits embedded SQL, local configuration and the existing Go application boundary. A separate global migration CLI adds installation/configuration state without a current consumer. Neither tool proves transaction or authority correctness; M1.01 tests exercise those invariants against real PostgreSQL.

The library adds a pinned dependency and an explicit schema lifecycle. Session locking prevents competing migration runners from independently advancing the same database, while application authority uses its own concurrency control. The runtime and upgrade policy is the one of the [SH amendment](#amendment-2026-10-20-phase-sh-a-separate-migrate-step-and-a-least-privilege-role): `suiteward migrate` is the only command that changes the schema. R1 note: the "whole-Suite fence" and M1.02 remarks of the original decision are superseded by the per-Suite lock in the PostgreSQL unit of work (see the [amendment](#amendment-2026-10-05-phase-r1-pre-release-reset)).

## Sources

- [Goose v3.28.0 release](https://github.com/pressly/goose/releases/tag/v3.28.0), verified 2026-10-02.
- [Goose Provider](https://pressly.github.io/goose/blog/2023/goose-provider/).
- [pgx v5.11.0 release](https://github.com/jackc/pgx/releases/tag/v5.11.0), verified 2026-10-02.

## Amendment (2026-10-05, phase R1): pre-release reset

No installation exists yet, so the migrations are rewritten as a single normalized `00001` (see the [persistence contract](../contracts/persistence.md)). The forward-only rule applies from this reset on; the earlier two-step sequence and its populated 1-to-2 upgrade test no longer apply. The earlier M1-C01 contract is replaced by the persistence contract.

## Amendment (2026-10-10, phase M1.2): River's schema inside goose

River's own migrations are vendored into goose migrations (one file per River version, each inserting its `river_migration` row, followed by the outbox table), so goose stays the only migrator and `migrations.SupportedVersion` the only readiness version, and covers River's tables. A test checks that River's own migrator validates a goose-migrated schema. Upgrading River means adding goose migrations; the forward-only rule still holds.

## Amendment (2026-10-20, phase SH): a separate migrate step and a least-privilege role

The service no longer owns its schema, so a flaw in it cannot drop the immutability triggers, truncate history or move the schema version.

- **`serve` does not migrate.** It checks the schema version at startup and refuses to run on a schema that is not at `migrations.SupportedVersion`, older or newer; it connects as the application role. `suiteward setup` refuses the same way.
- **`suiteward migrate` is a one-shot step** that runs with the owner credential, `SUITEWARD_MIGRATION_DATABASE_URL` or `SUITEWARD_MIGRATION_DATABASE_URL_FILE`. Only `migrate` accepts it; `serve`, `setup` and `probe` refuse to start when it is set. The role it grants is the user of the service's own `SUITEWARD_DATABASE_URL`. It waits for the database to accept the owner connection (30 attempts, one second apart); a refused login or an unknown database fails at once, and nothing retries the step afterwards.
- **Grants.** After goose reaches `SupportedVersion`, `migrate` applies the grants on `current_schema()` to the application role: usage on the schema, read and write on the tables, usage on the sequences, only read on `goose_db_version`, and default privileges for what later migrations create. The grants are idempotent and convergent: every run also revokes `TRUNCATE`, `TRIGGER` and `REFERENCES` from the role.
- **Role checks.** Before it changes anything, `migrate` refuses a role that does not exist, that is the migration role or a member of it, a superuser, a role with `BYPASSRLS`, the owner of the schema or a member of its owner, or the owner of a table in the schema. The operator creates the role, because creating it needs a password.
- **River's reindexer is off.** River reindexes its own indexes every day, which needs the owner of the schema, so the runtime configures it with an empty index list.

The forward-only rule is unchanged.

