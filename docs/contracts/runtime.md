# Runtime contract

- **Status:** frozen for phase M1.2 by M1.2-C0 (decision D-RUNTIME).
- **Consumers:** M1.2-A (configuration), M1.2-B (jobs and outbox), M1.2-C (composition and lifecycle), M1.2-D (image, deployment, smoke).

One binary, `suiteward`, runs the API and the workers. This document fixes the surface other tasks build against: settings, commands, endpoints, lifecycle and container conventions. The persistence side (jobs, outbox) is in the [persistence contract](persistence.md#jobs-and-outbox-m12-normative).

## Settings

Every setting is an environment variable named `SUITEWARD_<NAME>`. A secret setting can instead be given as `SUITEWARD_<NAME>_FILE`, the path of a file whose content (trailing newline trimmed) is the value; setting both forms is an error. A variable that is set but empty counts as unset, in both the plain and the `_FILE` form (so compose-style `${VAR:-}` interpolation behaves). Settings are read once at startup, from an injected environment (a lookup function and the list of variables), never from the process environment inside `internal/config`, so tests do not depend on it. Validation reports every problem at once, names the variable, and never prints a secret value. An unrecognized `SUITEWARD_*` variable is logged as a warning at startup, except the names the development tooling sets (`SUITEWARD_TEST_*`, `SUITEWARD_TOOLS_*`, `SUITEWARD_HOOK_*`); it is not an error, because those variables exist in development shells and CI.

| Name | Secret | Default | Rule |
| --- | --- | --- | --- |
| `DATABASE_URL` | yes | none (required) | PostgreSQL URL accepted by pgx. |
| `ARTIFACT_DIR` | no | none (required) | Absolute path of the artifact store. |
| `HTTP_ADDR` | no | `127.0.0.1:8080` | `host:port` (the port is validated when listening). |
| `SHUTDOWN_TIMEOUT` | no | `30s` | Go duration, 1s to 10m. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error`. |
| `JOB_WORKERS` | no | `4` | Integer 1 to 64. |
| `JOB_TIMEOUT` | no | `1m` | Go duration, 1s to 1h; per-attempt limit. River rescues a job that stayed running for `JOB_TIMEOUT` plus one minute. |
| `JOB_MAX_ATTEMPTS` | no | `5` | Integer 1 to 25. |
| `OUTBOX_MAX_ATTEMPTS` | no | `8` | Integer 1 to 25. |
| `OUTBOX_POLL_INTERVAL` | no | `5s` | Go duration, 1s to 1h. |
| `OUTBOX_LEASE` | no | `1m` | Go duration, at least `5s`. |

Later phases add settings (for example GitHub App identifiers and the key file) under the same rules.

## Commands

- `suiteward serve`: run the service (below).
- `suiteward probe [--delay D] [--no-wait] [--wait ID] [--timeout D]`: diagnostic client for smoke tests. Without `--wait` it connects with the same settings as `serve` and calls the non-port adapter entry points `(*postgres.Store).EnqueueSystem(ctx, Job, OutboxMessage) (jobID int64, err error)`, which inserts a `probe` job (scheduled after `--delay`, default 0) and an outbox message of kind `probe` (key `probe-<random>`) in one transaction using the same statements as `Tx.Enqueue` and `Tx.Outbox` but with no Suite (see the persistence contract), and prints one JSON line with `probeId` (an opaque string encoding job id and outbox key), then, unless `--no-wait`, waits (default `--timeout 60s`) until the job is `completed` and the message `delivered`, and prints a final JSON line with both states and `elapsedMs`. `--wait ID` waits for an earlier probe by polling `(*postgres.Store).SystemStatus(ctx, jobID int64, outboxKey string) (SystemStatus, error)`, whose `JobState` and `OutboxState` are River's job state and the outbox state as text. Both entry points are owned by M1.2-B; the command in M1.2-C only calls them. Exit code 0 when both finished, 2 on timeout or when either reached a failed terminal state, 1 for any other error. The `probe` job and publisher only log; they exist to verify the runtime through the real worker, not governance.

Any other command, or none, prints usage and exits 2.

## `serve` lifecycle

1. Load settings, build the structured JSON logger (`log/slog`, stdout).
2. Open the PostgreSQL pool and run migrations to `migrations.SupportedVersion` under goose's session lock (safe with several instances), then `NewStore` verifies the schema.
3. Compose the artifact store, the `UnitOfWork` with the River inserter, the River client (workers, the outbox relay as a periodic job with `RunOnStart`), and the HTTP server.
4. Start River, then listen on `HTTP_ADDR`. `/readyz` turns 200 only when all of this succeeded.
5. On `SIGTERM` or `SIGINT`: `/readyz` returns 503, the HTTP server drains, River stops softly (running jobs finish), then the pool closes. All of it shares one `SHUTDOWN_TIMEOUT` budget; when it runs out, River is stopped with cancellation and the process exits 1. A clean stop exits 0. Jobs interrupted by a hard kill (or a forced stop) are re-run after the rescue interval; handlers must be idempotent.

A fatal startup error (bad settings, unreachable database, schema not ready) exits 1 after logging it; there is no retry loop inside the process (the supervisor restarts it).

## HTTP surface (Chi)

Bound to `HTTP_ADDR`; loopback by default. No endpoint changes state, and none returns secrets.

| Path | Meaning |
| --- | --- |
| `GET /healthz` | Liveness: 200 `{"status":"ok"}` while the process serves. |
| `GET /readyz` | Readiness: 200 when the database answers, the schema version equals `SupportedVersion` and River runs; 503 otherwise and during shutdown. Body `{"status":"ready","schemaVersion":N}` or `{"status":"unavailable","reason":"..."}`. |
| `GET /status` | JSON: `version`, `schemaVersion`, `jobs` (`available`, `running`, `retryable`, `scheduled`, `completed`, `discarded`) and `outbox` (`pending`, `delivered`, `failed`). Counts only. `discarded` and `failed` are the visible terminal failures. |

## Container conventions

- Image `ghcr.io/ignisdevne/suiteward`, multi-arch (`linux/amd64`, `linux/arm64`), distroless static, user 65532, read-only root, `ENTRYPOINT ["/suiteward"]`, `CMD ["serve"]`; the only writable path is the `/data` volume (artifacts under `/data/artifacts`). Labels `org.opencontainers.image.source` and `org.opencontainers.image.revision`.
- Image defaults: `SUITEWARD_HTTP_ADDR=0.0.0.0:8080`, `SUITEWARD_ARTIFACT_DIR=/data/artifacts`. Secrets are mounted files: `SUITEWARD_DATABASE_URL_FILE=/run/secrets/database-url`.
- Runtime layout on both targets: a user-defined network `suiteward`; PostgreSQL in a rootless container named `suiteward-db` (the image pinned by digest in CI, volume `suiteward-db-data`, password from a Podman secret) reachable as `suiteward-db:5432`; the service container `suiteward` publishes `127.0.0.1:8081:8080` only. Nothing listens on a public interface.
- The supervisor's stop timeout (Podman `StopTimeout`) must exceed `SHUTDOWN_TIMEOUT` (45 s with the default 30 s).
- Outbound-only: the service needs no inbound port; the host reaches `/healthz`, `/readyz` and `/status` over loopback.
