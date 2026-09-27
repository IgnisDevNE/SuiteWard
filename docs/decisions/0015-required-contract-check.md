# ADR 0015: Require the SuiteWard contract check before merge

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Product:** SuiteWard
- **Scope:** Mandatory PR governance check, readiness semantics, and enforcement boundaries.
- **Related:** [Exact approval](0002-exact-revision-approval.md), [bootstrap](0010-repository-bootstrap.md), [revocation](0011-approval-revocation-and-acknowledgments.md), [protected scope](0013-protected-scope-and-agent-assisted-review.md), and [post-merge promotion](0014-merge-triggered-canonical-promotion.md).

## Context

Post-merge canonical validation preserves SuiteWard's authoritative version but does not, by itself, stop unapproved test changes from entering the repository. The user requires protected test changes to block merging until the applicable authorization and integrity checks succeed.

The project's test execution remains the responsibility of its existing CI. SuiteWard's check expresses whether the protected contract is eligible for integration.

## Decision

Publish a check named `SuiteWard / Contract` and require it for PR merges into the configured integration branch. Informational-only checks are not the selected MVP experience.

A change to the protected contract blocks this requirement until its exact proposal has an eligible human approval and a valid integrity assessment. This includes protected additions, modifications, deletions, moves, and changes to the declared protection scope. Changing a test is allowed through the approved change process; it must not bypass that process.

The required check must identify the SuiteWard GitHub App as its expected source. Installing the App or publishing a check does not alone make it a merge requirement: the corresponding GitHub branch protection or ruleset must be configured. [GitHub protected branches](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches).

## User-facing outcomes

| Condition | Check behavior | Meaning and next action |
| --- | --- | --- |
| Canonical baseline exists, scope is valid, and the protected contract is unchanged | Success | The evaluated revision has no protected contract change requiring approval. |
| Protected change lacks an eligible exact approval | Pending | Show the proposal, change summary, and copyable approval command. |
| Exact proposal is approved and pre-integration integrity checks pass | Success: **Ready for integration** | SuiteWard's requirement is satisfied for the evaluated revision. Other repository checks still apply. |
| Covered inputs changed after approval | Pending new approval | Prepare the replacement revision; do not carry the old approval over to changed inputs. |
| Invalid declaration or confirmed contract-integrity failure | Failure | Explain the problem and the correction needed. |
| Evaluation cannot finish because necessary source, baseline, or service data is unavailable | Non-success / pending recovery | Do not treat incomplete assessment as an unchanged contract or issue a successful result. |
| Initial contract has not been approved and validated | Pending bootstrap | Show the accepted initial-contract review flow. |
| First-test bootstrap has eligible approval and passed its pre-integration checks | Success: **Bootstrap ready for integration** | Integration may proceed; first canonical promotion remains a post-integration operation. |

Success never means that SuiteWard executed the tests or already completed canonical promotion. Existing CI checks retain their own requirements and results.

After merge, validate the exact integrated revision and perform conditional promotion as required by ADR 0014. Report the new canonical version or unresolved promotion condition in the PR.

For an existing principal-branch baseline, preserve ADR 0010's ability to perform first promotion after approval and validation without waiting for the hosting PR's merge. Then assess that PR's own candidate contract changes separately.

## Evaluation identity and reassessment

