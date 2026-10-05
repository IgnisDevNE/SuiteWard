# ADR 0022: One active contract-change PR per suite and explicit priority transfer

- **Date:** 2026-09-26
- **Status:** Accepted, deferred to post-MVP (phase R1, 2026-10-05); the MVP has no admission gate or priority queue. Original status: Accepted design decision; implementation and GitHub coordination validation remain pending.
- **Product:** SuiteWard
- **Scope:** Contract-change scheduling, waiting PRs, human priority transfer, and required-check reassessment.
- **Related:** [Exact approval](0002-exact-revision-approval.md), [background jobs](0005-river-background-jobs.md), [canonical authority](0007-canonical-contract-authority.md), [bootstrap](0010-repository-bootstrap.md), [processed acknowledgments](0011-approval-revocation-and-acknowledgments.md), [authorization](0012-mvp-authorization-policy.md), [promotion](0014-merge-triggered-canonical-promotion.md), and [required check](0015-required-contract-check.md).

## Context

Two PRs can propose changes against the same canonical version. Promoting one changes the baseline against which the other must be assessed. The user selected an explicit priority queue so one contract-changing PR proceeds at a time, other PRs explain what they are waiting for, and the responsible human can transfer priority.

The existing integrity MVP controls its own required check. It does not independently execute tests or need to take over the project's CI workflows to manage this order.

## Decision

Maintain one active contract-changing PR per Suite. Other PRs proposing changes to that suite wait with a non-passing `SuiteWard / Contract` check and a link to the active PR.

The first eligible proposal admitted to the durable queue takes the active position when it is free. Queue admission and selection must be serialized: polling or MCP-triggered work may discover several PRs together even when their opening timestamps differ. Persist a deterministic order and do not silently give a late discovery priority over the active PR. Detailed ordering and eligibility representation remain implementation choices.

The queue applies to protected contract changes, including changes to existing tests and additions within the governed inventory. PRs that only change implementation outside the protected contract do not consume this position; they still receive the ordinary required-check assessment. SuiteWard must inspect waiting PRs to classify their changes and keep their state current. Waiting blocks readiness for integration, not all observation or analysis.

Priority is a scheduling property, separate from approval. An active PR still needs an eligible exact approval and all ordinary integrity conditions. A waiting PR cannot satisfy the required governance check merely because an approval exists for it.

In the provider-agnostic core, queue entries reference the Suite, proposals, and external change identities. GitHub PR numbers, comment identifiers, and check-run identifiers remain adapter details.

## Active position and advancement

| Situation | Behavior |
| --- | --- |
| Active PR is awaiting approval or has an unresolved assessment | Keep the active position and explain the condition; another PR can receive priority through the explicit command below. |
| Active PR is approved and passes the current integrity assessment | Its required check may indicate readiness for integration. Keep the position. |
| Active PR is merged | Validate the exact integrated revision and attempt canonical promotion. Keep the position while that processing is pending. |
| Canonical promotion completes | Release the position and reassess the next eligible waiting PR against the resulting canonical state. |
| Active PR is closed without merge | Release the position and reassess the next eligible waiting PR. |
| A transfer is requested | Enter the transfer flow; do not declare the new PR ready merely because the command was recorded. |

Approval alone does not advance the queue. The waiting message must refer to completion of integration and canonical promotion, rather than tell the user that simply approving the first PR will release the next one.

For a waiting PR, illustrative English product text is:

```text
Waiting for PR #42 to merge and complete canonical promotion.
To request priority for this PR, comment: /suiteward prioritize
```

If the active PR was integrated but cannot be promoted, preserve the canonical version and publish the unresolved condition. Do not release competing changes as though the integration had completed successfully. A recoverable resolution path is required; the exact handling of a corrective PR while this condition holds is still an implementation/product detail to validate before release.

Bootstrap retains ADR 0010's two flows. In particular, importing an already-integrated existing baseline does not gain a new requirement to wait for the PR hosting that approval to merge. Concurrent attempts to establish the first canonical pointer remain conditional and must reconcile.

## Human priority command

An authorized human comments on the PR that should become active:

```text
/suiteward prioritize
```

The hosting PR identifies the requested target. Validate the source comment through GitHub, the responsible human's explicit SuiteWard authority, the connected project, and the target's current eligibility. Agent credentials and MCP remain limited to their existing capabilities and cannot transfer priority through administrative escalation.

This command changes priority only. It does not approve a proposal, waive an integrity condition, grant a role, or directly promote a version. Existing approvals remain recorded and must be reassessed for eligibility; a priority change alone does not rewrite consent. When covered proposal inputs or context change, apply ADR 0002's new-revision and fresh-approval requirements. Implementation-only pushes follow the existing covered-input rule.

Process repeated observations of the same command idempotently. An old or retried command must not undo a later processed priority decision. An already-active target produces a truthful no-op outcome. Reject an unauthorized, closed, already-integrated, or otherwise ineligible target with an explanation instead of silently redirecting the command to another PR. Precise admission behavior for drafts and changed scope remains open.

## Transfer flow

