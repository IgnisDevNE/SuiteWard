# ADR 0011: Explicit approval revocation and PR acknowledgments

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Product:** SuiteWard
- **Scope:** Approval withdrawal, processed comment changes, and visible command outcomes.
- **Related:** [Exact approval](0002-exact-revision-approval.md), [synchronization](0001-self-hosted-github-synchronization.md), and [River jobs](0005-river-background-jobs.md).

## Context

A human may change their mind after approving a proposal. Periodic reconciliation can observe a command after GitHub state has changed, and cannot reconstruct every edit or deletion between observations.

The user needs an explicit withdrawal operation and a comment confirming what SuiteWard actually processed.

## Decision

Use an explicit PR command:

```text
/suiteward revoke P42-R3
```

The reference identifies the exact proposal revision. The command withdraws the authenticated author's approval for that revision; it does not implicitly grant authority over another principal's approval. Any broader administrative power requires an explicit policy decision.

A revocation becomes effective when SuiteWard durably processes it, not when the comment is posted or when an acknowledgment is successfully published.

| Observed condition | Result |
| --- | --- |
| Revocation commits before promotion | The withdrawn approval no longer satisfies policy; prevent a promotion that requires it. |
| Promotion has already committed | Preserve canonical state and promotion history. Explain that withdrawal does not undo promotion; reversal requires a separately authorized rollback proposal. |
| No active approval exists for this author and revision | Report that there was no active approval to withdraw. Preserve enough command history to prevent older commands from restoring withdrawn consent through delayed processing. |
| Invalid scope, identity, or authorization | Reject the command and explain the actionable reason without changing authority. |
| A processed approval comment is edited or deleted | Preserve the recorded approval. Require the explicit revoke command to withdraw it. |

Editing or deleting a processed revocation comment likewise does not restore approval. Reinstating consent requires a new explicit approval command evaluated against the revision's current eligibility.

A comment removed before observation may never be processed. If its body changes before first processing, evaluate only the authenticated content actually fetched; do not claim to know unseen history.

## Comment after processing

After durably recording the outcome of an approval or revocation command, SuiteWard must publish an acknowledgment comment in the same PR.

The acknowledgment must identify:

- The source command or a link to it.
- The exact proposal revision and relevant approver.
- The actual outcome, including rejection, an already-applied operation, or an already-completed promotion.
- The effect on promotion eligibility, as evaluated at processing time.
- The next action when one is required.

An approval acknowledgment includes the copyable withdrawal command. Product text remains in English. Example wording:

```text
Approval recorded for P42-R3.
To withdraw this approval, comment: /suiteward revoke P42-R3
```

For a withdrawal that removes required approval:

```text
Revocation recorded for P42-R3.
Your approval no longer counts. Promotion is awaiting valid approval.
```

For an already-promoted revision, explicitly state that the canonical version remains unchanged and that a rollback proposal is needed to reverse it. [ADR 0018](0018-corrective-pr-rollback-and-audit-history.md) defines that correction as a new PR-based proposal, exact approval, integration, and promotion of a new version while retaining history.

The message must reflect the actual policy result. If sufficient other approval remains, do not incorrectly claim that all promotion is blocked. These examples are templates to refine, not permission to report a result before it is committed.

A historical acknowledgment records the outcome at that time. It is not an unconditional claim about the PR's latest state.

## Consistency and recovery

Serialize promotion eligibility and revocation within the domain transaction boundary so a promotion cannot depend on an approval already revoked in the committed state it evaluates. The operation committed first determines whether that promotion preceded revocation. The GitHub comment timestamp alone does not decide this race.

Keep approval and revocation history immutable. Duplicate observations must not duplicate effects, and late processing of an older approve command must not undo a processed withdrawal. Retain command ordering context across retries and restarts.

Record the outcome, its audit event, and the required acknowledgment job durably in the same transaction where applicable. A publication failure does not undo revocation or permit a blocked promotion. Retry publication through the shared GitHub budget, and surface persistent publication errors locally.

Associate acknowledgment attempts with a stable operation identity and reconcile uncertain GitHub outcomes before blindly posting again. Aim to avoid duplicate bot comments without claiming atomic or exactly-once publication across PostgreSQL and GitHub. Duplicate command observations should reuse the existing acknowledgment instead of producing retry spam.

Both MCP-triggered and periodic reconciliation use these rules. No MCP call is required for a valid human command to be discovered.

## Acceptance criteria for implementation

1. Every durably processed approval or revocation command schedules an outcome comment on the same PR.
2. An approval acknowledgment names the revision and supplies its withdrawal command.
3. A timely processed revocation removes the targeted approval from promotion eligibility.
4. A concurrent promotion/revocation race has one consistent committed outcome and a truthful acknowledgment.
5. A post-promotion revocation never silently rolls back the canonical version.
6. Editing or deleting a processed command does not rewrite recorded consent.
7. Replayed or older commands cannot reinstate withdrawn consent accidentally.
8. Publication failure preserves the domain outcome and durable retry intent.
9. Unauthorized withdrawal cannot affect another principal's approval.

## Remaining implementation details

- Final acknowledgment formatting and reconciliation marker.
- Command ordering representation and transaction isolation strategy.
- Implement the MVP owner/agent boundaries in [ADR 0012](0012-mvp-authorization-policy.md); future organizational/admin delegation remains open.
- Corrective-PR rollback tooling under ADR 0018 and backup/restore implementation under [ADR 0019](0019-encrypted-backups-and-instance-recovery-key.md); administrative-access recovery remains open.

Explicit withdrawal, its effective point, and the PR acknowledgment requirement are accepted.

