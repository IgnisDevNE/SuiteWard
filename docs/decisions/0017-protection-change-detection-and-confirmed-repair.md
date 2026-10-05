# ADR 0017: Detect protection changes and require confirmation to repair

- **Date:** 2026-09-26
- **Status:** Accepted; automation/repair deferred post-MVP by the R1 amendment. Not yet implemented.
- **Product:** SuiteWard
- **Scope:** Detection, notification, and repair of required merge protection after setup.
- **Related:** [Periodic synchronization](0001-self-hosted-github-synchronization.md), [MVP authorization](0012-mvp-authorization-policy.md), [canonical promotion](0014-merge-triggered-canonical-promotion.md), [required contract check](0015-required-contract-check.md), and [integrated setup](0016-integrated-protection-onboarding.md).

## Context

GitHub protection can change after SuiteWard has successfully configured it. A required check may be removed, its expected source changed, or enforcement weakened. Separately, access failures or API outages can prevent SuiteWard from determining the current state.

The owner may have intentionally changed repository administration settings. Automatic detection should make the loss of protection visible without silently reversing an administrative decision.

## Decision

Detect relevant protection changes automatically, notify the responsible user, and require explicit confirmation before SuiteWard repairs the configuration.

Use independent periodic reconciliation for detection, preserving the existing request budget and retry behavior. Detection does not depend on an agent invoking MCP. The integrated installation confirmation in ADR 0016 does not authorize subsequent automatic repairs.

The normal repair path reuses the administrative setup interface: the responsible human chooses **Repair protection**, reviews the proposed changes, and confirms. SuiteWard then applies and verifies the authorized repair while preserving other rules.

## Observed protection state

| State | Meaning | Product behavior |
| --- | --- | --- |
| **Merge protection enabled** | The intended effective protection was verified at the recorded observation. | Show when it was verified; continue reconciliation. |
| **Protection needs repair** | A completed inspection confirms that the required protection is missing or insufficient. | Explain the specific difference and offer the repair flow. |
| **Protection status unknown** | An access problem, API failure, incomplete response, or other verification gap prevents a reliable determination. | Explain the verification problem, retain the last known observation as history, and retry appropriately. |

Assess the effective requirement for the configured branch and expected App source, including relevant enforcement settings. A cosmetic configuration change or a different rule arrangement with equivalent verified protection need not produce a repair request.

Do not treat an unsuccessful API request as proof that someone removed a rule. Do not present historical success as a current verified state when verification is unavailable. Keep observation time and reason available to the user.

Existing setup-pending states from ADR 0016 continue to cover repositories whose initial protection was never established.

## Notification behavior

- Show the changed protection state and reason in the local setup/administration surface.
- Publish an understandable notice in affected monitored PRs when repository access and GitHub availability allow it.
- Describe the specific protection gap or verification problem, the observation time, and the next step.
- Notify on meaningful state changes and recovery. Repeated polling of the same condition must not create repetitive PR comments.
- Persist publication work and reconcile uncertain delivery outcomes before retrying. A failure to post a notice must not lose the underlying condition or become permission to repair automatically.

Notification is constrained by actual access: if the App cannot reach or comment on the repository, keep the condition visible locally and retry eligible publication later. This decision does not add an email, chat, or other notification integration.

Once the required rule is removed, a failing check alone does not reestablish the merge requirement. SuiteWard must therefore describe the observed enforcement gap rather than claim that every affected PR remains blocked. Required-check enforcement depends on GitHub's configured protection. [GitHub required status checks](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches#require-status-checks-before-merging).

Detection occurs on observation, not necessarily at the moment of the external change. Existing polling delays and cross-system coordination limits remain applicable.

## Confirmed repair flow

