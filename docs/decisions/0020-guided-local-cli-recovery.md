# ADR 0020: Guided reconstruction through a local CLI

- **Date:** 2026-09-26
- **Status:** Accepted design direction; not yet implemented. Detailed prompts, flags, and runtime packaging remain open.
- **Product:** SuiteWard
- **Scope:** Local guided restore, reconstruction of instance configuration, and controlled resumption of GitHub integration.
- **Related:** [Hosting](0001-self-hosted-github-synchronization.md), [authorization](0012-mvp-authorization-policy.md), [protection onboarding](0016-integrated-protection-onboarding.md), [confirmed repair](0017-protection-change-detection-and-confirmed-repair.md), and [encrypted backups](0019-encrypted-backups-and-instance-recovery-key.md).

## Context

Project archives and the external instance recovery kit recover canonical contracts and their governance history. They intentionally exclude instance-wide operational credentials. After losing the host, the user also needs to establish a working instance and reconnect its GitHub integration.

The user accepted guided reconstruction and proposed starting the restore locally from a terminal in the repository directory. This should remain usable when the primary SuiteWard service is unavailable and should not require the user to assemble database and object-store recovery commands manually.

## Decision

Provide a locally installed CLI with this entry point:

```sh
suiteward restore
```

Running it inside a repository starts an interactive restore assistant using the directory as a discovery hint. The executable is installed for the user or host and can serve multiple repositories; it is not a dependency supplied by each candidate repository. The shell command has no leading slash. Existing slash commands such as `/suiteward approve P42-R3` remain PR-comment commands.

Choose guided reconstruction for the MVP: recover project data from ADR 0019 archives, guide the owner through required instance configuration and operational credentials, and reconnect and verify GitHub before resuming normal operation. A separate full-instance archive containing administrative credentials is a possible future extension, not required by this recovery flow.

Only `suiteward restore` is selected here. Additional CLI commands, flags, shell completion, machine-readable output, and noninteractive administration are not introduced by this decision.

## Guided flow

1. **Discover the project and recovery environment.** Suggest the repository from the current directory, locate a reachable instance or offer to prepare a fresh isolated recovery environment, and explain any missing supported runtime prerequisites. Do not require the damaged primary API or database to be running.
2. **Select a backup.** Offer available local copies, the dedicated Release assets selected in [ADR 0021](0021-release-backups-and-health-reconciliation.md), any identified Git fallback copy, and a manually selected archive path. Show the source and encrypted-byte SHA-256, and validate any trusted external receipt. Exact namespace and fallback-path details remain open. Fetching a private remote copy may require owner-authorized read access before the permanent GitHub App connection is restored.
3. **Load the recovery kit locally.** Request the key file through local selection or a protected input mechanism. Do not request the secret in chat, PR comments, MCP, ordinary command arguments, or logs. Normal operation continues to retain only the public encryption key.
4. **Validate and preview.** Decrypt and verify format, project identity, manifests, content objects, audit relationships, and compatibility. Show the verified project, canonical version, covered history, recovery point, destination, and any missing prerequisites. Displayed unverified metadata must not be presented as verified coverage.
5. **Resolve conflicts before committing the restore.** If the destination already contains that project or incompatible state, identify the conflict and offer a separate recovery target or cancellation. Never silently replace a functioning canonical contract or other projects. The MVP does not need an in-place destructive replacement mode.
6. **Restore into recovery mode.** After the user reviews the concrete recovery plan and selects the target, reconstruct metadata and content in isolated state. Suspend normal promotion, checks, comments, background publication, and replay of historical queued work for the recovering target. Persist progress so the same command can resume after an interruption.
7. **Reestablish ownership and integration.** Guide the responsible human through identity verification, required instance configuration, and reattachment to the existing GitHub App where possible. Operational credentials must be supplied or recreated through the supported owner flow; the recovery kit does not recreate them or grant approval authority.
8. **Reconcile and resume.** Check current GitHub state, pending proposals and revocations, App installation, and effective required-check protection. Explain unresolved differences. Enable normal operation only after the required checks and deliberate owner resumption; preserve a visible recovery state if any prerequisite is unresolved.

The wizard may pause after restoring local data when GitHub is unavailable. Local recovery progress and readiness to resume governance are distinct outcomes. It must not claim protection is active merely because decryption or data import succeeded.

## Project and instance scope

