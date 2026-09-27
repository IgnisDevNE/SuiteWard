# ADR 0012: Minimal authorization policy for the individual-developer MVP

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Product:** SuiteWard
- **Scope:** Initial human and agent capabilities, approval threshold, and PR authorship.
- **Related:** [Canonical authority](0007-canonical-contract-authority.md), [bootstrap](0010-repository-bootstrap.md), [exact approval](0002-exact-revision-approval.md), and [revocation](0011-approval-revocation-and-acknowledgments.md).

## Context

The initial audience is an individual developer working with AI agents. Requiring a second person for every contract change would prevent the intended solo workflow.

Human approval and candidate implementation still need distinct authority. GitHub repository access alone must not determine who can change the canonical contract.

## Decision

Expose two initial user-facing authorization profiles:

| Profile | Capabilities |
| --- | --- |
| Human project owner | Administer the project, approve exact eligible proposal revisions, and revoke their own approvals. |
| Agent | Implement repository changes, present proposals, request synchronization, and inspect permitted results. |

The human project owner combines project administration and approval in the MVP. The verified identity and initial authority are established through the bootstrap process in ADR 0010.

One eligible approval from the authorized human is sufficient. The human may approve a PR they authored. PR authorship does not disqualify that approval, and authorship alone does not grant approval authority.

Keep the following boundaries:

- Repository write or administrator permission on GitHub does not automatically confer SuiteWard approval or administration privileges.
- An agent can propose a change through the connected PR workflow, but cannot approve it, withdraw someone else's consent, grant roles, or rewrite the canonical pointer.
- The local MCP interface keeps its synchronization and status capabilities; this decision does not add privileged approval or administration tools.
- The human owner may request contract-change priority transfer using the PR command in [ADR 0022](0022-contract-change-pr-priority.md). It is a scheduling action, not approval; agent MCP and credentials do not gain this authority.
- SuiteWard performs promotion only when required approvals, verification, integration validation, and policy conditions are satisfied.
- Project administration does not provide a normal workflow for bypassing contract governance.
- A policy change is evaluated and authorized under the governing policy before replacement. A proposal cannot lower its own approval threshold or grant itself authority.

Service principals used internally for GitHub integration, publication, and execution keep narrowly scoped operational capabilities. They are not additional human approvers.

## Credential and identity boundary

Credentials that can exercise human approval authority must remain outside the agent's access.

The system checks authenticated GitHub identity and its explicit SuiteWard authorization. It cannot prove that a person manually wrote a comment merely from the login name or text. A token or session that lets an agent post approval commands as the authorized human would violate the intended separation.

The implementation must separate the agent's integration credentials and identity from the approving identity wherever those credentials permit privileged command submission. Local placement alone does not establish that separation.

This policy relies on the credential and execution boundaries of the threat model. It does not claim to resist the trusted host administrator deliberately exposing approval credentials or rewriting storage.

## Consequences and alternatives

The initial product remains usable by one person, with one approval and a single project-owner profile to configure.

A mandatory independent reviewer or a ban on approval by the PR author was considered unsuitable for the initial audience. Such controls may become optional organizational policies later.

Teams, configurable multi-person quorum, delegated administrators, and administrative withdrawal of other people's approvals are not introduced by this decision. The domain can accommodate future roles without making their UI or behavior part of the MVP.

Human approval expresses authorization of the exact contract revision; it does not establish that tests are sufficient or replace required verification.

## Acceptance criteria for implementation

1. One authorized, eligible human approval satisfies the MVP approval threshold.
2. A PR authored by that same human remains eligible for their approval.
3. An otherwise unregistered GitHub repository administrator cannot approve through repository permissions alone.
4. Agent credentials and the MCP interface cannot grant approval or perform administrative escalation.
5. Proposal and policy changes cannot authorize themselves under a newly proposed weaker policy.
6. Revocation affects the author's approval under ADR 0011; broader withdrawal power is not inferred.
7. Promotion still requires all applicable non-approval gates and the canonical state transition.

## Open implementation details

- Policy serialization and initial owner configuration UI.
- Concrete credential/identity separation for agent PR creation and human approval.
- Owner replacement, loss-of-access recovery, and future organizational roles.
- Internal service-principal capabilities and their credential lifecycle.

The MVP profiles, one-approval threshold, and approval by an authorized PR author are accepted.

