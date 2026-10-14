# Decision register

- **Updated:** 2026-10-14 (phase M1.6a: D-COMMANDS accepted; D-IDENTITY and D-CHECKS amended)

These gates record product and implementation choices that accepted ADRs deliberately left open. They do not reopen accepted ADRs. The integrator can resolve implementation choices within accepted policy; changes to product authority, scope, or release commitments need the project owner. Do not choose an unresolved product policy to unblock a task; record the decision needed and take independent ready work.

## Accepted

### D-BOT: Authorized GitHub publication identity

Accepted 2026-09-30: `ignisdevne[bot]` (App 5028495, installation 163660443). Never publish with a personal account, and never fall back to personal credentials after a bot access denial. Tokens are restricted to SuiteWard and never appear in artifacts or conversation.

### D-RUNTIME: Runtime configuration and lifecycle

Accepted 2026-10-05. A single `suiteward serve` binary runs the API and the worker. Configuration comes from environment variables plus file-mounted secrets. Shutdown is graceful with a timeout. River retries are bounded and end in a visible terminal state.

### D-SYNC: Polling, budget and MCP boundary

Accepted 2026-10-05 as initial defaults; it no longer gates the runtime phase.

- Poll every 60 s with conditional (ETag) requests.
- Use at most 50% of the installation's API rate budget.
- Supported limit: at most 20 repositories.
- MCP is limited to sync and status, authenticated with a local token.

Final numbers come from the S1 walking skeleton.

### D-SCOPE: Declaration and source-inventory semantics

Accepted 2026-10-05.

- The declaration uses gitignore-style include and exclude rules, case-sensitive.
- A symlink or submodule inside the scope makes the declaration invalid; the governing scope stays in force.
- Approval binds the raw declaration bytes plus the normalized rules.
- A candidate omission cannot remove existing protection; reducing the scope requires approval like any other change (ADR 0013).

### D-SCOPE-DEFAULTS: Which files the protected defaults cover

Accepted 2026-10-11 (owner, phase M1.3). The ADR 0013 amendment protects the workflows, the test runner configuration and the scripts that run tests; which runner files exist depends on the project's ecosystem, so SuiteWard does not guess them.

