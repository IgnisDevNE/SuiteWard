# ADR 0018: Corrective PR rollback and permanent canonical history

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Product:** SuiteWard
- **Scope:** Same-PR contract changes, correction of already-promoted changes, and preservation of canonical audit history.
- **Related:** [Exact approval](0002-exact-revision-approval.md), [canonical authority](0007-canonical-contract-authority.md), [approval revocation](0011-approval-revocation-and-acknowledgments.md), [protected scope](0013-protected-scope-and-agent-assisted-review.md), [promotion](0014-merge-triggered-canonical-promotion.md), and [required check](0015-required-contract-check.md).

## Context

Changing tests is a normal part of development. SuiteWard must let the authorized human review and accept a proposed contract change in the PR that contains it, while preserving previous versions for audit.

A different situation arises when an unwanted change has already been approved, integrated, and promoted. Correcting that contract must preserve the same authority and integrity requirements without rewriting its history or leaving the repository on different content through a pointer-only rollback.

## Decision

Use the existing proposal, approval, merge, and promotion flow for both ordinary contract evolution and corrective rollback:

| Situation | Accepted flow |
| --- | --- |
| A PR is open and proposes protected changes | Review and approve the exact change in that same PR. No separate approval-only PR is required. |
| An unwanted change was already integrated and promoted | Prepare a corrective PR, which may restore historical content or revert selected changes, then follow the normal exact approval and promotion flow. |

Retain every historical canonical version and the artifacts and records needed to reconstruct and audit it. A correction creates a new immutable canonical version; it does not erase the version being corrected.

The MVP does not provide a separate command that directly resets the canonical pointer to an older version without the corrective repository integration and validation flow.

## Changes accepted in the original open PR

1. Compare the proposed protected contract with the current canonical baseline, including the governing scope and covered context.
2. Publish a clear explanation and complete reviewable difference: additions, modifications, deletions, moves, supporting files, and changes to protection scope.
3. Keep `SuiteWard / Contract` non-passing while the proposed change lacks an eligible exact approval.
4. Let the authorized human accept the displayed revision using `/suiteward approve <proposal-revision-reference>` in that PR. The existing command remains the MVP approval mechanism; the phrase **Accept changes** does not introduce a new generic approval or button protocol.
5. After approval and the required pre-integration assessment, report readiness for integration.
6. After merge, validate the exact integrated contract and covered inputs, then conditionally promote the new immutable version. Include new tests within the approved scope and inventory.

Acceptance applies to the reviewed revision, not every future state of the PR. Later changes to covered inputs require a replacement proposal revision and fresh approval. Approval does not immediately replace the canonical version before integration.

This clarifies the existing normal workflow; it does not require a second PR merely because tests differ from the canonical contract.

## Corrective rollback after promotion

1. The user or agent prepares a subsequent PR containing the intended correction. It may restore selected historical files, adjust the current tests, or use a revert as a starting point.
2. SuiteWard evaluates the correction against the current canonical baseline, rather than treating a historical version as the governing authority.
3. Show the correction's exact effect, including any later changes that would be removed. Do not silently discard valid additions or fixes introduced since the historical version.
4. Require a new exact human approval under the currently governing policy. An old approval of similar or identical file content does not authorize the present correction in a different context.
5. Apply the normal required check, merge, integrated-revision integrity validation, and conditional promotion requirements.
6. Create a new immutable version and promotion record, retaining the corrected version and all earlier history. Publish the actual result in the corrective PR.

The corrective change uses the existing `TEST_SUITE` proposal path. It does not require a new proposal type, separate promotion command, independent test executor, or special approval bypass. A suitable open follow-up PR may carry the correction; no additional PR solely to record SuiteWard approval is required.

GitHub can create a new PR to revert a merged PR, but conflicts or the original merge circumstances may require reverting individual commits. A revert is therefore a preparation aid, not proof that the resulting contract is the desired historical content or that later valid changes are preserved. [GitHub: reverting a pull request](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/reverting-a-pull-request).

