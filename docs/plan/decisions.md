# Decision register

Source: [backlog.json](backlog.json). These gates preserve explicitly deferred details; they do not reopen accepted ADRs or automatically require a new permission prompt. The integrator can resolve implementation choices within accepted policy and record evidence. Changes to product authority, scope, or release commitments require the project owner decision identified by the relevant task.

Discovery/checkpoint tasks may collect evidence before a gate is accepted. Affected implementation and phase completion remain blocked until its recorded criteria are met. When resolving a gate, update the JSON and regenerate phase pages; link the accepted design and review evidence in the execution record.

## D-BOT: Authorized GitHub publication identity

Status: accepted. Owner: project owner and integrator. Applies to phase completion: F0.

Identify and authenticate the authorized bot using existing approved credentials and permissions; never publish using the current personal account.

Resolved on 2026-09-30: `ignisdevne[bot]`, App 5028495, installation 163660443. The owner authorized Workflows write access on the shared SuiteWard/CircuitoNE installation, and its effective grant was verified. Tokens for this work are restricted to SuiteWard. See [foundation readiness](readiness.md).

- Identity is verifiably the designated bot.
- Required publication actions fit its granted repository permissions.
- No credentials appear in project artifacts or conversation.

## D-M0-COVERAGE: Coverage policy after the first real domain milestone

Status: open. Owner: project owner and integrator. Blocks phase completion: M0.04.

Select the post-M0 project-wide coverage non-regression policy using genuine reports; retain the accepted 90% patch target and critical scenario requirements.

- Use observed coverage behavior and report limits.
- Record the selected policy and required contexts without changing it solely to pass a failing change.

## D-APPROVAL-CONTEXT: Approval binding for implementation-only source changes

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.03, M1.06.

Define which source and execution/context inputs are covered by an approval, how implementation-only pushes affect proposal revision and consent, and when a previously approved revision becomes ineligible. Preserve exact-source evidence even if consent can remain eligible across an explicitly permitted source change.

- Examples specify no protected change, changed test content, changed scope/support files, changed governing policy, and changed implementation-only source.
- A later push is reassessed and cannot silently reuse evidence from the old source.
- A changed covered input creates a new proposal revision requiring fresh exact approval.
- The GitHub adapter and promotion coordinator apply the same recorded policy; the candidate cannot weaken it.
- M0 remains executable with explicit covered-input bindings and does not assert a selected production rule for this open policy.

## D-MIGRATIONS: Migration tooling and database change discipline

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.01.

Integrator selects and pins migration tooling within the accepted PostgreSQL/pgx/sqlc stack, migration ordering, upgrade checks and shared-file ownership; no renewed product approval is required for routine implementation choices.

- Fresh-install and supported upgrade scenarios are defined.
- Tool choice and migration ownership are versioned.

## D-RUNTIME: Runtime configuration and lifecycle

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.02.

Integrator defines trusted configuration/secret sources, process packaging, shutdown and bounded job retry/timeout defaults within the accepted architecture.

- Secret handling and required settings are explicit.
- Restart/shutdown and terminal failure semantics are testable.

## D-SYNC: Polling targets, budget and MCP boundary

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.02, M1.04.

Integrator measures feasible repository count/discovery delay and chooses budget, scheduling, coalescing and local MCP schema/auth defaults; escalate only material changes to user-visible guarantees or authority.

- All trigger paths obey one budget and authenticated GitHub limits.
- Discovery after an omitted MCP call, fairness and restart catch-up have measurable tests.
- MCP grants only scoped synchronization/status.

## D-SCOPE: Declaration and source-inventory semantics

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.03.

Freeze YAML/path/pattern/case/symlink/submodule and invalid-declaration behavior plus exact identity of approved declaration inputs. The owner decides any material user-facing scope limitation; integrator selects deterministic encoding and parsing.

- No candidate omission can remove existing protection.
- Complete inventory and meaningful configuration changes remain reviewable.
- Supported source cases and rejected cases have explicit tests.

## D-GITHUB-ACCESS: GitHub App permissions and supported access

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.04.

Map each operation to minimal GitHub App permissions, supported host and installation/token lifecycle. Preserve outbound-only operation and the already accepted administration capability for confirmed protection setup.

- Permission matrix covers reads, checks, comments, protection and eventual backup publication without unexplained privileges.
- Local/headless access flow works without public webhooks.
- Denied/revoked access has a visible scoped state.

## D-IDENTITY: Owner identity, policy serialization and credential separation

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.05.

Define initial human proof, local/headless setup sessions, trusted App installation association and explicit SuiteWard authorization; do not infer authority from repository administration. Owner accepts any new authority policy; integrator implements proof and policy encoding.

- Agent access cannot impersonate the approving owner.
- Installation URL parameters alone cannot establish authority.
- One eligible owner approval, including own-authored PRs, is preserved.

## D-PROTECTION: Effective protection, confirmation and notification lifecycle

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.05.

Integrator selects branch-protection/ruleset APIs, effective-rule comparison, freshness/readback handling and notification identity. Preserve explicit owner confirmation for setup and specific repairs.

- Preserve unrelated required checks/rules.
- Missing/insufficient protection is distinct from unavailable verification.
- Material preview changes require new confirmation; detection does not auto-repair.

