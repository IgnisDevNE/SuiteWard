# S1 spike: poll, check, approve and promote

Throwaway program for the S1 spike (see [CONTRACT.md](CONTRACT.md), which is frozen). It authenticates as the test GitHub App, polls one repository with conditional requests, digests the protected set of every open PR head, publishes a check run, turns an owner approval comment into `success` and records how a PR was merged. A scenario driver in `driver/` generates the GitHub traffic. It is also the measurement harness: every GitHub call is logged with a whitelist of response headers. Standard library only; nothing here is imported by `internal/`.

## Run

Environment variables and defaults are in the contract. The key is a mounted file (`S1_KEY_FILE`); it is read once and never printed, logged or copied.

```powershell
# from spikes/s1; run via the project toolchain: ./scripts/dev.ps1 go --% -C spikes/s1 build -o <out> .
$env:S1_APP_ID = '...'; $env:S1_INSTALLATION_ID = '...'; $env:S1_REPO = 'IgnisDevNE/SuiteWardQ'; $env:S1_OWNER_LOGIN = 'magalz'
$env:S1_KEY_FILE = '<path to the .pem>'; $env:S1_STATE_FILE = '<scratch>/state.json'
$env:S1_ONCE = '1'                                  # one cycle, then exit (non-zero when a step failed)
$env:S1_TOKEN_PERMISSIONS = 'pull_requests:read,contents:read,issues:read,checks:write'   # narrow it per measurement; a PR whose comments cannot be read gets no check published that cycle
./s1.exe
```

Without `S1_ONCE` it polls every `S1_POLL_INTERVAL` and serves `GET /status` on `S1_STATUS_ADDR` (uptime, last poll, per-PR state).

Because the App cannot be the human owner, volume runs set `S1_OWNER_LOGIN=suitewardq-spike[bot]` and let the driver post the approval; the real owner login is exercised by hand. The first live runs set `S1_TOKEN_PERMISSIONS` explicitly (there is no read-only mode).

Tests (no network): `go test ./...` inside `spikes/s1` (covers the program and the driver).

## Behavior beyond the contract

