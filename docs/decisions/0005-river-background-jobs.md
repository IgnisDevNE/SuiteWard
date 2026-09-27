# ADR 0005: River for background jobs

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Product:** SuiteWard
- **Scope:** Durable application jobs, transactional enqueueing, retries, and reconciliation scheduling.

## Context

SuiteWard needs background processing for repository discovery, PR reconciliation,
approval processing, and publication of checks. Its initial self-hosted deployment
already requires PostgreSQL and uses pgx with sqlc for application data access.

The installation should remain small. A committed domain change must not lose its
required follow-up work if the process exits between saving state and enqueueing.
The outbound synchronization model in [ADR 0001](0001-self-hosted-github-synchronization.md)
also requires recovery when an agent omits an MCP call or the instance restarts.

## Decision

Use the **open-source core of River with PostgreSQL** for background jobs.
Use its pgx integration alongside existing application transactions and sqlc queries.
River supports insertion within a database transaction through its driver interface. [River database drivers](https://riverqueue.com/docs/database-drivers).

When a domain change requires follow-up work, insert the River job using the same
pgx transaction as the change and its required audit records. Commit or roll back
them together; do not enqueue separately after committing. This removes the
database-to-queue dual-write gap for those operations. [Transactional enqueueing](https://riverqueue.com/docs/transactional-enqueueing).

Jobs invoke application services; they do not own domain authority. Workers must
apply normal project authorization, exact approval, evidence binding, and atomic
promotion rules. A completed job is not a successful Verification or permission
to promote a suite.

River coordinates application work; it is **not the test execution backend**.
The later DockerExecutionBackend executes candidate code across its own trust
boundary. Job workers must not run untrusted candidate code in the control plane.

Treat processing as repeatable. Use stable operation identities, database
constraints, and conditional state transitions to make domain effects idempotent.
For external effects, record intent durably and reconcile uncertain outcomes before
repeating an operation when possible. A PostgreSQL transaction cannot atomically
commit a GitHub API call. This design makes no exactly-once execution claim.

Use bounded retries with appropriate backoff for transient failures; preserve
actionable terminal failures for inspection and recovery. River supports retry
limits and configurable delay policies. [Job retries](https://riverqueue.com/docs/job-retries).

MCP-triggered and periodic work use the same reconciliation path and GitHub request
budget defined in ADR 0001. Coalesce redundant requests without losing a newer
revision or a trigger received while processing. Neither retries nor MCP requests
may bypass rate-limit delays, authorization checks, or fair repository scheduling.

Use River's core periodic scheduler as a trigger. Its schedule is held in memory
and resets after restarts or leader changes; individual trigger times may be missed.
SuiteWard must persist application reconciliation checkpoints and perform startup
catch-up. Advance a successful checkpoint only after the relevant scan and durable
work recording complete. Correctness must not depend on observing every timer tick. [Periodic jobs](https://riverqueue.com/docs/periodic-jobs).

Do not introduce River Pro implicitly. Durable periodic scheduling is a Pro feature;
the initial recovery design uses core scheduling plus SuiteWard checkpoints. [River Pro](https://riverqueue.com/docs/pro).

## Alternative considered

**Redis + Asynq** provides background workers, retries, and scheduled tasks in a
separate queue store. [Asynq](https://github.com/hibiken/asynq).
It would allow queue infrastructure to be operated independently, but adds another
service and a PostgreSQL-to-Redis consistency boundary. Reliable enqueueing would
require a transactional outbox and a relay that tolerates repeated publication.
These costs are unnecessary for the initial deployment; no throughput comparison
or assumption that PostgreSQL will satisfy every future workload is part of this decision.

## Consequences

- Operators maintain one primary data service for domain state and durable jobs.
- Jobs share PostgreSQL capacity and availability with the application; monitor queue age, failures, connection usage, and database load.
- River schema migrations and job retention become part of the upgrade and maintenance procedure.
- Application checkpoints and idempotent effects remain SuiteWard responsibilities; queue features do not replace them.
- Queue payloads should reference scoped records and immutable revisions, rather than embed credentials or candidate-supplied authority.

## Open implementation decisions

- Queue layout, worker concurrency, retry limits, timeouts, and retention periods.
- Checkpoint representation, coalescing semantics, and recovery for interrupted scans.
- API and worker process packaging, shutdown behavior, and operator retry controls.
- Polling defaults and acceptable discovery delay, validated against the shared GitHub budget in ADR 0001.
