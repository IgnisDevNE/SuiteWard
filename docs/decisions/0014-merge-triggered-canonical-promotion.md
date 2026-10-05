# ADR 0014: Merge-triggered canonical promotion in the integrity MVP

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **R1 note (2026-10-05):** the first-test-PR flow and the priority-queue behavior below are deferred to post-MVP (see the amendments to [ADR 0010](0010-repository-bootstrap.md) and [ADR 0022](0022-contract-change-pr-priority.md)).
- **Product:** SuiteWard
- **Scope:** Repository acceptance, post-integration integrity checks, and canonical promotion in M1.
- **Related:** [Exact approval](0002-exact-revision-approval.md), [canonical authority](0007-canonical-contract-authority.md), [integrity before execution](0008-progressive-integrity-and-execution.md), [bootstrap](0010-repository-bootstrap.md), [revocation](0011-approval-revocation-and-acknowledgments.md), and [protected scope](0013-protected-scope-and-agent-assisted-review.md).

## Context

The integrity MVP should govern the test contract without operating its own test execution environment. Projects already choose how to review changes and run tests before merging. SuiteWard can use integration as the project's acceptance signal while preserving explicit authorization of canonical changes.

This separates acceptance by the repository's process from SuiteWard's responsibility to preserve the approved contract. A merge is not proof that tests ran, passed, or are correct.

## Decision

For M1, use a confirmed PR merge into the project's configured integration branch as the trigger to assess and promote an approved contract change. Rely on the project's own review and CI process to decide whether the implementation and tests are acceptable.

Keep the explicit, exact human approval from ADR 0002. The accepted flow is:

```text
Protected contract change
  -> exact proposal and human approval
  -> PR merge
  -> integrity validation of the integrated revision
  -> conditional canonical promotion
  -> visible outcome in the PR
```

Merge confirms integration; it does not supply a missing approval, restore a revoked approval, or authorize replacement content. Approval alone does not skip integration or final validation.

SuiteWard does not execute tests as part of this M1 flow. The default MVP does not require SuiteWard to independently orchestrate CI or duplicate the project's test-result acceptance gates before promotion. This does not weaken an explicitly configured policy requirement or permit an execution-required installation to silently accept integrity-only evidence.

## Promotion flow

1. Discover a confirmed merge through normal reconciliation. A closed but unmerged PR is not sufficient.
2. Identify the exact integrated source revision and target project/branch. Do not substitute a moving branch tip or assume that the pre-merge PR head is the integrated revision.
3. Resolve the applicable proposal and confirm that its exact human approval remains eligible under the governing policy, including revocation and covered context.
4. Resolve the protected inventory using the governing and proposed scope rules. Check that integrated contract content and all other covered inputs correspond to the approved change, including additions, modifications, removals, and scope changes.
5. Preserve already accepted canonical changes. Confirm the expected current canonical state; if another promotion has intervened, reconcile against it before attempting promotion.
6. Seal the eligible immutable version and conditionally update the canonical pointer together with the promotion and audit records.
7. Publish the outcome in the PR, identifying the new canonical version and integrated source, or explaining why promotion remains pending. Persist publication work so a posting failure does not lose the outcome.

GitHub's PR API provides integration state and merge-related references. The adapter must interpret those references according to the merge method; the domain continues to use provider-neutral integration and source identities. [GitHub REST API for pull requests](https://docs.github.com/en/rest/pulls/pulls#get-a-pull-request).

Repeated observations and job retries must not create duplicate versions or promotions. A PR that does not change the protected contract does not require a new canonical version merely because it was merged.

## Missing approval, divergence, and concurrency

If the PR was merged without an eligible approval, or integrated covered inputs differ from the approved proposal, retain the current canonical version and report the pending governance condition in the PR. Do not silently absorb the repository's new contents.

If the canonical baseline changed in the meantime, do not overwrite it using the older proposal. Reassess the resulting contract and approval binding; any changed covered inputs require a new exact approval. A retry is not permission to discard another accepted change.

[ADR 0022](0022-contract-change-pr-priority.md) establishes one active contract-changing PR per suite and explicit human priority transfer. Normal queue advancement waits for integration and canonical promotion, or closure without merge; approval alone does not release the position. Promotion must respect current scheduling/transfer state alongside these canonical and approval checks. The next PR is assessed against the resulting baseline. A merged-but-unpromotable active PR requires visible recovery rather than pretending that the queue completed normally.

A merged repository may therefore temporarily differ from SuiteWard's canonical contract. Report that condition honestly. SuiteWard does not claim to undo an already completed merge or guarantee strict gate-to-merge coordination through a database transaction.

Approval withdrawal still takes effect according to ADR 0011: a durably processed revocation can prevent a dependent promotion that has not completed; it does not retroactively rewrite a completed canonical version.

## Bootstrap compatibility

- **Existing principal-branch baseline:** the source is already integrated. After exact approval and required integrity validation, first promotion need not wait for the PR hosting the approval conversation to merge. Its candidate test changes remain separate.
- **First-test PR:** approval and pre-integration checks establish readiness. Confirmed integration and final integrity validation then permit first canonical promotion.

These exceptions preserve the two bootstrap paths in ADR 0010; merge-triggered updates do not require an artificial setup PR.

## Assurance and delivery boundary

The successful outcome means that the approved contract was integrated and promoted with the required integrity and authorization checks. It does not mean SuiteWard independently executed the tests or proved that the project's CI exercised the canonical contract faithfully.

External CI evidence, when collected, retains its source and revision identity and remains evidence rather than approval authority. A merge does not manufacture execution evidence.

Independent canonical execution remains a later M2 capability. Choosing its first ecosystem or executable RunnerProfile is deferred while the M1 governance flow is being defined and validated. M1 does not become limited to Go, Playwright, or another test framework through this deferral.

## Acceptance criteria for implementation

1. A confirmed merge with an eligible exact approval and matching integrated contract can lead to automatic promotion without a second human promotion command.
2. M1 performs integrity and authorization validation without launching a test execution backend.
3. A closed, unmerged PR or a merge into an unrelated branch cannot trigger canonical promotion for the configured integration target.
4. Missing or revoked approval, changed covered inputs, or unresolved canonical conflicts preserve the current canonical version and produce a visible explanation.
5. Validation binds the exact integrated revision rather than silently reading the latest branch tip or reusing evidence for another source.
6. Concurrent or repeated reconciliation does not duplicate promotion or erase a newer accepted contract.
7. Successful promotion and pending conditions are reported in the PR without claiming independent execution assurance.
8. Both established bootstrap paths retain their accepted activation conditions.

## Remaining implementation details

- GitHub check lifecycle and branch-protection onboarding; [ADR 0015](0015-required-contract-check.md) defines the mandatory `SuiteWard / Contract` check and its readiness semantics.
- Exact integration-reference resolution for supported merge methods and reconciliation after multiple merges.
- Recovery and review presentation when a merged contract requires renewed approval or conflict resolution.
- Strict coordination between GitHub checks, merge, and canonical promotion.
- Optional future external-CI policy gates and the later M2 execution profile.

This decision accepts the default M1 promotion flow, not a proof of the unresolved integration protocol.
