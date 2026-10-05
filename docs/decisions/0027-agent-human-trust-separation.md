# ADR 0027: Agent/human trust separation (D-TRUST)

- **Date:** 2026-10-05
- **Status:** Accepted; implemented in roadmap phases M1.5 and M1.6.
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

- A code is single-use per time window: a replayed code is rejected.
- A code is bound to the command it authorizes (verb and reference); it cannot be reused for a different command in the same window.
- The TOTP secret is enrolled at owner setup and stored only inside the instance, outside the repository and outside the agent's reach.
- A command without a valid code records no approval or revocation and receives an explicit rejection acknowledgment.

### Installation tiers

- **Isolated:** the instance runs under another OS user, in a VM, or on another host, or the agent is sandboxed without access to the container runtime and keyring. This tier gives the full assurance.
- **Co-located:** supported, but SuiteWard reports that the App key and database are reachable by the agent and labels its assurance accordingly. It does not claim protection it cannot provide.

### Agent identity

Agents should use their own GitHub identity, never the owner's credentials.

## Consequences

- Amends ADR 0002: the command syntax gains the code. Amends ADR 0012: separate credentials become concrete, with the TOTP secret as the approval credential held outside the agent's reach.
- Implemented in roadmap phases M1.5 (owner identity and setup) and M1.6 (approval processing).
- Does not protect against a compromised host administrator, or against an agent that can read the TOTP secret on a co-located install; the co-located label states this.
- Owner enrollment adds a setup step and an authenticator app for the owner.
