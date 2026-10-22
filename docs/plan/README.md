# Delivery plan

- **Updated:** 2026-10-21 (security hardening [SH](phases/SH.md) done and released as `v0.2.0-rc.1` and `v0.2.0-rc.2`; phase [M1.11](phases/M1.11.md) approved, it ships as `v0.2.0-rc.3`; the final `v0.2.0` comes after it)

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
| [M1.2](phases/M1.2.md) | `suiteward serve` and `probe`: configuration, River jobs enqueued in the unit-of-work transaction (as built in M1.2; M1.10 amended [ADR 0005](../decisions/0005-river-background-jobs.md): the outbox carries the transactional guarantee), publication outbox, graceful shutdown, one image built in CI and smoke-tested on both verification targets. Results in the [phase page](phases/M1.2.md#outcome). |
| [M1.3](phases/M1.3.md) | `.suiteward.yml` scope with protected defaults, the protected inventory of a source tree and its change classification, proposal revisions, and the baseline proposal for the existing-baseline bootstrap; first real tasks run through Orbit. Results and limitations in the [phase page](phases/M1.3.md#outcome). |
| [M1.4](phases/M1.4.md) | GitHub synchronization: App authentication with an exact read-only permission map, a budgeted conditional REST client, open PRs, heads and append-only comment versions reconciled on a one-minute tick and on MCP request, `inventory.Tree` reads, and a local MCP endpoint (`status`, `sync`) on the official Go SDK; verified on both targets, with the real Claude Code client and a live test against the real App. Results in the [phase page](phases/M1.4.md#outcome). |
| [M1.5](phases/M1.5.md) | Owner and trust: `suiteward setup` binds the owner and enrolls TOTP on the instance, the declared installation tier, and protection verification of rulesets and classic protection (results, findings, the bypass caveat) at setup and every 15 minutes; verified live and on both targets. Results in the [phase page](phases/M1.5.md#outcome). |
| [M1.6a](phases/M1.6a.md) | PR commands and bootstrap: `/suiteward approve` and `revoke` with TOTP, the command ledger, acknowledgment comments, and the existing-baseline bootstrap (never an empty baseline); verified on both targets. Results in the [phase page](phases/M1.6a.md#outcome). |
| [M1.6b](phases/M1.6b.md) | Integrity assessment of each PR head and the required `SuiteWard / Contract` check published through the outbox: exact-revision approval, consent kept across implementation-only pushes, revoke blocking again; verified on both targets. Results in the [phase page](phases/M1.6b.md#outcome). |
| [M1.7](phases/M1.7.md) | Promotion on merge: merge, squash and rebase resolved through the merge commit's tree, explicit reported states for everything else, corrections, and hourly canonical integrity verification; verified on both targets. Results in the [phase page](phases/M1.7.md#outcome). |
| [M1.7b](phases/M1.7b.md) | Bootstrap from the first PR that carries tests (ADR 0010's other path): a proposal and readiness check on the PR, and V1 promoted from its merge commit; one comment per new change-proposal revision. Reworked after its first live run into an explicit bootstrap (`/suiteward propose` and `/suiteward skip`); verified on both targets and merged 2026-10-16 (PR #25). Results in the [phase page](phases/M1.7b.md#outcome). |
| [M1.7c](phases/M1.7c.md) | Lower latency: the commands watermark in GitHub's clock, a 20 s paced poll, and wake-ups between jobs. Verified on both targets and merged 2026-10-17 (PR #27). Results in the [phase page](phases/M1.7c.md#outcome). |
| [M1.8](phases/M1.8.md) | MVP acceptance: install guide, backup and re-bootstrap runbook, end-to-end run, dogfooding on SuiteWard. Merged 2026-10-17 (PRs #30–#33), released as `v0.1.0`. |
| [M1.9](phases/M1.9.md) | Scope discovery: a suggestion that finds tests at any depth and runner files, a scope notice on PRs that add tests outside the scope, `/suiteward suggest`, the reduction detail, and the key-rotation guide. Merged 2026-10-19 (PR #34) with its release fixes; released as `v0.1.1` from the fixes' merge. Results in the [phase page](phases/M1.9.md#outcome). |
| [M1.10](phases/M1.10.md) | Code review and maintenance (owner request, 2026-10-19), before the security work:<ul><li>a review of the whole codebase;</li><li>defects removed, such as nil handling that hides an impossible state (`return nil, nil`, swallowed errors, empty `default`) and dead code;</li><li>dangerous complexity reduced: hotspots, large functions and duplicated helpers, refactored with their tests;</li><li>the low findings carried from M1.9 closed or explicitly kept.</li></ul>It changes no product behavior. It ends when the code has no dead code, no hidden impossible states and no unaddressed hotspot, as the phase page measures. Merged 2026-10-20 (PR #38), released as `v0.1.2`. |
| [SH](phases/SH.md) | Security hardening from the security gate's findings: commands that follow their pull request, case-fold and invisible-character protection, a least-privilege database role with a migrate-only owner credential, an AlmaLinux 9 isolated host with digest-pinned units, pinned images and tools with scanners and weekly fuzzing, signed releases with provenance and an SBOM, [`SECURITY.md`](../../SECURITY.md). Merged 2026-10-21 (PR #42) and released as `v0.2.0-rc.1`, then `v0.2.0-rc.2` with PR #43 (a backup runbook per tier and the index digest) and PR #44 (parallel cold reads and a blob cache for the checks job). Both tiers were validated on rc.2; the isolated host is now AlmaLinux 9. Results in the [phase page](phases/SH.md#outcome). |

Evidence for the earlier phases lives in [history](../history.md).

## MVP roadmap

The MVP is a self-hosted, tamper-evident, human-approved test contract for one developer working with AI agents on GitHub.

| Phase | Outcome | Depends on |
| --- | --- | --- |
| Security gate (owner request, 2026-10-19) | The last step before the final MVP release, after M1.10 and every other phase:<ul><li>a hardening session;</li><li>a heavy pentest of both tiers and of the managed-service shape (App key, permissions, TOTP flow, host, outbox, check forgery, workflows and tags);</li><li>a dedicated SecOps review between the owner and the orchestrator.</li></ul>Its findings decide what the release carries and what goes to M3. The assessment began on 2026-10-20: the [threat model](../security/threat-model.md), scanners, fuzzing, and live tests on the co-located target. It shipped `v0.1.3` (Go 1.27.3, for the `crypto/x509` distrust of compromised intermediates). The assessment and the hardening ([SH](phases/SH.md)) are done. | M1.10 |
| [M1.11](phases/M1.11.md) | Close-out and robustness (owner, 2026-10-21), one phase that joins what SH left:<ul><li>the budget and the instance in `/status` and MCP `status`; MCP syncs counted in the poll's pace; an ETag cache bounded by bytes;</li><li>a test that keeps the job-kind list of `status.sql` in sync with Go;</li><li>a weekly, non-gating scan of the pinned PostgreSQL image;</li><li>the Deploy workflow retired, CI building the image without pushing, and `local.ps1 -Image` mandatory;</li><li>these records, and a private security register kept locally.</li></ul>It ships as `v0.2.0-rc.3`. | SH |
| Final `v0.2.0` | The MVP release, after M1.11 and the owner's authorization, cut from a revision validated on three targets: A3 (co-located), the AlmaLinux 9 host (isolated), and a throwaway instance that ran the live in-place upgrade of [install 11.5](../install.md#11-updating) from v0.1.3 to rc.3. That instance ran on Fedora 44 with Podman 6.1.4, not on AlmaLinux (see the [M1.11 outcome](phases/M1.11.md#outcome)). All three passed on 2026-10-21. Verified with `scripts/verify-release.ps1` before any host pins it. | M1.11 |

S1's findings may change the tasks of M1.4–M1.7; the orchestrator updates those pages before starting them.

## Verification targets

Every phase that changes what runs (S1, M1.2, M1.4–M1.8, including M1.7b and M1.7c) is verified on both targets before its PR, and the MVP is accepted on both:

| Target | ADR 0027 tier | Shape |
| --- | --- | --- |
| Local container | co-located | The same OCI image under Podman on the developer machine, next to the agent; reduced assurance is reported. |
| Remote host | isolated | The same image on a remote Linux host as a systemd service (Podman quadlet). Outbound-only: no inbound ports; status and MCP reached through Session Manager port forwarding (the Cloudflare tunnel was removed on 2026-10-19); owner setup and TOTP enrollment run as a local command in the owner's own Session Manager session (D-IDENTITY). The App key and database never leave the host. |

There is no deployment workflow: the Deploy workflow, the `:deploy` tag and the `remote-poc` environment were retired on 2026-10-21 (D-DEPLOY), and CI builds the image without pushing it. Releases are the only published images. A host pins a release's signed index digest by hand in the unit files, with no automatic update (no deploy key or registry credential leaves GitHub or reaches the host), and the owner places the App key on the host once. The agent never holds host credentials, so the remote target stays a genuine isolated installation. Host choice and access are decided in D-DEPLOY.

## After the MVP

Deferred by the R1 amendments, in no fixed order until the MVP is in use:

- Contract-change priority queue and `/suiteward prioritize` (ADR 0022).
- Automatic protection setup and confirmed repair (ADRs 0016, 0017).
- Encrypted backups, Release storage, contingency PRs and guided restore (ADRs 0019–0021).
- M2: canonical execution. It starts with the D-EXECUTION decision, the first ecosystem and its RunnerProfile (ADR 0008).
- Scope-discovery UX: the owner's questions Q2 to Q4 and the [M1.9 backlog](phases/M1.9.md#backlog-carried), before or after M2 as the owner chooses.
- M3: hardening selected from observed threats.
- Change report for protected files (owner, 2026-10-19), with no analysis by an agent:
  - in the revision comment, `+N/−M` per protected file, the files with removed lines highlighted, and a link to each file's diff between the canonical commit and the head;
  - a full report on the check's Details page, built from the bytes SuiteWard compared, with the removed and added lines;
  - deterministic markers to look at, never verdicts: removed assertions, added skips, deleted test functions, changed runner files;
  - an HTML report served by the managed service, behind its sign-in.
- A single store read by manifest digest in `nextMergeVersion`, which today reads one version per step (M1.10).
