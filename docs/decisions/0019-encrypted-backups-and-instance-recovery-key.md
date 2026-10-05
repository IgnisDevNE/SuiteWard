# ADR 0019: Encrypted backups and an instance recovery key

- **Date:** 2026-09-26
- **Status:** Accepted, deferred to post-MVP (phase R1, 2026-10-05). Original status: Accepted design decision; the primary remote destination is updated by [ADR 0021](0021-release-backups-and-health-reconciliation.md). Physical format and implementation details remain open. Not yet implemented.
- **Product:** SuiteWard
- **Scope:** Local recovery copy, encrypted versioned repository backup, one external recovery kit per instance, recovery references, and restore boundaries.
- **Related:** [Hosting](0001-self-hosted-github-synchronization.md), [background jobs](0005-river-background-jobs.md), [canonical authority](0007-canonical-contract-authority.md), [authorization](0012-mvp-authorization-policy.md), [promotion](0014-merge-triggered-canonical-promotion.md), and [history retention](0018-corrective-pr-rollback-and-audit-history.md).

## Context

The self-hosted instance retains canonical contracts and their audit history. A container restart, damaged application state, or loss of the host must have an explicit recovery path. A backup that cannot be decrypted after the host is lost does not provide that path.

The user selected a local recovery copy managed by another container, an encrypted versioned remote backup, and one recovery kit for all repositories belonging to the instance. The initial remote destination was a committed repository file; ADR 0021 replaces that default with dedicated Release assets and retains Git as a proposed contingency destination. The selected logical payload combines a ledger describing each version with a shared collection of distinct file contents. New versions reuse unchanged contents; additions contribute new content, and modifications preserve the previous content for historical recovery.

## Accepted direction

| Concern | Direction |
| --- | --- |
| Local recovery | A backup service/container keeps a recovery file in its own persistent backup volume, separate from the primary data volume. |
| Remote recovery | Each project has encrypted versioned backups published as dedicated Release assets under ADR 0021. Git file publication is a proposed fallback. |
| Trigger | Queue background backup work after a successful canonical promotion. Periodic backup also covers relevant state changes that occur without promotion. |
| Recovery key | One dedicated recovery key pair per SuiteWard instance can cover all its project archives. The human keeps the private key in an external recovery kit. |
| Runtime key material | Normal backup encryption needs only the public key. The private recovery key is supplied by the owner when recovery is needed. |
| Logical payload | A self-contained project archive with version manifests/ledger, required governance records, and one copy of each distinct referenced file content. |
| Visible recovery point | PR records identify the canonical contract and, when available, the exact completed backup and its SHA-256 digest. |
| Failure reporting | Expose backup health through local status/MCP and report persistent incidents through a deduplicated GitHub issue when the integration is available. |
| Restore | Restore into an isolated instance with external writes disabled, verify completeness, and reconcile before resuming normal operation. |

These are product requirements, not evidence of a working backup implementation or a guarantee of zero data loss.

## Local and repository copies

The backup file must live on a persistent volume rather than only in the backup container's writable layer. Docker volumes survive the lifecycle of a container but normally remain on the host. This local copy supports recovery from primary-state damage; it does not independently survive loss of the same disk or host. [Docker volumes](https://docs.docker.com/engine/storage/volumes/).

The repository copy must be successfully uploaded and verified before it is reported as available off-host. A file that exists only in a local checkout does not meet this requirement. A gitignored local export may be an additional convenience but is no longer the selected remote-backup design.

For Release storage, a receipt identifies the exact release and asset along with its snapshot and encrypted-file digest. For a Git fallback, use a stable file path whose updates occur in new commits and retain the exact repository, commit, and path. Do not amend or force-update away preceding backup revisions as part of normal rotation. A moving latest file or release is insufficient for historical recovery.

The exact Release namespace and Git fallback branch/path remain open. Backup writes must not weaken the protected development branch, require a governance bypass, re-enter the canonical inventory, or trigger recursive backup and promotion loops. A dedicated backup branch is a candidate, not a selected implementation.

