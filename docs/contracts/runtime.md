# Runtime contract

- **Status:** frozen for phase M1.2 by M1.2-C0 (decision D-RUNTIME); GitHub settings added for phase M1.4 by M1.4-C0.
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
| `GITHUB_APP_ID` (M1.4) | no | none | Positive integer. The GitHub settings are all set or all unset; when unset, `serve` runs without synchronization. |
| `GITHUB_APP_KEY` (M1.4) | yes | none | The App's RSA private key in PEM (PKCS#1 or PKCS#8); normally given as `GITHUB_APP_KEY_FILE`. |
| `GITHUB_INSTALLATION_ID` (M1.4) | no | none | Positive integer. |
| `GITHUB_REPOSITORIES` (M1.4) | no | none | Comma-separated `owner/name`, 1 to 20 (D-SYNC), no duplicates. |
| `GITHUB_API_URL` (M1.4) | no | `https://api.github.com` | Absolute `https` URL (an `http` URL is accepted only for a loopback host, for tests). |

Later phases add settings under the same rules. The GitHub settings of M1.4 are an interim operator configuration: they do not establish the trusted App installation association, which D-IDENTITY and M1.5 decide (see [github](github.md#configuration-interim-m14)).

## Commands

- `suiteward serve`: run the service (below).
- `suiteward probe [--delay D] [--no-wait] [--wait ID] [--timeout D]`: diagnostic client for smoke tests. Without `--wait` it connects with the same settings as `serve` and calls the non-port adapter entry points `(*postgres.Store).EnqueueSystem(ctx, Job, OutboxMessage) (jobID int64, err error)`, which inserts a `probe` job (scheduled after `--delay`, default 0) and an outbox message of kind `probe` (key `probe-<random>`) in one transaction using the same statements as `Tx.Enqueue` and `Tx.Outbox` but with no Suite (see the persistence contract), and prints one JSON line with `probeId` (an opaque string encoding job id and outbox key), then, unless `--no-wait`, waits (default `--timeout 60s`) until the job is `completed` and the message `delivered`, and prints a final JSON line with both states and `elapsedMs`. `--wait ID` waits for an earlier probe by polling `(*postgres.Store).SystemStatus(ctx, jobID int64, outboxKey string) (SystemStatus, error)`, whose `JobState` and `OutboxState` are River's job state and the outbox state as text. Both entry points are owned by M1.2-B; the command in M1.2-C only calls them. Exit code 0 when both finished, 2 on timeout or when either reached a failed terminal state, 1 for any other error. The `probe` job and publisher only log; they exist to verify the runtime through the real worker, not governance.

Any other command, or none, prints usage and exits 2.

## `serve` lifecycle

1. Load settings, build the structured JSON logger (`log/slog`, stdout).
2. Open the PostgreSQL pool and run migrations to `migrations.SupportedVersion` under goose's session lock (safe with several instances), then `NewStore` verifies the schema.
3. Compose the artifact store, the `UnitOfWork` with the River inserter, the River client (workers, the outbox relay as a periodic job with `RunOnStart`), and the HTTP server. When the GitHub settings are set (M1.4), also compose the App installation (`appauth`, with the read-only synchronization permission map of D-GITHUB-ACCESS), the REST client (`rest`, one process-wide budget at 0.5) and the reader (`github`), and register the synchronization jobs below; when they are unset, nothing GitHub-related is composed.
4. Start River, then listen on `HTTP_ADDR`. `/readyz` turns 200 only when all of this succeeded.
5. On `SIGTERM` or `SIGINT`: `/readyz` returns 503, the HTTP server drains, River stops softly (running jobs finish), then the pool closes. All of it shares one `SHUTDOWN_TIMEOUT` budget; when it runs out, River is stopped with cancellation and gets up to 5 s more to return (River's cancelling stop gives up at once when its context is already spent, so cancelled jobs would otherwise never write their state), the pool stays open if River still does not return, and the process exits 1. A clean stop exits 0. Jobs interrupted by a hard kill (or a forced stop) are re-run after the rescue interval; handlers must be idempotent.

A fatal startup error (bad settings, unreachable database, schema not ready) exits 1 after logging it; there is no retry loop inside the process (the supervisor restarts it).

## HTTP surface (Chi)

Bound to `HTTP_ADDR`; loopback by default. No endpoint changes state, and none returns secrets.

| Path | Meaning |
| --- | --- |
| `GET /healthz` | Liveness: 200 `{"status":"ok"}` while the process serves. |
| `GET /readyz` | Readiness: 200 when the database answers, the schema version equals `SupportedVersion` and the service is not shutting down; 503 otherwise. It does not query River: River starts before the listener opens, the process never cancels the context it started with, and only the shutdown stops it, so River runs while this answers 200. During shutdown the 503 is best effort: the listener closes at once and `net/http` drops a request it reads after the drain began, so only a request in flight at that instant sees it. Body `{"status":"ready","schemaVersion":N}` or `{"status":"unavailable","reason":"..."}`. |
| `GET /status` | JSON: `version`, `schemaVersion`, `jobs` (`available`, `running`, `retryable`, `scheduled`, `completed`, `discarded`) and `outbox` (`pending`, `delivered`, `failed`). Counts only. `discarded` and `failed` are the visible terminal failures. The job counts exclude the outbox relay's own periodic job (`outbox_relay`) and cover only the jobs River still retains (see the [persistence contract](persistence.md#jobs-and-outbox-m12-normative)); a relay that keeps failing shows as a growing `outbox.pending`, not in `jobs`. Since M1.4, when GitHub is configured, also `github`: one entry per configured repository with `repo`, `lastSuccess`, `lastError` (text without secrets or bodies) and `waitUntil` (RFC 3339 or absent). The job counts also exclude the synchronization tick (`github_sync_tick`). |

### Synchronization jobs (M1.4)

- `github_sync_tick`: one periodic River job every 60 s (D-SYNC) with `RunOnStart`, so a restart catches up at once. It syncs every configured repository whose `wait_until` has passed, with at most 4 repositories in flight (S1: serial requests take about 0.37 s each, so 20 repositories with several open PRs would overrun 60 s serially). One cycle of one repository is the reconciliation of the [GitHub observations](persistence.md#github-observations-m14-normative). The tick gives itself a deadline below `JOB_TIMEOUT` (`JOB_TIMEOUT` minus 5 s), starts no repository after it, and records only the cycles it finished; repositories it did not reach wait for the next tick. A tick does not start while an earlier one is still running (River's unique insert for the periodic job, or an explicit skip that completes at once).
- `github_sync_repo` (args: `repo`): one repository now, inserted by the local MCP `sync` (M1.4-C); unique by args while pending, so repeated requests coalesce. It honors `wait_until` like the tick (it returns without calling GitHub and records nothing while waiting).
- A rate limit or exhausted budget ends that repository's cycle with `wait_until`; other repositories continue. A failed cycle is not a failed job: the job completes and the failure is in `/status`. `/readyz` does not depend on GitHub.

## Local MCP (M1.4; normative)

D-SYNC limits MCP to `sync` and `status`, authenticated with a local token. It lets a coding agent ask for a synchronization and read what SuiteWard observed; it has no approval, revocation, administration or configuration capability, and nothing it returns is authority. The "no endpoint changes state" rule of the HTTP surface covers `HTTP_ADDR`; the MCP `sync` tool enqueues work on its own listener.

- **Transport and library:** MCP Streamable HTTP served by the official Go SDK (`github.com/modelcontextprotocol/go-sdk`, a version that supports revision `2026-07-28` and the earlier `initialize`-based revisions, pinned in `go.mod`), so protocol negotiation, `server/discover`, request-metadata header validation and legacy clients follow the specification without a hand-written protocol. JSON responses; no server-initiated messages; `GET` and `DELETE` answer 405 unless the SDK requires them for a supported legacy revision. Endpoint `POST /mcp` on a separate listener `MCP_ADDR` (default `127.0.0.1:8090`).
- **Origin:** a request carrying an `Origin` header is rejected with 403 unless the origin is in `MCP_ALLOWED_ORIGINS` (comma-separated, empty by default), against DNS rebinding; non-browser clients send none.
- **Authentication:** `Authorization: Bearer <MCP_TOKEN>`, compared in constant time; a missing or wrong token answers 401 with no detail. The token is never logged or returned. Remote access only through Session Manager port forwarding or Cloudflare Access with a service token (D-DEPLOY).
- **Backend port** (in `internal/adapters/mcp`, implemented by the composition over the GitHub observations and the job inserter):

  ```go
  type Backend interface {
  	Status(ctx context.Context) (StatusView, error)
  	RequestSync(ctx context.Context, repo string) (SyncResult, error)
  }
  ```

- **Tool `status`** (no arguments): GitHub configured or not; per configured repository `repo`, `lastSuccess`, `lastError`, `waitUntil`, and its open pulls (`number`, `headSha`, `baseRef`, `updatedAt`, `notOpenSince`). No comment bodies (they may carry commands). Returned as structured content and as one JSON text item.
- **Tool `sync`** (argument `repo`, one of the configured `owner/name`): inserts `github_sync_repo` (coalesced while pending) and returns `queued` true, or false with `reason` (`already_pending`, `waiting` with `waitUntil`); it does not wait for the cycle. Unknown repository, or GitHub not configured: a tool error (`isError: true`).
- **Composition:** `serve` starts the MCP listener whenever `MCP_TOKEN` is set, independently of the GitHub settings (without them `status` says GitHub is not configured and `sync` is a tool error), after River and before `/readyz` turns 200; shutdown drains it with the HTTP server within `SHUTDOWN_TIMEOUT`.
- **Settings:** `MCP_ADDR` (default `127.0.0.1:8090`; the image sets `0.0.0.0:8090` like `HTTP_ADDR`), `MCP_TOKEN` (secret, `_FILE` form expected, at least 32 bytes), `MCP_ALLOWED_ORIGINS` (optional).
- **Compatibility evidence:** the phase verification connects the real Claude Code client (`claude mcp add --transport http …`) to a local `serve` and runs `tools/list` and both tools; a protocol implementation counts as compatible only after a real client used it.

## Container conventions

- Image `ghcr.io/ignisdevne/suiteward`, multi-arch (`linux/amd64`, `linux/arm64`), distroless static, user 65532, read-only root, `ENTRYPOINT ["/suiteward"]`, `CMD ["serve"]`; the only writable path is the `/data` volume (artifacts under `/data/artifacts`). Labels `org.opencontainers.image.source` and `org.opencontainers.image.revision`.
- Image defaults: `SUITEWARD_HTTP_ADDR=0.0.0.0:8080`, `SUITEWARD_ARTIFACT_DIR=/data/artifacts`. Secrets are mounted files: `SUITEWARD_DATABASE_URL_FILE=/run/secrets/database-url`.
- Runtime layout on both targets: a user-defined network `suiteward`; PostgreSQL in a rootless container named `suiteward-db` (the image pinned by digest in CI, volume `suiteward-db-data`, password from a Podman secret) reachable as `suiteward-db:5432`; the service container `suiteward` publishes `127.0.0.1:8081:8080` only. Nothing listens on a public interface.
- The supervisor's stop timeout (Podman `StopTimeout`) must exceed `SHUTDOWN_TIMEOUT` plus the 5 s forced-stop grace (45 s with the default 30 s; a `SHUTDOWN_TIMEOUT` of 40 s or more needs a larger `StopTimeout`).
- Outbound-only: the service needs no inbound port; the host reaches `/healthz`, `/readyz` and `/status` over loopback.
