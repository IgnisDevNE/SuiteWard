# S1 findings: GitHub walking skeleton

- **Status:** draft for the owner's review; the remote-host section is incomplete (see [Remote host](#remote-host-pending)).
- **Phase:** [S1](../plan/phases/S1.md). Code: `spikes/s1/` (throwaway, separate module, never imported by `internal/`).
- **Measured:** 2026-10-10 against `IgnisDevNE/SuiteWardQ` (public) and `IgnisDevNE/SuiteWardQ-private` with the test App `SuiteWardQ Spike` (not the production App).

This document proposes decision-register updates. It does not apply them: [decisions.md](../plan/decisions.md) is unchanged and each proposal below needs the owner's review.

## What was run

The spike program polls one repository with conditional requests, computes a digest of the protected set (blobs under `tests/` at the PR head), publishes a check run, accepts `/suiteward approve <ref>` from a configured login, detects a merge and records a promotion. A scenario driver generated repeatable traffic (open, push, comment, edit, delete, revert, merge, close). Raw logs with nanosecond timestamps were kept outside the repository (they hold no secrets: request headers, tokens and keys are never logged).

Limits that apply to everything below:

- The harness commented as the App itself (`suitewardq-spike[bot]`) and the program was told to accept that login as the owner. The real owner login was never exercised end to end.
- Latencies were measured from one Windows PC to `api.github.com` with a 20 s poll for the scenarios and 60 s for the long run. Single repository, 1 to 3 open PRs.
- The remote host was not reached; see below.

## Permission matrix

Measured on the **private** repository by minting an installation token narrowed to one permission set and calling each endpoint the program uses. Status codes are exact. `403` means the permission set was insufficient.

| Operation | `GET /pulls?state=open` | `GET /git/trees/{sha}?recursive=1` | `GET /issues/{pr}/comments` | `POST /check-runs` |
| --- | --- | --- | --- | --- |
| `pull_requests: read` | 200 | 403 | 200 | 403 |
| `contents: read` | 403 | 200 | 403 | 403 |
| `issues: read` | 403 | 403 | **403** | 403 |
| `checks: write` | 403 | 403 | 403 | 201 |
| `pull_requests: read` + `contents: read` | 200 | 200 | 200 | 403 |
| `pull_requests: read` + `contents: read` + `checks: write` | 200 | 200 | 200 | 201 |
| `issues: read` + `contents: read` + `checks: write` | 403 | 200 | 403 | 201 |

- **Minimum for the loop:** Pull requests read, Contents read, Checks write (plus Metadata read, implicit). The loop ran for hours with exactly that token.
- **Comments on a PR need Pull requests read.** Issues read alone gets 403 for PR comments.
- **On a public repository every read succeeds with no permission at all** (observed with `checks: write` only); only creating the check needs a permission. A permission test on a public repository proves nothing about reads.
- Reading checks written by others (`checks: read`) and the permission needed to post an acknowledgement comment were **not** measured individually.
- The test App also holds `contents`, `pull_requests` and `issues` write. They were used only by the driver (push, PR, comment, merge) and are harness-only; they must not be read as product requirements.
- The installation token lives one hour (`expires_at` is 60 minutes after minting). Tokens requested with a `repositories` list and a `permissions` map are honoured exactly (`granted_permissions` in the log matches).

## Check identity and state mapping

Measured on `main` of the public repository with a ruleset requiring the check.

- The check is a check run on the PR head SHA. A new head SHA needs a new check run; the run on the old SHA stays as it was.
- **The latest check run with the required name on the head wins.** Two runs on one head (an old `success`, a newer `failure`) gave "failing"; publishing a fresh `success` unblocked the merge about 3 s later.
- The ruleset stores the required check with the App's `integration_id`, so only that App can satisfy it. The branch-protection UI offers the same pinning ("source").
- Effect of the published state on a merge attempt:

| Published state | Merge result | GitHub message |
| --- | --- | --- |
| `in_progress` | blocked | `Required status check "…" is in progress.` |
| `action_required` | blocked | `… is action required.` |
| `failure` | blocked | `… is failing.` |
| `neutral` | **allowed** | (merged) |
| `success` | allowed | (merged) |