1. The authenticated, authorized human opens **Repair protection** for the affected project.
2. SuiteWard reads the current effective configuration and prepares a preview identifying the repository, branch, required check, expected App source, and proposed changes.
3. The human reviews and confirms that specific repair. A status query, acknowledgement of a notice, or earlier installation confirmation is not repair authorization.
4. SuiteWard rechecks the relevant configuration before applying the operation. If material inputs changed, prepare a revised preview for confirmation instead of applying a stale replacement.
5. Apply the authorized changes while preserving unrelated protections and required CI checks, using ADR 0016's repeatable operations and conflict handling.
6. Read back and verify the result. Report **Merge protection enabled** only when protection is established; otherwise retain the appropriate repair-needed or unknown state and explain the remaining condition.
7. Record the responsible principal, confirmed scope, configuration observations, application outcome, and verification result. Publish the recovery or unresolved result without duplicate notices.

A retry of the same authorized operation does not require the human to repeat confirmation solely because delivery or processing was interrupted. Revalidate authorization and scope, reconcile whether the change already applied, and require a new confirmation if the proposed repair materially changes. Confirmation does not grant an indefinite background repair mandate.

If the owner restores protection directly in GitHub, normal reconciliation can verify recovery and clear the outstanding condition without issuing a redundant write or requiring a separate SuiteWard repair confirmation.

## Authority and canonical contract boundaries

The agent's MCP remains limited to synchronization and status. It can surface the condition to the user but cannot confirm or perform administrative repair. Administrative credentials remain within the trusted setup/control-plane boundary.

Protection configuration, test-contract approval, and canonical promotion remain distinct operations. Repair does not approve a pending test change, revoke an approval, import repository contents, or rewrite a canonical version.

This decision does not add or waive the canonical-promotion conditions defined in ADR 0014. Missing authorization, changed covered inputs, or unavailable information required for promotion still prevent a valid promotion. No additional automatic promotion-freeze policy is introduced solely by this repair workflow.

## Alternatives and consequences

Automatic repair would reduce user intervention but could reverse an intentional administrative change. Notification without an integrated repair action would preserve control while pushing configuration work back onto the user.

The selected flow combines automatic detection with an owner-confirmed repair that SuiteWard applies and verifies. Protection may remain absent until the owner acts; the product must show that condition honestly and maintain its independent canonical-authority safeguards.

## Acceptance criteria for implementation

1. Relevant loss or weakening of protection is detected through periodic reconciliation without an MCP trigger.
2. Confirmed protection gaps and unavailable verification produce distinct states with meaningful observation history.
3. The local interface and reachable affected PRs receive actionable notices without one new comment per poll.
4. SuiteWard does not change repository rules in response to detection alone or reuse installation confirmation as standing repair authorization.
5. An authorized human can preview and confirm repair in the existing administrative flow; agent MCP cannot grant that confirmation.
6. Repairs preserve unrelated rules, handle changed previews and uncertain results, and verify success before announcing restored protection.
7. Retries of an unchanged confirmed operation remain repeatable, while a materially different repair requires fresh confirmation.
8. Direct external restoration is recognized without unnecessary configuration writes.
9. Repair and its notifications do not modify canonical contract authority or substitute for exact test-change approval.

## Remaining implementation details

- Effective-protection comparison across supported GitHub rule types and merge contexts.
- Observation freshness, transient-error handling, and notification coalescing without fixed polling intervals in this ADR.
- Notice lifecycle, persistence identifiers, and which monitored PRs are affected by each branch-level condition.
- Repair-session identity, concurrency controls, and the concrete configuration API operations.
- Corrective-PR rollback and history tooling under [ADR 0018](0018-corrective-pr-rollback-and-audit-history.md), backup/restore implementation under [ADR 0019](0019-encrypted-backups-and-instance-recovery-key.md), and administrative-access recovery; these remain separate from repairing GitHub protection.

The automatic detection, notice, and mandatory human repair confirmation are accepted; implementation remains pending.

## Amendment (2026-10-05, phase R1): repair moves post-MVP

Detection, the distinct unknown state, and notices remain in the MVP. Confirmed repair through the setup interface moves post-MVP; the MVP reports the gap and gives manual instructions. See the amendment in [ADR 0016](0016-integrated-protection-onboarding.md).
