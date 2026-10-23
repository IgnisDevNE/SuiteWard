# Runtime contract

- **Status:** frozen for phase M1.2 by M1.2-C0 (decision D-RUNTIME); GitHub settings added for phase M1.4 by M1.4-C0; extended by each later phase through M1.11 (`v0.2.0`).
- **Consumers:** M1.2-A (configuration), M1.2-B (jobs and outbox), M1.2-C (composition and lifecycle), M1.2-D (image, deployment, smoke).

One binary, `suiteward`, runs the API and the workers. This document fixes the surface other tasks build against: settings, commands, endpoints, lifecycle and container conventions. The persistence side (jobs, outbox) is in the [persistence contract](persistence.md#jobs-and-outbox-m12-normative).

## Settings

Every setting is an environment variable named `SUITEWARD_<NAME>`. A secret setting can instead be given as `SUITEWARD_<NAME>_FILE`, the path of a file whose content (trailing newline trimmed) is the value; setting both forms is an error. A variable that is set but empty counts as unset, in both the plain and the `_FILE` form (so compose-style `${VAR:-}` interpolation behaves). Settings are read once at startup, from an injected environment (a lookup function and the list of variables), never from the process environment inside `internal/config`, so tests do not depend on it. Validation reports every problem at once, names the variable, and never prints a secret value. An unrecognized `SUITEWARD_*` variable is logged as a warning at startup, except the names the development tooling sets (`SUITEWARD_TEST_*`, `SUITEWARD_TOOLS_*`, `SUITEWARD_HOOK_*`); it is not an error, because those variables exist in development shells and CI.

| Name | Secret | Default | Rule |
| --- | --- | --- | --- |
| `DATABASE_URL` | yes | none (required) | PostgreSQL URL accepted by pgx. The service connects as the least-privilege application role, the user of this URL (see [persistence](persistence.md#database-roles-and-grants-sh-h4)), which cannot migrate. There is no separate setting for the role's name: `suiteward migrate` grants to the user of this URL. |
| `MIGRATION_DATABASE_URL` | yes | none | PostgreSQL URL of the role that owns the schema. Only `suiteward migrate` accepts it and requires it (exit 2 when missing). `serve`, `setup` and `probe` refuse to start when it (or the `_FILE` form) is set (exit 1: "the migration credential is for `suiteward migrate` only; remove it from this service's environment"), so the owner credential never reaches the service. Same rule as `DATABASE_URL`. |
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
| `GITHUB_REPOSITORIES` (M1.4) | no | none | Comma-separated `owner/name`, 1 to 20 (D-SYNC), no duplicates. An owner has at most 39 characters and a name at most 100, as on GitHub (since the M1.9 release review). |
| `GITHUB_API_URL` (M1.4) | no | `https://api.github.com` | Absolute `https` URL (an `http` URL is accepted only for a loopback host, for tests). |
| `INSTALLATION_TIER` (M1.5) | no | `co-located` | `isolated` or `co-located` (D-TIER). Declared by the deployment, never detected; the remote quadlet sets `isolated`, and `suiteward install` sets it on the service container to the tier it installs (`--tier`, `co-located` by default; `internal/installer/stack.go` for the co-located containers, the rendered quadlet for the isolated ones). |
| `REPORT_BASE_URL` (M1.13) | no | none | `http://127.0.0.1:<port>` or `http://localhost:<port>` (the host is lowercased, the port is 1 to 65535 without a leading zero, no path, query, fragment or user info, and a trailing slash is dropped): the origin that the change report links of a revision comment name, the HTTP port of the instance as the machine that runs it sees it (on the isolated tier, the end of a port forward). Anything else is refused at startup; the value is not echoed. Unset: the revision comment has no link. `suiteward install` sets it on both tiers from `--http-port` (`http://127.0.0.1:<HTTP port>`); a stack of an earlier release lacks it until `install --upgrade` adds it ([install guide, section 12](../install.md#what-the-installer-does-for-review)). |

Later phases add settings under the same rules. The GitHub settings of M1.4 are an interim operator configuration: they do not establish the trusted App installation association, which D-IDENTITY and M1.5 decide (see [github](github.md#configuration-interim-m14)).

## Commands

- `suiteward serve`: run the service (below). It never changes the schema.
- `suiteward migrate`: bring the schema to `migrations.SupportedVersion` and grant the application role its access (SH-H4). It takes the settings of `serve` plus `MIGRATION_DATABASE_URL`, the only command that accepts that setting (`serve`, `setup` and `probe` refuse to start when it is set), and runs as the owner of the schema: it first reads the role from the user of `DATABASE_URL`, waits up to 30 attempts (1 s apart, each bounded to 5 s) for the database to accept the owner connection (a database container that has started does not accept connections yet; a refused login or a missing database fails at once), and checks the role before it changes anything: it must exist and be least-privilege, so it is not the migration role or a member of it, a superuser, a role with `BYPASSRLS`, the owner of the schema or a member of its owner, or the owner of a table in the schema ("the service must connect as a least-privilege role"; exit 1, no URL printed). It then applies the goose migrations under goose's session lock (safe with several instances), then applies the grants of the persistence contract in one transaction (idempotent and convergent: every run applies them again, so a new migration's tables are covered even if a run was interrupted, and it revokes `TRUNCATE`, `TRIGGER` and `REFERENCES` from the role). It does not create the role, which needs a password: a missing role is an error that names it. The service role is never created or altered by `migrate`. Logs one JSON line (`database migrated`, `version`, `role`) to stdout, never a URL or secret. Exit 0 when the schema is at the supported version and the grants are applied, 2 when `MIGRATION_DATABASE_URL` is missing, 1 for any other error (database unreachable after the attempts, a migration failed, a schema newer than this build, the role missing or not least-privilege).
- `suiteward probe [--delay D] [--no-wait] [--wait ID] [--timeout D]`: diagnostic client for smoke tests. Without `--wait` it connects with the same settings as `serve` and calls the non-port adapter entry points `(*postgres.Store).EnqueueSystem(ctx, Job, OutboxMessage) (jobID int64, err error)`, which inserts a `probe` job (scheduled after `--delay`, default 0) and an outbox message of kind `probe` (key `probe-<random>`) in one transaction using the outbox statement of `Tx.Outbox` and the job inserter but with no Suite (see the persistence contract), and prints one JSON line with `probeId` (an opaque string encoding job id and outbox key), then, unless `--no-wait`, waits (default `--timeout 60s`) until the job is `completed` and the message `delivered`, and prints a final JSON line with both states and `elapsedMs`. `--wait ID` waits for an earlier probe by polling `(*postgres.Store).SystemStatus(ctx, jobID int64, outboxKey string) (SystemStatus, error)`, whose `JobState` and `OutboxState` are River's job state and the outbox state as text. Both entry points are owned by M1.2-B; the command in M1.2-C only calls them. Exit code 0 when both finished, 2 on timeout or when either reached a failed terminal state, 1 for any other error. The `probe` job and publisher only log; they exist to verify the runtime through the real worker, not governance.

- `suiteward setup --owner <login> --test-check <owner/name>=<check> ...` (M1.5; D-IDENTITY): bind the instance owner and enroll TOTP. It needs the same settings as `serve` including the GitHub settings, does not migrate (the schema must already be at the supported version, like for `serve` step 2), and is interactive: it is run on the instance itself (`podman exec -it suiteward /suiteward setup ...` on the co-located target, the same command in the owner's own Session Manager session on the isolated one). Exactly one `--test-check` per configured repository (missing, extra or duplicate is a usage error, exit 2). Steps:
  1. Look up the owner (`GET /users/{login}`, which must be a `User`) and every configured repository (`GET /repos/{r}`, which must be reachable with the configured installation); any failure exits 1 before anything is written.
  2. Generate a TOTP secret (`internal/domain/totp`) and print, once, its `otpauth://` URI and the base32 secret to stdout, with a warning on stderr that a recorded terminal session (Session Manager logging) keeps them.
  3. Read a code from stdin (up to 3 attempts) and verify it against the current time with a tolerance of one step; three failures exit 1 and write nothing.
  4. Atomically: replace the owner and the secret, append one policy revision per repository owned by `github:<numeric id>`, record each repository's test check, and consume the verified step (persistence contract, M1.5).
  5. Verify protection once per repository (D-PROTECTION) and print each result with its instructions.
  Exit 0 when enrollment succeeded, whatever the protection results; the secret, codes and tokens never reach a log. Re-running setup replaces the secret.

- `suiteward check [--json]`: the GitHub and database half of `suiteward doctor`, run in the service container (`podman exec suiteward /suiteward check`). It needs the settings of `serve`, including the GitHub settings, reads and posts nothing, and prints `ok <name>` or `FAIL <name>: <detail> — <fix>` for the App (`github.app`), the installation, its exact permissions, every repository's coverage and its protection result, the owner enrollment, the schema version and the service role's privileges (`github.installation`, `github.permissions`, `github.repositories`, `protection.<owner/name>`, `owner.enrolled`, `database.schema`, `database.role`), or with `--json` an array of `{name, ok, detail, fix}` (plus `"warning": true` on a warning). Without GitHub, the database checks run and the GitHub ones and `owner.enrolled` print `WARN <name>`, a pass that needs a look. Exit 0 when no result failed (a warning is not a failure), 1 otherwise.
- `suiteward install [flags]`, `suiteward install --upgrade [flags]`, `suiteward install host [flags]`, `suiteward uninstall [--purge] [--yes] [flags]`, `suiteward enroll`, `suiteward backup init`, `suiteward backup`, `suiteward restore` and `suiteward doctor [--json] [--settings PATH]`: the installer, run on the machine that installs (the host has no `SUITEWARD_*` environment), so they run before any configuration is loaded. The flags, the answers file and the checks are in the [install guide](../install.md#12-the-installer-flags-answers-file-and-what-it-does), and the backup, the recovery key and the restore (M1.13) in the [backup runbook](../backup.md). Exit 0 on success, 1 on a failure, 2 on a usage error, and 130 when the command is interrupted (Ctrl-C or SIGTERM) and ends with a cancelled context, as every one of them but `doctor` does. The usage the binary prints lists every one of these lines.

Any other command, or none, prints usage and exits 2.

## `serve` lifecycle

1. Load settings, build the structured JSON logger (`log/slog`, stdout).
2. Open the PostgreSQL pool as the application role and let `NewStore` verify the schema: `serve` does not migrate. A schema that is older than `migrations.SupportedVersion` is fatal and the error says to run `suiteward migrate`, naming both versions (for example, "schema version 19, this build requires 20"). A schema newer than the build is fatal too and says the database was migrated by a newer release: run that release or restore a backup. Only a schema that is not usable is reported as "not ready" (the version, a missing schema or version function, or a role without access to it, SQLSTATE 42883, 42P01, 42501 or 3F000); a refused login, an exhausted server or an unreachable database is reported as itself, never with the advice to migrate, and `/readyz` reports it as "database unavailable". `suiteward migrate` runs before `serve` on every upgrade; a deployment runs it as a separate step, never inside the service's own process: the quadlet unit [`suiteward-migrate.container`](../../deploy/quadlet/suiteward-migrate.container) (a one-shot that `suiteward.service` requires, so it exits 0 before the service starts) on the isolated host, and the same step of `suiteward install` on the co-located stack ([install guide](../install.md)).
3. Compose the artifact store, the `UnitOfWork` with the River inserter, the River client (workers, the outbox relay as a periodic job with `RunOnStart`), and the HTTP server. When the GitHub settings are set (M1.4), also compose the App installation (`appauth`, with the read-only synchronization permission map of D-GITHUB-ACCESS), the REST client (`rest`, one process-wide budget at 0.5) and the reader (`github`), and register the synchronization jobs below; when they are unset, nothing GitHub-related is composed.
4. Start River, then listen on `HTTP_ADDR`. Both listeners (`HTTP_ADDR` and the MCP address) bound each connection: ReadHeaderTimeout 10 s, ReadTimeout 30 s, WriteTimeout 30 s and IdleTimeout 60 s. They exceed the slowest answer (a store or MCP backend call is cut at 3 s) and the shutdown test's 1 s drain. The MCP SDK's one long-lived stream (`subscriptions/listen`, protocol 2026-07-28 and later) therefore ends after 30 s, and the client reconnects. `/readyz` turns 200 only when all of this succeeded.
5. On `SIGTERM` or `SIGINT`: `/readyz` returns 503, the HTTP server drains, River stops softly (running jobs finish), then the pool closes. All of it shares one `SHUTDOWN_TIMEOUT` budget; when it runs out, River is stopped with cancellation and gets up to 5 s more to return (River's cancelling stop gives up at once when its context is already spent, so cancelled jobs would otherwise never write their state), the pool stays open if River still does not return, and the process exits 1. A clean stop exits 0. Jobs interrupted by a hard kill (or a forced stop) are re-run after the rescue interval; handlers must be idempotent.

A fatal startup error (bad settings, unreachable database, schema not ready) exits 1 after logging it; there is no retry loop inside the process (the supervisor restarts it, and the supervisor starts `serve` after `migrate` has succeeded). River's daily reindexer is disabled (an empty `ReindexerIndexNames`): `REINDEX INDEX CONCURRENTLY` needs the owner, and the role the service runs as is not.

## HTTP surface (Chi)

Bound to `HTTP_ADDR`; loopback by default. No endpoint changes state, and none returns secrets.

| Path | Meaning |
| --- | --- |
| `GET /healthz` | Liveness: 200 `{"status":"ok"}` while the process serves. |
| `GET /readyz` | Readiness: 200 when the database answers, the schema version equals `SupportedVersion` and the service is not shutting down; 503 otherwise. It does not query River: River starts before the listener opens, the process never cancels the context it started with, and only the shutdown stops it, so River runs while this answers 200. During shutdown the 503 is best effort: the listener closes at once and `net/http` drops a request it reads after the drain began, so only a request in flight at that instant sees it. Body `{"status":"ready","schemaVersion":N}` or `{"status":"unavailable","reason":"..."}`. |
| `GET /status` | JSON: `version`, `schemaVersion`, `instance` (the first 8 characters of the instance id, as in the name of the setup authenticator entry), `jobs` (`available`, `running`, `retryable`, `scheduled`, `completed`, `discarded`) and `outbox` (`pending`, `delivered`, `failed`). Counts only. `discarded` and `failed` are the visible terminal failures. The job counts exclude the outbox relay's own periodic job (`outbox_relay`) and cover only the jobs River still retains: finished jobs follow River's default retention (completed and cancelled jobs 24 h, discarded jobs 7 days; decision D-JOB-RETENTION, verified against River v0.49.0) (see the [persistence contract](persistence.md#jobs-and-outbox-m12-normative)); a relay that keeps failing shows as a growing `outbox.pending`, not in `jobs`. Since M1.4, when GitHub is configured, also `github`: one entry per configured repository with `repo`, `lastSuccess`, `lastError` (text without secrets or bodies) and `waitUntil` (RFC 3339 or absent). The job counts also exclude the synchronization tick (`github_sync_tick`). Since M1.5, always `tier` (the effective tier: `co-located` when either `INSTALLATION_TIER` or the enrollment says so; with `tierNote` when they differ, saying to re-run setup) and `owner` (`login`, `accountId`, `enrolledAt`; absent before setup), and each `github` entry has `protection`: `result` (`enforced`, `findings`, `enforcement-unavailable`, `unknown`, or absent before the first verification), `findings` and `caveats` (lists of the D-PROTECTION codes), `checkedAt`. The job counts also exclude `github_protection_check`, `github_bootstrap`, `github_commands` and, since M1.6b, `github_checks`; each `github` entry then also has `checks`, the last check-job error for that repository, absent when none. Since M1.6b each entry also has `bootstrap`, a fixed code for the last reason that repository's bootstrap is waiting or stopped (`no-open-pull-request`, `awaiting-initial-contract`, `read-failed`, `repository-changed`, `invalid-source`, `origin-not-passed`, `assessment-conflict`, `blocked-<reason>`, `awaiting-merge-decision` (M1.7b) or `failed`; since the M1.7b rework the bootstrap codes are those of [Explicit bootstrap: propose and skip](#explicit-bootstrap-propose-and-skip-m17b-rework-2026-10-16)), kept in memory and cleared by the next run that finds nothing wrong, absent when none; never GitHub text. The MCP `status` tool does not show it. Since M1.7 each entry also has `merges` and `integrity`, and the job counts also exclude `github_merges` and `canonical_integrity` (see "Merges and integrity jobs"). Since M1.7c each `github` entry also has `cycleStartedAt` when one is recorded (see "Latency (M1.7c)"). Since M1.8, `source`: the URL of the source revision the binary was built from, set at build time like `version`, and absent when the build set none (D-RELEASE, AGPL section 13). When GitHub is configured, `unconfiguredRepositories` lists the enrolled repositories that `GITHUB_REPOSITORIES` does not name (the GitHub jobs skip them, and the commands horizon ignores them), absent when there are none; the MCP `status` tool shows it too. Since M1.11, when GitHub is configured and the current rate-limit window is known (a response was read and the window has not ended), `budget`: `remaining` (the requests SuiteWard may still send in the window, 0 once it refuses) and `reset` (RFC 3339, when the window ends and requests resume, corrected for the clock offset); absent otherwise; the MCP `status` tool shows it too. |
| `GET /reports/{owner}/{repo}/{revision}` | Since M1.13, the change report of one change proposal revision (`{revision}` is `P<n>-R<m>`) as one HTML page: the revision, the base canonical version, the numbers of its comment (files, lines, marker lines seen), and every file with its status, its `+N/−M` or why it has none (binary, too large, too different, work budget) and its unified diff. The page is static (inline style in the brand colors, light and dark; no script, nothing to load), and every string from the repository is escaped, with its hidden characters (control, bidirectional, zero-width, invalid UTF-8) written as escapes. It is rebuilt on every request from the manifest of the revision, which the checks job keeps in the artifact store under the manifest's own digest (its canonical encoding, whose SHA-256 is that digest), from the manifest of the base canonical in the database, and from the bytes of the files that differ, so a revision that a later head superseded still has its page. It reads nothing else. A repository that is not enrolled (and any repository before an owner is enrolled), a reference that is not `P<n>-R<m>`, an unknown or baseline revision (a baseline expects no canonical, so there is nothing to compare it with) and a revision recorded before M1.13, which has no stored manifest, are 404. Only a `Host` of `127.0.0.1`, `localhost` or `[::1]`, with any port or none, is answered; any other is 403 (DNS rebinding). That is no authentication and the route takes no token: anyone who can reach the HTTP listener reads the reports of the enrolled repositories, as they read `/status`. Every answer, errors included, carries `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` and `Cache-Control: no-store`. The sizes of the files that differ are read before their bytes: a file with a side over 1 MiB, the bound of the report, is not read, and its entry says it is too large to compare, with its sizes and no line count, an added or removed file included; a revision whose other files would take more than 32 MiB to read (both sides together) is refused before any of them is read, and so is a page over 8 MiB, both with 413. A file whose lines mostly end with a carriage return is shown without it and marked once as CRLF. A literal backslash of the repository is written twice, so that an escape is never the spelling of a file's own text; a report that cannot be built now is 503 with a fixed text, the cause logged. One report is built at a time in the process, and a request waits for its turn up to 20 seconds. The answer is `text/html; charset=utf-8`. The revision comment links this page when `REPORT_BASE_URL` is set; the page opens only where the instance is reachable. The contents and the bounds are in [Change report (M1.13)](#change-report-m113-normative). |

### Command and bootstrap jobs (M1.6a)

Both jobs are registered only when the GitHub settings are set, and they act only for repositories with an enrolled owner (D-COMMANDS).

**`github_bootstrap`**
- Periodic every 60 s with `RunOnStart`, `MaxAttempts` 1, unique while unfinished. Since M1.7c it runs on its own one-worker queue and can be woken; see "Latency (M1.7c)" below.
- For each enrolled project:
  1. If `Do` reports that the Suite does not exist (`ErrNotFound`), create it with `UnitOfWork.CreateSuite`: `default`, the owner's governing policy and `branch:<default branch>`. Never call `CreateSuite` for an existing Suite. After a same-owner re-enrollment it would conflict on the policy.
  2. Propose a baseline when the Suite has no canonical and either of these holds:
     - it has no proposal yet;
     - its latest proposal `P<n>` is unapproved and its carrier PR is no longer open. The bootstrap then moves to `P<n+1>-R1`. Synchronization lists comments only for open PRs, so approval cannot happen on a closed carrier.

     In both cases the repository must have an open PR and the inventory at the pinned head is non-empty (ADR 0010: the project keeps awaiting its initial contract; never an empty baseline). The steps:
     - pin the default branch head (`Reader.BranchHead`);
     - call `ProposeBaseline` with `P<n>-R1`, carried by the lowest-numbered open PR;
     - in `BaselineRequest.Outbox`, include a `github_comment` message with key `bootstrap-summary:<repository id>:P<n>-R1`. The publisher renders its body: the inventory summary, the origin and the copyable `/suiteward approve P<n>-R1 <code>`.
  3. If the latest baseline proposal has the owner's current approval and the Suite still has no canonical:
     - re-read the origin tree and record the assessment;
     - call `Bootstrap` with operation `bootstrap:<repository id>:P<n>-R1` and version `V1`. Operation ids are global (`operations` is keyed by the id alone), so the repository id keeps two repositories' first baselines apart. Before M1.7 the id was `bootstrap:P<n>-R1`; a receipt already stored under that form is never replayed again, because the bootstrap runs only while a Suite has no canonical;
     - in `PromoteRequest.Outbox`, include a `github_comment` message with key `bootstrap-promoted:<repository id>:P<n>-R1`.
  4. **Re-enrolling the same owner.** The Suite keeps the policy revision it was created with. That is harmless, because the owner is the same.
- A read failure skips the project until the next run. GitHub text is never stored or logged.

**`github_commands`**
- Periodic every 15 s with `RunOnStart`, `MaxAttempts` 1, unique while unfinished. It is a single job, so commands are serialized across all repositories (D-COMMANDS).
- It computes the sync watermark (D-COMMANDS) and takes `commands.Store.Pending` up to that horizon, in order. While an enrolled repository has never synchronized successfully there is no horizon: the run does nothing and logs a Warn naming that repository, because every command waits. Each verified owner code logs its offset in steps from the comment's step, and each rejected one whether a window of 2 steps would have matched (log only, never the code; M1.8 measurement). The horizon covers every enrolled repository, also one that is no longer in `GITHUB_REPOSITORIES` (fail closed); the first run to see such a repository logs a Warn naming it, once per repository per process. For each candidate:
  - **Before enrollment.** Record `before-enrollment` when the comment's `created_at` is before the current enrollment's `enrolled_at`. This covers S1's old commands and comments the owner wrote under a replaced secret.
  - **Not a command.** Record `not-command` when the author is the bot or the body has no command line.
  - **Not the owner.** Record `ignored-author` when the author is not the enrolled owner. It is otherwise ignored.
  - **Owner-authored.**
    1. Check the first observed version: an `updated_at` different from its `created_at` gives `edited-first`.
    2. Parse the command. Unparseable gives `malformed`.
    3. For an `approve`, a `revoke` or a `skip`, read the observed pull request. When its `not_open_since` is set and the synchronizer first observed the comment (`first_seen`) at or after it, record `not-command` with no acknowledgment and use no step: SuiteWard stops interacting with a closed pull request. Both times are the synchronizer's own clock, never GitHub's `created_at`, and equal times count as closed. A comment first observed while the pull request was listed open is decided as usual even after the pull request closed or merged. The guard compares two times of one clock, so it fires only when the synchronizer records cycles out of time order; in a normal run a pull request that lists a comment is open in that cycle, and every observed command is decided.
    4. For an `approve` or a `revoke`, when the proposal it names exists and another pull request carries it, record `other-pull` before any step is used, with an acknowledgment that names the reference and says to comment the command on that pull request. It never sets or clears the check.
    5. Verify the code at the comment's `created_at` with tolerance 1. A failure gives `invalid-code`.
    6. Call `ConsumeStep` with the source `github-comment:<id>`. `ErrStepUsed` gives `replayed-code`.
    7. Call `ProcessConsent` with the D-COMMANDS identifiers, `Order` set to the comment id, and the actor `github:<owner id>`. Its `Outbox` holds the acknowledgment, with key `ack:<repository id>:<comment id>`. When it reports `ErrNotFound`, look at the Suite and the proposal in one unit of work: a project with no Suite yet records `no-suite`, with an acknowledgment saying that the bootstrap is pending, and a proposal that does not exist on an existing Suite records `unknown-proposal` (the step stays consumed, because the code was valid). Any other probe failure is a store error.
    8. Record `processed` with the operation.
  - **Rejected owner commands.** `malformed`, `invalid-code`, `replayed-code`, `no-suite`, `unknown-proposal`, `other-pull` and `edited-first` get an acknowledgment, written with the ledger entry in one transaction. A known proposal with an unknown revision is processed: `ProcessConsent` rejects it with `UnknownRevision`, and the acknowledgment explains it. `unknown-proposal` is acknowledged with the same text (`Rejected`, reason `UnknownRevision`), which is true for a proposal that does not exist. `other-pull` is acknowledged as `OtherPullRequest`: `<ref> belongs to another pull request; comment the command on that pull request. Nothing changed.`
    - **The wording of a wrong code** (M1.10). The acknowledgment of `invalid-code` and `replayed-code` says that the command had no effect, and that the `SuiteWard / Contract` check may now show as failed until the owner comments a corrected command with a current code (a new one after a replayed code), or pushes. It no longer says that nothing changed, because the check does change (see "Owner-command failure").
- **Errors.**
  - A store or network error stops the run. A later candidate must never go ahead of an earlier one, because that would reopen the race the watermark closes.
  - After 5 consecutive failed attempts in this process, the candidate is recorded as `failed` and logged (repository, comment id, error kind; never GitHub text). The next candidate is then processed.
  - The job fails only when the ledger itself cannot be read or written.

**Outbox kind `github_comment`**
- **Payload.** The publisher receives only the key and the payload (`river.Publisher`). The payload is a JSON object with explicit tags. It never holds comment text, a code value or a secret. Every payload carries `repositoryId`, `repo` (`owner/name`) and `pull`, the PR to post on. The rest depends on the key:
  - `ack:<repository id>:<comment id>` for a **processed** command: `project`, `suite`, `operation`, `comment` and `owner` (the owner's login at processing time). The publisher reads the receipt with `Tx.Receipt` inside `UnitOfWork.Do(project, suite)`. It renders from the receipt, which holds the outcome as evaluated at processing time (ADR 0011):
    - `Result` gives approved, revoked, no active approval, or rejected with its reason;
    - `CurrentApprovalEligible` gives the approval-requirement line (a valid approval exists; it never claims that the revision can be promoted);
    - a non-empty `PromotedVersionID` selects `ApprovedAfterPromotion` for an approval, and `RevokedAfterPromotion` for a revocation, with or without an active approval.
  - `ack:<repository id>:<comment id>` for a **rejected disposition** (`malformed`, `invalid-code`, `replayed-code`, `no-suite`, `unknown-proposal`, `other-pull`, `edited-first`): `disposition`, `comment` and `owner`, plus `reason` (the malformed reason code) for `malformed`, and `reference` (`P<n>-R<m>`) when the command line parsed. These are system rows written by `commands.Store.Record`, so there is nothing to read back; the payload holds the decision.
    - The `reason` codes are `unknown-verb`, `missing-reference`, `bad-reference`, `missing-code`, `bad-code` and `extra-text`. `reference` is required for `unknown-proposal` and `other-pull` and optional for `invalid-code`, `replayed-code` and `no-suite` (a skip has none), and absent for `malformed` and `edited-first`.
  - `bootstrap-summary:<repository id>:P<n>-R1`: `summary`, the inputs of the bootstrap summary. These are the proposal and revision numbers, `origin`, `branch`, `protected` (the count), `paths`, `include`, `runner` (since M1.9; absent in a message of v0.1.0 or older, which renders as an empty list, "none") and `exclude` (at most 20 each), `includeTotal`, `runnerTotal` and `excludeTotal` (how many rules the declaration has, so the publisher can say how many are not listed), and `tier`, the effective tier (D-IDENTITY: `co-located` when either the setting or the enrollment says so).
    - The payload must stay within the outbox limit of 64 KiB after JSON encoding, whatever the repository holds. A payload over the limit would fail `ProposeBaseline` on every run.
    - So the bootstrap cuts each path, rule and the branch to at most 150 bytes (a runner rule to 30 bytes since the M1.9 release review, so that the worst case of escaped text, with a 140-character repository, stays 2 KiB under the 60 KiB guard) on a UTF-8 boundary before encoding, and ends a shortened entry with `…` (counted in the 150 bytes) so a cut entry never looks complete. `json.Marshal` turns a control byte into a 6-byte escape and invalid UTF-8 into U+FFFD, and that is acceptable for display.
    - A test pins the worst case: 20 paths and 20 rules of each kind, made of control bytes and invalid UTF-8.
  - `bootstrap-promoted:<repository id>:P<n>-R1`: `reference` and `version` (`V1`).
- **Rendering.** The publisher renders the body with the command-text package from these inputs.
  - An acknowledgment links its source comment (`#issuecomment-<id>`, an issue comment id).
  - A payload that does not parse, or does not match its key, is an error. The relay retries it and then marks it failed, and the error names the key, never the payload.
- It posts with `Writer.PostComment` and the comment token. The body ends with the hidden marker `<!-- suiteward:<instance id>:<outbox key> -->`.
- Before posting, it lists the PR's comments by the bot and skips the post when one already carries the same marker. Delivery is at least once (persistence contract), so posting is idempotent by marker.
- The instance id keeps two installations that post as the same App from mistaking each other's comments for their own.
- The job counts in `/status` exclude `github_bootstrap`, `github_commands` and, since M1.6b, `github_checks`.

### Check job and publisher (M1.6b)

**`github_checks`**
- Periodic every 30 s with `RunOnStart`, `MaxAttempts` 1, unique while unfinished; registered only with the GitHub settings; excluded from the `/status` job counts. It is the only writer of check states (D-CHECKS).
- For each enrolled and configured repository, for each open PR in the synchronization store (`NotOpenSince` zero):
  - **No Suite or no canonical:** `in_progress`, reason `bootstrap-awaiting` (`bootstrap-pending` before the M1.7b rework). See [Explicit bootstrap: propose and skip](#explicit-bootstrap-propose-and-skip-m17b-rework-2026-10-16).
  - **Otherwise** compare with `checks.Store.Current`:
    - same head, same canonical, same Suite revision and same latest owner command: nothing to do;
    - a stored `unchanged` result for the same head and canonical: keep it without reading the tree (consent cannot change it);
    - anything else: read the head tree (`Reader.Tree`) and call `governance.AssessHead`, which is idempotent. The inventory reads the covered files of a cold head eight at a time (the first failed read cancels the rest and is the error), and the GitHub reader keeps the blobs it read, up to 32 MiB in memory, by repository and blob SHA: a run cut off by the job timeout, a retry and the next head with the same blobs do not fetch them again. Neither changes the outcome, the API budget accounting or any stored state.
  - **State.**
    - `success`: the head is unchanged against the canonical (reason `unchanged`), or ready (reason `ready`).
    - `in_progress`: reasons `approval-missing` (including a revision rebound after the canonical or the policy changed), `not-current`, `assessment-pending`, `source-unavailable` (a tree GitHub truncated); `ErrCanonicalMoved` from `AssessHead` (the canonical moved while the head was assessed) keeps the current record and retries on the next run; any other `ErrConflict`, such as a conflicting assessment, is deterministic and is an opaque failure (`/status` problem `failed`) that fails that pull request's step while the run continues.
    - `failure`: reasons `invalid-source`, `integrity-failed`, `owner-command-rejected`. Since M1.7 `integrity-failed` comes only from the canonical integrity verdict. A head assessment never fails, because its binding is built from that head's own inventory, so `AssessHead` answering `IntegrityNotPassed` or `AssessmentMismatch` is an impossible state: an opaque failure of that pull request's step (`/status` problem `failed`), never a published state.
  - **Owner-command failure.** Amended 2026-10-19 (D-COMMANDS, "A wrong command has no effect"): only a wrong confirmation code counts. When the latest counted command on the pull is `invalid-code` or `replayed-code` and was created at or after the record's `HeadSince`, the state is `failure` with reason `owner-command-rejected`, on any open non-fork pull request, the bootstrap carrier and awaiting or skipped pull requests included. A processed command whose consent result is rejected is passed over (`LatestOwnerCommand` with `before`), so it neither sets nor clears the failure; the walk-back stops at `HeadSince`. An integrity failure and a fork keep precedence. A wrong code replaces an `approval-missing`, `invalid-source` or `runner-uncovered` result. A rejected owner command posted between a push and the job first recording that head does not set `failure` (the record's `HeadSince` is SuiteWard's own clock): the window is about the synchronization interval plus the job interval, a known limitation to be fixed with a per-head observation time.
  - **Transient read failure** (GitHub, tree): when the stored head equals the open head, keep the record. When the open head is new (no record, or the record holds another head), publish `in_progress`, reason `assessment-pending`, for the new head, so the new SHA never stays without a check.
  - **Lost publication.** When the current generation's message ended `failed` in the outbox, `Publish` re-sends the same state under the next generation.
  - Publish through `checks.Store.Publish` with a `github_check` message.
- A store error fails the run after every repository was tried. The last error per repository is kept in memory and shown in `/status` (`github[].checks`).
- Memory is bounded (M1.10-M11). What the job remembers per pull request (unchanged heads, scope decisions, announced comments, runner files) is dropped for the pull requests of a repository that the observation read at the start of its run does not list as open; a repository whose observation could not be read keeps its entries, and a reopened pull request costs one more decision. The ETag cache of the REST client is bounded too ([GitHub contract](github.md)).

**Outbox kind `github_check`**
- Key `check:<repository id>:<pull>:<generation>`. A system row (no project or Suite).
- Payload (explicit tags, strict decoding): `repositoryId`, `repo`, `pull`, `headSha`, `generation`, `state`, `reason`, `reference` (optional), `tier` (effective), `version` (the canonical version, optional).
- The publisher:
  - reads `checks.Store.Current`; a record whose generation differs from the payload's means the message is stale, and it is dropped (success, nothing posted);
  - reads back the head's check runs named `SuiteWard / Contract` from this App; one whose `external_id` is `suiteward:<instance id>:<key>` means it was already posted;
  - otherwise posts the run: `success` and `failure` as `completed` with that conclusion, `in_progress` as `in_progress`; the output title and summary come from the command-text package (bounded; the tier; the copyable `/suiteward approve <ref> <code>` when approval is missing; never a claim that tests ran);
  - since M1.13, when the payload names a revision (`reference`) and a canonical version (`version`), rebuilds the change report of that revision ([Change report (M1.13)](#change-report-m113-normative)) and states its numbers in the summary, without a link or a path, because a check summary is public. A report that cannot be built is logged and the check is posted without the numbers: the check is the gate of the merge and must not wait for them. A delivery whose context ended is the only error, and it is delivered again;
  - logs a 422 for a head that no longer exists and treats it as delivered.

### Merges and integrity jobs (M1.7)

Both jobs are registered only with the GitHub settings. Both are excluded from the `/status` job counts.

**`github_merges`** (D-INTEGRATION, ADR 0014)
- Periodic every 30 s with `RunOnStart`, `MaxAttempts` 1, unique while unfinished.
- It is the only writer of merge facts and the only caller of `governance.PromoteMerge`.
- For each enrolled and configured repository whose Suite has a canonical (since M1.7b: whose Suite exists, see "Bootstrap from a PR"):
  1. **Closed listing.** `Reader.ClosedPulls`, then `githubsync.Store.RecordClosed`.
  2. **Resolution.** For each pull from `AwaitingResolution`, call `Reader.Pull`:
     - `open`: nothing; the next cycle upserts it.
     - Closed and not merged: `RecordResolution`.
     - Merged with a `merge_commit_sha`: `RecordResolution`.
     - Merged without one: retry on later runs. After 30 minutes since `not_open_since`, record it without the SHA.
     - `ErrStaleResolution` is skipped.
     - A failed read leaves the pull unresolved.
  3. **Outcomes.** Only when step 2 left no pull unresolved, other than pulls GitHub still answers `open` (an earlier merge waiting for its SHA or for a read must be decided first). For each pull from `Merged` that is not `Processed`, in order: if `MergeOutcomeOf` already has an outcome, `MarkProcessed` and continue. Otherwise build a `MergeRequest`:
     - carrier `github-pr:<repository id>/<pull>`, source the merge commit, target `branch:<base ref>`, `ObservedAt` the `not_open_since`;
     - set `Refusal` without reading anything for:
       - `fork-unsupported`: `HeadRepo` is not this repository (including `(deleted)`); the value comes from `Reader.Pull`, so it is never unobserved here;
       - `shared-merge-commit`: another pull from `Merged`, processed or not, has the same non-empty `merge_commit_sha`;
       - `integration-unreadable`: no SHA;
       - `integrity-failed`: the Suite's integrity verdict is `failed` for the current version;
     - otherwise read the tree with `Reader.Tree(repo, sha)`. A 404, a 422 or a truncated tree is retried until 30 minutes after `not_open_since`, then becomes `integration-unreadable`. The same bound applies to a blob of the tree that answers 404 or an invalid response while `PromoteMerge` reads it: a permanently unreadable file must not stall the repository. Other failures (rate limit, budget, 5xx, transport) stay transient and unbounded;
     - call `PromoteMerge`, then `MarkProcessed`.
- **Order and stopping.** A pull that cannot be decided yet stops that repository's outcomes for this run. That covers a transient read, a store error and the bound not yet reached. A later merge's tree contains the earlier one, so outcomes are never decided out of order. Other repositories continue.
- **Errors.** A store error fails the run after every repository was tried. The last problem per repository is kept in memory and shown in `/status` as `github[].merges`, a fixed code: `read-failed`, `waiting` (bound not reached), `assessment-conflict` or `failed`. It is cleared by a run that finds nothing wrong. GitHub text is never stored or logged.
- **Comments after closing.** Comments on a PR that is no longer open are not observed (M1.4). A `/suiteward` command posted after the merge is therefore never processed, and promotion uses only consent recorded before it.

**Outbox kind `github_comment`, key `merge:<repository id>:<pull>`** (one outcome comment per merged PR)
- Written by `PromoteMerge` only for `promoted`, `corrected` and `blocked`.
- Payload: `repositoryId`, `repo`, `pull`, `kind`, `reason` (blocked only), `source`, `version`, `corrects` (corrected only), `reference` (optional), and `tier` (effective).
- The publisher renders it with the command-text package, like the other `github_comment` keys (marker, idempotent post).
  - **promoted or corrected:** the new version and the merge commit; for a correction, the version corrected. It says that SuiteWard checked the approval and the integrity of the protected files at that commit and did not run the tests.
  - **blocked:** that the canonical stays at `version`, the reason in plain words, and the recovery. The recovery is a new PR that restores the protected files of `version`, or one that carries the integrated files for a new exact approval. There is no reset command.
- `splitKey` accepts the `merge` kind. The comment is bounded like the bootstrap summary.

**`canonical_integrity`** (M1.7-C)
- Periodic every hour with `RunOnStart`, `MaxAttempts` 1, unique while unfinished.
- For each enrolled project whose Suite has a canonical:
  - read the current version's manifest in one `Do`;
  - call `filesystem.Store.Verify` for every entry;
  - record the verdict with `integrity.Store.Record`.
- A failed verdict is logged at Error, with the project and version only.
- **Effect on checks.** While the Suite's verdict is `failed` for its current version, `github_checks` publishes `failure` with reason `integrity-failed` on every open PR of that repository, before any assessment. It also refuses merges (`integrity-failed`). `unavailable` changes no check.

**Changes to `github_checks` (M1.6b)**
- **Fork PRs.** A PR whose `HeadRepo` is observed and not the repository (including `(deleted)`) gets `in_progress`, reason `fork-unsupported`. `AssessHead` is not called, so a fork PR never gets a proposal. An unobserved `HeadRepo` (a row from before M1.7, until its first cycle) is treated as before M1.7.
- **Reason list.** The reasons gain `fork-unsupported` (`in_progress`). `integrity-failed` (`failure`) becomes reachable through the integrity verdict.
- **Lost publication.** A lost publication is re-sent no sooner than 15 minutes after the failed message finished (persistence contract).

**`/status`** (since M1.7): each `github` entry also has:
- `merges` (above);
- `integrity`: the verdict of the Suite's current version (`passed`, `failed`, `unavailable`), absent before the first verification or for another version.

The job counts also exclude `github_merges` and `canonical_integrity`.

### Bootstrap from a PR and the revision comment (M1.7b)

**Who writes what.** Superseded by [Explicit bootstrap: propose and skip](#explicit-bootstrap-propose-and-skip-m17b-rework-2026-10-16).
- The bootstrap job writes only `branch` baselines.
- The checks job writes the `pull` bootstrap proposal and every change-proposal revision.
- The merges job decides V1 from a merged bootstrap PR.

**`github_bootstrap` (changes).** Superseded by [Explicit bootstrap: propose and skip](#explicit-bootstrap-propose-and-skip-m17b-rework-2026-10-16).
- **It reads only the latest `OriginBranch` proposal.** It never promotes an `OriginPull` revision, and the domain refuses that anyway.
- **When the default branch has protected files, it proposes a `branch` baseline `P<n+1>`** on the lowest open non-fork PR in two cases:
  - there is no `branch` proposal yet;
  - the latest proposal is `OriginPull`, which it supersedes.

  Otherwise its rules are unchanged. `awaiting-initial-contract` still means the default branch has no protected files (the job checks for an open PR first, so a repository with none shows `no-open-pull-request`).
- **It never supersedes a `pull` proposal whose carrier merged before the merges job decided that merge.** While the latest proposal is `OriginPull`, its carrier PR is no longer open, and the merges job has recorded no outcome for that carrier (`MergeOutcomeOf`), the bootstrap job stops with `awaiting-merge-decision`. The carrier's own merge is what put the protected files on the default branch, and it decides V1 itself.
  - It supersedes only in two cases: the carrier is still open (the files came from elsewhere), or the carrier closed without a merge or its merge outcome is recorded and is not a promotion.

**`github_checks` without a canonical.** Steps 1–4 are superseded by [Explicit bootstrap: propose and skip](#explicit-bootstrap-propose-and-skip-m17b-rework-2026-10-16).

1. **A repository whose Suite does not exist yet** (`Do` answers `ErrNotFound`, before the bootstrap job's first run) gets `in_progress` / `bootstrap-awaiting` on every open PR (`bootstrap-pending` before the M1.7b rework). The checks job reads no default branch since that rework: the `github_bootstrap` job does, and a PR's check does not wait for it.
2. **Choose the bootstrap carrier,** deterministically.
   - First, the lowest-numbered open non-fork PR for which `BaselineProposalFor(github-pr:<repository id>/<pull>)` finds a proposal.
   - When no open PR has one, the lowest-numbered open non-fork PR whose head has protected files.
   - **Superseded proposals.** In the design this replaced, a proposal superseded by the branch path was never chosen. "Superseded" has no store marker. The proposal stays, and if its PR merges with a tree that matches the approved revision exactly, `PromoteMerge` still promotes it, which is safe because an exact match means no drift. A later branch baseline then blocks with `CanonicalPresent`.

   Head inventories are read only while looking for a carrier, and each verdict is cached by head SHA.
   - **A carrier keeps its role while its PR is open,** even when a later push removes its protected files (its check is then `bootstrap-awaiting`). Another PR with protected files waits until the owner closes it. Changing that is a policy decision, not taken here.
   - **A default branch whose tree GitHub truncates** is treated like one with protected files: every PR gets `bootstrap-awaiting`, cached by head SHA. The bootstrap job reports the same case as `invalid-source`.
3. **The carrier's head.** First the M1.6b fast path: same head, same Suite revision and same latest owner command keep the record without a read (an approval bumps the Suite revision, so readiness still changes on the next run). Otherwise call `AssessBootstrapHead`, with a `bootstrap-summary` message for a committed revision.
   - Not ready: `in_progress`, `approval-missing`, with the reference.
   - Ready: `success`, `bootstrap-ready`.
   - An invalid source is `failure` / `invalid-source`. A truncated tree is `source-unavailable`.
   - The owner-command `failure` applies to the carrier as it does to change PRs.
   - `ErrCanonicalMoved` keeps the record, and the next run retries.
4. **Every other open PR** gets `bootstrap-awaiting`. A fork PR keeps `fork-unsupported`.

**`bootstrap-ready` check text.**
- Title: "Bootstrap ready for integration".
- The summary covers:
  - the approved revision;
  - that this head was assessed;
  - that SuiteWard will promote the first canonical version from the exact merge commit after the merge;
  - that no canonical exists yet;
  - the tier.
- Never claim that tests ran.

**`github_merges` (change).**
- It processes every enrolled and configured repository whose Suite exists, with or without a canonical.
- Without a canonical, its steps are unchanged and `PromoteMerge` applies the rules of the persistence contract.
- An earlier merge with no canonical is recorded `before-canonical` and never stops the queue.

**Outbox `github_comment`, key `bootstrap-summary:<repository id>:P<n>-R<m>` (change).**
- **Every bootstrap revision gets one, R2 and later included.**
- **The payload gains `source`** (`branch` or `pull`). For `pull` it also gains the PR head SHA as `origin`, and omits `branch`.
- **The `pull` text** says:
  - the revision is the exact head of this PR;
  - approving it lets the PR merge;
  - V1 is promoted only from the exact merge commit after the merge;
  - a later protected push needs a new approval.

**Outbox `github_comment`, key `merge:<repository id>:<pull>` (change).**
- **`promoted` with version `V1` from a merged bootstrap PR** names the merge commit and says this is the first canonical version.
- **A `blocked` outcome without a version** says that SuiteWard has no canonical contract yet and gives the reason. For recovery, it says that the default branch now holds the merged files, so SuiteWard proposes the default branch's contract for approval on an open PR (the branch path).

**Outbox `github_comment`, key `revision:<repository id>:P<n>-R<m>` (new).**
- **Written by `AssessHead`** through `HeadRequest.Revision`, for each new change-proposal revision.
- **Payload:**
  - `repositoryId`, `repo`, `pull`, `reference`;
  - `previous` (optional);
  - `canonical`, the base version;
  - `added`, `modified` and `removed`: at most 20 each, every entry cut to 150 bytes with `…`, like the bootstrap summary;
  - `addedTotal`, `modifiedTotal`, `removedTotal`;
  - `reduced`;
  - `tier`.
- **The text** lists the changes inside code fences with control characters escaped, then the copyable `/suiteward approve P<n>-R<m> <code>`, and, when `previous` is set, "This supersedes `P<n>-R<m-1>`". Since M1.13 the lists give way to the numbers of the change report when they can be counted, and the text states them at delivery, so the payload is unchanged and holds none of them ([Change report (M1.13)](#change-report-m113-normative)).
- It never claims that tests ran, and stays under the comment limit with a worst-case test.
- `splitKey` accepts the kinds `revision` and `bootstrap-summary` with their three parts.

### Explicit bootstrap: propose and skip (M1.7b rework, 2026-10-16)

This replaces these parts of the section above: "Who writes what", the `github_bootstrap` changes, and `github_checks` without a canonical (steps 1–4). The texts, keys and merges-job rules of the section above still apply unless changed here.

**Who writes what.**
- The commands job writes every bootstrap proposal, of both kinds, on `/suiteward propose`.
- The checks job assesses the carrier's head but writes no proposal.
- The bootstrap job only promotes an approved `branch` baseline.
- The merges job decides V1 from the merged `pull` carrier.

**`github_commands`: `/suiteward propose`.** Owner-authored, no code. It goes through the D-COMMANDS checks first (before enrollment, not a command, author, edited first). Then, in order:
1. **Refusals**, recorded `propose-refused` with ack `ack:<repository id>:<comment id>`:
   - `canonical-present`: the Suite has a canonical;
   - `fork`: the PR is a fork, by `HeadRepo` as in M1.7.
   - `not-open`: the PR is no longer open in the synchronized state.
   - `base-not-target`: the PR's base branch is not the Suite's target (the default branch), so its merge could never decide V1.
2. **Pick the source.** Read the default branch head (`Reader.BranchHead`) and its tree.
   - The tree has `.suiteward.yml`: the source is that pinned commit, with origin kind `branch`.
   - Otherwise, read the PR head tree. If it has `.suiteward.yml`, the source is the PR head, with origin kind `pull`.
   - Otherwise, `propose-refused` with reason `declaration-missing`. The ack payload carries:
     - `suggestion`: up to 20 include patterns, from the test-like paths of the PR head tree (or of the default branch when the PR has none). Since M1.9 it is built by `inventory.Suggest` ([Scope discovery (M1.9)](#scope-discovery-m19)); the M1.7b rule of one pattern per directory is gone.
     - `branch`: the PR's head ref, for the link.

     When no test-like path is found, `suggestion` is empty. The text then gives a minimal `version: 1` with an `include:` placeholder to fill in, and the link carries that minimal file. The text shows the YAML and the link `https://github.com/<repo>/new/<path-escaped branch>?filename=.suiteward.yml&value=<URL-encoded YAML>` (verified 2026-10-16 on SuiteWardQ2: GitHub fills both the name and the content).
3. **Invalid or empty source.**
   - A source that cannot be read as a contract gives `propose-refused`, reason `invalid-source`: an invalid `.suiteward.yml`, a protected path that is a link, a truncated tree, or (M1.10-M10) a path of the head or of the default branch that is not a valid path, which is refused too when no declaration exists and the suggestion would have been built from it, with no `base` and no link. These are the cases the bootstrap job reports as `invalid-source`. They are deterministic, so they are refused rather than retried (clarified 2026-10-16). When the source was read but failed to build, the ack also carries `base` (`pull` or `default-branch`) and `branch` (optional), and the text adds that, if the declaration is the problem, GitHub's editor for `.suiteward.yml` on that branch fixes it (amended 2026-10-19). A source that could not be read at all gets no link.
   - An empty inventory gives `propose-refused`, reason `empty-inventory`. A source that has `.suiteward.yml` always covers that file, so this cannot happen. The job treats it as an invalid state, an error, and the reason stays reserved.
4. **Idempotent reuse.** If the Suite's latest bootstrap proposal is carried by this PR, and its current revision has the same origin and coverage as the source, reuse it: no new proposal. Record `proposed` with a short acknowledgment, "already proposed as `P<n>-R<m>`". This covers a retry after a failure between the proposal and the ledger write, and a repeated `propose` on an unchanged PR.
5. **Otherwise propose.** Call `ProposeBaseline`:
   - `P<n+1>-R1`, with `n` the Suite's highest proposal number;
   - the carrier is this PR;
   - the origin is the source commit, with the kind from step 2;
   - with the `bootstrap-summary` message as the acknowledgment.

   Record `proposed`.
6. **Errors.** Only a transient GitHub read or store error is an error of this candidate. It follows the D-COMMANDS retry rule: the run stops, and after 5 attempts the candidate is recorded `failed`. A `propose` recorded `failed` gets an acknowledgment that tells the owner to comment `/suiteward propose` again.
7. **Known limit.** The reads run inside the single serialized commands job and its `JOB_TIMEOUT`: the default-branch tree, possibly the PR tree, and one blob per protected file. At about 0.37 s per request (S1), about 150 protected files fill one run of the default 1 minute. A larger source times out, delays every command behind it for 5 runs, and then ends `failed`, with its acknowledgment. Raising `JOB_TIMEOUT` or moving the reads out of the queue comes in M1.7c or later.

**`github_commands`: `/suiteward skip <code>`.** Owner-authored. The code is checked and consumed as for `approve`, with the same dispositions for a bad code.
- **Refusals**, recorded `skip-refused` with an ack:
  - `canonical-present`;
  - `fork`;
  - `carrier`: the PR carries the Suite's latest bootstrap proposal.
- **Otherwise** record `skipped` with an ack. The ack says the PR may merge before the first contract and that nothing was assessed.

**`github_checks` before V1**, for each open PR of a repository whose Suite exists and has no canonical. A repository with no Suite gives `bootstrap-awaiting` on every PR.
1. **Fork:** `in_progress`, `fork-unsupported`.
2. **The carrier.** The carrier is the PR carrying the Suite's latest bootstrap proposal (`Tx.LatestBootstrapProposal`).
   - **A `pull` proposal.** After the M1.6b fast path, call `AssessBootstrapHead` on the head:
     - cancelled (the coverage differs from the proposal's current revision): `in_progress`, `bootstrap-awaiting`, plus one comment `bootstrap-cancelled:<repository id>:P<n>-R<m>` saying the protected files changed and to comment `/suiteward propose` again. A later push that restores the exact coverage makes the revision current again, and that is safe.
     - otherwise: `in_progress`, `approval-missing` with the reference, or `success`, `bootstrap-ready`.
   - **A `branch` proposal.** The check is `in_progress`, `approval-missing`, with the reference, until the bootstrap job promotes V1. Right after the approval it may stay so for up to one bootstrap period (about 60 s), until the promotion. That is accepted: V1 then exists, and the PR is assessed as a change.
3. **Skipped.** A `skipped` entry exists for this pull under the current enrollment (`commands.Store.Skipped`): `success`, `bootstrap-skipped`. This is read on every run for non-carrier PRs and is never behind the fast path: that path keys on the latest owner command, which ignores `skip`.
4. **Otherwise:** `in_progress`, `bootstrap-awaiting`, plus one comment `bootstrap-notice:<repository id>:<pull>` saying how to start (`/suiteward propose`) or skip (`/suiteward skip <code>`).

Other rules:
- The owner-command `failure` comes only from a wrong code (`invalid-code` or `replayed-code`), whatever the verb, so a `skip` with a wrong code sets it too, on an awaiting or skipped pull request as well (amended 2026-10-19, D-COMMANDS). Only a processed command that is not rejected clears it, or a new head. `proposed`, `propose-refused`, `skipped`, `skip-refused` and every other rejected disposition never set it or clear it.
- The automatic carrier choice, the default-branch plan and its cache are removed.
- **One cancelled comment per revision.** The `bootstrap-cancelled` key names the revision, so if a push restores the coverage and a later push breaks it again, the check returns to `bootstrap-awaiting` with no second comment.
- **Superseded 2026-10-19 (D-COMMANDS, "A wrong command has no effect").** Every other rejected disposition, and a processed command with a rejected consent result, leaves the check unchanged. Only `invalid-code` and `replayed-code` set `owner-command-rejected`, whatever the verb. The ledger does not record the verb, so a wrong code on a `skip` counts like one on an `approve`.

**`github_bootstrap` (promote only).**
- It still creates the Suite on `ErrNotFound`, unchanged from M1.6a step 1. A `propose` that comes before that keeps today's `no-suite` disposition and its acknowledgment.
- With no canonical, when the Suite's latest bootstrap proposal is `branch` and the owner approved its current revision, it re-reads the origin, records the assessment and calls `Bootstrap`, as before.
- It proposes nothing.
- Its `/status` codes are `read-failed`, `repository-changed`, `origin-not-passed`, `assessment-conflict`, `blocked-<reason>` and `failed`. These were removed: `no-open-pull-request`, `awaiting-initial-contract`, `awaiting-merge-decision`, and `invalid-source`. An unreadable origin is assessed as unavailable and stops at `origin-not-passed`.

**`github_merges`.** `PromoteMerge` decides V1 only for the carrier of the Suite's latest bootstrap proposal, and only when that proposal is `pull`. Every other merge before V1 is `before-canonical`.

**Check texts.**
- `bootstrap-awaiting`: "SuiteWard has no canonical contract yet." It says how to start (`/suiteward propose` on the PR that should carry the first contract) and how to skip (`/suiteward skip <code>`). It never names an approve command.
- `bootstrap-skipped` (`success`): "Skipped by the owner before the first contract." It says that no contract exists and nothing was assessed.
- `bootstrap-pending` is replaced by `bootstrap-awaiting`.

**Comment keys.** `bootstrap-notice:<repository id>:<pull>` and `bootstrap-cancelled:<repository id>:P<n>-R<m>` are new. `splitKey` accepts them. `ack:` payloads gain the dispositions `proposed` (not used: a proposal's ack is its `bootstrap-summary`), `propose-refused` (`reason`: `canonical-present`, `fork`, `not-open`, `base-not-target`, `declaration-missing`, `invalid-source` or `empty-inventory`; plus `suggestion` and `branch` for `declaration-missing`), `skipped` and `skip-refused` (`reason`).

### Scope discovery (M1.9)

[D-SCOPE-DISCOVERY](../plan/decisions.md#d-scope-discovery-suggesting-and-widening-the-scope) decides the rules. This section changes the `declaration-missing` reply above, the revision comment, and the command list.

**Suggestion.** `inventory.Suggest(entries)` replaces `SuggestScope`. It returns the following, from the paths of `File` entries only:
- `include`: the test-file patterns and the test-directory patterns of D-SCOPE-DISCOVERY, each emitted only when it matches a file, and never under a vendored directory;
- `runner`: the runner files found, plus the higher-priority files of every runner whose files were found. A higher-priority pattern may match nothing;
- `mixed`: the mixed files found (at most 20 paths);
- `cut`: true when the limit of 20 patterns, include and runner together, dropped some.

The order is deterministic: directory patterns first, then file patterns, each group sorted. The runner list is sorted too. The limit of 20 is shared (see below): the runner patterns keep their room, and the include patterns, in that order, fill the rest.

**Room for the missing runner patterns** (M1.10-M10, decision 16). Every suggestion keeps the runner patterns ahead of the include patterns inside the limit of 20: the runner patterns (at most 20, as 13 runner families bring up to 25) take their room first, and the include patterns fill what is left, so the total stays at 20 or fewer and `cut` is set whenever anything was dropped. With a base declaration (`SuggestAgainst`, `Suggestion.WithRunner`) the runner patterns are the ones the base lacks, because the others are left out before the limit. With no base, before the first contract (the `declaration-missing` reply of a `suggest`, the refusal of a `propose` that finds no declaration, and the head's mixed files and submodules), every runner pattern found is missing, so all of them keep their room. `MissingRunner` reads the runner patterns before the limit, so 20 or more include patterns never hide a missing runner name. Before this, 20 include patterns left no room and the runner name was dropped without a word.

**The `declaration-missing` reply.** The ack payload keeps `suggestion` (the include list) and `branch`, and adds `runner`, `mixed`, `cut` and `submodules`, all optional, so a payload written under `v0.1.0` still decodes.
- The text renders `include:` and `runner:`.
- When `cut` is set, it says that the list was cut at the limit and that the owner should review it.
- When `mixed` is not empty, it lists those files and says that they may hold test settings, and that SuiteWard does not suggest them.
- The editor link stays within the URL budget of the worst case: at most 8000 bytes, with 20 patterns of 100 bytes and the longest branch.

**Revision comment.** The `revision:` payload adds `reducedRules` (at most 20) and `reducedPaths` (at most 20, each cut to 150 bytes), plus `reducedPathsTotal`, all optional. When `reduced` is set, the text lists the rules and paths that the reduction removes, inside code fences. The payload goes through the 60 KiB guard (amended 2026-10-19, M1.9 release review): when it would not fit, `reducedRules` and `reducedPaths` are dropped and `reducedOmitted` (optional) is set, while `reducedPathsTotal`, the warning and every list of files stay. The text then says that the detail did not fit and asks the owner to compare `.suiteward.yml` with the canonical one before approving. The revision always exists.

**Test-like and uncovered.** `Suggest` and `Uncovered` share one classifier, so they always agree on what counts as test-like. Test-like means the include rules plus the runner file names. `Uncovered(entries, s)` returns the test-like file paths that `s` does not cover. For example, a new `conftest.py` outside the scope is reported. It never returns a path that matches an `exclude` of `s`, because an exclusion is the owner's choice and adding patterns cannot undo it. The **new patterns** for a set of uncovered paths are `Suggest` applied to those entries: its include list and its runner list.

**Outbox `github_comment`, key `scope-notice:<repository id>:<pull>:<notice digest>` (new; the digest amended in M1.10-M10, decision 16).**
- **When it is written.** For an open, non-fork PR whose Suite has a canonical, the checks job follows these steps.
  1. It takes the uncovered paths of the head tree: `Uncovered` under the governing scope, minus the paths covered by the head's own scope when its `.suiteward.yml` is valid. A PR that already widens the scope to cover them gets no notice; that widening still needs approval.
  2. When there are none, it reads nothing more.
  3. When there are some, it reads the tree of the PR's base commit and keeps the paths absent from it, which are the ones this PR adds.
  4. When any remain, it builds the notice and computes the `notice digest`. If a message with that key already exists, it stops. The decision is remembered for each set of paths and each canonical, so the existing-key check comes before the base-tree read whenever the set is already known under the same canonical. Otherwise it writes the message.

  The **notice digest** is the SHA-256 hex digest of what the comment posts: the digest of the sorted paths joined by newlines, then the declaration shown, or else (in the form that lists the patterns alone) the patterns shown, each part with its length in front. The Suite's version is no part of it: a promotion would otherwise post a new comment on every open PR. A new canonical therefore posts a new notice only when it changes the shown declaration or patterns, and one that leaves the shown text identical posts none, because the key already exists.

  The result is one notice per distinct set of added uncovered paths and shown text, not one per push.
- **Payload:**
  - `repositoryId`, `repo`, `pull`, `head`, `branch`;
  - `paths`: at most 20, each cut to 150 bytes, plus `pathsTotal`;
  - `declaration`: the canonical declaration bytes with the new patterns added by `scope.AddPatterns`. It is never built from the PR head's file. It is at most 8192 bytes (`maxDeclaration`, which bounds the comment), and it holds no character that is not graphic besides newline (LF or CRLF) and tab (decision 7 of M1.10: a comment of the owner's `.suiteward.yml` could carry a bidi, zero-width or control character). A declaration that fails either rule is never shown: the payload then has no `declaration` and carries `addInclude` and `addRunner` instead (see [A declaration that cannot be shown](#a-declaration-that-cannot-be-shown-decision-7-of-m110)), so the notice is never skipped for it;
  - `addInclude` and `addRunner` (optional, only without `declaration`): the include and runner patterns that the declaration would add, at most 20 together, each cut to 150 bytes;
  - `mixed`;
  - `submodules` (optional): the submodules that `Suggest(head entries)` would reach, listed with a warning that their contents are not protected;
  - `cut` (optional): set when the new patterns were cut at the limit of 20, so the text says the declaration is partial and asks the owner to review it.
- **The text:**
  - it lists the paths and says they are not protected by the current contract;
  - it gives the full `declaration` in a fenced block (GitHub shows a copy button) and the link `https://github.com/<repo>/edit/<path-escaped branch>/.suiteward.yml`. Without a declaration it lists the patterns to add to each list as lines to copy, and the same link;
  - it says that committing it creates a protected change for the owner to approve, and that nothing is protected until then;
  - it never changes the check and never claims tests ran.
- `splitKey` accepts the kind with its four parts.

#### A declaration that cannot be shown (decision 7 of M1.10)

A declaration that holds a character that is not graphic besides newline (LF or CRLF) and tab, or is over 8192 bytes, is never shown. The comment is never lost for it: each comment that quotes a declaration has a form with the missing patterns alone, as lines to copy in a fence, and the same editor link. The job decides this by asking `commandtext` whether it would show the declaration, and stores the payload in the degraded form, so the publisher never refuses it for this reason.
- **`scope-notice`** (the checks job): no `declaration`; `addInclude` and `addRunner` list the patterns, under "Patterns missing from the `include` list" and the same for `runner`, and the editor step says to add them to the lists. An oversize declaration degrades the same way; the job no longer skips the notice.
- **`suggest`, the `scope-notice` reply**: no `declaration`; the reply keeps its lists of missing patterns (`addInclude`, `addRunner`) and drops the declaration paragraph. Its next steps say "these patterns" where they said "this declaration". The same holds for a declaration over the size limit.
- **`runner-uncovered`**: the protect choice lists the runner patterns (`protectPatterns`); the ignore choice is offered only when its own declaration can be shown, and the text then says that a declaration that cannot be shown is not offered. When neither can be shown, the comment lists the patterns alone and the editor step says to add them.
- **`invalid-source`** (kind `link`): the comment shows the one-line pattern, as before.
- **The commandtext validators** accept an empty declaration in these forms only when the patterns are given. The checks job's notice and the runner comment refuse a declaration and its patterns together; the `/suiteward suggest` reply carries both when the declaration can be shown.

**Outbox `github_comment`, key `scope-review:<repository id>:<pull>:<review digest>` (new; the digest amended in M1.10-M10, decision 16).**
- **When it is written.** Only together with a `scope-notice`. `Reader.PullFiles` must show `.suiteward.yml` among the PR's changed files, the head's file must be a valid declaration in block style, and a hunk must contain an item line of the `include:` list, the `runner:` list, or both.
- **Payload:** `repositoryId`, `repo`, `pull`, `head`, and `suggestions`: one or two entries `{list, line, anchor, patterns, quoting}`, one per list (`include` or `runner`). `quoting` (`plain`, `single` or `double`; decision 9 of M1.10) is the quoting that `scope.AddPatterns` writes for the head's list. It is required, except for a list that the block creates (`newList`), which is plain and has none; a payload without it is refused. The marker of each posted comment is the key followed by `:include` or `:runner`.
- **What it posts.** One review comment (`POST /repos/{repo}/pulls/{pull}/comments`, `commit_id` = head, `path` = `.suiteward.yml`, `side` = `RIGHT`) per list that has such a line, on that line.
  - The body is a `suggestion` block holding that line followed by the list's new patterns, at the same indentation and in the quoting of the list: the quoting that every item of the list shares, plain when they differ (decision 9 of M1.10). A list that the block creates is plain.
  - It leaves out the patterns that the head's file already lists, so the result never holds a duplicate.
  - It posts nothing for a list whose new patterns are all present.
  - Patterns of a list with no line inside a hunk stay only in the notice's copyable block.
  - A flow-style list (`include: [a, b]`) gets no review suggestion.
- **The review digest** is the SHA-256 hex digest of the head's `.suiteward.yml` bytes, a newline and the suggestions (list, line, anchor, patterns, quoting) as JSON: what the review posts, and nothing else. Two suggestions that say the same on the same file share a key, whichever paths, head or command produced them, so the checks job and `suggest` never post one suggestion twice under two keys. A different suggestion, or the same one on a changed file, is a new review. The same function keys the `runner-uncovered` review, whose lists are `protect` and `ignore`.
- **Where the lines come from.** This is the only place that starts from the PR head's file ([D-SCOPE-DISCOVERY](../plan/decisions.md#d-scope-discovery-suggesting-and-widening-the-scope)).
- **Dedup** uses `Writer.ReviewCommentsByAuthor` and the marker.
- **A 422** from GitHub (the line is no longer in the diff) completes the message without retry, and the job logs the `kind` and `status` of the failure.

**A head path that is not a valid path** (amended 2026-10-19, M1.9 release review): the head entries are validated once before any discovery, as for the inventory. Such a head is `invalid-source` for the checks job, which decides it once, posts no notice and fails no job, and for `/suiteward suggest`, which answers `suggest-refused` / `invalid-source`. Before the first contract with no declaration anywhere, `suggest` validates the paths of the head and of the default branch, whose tree is the fallback of the suggestion (M1.10-M10), and answers `invalid-source` instead of `declaration-missing`, with the same text as with a canonical: the refusal names no file and carries no editor link.

**Keys after the v0.1.2 upgrade, and a read that fails after a restart** (M1.10-M10, decision 16).
- The keys of the `scope-notice`, of the `runner-uncovered` comment and of every `scope-review` changed format (see the digests above). After the upgrade, an open PR that still warrants a notice, a runner comment or a review gets it once more, under its new key; the release notes of v0.1.2 say so. Nothing is migrated.
- A notice or a runner comment and its review are written one after the other, each in its own unit of work, the comment first, and a key that exists is skipped. An existing review (the same suggestion on the same file, from the other producer or under the previous canonical) never keeps the new comment from being written; a failed write is retried and skips what was written.
- L2 is kept, not fixed. The checks job remembers in memory which head it found runner files for. After a restart, one failed read of `PullFiles` leaves a green head `in_progress` (`assessment-pending`) until the next run, 30 seconds later. It never yields a false `success`, and no new state is stored.

**Command `/suiteward suggest`.** It is owner only and takes no code. The parser accepts the verb without argument. An extra token is `ExtraText`, as for `propose`.
- **`suggest-refused`** is recorded with a short ack that names the reason and quotes no repository text. It is never retried. The reasons are:
  - `fork` or `not-open`, as for `propose`;
  - `invalid-source`: the tree is invalid or truncated, the canonical has no declaration, or the reply cannot be rendered (an unusable branch name, for example);
  - `declaration-too-large` is no longer written. A suggested declaration over 8192 bytes, or a base declaration that holds a character that is not graphic besides newline and tab, is not refused: the reply lists the patterns alone (see [A declaration that cannot be shown](#a-declaration-that-cannot-be-shown-decision-7-of-m110)). An acknowledgment of that reason, queued by v0.1.1, is refused.

  A canonical that fails its integrity check gets no reply: the command is retried, then recorded `failed`, like any other failure.
- Otherwise it records **`suggested`**, and its ack replies to the command. The reply compares the head with a **base declaration**:
  - **after V1**, the canonical declaration;
  - **before V1**, the default branch's `.suiteward.yml` if it has one, else the head's own. This is the same order `propose` uses to choose its source. An invalid base file is refused as `invalid-source`. This grants nothing: before V1 only `propose` binds.
  - **After V1 the reply compares strictly with the canonical.** A path that the head's own file covers but the canonical does not is still reported. Only the checks job's unsolicited notice is silenced by the head's file.
  - **Before V1 with no declaration anywhere**, there is nothing to compare, and the reason is `declaration-missing`: the suggested declaration for that PR, built from the head tree or else the default branch.

  With a base, the reason is one of:
  - **`scope-notice`:** a reply with the head's test-like paths that the base does not cover. These are not filtered by the PR's base commit, and the base's excludes are respected. The ack fields are:
    - `paths`, `pathsTotal`;
    - `declaration`: the base bytes plus the new patterns, from `scope.AddPatterns`;
    - `cut`, `mixed`, `submodules`;
    - `base`: `pull`, `default-branch` or `contract`;
    - `addInclude` and `addRunner`: the missing patterns, at most 20 in all.

    When the base is the default branch's file, the change belongs on the default branch. The reply then has no review suggestion. Its link opens GitHub's editor for `.suiteward.yml` on the default branch (amended 2026-10-19, D-SCOPE-DISCOVERY): the ack adds `defaultBranch` (optional), the Suite's integration target, and the text says that GitHub offers a new branch and a pull request when the default branch is protected. It gives the updated file to commit or merge there, and then `/suiteward propose`. An ack queued without `defaultBranch` renders without the link.
    - The text names the base: "this pull request's `.suiteward.yml`", "the default branch's `.suiteward.yml`" or "the current contract". It shows the missing patterns and the updated file.
    - The text gives the next step as a copyable command:
      - base `pull`: commit the change on this branch, then `/suiteward propose`;
      - base `default-branch`: the first contract is proposed from the default branch's file, so the change must reach the default branch first, then `/suiteward propose`;
      - after V1: approve the new revision SuiteWard posts.
    - The command also writes a **`scope-review`** under the same rules as the checks job: the PR's diff shows `.suiteward.yml` in block style with a list item line inside a hunk, the lines are inserted into the head's file, and patterns already listed are skipped. The key is the same `scope-review:<repository id>:<pull>:<review digest>`, so a notice and a command that post the same suggestion on the same file post one review.
    - **The missing patterns are of two kinds** (amended 2026-10-18, live run 3):
      - the patterns of `Suggest(uncovered entries)`, for the test-like files the base does not cover, without the runner patterns the base lists or covers (amended 2026-10-19, A3 pull request 7: a Python test file brings the pytest family, and the base may hold it already). They are left out before the 20-pattern limit applies;
      - the runner patterns of `Suggest(head entries)` that the base neither lists nor covers.

      A runner name can match no file yet, so a missing runner name is reported even when no file is uncovered. The text explains that runner names matter before their files exist. The checks job's unsolicited notice keeps the first kind only: it reports what a pull request adds.
  - **`nothing-uncovered`:** a short ack that names the base (`base` is required) and says that it covers every test-like path of the head. It is sent only when neither kind of missing pattern exists.
  - **An invalid base file** is refused as `invalid-source`. The ack carries `base` (`pull` or `default-branch`) and `branch` (optional, the branch that holds that file). The text names the file ("this pull request's `.suiteward.yml` is not a valid declaration") without quoting it, and links to the editor for it.
  - **Excludes and the review key.**
    - A runner name without glob characters that an `exclude` of the base removes is not reported. A glob runner pattern counts as handled when the base lists the same string in `include`, `runner` or `exclude`. Either way, the exclusion is the owner's choice.
    - The command's `scope-review` key is the review digest above, from the suggestions it posts. It matches the checks job's key whenever the suggestions are the same, including the runner patterns that no uncovered path explains.
    - **The review key also digests the head's `.suiteward.yml` bytes** (amended 2026-10-18, live run 4). A review is anchored to the lines of one version of the file, and GitHub marks it outdated after a change. So a later `suggest` after the file changed posts a fresh review, while a repeat on the same file does not.
    - The added list items follow the quoting style of the list's existing items: the quoting that every item shares, plain when they differ (decision 9 of M1.10; the review payload carries it as `quoting`).

  **Payload guard.** A suggest ack or a scope notice whose JSON payload would exceed 60 KiB is treated as not renderable, which can happen when a hostile repository has many escaped characters in its paths. A command is then refused as `invalid-source`. The checks job logs the notice as skipped and remembers the set.

  The `declared` ack field of v0.1.1 is gone: a payload that holds it does not decode and is refused. An upgrade from v0.1.1 or older goes through v0.1.3 first.
- Neither disposition is counted by `LatestOwnerCommand`. A `suggest` on a project with no Suite records `suggested` with the no-suite acknowledgment.

**Links and submodules** ([D-SCOPE](../plan/decisions.md#d-scope-declaration-and-source-inventory-semantics), amended 2026-10-17). `inventory`'s manifest decides a non-file entry as follows:
- **A symlink.** It is checked only if the scope covers it or may cover a path below it. Then its target is resolved in the same tree. The target is the link's blob, taken relative to the link's directory, and at most 8 links are followed. The target is resolved one component at a time, never by cleaning the text: `.` and empty components are skipped; `..` steps back one component, and stepping past the root leaves the target unresolved; every other component before the last must be a real directory of the tree, not a link, file or submodule. Intermediate links are not followed. If the target is a regular file, the link is unsafe only if the scope covers the link's path. Any other target (a directory, outside the repository, dangling, or a loop) keeps today's rule: unsafe.
- **An accepted link** (amended 2026-10-19): when an `exclude` pattern of the scope is the link's own path, anchored or with an interior slash, and matches that path exactly, the link is skipped, not read and not unsafe. The `invalid-source` comment of kind `link` adds this choice, with its declaration (the head's file plus the anchored path in `exclude`), and says that nothing reached through the link is protected and that the change is a reduction the owner approves.
- **A submodule.** It is never unsafe and never read. `Suggest` returns the submodules that its patterns would reach as `Submodules` (at most 20). Every reply that suggests a declaration warns that their contents are not protected.

**Check reason `runner-uncovered` (new, 2026-10-18; D-SCOPE-DISCOVERY).**
- **When it applies.** After V1, for an open, non-fork PR, the checks job takes the head's runner files: the paths matching a runner family name, not covered by the governing scope, not excluded and not vendored.
  - **If there are none,** nothing changes, and it makes no extra read.
  - **If there are some,** it reads `Reader.PullFiles` once per head and keeps the ones the PR adds, modifies or renames into place.
  - **If any remain,** the check is `in_progress` with reason `runner-uncovered` wherever it would otherwise be `success`.
  - **A head that is also `approval-missing`** stays `approval-missing` first. After the approval, it becomes `runner-uncovered`.
  - **Every other reason keeps precedence:** `invalid-source`, `integrity-failed`, `not-current` and the rest.
- **The summary** lists the runner files, at most 20, and gives the two ways out: protect them by committing the suggested declaration and approving it, or exclude them by adding the paths to `exclude` and approving it. It never claims tests ran.
- **The scope notice** for the same head is unchanged, and still carries the suggested declaration when it can be shown.
- **Two one-click choices** (owner, 2026-10-18). Each choice is a declaration to commit and approve:
  - **protect:** the base plus the runner family patterns;
  - **ignore:** the base plus the paths themselves in `exclude`, with the list created when it is missing.
  - **When the PR's diff shows `.suiteward.yml`** in block style, with lines a suggestion can anchor to, SuiteWard posts both as `scope-review` suggestions. Each one has its own marker suffix, `:protect` or `:ignore`.
  - **When it does not**, the `runner-uncovered` comment gives both full declarations to copy, with the edit link (each only when it can be shown).
  - **The comment** is one `github_comment` per head set and shown text, key `runner-uncovered:<repository id>:<pull>:<digest>` (the digest amended in M1.10-M10). The digest covers the paths, the head's `.suiteward.yml` bytes (the review is anchored to them) and what the comment shows: the two declarations, or the patterns of the protect choice. A new canonical that changes a shown declaration posts a new comment; one that leaves the shown text identical posts none. The checks job decides a head once for each canonical.
- **Resolution** is by the usual flow: the next head's assessment sees the changed scope.
- **Which scope decides "covered".** It is the head's own declaration. This is sound because a head can be `success` only when it is `unchanged` (its declaration bytes equal the canonical's) or `ready` (the owner approved that exact revision, its declaration included). An approved reduction that drops a runner pattern therefore blocks, which fails closed.
- **What counts as a runner file.** A runner file is a regular file or a symbolic link whose name is a runner-family name. A link counts too, so `conftest.py -> other.py` cannot slip past.
- **The ignore choice** excludes only the file itself: a path with no slash is written anchored, as `/conftest.py`. A path with `*`, `?`, `[` or `\`, or one that is no valid exclude pattern (a leading `!`), gets no ignore choice, because no exclude names exactly that file; and a choice whose declaration cannot be shown is dropped (it is over 8192 bytes or holds a character that is not graphic besides newline and tab, decision 7 of M1.10), so the comment offers the protect choice alone (amended 2026-10-19, M1.9 release review). The protect declaration, when it cannot be shown for the same reasons, is replaced by the runner patterns of the files that the canonical's runner list lacks, as lines to copy (`protectPatterns`, at most 30, in place of `protect`; all of the files' patterns when the canonical lists them all, which an approved reduction of the head's own declaration can cause); the comment is never skipped for it. A declaration that `scope.AddPatterns` or `scope.AddExcludes` cannot extend is an impossible state, because the canonical was verified and the patterns are constants, so it fails the job instead of dropping the comment (decision 5 of M1.10).
- **The copyable declarations** start from the canonical. For a head whose own approved declaration differs from the canonical, the review suggestions apply to the head's file, while pasting a copy block would revert those edits. The comment says so.

**Outbox `github_comment`, key `invalid-source:<repository id>:<pull>:<problem digest>` (new, 2026-10-18, live run 5).**
- **When it is written.** When the checks job publishes `failure` with reason `invalid-source` for an open, non-fork PR, it also writes one comment that explains the problem.
- **The problem** has three kinds:
  - **`declaration`:** the head's `.suiteward.yml` is not a valid declaration;
  - **`link`:** a symbolic link the scope covers or may cover below, whose target is not a regular file of the tree; the payload adds `path`, the link's path cut to 150 bytes, `pathCut` (optional, set when it was cut: the comment then offers no one-line pattern), and `declaration` (amended 2026-10-19, optional): the head's `.suiteward.yml` with the link's anchored path added to `exclude`, omitted when the head has no declaration, it cannot be built, it exceeds 8 KiB, or it holds a character that is not graphic besides newline and tab (decision 7 of M1.10: the comment then shows the pattern line alone, and the key's digest still covers the declaration, so a key never moves);
  - A truncated tree is `source-unavailable`, not `invalid-source`, so it gets no such comment.
- **The key.** The problem digest is the SHA-256 of the kind and the path; for a `link` that carries a `declaration` it also covers that declaration's SHA-256, so an edit of the head's `.suiteward.yml` posts a comment with a current one. A later push with the same problem posts nothing; a different problem posts a new comment.
- **The text:**
  - it names the problem and gives the next step:
    - fix the declaration;
    - remove the link or point it to a regular file; or accept it with its exact anchored path in `exclude` (an accepted link), with the warning that nothing reached through it is protected and that the change is a reduction the owner approves. The comment never suggests narrowing the include patterns;
  - the path goes inside a fence, escaped;
  - it quotes no file content except the head's own declaration of a `link` comment;
  - it says the canonical contract stays in force and the check stays `failure` until the next push fixes it;
  - it links to GitHub's editor for `.suiteward.yml` on the PR's branch, for fixing the declaration and for accepting a link (amended 2026-10-19, D-SCOPE-DISCOVERY). The payload adds `branch` (optional; the repository is already in every comment payload), and a message queued without it renders without the link. A head with no `.suiteward.yml` sets `noDeclaration`, and the accept choice then links to GitHub's new-file page instead. A `.suiteward.yml` that is a link or another non-regular entry gets no link, because the web editor cannot fix it, and neither does a branch that is not a valid ref name.
- **The check summary** for `invalid-source` no longer mentions submodules, which are outside the scope since the D-SCOPE amendment.

**Bot identity.** When the bot account cannot be resolved, the commands job logs the failure's `kind` and `status` (`readFailure`), never GitHub's text. This also covers a refused App JWT on `GET /app`.

**Logs of the checks job (M1.10).** A GitHub read that fails is logged by its `kind` and `status` through `logReadFailure`, never with the error's text; this includes the read of the head's declaration for an accepted link. A declaration that `scope.AddExcludes` cannot extend is logged with a fixed message and only whether it was an invalid declaration, because the error text quotes the head's file. A skipped review logs the reason of a diff that cannot be read, which names only the constant path `.suiteward.yml`.

### Change report (M1.13; normative)

Decided by the owner on 2026-10-22 ([D-M1.13](../plan/decisions.md#d-m113-macos-the-one-line-install-installer-screens-the-change-report-and-encrypted-backups)). A change report states what differs between the protected files of a proposal revision and those of the canonical version it expects: the files, the lines and the marker lines. It is a fact about bytes, computed by `internal/application/report` from the files of both sides, with no analysis by an agent. A marker line is a line to look at, never a verdict. Nothing of it goes to GitHub beyond the numbers of a comment and of a check summary, because the App has no Contents write.

**The manifest of a revision.** The database holds only the digest of the manifest that a revision proposes. The checks job gives `AssessHead` a `Revision` callback that stores the canonical encoding of that manifest in the artifact store under its own digest (`Put` is idempotent) before the revision commits. A manifest that cannot be stored leaves no revision: the assessment of that head fails. A revision recorded before M1.13 has no stored manifest, so no report can be built for it. The payload of the `revision:` message does not change and holds none of the report.

**Building a report.** `revisionReport` builds it, whenever it is asked to, so that the comment, the check summary and the page state the same numbers. It builds one report at a time in the process and waits for its turn until its context ends. It:
1. reads, in one unit of work, the revision's binding (the canonical version it expects and the digest of its own manifest) and the manifest of that canonical;
2. reads the proposed manifest from the store, and refuses bytes that are the encoding of another manifest than the one the revision names;
3. lists the files that differ in path order, and sizes each side without reading it. A file with a side over 1 MiB is not read: it is listed with its sizes, without a line count, as too large to compare, an added or removed file included. A revision whose other files would take more than 32 MiB to read, both sides together, is refused before any of them is read (`errReportTooLarge`);
4. reads the files that differ and builds the report with the report core, which bounds its work whatever the files are: 1 MiB and 20,000 lines per side, an edit distance of 1000 lines, 300 million steps for all the comparisons of one report, 4 MiB of hunks, 1000 listed markers, lines cut at 500 bytes and marker text at 200.

A revision that is not found, one that expects no canonical (a baseline), a canonical that is not found and a manifest that was never stored are `governance.ErrNotFound`. A file that is not in the store is an error that wraps `fs.ErrNotExist`.

**The revision comment** rebuilds the report when the publisher delivers it:
- When it can, the comment states the numbers and links the report. After the base version it holds, in this order: the sentence "What changed in the protected files. SuiteWard counts the changed lines and points out some of them as marker lines; it does not judge them."; the bullets `Files: <a> added, <m> modified, <r> removed`, `Lines: +<n>/−<m> (counted in <x> of <y> files)`, `Not shown line by line: <k> files (listed below)` when there are some, and `Marker lines seen: ...`; the changed files in a fence (`+N/−M`, status and path per row), the most changed lines first and then by path, at most 20, with the heading `(20 of N)` and `and K more` when there are more; the files without a line-by-line diff in a fence (reason and path), at most 10, with `and K more`; "Some details of the full report were left out to keep it small." when the report left some out; and the link. A path is shown with control characters escaped, and cut to 300 bytes with `… [shortened]` (`display` and `maxShown` in `internal/application/commandtext/render.go`).
- The markers are listed in a fixed order: removed assertion lines, added skip lines, added focus lines, removed test declarations, and removed test files, or `none`. When the revision reduces the scope, a removed file left the protected files and may still exist, so the comment says `removed from the protected files` and `test files removed from the protected files`.
- A file has no line-by-line diff, and is listed with its reason, when it is binary (`binary file`), too large (`too large to compare`), too different (`too different to compare`), not compared because the comparison work ran out (`not compared (work budget ran out)`), or because its diff was left out of the report's size budget (`diff left out (report size budget)`).
- The link is `<REPORT_BASE_URL>/reports/<owner>/<repo>/P<n>-R<m>`, with the owner and the name path-escaped, and it is set only when `REPORT_BASE_URL` is. The renderer refuses any other link than that of a loopback report of this revision. It is followed by "This link opens on the machine that runs this SuiteWard instance; on the isolated tier, through a port forward of the HTTP port."
- When the report cannot be built, whatever the cause (a revision without a stored manifest, a file missing from the store, a store error, a revision over the byte budget), the comment is posted without the numbers and without the link: the lists of the protected files added, modified and removed, as in M1.8, and the sentence "SuiteWard could not read the files of this revision to count the lines that changed, so the numbers are unknown." The cause is logged, never posted. The comment carries the command that approves the revision, so it never waits for the numbers. A comment is never edited, so a delivery that found no numbers keeps saying so. Numbers or a link that the renderer refuses are dropped too: the comment is then posted with the lists only, and the cause is logged.
- A delivery whose context ended is the only error: the message is delivered again, and the numbers are counted then.

**The check summary** states the same four facts (files, lines, files not shown, marker lines seen) as bullets of its list, for a check whose payload names a revision and a canonical version, followed by "Marker lines are lines to look at, not a verdict." It has no link and no path.

**The page** is `GET /reports/{owner}/{repo}/{revision}` of the [HTTP surface](#http-surface-chi), served on the HTTP listener, which is loopback by default and published on the host's loopback only by the installer. Only a `Host` of `127.0.0.1`, `localhost` or `[::1]` is answered (any other is 403), which is a defence against DNS rebinding and not an authentication: anyone who can reach the listener reads the reports of the enrolled repositories, as they read `/status`. It reads only what the revision binds, so it shows nothing that the revision's own manifest and its base canonical do not name.

### Protection verification job (M1.5)

- `github_protection_check`: one periodic River job every 15 minutes with `RunOnStart`, `MaxAttempts` 1 and unique while unfinished, registered only when the GitHub settings are set. It verifies every configured repository (D-PROTECTION) with the protection token (Metadata and Administration read) through the same budget as synchronization, records each result (persistence contract, M1.5), and never fails the job for a GitHub answer: a read failure is the result `unknown`. A budget or rate-limit wait skips the repository until the next run. The check starts no repository after the tick deadline (`JOB_TIMEOUT` minus 5 s) and records only the repositories it finished. Results are keyed by the repository's numeric id: an enrolled repository is recorded even when its read fails (`unknown`), while a repository that was never read and is not enrolled has no result yet, so `/status` omits its `protection` until a read succeeds.

### Synchronization jobs (M1.4)

- `github_sync_tick` (every 20 s and paced since M1.7c, see "Latency (M1.7c)" below): one periodic River job every 60 s (D-SYNC) with `RunOnStart`, so a restart catches up at once. It syncs every configured repository whose `wait_until` has passed, with at most 4 repositories in flight (S1: serial requests take about 0.37 s each, so 20 repositories with several open PRs would overrun 60 s serially). One cycle of one repository is the reconciliation of the [GitHub observations](persistence.md#github-observations-m14-normative). The tick gives itself a deadline below `JOB_TIMEOUT` (`JOB_TIMEOUT` minus 5 s), starts no repository after it, and records only the cycles it finished; repositories it did not reach wait for the next tick. A tick does not start while an earlier one is still running (River's unique insert for the periodic job, or an explicit skip that completes at once).
- `github_sync_repo` (args: `repo`): one repository now, inserted by the local MCP `sync` (M1.4-C); unique by args while unfinished (available, pending, retryable, running or scheduled; River requires `running` among the unique states), so repeated requests coalesce, including one that arrives while that repository's sync is running: the next tick covers it. It honors `wait_until` like the tick (it returns without calling GitHub and records nothing while waiting). It runs on the `github_sync_tick` queue, which has one worker, so it never runs while a tick runs and waits for one that does: two cycles that listed one repository at different moments would otherwise commit in either order, and the older listing could mark a newer pull request not open, which the commands job reads. The requests it makes count toward the pace of the next tick.
- A rate limit or exhausted budget ends that repository's cycle with `wait_until`; other repositories continue. A failed cycle is not a failed job: the job completes and the failure is in `/status`. `/readyz` does not depend on GitHub.

### Latency (M1.7c; normative)

This section is normative from M1.7c. It amends "Synchronization jobs" and "Command and bootstrap jobs" above. The decisions are in [D-SYNC](../plan/decisions.md) and [D-COMMANDS](../plan/decisions.md), both amended 2026-10-16.

**Cycle start.**
- `rest.Response` carries the server `Date`, parsed as `time.Time` in UTC. It is zero when the header is missing or cannot be parsed.
  - A 304 carries the `Date` of that 304, never the cached entry's.
  - The budget keeps its own use of `Date` unchanged.
- One repository's cycle starts at the `Date` of its first response, the first page of the open pull requests.
  - That value is `Cycle.StartedAt`. It is never the tick's first response, and never the local clock.
  - A zero `Date` fails that repository's cycle with the failure kind `decode`. Nothing is recorded as a success, and `/status` shows `lastError` `decode`.
  - **Sanity bound.** A `Date` more than 5 minutes ahead of the local clock fails the cycle the same way. A broken or hostile `Date` would otherwise push the horizon ahead of real time. The local clock is only a bound here and never enters the horizon.
- `Cycle.Requests` is the number of requests the cycle made, and `Cycle.Changed` reports whether any of them was not a 304.
- `Reconciler.Tick` and `SyncRepo` return a `TickResult`, which carries:
  - `Requests`, summed over the cycles;
  - `Changed`, true if any recorded cycle changed;
  - `Recorded`, the number of cycles recorded (M1.7c-E), which decides the commands wake-up;
  - `FirstObserved`, the comments recorded for the first time, each with its repository, id, `created_at`, the cycle's `StartedAt` and the previous `CycleStartedAt`.

  The app logs the detector line from it and decides the wake-ups. `RecordCycle` returns the ids of the comments it inserted for the first time.
- **Listing-lag detector.** For each comment a cycle observes for the first time, the sync logs one Info line, "github comment first observed". It carries:
  - the repository and the comment id;
  - the comment's `created_at`;
  - this cycle's `StartedAt`;
  - that repository's previous successful `StartedAt`, which is its stored `CycleStartedAt` read at the start of the cycle, or none;
  - `missed_by_ms`, the previous start minus `created_at`, logged only when it is positive.

  It carries no comment text. A positive `missed_by_ms` means a cycle that started after the comment did not list it, which is GitHub's listing lag. The margin must stay above the largest value seen.

**Commands horizon.** `commandsJob.horizon` returns the earliest `CycleStartedAt` among the enrolled repositories, minus `commandListingMargin` = 10 s.
- An enrolled repository with no `CycleStartedAt` makes `ready` false, with the Warn line "an enrolled repository has not synchronized yet", which is today's line for a missing `LastSuccess`.
- `commandSkew` and `jobTimeout` no longer enter the horizon. `jobTimeout` stays for the tick deadline.

**Sync pacing.**
- `github_sync_tick` runs every 20 s, with `RunOnStart`.
- A tick returns at once, recording nothing and calling no GitHub, while it is not yet due:
  - it is due 18 s after the previous tick started. The 20 s periodic grid fires on fixed multiples, but each run starts a little after its slot, so a gate of exactly 20 s would skip about half of the ticks. The 2 s tolerance absorbs that pickup jitter (whole-phase review, 2026-10-16);
  - and no sooner than `previous requests × 60 s / 450` after that start.
- The previous tick's start and the requests made since it started are kept in memory, and a restart starts at once. Those requests are the tick's own, counting those of a tick that ends early (a deadline or a budget stop), and those of every `github_sync_repo` job that finished since: an MCP `sync` spends the same budget, so it paces the next tick. A job that ran before the previous tick started is not counted.

**Queues and wake-ups.**
- `github_sync_tick`, `github_commands`, `github_checks`, `github_merges`, `github_bootstrap` and, since SH-H5, `canonical_integrity` each run on their own River queue with `MaxWorkers` 1. Runs of one kind never overlap, so "a run does not start while an earlier one runs" no longer needs the unique periodic insert.
- The periodic inserts keep their unique options, so periodic runs still coalesce. The relay's periodic job is unique while unfinished too: after an unclean stop, its next run waits until River rescues the stuck job (up to `JOB_TIMEOUT` plus 1 minute). Outbox messages stay durable meanwhile, and the lease redelivers a claimed one.
- A **wake-up** is a one-off insert of a job kind with no unique options and `MaxAttempts` 1, made after the waking run's last commit.
  - **It is skipped** when a job of that kind is already `available` or `scheduled`, including a pending periodic run. The River adapter checks this with one query on `river_job`.
  - The race between two concurrent wakers can leave at most one extra job per waker, which is harmless.
  - So wake-ups that arrive during a run queue one follow-up between them, and the queue never grows with the number of wake-ups.
  - A wake-up whose check or insert fails is logged at Warn, with the kind and the error cause (River's or the database's: the job arguments of a wake-up are fixed, so no GitHub text is in it). It never fails the waking run, because the periodic run is the fallback.
- **How a run names its wake-ups.**
  - In `river.Config`, `SyncTick`, `SyncRepo`, `Commands`, `Merges` and `Bootstrap` return `(river.Wake, error)`; `Wake` is the set of job kinds to wake.
  - The River worker inserts those wake-ups after the run returns, whatever its error, because a run that failed late may still have committed work. So "after the commit" holds without passing the runtime to the jobs.
  - `Checks`, `ProtectionCheck` and `Integrity` keep `func(ctx) error`.
  - A kind outside commands, checks, merges, bootstrap and integrity is a programming error. The worker logs it at Error and inserts nothing.
- **Who wakes whom:**

  | Run | Wakes | When |
  |---|---|---|
  | `github_sync_tick` | `github_commands` | at least one cycle was recorded, changed or not: a cycle that only moves the horizon can make an observed comment decidable |
  | `github_sync_tick` | `github_checks`, `github_merges` | at least one recorded cycle has `Changed` |
  | `github_commands` | `github_checks`, `github_bootstrap` | the run recorded at least one ledger entry |
  | `github_merges` | `github_checks`, `canonical_integrity` | the run recorded at least one `promoted` or `corrected` outcome; both record a new canonical version, which the integrity job verifies at once instead of within the hour |
  | `github_bootstrap` | `github_checks`, `canonical_integrity` | the run promoted V1 |

- `github_sync_repo` (MCP) wakes the same kinds as a tick, under the same condition.

**`/status`.** Each `github` entry gains `cycleStartedAt` (RFC 3339, GitHub's clock) when one is recorded. Nothing else changes.

## Local MCP (M1.4; normative)

D-SYNC limits MCP to `sync` and `status`, authenticated with a local token. It lets a coding agent ask for a synchronization and read what SuiteWard observed; it has no approval, revocation, administration or configuration capability, and nothing it returns is authority. The "no endpoint changes state" rule of the HTTP surface covers `HTTP_ADDR`; the MCP `sync` tool enqueues work on its own listener.

- **Transport and library:** MCP Streamable HTTP served by the official Go SDK (`github.com/modelcontextprotocol/go-sdk`, a version that supports revision `2026-07-28` and the earlier `initialize`-based revisions, pinned in `go.mod`), so protocol negotiation, `server/discover`, request-metadata header validation and legacy clients follow the specification without a hand-written protocol. JSON responses; no server-initiated messages; `GET` and `DELETE` answer 405 unless the SDK requires them for a supported legacy revision. Endpoint `POST /mcp` on a separate listener `MCP_ADDR` (default `127.0.0.1:8090`).
- **Origin:** a request carrying an `Origin` header is rejected with 403 unless the origin is in `MCP_ALLOWED_ORIGINS` (comma-separated, empty by default), against DNS rebinding; non-browser clients send none.
- **Authentication:** `Authorization: Bearer <MCP_TOKEN>`, compared in constant time; a missing or wrong token answers 401 with no detail. The token is never logged or returned. Remote access only through Session Manager port forwarding (D-DEPLOY).
- **Backend port** (in `internal/adapters/mcp`, implemented by the composition over the GitHub observations and the job inserter):

  ```go
  type Backend interface {
  	Status(ctx context.Context) (StatusView, error)
  	RequestSync(ctx context.Context, repo string) (SyncResult, error)
  }
  ```

- **Tool `status`** (no arguments): GitHub configured or not; since M1.11 also `instance` (as in `/status`) and, when GitHub is configured and the window is known, `budget` (`remaining`, `reset`, as in `/status`); since M1.5 also the `tier` and the `owner` as in `/status`, and per repository its `protection`; per configured repository `repo`, `lastSuccess`, `lastError`, `waitUntil`, and its open pulls (`number`, `headSha`, `baseRef`, `updatedAt`, `notOpenSince`). No comment bodies (they may carry commands). Returned as structured content and as one JSON text item.
- **Tool `sync`** (argument `repo`, one of the configured `owner/name`): inserts `github_sync_repo` (coalesced while unfinished; `already_pending` means an unfinished job for that repository exists) and returns `queued` true, or false with `reason` (`already_pending`, `waiting` with `waitUntil`); it does not wait for the cycle. Unknown repository, or GitHub not configured: a tool error (`isError: true`).
- **Composition:** `serve` starts the MCP listener whenever `MCP_TOKEN` is set, independently of the GitHub settings (without them `status` says GitHub is not configured and `sync` is a tool error), after River and before `/readyz` turns 200; shutdown drains it with the HTTP server within `SHUTDOWN_TIMEOUT`.
- **Settings:** `MCP_ADDR` (default `127.0.0.1:8090`; the image sets `0.0.0.0:8090` like `HTTP_ADDR`), `MCP_TOKEN` (secret, `_FILE` form expected, at least 32 bytes), `MCP_ALLOWED_ORIGINS` (optional).
- **Compatibility evidence:** the phase verification connects the real Claude Code client (`claude mcp add --transport http …`) to a local `serve` and runs `tools/list` and both tools; a protocol implementation counts as compatible only after a real client used it.

## Container conventions

- Image `ghcr.io/ignisdevne/suiteward`, multi-arch (`linux/amd64`, `linux/arm64`), distroless static, user 65532, read-only root, `ENTRYPOINT ["/suiteward"]`, `CMD ["serve"]`; the only writable path is the `/data` volume (artifacts under `/data/artifacts`). Labels `org.opencontainers.image.source` and `org.opencontainers.image.revision`.
- Image defaults: `SUITEWARD_HTTP_ADDR=0.0.0.0:8080`, `SUITEWARD_MCP_ADDR=0.0.0.0:8090`, `SUITEWARD_ARTIFACT_DIR=/data/artifacts`. Secrets are mounted files: `SUITEWARD_DATABASE_URL_FILE=/run/secrets/database-url`.
- Runtime layout on both targets: a user-defined network `suiteward`; PostgreSQL in a rootless container named `suiteward-db` (the image pinned by digest in CI, volume `suiteward-db-data`, password from a Podman secret) reachable as `suiteward-db:5432`; the service container `suiteward` publishes `127.0.0.1:8081:8080` (HTTP) and `127.0.0.1:8091:8090` (MCP) only. Nothing listens on a public interface.
- The supervisor's stop timeout (Podman `StopTimeout`) must exceed `SHUTDOWN_TIMEOUT` plus the 5 s forced-stop grace (45 s with the default 30 s; a `SHUTDOWN_TIMEOUT` of 40 s or more needs a larger `StopTimeout`).
- Outbound-only: the service needs no inbound port; the host reaches `/healthz`, `/readyz` and `/status` over loopback.
- Tags and build stamps (M1.8, D-RELEASE):

  | Build | Image tags | `version` | `source` |
  | --- | --- | --- | --- |
  | CI `Image build`, on a pull request or a push to `main` | none: both platforms are built and nothing is pushed, signed or attested; no deployment build or `deploy` tag exists any more | `dev` | empty |
  | release, on a `vX.Y.Z` or `vX.Y.Z-rc.N` tag, after the `release` environment approval | the full tag, `vX.Y.Z` or `vX.Y.Z-rc.N`; `latest-stable` too unless it is a pre-release; a final `vX.Y.Z` only on a commit of `main` | the full tag, `vX.Y.Z` or `vX.Y.Z-rc.N` | `https://github.com/IgnisDevNE/SuiteWard/tree/<tag>` |

- `/licenses/THIRD_PARTY_NOTICES` in the image holds the license text of every module linked into the binary. The build fails when a module has none. `/licenses/LICENSE` is SuiteWard's own license.
