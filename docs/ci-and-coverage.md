# CI and coverage

- **Updated:** 2026-10-23 (macOS tests, the five-target cross-build and the AlmaLinux quadlet job, phase M1.13)
- **Status:** Active. `main` requires an up-to-date PR with the contexts `CI / Gate` (GitHub Actions) and `codecov/patch` (Codecov).

## Quality policy

Build and tests must pass on Windows, Linux and macOS. Formatting (`gofmt`), static analysis (`go vet`), and `golangci-lint` against `.golangci.yml` are required; any lint finding fails the build. The linter is the version pinned in `dev/tools.json` (2.14.0), installed from its checksum-verified release archive, and runs once per Go runner on Windows and Linux in the default mode of `scripts/check-go.ps1`; the race and coverage runner does not repeat it. The macOS runner runs `scripts/check-go.ps1 -SkipLint`: gofmt, vet, build and the tests, but not the linter nor the sqlc check, because the pinned tools exist for Windows and Linux x64 only and the other two runners check the same sources. Run it locally with `./scripts/dev.ps1 lint`. Linux CI also runs the race detector and `govulncheck`, with reachable known vulnerability findings treated as failures. A failed scanner is not a clean security result.

Linux race and coverage runs execute all tests with the `integration` tag against a healthy PostgreSQL 18.6 service pinned to the same immutable image as local development. The coverage step rejects an absent database URL. The Go runners and the coverage runner regenerate sqlc queries into private staging and require an exact match with the versioned output; they never mutate tracked generated files.

Critical governance scenarios are required regardless of any percentage. Follow the [development guide](development-guide.md), the [test-first rule](tdd.md), and the invariant, approval, and promotion ADRs. Coverage and green CI do not show that tests preceded the implementation or that they are meaningful; commit history and review do.

## Workflow

The workflow is `.github/workflows/ci.yml`. It runs on pull requests, pushes to `main`, and manual dispatch, without path filters that could suppress a required check.

| Job | Behavior |
| --- | --- |
| Inspect repository | Compare tracked files with the base revision to select foundation or Go verification. Removing an existing Go module or source cannot silently disable the Go checks. |
| Foundation, Windows and Linux | Parse all PowerShell scripts; validate every workflow file with actionlint and the documentation links; audit the workflows offline with zizmor and scan the whole git history for secrets with gitleaks (redacted, never printed; the job fetches the full history; a scanner configuration or ignore file, or a `gitleaks:allow` comment, is not honored); check that every copy of a pinned image digest agrees and that the Containerfile pins its base images; run the tests for CI classification and gate behavior, coverage argument handling, Go, persistence, and development tooling safety and isolation, and the bot-token script; run the self-test of `scripts/install.sh`, the one-line install script, which uses no network (it is skipped, with a warning, on a Windows runner that has no `sh`). |
| Image build | Build `deploy/Containerfile` for `linux/amd64` and `linux/arm64` with buildx on the BuildKit image pinned in `release.yml`, with the Go version of `.go-version`. Nothing is pushed, signed or attested, and the job holds no credential and no `packages` permission: a broken Containerfile or a failing arm64 build shows on the pull request, not at the release tag. |
| Go, Windows, Linux and macOS | Verify formatting, analyze, lint, build and run portable tests; verify fresh sqlc output. macOS runs the formatting, analysis, build and tests only (no lint, no sqlc), on `macos-15`, which is Apple silicon: the installer's tests run there natively against a fake runtime, and nothing exercises a real Podman machine or Docker Desktop on a Mac. The Linux runner also cross-builds the installer for the five release targets (`linux/amd64`, `linux/arm64`, `windows/amd64`, `darwin/amd64` and `darwin/arm64`, the list of `release.yml`), so every target keeps compiling; `darwin/amd64` is built and never run. A module without real packages fails. |
| Quadlet units on AlmaLinux 9 | Render the units of both installer shapes with the installer's own render code (with GitHub on the default ports, and without GitHub on other ports with a job timeout), then run AlmaLinux 9's Podman quadlet generator over each shape in a container, with `--user --dryrun`. The job fails when the generator refuses a unit or leaves out one of the six services (`suiteward`, `suiteward-migrate`, `suiteward-db`, the network and the two volumes). It needs no systemd. The AlmaLinux image is pinned by digest, which pins the base image and not the package: `dnf install podman` takes the current el9 Podman, as the isolated host does, and the job prints its version. |
| Race, security, and coverage | Test the exact event head against an isolated PostgreSQL service with integration tests, verify fresh sqlc output, scan vulnerabilities, preserve the coverage report, and upload it to Codecov. |
| CI / Gate | Always evaluate all prerequisite results (the inspect, foundation and image jobs must succeed). Accept skipped Go, coverage and quadlet jobs only when the successful classifier established foundation-only applicability. Failures, cancellations, missing classification, and unexpected skips fail the gate. |

