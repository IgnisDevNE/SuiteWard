# ADR 0021: Dedicated backup releases and periodic health reconciliation

- **Date:** 2026-09-26
- **Status:** Accepted, deferred to post-MVP (phase R1, 2026-10-05). Original status: Accepted design decision, including the 29-day escalation rule for state awaiting its first verified remote coverage. Not implemented.
- **Product:** SuiteWard
- **Scope:** Release assets as the primary remote destination, independent backup publication, health checks, and proposed Git fallback/cleanup.
- **Updates:** The default committed-file destination in [ADR 0019](0019-encrypted-backups-and-instance-recovery-key.md). Its archive contents, encryption, recovery kit, local copy, and history requirements remain in force.
- **Related:** [Background jobs](0005-river-background-jobs.md), [canonical authority](0007-canonical-contract-authority.md), [history retention](0018-corrective-pr-rollback-and-audit-history.md), and [local restore](0020-guided-local-cli-recovery.md).

## Context

The user considered downloadable CI artifacts as an alternative to committing encrypted archives to Git. Actions artifacts expire and require a workflow upload path; the canonical backup originates in SuiteWard's local state. The user accepted direct publication as GitHub Release assets and, after clarification, selected SuiteWard's own backups between normal product releases.

The user also requested periodic checking, a repository fallback proposed through a PR, and cleanup when Release storage makes that fallback redundant. With independent backup releases, the accepted 29-day clock measures the oldest recovery-relevant state change still awaiting verified remote coverage. New PR activity does not reset the clock. File addition and cleanup remain reviewable proposals.

## Accepted direction

1. Keep the separate persistent local backup copy defined in ADR 0019.
2. Use encrypted assets in dedicated SuiteWard backup releases as the primary remote destination in the same repository. Routine backup publication no longer creates commits containing the archive.
3. Publish backup snapshots independently of the product's release schedule, after canonical promotions and for the periodic checkpoints already required by ADR 0019. A product release is not required to preserve a new canonical version off-host.
4. Generate the archive in SuiteWard from a consistent canonical/audit recovery point, encrypt once, and publish those same bytes. Repository CI does not acquire canonical authority or need the recovery private key.
5. Periodically reconcile remote backup availability, integrity, coverage, and publication failures using the existing scheduler. Do not depend on PR activity or an agent invoking MCP.
6. Support reviewable Git fallback and subsequent cleanup proposals when needed. Do not automatically merge these PRs or bypass repository protection.

The default committed archive described by ADR 0019 is superseded by Release assets. Git is the reviewable contingency destination at the accepted escalation threshold; its exact branch/path remains open.

## Publication and identity

Use a recognizable namespace for backup releases and unique snapshot asset identities, separate from software version names. Backup publication must not select a backup as the product's latest release or edit product release notes/assets. The exact tag naming and grouping strategy remain implementation details.

Create the backup release in a draft/preparation state, attach its complete snapshot and recovery metadata, and publish only after validation. This is compatible with GitHub's immutable-release workflow, which requires attaching assets before publication; it must not rely on adding a backup to an already published immutable product release. Do not weaken a repository's immutable-release setting. [GitHub immutable releases](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases).

