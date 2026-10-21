# ADR 0027: Agent/human trust separation (D-TRUST)

- **Date:** 2026-10-05
- **Status:** Accepted and implemented: owner enrollment and the tiers in M1.5, TOTP verification of approvals in M1.6a, released in `v0.1.0`. Amended 2026-10-20 (security review, phase SH).
- **Product:** SuiteWard
- **Scope:** How SuiteWard distinguishes the verified human owner from AI agents that share the owner's machine.
- **Amends:** [ADR 0002](0002-exact-revision-approval.md) (command syntax) and [ADR 0012](0012-mvp-authorization-policy.md) (credential separation).
- **Related:** [ADR 0001](0001-self-hosted-github-synchronization.md), [ADR 0011](0011-approval-revocation-and-acknowledgments.md), [ADR 0016](0016-integrated-protection-onboarding.md).

## Context

The target user is a solo developer working with AI agents. In the common setup the agent runs as the same OS user as the human and as the SuiteWard instance. From there it can:

- use the human's `gh` credentials to post `/suiteward approve`;
- read the GitHub App private key, which may hold `Administration: write`;
- reach the PostgreSQL database and the artifact volume.

A container run under the same OS user is not isolation: a rootless `podman exec` reaches it. Checking that the approving GitHub account is the verified owner therefore does not prove that the owner, rather than an agent holding the owner's credentials, typed the comment.

## Decision

### Owner confirmation by TOTP

Every `/suiteward approve <ref> <code>` and `/suiteward revoke <ref> <code>` requires a current TOTP code from the verified owner.

- A code is consumed once per owner and time step across all commands (global single use, not per command). The code appears in a public PR comment, so it must not be reusable for a different command. A replayed code is rejected.
  - **Amended 2026-10-20 (owner, security review).** The code is consumed by the comment that used it: a step is recorded once per owner with the comment that consumed it, and only a retry of that same comment after a crash is not a replay. Only the enrolled owner's numeric GitHub account id counts; a login, a display name or another account never does.
- A code is also bound to the command it authorizes (verb and reference). The binding and the global single use are separate checks.
- Validity is evaluated against the GitHub comment's `created_at` with a stated tolerance (for example one step either side), not against processing time, so polling delay does not invalidate a code.
- Commands are processed in comment order, so the owner's own comment consumes the code before any later comment that copies it.
- The TOTP secret is enrolled at owner setup and stored only inside the instance, outside the repository and, on isolated installations, outside the agent's reach. On co-located installations the agent can reach it, and SuiteWard labels the assurance accordingly.
  - **At rest (accepted 2026-10-20, owner).** The secret is stored in the instance's PostgreSQL database, in a column that is not encrypted. This is accepted for the MVP: whoever reads the database reads the secret, which is the trust the tiers already describe, and a backup holds it ([backup runbook](../backup.md)). Encrypting it with a host key is a candidate for M3, together with the encrypted backups of [ADR 0019](0019-encrypted-backups-and-instance-recovery-key.md).
- A command without a valid code records no approval or revocation and receives an explicit rejection acknowledgment.
- **A command applies to its own pull request (amended 2026-10-20).** An approve or a revoke names a proposal, and the pull request that carries that proposal is the only one where it counts. A command that references a proposal carried by another pull request is refused with the `other-pull` acknowledgment before the code is checked, so it consumes no step and grants nothing.
- **Closed pull requests (amended 2026-10-20).** An approve, revoke or skip that the synchronization first observed after its pull request stopped being open is ignored: no step, no consent and no acknowledgment. A command the synchronization observed while the pull request was listed open is still decided after the pull request closes. Both times come from the synchronization's own clock, never from GitHub's `created_at`.

### Installation tiers

- **Isolated:** the instance runs under another OS user, in a VM, or on another host, or the agent is sandboxed without access to the container runtime and keyring. This tier gives the full assurance.
- **Co-located:** supported, but SuiteWard reports that the App key and database are reachable by the agent and labels its assurance accordingly. It does not claim protection it cannot provide.

### Agent identity

Agents should use their own GitHub identity, never the owner's credentials.

## Consequences

- Amends ADR 0002: the command syntax gains the code. Amends ADR 0012: separate credentials become concrete, with the TOTP secret as the approval credential held outside the agent's reach on isolated installations.
- Implemented in M1.5 (owner identity and setup) and M1.6a (approval processing), and hardened in phase SH (see the [threat model](../security/threat-model.md)).
- Does not protect against a compromised host administrator, or against an agent that can read the TOTP secret on a co-located install; the co-located label states this.
- Owner enrollment adds a setup step and an authenticator app for the owner.
