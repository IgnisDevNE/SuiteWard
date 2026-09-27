# ADR 0013: Protected scope and agent-assisted review in the PR

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Product:** SuiteWard
- **Scope:** Declaring, reviewing, and changing the protected inventory.
- **Related:** [Bootstrap](0010-repository-bootstrap.md), [exact approval](0002-exact-revision-approval.md), [canonical authority](0007-canonical-contract-authority.md), [PR acknowledgments](0011-approval-revocation-and-acknowledgments.md), and [MVP authorization](0012-mvp-authorization-policy.md).

## Context

Protecting only a directory named `tests` may omit fixtures, snapshots, helpers, and configuration that influence validation. Requiring the user to identify every relevant file before seeing a suggestion increases setup work.

The user already collaborates with an agent through repository changes and PR review. SuiteWard should make scope selection understandable in that workflow while retaining authority over the approved contract outside the candidate repository.

## Decision

Use assisted discovery, a repository declaration named `.suiteward.yml`, and an exact proposal reviewed in the PR. The agent can help prepare the declaration; an authorized human approves the resulting proposal through the established command.

The repository declaration contains proposed file-selection rules. SuiteWard stores the approved rules and an exact, content-bound inventory as part of the canonical contract. The candidate declaration is not authoritative simply because it exists in a PR or was merged.

Discovery can produce a first suggestion without a pre-existing `.suiteward.yml`. The agent and user then refine the declaration in the PR. A user may also edit the file directly through the normal repository workflow.

## User and agent flow

1. SuiteWard proposes the initial scope in the first real monitored PR, following the appropriate bootstrap path in ADR 0010.
2. Its English-language comment identifies the source revision and proposal revision, summarizes tests and supporting files, and provides an expandable inventory and a copyable approval command.
3. The agent reads the proposal from the PR or supported status output, discusses the scope with the user, edits `.suiteward.yml`, and updates the PR.
4. Agent-triggered synchronization or independent periodic reconciliation reevaluates the proposed scope and covered content.
5. If the covered inputs changed, SuiteWard creates a new immutable proposal revision and publishes the revised scope, differences, and exact approval command. Repeat observations alone do not create a new revision.
6. The authorized human posts a command such as `/suiteward approve P1-R2` after reviewing that revision.
7. SuiteWard durably processes and acknowledges the command. Required validation and conditional promotion still determine when the proposal becomes canonical.

Discussion with the agent prepares a proposal; it is not the human approval consumed by SuiteWard. The agent does not post the approval using the human's privileged credentials.

A bot comment does not, by itself, start or wake an external agent. The agent must read PR comments or consult status, either in its existing workflow or when the user asks. Integration guidance should include checking for pending scope review after synchronization. This does not introduce an agent orchestration service or expand MCP beyond its accepted synchronization and status capabilities.

## Review presentation

The PR summary must make the scope understandable without requiring the user to infer it from raw YAML:

- Group selected files by purpose where known: tests, fixtures, snapshots, helpers, and relevant configuration.
- Show the applicable source revision, immutable proposal reference, and exact approval command.
- Explain additions to protection, removals from protection, and changes to protected content.
- Provide access to the complete exact inventory; an abbreviated summary must not appear to be exhaustive.
- Describe discovery as a suggestion. It does not prove that every dynamic dependency or validation input has been discovered.

For example, an initial proposal can identify `tests/**`, `fixtures/**`, `test-support/**`, and a test configuration file, with the resolved file inventory available for review. These paths illustrate the experience; they do not select a test ecosystem or fix the YAML schema.

After an adjustment, the comment identifies the replacement revision and its changes. An approval for `P1-R1` does not authorize changed covered inputs in `P1-R2`.

## Selection rules and canonical content

A rule such as `tests/**` selects files to evaluate. Each approved version still identifies exact files and content digests. A later matching file cannot silently become part of an existing immutable canonical version.

Apply the currently approved selection rules and inventory when assessing a candidate. Evaluate proposed rule changes explicitly and compare their effects with the existing contract. The candidate must not decide which existing protections SuiteWard will check.

In particular:

- Adding, changing, deleting, or moving protected content requires the applicable contract-change proposal.
- Narrowing an inclusion, adding an exclusion, or removing a protected directory is a proposed reduction of protection.
- The scope-defining declaration is itself governed even if the candidate's selection rules omit it. Removing or changing `.suiteward.yml` does not disable protection or restart bootstrap.
- New files selected by the governing or proposed rules are assessed as candidate inventory changes. They become canonical only through the normal approval, validation, and promotion flow.
- Approved content stays in SuiteWard's immutable storage; the candidate declaration cannot overwrite it.

For example, excluding `tests/security/**` in a PR produces a visible removal proposal. The existing canonical tests remain authoritative until an eligible replacement is approved, validated, and promoted.

File selection does not grant permissions or independently replace a trusted RunnerProfile. Changes to runner or policy inputs retain their respective proposal types and governing authorization rules. This ADR does not combine them into an unrestricted repository configuration mechanism.

## Preserve both bootstrap paths

For an existing repository, the baseline inventory resolves against the pinned principal-branch source in ADR 0010. The hosting PR can carry the proposed scope declaration and approval conversation, but its test-content changes are not silently imported into that baseline. Bind the exact proposed rules and pinned artifact source in the proposal and show their distinct references where applicable. Test changes in the hosting PR remain a separate proposal.

For a repository without an initial contract, scope review uses the first relevant test PR. Approval and pre-integration validation establish bootstrap readiness; canonical activation follows integration and validation of the exact integrated revision.

Neither path requires an artificial setup PR, an existing dashboard, or automatic trust in detected files.

## Alternatives and consequences

Manual configuration before discovery would make initial scope explicit but require the user to do more setup work. Automatic authoritative discovery would remove the review step but allow detection heuristics to determine the contract.

The selected flow combines a suggested starting point with normal repository edits and explicit human approval. It requires clear scope differences and reliable reconciliation as the PR changes. A separate scope-editing dashboard is not required for this flow.

## Acceptance criteria for implementation

1. Discovery can suggest an inventory before `.suiteward.yml` exists, without creating a canonical version automatically.
2. The PR identifies the source, proposed rules, complete resolved inventory, revision reference, and approval command.
3. Agent or manual declaration edits produce a reviewable scope difference; changed covered inputs require a new exact approval.
4. Repeated synchronization does not create new revisions or approvals merely because the same inputs were observed again.
5. Candidate exclusions, declaration deletion, and file moves cannot silently remove existing canonical protection.
6. Matching a selection rule does not mutate an immutable canonical version or bypass authorization for new contract content.
7. User-agent discussion and agent-originated requests cannot satisfy human approval authority.
8. Scope adjustment and proposal publication recover through periodic reconciliation without an MCP call or an automatically awakened agent.
9. Existing-baseline import remains separate from test changes in its hosting PR; first-test bootstrap still waits for integration and final validation.

## Remaining implementation details

- YAML schema, canonical rule representation, and which configuration inputs belong to each proposal type.
- Pattern semantics, case sensitivity, path normalization, symlinks, submodules, and invalid-declaration handling that preserves existing authority.
- Framework-specific discovery and dependency limitations.
- Summary update strategy, complete-inventory pagination for large repositories, and the supported agent status payload.
- Exact revision binding for declaration bytes versus normalized rules; no normalization may hide a meaningful change to covered inputs.

These details must preserve the accepted PR interaction and external canonical authority.
