# S1 spike: poll and check

Throwaway program for the S1 spike (see [CONTRACT.md](CONTRACT.md), which is frozen). It authenticates as the test GitHub App, polls one repository with conditional requests, digests the protected set of every open PR head and publishes a check run. It is also the measurement harness: every GitHub call is logged with a whitelist of response headers. Standard library only; nothing here is imported by `internal/`.

## Run

Environment variables and defaults are in the contract. The key is a mounted file (`S1_KEY_FILE`); it is read once and never printed, logged or copied.

```powershell
# from spikes/s1; run via the project toolchain: ./scripts/dev.ps1 go --% -C spikes/s1 build -o <out> .
$env:S1_APP_ID = '...'; $env:S1_INSTALLATION_ID = '...'; $env:S1_REPO = 'IgnisDevNE/SuiteWardQ'; $env:S1_OWNER_LOGIN = 'magalz'
$env:S1_KEY_FILE = '<path to the .pem>'; $env:S1_STATE_FILE = '<scratch>/state.json'
$env:S1_ONCE = '1'                                  # one cycle, then exit (non-zero when a step failed)
$env:S1_TOKEN_PERMISSIONS = 'pull_requests:read,contents:read'   # no checks:write: a read-only run; GitHub is expected (not yet observed) to refuse publication
./s1.exe
```

Without `S1_ONCE` it polls every `S1_POLL_INTERVAL` and serves `GET /status` on `S1_STATUS_ADDR` (uptime, last poll, per-PR state).

Tests (no network): `go test ./...` inside `spikes/s1`.

## Behavior beyond the contract

- One GET per cycle on `/repos/{r}/pulls?state=open&per_page=100` (first page only) and one on `/repos/{r}/git/trees/{head}?recursive=1` per open PR, each with `If-None-Match` when a body is cached. The cache is memory only: after a restart the first request of every URL is unconditional (the ETags in the state file are kept, but a 304 needs a body to reuse). A truncated tree is an error.
- A check run belongs to one head sha: a new head creates a new run. An approval covers one ref: when the digest changes, `approved_comment_id` and `approved_at` are cleared. The desired state is `success` when `approved_comment_id != 0`, else `in_progress`; `S1_FORCE_CONCLUSION` overrides it. S1-B owns approval detection and sets those fields.
- The last published `<check run id>:<state>` per PR is remembered in memory, so an unchanged PR causes no PATCH per cycle (a restart republishes once).
- ETags of PR heads that are no longer open are dropped from the state each cycle.
- A failing step (one PR, the token, the state write) is logged in the `poll` event and does not stop the other steps; the next cycle retries.

## Reading the log

JSON lines on stdout (and `S1_LOG_FILE`). Every line has `ts` (UTC, nanoseconds; for `http` it is the time the response was fully read) and `event`.

| event | fields |
| --- | --- |
| `http` | `method`, `path` (with query), `status`, `dur_ms`, `mono_ms` (monotonic ms since process start, at request start), `etag_sent`, `headers` (whitelist only), `message` (GitHub error message, errors only, 200 chars) |
| `poll` | `cycle`, `open_prs`, `calls` (method, path, status of every call in the cycle, 200 versus 304), `dur_ms`, `ok`, `error` |
| `digest` | `number`, `head_sha`, `digest`, `ref`, `files`, `prefix`; only when the head or digest changed |
| `check` | `number`, `head_sha`, `state`, `check_run_id`, `action` (`create` or `update`) |
| `token` | `action:"minted"`, `expires_at`, `requested_permissions`, `granted_permissions`, `repository_selection` (never the token or the raw response body) |

Only the whitelisted response headers are logged: `ETag`, `Date`, `Last-Modified`, `X-GitHub-Request-Id`, `X-RateLimit-*`, `Vary`. Request headers, the JWT and the installation token never reach the log (`TestHTTPLogKeepsOnlyWhitelistedHeaders`). GitHub's `Vary` value contains the word `Authorization` (a header name, not a credential), so a grep for it in a real log matches.

304 rate per endpoint: count `poll.calls` entries by `path` and `status`. Rate-limit cost of a 304: compare `X-RateLimit-Used` across consecutive `http` lines.

## Permission matrix (to fill from measurement)

Request the token with `S1_TOKEN_PERMISSIONS` narrowed and read the `status` and `message` of each call.

| Call | Expected permission (unverified) | Observed |
| --- | --- | --- |
| `POST /app/installations/{id}/access_tokens` | JWT only | 201 |
| `GET /repos/{r}/pulls?state=open` | `pull_requests:read` | 200, observed with the pair `pull_requests:read,contents:read`; per-permission not isolated |
| `GET /repos/{r}/git/trees/{sha}?recursive=1` | `contents:read` | not yet measured (no open PR in the repository) |
| `POST /repos/{r}/check-runs`, `PATCH .../{id}` | `checks:write` | not yet measured |

First read-only run against `IgnisDevNE/SuiteWardQ` (token narrowed to `pull_requests:read,contents:read`): token 201 in about 420 ms, PR list 200 in about 460 ms with `X-RateLimit-Limit: 5000`, no open PR. These numbers come from the implementer's console output; no log was kept. The `token` event also carries `granted_permissions` and `repository_selection` from the response (never the token), which settles what the installation token actually received.

## Reading the numbers

- The polling period is `S1_POLL_INTERVAL` plus the cycle time; consumers must use `poll.ts`, not the interval.
- `dur_ms` of an `http` event includes reading the body. `poll.calls` omits transport-level failures (no HTTP status); count `http` events instead.
- A 401 for the installation token clears it, so the next call mints a new one.

## Deviations from the contract wording

- `etags` are keyed by request path plus query (for example `/repos/o/r/pulls?state=open&per_page=100`), not by full URL.
- `http` events carry extra fields `mono_ms` and `message`; `token` events carry `requested_permissions`, `granted_permissions`, `repository_selection`.
- `Retry-After` is not in the header whitelist, so a secondary rate limit is visible only through the status and `message`.
