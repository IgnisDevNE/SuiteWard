# CI and coverage

- **Updated:** 2026-09-26
- **Status:** Policy accepted; initial configuration is submitted in [PR #1](https://github.com/IgnisDevNE/SuiteWard/pull/1). Foundation CI has passed on GitHub-hosted Windows and Linux runners. Application coverage enforcement awaits real Go code and reports.

## Accepted quality policy

Build and tests must pass on Windows and Linux. Formatting (`gofmt`) and static analysis (`go vet`) are required. Linux CI also runs the race detector and `govulncheck`, with reachable known vulnerability findings treated as failures. A failed scanner is not a clean security result.

When persistence is implemented, its task must add real PostgreSQL integration checks and verify that sqlc-generated code matches the versioned SQL. Those checks are not currently implemented because neither persistence nor generated code exists.

Codecov must require 90% coverage of executable lines added or changed by a PR, with no percentage-point tolerance. Whole-project coverage is informational during M0; the project-wide non-regression policy will be selected at M0 completion. Exclude only the dedicated sqlc output directory, `internal/adapters/postgres/internal/dbgen/`, from the current coverage scope.

Critical governance scenarios remain required regardless of the coverage percentage. Follow the [development guide](development-guide.md) and the invariant/approval/promotion ADRs.

## Initial workflow

The workflow is `.github/workflows/ci.yml`. It runs on pull requests, pushes to `main`, and manual dispatch, without path filters that could suppress a required check.

| Job | Current behavior |
| --- | --- |
| Inspect repository | Compare tracked files with the base revision to select foundation or Go verification. Removing an existing Go module/source cannot silently disable the Go checks. |
| Foundation, Windows and Linux | Validate local documentation links and the CI classification/gate behavior. |
| Go, Windows and Linux | When real Go source exists: verify formatting, analyze, build, and test. A module without real packages fails. |
| Race, security, and coverage | When Go exists: run Linux race/coverage tests, scan reachable vulnerabilities, preserve the coverage artifact, and upload the actual report. |
| CI / Gate | Always evaluate all prerequisite results. Accept skipped Go jobs only when the successful classifier established foundation-only applicability. Failures, cancellations, missing classification, and unexpected skips fail the gate. |

There is no placeholder application package and no artificial coverage upload. The PowerShell tests verify CI infrastructure behavior and are not counted as application coverage. Once Go code exists, even a documentation-only PR runs the Go jobs so required coverage contexts remain available.

Go is pinned to 1.27.1 in `.go-version`. The workflow uses explicit Windows Server 2025 and Ubuntu 24.04 runner labels. Actions are pinned to commit SHAs; govulncheck is pinned to v1.8.0 and the Codecov CLI to v11.3.1. Tool downloads and caches use the job workspace where applicable; the compiler is provisioned in the ephemeral runner environment. Local developer Go bootstrap remains a separate preparation task under [ADR 0023](decisions/0023-development-environment-and-project-local-tooling.md).

## Codecov authentication and policy

The organization already had the Codecov GitHub App installed and tokenless public uploads enabled when this setup began. This work does not expand that installation, change organization-wide policy, or generate a persistent upload token.

The repository configuration is `codecov.yml`. Uploads from repository branches use GitHub OIDC with `id-token: write` limited to the coverage job. The pinned Codecov Action handles public fork PRs through its tokenless flow. Workflows do not use `pull_request_target` to run candidate code and do not retain checkout credentials.

The uploader searches only the explicitly named `coverage.out` file. Missing/empty reports and upload errors fail the job. Codecov's patch status is non-informational and configured to fail when its expected head report is absent. Whole-project coverage remains informational.

Codecov waits for CI to succeed before reporting its final status. `CI / Gate` must therefore check completion of the upload job, not wait for the asynchronously computed Codecov patch status. The patch status becomes a separate branch requirement once it has been observed and verified.

## Staged activation

1. Completed: the user authorized publication of documentation bootstrap `a4be88c` as the initial `main` history.
2. Completed: `infra/codecov-ci` is published as PR #1. Its Windows/Linux foundation jobs and `CI / Gate` succeeded in [the first hosted run](https://github.com/IgnisDevNE/SuiteWard/actions/runs/36287204180) for revision `0d4b4ae`.
3. Completed: `main` requires an up-to-date PR and `CI / Gate` from the observed GitHub Actions App (ID 15368), with administrator enforcement and force pushes/deletion disabled. The PR requirement has zero independent approving reviews, because a same-account PR author cannot provide that separate review. Merging PR #1 still requires the user's authorization.
4. The first real Go implementation PR must exercise the Go/coverage workflow, establish an actual report, and validate Codecov's status behavior. Confirm an under-covered change fails before enabling the observed patch check as a required branch context. No empty coverage baseline is substituted for this step.
5. Add and prove PostgreSQL integration and generated-code checks in the same delivery that introduces persistence. They must become applicable requirements before that capability is considered complete.

The user continues to authorize every integration into `main`. GitHub checks and PR requirements are active, but the provider cannot distinguish a human from an agent using the same GitHub identity. Human merge authorization remains an operational rule until a separate identity/credential design is established; a green CI run alone is not that authorization.

## Local verification performed during preparation

- Workflow analyzed with actionlint 1.7.12, installed under the project's ignored `.tools/` directory from a checksum-verified official release.
- PowerShell files parsed and CI behavior scenarios executed locally on Windows.
- Tracked documentation links checked and staged changes checked for whitespace errors.
- `codecov.yml` validated against Codecov's official validation endpoint.

The initial foundation checks were subsequently verified on GitHub-hosted Windows and Linux runners, as recorded above. Go build/tests, race detection, vulnerability scanning, and coverage upload remain intentionally inapplicable until application code exists. No application test result or successful coverage upload is claimed.

## References

- [Codecov status checks](https://docs.codecov.com/docs/commit-status)
- [Codecov YAML configuration and validation](https://docs.codecov.com/docs/codecov-yaml)
- [Pinned Codecov Action](https://github.com/codecov/codecov-action/tree/v7.1.1)
- [GitHub required-check behavior](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-required-status-checks)
- [Go vulnerability checking](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)