Bind each check result to the evaluated PR source revision and record the applicable canonical baseline, proposal, policy, and covered context. GitHub check runs identify their evaluated commit using `head_sha`; the adapter must also handle the applicable integration context without conflating it with domain approval identity. [GitHub check runs API](https://docs.github.com/en/rest/checks/runs#create-a-check-run).

A new PR commit requires assessment for that commit. If it changes only implementation inputs outside the approval's covered context, the previous exact proposal approval may remain eligible; a previous check result is not silently reused as an assessment of the new commit.

Changes to the canonical baseline, governing policy, scope, or approval eligibility require reassessment of affected checks. Repeated observations with unchanged inputs must remain idempotent.

[ADR 0022](0022-contract-change-pr-priority.md) adds priority eligibility for contract-changing PRs: one PR is active per suite, others have a non-passing required check linked to it, and a transfer withdraws the former PR's published eligibility before replacement readiness. Implementation-only PRs do not wait merely because the contract-change queue is occupied. This affects SuiteWard's own check without requiring external CI reruns.

Use a non-passing result for missing approval, invalid input, or incomplete evaluation. Do not represent these conditions with `neutral` or `skipped`, since GitHub can treat those conclusions as satisfying a required check. The exact API mapping for pending and terminal conditions remains implementation work. [GitHub required status checks](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches#require-status-checks-before-merging).

## Required protection and onboarding

The connected repository must have an effective required-check rule for the intended branch and expected App source. Onboarding must distinguish connection from verified enforcement; an App installation alone is not a protected setup.

Identify bypass settings that would allow relevant actors to merge despite the check and account for them when verifying the intended protection. GitHub's default branch protection can exempt administrators unless configured otherwise, and feature availability depends on repository visibility and plan. If effective enforcement cannot be established, report incomplete protection instead of silently presenting an informational check as the required gate. [GitHub branch protection settings](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches).

[ADR 0016](0016-integrated-protection-onboarding.md) resolves the setup choice: the verified owner confirms activation within the installation flow, and SuiteWard applies and verifies the required rule using administrative write capability. Concrete API operations, remaining permissions, and verification mechanics still require implementation.

## Outages, revocation, and concurrency

New required checks that are missing or pending do not satisfy the merge requirement. An unavailable SuiteWard instance cannot invent success to keep PRs moving.

An outage does not automatically erase a successful check already published on GitHub. Likewise, a processed approval revocation changes SuiteWard's authoritative eligibility immediately, while updating a previously published check requires a separate GitHub operation. Persist and retry these publication changes; do not claim that the two systems change atomically.

Retain the existing safeguards at promotion time, even when a pre-merge check passed. A stale external success, a configured bypass, or concurrent changes must not authorize an invalid canonical promotion. Strict coordination of checks, merge, and promotion remains an unresolved implementation requirement, not a guarantee established by selecting a required check.

The mandatory check is a required product behavior to implement and verify. This planning record does not configure an actual repository or prove that every merge race is prevented.

## Acceptance criteria for implementation

1. An unapproved change to a protected test prevents satisfaction of `SuiteWard / Contract`; the configured GitHub rule blocks normal merge accordingly.
2. Protected deletions, additions, moves, exclusions, and declaration changes receive equivalent governance treatment.
3. Eligible exact approval plus valid assessment can satisfy the requirement without an independent SuiteWard test execution or a second promotion command.
4. Missing baseline, incomplete discovery, invalid configuration, or failed evaluation cannot be mistaken for an unchanged protected contract.
5. New commits and changed covered inputs receive the appropriate reassessment; approvals do not move to replacement content.
6. A result from a different actor or integration does not satisfy the source-bound requirement.
7. Pending and failed governance conditions are not encoded as `neutral` or `skipped` success equivalents.
8. Bootstrap readiness permits the first-test PR to integrate without falsely claiming that a canonical suite already exists.
9. Connection and verified enforcement are presented distinctly during setup.
10. Post-merge validation, approval revocation, and concurrent promotion safeguards remain effective independently of a previously published check.

## Remaining implementation details

- Concrete automatic rule-setup operations, remaining permission details, and effective-enforcement verification under ADR 0016.
- Check-run lifecycle, API conclusion mapping, retry policy, and concise status wording.
- Integration contexts, supported merge methods, merge queues, and strictness of branch-update requirements.
- Reconciliation of pending changes to policy, canonical baseline, revocations, and already published successful checks.
- Recovery from a repository merged through a bypass or while an external result was stale.
