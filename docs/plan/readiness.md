# Engineering foundation readiness

Snapshot: 2026-09-30. This describes SuiteWard's own development infrastructure, not a deployed SuiteWard product.

| Area | Observed state | Remaining activation |
| --- | --- | --- |
| Local tooling | Pinned, checksum-verified Go 1.27.1, sqlc 1.31.1, actionlint 1.7.12, and govulncheck 1.8.0 installed inside the checkout. | New worktrees run the local setup command. |
| Local PostgreSQL | Digest-pinned PostgreSQL 18.6 in rootless Podman, authenticated TCP verified; separate checkouts have distinct resources. | Real pgx/sqlc integration arrives in M1.01. |
| Windows/Linux | Local infrastructure checks verified on Windows and Linux; F0 foundation jobs passed on both hosted OS runners for `8cb7682` in [run 36662944560](https://github.com/IgnisDevNE/SuiteWard/actions/runs/36662944560). | Every subsequent PR revision must pass its own applicable checks. |
| GitHub main | Public repository; main is protected. Latest observed main revision is `61b60f0ee3c7813b372842d6578e5675f61633b2`. | Preserve protection when publishing F0. |
| Existing CI | [Main run 36287533851](https://github.com/IgnisDevNE/SuiteWard/actions/runs/36287533851) succeeded. Required gate is `CI / Gate`; [PR #3](https://github.com/IgnisDevNE/SuiteWard/pull/3) adds hosted workflow linting, script parsing, and plan validation. | Activate the F0 workflow on main through the authorized PR merge. |
| Codecov | Organization App installed and public tokenless/OIDC upload configured. Patch target 90%; project total informational during M0. | First genuine report and negative patch-status proof in M0.01; no artificial baseline. |
| GitHub publication identity | `ignisdevne[bot]`, App 5028495, installation 163660443. Existing key authentication, effective Workflows write permission, and a short-lived token restricted to SuiteWard were verified. | Publication uses the bot. Main merge requires the user's authorization. |
| Memtrace | CLI 1.2.8; user initialized repository scope. Recent retrieval did not establish the new code/decision context reliably. | Supplementary only; versioned documents remain canonical. Fleet/worktree behavior is not yet a dependency. |
| Delivery plan | Versioned phase/task graph, contracts, decision gates, ownership, acceptance, and integration rules prepared. | Checkpoints record actual signatures and examples during each phase. |

## Local evidence and its limits

Local preparation verified fresh and repeat tool installation, archive checksums, environment restoration, checkout isolation, authenticated database access, and two independent development databases. Windows uses a checkout-specific rootless connection because the installed rootful Podman connection did not provide working localhost forwarding. The host default was preserved.

Foundation checks validate documentation links, PowerShell syntax, workflow syntax, CI gate behavior, tooling safety, and the plan graph/generated pages. They do not produce application coverage or prove a production deployment.

## GitHub configuration baseline

The prior setup recorded `CI / Gate` from GitHub Actions App ID 15368, strict up-to-date PRs, administrator enforcement, zero required independent approving reviews, and disabled force push/deletion on `main`. The current public API confirms protection and a successful main run; its exact administrative settings have not been reread through a bot in this run.

Before claiming F0 published, verify the bot's granted permissions, push the phase branch, open the phase PR, and observe its Windows/Linux CI. On 2026-09-30 the owner explicitly authorized Workflows write access for the shared SuiteWard/CircuitoNE App installation, and the permission was saved and accepted through the personal account. All other installation permissions and its selected repositories were preserved. Publication tokens are restricted to SuiteWard. This exception does not authorize personal-account publication, unrelated permission changes, or weaker protection. The user separately authorizes the merge.

The ignored checkout-local `.local/github-app.json` records the existing key path and expected App/installation identity. Private key material and installation tokens remain outside versioned files. Mint short-lived repository-scoped tokens, verify identity/scope before writes, disable personal credential fallback, and revoke temporary tokens after use. Do not change the user's global Git or CLI identity.

The project's `CI / Gate` is distinct from the future customer-facing `SuiteWard / Contract` check. Product App permissions, owner identity, protected-branch setup, and restoration are M1 work.

## Deliberately staged work

- M0 introduces useful Go code and real coverage. Its first implementation PR verifies and then activates the observed Codecov patch requirement.
- M1.01 introduces real PostgreSQL transactions, migrations, sqlc freshness, and their CI checks.
- M1.11 selects and proves the production packaging/support matrix and closes release licensing.
- M2 selects a customer test profile and execution isolation policy before implementing the Docker backend.
- Managed hosting, especially hosted execution versus customer infrastructure, remains an explicit later decision.

F0 is published in [PR #3](https://github.com/IgnisDevNE/SuiteWard/pull/3), authored by `ignisdevne[bot]`. Its initial foundation jobs passed on Windows and Linux. Consult the PR's current revision checks before integration; F0 is not integrated until the user authorizes and completes the main merge. Publication and earlier successful checks do not themselves grant that authorization.
