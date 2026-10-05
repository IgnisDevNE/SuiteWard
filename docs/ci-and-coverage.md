# CI and coverage

- **Updated:** 2026-10-05
- **Status:** Active. `main` requires an up-to-date PR with the contexts `CI / Gate` (GitHub Actions) and `codecov/patch` (Codecov).

## Quality policy

Build and tests must pass on Windows and Linux. Formatting (`gofmt`) and static analysis (`go vet`) are required. Linux CI also runs the race detector and `govulncheck`, with reachable known vulnerability findings treated as failures. A failed scanner is not a clean security result.

Linux race and coverage runs execute all tests with the `integration` tag against a healthy PostgreSQL 18.6 service pinned to the same immutable image as local development. The coverage step rejects an absent database URL. The Go runners and the coverage runner regenerate sqlc queries into private staging and require an exact match with the versioned output; they never mutate tracked generated files.

Critical governance scenarios are required regardless of any percentage. Follow the [development guide](development-guide.md), the [test-first rule](tdd.md), and the invariant, approval, and promotion ADRs. Coverage and green CI do not show that tests preceded the implementation or that they are meaningful; commit history and review do.

## Workflow

The workflow is `.github/workflows/ci.yml`. It runs on pull requests, pushes to `main`, and manual dispatch, without path filters that could suppress a required check.

| Job | Behavior |
| --- | --- |
| Inspect repository | Compare tracked files with the base revision to select foundation or Go verification. Removing an existing Go module or source cannot silently disable the Go checks. |
| Foundation, Windows and Linux | Parse all PowerShell scripts; validate the workflow with actionlint and documentation links; run the tests for CI classification and gate behavior, coverage argument handling, Go, persistence, and development tooling safety and isolation. |
| Go, Windows and Linux | Verify formatting, analyze, build and run portable tests; verify fresh sqlc output. A module without real packages fails. |
| Race, security, and coverage | Test the exact event head against an isolated PostgreSQL service with integration tests, verify fresh sqlc output, scan vulnerabilities, preserve the coverage report, and upload it to Codecov. |
| CI / Gate | Always evaluate all prerequisite results. Accept skipped Go jobs only when the successful classifier established foundation-only applicability. Failures, cancellations, missing classification, and unexpected skips fail the gate. |

There is no placeholder application package and no artificial coverage upload. Once Go code exists, even a documentation-only PR runs the Go jobs so the required coverage contexts remain available. The PowerShell tests verify CI infrastructure and are not counted as application coverage.

Go is pinned to 1.27.1 in `.go-version`. The workflow uses explicit Windows Server 2025 and Ubuntu 24.04 runner labels. Actions are pinned to commit SHAs; govulncheck is pinned to v1.8.0 and the Codecov CLI to v11.3.1. The [local bootstrap](local-development.md) installs the same Go and scanner versions inside each checkout and reuses `scripts/check-go.ps1`.

## Required contexts

| Context | Source | Meaning |
| --- | --- | --- |
| `CI / Gate` | GitHub Actions | Every applicable job above passed. |
| `codecov/patch` | Codecov | Coverage of the executable lines added or changed by the PR meets the patch target. |

`codecov/patch` fails when its report is absent; `CI / Gate` fails when any applicable job did not succeed. The PR requirement has zero independent approving reviews, because a same-account PR author cannot provide that review; human merge authorization is an operational rule, and a green CI run is not that authorization.

## Codecov policy

`codecov.yml` declares:

| Status | Rule |
| --- | --- |
| `patch/default` | Require 90% coverage of lines added or changed by the PR, zero tolerance, failing when the report is missing. Not informational. |
| `project/default` | Compare whole-project coverage with the PR base (`target: auto`) with a 1% threshold. Informational only: it reports a trend and gates nothing. |

The dedicated sqlc output directory, `internal/adapters/postgres/internal/dbgen/`, is excluded from the coverage scope. Go statement coverage and Codecov line coverage are different metrics.

Uploads from repository branches use GitHub OIDC, with `id-token: write` limited to the coverage job; the pinned Codecov Action handles public fork PRs tokenlessly. The uploader reads only the named `coverage.out`; a missing or empty report or an upload error fails the job. Workflows do not use `pull_request_target` to run candidate code and do not retain checkout credentials. Codecov waits for CI before sending final statuses.

## References

- [Codecov status checks](https://docs.codecov.com/docs/commit-status)
- [Codecov YAML configuration and validation](https://docs.codecov.com/docs/codecov-yaml)
- [Codecov line coverage and partial lines](https://docs.codecov.com/docs/about-code-coverage)
- [Pinned Codecov Action](https://github.com/codecov/codecov-action/tree/v7.1.1)
- [GitHub required-check behavior](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-required-status-checks)
- [Go vulnerability checking](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)

The earlier activation record for this setup (staged rollout, the custom project-policy gate, and its hosted observations) is in git history; see [delivery history](history.md).
