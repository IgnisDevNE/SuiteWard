# CI and coverage

- **Updated:** 2026-09-30
- **Status:** Active. M0.01 verified real Windows/Linux Go checks, Linux race/security/coverage, and Codecov success/failure at the 90% patch threshold. `main` requires both `CI / Gate` and `codecov/patch` from their expected Apps.

## Accepted quality policy

Build and tests must pass on Windows and Linux. Formatting (`gofmt`) and static analysis (`go vet`) are required. Linux CI also runs the race detector and `govulncheck`, with reachable known vulnerability findings treated as failures. A failed scanner is not a clean security result.

When persistence is implemented, its task must add real PostgreSQL integration checks and verify that sqlc-generated code matches the versioned SQL. Those checks are not currently implemented because neither persistence nor generated code exists.

Codecov must require 90% coverage of executable lines added or changed by a PR, with no percentage-point tolerance. Whole-project coverage is informational during M0; the project-wide non-regression policy will be selected at M0 completion. Exclude only the dedicated sqlc output directory, `internal/adapters/postgres/internal/dbgen/`, from the current coverage scope.

Critical governance scenarios remain required regardless of the coverage percentage. Follow the [development guide](development-guide.md) and the invariant/approval/promotion ADRs.

[ADR 0025](decisions/0025-test-driven-development.md) requires task-level TDD. The [TDD rule](tdd.md) separates machine-checked evidence consistency from independent review of observed behavior; coverage and green CI alone prove neither the RED-before-GREEN process nor test quality.

## Initial workflow

The workflow is `.github/workflows/ci.yml`. It runs on pull requests, pushes to `main`, and manual dispatch, without path filters that could suppress a required check.

| Job | Current behavior |
| --- | --- |
| Inspect repository | Compare tracked files with the base revision to select foundation or Go verification. Removing an existing Go module/source cannot silently disable the Go checks. |
| Foundation, Windows and Linux | Parse PowerShell scripts; validate the workflow with actionlint, documentation links, CI classification/gate behavior, coverage argument handling, development tooling safety/isolation, and the delivery graph/generated phase pages. |
| TDD evidence | F0.01 adds validation of task modes, execution-record structure, complete changed-file accounting, distinct author/reviewer declarations, RED/GREEN Git ancestry, and final task-file binding for PRs and `main` pushes. It is a required prerequisite of `CI / Gate`. |
| Go, Windows and Linux | When real Go source exists: verify formatting, analyze, build, and test. A module without real packages fails. |
| Race, security, and coverage | When Go exists: run Linux race/coverage tests, scan reachable vulnerabilities, preserve the coverage artifact, and upload the actual report. |
| CI / Gate | Always evaluate all prerequisite results. Accept skipped Go jobs only when the successful classifier established foundation-only applicability. Failures, cancellations, missing classification, and unexpected skips fail the gate. |

There is no placeholder application package and no artificial coverage upload. The PowerShell tests verify CI infrastructure behavior and are not counted as application coverage. Once Go code exists, even a documentation-only PR runs the Go jobs so required coverage contexts remain available.

The TDD validator reads committed evidence and Git history; it never executes commands supplied by that evidence. Full checkout history is required to check the recorded checkpoints. A well-formed JSON claim does not prove that a command ran, when it ran, that its diagnostic is authentic, or that its assertions are meaningful. Independent review checks those limits; normal CI executes the final revision's tests. F0.01 established hosted enforcement and F0.02 restored the checkpoint ancestry lost in its squash integration. The already completed F0 baseline has explicit historical status and no invented retrospective RED/GREEN evidence.

Go is pinned to 1.27.1 in `.go-version`. The workflow uses explicit Windows Server 2025 and Ubuntu 24.04 runner labels. Actions are pinned to commit SHAs; govulncheck is pinned to v1.8.0 and the Codecov CLI to v11.3.1. Tool downloads and caches use the job workspace where applicable; the compiler is provisioned in the ephemeral runner environment. The [local bootstrap](local-development.md) installs the same Go/scanner versions inside each checkout and reuses `scripts/check-go.ps1` for application verification.

## Codecov authentication and policy

The organization already had the Codecov GitHub App installed and tokenless public uploads enabled when this setup began. This work does not expand that installation, change organization-wide policy, or generate a persistent upload token.

