# ADR 0016: Integrated installation and automatic protection setup

- **Date:** 2026-09-26
- **Status:** Accepted; automation/repair deferred post-MVP by the R1 amendment. Not yet implemented.
- **R1 note (2026-10-05):** the first-test-PR bootstrap path is deferred to post-MVP; only the existing-baseline path is in the MVP (see the amendment to [ADR 0010](0010-repository-bootstrap.md)).
- **Product:** SuiteWard
- **Scope:** Owner-confirmed setup of mandatory GitHub merge protection during onboarding.
- **Related:** [Hosting and GitHub synchronization](0001-self-hosted-github-synchronization.md), [bootstrap](0010-repository-bootstrap.md), [MVP authorization](0012-mvp-authorization-policy.md), and [required contract check](0015-required-contract-check.md).

## Context

The required contract check needs an effective GitHub protection rule. Asking users to leave onboarding and locate the appropriate repository settings adds setup work and opportunities for incomplete protection.

App installation grants requested permissions and repository access. It does not itself create a required-check rule. SuiteWard can perform that configuration using its API access as part of a continuous installation experience. [About using GitHub Apps](https://docs.github.com/en/apps/using-github-apps/about-using-github-apps).

## Decision

Use an integrated onboarding flow: install the App, select repositories, review the proposed protection settings, confirm activation, and let SuiteWard apply and verify the required-check rule.

The normal path does not require users to configure that rule manually in GitHub. Configuration remains an explicit action of the verified human owner in the setup interface. Installing the App or granting repository access alone does not authorize arbitrary changes to repository rules or approve a canonical contract.

The App requests repository `Administration: write` for the selected automatic branch-protection operations. GitHub's branch-protection API requires this permission for changes; reading the configuration requires administrative read access. The complete permission matrix remains subject to the concrete operations and API implementation. [GitHub branch-protection API](https://docs.github.com/en/rest/branches/branch-protection).

This accepts the additional administrative capability needed for setup. It does not expose administration through the agent's MCP interface or give agents approval authority.

## Integrated user flow

1. The administrator starts the SuiteWard instance and follows the existing instance-associated App registration flow.
2. The user installs the App, reviews its requested permissions, and selects repositories.
3. SuiteWard validates the installation association and the human's authority, then discovers the authorized repositories and their current protection configuration.
4. The setup interface presents a preview for each selected project: repository, target branch, required `SuiteWard / Contract` check, expected SuiteWard App source, and any enforcement changes needed under ADR 0015.
5. The verified owner confirms **Enable protection** for the displayed scope.
6. SuiteWard applies the necessary configuration through GitHub's API and reads back the effective state.
7. Show **Merge protection enabled** only after verifying the required check, source, branch coverage, and relevant enforcement settings. Otherwise show **Protection setup pending** with the reason and next action.

Existing required CI checks and unrelated protections must be preserved. If no change is necessary, validate the existing setup instead of creating duplicate rules.

GitHub supports redirecting the browser to a setup URL after App installation. This is an onboarding entry point, not a replacement for authenticating the user or validating the installation: a received `installation_id` alone is not trusted proof. The exact local browser and headless completion mechanism must be validated during implementation. [GitHub setup URL](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/about-the-setup-url).

## Separate setup, enforcement, and canonical authority

Track these as distinct conditions:

| Condition | What it establishes |
| --- | --- |
| App connected | SuiteWard has validated access to the selected repository. |
| Protection setup pending | The required merge protection has not yet been verified. |
| Merge protection enabled | The intended required-check configuration was verified at the recorded observation. |
| Initial contract pending or canonical contract active | The independent bootstrap/governance state defined by ADR 0010. |

Enabling the merge rule does not import tests as canonical or replace `/suiteward approve <revision>`. Bootstrap still uses the first real monitored PR and its exact human approval. The check's bootstrap-readiness semantics prevent a circular requirement for a canonical version before the first test PR can merge.

If the target branch does not exist yet, the necessary API operation is unavailable, permissions are insufficient, or the hosting plan cannot enforce the intended rule, retain a pending setup state. Do not invent a branch, create an artificial setup PR, or announce protection merely because installation succeeded.

## Administrative boundary

Only an authenticated human with the established SuiteWard administrative capability can confirm the setup action. Verify its project and installation scope using trusted state. PR comments, repository configuration, installation URL parameters, and agent requests cannot grant that capability.

The agent's MCP remains limited to synchronization and status. It may report setup progress or a pending condition but cannot activate protection, alter repository rules, or receive administrative credentials.

Protect App credentials within the trusted control plane. GitHub's administrative permission is broader than the specific setup operation; enforce the narrower allowed operation in SuiteWard's application services and audit the responsible principal and affected repository.

## Preserve existing settings and verify changes

- Read the current effective configuration before preparing the preview and again before applying changes.
- If material settings changed after the preview, present the updated proposal for confirmation instead of applying a stale replacement.
- Preserve existing required checks, source bindings, and unrelated repository protections. Do not replace the entire policy with SuiteWard defaults.
- If an existing rule conflicts with the intended protection, explain the conflict and the specific changes needed before applying them.
- Apply repeatable operations and reconcile uncertain API outcomes before retrying; retries must not accumulate duplicate rules or discard unrelated settings.
- Verify the resulting protection and record what was requested, applied, and observed. If partial application or a conflict remains, expose the pending condition rather than reporting success.

Reading, writing, and verifying GitHub settings do not create an atomic transaction across systems. Do not claim that preview freshness checks eliminate every concurrent repository edit. Concrete API choices and concurrency handling must be demonstrated during implementation.

## Networking and later reconciliation

Protection setup uses outbound GitHub API requests from SuiteWard. It does not introduce a required public webhook endpoint, tunnel, domain, or SuiteWard-operated relay into the default deployment. Browser navigation to an onboarding page is separate from GitHub delivering a webhook to the instance.

Retain periodic verification of relevant protection settings alongside normal synchronization. If SuiteWard observes that required protection is missing or no longer verifiable, update the observed state and surface the condition. A historical successful setup is not proof of the current configuration.

[ADR 0017](0017-protection-change-detection-and-confirmed-repair.md) defines the later response: detect and notify automatically, then require the responsible human to confirm a specific repair. Initial setup confirmation is not standing authorization to restore rules automatically.

## Alternatives and consequences

Assisted manual rule configuration would keep write administration out of SuiteWard, but require the user to configure each repository and wait for verification. The selected flow prioritizes lower onboarding effort and consistent setup.

The cost is a broader App permission and additional responsibility for preserving rules, handling conflicts, securing the administrative path, and verifying partial results. No standalone dashboard or administrative MCP tool is required by this choice.

## Acceptance criteria for implementation

1. A user can install the App and activate the required protection within one guided setup flow, without manually editing GitHub rules in the normal path.
2. The verified owner reviews the target repositories, branches, and proposed changes before confirming activation.
3. SuiteWard configures the source-bound required contract check and preserves unrelated protections and CI checks.
4. Repeated or uncertain setup attempts reconcile without duplicate rules or blind replacement of newer settings.
5. A repository is marked as having merge protection enabled only after effective configuration is verified.
6. Missing branches, insufficient permissions, conflicting settings, and unavailable enforcement remain visible pending conditions.
7. Installing the App and enabling its merge rule do not approve the canonical baseline or any test-change proposal.
8. Agent MCP requests and unvalidated setup URL parameters cannot exercise administrative authority.
9. The normal setup requires no public webhook delivery and preserves ADR 0001's network model.
10. Later loss of verified protection becomes visible; the product does not silently assume that setup-time confirmation authorizes every repair.

## Remaining implementation details

- Concrete branch-protection versus ruleset operations and the complete App permission matrix.
- Verified owner identity, setup sessions, and local/headless browser completion.
- First-check registration or discovery, target-branch availability, and effective-rule verification.
- Conditional API update capabilities, partial-application recovery, and concurrent configuration edits.
- Concrete protection-change detection and repair mechanics under ADR 0017; mandatory human repair confirmation is settled.

The integrated, owner-confirmed automatic setup is accepted; no live GitHub rules have been changed by recording this decision.

## Amendment (2026-10-05, phase R1): verified protection, support matrix, deferred automation

**Required checks.** Setup verifies that at least one project test check is a required status check, in addition to `SuiteWard / Contract`. If none is, SuiteWard reports a distinct failing state; the contract check alone does not make the project's tests a merge requirement.

**GitHub support matrix.** Branch protection and rulesets are unavailable for private repositories on GitHub Free. On such repositories SuiteWard reports "enforcement unavailable" and never claims the contract is protected.

| Repository | Plan | Protection |
| --- | --- | --- |
| Public | Any | Verified through branch protection or rulesets. |
| Private | Pro, Team, Enterprise | Verified through branch protection or rulesets. |
| Private | Free | "Enforcement unavailable"; SuiteWard still reports contract state. |

**Automation moved post-MVP.** Automatic application of protection rules (the owner-confirmed setup above) moves post-MVP, as does automatic repair under [ADR 0017](0017-protection-change-detection-and-confirmed-repair.md). The MVP verifies protection and gives manual instructions. This also removes the need for `Administration: write` on the App in the MVP, which matters under [ADR 0027](0027-agent-human-trust-separation.md). Protection-drift detection remains in the MVP.