- One GET per cycle on `/repos/{r}/pulls?state=open&per_page=100` (first page only) and one on `/repos/{r}/git/trees/{head}?recursive=1` per open PR, each with `If-None-Match` when a body is cached. The cache is memory only: after a restart the first request of every URL is unconditional (the ETags in the state file are kept, but a 304 needs a body to reuse). A truncated tree is an error.
- A check run belongs to one head sha: a new head creates a new run. `S1_FORCE_CONCLUSION` overrides the computed state. Approval and failure are described under [Approval](#approval-and-the-failure-rule).
- The first page of each PR's comments (`per_page=100`) is read, with `If-None-Match` like the other calls; the scan runs from the cached body on a 304 cycle too. A PR with more than 100 comments is not handled (shortcut).
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
| `approval` | `number`, `comment_id`, `result` (`approved`, `rejected`, `edited`, `deleted`), `reason` (`rejected` only: `malformed`, `wrong_author`, `stale_ref`), `author`, `created_at`, `updated_at`, `ref`; `edited` and `deleted` carry `approved_updated_at`, `updated_at` and (edited) `still_valid`. Each verdict and observation is logged once. |
| `merge` | `number`, `merged`; for a merge `result` holds the [merge record](#merge-detection-and-the-promotion-record) |
| `promotion` | `number`, `merge_commit_sha`, `integrated_digest`; only when the integrated digest equals the approved one |
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
| `GET /repos/{r}/issues/{n}/comments` | `issues:read` or `pull_requests:read` | not yet measured |
| `GET /repos/{r}/pulls/{n}` | `pull_requests:read` | not yet measured |
| `GET /repos/{r}/git/commits/{sha}` | `contents:read` | not yet measured |
| driver: contents, refs, PRs, comments, merge | `contents:write`, `pull_requests:write`, `issues:write` | not yet measured |

First read-only run against `IgnisDevNE/SuiteWardQ` (token narrowed to `pull_requests:read,contents:read`): token 201 in about 420 ms, PR list 200 in about 460 ms with `X-RateLimit-Limit: 5000`, no open PR. These numbers come from the implementer's console output; no log was kept. The `token` event also carries `granted_permissions` and `repository_selection` from the response (never the token), which settles what the installation token actually received.

## Approval and the failure rule

A command is a comment whose first line starts with the word `/suiteward`; every other comment is ignored (not logged). `/suiteward approve <ref>` must have exactly three words on that line (extra whitespace is fine, extra words are not), the comment author's `user.login` must equal `S1_OWNER_LOGIN`, and `<ref>` must equal the PR's current ref. Logins and refs are compared exactly, case-sensitively. A bot owner such as `suitewardq-spike[bot]` works because the comparison is a plain string match. Otherwise the verdict is `malformed`, `wrong_author` or `stale_ref` (checked in that order) and nothing but the failure marker changes.

Every cycle, inside `pull()` after the ref is updated:

1. If `approved_comment_id` is set for the current ref, it is kept; its comment is only observed. A changed `updated_at` logs `approval` `edited` (with `still_valid`, whether the new text would still be a valid approval); a missing comment logs `deleted`. The approval state does not change either way (observed, not interpreted). The observation is stored in `approval_observed` and logged once per change.
2. Otherwise the earliest valid approval of the current ref in the list wins and is recorded (`approved_comment_id`, `approved_at` = `created_at`, `approved_updated_at`, `approved_digest`). This rescan is why a ref that returns to an earlier value (push, then revert) regains its approval, and why the state's `approved_comment_id` is a cache that a ref change clears.
3. A ref change clears the approval, its observation and the failure marker.

Check state, with precedence `success`, then `failure`, then `in_progress`:

- `success` while an approval covers the current ref.
- `failure` when no approval covers the ref and a command was judged invalid (`malformed`, `wrong_author`, `stale_ref`) under the current ref. "Judged under the current ref" means the verdict was first made, or the comment was edited, after the PR reached its current ref; commands already judged are remembered per comment id and `updated_at` in `judged`, so a restart does not republish. An approval that was valid when posted is not re-judged after a push, so a push after an approval gives `in_progress`, not `failure`. The reason is added to the check summary.
- `in_progress` otherwise.

`failure` therefore ends on a ref change or on a valid approval of the current ref. A stale approval that was first seen only after the ref changed (for example when the program was off) is `failure` until the next ref change.

## Merge detection and the promotion record

A PR that was open in the state and is no longer in the open list is fetched once (`GET /pulls/{n}`). Not merged: `closed` is recorded and `merge` logs `merged:false`. Merged: the merge commit, the head commit and the tree of the merge commit (`recursive=1`) are fetched, the protected digest is recomputed from that tree and compared with `approved_digest`. Nothing is recorded until all requests succeed, so a failed cycle retries. A PR that is opened and merged within one poll interval is never seen open, so it is never detected.

The record (`pulls.<n>.merged`, and the `merge` event's `result`):

| field | meaning |
| --- | --- |
| `method` | structure only: `merge` for two parents, `squash_or_rebase` for one parent. Squash and rebase leave the same structure (one parent, and a tree equal to the head tree whenever the base did not move), so they cannot be told apart from parents and trees. |
| `title_hint` | a guess from commit titles, not a fact: `squash` when the merge commit's first line ends with `(#<n>)`, `rebase` when it equals the head commit's first line, else empty. It depends on the repository's squash-title setting; compare it with the driver's `requested_method`. |
| `merge_commit_sha`, `parents`, `tree_sha` | as GitHub reports them |
| `head_sha`, `head_tree_sha`, `tree_equals_head`, `merge_commit_is_head` | `tree_equals_head` compares tree shas; `merge_commit_is_head` is the same-SHA case (the merge commit is the head commit) |
| `base_sha`, `head_is_base` | `base.sha` of the closed PR as GitHub reports it; it is not the post-merge tip and is not interpreted |
| `merged_by`, `merged_at` | from the PR; `merged_by` is empty when GitHub sends null |
| `approved_digest`, `integrated_digest`, `matches_approved`, `reason` | `reason` is `no approval recorded` or `integrated digest differs from the approved digest` when not matching |
| `at` | when the program recorded it (UTC) |

When `matches_approved` is true a `promotions` entry is appended: the same record plus `number`. Otherwise `promotions` is untouched and `matches_approved:false` with the `reason` is the only trace. A merged PR with `merge_commit_sha` null is an error that is retried.

## Driver

`spikes/s1/driver` is a separate `package main` (build with `go -C spikes/s1 build -o <out> ./driver`). It reads the same `S1_APP_ID`, `S1_INSTALLATION_ID`, `S1_KEY_FILE`, `S1_API_BASE` and `S1_REPO`, and refuses to run unless `S1_REPO` is `IgnisDevNE/SuiteWardQ` (checked before the key is read). It mints its own installation token with exactly `contents:write`, `pull_requests:write`, `issues:write`.

| command | what it does |
| --- | --- |
| `seed` | creates `README.md` and `tests/a.txt` on `main` through the Contents API when missing (commits to the default branch without naming it; the Contents API is the likely route for an empty repository, not verified against GitHub) |
| `open <name>` | branch `<name>` from `main`, commit `tests/<name>.txt`, open the PR, print its number |
| `push <pr> [-outside]` | add `tests/push-<ns>.txt` (or `other/push-<ns>.txt` with `-outside`) to the PR branch |
| `comment <pr> <text>`, `edit <pr> <comment-id> <text>`, `delete <pr> <comment-id>` | issue comment calls |
| `merge <pr> <merge\|squash\|rebase>` | merges with that method; the output has `requested_method` |
| `close <pr>` | closes without merging |

Each prints one JSON line: `cmd`, `status` (of the last request), ids and shas, and `t_before`/`t_after`, the local UTC time (nine fractional digits) taken just before the last request and just after its response. Preparatory requests (token, lookups) are not bracketed. Nothing prints a token or key.

## Reading the numbers

- The polling period is `S1_POLL_INTERVAL` plus the cycle time; consumers must use `poll.ts`, not the interval.
- `dur_ms` of an `http` event includes reading the body. `poll.calls` omits transport-level failures (no HTTP status); count `http` events instead.
- A 401 for the installation token clears it, so the next call mints a new one.

## Deviations from the contract wording

- `etags` are keyed by request path plus query (for example `/repos/o/r/pulls?state=open&per_page=100`), not by full URL.
- `http` events carry extra fields `mono_ms` and `message`; `token` events carry `requested_permissions`, `granted_permissions`, `repository_selection`.
- `Retry-After` is not in the header whitelist, so a secondary rate limit is visible only through the status and `message`.
- `pulls.<n>` carries extra state fields: `approved_updated_at`, `approved_digest`, `approval_observed`, `approval_observed_updated_at`, `rejected`, `judged`, `closed` (`""`, `merged` or `closed`). `merged` has more fields than the contract's four (see the merge record) and `matches_approved` is false until a merge is examined. A `closed` PR is tracked again if it shows up open.