If another valid promotion changes the canonical baseline while the correction is pending, reconcile before promotion. An older correction must not overwrite the newer canonical state or inherit approval for changed reconciled inputs.

## Immutable audit history and retention

Preserve all historical canonical versions, including:

- Their exact manifests, approved scope, content identity, and referenced artifacts needed to reconstruct the protected contract.
- Associated proposal revisions, approvals and revocations, integrated-source references, promotion records, and audit context explaining each transition.
- The relationship between a corrective change, its current baseline, and the version or change it intends to correct where identified.

Historical canonical versions do not expire automatically because of age or because they are no longer current. Audit records are not rewritten to make a correction appear as though the unwanted promotion never happened.

Identical content may reuse immutable content-addressed objects. Reuse must not collapse distinct versions, approvals, or promotion history, and storage cleanup must retain every object referenced by a historical canonical version.

This retention requirement does not require keeping every unpromoted candidate, temporary artifact, or operational log forever. Their lifecycle remains a separate implementation decision and must not remove evidence needed to reconstruct canonical history.

Preserving history in the instance is distinct from backup and disaster recovery. [ADR 0019](0019-encrypted-backups-and-instance-recovery-key.md) defines the accepted two-copy backup workflow, encrypted repository history, external instance recovery kit, ledger with shared distinct file contents, and restore boundaries. Its physical format and remaining operating details are still open; they must preserve this history requirement.

## Revocation and authority boundaries

Preserve ADR 0011's behavior: a revocation processed before promotion can remove an approval needed by that promotion. Revoking an approval after a completed promotion does not undo the canonical version. A functional correction follows the corrective PR flow above.

The human's explicit consent authorizes contract evolution, including intentionally changed expectations. An agent or candidate repository cannot authorize that evolution alone. Retaining history improves traceability; it does not establish that an approved change is semantically correct or that the tests remain sufficient.

M1 continues to validate integrity and authorization without independently executing the tests. Runner and policy changes retain their own governing proposal and authorization requirements.

## Alternatives and consequences

A dedicated PR for every test-contract approval would add work to ordinary open PRs without changing the authority model. The same-PR review flow avoids that extra step.

A direct historical-pointer rollback could change SuiteWard's contract while the integrated repository still contains the unwanted version. It would need an additional coordination and authorization workflow. The MVP instead reuses the corrective PR path.

Retaining all canonical history requires storage capacity and backup coverage. Content-addressed reuse can reduce duplicate storage without weakening audit identity or retention.

## Acceptance criteria for implementation

1. An open PR can have its protected changes reviewed and approved in that same PR, including new tests in the approved inventory.
2. Missing approval keeps the required check non-passing, and changed covered inputs cannot inherit a prior revision's approval.
3. Correcting an already-promoted change requires a new proposal and exact approval relative to the current canonical baseline.
4. The correction becomes canonical only after its integration, final integrity validation, and conditional promotion.
5. A correction produces a new immutable version and retains all earlier canonical versions and their reconstructable audit history.
6. Historical approvals do not authorize a new corrective proposal solely because it reuses old file content.
7. Concurrent accepted changes are reconciled rather than silently discarded by a historical restore.
8. Revocation after completed promotion does not directly roll back the canonical pointer.
9. Referenced historical artifacts are not expired or deleted by routine cleanup; deduplication preserves every logical version and its records.

## Remaining implementation details

- History browsing, diffs against historical versions, and preparation or export of content for a corrective PR.
- Representation of correction intent and links to prior versions without a separate proposal type.
- Capacity reporting and cleanup of unreferenced or transient data while retaining all canonical history.
- Physical backup format and restore implementation under ADR 0019, including verification that restored history is complete.
- Existing unresolved concurrency and integration coordination mechanisms.

Same-PR acceptance, corrective-PR rollback, and preservation of all historical canonical versions are accepted design requirements.