There is no placeholder application package and no artificial coverage upload. Once Go code exists, even a documentation-only PR runs the Go jobs so the required coverage contexts remain available. The PowerShell tests verify CI infrastructure and are not counted as application coverage.

Go is pinned to 1.27.3 in `.go-version`. The workflow uses explicit Windows Server 2025, Ubuntu 24.04 and macOS 15 runner labels. Actions are pinned to commit SHAs; govulncheck is pinned to v1.8.0 in `dev/tools.json` (CI reads the version there) and the Codecov CLI to v11.3.1. The base images in `deploy/Containerfile`, the PostgreSQL image and the BuildKit image are pinned by digest, and `scripts/check-pins.ps1` fails when copies of one image disagree. The release workflow builds the linux/amd64 image to an archive and scans it with trivy before anything is pushed, failing on a fixable HIGH or CRITICAL vulnerability (vulnerabilities only, not secrets or misconfiguration). The scan covers a linux/amd64 image built separately from the multi-architecture index that is pushed next, so the pushed amd64 layer is rebuilt from the same inputs and not the scanned file, and the arm64 image is not scanned. No other workflow publishes an image: the CI image job builds without pushing and does not scan. The scheduled `Fuzz` workflow (weekly and on demand, not a required check) runs every Fuzz target of the untrusted-input parsers for a few minutes and keeps a failing input as an artifact. The scheduled `Image scan` workflow (weekly and on demand, not a required check, not part of the gate) scans the pinned PostgreSQL image of `dev/tools.json` and the pinned Go image that the executor's sandboxes run from (`internal/executor/goprofile`, with copies in `deploy/Containerfile` and the workflow, which `scripts/check-pins.ps1` compares) with trivy on `linux/amd64` and `linux/arm64` for vulnerabilities only, and fails the run on a fixable HIGH or CRITICAL finding in the OS packages of either image (both are scanned before it fails); findings in language packages are only reported. The tests that run real containers (the `sandbox` build tag, [local development](local-development.md)) are run by hand: `check` and the CI gate do not run them, and `golangci-lint` and `go vet` of `check` do not compile them either. The [local bootstrap](local-development.md) installs the same Go, scanner, and linter versions in the shared per-user tool cache (not inside each checkout) and reuses `scripts/check-go.ps1`.

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

The dedicated sqlc output directory, `internal/adapters/postgres/internal/dbgen/`, is excluded from the coverage scope, as is `internal/application/governance/governancetest/**`, which is test support code. Go statement coverage and Codecov line coverage are different metrics.

Uploads from repository branches use GitHub OIDC, with `id-token: write` limited to the coverage job; the pinned Codecov Action handles public fork PRs tokenlessly. The uploader reads only the named `coverage.out`; a missing or empty report or an upload error fails the job. Workflows do not use `pull_request_target` to run candidate code and do not retain checkout credentials. Codecov waits for CI before sending final statuses.

## References

- [Codecov status checks](https://docs.codecov.com/docs/commit-status)
- [Codecov YAML configuration and validation](https://docs.codecov.com/docs/codecov-yaml)
- [Codecov line coverage and partial lines](https://docs.codecov.com/docs/about-code-coverage)
- [Pinned Codecov Action](https://github.com/codecov/codecov-action/tree/v7.1.1)
- [GitHub required-check behavior](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-required-status-checks)
- [Go vulnerability checking](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)

The earlier activation record for this setup (staged rollout, the custom project-policy gate, and its hosted observations) is in git history; see [delivery history](history.md).