Upload directly from the instance through the GitHub API, preserving the outbound-only deployment model. Release assets have their own upload/download APIs and do not require execution in a GitHub Actions job. [Release asset API](https://docs.github.com/en/rest/releases/assets).

Publishing a release alone is not successful backup completion. Verify the intended asset's availability and encrypted-byte SHA-256, bind its project and snapshot coverage, and store its immutable identifying references. A receipt includes the repository, release/tag and asset IDs, asset name, snapshot ID, recovery point, canonical coverage, and archive SHA-256. A generic link to the repository's latest release is insufficient.

Preserve previously referenced backup files under ADR 0019's recovery-receipt requirements. Retry partially completed publication by reconciling the intended snapshot; do not replace completed snapshot assets or create repeated releases merely because a request timed out. The external API and local transaction do not provide exactly-once execution.

Release assets do not use the Actions artifact retention clock. They remain administratively deletable through their release lifecycle, so this design does not promise indestructible or unlimited storage. GitHub currently limits one release to 1,000 assets and each asset to less than 2 GiB; define capacity checks and grouping without silently dropping history. [About releases](https://docs.github.com/en/repositories/releasing-projects-on-github/about-releases). Actions artifacts remain unsuitable as the sole long-term remote copy because they expire and are removed when their workflow run is deleted. [Artifact retention](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/download-workflow-artifacts), [artifact lifecycle](https://docs.github.com/en/actions/concepts/workflows-and-actions/workflow-artifacts).

## Periodic health reconciliation

Track local snapshot creation and remote publication as separate states. The scheduler checks:

- The last verified remote snapshot, its recovery point, covered canonical versions, and relevant governance checkpoint.
- Canonical promotions and other durable recovery-relevant changes not yet covered by a verified remote snapshot.
- Whether the release/asset is still reachable and its identity and integrity remain consistent with the recorded receipt.
- Failed or incomplete uploads, missing assets, denied access, size failures, and pending contingency proposals.

Distinguish a confirmed missing/corrupt asset from an API outage or lost permission that makes verification unavailable. Report meaningful state changes through existing status/MCP and incident notifications, with retries and no repeated issue/PR spam. Full download verification frequency and cheaper intermediate checks remain implementation details; metadata alone must not be reported as a fresh end-to-end integrity check.

A quiet repository with a valid covering snapshot is healthy even if its last PR or product release is old. A newly published software release does not resolve a missing backup unless its intended backup copy has actually been verified. Asynchronous publication still has a recovery gap, and downtime of the entire host can prevent both checks and notifications.

## Accepted 29-day escalation rule

The user's original suggestion was a repository fallback after 29 days since the last PR without a release. With independent backup releases, normal product-release inactivity no longer indicates a missing backup.

Use 29 days as the escalation threshold for the oldest recovery-relevant state change still awaiting its first verified remote backup. New PRs or later promotions must not reset that outstanding gap. Report publication failure as soon as detected and retry; do not wait 29 days to warn. Advance or clear the outstanding-gap clock only when verified publication covers the corresponding changes.

Propose the Git fallback when reconciliation observes that the uncovered interval has reached 29 days. Preserve that interval across restarts; after downtime, reconcile an already overdue case without restarting the clock. This is a proposal threshold, not a guarantee that a human will merge the PR by then or that a remote snapshot exists throughout the interval. Loss of a previously verified remote copy must also be reported immediately; its precise escalation clock remains to be defined. No threshold-based fallback is needed merely because a healthy repository has been inactive.

This trigger is accepted for state awaiting its first verified remote coverage. The 29 days are SuiteWard's escalation policy, not a GitHub Release expiration period.

## Git fallback and cleanup proposals

Prepare a fallback from the current verified local snapshot, including current canonical history and the necessary governance state. Downloading and recommitting an older Release asset can preserve those older bytes, but must not be represented as backing up later promotions that the archive never contained. If the current source is unavailable, show the older snapshot's actual recovery point and the gap.

When permissions and capacity allow, create or update one reviewable PR containing the encrypted fallback file and a clear receipt. Respect existing protected-branch requirements and exclude backup artifacts from the protected test inventory and recursive backup triggers. A proposal branch contains a provisional remote copy, but an open PR is not proof that the configured durable destination has been accepted and preserved.

When Release storage is healthy again, propose removal of the fallback file from the current repository tree only after verifying the replacement coverage and preservation of every exact archive still referenced by recovery receipts. A newer logical snapshot does not reproduce an older encrypted archive's digest. Preserve the referenced bytes in an accessible retained location and record any locator change before cleanup.

Cleanup uses an ordinary reviewable PR. Do not auto-merge, rewrite Git history, delete prior canonical records, or remove the only retrievable copy of a referenced archive. Deleting a file in a later commit does not remove its older blobs from Git history; this fallback is not a way to recover the space already committed.

## GitHub App permissions and operational limits

The integration needs repository content-write capability for Release management and Git fallback file changes, plus pull-request-write capability to open the proposed fallback/cleanup PRs. Content writes are repository permissions, not a guarantee of access restricted to a backup path; SuiteWard's own authorization and repository rules must constrain its behavior. [Contents API](https://docs.github.com/en/rest/repos/contents#create-or-update-file-contents), [PR creation API](https://docs.github.com/en/rest/pulls/pulls#create-a-pull-request), [Releases API](https://docs.github.com/en/rest/releases/releases#create-a-release).

These are permission requirements for the planned feature, not a claim that the App is already installed or granted them. The full permission matrix and setup consent remain to be finalized. Some tag/release operations involving workflow changes may require additional workflow-write capability; validate the chosen namespace/tag strategy rather than silently broadening permissions. This decision does not authorize editing workflows.

Technical capability to write files is not a bypass of branch protection or authority to merge proposals. If permissions, repository policy, or file-size limits block fallback, retain the existing copy, show the failure, and provide the owner with the recovery action. Do not silently drop data to fit a limit.

## Restore behavior

`suiteward restore` discovers SuiteWard backup releases and selects the newest valid snapshot for the requested project and recovery point, rather than blindly trusting the repository's latest release. It can also recover from an identified Git fallback revision or local archive. All paths retain the key handling, receipt verification, owner verification, isolated import, and reconciliation requirements of ADR 0020.

## Acceptance criteria for implementation

1. A canonical promotion can obtain a verified remote backup without any new product release or CI run.
2. The local and remote copies of a given snapshot have the same encrypted-byte digest.
3. Backup releases are distinguishable from software releases and do not displace the intended latest software release.
4. Publication works with immutable releases by completing the assets before publication, preserving existing immutability settings.
5. Periodic checks detect confirmed missing/corrupt backups and distinguish those from unknown access/availability states.
6. New PR activity does not conceal an older uncovered recovery gap; old PR activity does not make a valid covering snapshot unhealthy.
7. A fallback proposal uses current recoverable state or explicitly reports older coverage, respects protection, and does not create duplicate proposals on every poll.
8. Cleanup preserves exact referenced archives and waits for verified replacement storage; it does not rewrite Git history or auto-merge.
9. Restore locates snapshots by backup identity and validates their coverage and digest before activation.
10. At 29 days of unresolved first-time remote coverage, reconciliation prepares one fallback proposal when access and capacity allow. Later PRs, later promotions, and service restarts do not postpone an older outstanding gap; earlier publication failures are still reported promptly.

## Remaining decisions

- Define the escalation clock for loss of a formerly verified remote copy; the 29-day clock for state awaiting its first remote coverage is settled.
- Exact release/tag/asset naming and grouping, fallback branch/path, and capacity behavior.
- Verification cadence, API budgets, integrity re-download frequency, and notification thresholds.
- Final App permission matrix and tag strategy, including interaction with repository rules and workflow-related permissions.
- Retention and locator migration for physical archives referenced by old receipts, while preserving logical canonical history.

Dedicated Release assets, publication independent of product releases, periodic health reconciliation, reviewable contingency proposals, and the 29-day escalation clock for state awaiting its first verified remote coverage are accepted.

## Amendment (2026-10-05, phase R1): deferred to post-MVP

Release-asset backups, health reconciliation, and the 29-day Git contingency are deferred; the decision stays accepted. See the interim MVP backup in the amendment to [ADR 0019](0019-encrypted-backups-and-instance-recovery-key.md).
