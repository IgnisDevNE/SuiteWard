# Local development

SuiteWard develops on Windows and verifies portable behavior on Windows and Linux. This bootstrap supports x64 Windows/Linux with PowerShell 7.2 or newer. It prepares development infrastructure; application implementation starts separately with M0.

## Start here

From the checkout, using PowerShell 7:

```powershell
./scripts/dev.ps1 setup
./scripts/dev.ps1 doctor
./scripts/dev.ps1 check
```

From another shell, prefix each command with `pwsh -NoProfile -File`. Scripts locate the checkout from their own path, so they also work when invoked from another directory.

`setup` installs the pinned local tools and starts this checkout's PostgreSQL. `doctor` verifies tool versions, selected Go installation, an authenticated database transaction, and Windows/Linux access to the published TCP port. `check` runs offline infrastructure checks, documentation link validation, workflow validation, and the same portable Go verification as CI once application code exists. Domain tests and `check` do not require Podman.

Existing host prerequisites are Git, PowerShell 7, and a running Podman engine/machine. The bootstrap does not install host components, edit shell profiles, change machine-wide environment variables, restart Podman, or change its default connection. No additional package manager or Compose provider is required.

## Pinned tools

| Tool | Version | Installation |
| --- | --- | --- |
| Go | 1.27.1 | Official archive, SHA-256 checked; must match `.go-version`. |
| sqlc | 1.31.1 | Official release archive, SHA-256 checked. |
| actionlint | 1.7.12 | Official release archive, SHA-256 checked. |
| govulncheck | 1.8.0 | Exact Go module version, verified using the public Go checksum database. |
| PostgreSQL | 18.6, Debian trixie | Official image pinned to an immutable multiarchitecture digest. |

The declarations and original checksum sources live in `dev/tools.json`. Review version, checksum, CI, and compatibility changes together. No download uses a floating `latest` version. `gofmt`, `go vet`, and the standard test runner are included with Go. Migration tooling remains a separate decision; sqlc configuration is introduced with actual persistence SQL.

```powershell
./scripts/dev.ps1 tools             # Install tools without a database
./scripts/dev.ps1 go version
./scripts/dev.ps1 go test ./...     # Once real Go packages exist
./scripts/dev.ps1 sqlc version
./scripts/dev.ps1 govulncheck ./... # Once real Go packages exist
```

The wrapper prioritizes the checkout SDK and scopes Go installation/cache paths to the process. `GOENV=off`, `GOTOOLCHAIN=local`, and `GOWORK=off` prevent a user's persisted Go settings, toolchain download, or ancestor workspace from silently changing the selected toolchain. During tool execution only, the OS configuration directory (`APPDATA` on Windows, `XDG_CONFIG_HOME` on Linux) points to `.cache/tool-config`, keeping Go telemetry/configuration local too. Podman uses its existing host configuration. The wrapper restores the caller's environment on completion or failure. Dependencies for the application still belong in its eventual `go.mod` and `go.sum`.

## Local state and installation recovery

| Directory | Contents |
| --- | --- |
| `.tools/<tool>/<version>/<platform>/` | Per-checkout executables and Go SDK. |
| `.cache/downloads/` | Verified release archives. |
| `.cache/go-*`, `.cache/gopath/` | Build, module, temporary, and package caches. |
| `.local/dev/` | Checkout database metadata, random local credentials, selected Podman connection, operation locks. |
| `.memdb/`, `.memtrace/fts/` | Local Memtrace data/search cache. |

These paths are ignored by Git. Images and named volumes live in Podman's own storage. Database passwords are random per checkout and written only under `.local/dev/`; preserve this directory if preserving its database volume. These are disposable development credentials, never GitHub credentials or SuiteWard approval identities.

Setup uses an exclusive per-checkout lock. Archives download to unique partial files; their SHA-256 is checked before extraction. Archive tools extract into a fresh staging directory and become usable only after a ready receipt and a successful version check. Repeated setup reuses valid installations and the existing database.

A mismatched checksum fails closed. Remove only the named corrupt archive, then retry. An incomplete final installation is reported explicitly: move that exact tool/version/platform directory aside before retrying. Abandoned `.install-*` directories can be inspected and removed when no setup owns the checkout lock; they never count as installed tools. Setup does not automatically erase or replace a database after a version/ownership mismatch.

## PostgreSQL and parallel work

```powershell
./scripts/dev.ps1 db-start
./scripts/dev.ps1 db-status
./scripts/dev.ps1 db-test
./scripts/dev.ps1 db-stop
```

Each canonical checkout path determines a stable resource ID. Windows path casing and trailing separators do not change it. Every worktree gets its own container, volume, credential, caches, and loopback port. Let Podman allocate the port; discover it with `db-status` rather than assuming 5432. Container ownership labels, image digest, and mounted volume are checked before use or stop. `db-stop` preserves data. There is no automatic reset or volume deletion command.

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

## Verification boundaries

The offline development safety tests are part of the Windows/Linux foundation CI job. They cover archive integrity, interrupted installation detection, exclusive setup, path/resource separation, ownership rejection, and process environment isolation. A real database smoke check runs locally through `doctor`/`db-test`.

M0.01 introduces real Go domain packages and tests for artifact identity, authority, and immutable canonical snapshots. For this domain-only work, `./scripts/dev.ps1 tools` prepares the local tools and `./scripts/dev.ps1 check` runs the checks without starting a database or requiring containers. SQL migrations, pgx integration tests, sqlc freshness checks, and production platform support remain separate implementation work.

This preparation was verified on the Windows host and in an isolated Ubuntu 24.04 container: fresh pinned-tool installation, repeat installation, development checks, 15 CI checks, documentation links, and actionlint passed. Two Windows checkouts ran independent PostgreSQL clusters on different ports; stopping/restarting one preserved the other's state. Wrong database credentials were rejected, a failed tool command restored the caller's environment, and conflicting ambient Go settings did not select a global SDK. These local runs do not substitute for the next PR's hosted CI run.

## References

- [ADR 0023: development environment](decisions/0023-development-environment-and-project-local-tooling.md)
- [Development conventions](development-guide.md)
- [CI and coverage](ci-and-coverage.md)
- [Go releases and checksums](https://go.dev/dl/)
- [Go command environment](https://pkg.go.dev/cmd/go#hdr-Environment_variables)
- [sqlc release and asset digests](https://github.com/sqlc-dev/sqlc/releases/tag/v1.31.1)
- [Official PostgreSQL image](https://hub.docker.com/_/postgres)
- [Podman port publishing](https://docs.podman.io/en/latest/markdown/podman-run.1.html)