1. Validate and durably record the priority request, responsible principal, current and requested entries, and the pending transfer. Schedule the necessary publication work through the existing job mechanism.
2. Invalidate work based on the former scheduling state so delayed assessments or publication jobs cannot restore its eligibility after the transfer began. Coordinate worker ownership and retry ordering with the transfer record.
3. Make the former active PR's SuiteWard check non-passing for the applicable current revision and confirm that GitHub reflects that result. Account for older publications still in flight or with uncertain outcomes; a single readback is not sufficient if such a write can restore the old success afterward. If this cannot be reconciled, retain a pending transfer and do not release the replacement.
4. Reconcile whether the former PR has already merged. If it has, process that integration and its canonical outcome before completing the transfer. Do not pretend that the priority command undid the merge.
5. Revalidate the requested target's current source revision, applicable canonical baseline, proposal, approval eligibility, and integrity. A transfer never carries an old successful assessment onto a changed commit.
6. Complete the scheduling change and publish the target's actual assessment. It may become active while still awaiting approval; priority does not guarantee a passing check. Move the former unmerged PR to waiting when it remains eligible.
7. Publish the outcome in both affected PRs, including the new active PR, pending conditions, and any next action. An intermediate acknowledgment says that the transfer is being processed; it must not claim completion prematurely.

Reconcile uncertain remote outcomes before repeating mutations. Conflicting transfers must have one recorded ordering and a stable resulting state. Database transactions, job fencing/version checks, and serialized remote publication need a concrete implementation; this flow is a behavioral requirement rather than a completed concurrency protocol.

## Required check and external CI

Use the existing source-bound `SuiteWard / Contract` check and verified branch protection. GitHub Apps can update their check runs with check-write permission. Represent waiting and transfer conditions using a non-passing API state; `neutral` and `skipped` must not represent a queue wait because they can satisfy required checks. The exact API status mapping remains implementation work. [GitHub check runs](https://docs.github.com/en/rest/checks/runs#update-a-check-run), [required status checks](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches#require-status-checks-before-merging).

Reassess SuiteWard's check when priority changes. This decision does not require rerunning or cancelling the project's CI, adding Actions-write permission, or operating a test execution backend in M1. Build and test workflows continue under the project's existing configuration. Their green results do not replace SuiteWard approval or priority eligibility.

## Cross-system limits and canonical safeguards

GitHub merge and SuiteWard's database are not a single transaction. A previously green PR may start or finish merging before the demotion reaches GitHub. Confirming a remote check update does not prove that no merge is already in flight. The implementation must reconcile this case and preserve exact approval and conditional promotion; it must not advertise instantaneous or atomic priority transfer.

Promotion must validate current scheduling/transfer state as well as the expected canonical baseline and normal approval/integrity requirements. Concurrent jobs cannot both promote incompatible proposals against the same expected baseline. A stale check, delayed publication, or repository bypass must not silently authorize an invalid canonical version.

GitHub checks are associated with commits. Two PRs sharing the same commit SHA can therefore require special handling; independently named PR records do not automatically provide independent required checks. Merge queues and their integration revisions also need adapter-specific validation. These cases remain explicit implementation limits until supported behavior is demonstrated.

The product must report a merged-but-unpromoted contract honestly and provide a recovery route without rewriting canonical history. Selecting this queue does not resolve every GitHub integration race or introduce an automatic repository rollback.

## Acceptance criteria for implementation

1. Concurrent discovery admits at most one active contract-changing entry for a Suite; the ordering survives retries and restart.
2. Waiting contract-changing PRs have a non-passing SuiteWard check and identify the active PR. Implementation-only PRs do not wait merely because this queue is occupied.
3. Approval alone retains the active position; successful integration and promotion, or closure without merge, advances eligible waiting work.
4. An eligible authorized human can request priority with `/suiteward prioritize` in the desired PR, while an unauthorized actor or agent cannot.
5. Priority transfer neither grants approval nor silently discards existing approval history; changed covered inputs still require fresh exact approval.
6. The previous active PR's remote eligibility is withdrawn and confirmed before the replacement can receive a passing result. An unresolved update leaves the transfer pending.
7. A delayed job or replayed command cannot reestablish an obsolete priority or publish a success from superseded scheduling state.
8. A former active PR found merged during transfer is reconciled before the replacement proceeds; an invalid integrated contract preserves the existing canonical version and reports the issue.
9. Both affected PRs receive accurate processed outcomes, with idempotent retries and no premature completion message.
10. The next active PR is assessed against the current canonical state and exact source revision; priority does not reuse stale verification evidence.
11. Priority changes work without SuiteWard rerunning external CI or executing tests in M1.
12. Bootstrap retains its accepted existing-baseline and first-test flows.

## Remaining implementation details

- Queue admission/order, transaction constraints, job fencing, and handling of concurrent commands and external updates.
- Draft transitions, a PR that ceases to alter the contract, withdrawal/re-entry, and other eligibility changes. No automatic priority expiration or timeout-driven reassignment is introduced by this decision.
- A PR affecting multiple suites, including acquisition/release ordering and avoidance of partial-readiness deadlocks.
- Correction/recovery when the active PR merged but cannot be promoted, without creating a permanent queue blockage.
- Same-SHA PRs, forks, supported merge methods, merge-group checks, and strict gate-to-merge coordination.
- Exact check-state mapping, command processing order, retry behavior, and final comment wording.

The priority queue, human transfer command, acknowledgment behavior, and separation from test CI are accepted. Implementation, integration testing, and proof of the stated coordination behavior remain pending.

## Amendment (2026-10-05, phase R1): no admission gate in the MVP

The MVP has no admission gate, single active position, or priority queue; the decision stays accepted for post-MVP. Any approved contract-changing proposal may be promoted. Concurrent contract-changing PRs based on the same canonical version are resolved at promotion by the canonical compare-and-set: the first promotion wins and the later PR is blocked with a changed-canonical reason. It needs a new revision against the new canonical version and fresh exact approval, so parallel agent work is not serialized. A queue may return after the MVP if real use shows contention. `/suiteward prioritize` is not part of the MVP command set.
