# Local development

SuiteWard develops on Windows and verifies portable behavior on Windows and Linux. This bootstrap supports x64 Windows/Linux with PowerShell 7.2 or newer. Implementation is underway: M0, M1.01 and R1 delivered the domain and application packages and the PostgreSQL and filesystem adapters. This document covers the development infrastructure they run on.

## Start here

From the checkout, using PowerShell 7:

```powershell
./scripts/dev.ps1 setup
./scripts/dev.ps1 doctor
./scripts/dev.ps1 check
./scripts/dev.ps1 persistence
```

From another shell, prefix each command with `pwsh -NoProfile -File`. Scripts locate the checkout from their own path, so they also work when invoked from another directory.

`setup` installs the pinned local tools and starts this checkout's PostgreSQL. `doctor` verifies tool versions, selected Go installation, an authenticated database transaction, and Windows/Linux access to the published TCP port. `check` runs offline infrastructure checks, documentation link validation, workflow validation, and portable Go verification. Domain tests and `check` do not require Podman. `persistence` separately verifies the actual PostgreSQL adapter and generated queries using this checkout's database; missing or blank database identity fails rather than skipping adapter verification.

Existing host prerequisites are Git, PowerShell 7, and a running Podman engine/machine. The bootstrap does not install host components, edit shell profiles, change machine-wide environment variables, restart Podman, or change its default connection. No additional package manager or Compose provider is required.

## Pinned tools

| Tool | Version | Installation |
| --- | --- | --- |
| Go | 1.27.1 | Official archive, SHA-256 checked; must match `.go-version`. |
| sqlc | 1.31.1 | Official release archive, SHA-256 checked. |
| actionlint | 1.7.12 | Official release archive, SHA-256 checked. |
| govulncheck | 1.8.0 | Exact Go module version, verified using the public Go checksum database. |
| PostgreSQL | 18.6, Debian trixie | Official image pinned to an immutable multiarchitecture digest. |

The declarations and original checksum sources live in `dev/tools.json`. Review version, checksum, CI, and compatibility changes together. No download uses a floating `latest` version. `gofmt`, `go vet`, and the standard test runner are included with Go. [ADR 0026](decisions/0026-versioned-postgresql-migrations.md) selects embedded Goose migrations with pgx; exact dependencies are versioned in `go.mod`/`go.sum`. `sqlc.yaml` consumes the real persistence SQL.

```powershell
./scripts/dev.ps1 tools             # Install tools without a database
./scripts/dev.ps1 go version
./scripts/dev.ps1 go test ./...     # Once real Go packages exist
./scripts/dev.ps1 sqlc version
./scripts/dev.ps1 govulncheck ./... # Once real Go packages exist
```

The wrapper prioritizes the pinned SDK from the shared cache and scopes Go installation/cache paths to the process. `GOENV=off`, `GOTOOLCHAIN=local`, and `GOWORK=off` prevent a user's persisted Go settings, toolchain download, or ancestor workspace from silently changing the selected toolchain. During tool execution only, the OS configuration directory (`APPDATA` on Windows, `XDG_CONFIG_HOME` on Linux) points to `.cache/tool-config`, keeping Go telemetry/configuration local too. Podman uses its existing host configuration. The wrapper restores the caller's environment on completion or failure. Application dependencies belong in `go.mod` and `go.sum`.

## Local state and installation recovery

| Directory | Contents |
| --- | --- |
| Shared tool cache (see below) | Pinned executables, the Go SDK, `govulncheck`, and verified release archives, shared by every checkout of this user. |
| `.cache/go-*`, `.cache/gopath/` | Per-checkout build, module, temporary, and package caches. |
| `.local/dev/` | Checkout database metadata, random local credentials, selected Podman connection, operation locks. |
| `.memdb/`, `.memtrace/fts/` | Local Memtrace data/search cache. |

These paths are ignored by Git. Images and named volumes live in Podman's own storage. Database passwords are random per checkout and written only under `.local/dev/`; preserve this directory if preserving its database volume. These are disposable development credentials, never GitHub credentials or SuiteWard approval identities.

### Shared tool cache

