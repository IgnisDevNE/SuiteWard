# Decision register

- **Updated:** 2026-10-10 (phase M1.2: D-DEPLOY settled from S1)

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

### D-APPROVAL-CONTEXT: Approval binding for implementation-only changes

Accepted 2026-10-05. Consent covers the protected inventory digests, the declaration, the governing policy, and the canonical baseline. An implementation-only push keeps consent but triggers a fresh integrity assessment against the exact new source. A change to any covered input creates a new proposal revision that needs fresh approval.

### D-TRUST: Agent/human trust separation

Accepted 2026-10-05: [ADR 0027](../decisions/0027-agent-human-trust-separation.md). TOTP code on every approve and revoke command, isolated and co-located installation tiers, agents use their own GitHub identity.

### D-MIGRATIONS: Migration tooling

Superseded by R1. Goose, pgx and sqlc stay ([ADR 0026](../decisions/0026-versioned-postgresql-migrations.md)); the migrations are reset to a single normalized `00001` described in the [persistence contract](../contracts/persistence.md).

### D-M0-COVERAGE: Coverage policy

Updated by R1. Patch coverage of 90% is required; project coverage is informational. The earlier 99% project floor and non-regression rule are removed. See the [CI guide](../ci-and-coverage.md).

## Open

- **D-DEPLOY:** remote verification host. Partly settled 2026-10-06: AWS EC2 in us-east-2 (cheapest region; close to the GitHub API), `t4g.small` Graviton (2 vCPU, 2 GiB; arm64, so images are multi-arch amd64+arm64), Ubuntu 24.04, 20 GiB encrypted gp3, 2 GiB swap, rootless Podman under an unprivileged `suiteward` user, `cloudflared` installed but not yet configured. No inbound ports and no SSH daemon; administration only through SSM Session Manager (instance role limited to `AmazonSSMManagedInstanceCore`; IMDSv2 is required; the hop limit of 1 does not stop a rootless container, see the IMDS mitigation below). Service access through Cloudflare Tunnel and Cloudflare Access on the owner's domain (service token for MCP sync/status). Deployment is pull-based: CI publishes a multi-arch image to GHCR, promotion to the deploy tag requires the owner's approval in a protected environment, and the host's Podman quadlet uses `AutoUpdate=registry`. Scale-out path: same image on ECS, PostgreSQL to RDS, artifacts to EFS or the S3 adapter. Done 2026-10-06: host `i-0f556162ae6b59b22`; cost budget `suiteward-poc-monthly` (US$25/month measured before credits, email alerts at 80% actual and 100% forecast); Cloudflare tunnel `suiteward-poc` (`09d32d23-e113-48d3-b870-ec1acda0dc69`) running as a system service on the host with hostname `suiteward-poc.magalz.space` answering 404 for everything; the agent's AWS session logged out afterwards. Remaining before S1-D: a Cloudflare Access application for the hostname (owner, Zero Trust dashboard) before any route reaches a service, the GHCR deployment workflow with its protected environment, and the owner placing the test App key on the host through Session Manager. Settled 2026-10-10 from the S1 findings ([s1-github](../spikes/s1-github.md)): CI publishes `ghcr.io/ignisdevne/suiteward` (tags `sha-<commit>` and `deploy`) with a workflow using `GITHUB_TOKEN`; the package is public so the host holds no registry credential; moving `deploy` requires the owner's approval in the `remote-poc` environment (deployment branch rule: `phase/M1.2` only; `main` is added when the owner decides); the host adopts it through `AutoUpdate=registry` (the default timer is daily, so smoke runs start one update by hand); access is Session Manager only (no SSH daemon, no Tailscale), plus Cloudflare Access for any HTTP route. PostgreSQL runs as a rootless container beside the service on both targets (the image pinned by digest in CI, a named volume, the password in a Podman secret the owner creates); RDS stays the scale-out path. IMDS: a firewall rule on the host rejects `169.254.169.254` for the `suiteward` user and is re-verified with the S1 check; the SSM agent keeps its access. Cancelling a stale run waiting for approval is a human action. An agent never holds host access; the owner runs host steps in their own Session Manager session. The S1 spike service, its Podman secret and volume are removed from the host by the owner.
- **D-GITHUB-ACCESS:** map each operation to minimal GitHub App permissions and the supported host and token lifecycle, without public webhooks. Administration write is not needed in the MVP (ADR 0016 amendment).
- **D-IDENTITY:** initial owner proof, local/headless setup sessions, trusted App installation association, and policy serialization; builds on ADR 0027.
- **D-PROTECTION:** how to read and compare effective branch protection and rulesets, including the GitHub support matrix; the MVP verifies and gives manual instructions.
- **D-CHECKS:** `SuiteWard / Contract` check identity, source/context binding, state mapping, and stale-publication handling.
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
