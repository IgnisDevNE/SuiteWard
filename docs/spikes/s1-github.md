# S1 findings: GitHub walking skeleton

- **Status:** draft for the owner's review. Everything listed under "Not measured" at the end was not done.
- **Phase:** [S1](../plan/phases/S1.md). Code: `spikes/s1/` (throwaway, separate module, never imported by `internal/`).
- **Measured:** 2026-10-10 against `IgnisDevNE/SuiteWardQ` (public) and `IgnisDevNE/SuiteWardQ-private` with the test App `SuiteWardQ Spike` (not the production App).

This document proposes decision-register updates. It does not apply them: [decisions.md](../plan/decisions.md) is unchanged and each proposal below needs the owner's review.

## What was run

The spike program polls one repository with conditional requests, computes a digest of the protected set (blobs under `tests/` at the PR head), publishes a check run, accepts `/suiteward approve <ref>` from a configured login, detects a merge and records a promotion. A scenario driver generated repeatable traffic (open, push, comment, edit, delete, revert, merge, close). Raw logs with nanosecond timestamps were kept outside the repository (they hold no secrets: request headers, tokens and keys are never logged).

Limits that apply to everything below:

- The harness commented as the App itself (`suitewardq-spike[bot]`) and the program was told to accept that login as the owner, except in the two runs where the owner (`magalz`) commented from their own account (host and local container, see their sections).
- Latencies were measured from one Windows PC to `api.github.com` with a 20 s poll for the scenarios and 60 s for the long run. Single repository; 1 open PR in every cycle of the long run and at most 2 in the scenario runs.
- **Clock skew:** timestamps taken from the program's log use the clock of the machine that ran it. The developer PC and the Podman VM ran about 2 s behind GitHub (the `Date` response header minus the log timestamp averages +2.0 s on every cycle). Latencies between a driver action and a program event (both on the PC clock) are consistent with each other; where an owner action is involved, GitHub-side timestamps (`created_at`, `completed_at`) are quoted instead.
- Raw evidence is kept outside the repository, in `C:\Users\magal\.suiteward-s1\runs\` on the developer PC: the program and driver logs, the permission and enforcement runs, the container logs, and `host-events.log`, `host-auto-update.log` and `host-net.txt` captured from the host at the end (journal events with headers stripped, the auto-update journal, listeners, outbound connections and the IMDS status). Statements from the host that were first seen in session output are marked as such below.
- Scenario steps were paced by hand about 22 to 25 s apart, close to the real poll period (20 s sleep plus about 2 s of cycle), so the phase between an event and the next poll was not random and the latencies below are samples, not distributions.
- Some merge-attempt messages were seen interactively and not kept in a log; each such row says so.
- The remote host was operated by the agent except for the key; see the caveat in its section.

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

- **Minimum for the loop's own calls on a private repository:** Pull requests read, Contents read, Checks write (plus Metadata read, implicit). The hours-long loop ran with that token but on the public repository, where reads need nothing, so only the private-repository table above shows the minimum. The calls used after a merge (`GET /pulls/{n}`, commits, `PATCH /check-runs`) were not run with a narrowed token on the private repository.
- **Comments on a PR need Pull requests read.** Issues read alone gets 403 for PR comments.
- **On a public repository every read succeeds with no permission at all** (observed with `checks: write` only); only creating the check needs a permission. A permission test on a public repository proves nothing about reads.
- Reading checks written by others (`checks: read`) and the permission needed to post an acknowledgement comment were **not** measured individually.
- The test App also holds `contents`, `pull_requests` and `issues` write. They were used only by the driver (push, PR, comment, merge) and are harness-only; they must not be read as product requirements.
- The installation token lives one hour (`expires_at` is 60 minutes after minting). Tokens requested with a `repositories` list and an explicit, non-empty `permissions` map are honoured exactly (`granted_permissions` in the log matches). An empty map is **not** a narrow token: it grants everything the App has, so the request must always carry an explicit map.

## Check identity and state mapping

Measured on `main` of the public repository with a ruleset requiring the check.

- The check is a check run on the PR head SHA. A new head SHA needs a new check run; the run on the old SHA stays as it was.
- **The latest check run with the required name on the head wins.** On one head there was an old `success` and three newer `failure` runs (from accidental extra program runs); the merge was refused as "failing". Publishing a fresh `success` unblocked it: the success was published at 04:24:18.6Z and the merge request started 0.5 s later and succeeded.
- The ruleset stores the required check with the App's `integration_id`, which is meant to let only that App satisfy it. That is the configuration; no other actor was ever tried. The branch-protection UI offers the same pinning ("source").
- Effect of the published state on a merge attempt (each state was published as the latest run on the PR head, then a merge was requested):

| Published state | Merge result | GitHub message | Where it was seen |
| --- | --- | --- | --- |
| `in_progress` | blocked | `Required status check "…" is in progress.` | PR 6, interactive, not logged |
| `action_required` | blocked | `… is action required.` | PR 6, interactive, not logged |
| `failure` | blocked | `… is failing.` | PR 4 (kept in the driver log) and PR 6 (interactive) |
| `neutral` | **allowed** | (merged) | PR 5, merged with `neutral` as the latest run |
| `success` | allowed | (merged) | PRs 4, 6, 7 |

- `neutral` satisfies a required check, so it must never be used for "not approved".
- With "require branches to be up to date" on, PR 8 (behind its base, with a `success` run on its head) was blocked (`… is expected.`; interactive, not logged): the check has to run again on the updated head.
- A comment `/suiteward approve …` from the wrong login set the check to `failure` in the spike (the contract says an invalid approval is a failure). It happened by accident in three runs, so any commenter can set `failure`. From the code, `success` takes precedence over `failure` and a valid approval or a head change clears it; that precedence was **not** observed live.
- Latency from a repository event to the published state is bounded by the poll interval plus the cycle time (samples in [Polling](#polling-numbers)).

## Merge methods and integrated source

For each merged PR the program fetched the PR, the merge commit and the head commit, and recomputed the protected digest from the **tree of the merge commit**.

| Method | `merge_commit_sha` | Parents | Tree equals head tree | Digest check |
| --- | --- | --- | --- | --- |
| merge | merge commit | 2 (base tip, head) | yes (base unchanged under `tests/`) | matched, promotion recorded |
| squash | new commit | 1 (base tip) | yes | matched, promotion recorded |
| rebase, base unchanged | not measured | not measured | not measured | not measured |
| rebase, **base moved** | tip of rebased commits | 1 (new base tip) | **no** | **mismatch, no promotion** |

- The only rebase merge (PR 3) had a base that had moved, so the "base unchanged" row is empty on purpose.
- Squash and rebase both leave one parent. The spike did not try to tell them apart beyond a title heuristic labelled as a hint, so whether it is possible was not tested. Recomputing the digest from the integrated tree works for all three methods and does not need to know the method.
- The rebase-with-moved-base case matters: the approved digest was that of the PR head, but the integrated tree contained another PR's file, so the digests differed. The check had been green for the head. This is the case "require branches to be up to date" prevents (above).
- Not measured live: a fork PR, a PR opened and merged within one poll interval (the program misses it by design), and the PR whose head equals its base (covered only by a fake).

## Same-SHA and same-ref cases

- A push outside the protected set kept the ref and the approval; the new head SHA got a check run that was `success` from the start, 14.7 s after the push (an approving comment existed for that ref).
- A push inside `tests/` changed the ref and returned the check to `in_progress`.
- Reverting that push restored the **same digest** under a different commit SHA, confirming the ref is a function of content, not of the commit.
- Approval is recomputed from the comments each cycle. The only live revert happened after the approving comment had been deleted, so the check stayed `in_progress`; that a revert regains an approval whose comment still exists is covered by a test with a fake GitHub, not observed live.

## Comment edit and delete

- An edit changes `updated_at` and not `created_at`; the program saw it on the next cycle (about 10 s later) and logged it as `edited` with `still_valid: false`: the edited text was **no longer a valid approval command**. The check nevertheless stayed `success` because the spike keeps an approval already recorded for the same head (observed, not interpreted). That an approval survives its own comment becoming invalid is the authority-relevant fact and a product decision.
- A delete is seen as the comment id disappearing, also about 10 s later; the approval state is likewise unchanged for the same head. After the later head change and revert the approval was gone, because the comment was gone.
- Whether an approval should survive its comment being edited or deleted is a product decision, not decided here.

## Polling numbers

Long run: one repository, exactly 1 open PR in all 318 cycles, poll every 60 s for 5.4 hours (2026-10-10 04:27 to 09:52 UTC).

- 318 cycles, all successful, no 4xx. 954 `GET`s: **935 were 304** and 19 were 200 (98%): 3 at the start, 15 right after token rotations and 1 more PR-list 200 at 05:13 UTC (a change in the repository).
- **A 304 did not count against the rate limit.** The `X-RateLimit-Used` counter rose only with 200 responses and writes (the check-run `POST` carried the incremented value) and never with a 304: a bucket with 174 calls, all 304, stayed at 0, and buckets with 3 or 4 responses of 200 reached 3 or 4. Grouping the responses by their `X-RateLimit-Reset` gives 8 buckets, not 6 hourly windows, because the first request after some token rotations reported a different reset time. The limit is 5000; the starting value in the first bucket (102) came from earlier traffic of the same installation, so the counter is shared across processes, which was not otherwise isolated. Writes were not isolated either.
- **The ETag does not survive a token change.** After each of five token rotations (about every 55 minutes: the token lives one hour and is reused until five minutes before expiry) the first cycle returned 200 for all three endpoints even though `If-None-Match` was sent (the responses carry `Vary: Authorization`); the cycle after that was 304 again. Cost per rotation: `1 + 2N` full requests for a repository with N open PRs.
- Calls per cycle: one PR list plus, for each open PR, one tree and one comments request, so `1 + 2N`. This was observed at N=1 (long run, mean cycle 1.13 s) and N=2 (scenario run, 1.85 s) only. A merge adds four requests (PR, merge commit, head commit, merge tree).
- Latency per conditional request from this PC: median 365 ms, 5th to 95th percentile 308 to 575 ms, range 284 to 1302 ms (935 requests). The mean cycle over the whole run was 1.27 s. Requests are serial.
- Detection latency with a 20 s poll, in seconds, as individual samples (see the pacing limit above): approval to green check 3.1, 5.7, 13.5, 14.8, 18.8 (n=5, local program) and 22.0 (container); push to the new check 9.8, 13.3, 14.7; merge to promotion record 6.8, 7.5, 23.1 (local program) and 19.8 (container). The smallest approval samples are the two approvals posted together just before a poll.

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

- First log event 0.97 s after the `podman run` command was issued (0.73 s after the container was created); the first full cycle took 2.4 s.
- The full loop ran in the container: PR, check, approval, merge, promotion recorded with a digest equal to the approved one.
- After `podman restart` the state survived in the volume (the check run was reused by a PATCH instead of a new run) and the first event came 1.1 s after the command (0.51 s after the container started); the first cycle was unconditional because response bodies are cached in memory only.
- `/status` answered from inside the Podman machine but not through the published loopback port on Windows (WSL port forwarding); it was reached with `podman machine ssh`.
- **Owner's own approval:** with the container accepting only `magalz` as the owner, the owner commented `/suiteward approve <ref>` on PR 12 from their own account (GitHub `created_at` 10:46:05Z) and the container's check run completed at 10:46:09Z on GitHub (4 s; the comment landed just before a poll, so this is near the best case of a 20 s interval; the container's own clock said 10:46:07.0Z). The host poller, still running with the same owner setting, completed its own check run on the same head at 10:46:21Z (16 s), so two pollers wrote check runs for that PR; the latest one wins. The App merged at 10:47:14.9Z (PC clock) and the container recorded the promotion at 10:47:35.9Z (its clock; 21 s) with a digest equal to the approved one. The agent did not post the approval comment.
- The container needs only outbound HTTPS. There are no inbound requirements.

### Remote host (isolated target)

AWS EC2 `t4g.small` (arm64), Ubuntu 24.04, Podman 4.9.3, rootless user `suiteward`, administered only through SSM. **Caveat on the tier:** the owner authorized the agent to run the host steps with AWS CLI credentials that turned out to be the account root's, so the agent operated this host. Only the App key was entered by the owner in a Session Manager session (the agent could not pass it without writing it into an SSM command, which the SSM history and CloudTrail would keep). The measurements below are therefore real, but they do not demonstrate the "agent cannot reach the host" property of the isolated tier.

- **Quadlet:** Podman 4.9.3 accepted every key of the unit (`NoNewPrivileges`, `ReadOnly`, `DropCapability`, `EnvironmentFile`, `Secret=…,type=mount,uid=,mode=`, `AutoUpdate=registry`) and generated a service; the dry run printed only a harmless warning about an optional directory.
- **First pull:** 1.4 s for the arm64 image, anonymously, from the public package; the stored digest equals the promoted `deploy` index digest.
- **Start:** the service was active at once and the first log event came within 0.2 s of the start; the first cycle created the check run.
- **Full loop on the host:** PR, check, approval, merge, promotion recorded with a digest equal to the approved one. Approval to green check 13.1 s; merge to promotion record 12.3 s (20 s poll, single samples).
- **Restart:** `systemctl --user restart` returned `active`; the state survived (the existing check run was updated by a PATCH, no duplicate) and the first event came 0.13 s after the command.
- **Request latency** from the host in its first cycle: 163 to 420 ms (5 requests), lower than from the developer PC.
- **Status:** `/status` returned 200 through an SSM port forward (`AWS-StartPortForwardingSession`).
- **Outbound-only:** every listener is on loopback (the resolver, `cloudflared` metrics, and the published status port held by `rootlessport`); the only container connection was to a GitHub address on port 443; the security group has no inbound rules.
- **IMDS: a container could obtain a metadata token (HTTP 200).** D-DEPLOY states that IMDSv2 with hop limit 1 keeps containers from reading the role credentials; for a rootless container this did not hold. Only the token request was made, as the check specifies, and no credentials were read. According to D-DEPLOY the instance role is limited to the SSM managed-instance policy (not re-verified here), so the exposure would be that role. This is reported, not fixed.
- **Auto-update on promotion:** with the timer shortened to five minutes, the `promote` job finished at 10:42:57Z, `podman-auto-update.service` pulled and restarted the service at 10:45:17Z (2 min 20 s later; the timer's own jitter and period dominate this number), and the container then ran the new digest with the new `org.opencontainers.image.revision` label. The host stores no registry credential.
- **Owner's own approval:** the host was reconfigured to accept only `magalz` as the owner. The owner commented `/suiteward approve <ref>` on PR 11 from their own account (GitHub `created_at` 10:42:31Z); GitHub shows the check run completed at 10:42:44Z (13 s). The App then merged (`merged_at` 10:44:16Z) and the host recorded the promotion 16 s after the merge request with a digest equal to the approved one (`host-events.log`). The agent did not post that comment.
- **Steps that did not work as documented, now fixed in `HOST.md`:** `sudo -iu suiteward` fails because the account has a `nologin` shell, and `podman secret create … -` needs its stdin to be a pipe (`cat |`).
- Until the owner-approval run above, the env file on the host set `S1_OWNER_LOGIN` to the App's own login (the scenarios were approved by the App) instead of the owner's, and polled every 20 s. After it, the host accepts only `magalz`.

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
- Budget the cost as `1 + 2N` conditional requests per repository per poll (free when 304), `1 + 2N` full requests per repository at each token rotation (about hourly), four requests per merge, and the writes (not isolated here).
- The binding constraint is latency, not rate: requests were serial at about 0.37 s each. Twenty repositories with one open PR each are about 60 requests, about 22 s, which fits a 60 s cycle; the cycle would exceed 60 s from about 160 serial requests (for example 20 repositories averaging 4 open PRs). Poll repositories concurrently if that is expected. This is arithmetic from the measured latency, not a measurement of 20 repositories.
- The 50% budget cap can stay as a guard; it was never approached.

### D-PROTECTION (input only)

- Rulesets and branch protection are not enforced on a private repository of a free organization; verification of required checks and a "branches up to date" rule must say so and fall back to the manual instructions, or require a plan that enforces them.
- A required check can be pinned to the App that publishes it, and `neutral` satisfies a required check; the verification should reject a configuration that does not pin the source.

### D-DEPLOY

- Publish through a workflow with `GITHUB_TOKEN`; promote the `deploy` tag through an environment that requires the owner; make the package public so the host pulls without any credential (requires the organization to allow public packages); a private package would need a read credential on the host.
- Cancelling a stale waiting run is a human action (the bot lacks `actions: write`); document that in the install guide.
- Record the SSM-versus-SSH/Tailscale discrepancy: the spike reached `/status` through SSM port forwarding and needed no SSH daemon and no Tailscale.
- Correct the IMDS statement: hop limit 1 did not stop a rootless container from obtaining a token. Decide a mitigation (for example block `169.254.169.254` for the service user, or accept the exposure of the SSM-only role) before relying on it.
- Promotion by `AutoUpdate=registry` was observed end to end once the image was public: the host adopted the promoted digest 2 min 20 s after the `promote` job with a five-minute timer (the default timer is daily with a random delay, which sets the real latency); the arm64 pull took 1.4 s.
- Document that the key cannot be placed on the host by an agent without leaving it in the SSM command history; the owner enters it in their own session.
- State that this spike did not demonstrate the isolated tier's defining property: the agent operated the host through the AWS CLI (the credentials turned out to be the account root's, which the owner should replace and rotate). A real isolated installation means the agent holds no host access.

## Not measured

Fork PRs; `checks: read`; the permission for posting a comment; the real owner login; head-equals-base live; a PR opened and merged between polls; 20 repositories; secondary rate limits; the host with the agent unable to reach it (the isolated property).