Inside a repository, project recovery is the default scope. The current directory helps discover the project but is not proof of identity or authority. When outside a repository, the assistant can ask for an archive and destination rather than requiring a checkout solely for decryption and import.

On a new host, the same assistant guides creation of an instance and import of the chosen project. Other projects are imported from their own archives using the same instance recovery kit. The kit alone does not contain those archives or guarantee discovery of every repository previously connected to a lost instance.

Recovering one project into an existing instance must not overwrite unrelated projects or their configuration. An existing healthy project cannot have its canonical pointer reset through this restore workflow as a substitute for ADR 0018's corrective-PR process. Detailed instance-wide versus per-project recovery isolation is an implementation choice that must preserve these boundaries.

## Authority and trust boundaries

- CLI access and possession of the recovery kit do not establish the human's current SuiteWard approval or administration authority. The implementation must verify the responsible owner before reactivation; loss of the owner identity itself still requires a separate recovery policy.
- Keep private recovery keys and owner credentials outside agent access. A human-facing local CLI does not change the accepted threat model or add administrative powers to agent MCP tools.
- A repository directory, `.suiteward.yml`, or downloaded archive is untrusted discovery input. Restore must not execute repository hooks, tests, candidate scripts, or commands embedded in an archive to establish trust. Archive extraction/import must be confined to the chosen recovery target.
- Preserve ADR 0019's trusted-receipt and provenance requirements. Successful public-key decryption alone does not prove who produced an archive, and an old snapshot does not prove an approval is still valid.
- Reconcile historical approval/revocation state and pending work before enabling external effects; uncertainty must not revive stale authority.
- Verify the expected App source of the required check. Recreating or changing a GitHub App can change that identity. Any required protection repair follows the specific owner-confirmed preview in ADR 0017; a generic restore confirmation does not authorize unrelated rule changes.
- Ensure that a replacement instance and the old instance cannot simultaneously publish or promote for the same project when resuming service. The exact exclusion mechanism remains open.

## Architecture and failure behavior

The CLI is an adapter around the same provider-agnostic recovery and domain services used by the instance. It must not create a second implementation of canonical authority rules or rely on a working primary service for the disaster path. Packaging may reuse the Go binary and supported container deployment; the installation and runtime orchestration mechanics remain open.

Archive validation occurs before activation. Failed or interrupted imports remain isolated and resumable or explicitly discardable, preserving the last completed backup and unrelated live data. Repeated invocation must not create duplicate projects or replay old publications without reconciliation. Record the selected source, recovery point, verified identity, and resumption outcome without logging secrets.

The final result reports what was restored, the recovery point, unresolved gaps, and whether normal operation has resumed. Credentials are reestablished through the guided setup rather than recovered from a project archive.

## Acceptance criteria for implementation

1. A user can start `suiteward restore` inside a repository and choose the corresponding archive and external kit without performing manual database operations.
2. Restore can start when the primary SuiteWard service is unavailable, with clear guidance for missing runtime prerequisites.
3. A local archive can be validated and imported without immediate GitHub connectivity; resumption remains pending until external checks succeed.
4. The assistant shows verified project identity, recovery point, canonical version, and destination before committing the import.
5. Existing project conflicts and unrelated projects are preserved; a repeated restore does not silently overwrite current authority.
6. Private keys and operational credentials do not appear in ordinary command arguments, logs, MCP responses, or PRs.
7. The recovered instance does not publish, promote, or replay historical jobs before reconciliation and authorized resumption.
8. Effective GitHub protection and the expected App identity are checked before reporting active protection; repairs retain ADR 0017's confirmation requirements.
9. The same command can continue interrupted recovery and reports data-restored and service-resumed outcomes distinctly.

## Remaining implementation details

- CLI distribution, supported platforms, runtime prerequisites, and creation or connection of the isolated recovery environment.
- Exact prompt text, local key-file handling, progress persistence, flags, and additional commands if justified.
- Backup Release/asset and fallback-path discovery under ADR 0021, trusted receipt retrieval, and authentication for private-repository downloads.
- Specific owner verification and recredentialing steps, including headless operation and the separate case of losing the owner identity.
- Migration/compatibility checks, failed-import cleanup, and prevention of concurrent publication by old and replacement instances.
- Limits and operational targets already open in ADR 0019.

Guided reconstruction through a local `suiteward restore` command is accepted. This documentation does not install or implement the CLI.