The repository configuration is `codecov.yml`. Uploads from repository branches use GitHub OIDC with `id-token: write` limited to the coverage job. The pinned Codecov Action handles public fork PRs through its tokenless flow. Workflows do not use `pull_request_target` to run candidate code and do not retain checkout credentials.

The uploader searches only the explicitly named `coverage.out` file. Missing/empty reports and upload errors fail the job. Codecov's patch status is non-informational and configured to fail when its expected head report is absent. Whole-project coverage remains informational.

Codecov waits for CI to succeed before reporting its final status. `CI / Gate` must therefore check completion of the upload job, not wait for the asynchronously computed Codecov patch status. The patch status is a separate branch requirement, activated after it was observed and verified in M0.01.

## Staged activation

1. Completed: the user authorized publication of documentation bootstrap `a4be88c` as the initial `main` history.
2. Completed: `infra/codecov-ci` is published as PR #1. Its Windows/Linux foundation jobs and `CI / Gate` succeeded in [the first hosted run](https://github.com/IgnisDevNE/SuiteWard/actions/runs/36287204180) for revision `0d4b4ae`.
3. Completed: `main` requires an up-to-date PR and `CI / Gate` from the observed GitHub Actions App (ID 15368), with administrator enforcement and force pushes/deletion disabled. The PR requirement has zero independent approving reviews, because a same-account PR author cannot provide that separate review. The user merged PR #1; [the main-branch run](https://github.com/IgnisDevNE/SuiteWard/actions/runs/36287285580) passed for merge commit `650fb81`.
4. Verified in [PR #6](https://github.com/IgnisDevNE/SuiteWard/pull/6): the first real domain code activates Windows/Linux Go and Linux race/security/coverage. The actual Codecov report covers six domain files at 100%; a controlled real-test subset produces 6.34% and fails `codecov/patch` against 90%. The diagnostic configuration is restored exactly. Exact revisions, the coverage-argument regression repair, and both hosted observations are in the [execution record](plan/executions/M0.01.md). No empty baseline or synthetic application package was used.
5. Completed after explicit user authorization: `codecov/patch` from observed Codecov App 254 is required in addition to `CI / Gate` from App 15368. A one-time personal-account exception added that check because the bot lacks repository administration permission; all other protection fields were verified unchanged. The exception grants no standing personal fallback. The bot merged PR #6 with its checkpoint history intact; all eight jobs in [the main run](https://github.com/IgnisDevNE/SuiteWard/actions/runs/36670629908) passed at `808241c606daf08cf6f217fbc58e459d4f963cba`, and Codecov recorded 100% coverage. Every subsequent PR still needs checks for its final revision.
6. Add and prove PostgreSQL integration and generated-code checks in the same delivery that introduces persistence. They must become applicable requirements before that capability is considered complete.

The user continues to authorize every integration into `main`. GitHub checks and PR requirements are active, but the provider cannot distinguish a human from an agent using the same GitHub identity. Human merge authorization remains an operational rule until a separate identity/credential design is established; a green CI run alone is not that authorization.

## Local verification performed during preparation

- Workflow analyzed with actionlint 1.7.12, installed under the project's ignored `.tools/` directory from a checksum-verified official release.
- PowerShell files parsed and CI behavior scenarios executed locally on Windows.
- Tracked documentation links checked and staged changes checked for whitespace errors.
- `codecov.yml` validated against Codecov's official validation endpoint.

The initial foundation checks were subsequently verified on GitHub-hosted Windows and Linux runners, as recorded above. Application checks were inapplicable during that preparation. M0.01 supplies actual domain packages and the real hosted Go, race, vulnerability, and coverage results described in the staged activation record.

The F0 expansion adds actionlint, parsing of all PowerShell helpers, and delivery-plan validation through `scripts/check-foundation.ps1`. It is published in [bot PR #3](https://github.com/IgnisDevNE/SuiteWard/pull/3). Windows/Linux foundation jobs passed for `8cb7682` in [run 36662944560](https://github.com/IgnisDevNE/SuiteWard/actions/runs/36662944560); each later revision requires its own checks. See [foundation readiness](plan/readiness.md).

## References

- [Codecov status checks](https://docs.codecov.com/docs/commit-status)
- [Codecov YAML configuration and validation](https://docs.codecov.com/docs/codecov-yaml)
- [Pinned Codecov Action](https://github.com/codecov/codecov-action/tree/v7.1.1)
- [GitHub required-check behavior](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-required-status-checks)
- [Go vulnerability checking](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)
