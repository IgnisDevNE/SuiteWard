# S1 spike contract

Frozen by the orchestrator before wave 1. Throwaway spike: nothing here is a product contract and nothing under `spikes/s1/` is imported by `internal/`.

## Layout

- Separate Go module `github.com/IgnisDevNE/SuiteWard/spikes/s1` (`spikes/s1/go.mod`, `go 1.27.2`, standard library only, no `go.work` anywhere).
- One `package main` in `spikes/s1/*.go`. The build context of the image is the `spikes/s1/` directory.
- `spikes/s1/deploy/` holds the quadlet unit and host instructions. `.github/workflows/s1-publish.yml` is the only workflow the spike adds (removed in M1.2; `.github/workflows/deploy.yml` replaced it).

## Configuration (environment variables; secrets are mounted files, never variables)

| Variable | Default | Meaning |
| --- | --- | --- |
| `S1_APP_ID` | required | Test App id (5257122). |
| `S1_INSTALLATION_ID` | required | Installation id (169774099). |
| `S1_REPO` | required | `owner/name` of the throwaway repository (`IgnisDevNE/SuiteWardQ`). |
| `S1_OWNER_LOGIN` | required | The only login whose `/suiteward approve` counts (`magalz`). |
| `S1_KEY_FILE` | `/run/secrets/app-key` | PEM private key file. Never printed, logged or copied. |
| `S1_STATE_FILE` | `/data/state.json` | State and promotion records (JSON, written atomically). |
| `S1_LOG_FILE` | empty | Also append the log here. Stdout is always written. |
| `S1_POLL_INTERVAL` | `60s` | Go duration. |
| `S1_API_BASE` | `https://api.github.com` | |
| `S1_TOKEN_PERMISSIONS` | empty | `name:level,...` requested when minting the installation token (for the permission matrix). Empty means everything the App has. |
| `S1_PROTECTED_PREFIX` | `tests/` | The protected set is every blob in the PR head tree whose path starts with this prefix. |
| `S1_CHECK_NAME` | `SuiteWard Spike / Contract` | Check run name. |
| `S1_FORCE_CONCLUSION` | empty | Measurement aid: publish this check state instead of the computed one (`in_progress`, `success`, `failure`, `neutral`, `action_required`). |
| `S1_STATUS_ADDR` | `127.0.0.1:8080` | Read-only `GET /status` returning JSON (uptime, last poll, per-PR state). |
| `S1_ONCE` | empty | When `1`, run one poll cycle and exit (used for measurement runs). |

## Behavior fixed in advance

- Authentication: RS256 JWT built with `crypto/rsa`, then `POST /app/installations/{id}/access_tokens` (with `permissions` from `S1_TOKEN_PERMISSIONS` when set), reused until five minutes before `expires_at`.
- Polling: conditional requests with `If-None-Match`; one ETag remembered per URL; a `304` is a success with no change.
- Digest: `sha256` over the lines `<path>\0<blob sha>\n`, sorted by path, of the protected set read from `GET /repos/{r}/git/trees/{head sha}?recursive=1`; hex. The **ref** of a PR is the first 12 hex characters of its digest.
- Check run (name `S1_CHECK_NAME`, `head_sha` = PR head): `in_progress` while no approval covers the current ref, `success` once approved, `failure` for a malformed command or an invalid approval.
- Approval (S1-B): a comment `/suiteward approve <ref>` whose author login equals `S1_OWNER_LOGIN`. Record `created_at` and `updated_at`; later edits and deletions are observed and logged, not interpreted.
- Promotion (S1-B): a closed PR with `merged = true`; record the method observed, `merge_commit_sha`, parents, the protected digest recomputed at the integrated tree, and whether it equals the approved digest.

## State file

```json
{"etags":{"<url>":"<etag>"},"pulls":{"<number>":{"head_sha":"","digest":"","ref":"","check_run_id":0,"approved_comment_id":0,"approved_at":"","merged":{"method":"","merge_commit_sha":"","integrated_digest":"","matches_approved":false,"at":""}}},"promotions":[]}
```

## Log

JSON lines on stdout, one object per line, always with `ts` (UTC RFC 3339 with nanoseconds) and `event`. Every GitHub call logs `event:"http"` with `method`, `path`, `status`, `dur_ms`, `etag_sent` (bool), and a `headers` object restricted to this whitelist: `ETag`, `Date`, `Last-Modified`, `X-GitHub-Request-Id`, `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Used`, `X-RateLimit-Reset`, `X-RateLimit-Resource`, `Vary`. Request headers, the `Authorization` value, the JWT, installation tokens, the `/access_tokens` response body, and the key are never logged. Other events: `poll`, `digest`, `check`, `approval`, `merge`, `promotion`, `token`, and `status_error` (the `/status` listener failed or a snapshot could not be encoded; present in the code since S1-A but missing from the first freeze, listed here on 2026-10-10).

## Image and deployment (S1-D)

- Image `ghcr.io/ignisdevne/suiteward-s1`, multi-arch (`linux/amd64`, `linux/arm64`), tags `sha-<commit>` (published by the workflow) and `deploy` (moved only by the approval-gated job).
- The container runs as a non-root user, reads secrets from `/run/secrets/`, state from `/data`, and ignores any request for a listening port other than `S1_STATUS_ADDR`.
- The workflow publishes with the Actions `GITHUB_TOKEN` and `packages: write`; GitHub App installation tokens are not documented for ghcr.io and are not used.
- The `deploy` tag is moved by a job bound to the `remote-poc` environment (required reviewer: the owner). No host credential is stored in GitHub; the host pulls (`AutoUpdate=registry`).
