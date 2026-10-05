# ADR 0010: Bootstrap existing repositories and the first test PR

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Product:** SuiteWard
- **Scope:** Initial authority, initial contract selection, and first canonical promotion.
- **Related:** [Hosting and synchronization](0001-self-hosted-github-synchronization.md), [exact approval](0002-exact-revision-approval.md), [canonical authority](0007-canonical-contract-authority.md), and [assurance levels](0008-progressive-integrity-and-execution.md).

## Context

A connected repository may already contain tests on its principal branch, or its first test contract may arrive in a new PR. Repository age alone does not identify which bootstrap path applies.

SuiteWard must establish its first canonical version without allowing candidate files or an installation event to grant themselves authority. The user should reuse the existing GitHub approval workflow.

## Decision

Use one explicit bootstrap proposal process with two possible initial sources. Prepare the bootstrap when the repository is connected and use the first real monitored PR as the approval surface. Do not require an artificial PR solely for SuiteWard initialization.

| Case | Proposed source | First canonical activation |
| --- | --- | --- |
| An existing test contract is present | A pinned commit from the repository's principal branch | After human approval and required validation of the already-integrated source |
| Tests are introduced by the first relevant PR | An exact revision of that PR | After human approval, integration, validation of the integrated revision, and first promotion |

The PR carrying an approval comment and the commit supplying the initial contract are separate references. Store both explicitly.

## Initial authority

During instance setup, the administrator establishes the initial approval authority by linking a verified GitHub identity to the initial approver role and recording the initial policy.

App installation, PR authorship, repository administrator status, and candidate configuration do not implicitly grant SuiteWard approval authority. The first proposal is evaluated under the policy established outside candidate control.

The detailed identity-verification UI and later organizational policies remain implementation work. [ADR 0012](0012-mvp-authorization-policy.md) defines the accepted MVP capabilities and approval threshold.

## Existing repository with tests

1. Inspect the selected principal-branch commit and propose an inventory of tests and relevant supporting files, including fixtures, snapshots, helpers, and configuration.
2. Pin the source revision. Detection produces a suggestion; let the user adjust the protected scope before approving it.
3. Publish the bootstrap summary in the first real monitored PR. Clearly identify the inventory, source commit, immutable proposal revision, and copyable command such as `/suiteward approve P1-R1`.
4. Validate the human identity, authority, and exact revision using the normal approval service.
5. Validate the approved contract and its source/context under the required assurance mode, then perform the first canonical promotion.

Because this source is already integrated, canonical activation need not wait for the PR carrying the approval comment to merge.

If that PR also changes protected tests or other contract inputs, represent those changes in a separate proposal. Importing the principal-branch baseline does not approve the PR's candidate changes.

If the principal branch advances while bootstrap is pending, reassess whether the approved source and context remain eligible. Do not silently move the proposal to a newer commit or reinterpret an old approval. Changes to covered inputs require a new proposal revision.

## New repository or repository without an initial test contract

1. Keep the project awaiting its initial contract; do not manufacture an empty canonical suite.
2. Detect the first relevant PR containing proposed tests through MCP-triggered or periodic synchronization.
3. Prepare a bootstrap proposal from its exact source revision, with a reviewable inventory and copyable approval command.
4. After human approval and required pre-integration gates, publish a check that explicitly means **Bootstrap ready for integration**.
5. After integration, validate the exact integrated revision and approved contract, then promote the first canonical version.

The readiness check is a pre-integration assessment. It does not state that a canonical version already exists or that promotion has completed. Keep that distinction visible so the first PR can proceed without circularly requiring an existing canonical suite.

M1 validates integrity and governance. M2 adds canonical execution when required by policy. A successful integrity assessment must not be presented as independent test execution. Evidence from a different commit cannot satisfy the final integrated-revision validation.

A fully empty GitHub repository needs an initial branch/commit before its first PR between branches; a minimal README can provide that starting point. That starting commit does not establish a test contract. [GitHub PR creation](https://docs.github.com/en/pull-requests/how-tos/create-pull-requests/creating-a-pull-request).

## Common invariants and recovery

- Missing tests leave the project awaiting a contract. They do not produce a successful canonical-contract assessment.
- Changes to approved content or context require a new proposal revision and approval.
- Initial promotion conditionally requires that the suite has no current canonical version: `expected_current = null`.
- Only one concurrent bootstrap can establish that pointer. A losing attempt must reconcile against the newly established baseline.
- Repeat observations, approvals, and jobs remain idempotent and auditable.
- Periodic reconciliation must complete discovery and follow-up work even if the agent omits MCP calls.
- Missing approval, failed validation, revoked access, or unavailable infrastructure remain visible conditions; they are not bypassed to finish bootstrap.
- A pre-integration check does not prove strict coordination of GitHub merge and canonical promotion. Preserve the integration limitations in ADR 0007 until the coordination protocol is demonstrated.

## Alternatives and consequences

Automatic import upon App installation would reduce explicit review but allow detection or candidate-controlled configuration to determine authority.

A dedicated setup PR would provide a separate review surface but add an extra repository step. The selected flow reuses a real PR and clearly labels which source supplies the initial contract.

The first monitored PR can therefore include both a baseline import and a separate contract-change proposal. Their summaries must make those scopes understandable rather than combine their approvals.

## Acceptance criteria for implementation

1. Existing principal-branch tests produce an explicit, pinned bootstrap proposal.
2. Changes in the PR hosting that proposal are excluded from the baseline import and require their own approval when applicable.
3. No canonical suite is created solely because the App was installed or tests were detected.
4. A project without tests remains awaiting its initial contract until a suitable proposal is approved.
5. A first-test PR can reach bootstrap readiness before merge, with canonical activation only after final validation and promotion.
6. New approved inputs cannot inherit an older revision's approval.
7. Concurrent initial promotions cannot establish conflicting initial contracts.
8. Both scenarios recover through periodic reconciliation without an MCP trigger.

## Remaining implementation details

- Detection rules and detailed summaries for ambiguous inventories; [ADR 0013](0013-protected-scope-and-agent-assisted-review.md) defines the accepted protected-scope editing and PR review flow.
- Initial identity verification and setup UI.
- Mapping these lifecycle steps to final domain states and GitHub check presentation.
- Existing open decisions on external CI gates, assurance profiles, and strict integration coordination.

The two bootstrap paths and first-real-PR approval experience are accepted.

## Amendment (2026-10-05, phase R1): first-test-PR path deferred

The first-test-PR bootstrap path (a repository without an initial test contract) is deferred to post-MVP. The existing-baseline bootstrap above remains in the MVP.