## D-CHECKS: GitHub check identity and lifecycle

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.06.

Freeze source/context binding, external check-state mapping, stale publication invalidation and truthful notice lifecycle against supported GitHub behavior.

- Missing approval/incomplete/waiting is never represented by neutral/skipped readiness.
- Each source head and changed governing context is reassessed.
- Required App source and external presentation are unambiguous.

## D-QUEUE: Priority eligibility and merged-but-unpromotable recovery

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.07.

Integrator designs serialization/generation fencing and deterministic ordering. Owner settles unresolved admission/withdrawal, multiple-suite behavior and recovery when an active merged PR cannot promote; unsupported cases need explicit safe handling.

- Concurrent commands and delayed writes cannot resurrect obsolete eligibility.
- A merged active PR that cannot promote has a usable authorized recovery route.
- No automatic priority expiry/reassignment is invented.

## D-INTEGRATION: Supported GitHub merge contexts and exact integrated source

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.07.

Demonstrate supported merge/squash/rebase/fork/same-SHA/merge-group behavior and exact source resolution. Integrator may restrict technical support only through explicit documented safe behavior; material product limitations go to the owner.

- Every supported method has real GitHub adapter evidence.
- Same-SHA PRs cannot independently claim readiness without proven handling.
- A merge in flight during priority transfer is reconciled, not claimed atomically prevented.

## D-ARCHIVE: Backup format, cryptography, export consistency and capacity

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.08.

Integrator selects a versioned self-contained archive, maintained encryption/compression dependency, consistent export/import method, kit verification/rotation design, size budgets and checkpoint cadence. Preserve all accepted historical content/records and externally held private keys.

- One snapshot reconstructs every claimed historical version without old commits/archives.
- No hashes-only fallback, private recovery key or cross-project data in project archive.
- Actual coverage, exact encrypted-byte identity and incomplete state are distinguishable.

## D-BACKUP-REMOTE: Release layout, health clocks and fallback retention

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.09.

Integrator defines Release/tag/asset namespace, verification cadence/budget, capacity and immutable receipt locations. Owner settles escalation after loss of a formerly verified copy and any material retention policy; the 29-day first-coverage rule is already accepted.

- 29 days measures oldest never-covered recovery-relevant state and survives newer activity/restart.
- Fallback/cleanup are reviewable PRs without automatic merge or protection bypass.
- Historical referenced bytes remain retrievable and existing product/immutable-release settings are preserved.

## D-RESTORE: Restore compatibility, owner reconnection and instance exclusion

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.10.

Integrator specifies CLI distribution, isolated targets, extraction/format compatibility, progress and operational recredentialing. Owner accepts any new authority policy; loss of owner identity remains unsupported unless separately decided. Choose a verifiable old/new publisher exclusion mechanism. Define how trusted receipt/provenance is obtained and authenticated after loss of the original host, independently of the selected archive; do not select an M3 signing stack implicitly.

- Offline import never grants current ownership or enables external effects.
- Healthy/unrelated project authority cannot be overwritten.
- Owner, expected App source, protection and exclusive publisher are verified before resume.
- Forged-but-decryptable input is rejected; absent trustworthy provenance remains an explicit recovery condition rather than an authenticity success.

## D-RELEASE: Supported production matrix and license publication

Status: open. Owner: project owner and integrator. Blocks phase completion: M1.11.

Owner confirms supported production/CLI platforms and AGPL only-versus-or-later grant/notices. Integrator chooses packaging and upgrade mechanics within that matrix; native Windows development alone does not imply production Windows support.

- Clean supported-host install and independent restore drills are reproducible.
- Release notices use the accepted grant.
- M1 is described as integrity governance, with execution explicitly deferred.

## D-EXECUTION: First executable profile and execution trust model

Status: open. Owner: project owner and integrator. Blocks phase completion: M2.02, M2.03, M2.04.

M2.01 performs the decision work: choose first ecosystem/RunnerProfile and accepted Docker isolation, resources, network/private dependencies/secrets, trusted observation and supported host guarantees before implementing execution.

- Known failures/corrections and forged-result cases define acceptance.
- No candidate write access to canonical/control state or credentials.
- Execution-required assurance cannot silently downgrade when the backend is unavailable.

## D-MANAGED-EXECUTION: Future managed execution location and trust

Status: open. Owner: project owner and integrator. Blocks phase completion: None scheduled.

Future product decision: provider-hosted execution, customer infrastructure or both, including tenancy/isolation and credential/connectivity responsibilities. This does not block self-hosted M1 or the accepted local Docker backend and has no scheduled implementation task.

- A future managed proposal names the execution location and threat/operational ownership explicitly.
- No current task silently commits to a managed architecture.

## D-HARDENING-CONTROLS: Justified future hardening controls

Status: open. Owner: project owner and integrator. Blocks phase completion: None scheduled.

M3.01 evaluates concrete threats and proposes controls. The owner chooses justified signing/key-management/provenance/transparency increments; future executable phases are created only after those choices.

- Each selected control has a concrete threat, independent verification and operating/recovery cost.
- No speculative ready M3 implementation tasks or mandatory KMS/OIDC/transparency dependency.