- `neutral` satisfies a required check, so it must never be used for "not approved".
- With "require branches to be up to date" on, a PR behind its base is blocked even when its head has `success` (`… is expected.`): the check has to run again on the updated head.
- A comment `/suiteward approve …` from the wrong login set the check to `failure` in the spike (the contract says an invalid approval is a failure). It happened by accident in one run and shows that any commenter can set `failure`; `success` takes precedence, so an existing approval is not revoked, and a valid approval or a head change clears it.
- Latency from a repository event to the published state is about half the poll interval plus the cycle time (see [Polling](#polling-numbers)).

## Merge methods and integrated source

For each merged PR the program fetched the PR, the merge commit and the head commit, and recomputed the protected digest from the **tree of the merge commit**.

| Method | `merge_commit_sha` | Parents | Tree equals head tree | Digest check |
| --- | --- | --- | --- | --- |
| merge | merge commit | 2 (base tip, head) | yes (base unchanged under `tests/`) | matched, promotion recorded |
| squash | new commit | 1 (base tip) | yes | matched, promotion recorded |
| rebase, base unchanged | tip of rebased commits | 1 | yes | (not run) |
| rebase, **base moved** | tip of rebased commits | 1 (new base tip) | **no** | **mismatch, no promotion** |

- Squash and rebase leave the same structure (one parent) and cannot be told apart from the commits alone; a title heuristic was only a hint. Recomputing the digest from the integrated tree works for all three methods and does not need to know the method.
- The rebase-with-moved-base case matters: the approved digest was that of the PR head, but the integrated tree contained another PR's file, so the digests differed. The check had been green for the head. This is the case "require branches to be up to date" prevents (above).
- Not measured live: a fork PR, a PR opened and merged within one poll interval (the program misses it by design), and the PR whose head equals its base (covered only by a fake).

## Same-SHA and same-ref cases

- A push outside the protected set kept the ref and the approval; the new head SHA got a check run that was `success` from the start, 14.7 s after the push.
- A push inside `tests/` changed the ref and returned the check to `in_progress`.
- Reverting that push restored the **same digest** under a different commit SHA, confirming the ref is a function of content, not of the commit.
- Approval is recomputed from the comments each cycle, so a revert regains an approval whose comment still exists; with the comment deleted the check stayed `in_progress`.

## Comment edit and delete

- An edit changes `updated_at` and not `created_at`; the program saw it on the next cycle (about 10 s later) and the check stayed `success` by design (observed, not interpreted).
- A delete is seen as the comment id disappearing, also about 10 s later; the approval state is likewise unchanged for the same head. A later head or ref change, then a return to the same ref, loses the approval because the comment is gone.
- Whether an approval should survive its comment being edited or deleted is a product decision, not decided here.

## Polling numbers

Long run: one repository, 1 to 3 open PRs, poll every 60 s for 5.4 hours (2026-10-10 04:27 to 09:52 UTC).

- 318 cycles, all successful, no 4xx. 954 `GET`s: **935 were 304** and 19 were 200 (98%).
- **A 304 does not count against the rate limit; a 200 costs 1.** Per hourly window the `X-RateLimit-Used` equalled the number of 200s: a window with 174 calls, all 304, used 0; windows with 3 or 4 responses of 200 used 3 or 4. The limit is 5000 per hour and is shared by every process using the installation.
- **The ETag does not survive a token change.** After each of five token rotations the first cycle returned 200 for all three endpoints even though `If-None-Match` was sent (the responses carry `Vary: Authorization`); the cycle after that was 304 again. Cost: three full requests per hour per open PR.
- Calls per cycle: one PR list plus, for each open PR, one tree and one comments request, so `1 + 2N`. A merge adds four requests (PR, merge commit, head commit, merge tree).
- Latency per request from this PC was 340 to 620 ms; a cycle took 1.3 s on average with one to three open PRs. Requests are serial, so a cycle should grow with the number of PRs (not isolated in this run).
- Detection latency (20 s poll): approval to green check 14 to 18 s; push to check on the new head 13 to 15 s; merge to promotion record 7 to 23 s.

## Surprises

1. The ETag is tied to the token, so a restart or a rotation costs a full cycle.
2. `neutral` passes a required check.
3. Rulesets and branch protection are **not enforced on a private repository** of a free organization (the ruleset page says so); enforcement could only be measured after making the test repository public.
4. The latest check run per name decides; duplicates are not an error.
5. PR comments need Pull requests read, not Issues read.
6. Changing a repository's visibility and editing an App installation trigger GitHub's sudo-mode confirmation (passkey or OTP), which only the human can pass.
7. A GHCR package published by a workflow starts private, and the organization first blocked public and internal visibility.

## Deployment findings

### GHCR publication and promotion

- A workflow publishes with the Actions `GITHUB_TOKEN` and `packages: write`. GitHub's documentation lists only that token and personal access tokens for the registry and does not mention App installation tokens; they were not tried. The bot only pushes the workflow file (`workflows: write`).
- The first multi-arch build (amd64 + arm64, cross-compiled on the build platform, no QEMU) took 53 s. The pushed manifest list contains both platforms. Linking the package to the repository with an OCI `source` label and an index annotation worked without a manual step.
- The promotion job is bound to the `remote-poc` environment (required reviewer, no administrator bypass, deployment branches limited to `phase/S1`) and waited for the owner as intended. Self-review cannot be prevented because the owner is the only reviewer.
- A run waiting for approval holds the workflow's concurrency group: the next push stayed `pending` until the old run was cancelled. The bot cannot cancel runs (`actions` is read-only).
- The package was made public after the organization allowed public packages; an anonymous registry token could then list tags and pull. The image is 14.4 MB and runs as uid 65532 with a read-only root filesystem and all capabilities dropped.

### Local container (co-located target)

Podman on the developer PC, image built from the same Containerfile and commit.

- First event 0.97 s after start; the first full cycle took 2.4 s.
- The full loop ran in the container: PR, check, approval, merge, promotion recorded with a digest equal to the approved one.
- After `podman restart` the state survived in the volume (the check run was reused by a PATCH instead of a new run) and the first event came 1.1 s later; the first cycle was unconditional because response bodies are cached in memory only.
- `/status` answered from inside the Podman machine but not through the published loopback port on Windows (WSL port forwarding); it was reached with `podman machine ssh`.
- The container needs only outbound HTTPS. There are no inbound requirements.

### Remote host (pending)

Not yet run. The quadlet, the host steps and the outbound-only checks are written in `spikes/s1/deploy/HOST.md` and need the owner's SSM session. This section is to be completed with: startup, the full loop on the isolated tier, outbound-only behavior (including the IMDS check), restart, logs through `journalctl --user`, status access, and auto-update after the promotion.

Differences already visible between the phase page and D-DEPLOY: the phase page says status is reached through Tailscale or an SSH tunnel; D-DEPLOY says there is no SSH daemon and access is SSM only, plus Cloudflare Access for any HTTP route. The Cloudflare Access application for `suiteward-poc.magalz.space` now exists with one allow policy for the owner; an anonymous request is redirected to its login.

## Proposed decision updates

Each item restates what the register has today and what the measurements support. None is applied.

### D-GITHUB-ACCESS

- Minimal permissions for the MVP, private repositories included: **Metadata read, Pull requests read, Contents read, Checks write.** No Administration, no Issues, no webhooks.
- Request installation tokens narrowed to those permissions and to the installed repositories; renew every hour (reuse until five minutes before `expires_at`).
- Posting acknowledgements as comments needs a write permission that was not isolated here; measure it in M1.6 and add it only then.
- Install only on selected repositories and keep the key on the host (isolated tier) as D-TRUST already says.

### D-CHECKS

- One check run per PR head SHA, name `SuiteWard / Contract`, created by the App and pinned as the required check by `integration_id`.
- State mapping: `success` only for an approval that covers the current ref; `in_progress` while computing; `failure` for a mismatch or an invalid command; **never `neutral`**. Choose between `in_progress` and `action_required` for "awaiting approval" (both block; `action_required` says so explicitly).
- Publish at most once per (head SHA, state); an older SHA's run is harmless.
- Candidate rule: only an owner-authored invalid command sets `failure`; a wrong-author command is logged and acknowledged but does not change the check.
- Require "branches up to date" so the tested head and the integrated tree coincide.

### D-INTEGRATION

- Supported methods: merge, squash and rebase, all resolved by recomputing the protected digest from the tree of `merge_commit_sha` and comparing it with the approved digest; a mismatch is an explicit failed-promotion state, never a silent success.
- Do not rely on telling squash from rebase.
- Detect a PR closed between two polls by also listing recently closed PRs (the spike does not).
- Fork PRs and the head-equals-base case remain unmeasured live.

### D-SYNC

- Keep 60 s with conditional requests: a 304 is free, and the long run stayed far below 1% of the budget per hour.
- Budget the cost as `1 + 2N` conditional requests per repository per poll, three extra full requests per PR per hour for the ETag reset, and four requests per merge.
- The binding constraint is latency, not rate: requests were serial at about 0.4 s each, so a repository with many open PRs or 20 repositories would exceed a 60 s cycle unless repositories are polled concurrently. This is an estimate from the measured per-request latency, not a measurement.
- The 50% budget cap can stay as a guard; it was never approached.

### D-DEPLOY

- Publish through a workflow with `GITHUB_TOKEN`; promote the `deploy` tag through an environment that requires the owner; make the package public so the host pulls without any credential (requires the organization to allow public packages); a private package would need a read credential on the host.
- Cancelling a stale waiting run is a human action (the bot lacks `actions: write`); document that in the install guide.
- Record the SSM-versus-SSH/Tailscale discrepancy in the register once the remote section is complete.
- The remote isolated-tier results are still missing.

## Not measured

Fork PRs; `checks: read`; the permission for posting a comment; the real owner login; head-equals-base live; a PR opened and merged between polls; 20 repositories; secondary rate limits; the remote host.
