# Decision register

- **Updated:** 2026-10-05 (phase R1)

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

- **D-DEPLOY:** remote verification host (VPS provider or home server, OS, size), private access method (Tailscale or SSH tunnel), image registry (recommended: GHCR published by the bot), approval-gated deployment environment, and who provisions the host and places the App key. Needed before S1-D. Recommendation: small Ubuntu 24.04 VPS with Podman, Tailscale, no inbound ports, owner-provisioned.
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