Pinned archive tools (Go SDK, sqlc, actionlint), their downloaded archives, and `govulncheck` (the wrapper's `GOBIN`) install once per user, so a new worktree does not download them again:

| Platform | Default location |
| --- | --- |
| Windows | `%LOCALAPPDATA%\SuiteWard\tools` |
| Linux | `${XDG_CACHE_HOME:-$HOME/.cache}/suiteward/tools` |

Set `SUITEWARD_TOOLS_DIR` to use another directory (the offline infrastructure tests do this with a scratch directory). Layout is `<tool>/<version>/<platform>/`; versions are pinned in `dev/tools.json`, so checkouts on different pins coexist. The checksum and ready-receipt verification is unchanged, and an install path that resolves outside the cache root is rejected.

Setups from different checkouts serialize on a lock file in the cache root: a second setup waits (up to 15 minutes) instead of failing, then re-checks whether the tool is already installed before downloading. Build and module caches, `.local/dev` state, and the database stay per checkout. An older checkout's `.tools/` directory is no longer read; run `./scripts/dev.ps1 setup` (or `tools`) once to populate the shared cache, then delete `.tools/`.

Setup uses an exclusive per-checkout lock. Archives download to unique partial files; their SHA-256 is checked before extraction. Archive tools extract into a fresh staging directory and become usable only after a ready receipt and a successful version check. Repeated setup reuses valid installations and the existing database.

A mismatched checksum fails closed. Remove only the named corrupt archive, then retry. An incomplete final installation is reported explicitly: move that exact tool/version/platform directory aside before retrying. Abandoned `.install-*` directories can be inspected and removed when no setup holds the cache lock; they never count as installed tools. Setup does not automatically erase or replace a database after a version/ownership mismatch.

## PostgreSQL and parallel work

```powershell
./scripts/dev.ps1 db-start
./scripts/dev.ps1 db-status
./scripts/dev.ps1 db-test
./scripts/dev.ps1 db-stop
./scripts/dev.ps1 db-reset
```

Each canonical checkout path determines a stable resource ID. Windows path casing and trailing separators do not change it. Every worktree gets its own container, volume, credential, caches, and loopback port. Let Podman allocate the port; discover it with `db-status` rather than assuming 5432. Container ownership labels, image digest, and mounted volume are checked before use or stop. `db-stop` preserves data; no other command deletes data automatically.

`db-reset` is the one explicit, destructive command. It verifies the ownership labels (`io.suiteward.dev.owner` equal to this checkout's ID and `io.suiteward.dev.managed=true`) of both this checkout's container and volume, and refuses without removing anything if either belongs to another checkout. Otherwise it force-removes only that container and volume, deletes the local credential and connection files under `.local/dev/`, then creates and starts a fresh database with new random credentials. It prints no credential. Use it after a schema reset, such as the R1 migration reset that leaves older databases at an obsolete Goose version, then rerun `./scripts/dev.ps1 persistence`. All data in this checkout's database is lost.

Connection details are in `.local/dev/postgres.json`. The complete development URL is in the ignored `.local/dev/database-url.txt`; load it only into the process running a database client/test, and do not print or commit it. It includes `sslmode=disable` for the loopback-only development database. The authenticated smoke test uses the local credential over TCP inside the container and separately verifies the published host TCP port; it is not an application adapter integration test.

On Windows, setup selects the sole existing rootless Podman machine connection and records its name for this checkout. The current machine's rootful Podman 6.0.2 cannot forward its published database port to Windows localhost; the existing rootless connection works. This is consistent with the [upstream Podman Windows forwarding issue](https://github.com/podman-container-tools/podman/issues/29377). The workaround is local to SuiteWard and does not switch the host default or restart the shared VM.

If more than one rootless connection exists, choose explicitly on first setup:

```powershell
./scripts/dev.ps1 setup -PodmanConnection podman-machine-default
```

Subsequent commands reuse that recorded connection. They reject an implicit move to a different database engine/connection. Linux can use its local Podman engine, or an explicitly selected connection. Moving a checkout also changes its resource ID: stop the old checkout's database first and preserve its credentials/volume if its data matters.

## Agents, source of truth, and GitHub

Versioned ADRs and project documents are the canonical record. Memtrace is supplementary: its Cortex decision lookup was verified, while code search currently returns an inconsistent `repo_scope_required` response. Report that limitation and use targeted local reads; this does not block development or authorize changing Memtrace configuration in other repositories.

Use a separate working copy and branch for each implementation task. Run its own setup and checks. Share reviewed interfaces, not mutable database state or tool installation directories. Memtrace worktree/Fleet coordination needs separate validation before relying on it.

GitHub writes and PR publication use the authorized bot within its granted permissions. An access denial is not permission to switch to the user's personal credentials. The user continues to authorize each integration into `main`.

`scripts/bot-token.ps1` runs one command as the GitHub App bot, for example `./scripts/bot-token.ps1 -- gh pr create ...`. It reads the ignored `.local/github-app.json` (`app_id`, `installation_id`, `private_key_path`, `repository` as `owner/name`, `bot_login`), signs a short-lived RS256 App JWT with the local private key, confirms through `GET /app` that the App slug matches `bot_login` without `[bot]`, and requests an installation token restricted to the configured repository. It refuses a token that is not restricted to that single repository. The child command receives the token as `GH_TOKEN` and `GITHUB_TOKEN` in its own process environment only; the caller's environment is untouched and the child's exit code is returned.

Invoke it from a PowerShell session: `./scripts/bot-token.ps1 -- gh pr create ...`, or from another shell `pwsh -NoProfile -Command "& ./scripts/bot-token.ps1 -- gh pr create ..."`. Do not use `pwsh -File`; it mishandles the `--` separator. The command is validated first (a mistyped command requests no token) and must be a real executable, not a `.cmd` or `.bat` script. `app_id` and `installation_id` must be numeric.

Use it for `gh` and direct API calls only. `git push` ignores `GH_TOKEN` and `GITHUB_TOKEN` and could fall back to personal credentials, so do not use this script for it. Never run a child that prints its credential, such as `gh auth token`; the child receives the token by design and the script cannot stop it from displaying it.

The JWT and token are never written to stdout, stderr, files, or logs, and error messages from GitHub requests are replaced by a generic message with at most the HTTP status. The script never reads or changes global git or `gh` configuration. It refuses when the configuration, key, or command is missing or the slug does not match, and it never falls back to personal credentials: if it refuses or GitHub denies access, stop and report rather than retrying with a personal token or `gh auth` login. Keep the private key outside the repository or under `.local/`; never commit or print it. `./scripts/test-bot-token.ps1` verifies the JWT, refusals, and no-print behavior offline with an injected HTTP function and a generated key.

## Verification boundaries

The offline development safety tests are part of the Windows/Linux foundation CI job. They cover archive integrity, interrupted installation detection, exclusive setup, path/resource separation, ownership rejection, process environment isolation, the shared cache location and lock, and `db-reset` ownership checks. A real database smoke check runs locally through `doctor`/`db-test`.

Domain and application tests need no services, so `./scripts/dev.ps1 tools` and `./scripts/dev.ps1 check` support service-free development. The PostgreSQL and filesystem adapters need a database; run `./scripts/dev.ps1 persistence` before claiming their integration succeeds. The wrapper loads the ignored `database-url.txt` only into the verification process and restores any caller value even after failure. Each database fixture owns a unique schema and cleans only that schema, preserving other tests/checkouts.

`scripts/check-persistence.ps1 -Mode Generated` installs/checks the pinned local sqlc executable, regenerates into a fresh owned staging directory and compares complete, case-sensitive filenames and SHA-256 contents with the versioned output. It leaves tracked files untouched and rejects stale, missing, added or orphan output and generator failures. After intentional SQL changes, regenerate with `./scripts/dev.ps1 sqlc generate`, review and commit the output, then verify freshness. Fresh staging is necessary because in-place regeneration could leave obsolete files undetected.

Integration tests use the `integration` build tag. `scripts/check-go.ps1 -Integration` requires an explicitly supplied `SUITEWARD_TEST_DATABASE_URL`; `-Coverage -Integration` retains race detection and produces the real combined coverage report. Linux CI provides an isolated PostgreSQL service. Native Windows integration uses each worktree's Podman database. This phase does not introduce a production CLI, GitHub installation flow, execution backend or restore command.

The bootstrap was verified on the Windows host and in an isolated Ubuntu 24.04 container: fresh and repeated pinned-tool installation, development checks, documentation links, and actionlint. Two Windows checkouts ran independent PostgreSQL clusters on different ports, and stopping or restarting one preserved the other's state. These local runs do not substitute for hosted CI; the current checks are listed in [CI and coverage](ci-and-coverage.md).

## References

- [ADR 0023: development environment](decisions/0023-development-environment-and-project-local-tooling.md)
- [Development conventions](development-guide.md)
- [CI and coverage](ci-and-coverage.md)
- [Go releases and checksums](https://go.dev/dl/)
- [Go command environment](https://pkg.go.dev/cmd/go#hdr-Environment_variables)
- [sqlc release and asset digests](https://github.com/sqlc-dev/sqlc/releases/tag/v1.31.1)
- [Official PostgreSQL image](https://hub.docker.com/_/postgres)
- [Podman port publishing](https://docs.podman.io/en/latest/markdown/podman-run.1.html)
