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
| `INSTALLATION_TIER` (M1.5) | no | `co-located` | `isolated` or `co-located` (D-TIER). Declared by the deployment, never detected; the remote quadlet sets `isolated`, `deploy/local.ps1` sets `co-located`. |

Later phases add settings under the same rules. The GitHub settings of M1.4 are an interim operator configuration: they do not establish the trusted App installation association, which D-IDENTITY and M1.5 decide (see [github](github.md#configuration-interim-m14)).

## Commands

- `suiteward serve`: run the service (below).
- `suiteward probe [--delay D] [--no-wait] [--wait ID] [--timeout D]`: diagnostic client for smoke tests. Without `--wait` it connects with the same settings as `serve` and calls the non-port adapter entry points `(*postgres.Store).EnqueueSystem(ctx, Job, OutboxMessage) (jobID int64, err error)`, which inserts a `probe` job (scheduled after `--delay`, default 0) and an outbox message of kind `probe` (key `probe-<random>`) in one transaction using the same statements as `Tx.Enqueue` and `Tx.Outbox` but with no Suite (see the persistence contract), and prints one JSON line with `probeId` (an opaque string encoding job id and outbox key), then, unless `--no-wait`, waits (default `--timeout 60s`) until the job is `completed` and the message `delivered`, and prints a final JSON line with both states and `elapsedMs`. `--wait ID` waits for an earlier probe by polling `(*postgres.Store).SystemStatus(ctx, jobID int64, outboxKey string) (SystemStatus, error)`, whose `JobState` and `OutboxState` are River's job state and the outbox state as text. Both entry points are owned by M1.2-B; the command in M1.2-C only calls them. Exit code 0 when both finished, 2 on timeout or when either reached a failed terminal state, 1 for any other error. The `probe` job and publisher only log; they exist to verify the runtime through the real worker, not governance.

- `suiteward setup --owner <login> --test-check <owner/name>=<check> ...` (M1.5; D-IDENTITY): bind the instance owner and enroll TOTP. It needs the same settings as `serve` including the GitHub settings, runs migrations like `serve` step 2, and is interactive: it is run on the instance itself (`podman exec -it suiteward /suiteward setup ...` on the co-located target, the same command in the owner's own Session Manager session on the isolated one). Exactly one `--test-check` per configured repository (missing, extra or duplicate is a usage error, exit 2). Steps:
  1. Look up the owner (`GET /users/{login}`, which must be a `User`) and every configured repository (`GET /repos/{r}`, which must be reachable with the configured installation); any failure exits 1 before anything is written.
  2. Generate a TOTP secret (`internal/domain/totp`) and print, once, its `otpauth://` URI and the base32 secret to stdout, with a warning on stderr that a recorded terminal session (Session Manager logging) keeps them.
  3. Read a code from stdin (up to 3 attempts) and verify it against the current time with a tolerance of one step; three failures exit 1 and write nothing.
  4. Atomically: replace the owner and the secret, append one policy revision per repository owned by `github:<numeric id>`, record each repository's test check, and consume the verified step (persistence contract, M1.5).
  5. Verify protection once per repository (D-PROTECTION) and print each result with its instructions.
  Exit 0 when enrollment succeeded, whatever the protection results; the secret, codes and tokens never reach a log. Re-running setup replaces the secret.

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
| `GET /status` | JSON: `version`, `schemaVersion`, `jobs` (`available`, `running`, `retryable`, `scheduled`, `completed`, `discarded`) and `outbox` (`pending`, `delivered`, `failed`). Counts only. `discarded` and `failed` are the visible terminal failures. The job counts exclude the outbox relay's own periodic job (`outbox_relay`) and cover only the jobs River still retains (see the [persistence contract](persistence.md#jobs-and-outbox-m12-normative)); a relay that keeps failing shows as a growing `outbox.pending`, not in `jobs`. Since M1.4, when GitHub is configured, also `github`: one entry per configured repository with `repo`, `lastSuccess`, `lastError` (text without secrets or bodies) and `waitUntil` (RFC 3339 or absent). The job counts also exclude the synchronization tick (`github_sync_tick`). Since M1.5, always `tier` (the effective tier: `co-located` when either `INSTALLATION_TIER` or the enrollment says so; with `tierNote` when they differ, saying to re-run setup) and `owner` (`login`, `accountId`, `enrolledAt`; absent before setup), and each `github` entry has `protection`: `result` (`enforced`, `findings`, `enforcement-unavailable`, `unknown`, or absent before the first verification), `findings` and `caveats` (lists of the D-PROTECTION codes), `checkedAt`. The job counts also exclude `github_protection_check`, `github_bootstrap`, `github_commands` and, since M1.6b, `github_checks`; each `github` entry then also has `checks`, the last check-job error for that repository, absent when none. Since M1.6b each entry also has `bootstrap`, a fixed code for the last reason that repository's bootstrap is waiting or stopped (`no-open-pull-request`, `awaiting-initial-contract`, `read-failed`, `repository-changed`, `invalid-source`, `origin-not-passed`, `assessment-conflict`, `blocked-<reason>`, `awaiting-merge-decision` (M1.7b) or `failed`; since the M1.7b rework the bootstrap codes are those of [Explicit bootstrap: propose and skip](#explicit-bootstrap-propose-and-skip-m17b-rework-2026-10-16)), kept in memory and cleared by the next run that finds nothing wrong, absent when none; never GitHub text. The MCP `status` tool does not show it. Since M1.7 each entry also has `merges` and `integrity`, and the job counts also exclude `github_merges` and `canonical_integrity` (see "Merges and integrity jobs"). Since M1.7c each `github` entry also has `cycleStartedAt` when one is recorded (see "Latency (M1.7c)"). Since M1.8, `source`: the URL of the source revision the binary was built from, set at build time like `version`, and absent when the build set none (D-RELEASE, AGPL section 13). |

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
    3. Verify the code at the comment's `created_at` with tolerance 1. A failure gives `invalid-code`.
    4. Call `ConsumeStep` with the source `github-comment:<id>`. `ErrStepUsed` gives `replayed-code`.
    5. Call `ProcessConsent` with the D-COMMANDS identifiers, `Order` set to the comment id, and the actor `github:<owner id>`. Its `Outbox` holds the acknowledgment, with key `ack:<repository id>:<comment id>`. When it reports `ErrNotFound`, look at the Suite and the proposal in one unit of work: a project with no Suite yet records `no-suite`, with an acknowledgment saying that the bootstrap is pending, and a proposal that does not exist on an existing Suite records `unknown-proposal` (the step stays consumed, because the code was valid). Any other probe failure is a store error.
    6. Record `processed` with the operation.
  - **Rejected owner commands.** `malformed`, `invalid-code`, `replayed-code`, `no-suite`, `unknown-proposal` and `edited-first` get an acknowledgment, written with the ledger entry in one transaction. A known proposal with an unknown revision is processed: `ProcessConsent` rejects it with `UnknownRevision`, and the acknowledgment explains it. `unknown-proposal` is acknowledged with the same text (`Rejected`, reason `UnknownRevision`), which is true for a proposal that does not exist.
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
  - `ack:<repository id>:<comment id>` for a **rejected disposition** (`malformed`, `invalid-code`, `replayed-code`, `no-suite`, `unknown-proposal`, `edited-first`): `disposition`, `comment` and `owner`, plus `reason` (the malformed reason code) for `malformed`, and `reference` (`P<n>-R<m>`) when the command line parsed. These are system rows written by `commands.Store.Record`, so there is nothing to read back; the payload holds the decision.
    - The `reason` codes are `unknown-verb`, `missing-reference`, `bad-reference`, `missing-code`, `bad-code` and `extra-text`. `reference` is required for `invalid-code`, `replayed-code`, `no-suite` and `unknown-proposal`, and absent for `malformed` and `edited-first`.
  - `bootstrap-summary:<repository id>:P<n>-R1`: `summary`, the inputs of the bootstrap summary. These are the proposal and revision numbers, `origin`, `branch`, `protected` (the count), `paths`, `include` and `exclude` (at most 20 each), `includeTotal` and `excludeTotal` (how many rules the declaration has, so the publisher can say how many are not listed), and `tier`, the effective tier (D-IDENTITY: `co-located` when either the setting or the enrollment says so).
    - The payload must stay within the outbox limit of 64 KiB after JSON encoding, whatever the repository holds. A payload over the limit would fail `ProposeBaseline` on every run.
    - So the bootstrap cuts each path, rule and the branch to at most 150 bytes on a UTF-8 boundary before encoding, and ends a shortened entry with `…` (counted in the 150 bytes) so a cut entry never looks complete. `json.Marshal` turns a control byte into a 6-byte escape and invalid UTF-8 into U+FFFD, and that is acceptable for display.
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
  - **No Suite or no canonical:** `in_progress`, reason `bootstrap-pending`. Superseded by [Explicit bootstrap: propose and skip](#explicit-bootstrap-propose-and-skip-m17b-rework-2026-10-16).
  - **Otherwise** compare with `checks.Store.Current`:
    - same head, same canonical, same Suite revision and same latest owner command: nothing to do;
    - a stored `unchanged` result for the same head and canonical: keep it without reading the tree (consent cannot change it);
    - anything else: read the head tree (`Reader.Tree`) and call `governance.AssessHead`, which is idempotent.
  - **State.**
    - `success`: the head is unchanged against the canonical (reason `unchanged`), or ready (reason `ready`).
    - `in_progress`: reasons `approval-missing` (including a revision rebound after the canonical or the policy changed), `not-current`, `assessment-pending`, `source-unavailable` (a tree GitHub truncated); `ErrCanonicalMoved` from `AssessHead` (the canonical moved while the head was assessed) keeps the current record and retries on the next run; any other `ErrConflict`, such as a conflicting assessment, is deterministic and is an opaque failure (`/status` problem `failed`) that fails that pull request's step while the run continues.
    - `failure`: reasons `invalid-source`, `integrity-failed`, `owner-command-rejected`. Since M1.7 `integrity-failed` comes only from the canonical integrity verdict. A head assessment never fails, because its binding is built from that head's own inventory, so `AssessHead` answering `IntegrityNotPassed` or `AssessmentMismatch` is an impossible state: an opaque failure of that pull request's step (`/status` problem `failed`), never a published state.
  - **Owner-command failure.** When `LatestOwnerCommand` on the pull is a rejected disposition, or `processed` with a rejected consent result (read from its receipt), and the command was created at or after the record's `HeadSince`, the state is `failure` with reason `owner-command-rejected`. A rejected owner command posted between a push and the job first recording that head does not set `failure` (the record's `HeadSince` is SuiteWard's own clock): the window is about the synchronization interval plus the job interval, a known limitation to be fixed with a per-head observation time. Before a canonical exists, a rejected owner command does not set `failure` either: the state is `bootstrap-pending`. Superseded by [Explicit bootstrap: propose and skip](#explicit-bootstrap-propose-and-skip-m17b-rework-2026-10-16), where the carrier's `approve` and `revoke` can set it.
  - **Transient read failure** (GitHub, tree): when the stored head equals the open head, keep the record. When the open head is new (no record, or the record holds another head), publish `in_progress`, reason `assessment-pending`, for the new head, so the new SHA never stays without a check.
  - **Lost publication.** When the current generation's message ended `failed` in the outbox, `Publish` re-sends the same state under the next generation.
  - Publish through `checks.Store.Publish` with a `github_check` message.
- A store error fails the run after every repository was tried. The last error per repository is kept in memory and shown in `/status` (`github[].checks`).

**Outbox kind `github_check`**
- Key `check:<repository id>:<pull>:<generation>`. A system row (no project or Suite).
- Payload (explicit tags, strict decoding): `repositoryId`, `repo`, `pull`, `headSha`, `generation`, `state`, `reason`, `reference` (optional), `tier` (effective), `version` (the canonical version, optional).
- The publisher:
  - reads `checks.Store.Current`; a record whose generation differs from the payload's means the message is stale, and it is dropped (success, nothing posted);
  - reads back the head's check runs named `SuiteWard / Contract` from this App; one whose `external_id` is `suiteward:<instance id>:<key>` means it was already posted;
  - otherwise posts the run: `success` and `failure` as `completed` with that conclusion, `in_progress` as `in_progress`; the output title and summary come from the command-text package (bounded; the tier; the copyable `/suiteward approve <ref> <code>` when approval is missing; never a claim that tests ran);
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

1. **Read the default branch.** A repository whose Suite does not exist yet (`Do` answers `ErrNotFound`, before the bootstrap job's first run) gets `bootstrap-pending` on every open PR, as before. For each repository whose Suite exists and has no canonical, read the default branch head and its tree. Cache the result by head SHA in memory, so an unchanged head costs no tree read.
   - The head or tree read fails transiently: keep every stored record, as for a transient read.
   - The default branch has protected files: every open PR gets `in_progress` / `bootstrap-pending`, because the branch path is pending.
2. **Choose the bootstrap carrier,** deterministically.
   - First, the lowest-numbered open non-fork PR for which `BaselineProposalFor(github-pr:<repository id>/<pull>)` finds a proposal.
   - When no open PR has one, the lowest-numbered open non-fork PR whose head has protected files.
   - **Superseded proposals.** A proposal superseded by the branch path is never chosen, because step 1 already answered `bootstrap-pending`. "Superseded" has no store marker. The proposal stays, and if its PR merges with a tree that matches the approved revision exactly, `PromoteMerge` still promotes it, which is safe because an exact match means no drift. A later branch baseline then blocks with `CanonicalPresent`.

   Head inventories are read only while looking for a carrier, and each verdict is cached by head SHA.
   - **A carrier keeps its role while its PR is open,** even when a later push removes its protected files (its check is then `bootstrap-pending`). Another PR with protected files waits until the owner closes it. Changing that is a policy decision, not taken here.
   - **A default branch whose tree GitHub truncates** is treated like one with protected files: every PR gets `bootstrap-pending`, cached by head SHA. The bootstrap job reports the same case as `invalid-source`.
3. **The carrier's head.** First the M1.6b fast path: same head, same Suite revision and same latest owner command keep the record without a read (an approval bumps the Suite revision, so readiness still changes on the next run). Otherwise call `AssessBootstrapHead`, with a `bootstrap-summary` message for a committed revision.
   - Not ready: `in_progress`, `approval-missing`, with the reference.
   - Ready: `success`, `bootstrap-ready`.
   - An invalid source is `failure` / `invalid-source`. A truncated tree is `source-unavailable`.
   - The owner-command `failure` applies to the carrier as it does to change PRs.
   - `ErrCanonicalMoved` keeps the record, and the next run retries.
4. **Every other open PR** gets `bootstrap-pending`. A fork PR keeps `fork-unsupported`.

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
- **The text** lists the changes inside code fences with control characters escaped, then the copyable `/suiteward approve P<n>-R<m> <code>`, and, when `previous` is set, "This supersedes `P<n>-R<m-1>`".
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
   - A source that cannot be read as a contract gives `propose-refused`, reason `invalid-source`: an invalid `.suiteward.yml`, a protected path that is a link, or a truncated tree. These are the cases the bootstrap job reports as `invalid-source`. They are deterministic, so they are refused rather than retried (clarified 2026-10-16).
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
- The owner-command `failure` applies only to `approve` and `revoke` dispositions. `proposed`, `propose-refused`, `skipped` and `skip-refused` never set it or clear it.
- The automatic carrier choice, the default-branch plan and its cache are removed.
- **One cancelled comment per revision.** The `bootstrap-cancelled` key names the revision, so if a push restores the coverage and a later push breaks it again, the check returns to `bootstrap-awaiting` with no second comment.
- **Known limit: shared rejections on the carrier.** The ledger does not record the verb. A rejected disposition that every verb shares (`invalid-code`, `replayed-code`, `malformed`, `edited-first`) therefore turns the carrier `failure` / `owner-command-rejected` even when it came from a `skip` or a `propose`. A corrected `propose` does not clear it; a valid approve or a new head does.

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

The order is deterministic: directory patterns first, then file patterns, each group sorted. The runner list comes after them, sorted. The limit keeps the first patterns in that order.

**The `declaration-missing` reply.** The ack payload keeps `suggestion` (the include list) and `branch`, and adds `runner`, `mixed` and `cut`, all optional, so a payload written under `v0.1.0` still decodes.
- The text renders `include:` and `runner:`.
- When `cut` is set, it says that the list was cut at the limit and that the owner should review it.
- When `mixed` is not empty, it lists those files and says that they may hold test settings, and that SuiteWard does not suggest them.
- The editor link stays within the URL budget of the worst case: at most 8000 bytes, with 20 patterns of 100 bytes and the longest branch.

**Revision comment.** The `revision:` payload adds `reducedRules` (at most 20) and `reducedPaths` (at most 20, each cut to 150 bytes), plus `reducedPathsTotal`, all optional. When `reduced` is set, the text lists the rules and paths that the reduction removes, inside code fences.

**Test-like and uncovered.** `Suggest` and `Uncovered` share one classifier, so they always agree on what counts as test-like. Test-like means the include rules plus the runner file names. `Uncovered(entries, s)` returns the test-like file paths that `s` does not cover. For example, a new `conftest.py` outside the scope is reported. It never returns a path that matches an `exclude` of `s`, because an exclusion is the owner's choice and adding patterns cannot undo it. The **new patterns** for a set of uncovered paths are `Suggest` applied to those entries: its include list and its runner list.

**Outbox `github_comment`, key `scope-notice:<repository id>:<pull>:<set digest>` (new).**
- **When it is written.** For an open, non-fork PR whose Suite has a canonical, the checks job follows these steps.
  1. It takes the uncovered paths of the head tree: `Uncovered` under the governing scope, minus the paths covered by the head's own scope when its `.suiteward.yml` is valid. A PR that already widens the scope to cover them gets no notice; that widening still needs approval.
  2. When there are none, it reads nothing more.
  3. When there are some, it reads the tree of the PR's base commit and keeps the paths absent from it, which are the ones this PR adds.
  4. When any remain, it computes `set digest`, the SHA-256 hex digest of the sorted paths joined by newlines. If a message with that key already exists, it stops. The existing-key check comes before the base-tree read whenever the set is already known. Otherwise it writes the message.

  The result is one notice per distinct set of added uncovered paths, not one per push.
- **Payload:**
  - `repositoryId`, `repo`, `pull`, `head`, `branch`;
  - `paths`: at most 20, each cut to 150 bytes, plus `pathsTotal`;
  - `declaration`: the canonical declaration bytes with the new patterns added by `scope.AddPatterns`. It is never built from the PR head's file. It is at most 8192 bytes (`maxDeclaration`, which bounds the comment). For a larger declaration the job writes no notice and logs that it skipped it;
  - `mixed`;
  - `cut` (optional): set when the new patterns were cut at the limit of 20, so the text says the declaration is partial and asks the owner to review it.
- **The text:**
  - it lists the paths and says they are not protected by the current contract;
  - it gives the full `declaration` in a fenced block (GitHub shows a copy button) and the link `https://github.com/<repo>/edit/<path-escaped branch>/.suiteward.yml`;
  - it says that committing it creates a protected change for the owner to approve, and that nothing is protected until then;
  - it never changes the check and never claims tests ran.
- `splitKey` accepts the kind with its four parts.

**Outbox `github_comment`, key `scope-review:<repository id>:<pull>:<set digest>` (new).**
- **When it is written.** Only together with a `scope-notice`. `Reader.PullFiles` must show `.suiteward.yml` among the PR's changed files, the head's file must be a valid declaration in block style, and a hunk must contain an item line of the `include:` list, the `runner:` list, or both.
- **Payload:** `repositoryId`, `repo`, `pull`, `head`, and `suggestions`: one or two entries `{list, line, anchor, patterns}`, one per list (`include` or `runner`). The marker of each posted comment is the key followed by `:include` or `:runner`.
- **What it posts.** One review comment (`POST /repos/{repo}/pulls/{pull}/comments`, `commit_id` = head, `path` = `.suiteward.yml`, `side` = `RIGHT`) per list that has such a line, on that line.
  - The body is a `suggestion` block holding that line followed by the list's new patterns, at the same indentation.
  - It leaves out the patterns that the head's file already lists, so the result never holds a duplicate.
  - It posts nothing for a list whose new patterns are all present.
  - Patterns of a list with no line inside a hunk stay only in the notice's copyable block.
  - A flow-style list (`include: [a, b]`) gets no review suggestion.
- **Where the lines come from.** This is the only place that starts from the PR head's file ([D-SCOPE-DISCOVERY](../plan/decisions.md#d-scope-discovery-suggesting-and-widening-the-scope)).
- **Dedup** uses `Writer.ReviewCommentsByAuthor` and the marker.
- **A 422** from GitHub (the line is no longer in the diff) completes the message without retry, and the job logs the `kind` and `status` of the failure.

**Command `/suiteward suggest`.** It is owner only and takes no code. The parser accepts the verb without argument. An extra token is `ExtraText`, as for `propose`.
- **`suggest-refused`** is recorded with a short ack that names the reason and quotes no repository text. It is never retried. The reasons are:
  - `fork` or `not-open`, as for `propose`;
  - `invalid-source`: the tree is invalid or truncated, the canonical has no declaration, or the reply cannot be rendered (an unusable branch name, for example);
  - `declaration-too-large`: the suggested declaration is over 8192 bytes.

  A canonical that fails its integrity check gets no reply: the command is retried, then recorded `failed`, like any other failure.
- Otherwise it records **`suggested`**, and its ack replies to the command. Before V1 its reason is `declaration-missing`: the suggested declaration for that PR, built from the head tree or else the default branch. The ack adds `declared`, true when a `.suiteward.yml` already exists on the default branch or in the head, and the text says so. After V1:
  - reason `scope-notice`: the scope notice for the head's uncovered paths, not filtered by the base. The ack fields are `paths`, `pathsTotal`, `declaration`, `cut` and `mixed`. No `scope-review` is written.
  - reason `nothing-uncovered`: a short ack saying that every test-like path of the head is in the scope.
- Neither disposition is counted by `LatestOwnerCommand`. A `suggest` on a project with no Suite records `suggested` with the no-suite acknowledgment.

**Links and submodules** ([D-SCOPE](../plan/decisions.md#d-scope-declaration-and-source-inventory-semantics), amended 2026-10-17). `inventory`'s manifest decides a non-file entry as follows:
- **A symlink.** It is checked only if the scope covers it or may cover a path below it. Then its target is resolved in the same tree. The target is the link's blob, taken relative to the link's directory, and at most 8 links are followed. The target is resolved one component at a time, never by cleaning the text: `.` and empty components are skipped; `..` steps back one component, and stepping past the root leaves the target unresolved; every other component before the last must be a real directory of the tree, not a link, file or submodule. Intermediate links are not followed. If the target is a regular file, the link is unsafe only if the scope covers the link's path. Any other target (a directory, outside the repository, dangling, or a loop) keeps today's rule: unsafe.
- **A submodule.** It is never unsafe and never read. `Suggest` returns the submodules that its patterns would reach as `Submodules` (at most 20). Every reply that suggests a declaration warns that their contents are not protected.

**Bot identity.** When the bot account cannot be resolved, the commands job logs the failure's `kind` and `status` (`readFailure`), never GitHub's text. This also covers a refused App JWT on `GET /app`.

### Protection verification job (M1.5)

- `github_protection_check`: one periodic River job every 15 minutes with `RunOnStart`, `MaxAttempts` 1 and unique while unfinished, registered only when the GitHub settings are set. It verifies every configured repository (D-PROTECTION) with the protection token (Metadata and Administration read) through the same budget as synchronization, records each result (persistence contract, M1.5), and never fails the job for a GitHub answer: a read failure is the result `unknown`. A budget or rate-limit wait skips the repository until the next run. The check starts no repository after the tick deadline (`JOB_TIMEOUT` minus 5 s) and records only the repositories it finished. Results are keyed by the repository's numeric id: an enrolled repository is recorded even when its read fails (`unknown`), while a repository that was never read and is not enrolled has no result yet, so `/status` omits its `protection` until a read succeeds.

### Synchronization jobs (M1.4)

- `github_sync_tick` (every 20 s and paced since M1.7c, see "Latency (M1.7c)" below): one periodic River job every 60 s (D-SYNC) with `RunOnStart`, so a restart catches up at once. It syncs every configured repository whose `wait_until` has passed, with at most 4 repositories in flight (S1: serial requests take about 0.37 s each, so 20 repositories with several open PRs would overrun 60 s serially). One cycle of one repository is the reconciliation of the [GitHub observations](persistence.md#github-observations-m14-normative). The tick gives itself a deadline below `JOB_TIMEOUT` (`JOB_TIMEOUT` minus 5 s), starts no repository after it, and records only the cycles it finished; repositories it did not reach wait for the next tick. A tick does not start while an earlier one is still running (River's unique insert for the periodic job, or an explicit skip that completes at once).
- `github_sync_repo` (args: `repo`): one repository now, inserted by the local MCP `sync` (M1.4-C); unique by args while unfinished (available, pending, retryable, running or scheduled; River requires `running` among the unique states), so repeated requests coalesce, including one that arrives while that repository's sync is running: the next tick covers it. It honors `wait_until` like the tick (it returns without calling GitHub and records nothing while waiting).
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
- The previous tick's start and request count are kept in memory, and a restart starts at once. A tick that ends early (a deadline or a budget stop) counts the requests it made.

**Queues and wake-ups.**
- `github_sync_tick`, `github_commands`, `github_checks`, `github_merges` and `github_bootstrap` each run on their own River queue with `MaxWorkers` 1. Runs of one kind never overlap, so "a run does not start while an earlier one runs" no longer needs the unique periodic insert.
- The periodic inserts keep their unique options, so periodic runs still coalesce.
- A **wake-up** is a one-off insert of a job kind with no unique options and `MaxAttempts` 1, made after the waking run's last commit.
  - **It is skipped** when a job of that kind is already `available` or `scheduled`, including a pending periodic run. The River adapter checks this with one query on `river_job`.
  - The race between two concurrent wakers can leave at most one extra job per waker, which is harmless.
  - So wake-ups that arrive during a run queue one follow-up between them, and the queue never grows with the number of wake-ups.
  - A wake-up whose check or insert fails is logged at Warn, with the kind and nothing else. It never fails the waking run, because the periodic run is the fallback.
- **How a run names its wake-ups.**
  - In `river.Config`, `SyncTick`, `SyncRepo`, `Commands`, `Merges` and `Bootstrap` return `(river.Wake, error)`; `Wake` is the set of job kinds to wake.
  - The River worker inserts those wake-ups after the run returns, whatever its error, because a run that failed late may still have committed work. So "after the commit" holds without passing the runtime to the jobs.
  - `Checks`, `ProtectionCheck` and `Integrity` keep `func(ctx) error`.
  - A kind outside commands, checks, merges and bootstrap is a programming error. The worker logs it at Error and inserts nothing.
- **Who wakes whom:**

  | Run | Wakes | When |
  |---|---|---|
  | `github_sync_tick` | `github_commands` | at least one cycle was recorded, changed or not: a cycle that only moves the horizon can make an observed comment decidable |
  | `github_sync_tick` | `github_checks`, `github_merges` | at least one recorded cycle has `Changed` |
  | `github_commands` | `github_checks`, `github_bootstrap` | the run recorded at least one ledger entry |
  | `github_merges` | `github_checks` | the run recorded at least one `promoted` or `corrected` outcome; both record a new canonical version |
  | `github_bootstrap` | `github_checks` | the run promoted V1 |

- `github_sync_repo` (MCP) wakes the same kinds as a tick, under the same condition.

**`/status`.** Each `github` entry gains `cycleStartedAt` (RFC 3339, GitHub's clock) when one is recorded. Nothing else changes.

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

- **Tool `status`** (no arguments): GitHub configured or not; since M1.5 also the `tier` and the `owner` as in `/status`, and per repository its `protection`; per configured repository `repo`, `lastSuccess`, `lastError`, `waitUntil`, and its open pulls (`number`, `headSha`, `baseRef`, `updatedAt`, `notOpenSince`). No comment bodies (they may carry commands). Returned as structured content and as one JSON text item.
- **Tool `sync`** (argument `repo`, one of the configured `owner/name`): inserts `github_sync_repo` (coalesced while unfinished; `already_pending` means an unfinished job for that repository exists) and returns `queued` true, or false with `reason` (`already_pending`, `waiting` with `waitUntil`); it does not wait for the cycle. Unknown repository, or GitHub not configured: a tool error (`isError: true`).
- **Composition:** `serve` starts the MCP listener whenever `MCP_TOKEN` is set, independently of the GitHub settings (without them `status` says GitHub is not configured and `sync` is a tool error), after River and before `/readyz` turns 200; shutdown drains it with the HTTP server within `SHUTDOWN_TIMEOUT`.
- **Settings:** `MCP_ADDR` (default `127.0.0.1:8090`; the image sets `0.0.0.0:8090` like `HTTP_ADDR`), `MCP_TOKEN` (secret, `_FILE` form expected, at least 32 bytes), `MCP_ALLOWED_ORIGINS` (optional).
- **Compatibility evidence:** the phase verification connects the real Claude Code client (`claude mcp add --transport http …`) to a local `serve` and runs `tools/list` and both tools; a protocol implementation counts as compatible only after a real client used it.

## Container conventions

- Image `ghcr.io/ignisdevne/suiteward`, multi-arch (`linux/amd64`, `linux/arm64`), distroless static, user 65532, read-only root, `ENTRYPOINT ["/suiteward"]`, `CMD ["serve"]`; the only writable path is the `/data` volume (artifacts under `/data/artifacts`). Labels `org.opencontainers.image.source` and `org.opencontainers.image.revision`.
- Image defaults: `SUITEWARD_HTTP_ADDR=0.0.0.0:8080`, `SUITEWARD_ARTIFACT_DIR=/data/artifacts`. Secrets are mounted files: `SUITEWARD_DATABASE_URL_FILE=/run/secrets/database-url`.
- Runtime layout on both targets: a user-defined network `suiteward`; PostgreSQL in a rootless container named `suiteward-db` (the image pinned by digest in CI, volume `suiteward-db-data`, password from a Podman secret) reachable as `suiteward-db:5432`; the service container `suiteward` publishes `127.0.0.1:8081:8080` only. Nothing listens on a public interface.
- The supervisor's stop timeout (Podman `StopTimeout`) must exceed `SHUTDOWN_TIMEOUT` plus the 5 s forced-stop grace (45 s with the default 30 s; a `SHUTDOWN_TIMEOUT` of 40 s or more needs a larger `StopTimeout`).
- Outbound-only: the service needs no inbound port; the host reaches `/healthz`, `/readyz` and `/status` over loopback.
- Tags and build stamps (M1.8, D-RELEASE):

  | Build | Image tags | `version` | `source` |
  | --- | --- | --- | --- |
  | deployment, on a push to `main` or a phase branch | `sha-<commit>`; `deploy` after the `remote-poc` approval | `sha-<commit>` | `https://github.com/IgnisDevNE/SuiteWard/tree/<commit>` |
  | release, on a `vX.Y.Z` or `vX.Y.Z-rc.N` tag, after the `release` environment approval | the full tag, `vX.Y.Z` or `vX.Y.Z-rc.N`; `latest-stable` too unless it is a pre-release; a final `vX.Y.Z` only on a commit of `main` | the full tag, `vX.Y.Z` or `vX.Y.Z-rc.N` | `https://github.com/IgnisDevNE/SuiteWard/tree/<tag>` |

- `/licenses/THIRD_PARTY_NOTICES` in the image holds the license text of every module linked into the binary. The build fails when a module has none. `/licenses/LICENSE` is SuiteWard's own license.
