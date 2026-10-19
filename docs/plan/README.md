# Delivery plan

- **Updated:** 2026-10-17 (M1.8 merged: `v0.1.0` released and SuiteWard governs itself; M1.9 next)

Each phase produces one PR, integrated with a merge commit after the user's authorization. Work inside a phase is orchestrated as described in [orchestration](orchestration.md): the main session writes [briefs](task-brief.md), `sw-implementer` subagents implement them test-first in their own worktrees, and `sw-reviewer` subagents review them. Open and accepted product choices live in the [decision register](decisions.md).

Phase pages are written by hand and kept short. A task listed on a page becomes a brief when its phase starts; the orchestrator may split or merge tasks when the actual code shows a better boundary, and records the change on the page.

## Completed

| Phase | Outcome |
| --- | --- |
| F0, F0.01, F0.02 | Development environment, CI, initial plan, TDD rule. |
| M0.01–M0.04 | Domain values, proposals and consent, guarded promotion, application coordination. |
| M1.01 | First PostgreSQL and filesystem persistence (replaced in R1). |
| R1 | Simplification: normalized persistence and unit-of-work port, removal of speculative code and process machinery, trust and scope decisions, this roadmap. |
| [H1](phases/H1.md) | Orchestration v2, golangci-lint in `check` and CI, nil-check hygiene, Go 1.27.2. |
| [S1](phases/S1.md) | GitHub walking skeleton on both verification targets; findings and proposed decision updates in [s1-github](../spikes/s1-github.md). |
| [M1.2](phases/M1.2.md) | `suiteward serve` and `probe`: configuration, River jobs enqueued in the unit-of-work transaction, publication outbox, graceful shutdown, one image built in CI and smoke-tested on both verification targets. Results in the [phase page](phases/M1.2.md#outcome). |
| [M1.3](phases/M1.3.md) | `.suiteward.yml` scope with protected defaults, the protected inventory of a source tree and its change classification, proposal revisions, and the baseline proposal for the existing-baseline bootstrap; first real tasks run through Orbit. Results and limitations in the [phase page](phases/M1.3.md#outcome). |
| [M1.4](phases/M1.4.md) | GitHub synchronization: App authentication with an exact read-only permission map, a budgeted conditional REST client, open PRs, heads and append-only comment versions reconciled on a one-minute tick and on MCP request, `inventory.Tree` reads, and a local MCP endpoint (`status`, `sync`) on the official Go SDK; verified on both targets, with the real Claude Code client and a live test against the real App. Results in the [phase page](phases/M1.4.md#outcome). |
| [M1.5](phases/M1.5.md) | Owner and trust: `suiteward setup` binds the owner and enrolls TOTP on the instance, the declared installation tier, and protection verification of rulesets and classic protection (results, findings, the bypass caveat) at setup and every 15 minutes; verified live and on both targets. Results in the [phase page](phases/M1.5.md#outcome). |
| [M1.6a](phases/M1.6a.md) | PR commands and bootstrap: `/suiteward approve` and `revoke` with TOTP, the command ledger, acknowledgment comments, and the existing-baseline bootstrap (never an empty baseline); verified on both targets. Results in the [phase page](phases/M1.6a.md#outcome). |
| [M1.6b](phases/M1.6b.md) | Integrity assessment of each PR head and the required `SuiteWard / Contract` check published through the outbox: exact-revision approval, consent kept across implementation-only pushes, revoke blocking again; verified on both targets. Results in the [phase page](phases/M1.6b.md#outcome). |
| [M1.7](phases/M1.7.md) | Promotion on merge: merge, squash and rebase resolved through the merge commit's tree, explicit reported states for everything else, corrections, and hourly canonical integrity verification; verified on both targets. Results in the [phase page](phases/M1.7.md#outcome). |

Evidence for the earlier phases lives in [history](../history.md).

## MVP roadmap

The MVP is a self-hosted, tamper-evident, human-approved test contract for one developer working with AI agents on GitHub.

| Phase | Outcome | Depends on |
| --- | --- | --- |
| [M1.7b](phases/M1.7b.md) | Bootstrap from the first PR that carries tests (ADR 0010's other path): a proposal and readiness check on the PR, and V1 promoted from its merge commit; one comment per new change-proposal revision. Reworked after its first live run into an explicit bootstrap (`/suiteward propose` and `/suiteward skip`); verified on both targets and merged 2026-10-16 (PR #25). Results in the [phase page](phases/M1.7b.md#outcome). | M1.7 |
| [M1.7c](phases/M1.7c.md) | Lower latency: the commands watermark in GitHub's clock, a 20 s paced poll, and wake-ups between jobs. Verified on both targets and merged 2026-10-17 (PR #27). Results in the [phase page](phases/M1.7c.md#outcome). | M1.7b |
| [M1.8](phases/M1.8.md) | MVP acceptance: install guide, backup and re-bootstrap runbook, end-to-end run, dogfooding on SuiteWard. Merged 2026-10-17 (PRs #30–#33), released as `v0.1.0`. | M1.7c |
| [M1.9](phases/M1.9.md) | Scope discovery: a suggestion that finds tests at any depth and runner files, a scope notice on PRs that add tests outside the scope, `/suiteward suggest`, the reduction detail, and the key-rotation guide. Merged 2026-10-19 (PR #34) with its release fixes; released as `v0.1.1` from the fixes' merge. Results in the [phase page](phases/M1.9.md#outcome). | M1.8 |
| Security gate (owner request, 2026-10-19) | The last step before the final MVP release, after every other phase:<ul><li>a hardening session;</li><li>a heavy pentest of both tiers and of the managed-service shape (App key, permissions, TOTP flow, host, outbox, check forgery, workflows and tags);</li><li>a dedicated SecOps review between the owner and the orchestrator.</li></ul>Its findings decide what the release carries and what goes to M3. | All MVP phases |

S1's findings may change the tasks of M1.4–M1.7; the orchestrator updates those pages before starting them.

## Verification targets

Every phase that changes what runs (S1, M1.2, M1.4–M1.8, including M1.7b and M1.7c) is verified on both targets before its PR, and the MVP is accepted on both:

| Target | ADR 0027 tier | Shape |
| --- | --- | --- |
| Local container | co-located | The same OCI image under Podman on the developer machine, next to the agent; reduced assurance is reported. |
| Remote host | isolated | The same image on a remote Linux host as a systemd service (Podman quadlet). Outbound-only: no inbound ports; status and MCP reached through Session Manager port forwarding (the Cloudflare tunnel was removed on 2026-10-19); owner setup and TOTP enrollment run as a local command in the owner's own Session Manager session (D-IDENTITY). The App key and database never leave the host. |

Remote deployment runs through a GitHub Actions workflow bound to a protected environment that requires the owner's approval; the host pulls the promoted image itself (no deploy key or registry credential leaves GitHub or reaches the host), and the owner places the App key on the host once. The agent can prepare and trigger a deployment but never holds host credentials, so the remote target stays a genuine isolated installation. Host choice and access are decided in D-DEPLOY.

## After the MVP

Deferred by the R1 amendments, in no fixed order until the MVP is in use:

- Contract-change priority queue and `/suiteward prioritize` (ADR 0022).
- Automatic protection setup and confirmed repair (ADRs 0016, 0017).
- Encrypted backups, Release storage, contingency PRs and guided restore (ADRs 0019–0021).
- M2: canonical execution. Research on the first execution profile and isolation (D-EXECUTION) may start at any time.
- M3: hardening selected from observed threats.
- Change report for protected files (owner, 2026-10-19), with no analysis by an agent:
  - in the revision comment, `+N/−M` per protected file, the files with removed lines highlighted, and a link to each file's diff between the canonical commit and the head;
  - a full report on the check's Details page, built from the bytes SuiteWard compared, with the removed and added lines;
  - deterministic markers to look at, never verdicts: removed assertions, added skips, deleted test functions, changed runner files;
  - an HTML report served by the managed service, behind its sign-in.