- `.suiteward.yml` is always in scope: no exclude removes it, and changing or deleting it never disables protection (ADR 0013).
- `.github/workflows/**` is a fixed default.
- The declaration has a `runner` list of patterns that the owner fills with the runner configuration and test scripts.
- Once canonical, removing `.github/workflows/**` or a `runner` entry from the scope, by exclusion or by deleting the entry, is a scope reduction and needs approval like any other change.
- The declaration is YAML, parsed with `go.yaml.in/yaml/v3`; its schema is fixed by the M1.3-A2 brief.
- An `exclude` pattern that matches `.suiteward.yml` has no effect on coverage but is still classified as a scope reduction (decided by the integrator at the owner's request, 2026-10-11): the rule-level check is syntactic, and such an edit changes a covered file, so it needs approval either way; an exact rule would need a special case that buys no safety.

### D-GITHUB-ACCESS: GitHub App permissions and token lifecycle

Accepted 2026-10-11 (owner, from the S1 findings in [s1-github](../spikes/s1-github.md)).

- The App holds Metadata read, Pull requests read and write (write measured 2026-10-13, M1.6a-P0), Contents read, Administration read (since 2026-10-13, for protection verification, D-PROTECTION) and Checks write; no Administration write, no Issues, no webhooks. Reading PR comments needs Pull requests read (Issues read alone is refused).
- Installation tokens are requested with the installed repositories and an explicit, non-empty permission map (an empty map grants everything the App has), the narrowest one per use: synchronization is read-only and requests neither Checks write nor Administration; protection verification requests only Metadata and Administration read.
- A token lives one hour and is reused until five minutes before it expires.
- Acknowledgement comments use Pull requests write, requested only by the comment publisher with `{metadata: read, pull_requests: write}`. M1.6a-P0 measured on 2026-10-13 that a token requesting `pull_requests: read` from the App that holds write is granted exactly read, so synchronization stays read-only, and that the same comment POST with the read map answers 403.
- The GitHub adapter contract is [github](../contracts/github.md).

### D-COMMENT-EDIT: One comment is one action

Accepted 2026-10-11 (owner). A command comment is one action: SuiteWard processes the first observed version of each comment id, once. Editing that comment afterwards, or deleting it, does not change the recorded outcome: it neither revokes nor re-processes nor creates another action. Any further action (a revocation, a new approval) is a new comment. S1 observed both edits and deletions ([s1-github](../spikes/s1-github.md), "Comment edit and delete"); M1.4 keeps every comment version and marks deletions, so the history stays visible. M1.6-A implements the rule.

### D-APPROVAL-CONTEXT: Approval binding for implementation-only changes

Accepted 2026-10-05. Consent covers the protected inventory digests, the declaration, the governing policy, and the canonical baseline. An implementation-only push keeps consent but triggers a fresh integrity assessment against the exact new source. A change to any covered input creates a new proposal revision that needs fresh approval.

### D-TRUST: Agent/human trust separation

Accepted 2026-10-05: [ADR 0027](../decisions/0027-agent-human-trust-separation.md). TOTP code on every approve and revoke command, isolated and co-located installation tiers, agents use their own GitHub identity.

### D-IDENTITY: Owner setup and identity

Accepted 2026-10-13 (owner). The owner is bound by a local setup command run on the instance itself (`suiteward setup`, through `podman exec` on the co-located target or the owner's own Session Manager session on the isolated one); no HTTP or MCP surface can set up or change the owner. Setup takes the owner's GitHub login, resolves its numeric account id through the App's installation token, and records the owner as the principal `github:<numeric id>` of kind `Human` in a new policy revision; the login is display only. Proof of identity is access to the instance, which on the isolated tier only the owner has; on the co-located tier the agent can run setup too, which is part of the reduced assurance SuiteWard reports. The trusted installation is the operator-configured `GITHUB_INSTALLATION_ID`; setup confirms it covers every configured repository. Setup also enrolls the TOTP secret (ADR 0027), records the name of the project's required test check, and records the tier the instance had at enrollment: a secret enrolled on a `co-located` instance stays within the agent's reach even if the instance is later declared `isolated`, so the effective tier is `co-located` when either the setting or the enrollment says so, and `/status` says to re-run setup when they differ. Re-running setup replaces the TOTP secret and creates a new policy revision. One owner governs the whole instance (owner answer 2026-10-13): each configured repository is one SuiteWard project, identified by the repository's numeric GitHub id (`github-repo:<id>`, stable across renames), with its own policy owned by that owner and its own required test check (`--test-check owner/name=<check>`). An approval binding includes the policy revision (`contract.NewApprovalBinding`). In M1.5 no Suite exists yet, and setup refuses to change the owner of a project that already has a Suite; Changing the owner of a project that already has a Suite stays refused in the MVP (D-COMMANDS, 2026-10-14), so a changed owner never inherits earlier authority or approvals; re-enrolling the same owner is allowed.

### D-TIER: Installation tier

Accepted 2026-10-13 (owner). The tier is declared by the deployment, not detected: the setting `INSTALLATION_TIER` is `isolated` or `co-located`, default `co-located` (never claim more assurance than configured). The remote quadlet sets `isolated`, `deploy/local.ps1` sets `co-located`. The tier is shown in `/status` and MCP `status`, recorded at setup, and stated in the check summary from M1.6.

### D-PROTECTION: Protection verification

Accepted 2026-10-13 (owner), from the M1.5-P0 measurement on the S1 test repositories (recorded in the [M1.5 page](phases/M1.5.md)). SuiteWard verifies and reports; it never changes protection (ADR 0016 amendment). It reads the effective rules of the default branch (`GET /repos/{r}/rules/branches/{branch}`, Metadata read; GitHub returns only the rules of active rulesets, at repository and organization level, so `evaluate` and `disabled` rulesets never count) and the classic branch protection (`GET /repos/{r}/branches/{branch}/protection`, Administration read: 200 configured, 404 none, 403 unreadable), with a token that requests only Metadata and Administration read. The result is a set of findings evaluated over both sources together (a requirement met by either counts):

- `missing-contract-check`: no required check `SuiteWard / Contract` pinned to the App (ruleset `integration_id` or classic `app_id` equal to the App id; absent or `-1` is not pinned);
- `missing-test-check`: the repository's configured test check is not required (before setup there is no configured test check, so this finding also says to run setup);
- `not-up-to-date`: no source that requires the `SuiteWard / Contract` check also requires branches to be up to date (D-CHECKS: the required-check rule must be strict); when no source requires the contract check, `missing-contract-check` covers it and this finding is not raised;
- `admin-bypass`: classic protection does not enforce for administrators (`enforce_admins` false).

`enforced` means no finding. Two results are exclusive instead: `enforcement-unavailable` (a private repository whose effective rules answer 403: GitHub Free does not enforce there, or the read permission is missing; the instruction names both and SuiteWard never claims protection) and `unknown` (any other read failure, including a token whose granted permissions differ, with the instruction to grant Administration read). A ruleset's bypass list is not visible to the App, with or without Administration read (measured: an existing "Repository admin" bypass answered 0 bypass actors; GitHub returns `bypass_actors` only to a caller with write access to the ruleset, which the App does not request), so every result that relies on a ruleset carries the caveat `ruleset-bypass-unverified` with the manual check that the bypass list is empty. Verification runs at setup and every 15 minutes inside the D-SYNC budget, and `/status` shows the latest result per repository.

### D-COMMANDS: PR commands, identifiers and bootstrap

Accepted 2026-10-14 (owner answers, M1.6a planning), within ADRs 0002, 0010, 0011, 0015 and 0027.

**Who is heard**
- Only the enrolled owner's comments are commands.
- A `/suiteward` comment from any other account is recorded in the command ledger and otherwise ignored: no acknowledgment, no state change, no check effect. This is the ADR 0011 amendment of 2026-10-14.
- SuiteWard's own comments are recognized by the App bot's numeric account id and never parsed.

**Command syntax**
- `/suiteward approve <ref> <code>` and `/suiteward revoke <ref> <code>` (ADR 0027).
- The first line of the comment body that starts with `/suiteward`, outside fenced code blocks, is the command; the rest of the body is ignored.
- `<ref>` is `P<n>-R<m>` (ADR 0002 and ADR 0010 examples), resolved inside the PR's project and its single Suite. `<code>` is the 6-digit TOTP code.

**TOTP**
- The code is verified against the comment's `created_at` with a tolerance of one 30-second step either side (owner answer 2026-10-14).
- A step is consumed once per owner across all commands (ADR 0027). The consumed step records the command that used it, so a retry of the same command after a crash is not a replay.

**Order and binding of a code to its command (ADR 0027)**
- Commands are processed one at a time across all repositories, ordered by the comment's `created_at` and then its comment id.
- The command order given to the domain is the GitHub comment id.
- **Sync watermark.** A comment is processed only once every enrolled repository has completed a successful synchronization cycle that began after the comment was created. "Began after" is approximated as finished at least `JOB_TIMEOUT` plus 30 s of clock skew after `created_at`.
  - Without the watermark, an agent using the owner's GitHub identity could copy the owner's code into a command on another repository, sync that repository first, and use the code before the owner's own command.
  - Cost: a repository that cannot synchronize delays every command. `/status` shows the reason.
- **Edited before observed.** A comment whose first observed version was already edited (`updated_at` differs from `created_at`) is not a command. It uses up no step and is acknowledged with the reason.
  - Without this rule, an innocuous comment edited after the owner's code appeared would carry an earlier `created_at` than the owner's command.
- **Result.** With these two rules, the first command that can use a code is the one the owner wrote. That is what ADR 0027 means by a code "bound to the command it authorizes".

**One comment is one action**
- D-COMMENT-EDIT applies: only the first observed version of a comment id is processed, once.

**Identifiers**

| Value | Form |
| --- | --- |
| Project | `github-repo:<repository id>` (D-IDENTITY) |
| Suite | `default`; one per project in the MVP |
| Integration target | `branch:<default branch>` |
| Carrier | `github-pr:<repository id>/<number>` |
| Proposal | `P<n>`, numbered per project from 1 |
| Revision | `R<m>`, numbered per proposal from 1 |
| Version | `V<n>`, numbered per Suite from 1 |
| Source command | `github-comment:<comment id>` |
| Consent operation | `consent:github-comment:<comment id>` |
| Bootstrap operation | `bootstrap:P<n>-R<m>` |

The assessment emitter is the service principal `suiteward` (kind Service).

**Bootstrap of an existing repository (ADR 0010)**
1. When a configured repository has an enrolled owner and no Suite, SuiteWard creates its Suite with no canonical, governed by the owner's current policy revision.
2. When that repository has an open PR, SuiteWard:
   - pins the default branch head as the origin;
   - proposes its protected inventory as `P1-R1`, carried by the lowest-numbered open PR;
   - posts the bootstrap summary in that PR, with the copyable `/suiteward approve P1-R1 <code>`.
3. After the owner's valid approval, SuiteWard re-reads the origin tree, records the integrity assessment for the origin, and bootstraps the first canonical `V1` without waiting for the PR to merge.
4. If the default branch has moved meanwhile, the origin stays pinned. It is not silently moved (ADR 0010).

**One instance per repository**
- Two SuiteWard instances must never watch the same repository.
- Both post as the same App, and each has its own TOTP secret. So one would reject the owner's codes and, from M1.6b, publish `failure`.
- HOST.md says so. A target that is not under verification runs without GitHub settings, or with other repositories.
- The remote host (isolated) watches the test repositories by default (owner answer, 2026-10-14). The co-located stack runs with GitHub only during its own verification.

**Owner change**
- Changing the owner of a project that already has a Suite stays refused in the MVP, as `setup` does since M1.5.
- Re-enrolling the same owner (TOTP rotation) is allowed.
- This amends D-IDENTITY's M1.6 note.

### D-CHECKS: The `SuiteWard / Contract` check

Accepted 2026-10-13 (owner), from the S1 proposals ([s1-github](../spikes/s1-github.md)): one check run named `SuiteWard / Contract` per PR head SHA, published by the App and required with the App's `integration_id` as its source; never `neutral` (it passes a required check); `in_progress` while a contract change awaits approval; one publication per (head SHA, state); only an invalid command authored by the owner sets `failure`; a command from any other account is ignored (D-COMMANDS); the required-check rule must require branches to be up to date. M1.5 verifies the protection side; M1.6 publishes.

### D-MIGRATIONS: Migration tooling

Superseded by R1. Goose, pgx and sqlc stay ([ADR 0026](../decisions/0026-versioned-postgresql-migrations.md)); the migrations were reset to a single normalized `00001` (River and the outbox were added in M1.2) described in the [persistence contract](../contracts/persistence.md).

### D-M0-COVERAGE: Coverage policy

Updated by R1. Patch coverage of 90% is required; project coverage is informational. The earlier 99% project floor and non-regression rule are removed. See the [CI guide](../ci-and-coverage.md).

## Open

- **D-DEPLOY:** remote verification host. Partly settled 2026-10-06: AWS EC2 in us-east-2 (cheapest region; close to the GitHub API), `t4g.small` Graviton (2 vCPU, 2 GiB; arm64, so images are multi-arch amd64+arm64), Ubuntu 24.04, 20 GiB encrypted gp3, 2 GiB swap, rootless Podman under an unprivileged `suiteward` user, `cloudflared` installed but not yet configured. No inbound ports and no SSH daemon; administration only through SSM Session Manager (instance role limited to `AmazonSSMManagedInstanceCore`; IMDSv2 is required; the hop limit of 1 does not stop a rootless container, see the IMDS mitigation below). Service access through Cloudflare Tunnel and Cloudflare Access on the owner's domain (service token for MCP sync/status). Deployment is pull-based: CI publishes a multi-arch image to GHCR, promotion to the deploy tag requires the owner's approval in a protected environment, and the host's Podman quadlet uses `AutoUpdate=registry`. Scale-out path: same image on ECS, PostgreSQL to RDS, artifacts to EFS or the S3 adapter. Done 2026-10-06: host `i-0f556162ae6b59b22`; cost budget `suiteward-poc-monthly` (US$25/month measured before credits, email alerts at 80% actual and 100% forecast); Cloudflare tunnel `suiteward-poc` (`09d32d23-e113-48d3-b870-ec1acda0dc69`) running as a system service on the host with hostname `suiteward-poc.magalz.space` answering 404 for everything; the agent's AWS session logged out afterwards. Remaining before S1-D: a Cloudflare Access application for the hostname (owner, Zero Trust dashboard) before any route reaches a service, the GHCR deployment workflow with its protected environment, and the owner placing the test App key on the host through Session Manager. Settled 2026-10-10 from the S1 findings ([s1-github](../spikes/s1-github.md)): CI publishes `ghcr.io/ignisdevne/suiteward` (tags `sha-<commit>` and `deploy`) with a workflow using `GITHUB_TOKEN`; the package is public so the host holds no registry credential; moving `deploy` requires the owner's approval in the `remote-poc` environment (deployment branch rule: `phase/M1.2`, `phase/M1.4` since M1.4-D and `phase/M1.5` since M1.5; `main` is added when the owner decides); the host adopts it through `AutoUpdate=registry` (the default timer is daily, so smoke runs start one update by hand); access is Session Manager only (no SSH daemon, no Tailscale), plus Cloudflare Access for any HTTP route. PostgreSQL runs as a rootless container beside the service on both targets (the image pinned by digest in CI, a named volume, the password in a Podman secret the owner creates); RDS stays the scale-out path. IMDS: a firewall rule on the host rejects `169.254.169.254` for the `suiteward` user and is re-verified with the S1 check; the SSM agent keeps its access. Cancelling a stale run waiting for approval is a human action. An agent never holds host access; the owner runs host steps in their own Session Manager session. The S1 spike service, its Podman secret and volume are removed from the host by the owner.
- **D-JOB-RETENTION:** how long finished jobs stay visible on `/status`. Until decided, River v0.49.0's cleaner defaults apply (completed and cancelled 24 hours, discarded 7 days); the outbox keeps its terminal rows. Raised by the M1.2 phase review.
- **D-INTEGRATION:** supported merge methods (merge, squash, rebase, fork, same-SHA) and exact integrated-source resolution.
- **D-RELEASE:** supported production and CLI platforms, and the AGPL only-versus-or-later grant and notices.
- **D-EXECUTION:** first executable profile and execution trust model; decided in M2 before implementation.
- **D-MANAGED-EXECUTION:** future provider-hosted or customer-infrastructure execution; nothing scheduled.
- **D-HARDENING-CONTROLS:** justified future signing, provenance and transparency controls; nothing scheduled.

## Post-MVP

- **D-QUEUE:** contract-change priority queue ([ADR 0022](../decisions/0022-contract-change-pr-priority.md)).
- **D-ARCHIVE:** encrypted backup archive format ([ADR 0019](../decisions/0019-encrypted-backups-and-instance-recovery-key.md)).
- **D-BACKUP-REMOTE:** Release backups and the 29-day contingency ([ADR 0021](../decisions/0021-release-backups-and-health-reconciliation.md)).
- **D-RESTORE:** guided restore CLI ([ADR 0020](../decisions/0020-guided-local-cli-recovery.md)).

The interim MVP backup is `pg_dump` plus a tarball of the artifact volume, with a runbook that re-bootstraps from the protected main branch.