Repository archives are scoped to that project. They must not contain other projects' records, the instance recovery private key, or instance-wide administrative credentials. Encryption with the same recipient key does not remove these scope boundaries.

A project archive and a full-instance backup have different coverage. [ADR 0020](0020-guided-local-cli-recovery.md) selects guided reconstruction for the MVP: restore project data, reconfigure the instance, and reestablish operational credentials through a local CLI. A private administrator archive is a possible future extension. The private recovery key alone does not recreate the GitHub App's credentials or prove current owner authority. Detailed recredentialing and owner-verification mechanisms still require implementation and validation.

## One external recovery kit per instance

Use a dedicated recovery key rather than requiring a separate kit for every repository. All current project archives can be encrypted to its public key. The owner stores the private key outside the SuiteWard host, for example in a password manager with an independent offline copy, and verifies it through a small decryption/restore exercise during setup.

The kit identifies the instance and key and explains how to locate and restore backups. It does not need to be regenerated after every promotion or when an additional repository joins the instance. Public-key encryption such as age supports this separation between encryption recipients and private decryption identities; the exact dependency and archive encoding remain implementation choices. [age documentation](https://github.com/FiloSottile/age).

The private key can decrypt every archive encrypted to it; this is the deliberate scope of the instance-wide recovery key. Loss of all external copies makes those encrypted archives unrecoverable. The encrypted local copy also requires the external kit to restore; the second container makes the archive readily available but does not provide unattended decryption. A key rotation must retain the ability to decrypt historical archives, through preserved old identities or verified re-encryption; changing the public key does not update old files. One kit may therefore need to preserve multiple historical identities after rotation.

The recovery recipient is trusted administrative configuration. A candidate `.suiteward.yml`, PR, or agent MCP call cannot redirect backup encryption to a different key. Recovery-key possession is not a normal approval, repair, or administration credential.

## Archive coverage and accepted logical payload

ADR 0018 requires reconstructable historical canonical versions. The recovery design must preserve manifests, protected scope, governing policies, relevant principal/identity references, version relationships, the required proposal/approval/revocation/promotion audit records, and the bytes referenced by those versions at the recorded recovery point.

A hash ledger identifies and verifies content; recovery also needs the referenced bytes. Include those bytes in the same project archive so that recovery does not depend on finding test files in old source commits or retrieving preceding backup archives.

**Accepted payload:** store a version ledger, manifests mapping protected paths to content hashes and required file attributes, the project's required metadata/audit records, and one copy of every distinct referenced file content. Each version has an exact reconstructable inventory without a separate full copy of the suite's bytes.

| Change | Ledger/manifest update | Content stored |
| --- | --- | --- |
| Existing file unchanged | Reference its existing hash in the new version. | No additional copy. |
| New protected file | Add its path and hash to the new version. | Add its bytes only if that content hash is not already present. |
| Existing file modified | Reference the new hash; previous manifests retain the old hash. | Add the new content if absent; retain the old content for historical versions. |
| File removed from the protected inventory | Omit it from the new version's inventory; retain prior manifests. | Retain content referenced by historical canonical versions. |
| File renamed without changing content | Record the new path in the new manifest. | Reuse the existing content. |
| Historical content restored | Reference the existing historical hash in the new version. | Reuse the object; the new version and approval remain distinct. |

For example, the symbolic hashes below describe three versions:

```text
v1: a_test.go -> H1
v2: a_test.go -> H1, b_test.go -> H2
v3: a_test.go -> H3, b_test.go -> H2

Shared contents: H1, H2, H3 (one copy each)
```

The content collection grows by previously unseen contents, while immutable manifests and audit records preserve the history. Source test files may still be modified or removed through the existing approval flow; append-only history does not make source files unchangeable or authorize additions automatically.

The initial content unit is a protected file, consistent with the existing manifest model. Adding a test case inside an existing file changes that file's content. The archive preserves the previous and new file contents; deduplication at individual test-case, line, or chunk level is not required by this decision. No language-specific test parser is introduced for backup.

Collect and deduplicate the content, compress it, and then encrypt the result. The latest completed archive contains the unique contents and records needed to reconstruct all canonical versions within its stated coverage using the recovery kit. Ordinary text often compresses well, but fixtures, snapshots, binary supporting files, and an increasing number of distinct historical contents prevent any general promise that archives stay tiny. Compression settings and measurable size budgets remain open.

Logical additions to this object collection do not yet prescribe an in-place append operation on an encrypted file or guarantee incremental uploads. The physical archive format remains open. When Git fallback is used, changing the same encrypted filename does not keep repository history at the size of one file. If each publication freshly encrypts the whole archive, capacity planning must not rely on Git finding useful deltas between those archives. For illustration only, 1,000 distinct encrypted snapshots of 2 MiB each represent about 2 GiB before repository overhead. Internal deduplication reduces each archive, not necessarily duplication between separately encrypted archives. The default Release-asset destination avoids placing these bytes in Git history, but still needs archive storage capacity.

For Git fallback, GitHub warns for ordinary Git files above 50 MiB and blocks files above 100 MiB. The implementation must measure archive and history growth, report unsupported sizes clearly, and preserve existing backups on upload failure. It must never silently switch to an incomplete hashes-only backup to fit a limit. ADR 0021 covers the primary Release destination; larger-project and incremental-format behavior remains open. [GitHub large-file limits](https://docs.github.com/en/repositories/working-with-files/managing-large-files/about-large-files-on-github).

## Consistency, publication, and recovery receipts

1. Durably schedule backup work with the promotion transaction using the accepted background-job mechanism. Periodic work also captures changes such as later revocations and administrative updates; the interval is not fixed here.
2. Establish a consistent metadata recovery point, identify all content objects required by that state, and verify their availability and digests. Do not archive a live database directory as though it were a consistent logical snapshot.
3. Record the actual recovery point and covered versions in the archive manifest. A delayed job may capture a later state than its triggering promotion; the trigger alone must not determine the coverage claim.
4. Build and validate the archive, then encrypt it once. Copy those exact encrypted bytes to each destination for that project archive, checking the digest at each destination. Any separate full-instance archive has its own identity and coverage.
5. Keep the prior completed copy until the replacement is complete. Retries reuse or reconcile the intended snapshot and remote publication rather than produce unbounded duplicate releases, assets, or fallback commits.
6. Publish an external receipt containing archive identity, coverage, encrypted-byte SHA-256, and immutable retrieval reference. Do not put the final archive's own digest inside that same archive.

PostgreSQL provides consistent logical database exports, but that alone does not snapshot external content objects or all global database configuration. The implementation must coordinate its chosen metadata export with the content-addressed store and restore prerequisites. [PostgreSQL pg_dump](https://www.postgresql.org/docs/current/app-pgdump.html).

PR comments must distinguish contract identity from backup identity. A contract digest does not equal an encrypted archive digest, and neither is interchangeable with a Git commit ID. The intended receipt includes:

```text
Canonical version: <version>
Contract digest: sha256:<digest>
Recovery snapshot: <snapshot-id>
Recovery point: <recorded-state-reference>
Backup SHA-256: <encrypted-file-digest>
Backup location: <repository>, <release-id>, <asset-id>, <asset-name>
```

Before the corresponding snapshot is complete, show `Backup pending` or explicitly identify the last available snapshot and its coverage. Do not invent a future hash or imply that an older snapshot includes the current state. Publish the verified receipt afterward without delaying time-sensitive approval or revocation acknowledgments. Files referenced by historical receipts must remain retrievable under the backup retention design.

For Git fallback, the receipt instead records the immutable commit and file path. A later archive can reconstruct an earlier canonical version from its retained manifest and objects. That does not reproduce the earlier encrypted archive's bytes or SHA-256. Retrieving the exact archive referenced by an old receipt still requires that stored asset or Git revision, or a verified relocation of the same bytes.

Asynchronous publication leaves a window between a promotion and its verified backup. Durable scheduling does not eliminate data loss if the source storage is lost in that window. Failure to create a backup must not rewrite a completed canonical promotion as if it never happened.

## Failure visibility and restore

Report destination-specific status, the last verified recovery point, and actionable failures. MCP exposes this information to a connected agent; it does not guarantee waking an absent agent. Maintain one issue per persistent incident, updating it on meaningful changes and recovery without exposing secrets. A completely unavailable host cannot send its own failure report; guaranteed dead-host notification would require an independent observer.

Restore begins with a deliberate owner/administrator action. Select the archive and trusted recovery receipt, supply the external private key, verify the encrypted-byte hash, decrypt, validate the format and internal manifest, and check all required content and record relationships. A hash obtained only from the same untrusted replacement file does not prove its origin. Successful decryption alone does not establish who produced a public-key-encrypted archive.

Restore with workers and external writes disabled. Establish required credentials and current owner access, reconcile GitHub and pending work, and only then resume publication or promotion. Do not blindly replay queued jobs or reinstate an old approval whose later revocation may fall outside the recovered snapshot. Unresolved authority requires fresh verification or approval. Report the recovery point and any unrecovered interval honestly.

Disaster recovery restores recorded state; it does not replace ADR 0018's corrective-PR workflow for intentionally changing a functioning canonical contract.

## Acceptance criteria for implementation

1. One external recovery kit decrypts backups from two projects in the same instance without placing its private key in normal runtime storage.
2. Project archives contain only the intended project's recoverable state; cross-project data and instance administrative secrets are excluded.
3. A clean environment can recover every canonical version claimed by the archive, including the referenced content and required audit records.
4. Corruption, missing objects, unknown formats, and a digest mismatch are detected before enabling external effects.
5. Remote publication preserves retrievable preceding archives and their receipts, without bypassing development-branch protection or creating backup loops.
6. A partially failed upload leaves a usable previous copy and accurate per-destination status; retries do not create duplicate effects without reconciliation.
7. Pending and completed backups have distinct visible states and accurate coverage; revocation acknowledgments do not wait for backup completion.
8. Recovery from the local copy and the repository copy is exercised independently, including use of the external kit after primary runtime state is unavailable.
9. Restored pending authority and jobs are reconciled before normal operation resumes.
10. Repeated references to identical file content across paths or versions store those bytes once per archive while preserving distinct manifests, versions, and approvals.
11. Additions, modifications, removals, renames, and reuse of historical content follow the table above; every covered canonical version remains reconstructable from the completed archive without previous archives or historical source commits.

## Remaining decisions

- Implementation of guided instance reconstruction and recredentialing under ADR 0020; a separate archive containing instance administrative secrets is deferred.
- Release namespace and Git fallback branch/path, final App permissions, publication coordination, and protections for historical archives under ADR 0021.
- Archive format, consistent export/import mechanism, compression choice, size budgets, and behavior when a project outgrows ordinary Git storage.
- Backup cadence, state-change triggers, maximum recovery gap, and retention of completed snapshots and transient files.
- Owner-access recovery, key generation/delivery, rotation UI, and verification of the recovery kit.
- Notification thresholds and issue placement for instance-level failures; the `suiteward restore` interface direction is selected in ADR 0020, with detailed prompts and packaging still open.

The two-copy workflow, single external recovery kit per instance, and self-contained ledger plus distinct file contents are accepted. ADR 0021 updates the primary remote destination to Release assets. The physical archive format and remaining operational details are still open.

## Amendment (2026-10-05, phase R1): deferred to post-MVP

Encrypted archives, the recovery kit, and recovery receipts are deferred; the decision stays accepted. The MVP backup is `pg_dump` plus a tarball of the artifact volume, with a runbook that re-bootstraps the instance from the protected main branch. The archive design above is the target for the post-MVP backup work.
