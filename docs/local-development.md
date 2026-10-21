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

`setup` installs the pinned local tools and starts this checkout's PostgreSQL. `doctor` verifies tool versions, selected Go installation, an authenticated database transaction, and Windows/Linux access to the published TCP port. `check` runs offline infrastructure checks, documentation link validation, workflow validation, and portable Go verification, including `golangci-lint` 2.14.0 (the pinned version, installed from its checksum-verified archive if missing) against `.golangci.yml`; any finding fails `check`. Domain tests and `check` do not require Podman. `persistence` separately verifies the actual PostgreSQL adapter and generated queries using this checkout's database, with the race detector on (`go test -race`, as Linux CI does; it is slower than a plain run and needs a C compiler on Windows); missing or blank database identity fails rather than skipping adapter verification.

Existing host prerequisites are Git, PowerShell 7, and a running Podman engine/machine. The bootstrap does not install host components, edit shell profiles, change machine-wide environment variables, restart Podman, or change its default connection. No additional package manager or Compose provider is required.

## Pinned tools

| Tool | Version | Installation |
| --- | --- | --- |
| Go | 1.27.3 | Official archive, SHA-256 checked; must match `.go-version`. |
| sqlc | 1.31.1 | Official release archive, SHA-256 checked. |
| actionlint | 1.7.12 | Official release archive, SHA-256 checked. |
| golangci-lint | 2.14.0 | Official release archive, SHA-256 checked against the release's checksums file. |
| govulncheck | 1.8.0 | Exact Go module version, verified using the public Go checksum database. |
| gopls | 0.23.0 | Exact Go module version, installed like govulncheck. |
| PostgreSQL | 18.6, Debian trixie | Official image pinned to an immutable multiarchitecture digest. |

The declarations and original checksum sources live in `dev/tools.json`. Review version, checksum, CI, and compatibility changes together. No download uses a floating `latest` version. `gofmt`, `go vet`, and the standard test runner are included with Go. [ADR 0026](decisions/0026-versioned-postgresql-migrations.md) selects embedded Goose migrations with pgx; exact dependencies are versioned in `go.mod`/`go.sum`. `sqlc.yaml` consumes the real persistence SQL.

```powershell
./scripts/dev.ps1 tools             # Install tools without a database
./scripts/dev.ps1 go version
./scripts/dev.ps1 go test ./...     # Once real Go packages exist
./scripts/dev.ps1 sqlc version
./scripts/dev.ps1 govulncheck ./... # Once real Go packages exist
./scripts/dev.ps1 lint               # golangci-lint run ./... (arguments replace the default)
./scripts/dev.ps1 gopls version
```

The wrapper prioritizes the pinned SDK from the shared cache and scopes Go installation/cache paths to the process. `GOENV=off`, `GOTOOLCHAIN=local`, and `GOWORK=off` prevent a user's persisted Go settings, toolchain download, or ancestor workspace from silently changing the selected toolchain. During tool execution only, the OS configuration directory (`APPDATA` on Windows, `XDG_CONFIG_HOME` on Linux) points to `.cache/tool-config`, keeping Go telemetry/configuration local too. Podman uses its existing host configuration. The wrapper restores the caller's environment on completion or failure. Application dependencies belong in `go.mod` and `go.sum`.

## Local state and installation recovery

| Directory | Contents |
| --- | --- |
| Shared tool cache (see below) | Pinned executables, the Go SDK, `govulncheck`, `gopls`, and verified release archives, shared by every checkout of this user. |
| `.cache/go-*`, `.cache/gopath/` | Per-checkout build, module, temporary, and package caches. |
| `.local/dev/` | Checkout database metadata, random local credentials, selected Podman connection, operation locks. |
| `.memdb/`, `.memtrace/fts/` | Local Memtrace data/search cache. |

These paths are ignored by Git. Images and named volumes live in Podman's own storage. Database passwords are random per checkout and written only under `.local/dev/`; preserve this directory if preserving its database volume. These are disposable development credentials, never GitHub credentials or SuiteWard approval identities.

### Shared tool cache

Pinned archive tools (Go SDK, sqlc, actionlint, golangci-lint), their downloaded archives, and the Go-module tools `govulncheck` and `gopls` (each installed with the wrapper's `GOBIN` set to its own versioned directory) install once per user, so a new worktree does not download them again:

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

On Windows, setup selects the sole existing rootless Podman machine connection and records its name for this checkout. The current machine's rootful Podman 6.0.2 cannot forward its published database port to Windows localhost; the existing rootless connection works. This is consistent with the [upstream Podman Windows forwarding issue](https://github.com/podman-container-tools/podman/issues/29377). The workaround is local to SuiteWard and does not switch the host default or restart the shared VM. `deploy/local.ps1`, the co-located stack, follows the same rule and is stricter: it uses a rootless connection only and refuses a rootful one. It picks the machine's sole rootless connection, honours `$env:CONTAINER_CONNECTION` or `-PodmanConnection NAME` when you name one, and restores your own connection settings when it ends. See [Running the service](#running-the-service).

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

## Lint, code navigation, and hooks

`./scripts/dev.ps1 lint` runs the pinned golangci-lint from the repository root (`run ./...` unless arguments are given) and propagates its exit code. `.golangci.yml` configures it, and lint is part of `check` and of the CI Go job on Windows and Linux (the default mode of `scripts/check-go.ps1`, right after `go vet`); the `-Coverage` and `-Integration` modes do not repeat it. Run it alone with `./scripts/dev.ps1 lint`. Called from inside a PowerShell session, a bare `--flag` argument is split by PowerShell's own parameter binding (`./scripts/dev.ps1 lint --version` fails); use the subcommand form (`lint version`) or `pwsh -NoProfile -File ./scripts/dev.ps1 lint --version`.

`./scripts/gopls-mcp.ps1` starts the pinned `gopls mcp` for Claude Code's stdio MCP configuration. It installs nothing: if `go` or `gopls` is missing from the shared cache it writes one error line to stderr (run `./scripts/dev.ps1 tools`) and exits non-zero. Stdout carries only the server's protocol traffic, and the pinned Go and per-checkout caches are on its environment. `./scripts/dev.ps1 gopls ...` runs the same binary interactively.

Two scripts under `scripts/hooks/` are deterministic Claude Code hooks (no model reasoning). Both read the hook JSON from stdin, never fail the tool call, and write nothing but their documented output to stdout:

| Hook script | Event | Behavior |
| --- | --- | --- |
| `go-format.ps1` | `PostToolUse` for `Edit\|Write\|MultiEdit` | Runs the pinned `gofmt -w` on the edited `.go` file when it exists inside the repository; ignores everything else. It never installs tools; a missing `gofmt` or any internal problem is one line on stderr. |
| `repeat-failure.ps1` | `PostToolUseFailure` (and `PostToolUse` on `Bash` when the response reports a non-zero exit code) | Fingerprints a failure from the tool, the normalized command or file path, and the last three non-empty output lines with timestamps, durations, temp paths, and hex ids removed. Counts per `session_id` in `.local/hooks/failures.json` (reset if corrupt, capped at 20 sessions and 100 entries each). From the second identical failure it prints `additionalContext` telling Claude to stop retrying, find the root cause, and consult the advisor. `SUITEWARD_HOOK_STATE_DIR` overrides the state directory. |

Per the Claude Code hooks reference, a Bash command that exits non-zero fires `PostToolUseFailure` (its `error` starts with `Exit code N`); `PostToolUse` fires only for calls that succeeded, and its Bash `tool_response` has `stdout`, `stderr`, `interrupted`, and `isImage` but no documented exit code. The `PostToolUse` path therefore only counts a response that carries an explicit non-zero `exitCode`, and is a safety net.

Limits: `go-format.ps1` formats only files inside the checkout that launched the session and skips symbolic and hard links, so a subagent editing in another worktree relies on the gofmt step of `check`. `repeat-failure.ps1` normalizes only the last 10 non-empty lines (of at most 64 KB) of a failure, and its counts are best-effort: parallel tool calls can race on the state file and lose an increment. `scripts/test-hooks.ps1` runs both hooks against sample input in a temporary checkout copy and is part of `check`.

## Running the service

`suiteward serve` and `suiteward probe` are described in the [runtime contract](contracts/runtime.md). To run the real image on this PC, first select the Podman machine's rootless connection for the session (`$env:CONTAINER_CONNECTION = '<name>'`, from `podman system connection list`; the install guide's [step 5.2](install.md#5-install-co-located)), because an image lives in the store of the connection that built it and the stack runs rootless only. Build it with `podman build -f deploy/Containerfile -t localhost/suiteward:local .` (it reports version `dev`; pass `--build-arg VERSION=sha-<commit>` to stamp another), start the co-located stack with `./deploy/local.ps1 -Image localhost/suiteward:local` (one stack per machine) and run the smoke test inside the Podman machine, as its rootless user, with `Get-Content -Raw deploy/smoke.sh | podman machine ssh --username user "sh -s"` (to assert the reported version, use `podman machine ssh --username user "SMOKE_VERSION=<version> sh -s"`). The default ssh user is `root`, which is the rootful store and holds no stack. The script generates the database owner password and the application role's password into Podman secrets and prints neither. Every run starts the database, creates the role `suiteward_app` once, runs `suiteward migrate` as the owner (the one-shot step the quadlets call `suiteward-migrate.service`) and starts the service only when migrate exited 0, so an upgraded image migrates before it serves. A stack from v0.1.x, where `suiteward-database-url` held the owner URL, is converted in place: the script prints `Upgrading an existing stack ...`, creates `suiteward-database-owner-url` and the role with a new password, and replaces `suiteward-database-url` by the application URL; the data is untouched. The units and roles are described in the [runtime contract](contracts/runtime.md#commands) and the [install guide](install.md#5-install-co-located). The remote verification host follows [deploy/HOST.md](../deploy/HOST.md). The smoke script's failure-path self-test, `deploy/smoke_test.sh`, runs as part of `check`.

### Verifying M1.6a on the co-located stack

The verification sections below run in a PowerShell session with the rootless connection selected (see above), and every `podman machine ssh` takes `--username user`. They were first written for a stack on the default connection; the commands are the same, run rootless. Where an entry says to restart or update the image, `./deploy/local.ps1` does it: it migrates first, so a newer image never meets an old schema.

Only one target may watch `IgnisDevNE/SuiteWardQ` at a time (D-COMMANDS). The host is the default watcher, so the co-located stack runs without GitHub except during its own verification. `deploy/local.ps1` already supports both: GitHub is configured only when all three non-secret parameters are given, and a run without them replaces the container with one without GitHub and keeps the state. The scenario itself is in [HOST.md, section 6b](../deploy/HOST.md#6b-pr-commands-and-bootstrap-m16a); the steps here are the stack side. Run them from the checkout, in PowerShell 7.

1. **Stop the host's watching first** (HOST.md, 6b.1). Never run both with GitHub, not even briefly: two acknowledgments, and one instance rejects the code meant for the other.
2. **Start the stack with GitHub.** The stack was purged after M1.5, so its App key secret does not exist and the key file is needed once (it is copied into a Podman secret and not kept elsewhere):

   ```powershell
   podman pull ghcr.io/ignisdevne/suiteward:deploy
   ./deploy/local.ps1 -GitHubAppKeyFile <path to the PEM> -GitHubAppId <id> -GitHubInstallationId <id> -GitHubRepositories IgnisDevNE/SuiteWardQ
   Get-Content -Raw deploy/smoke.sh | podman machine ssh --username user "SMOKE_TIER=co-located SMOKE_VERSION=sha-<commit> sh -s"
   ```

   The pull avoids a stale cached `deploy` image. `<commit>` is the commit the `Deploy` run built.
3. **Enroll with a fresh authenticator entry.** Never reuse the host's secret:

   ```powershell
   podman exec -it suiteward /suiteward setup --owner <login> --test-check IgnisDevNE/SuiteWardQ=<check>
   ```

   The warnings of HOST.md section 6a apply (the URI and the base32 secret are printed once; share no setup output).
4. **Run the scenario** (HOST.md 6b.4). Read `/status` through the machine: `podman machine ssh --username user "curl -s http://127.0.0.1:8081/status"`.
5. **Close the verification PR, then stop the GitHub watching:** `./deploy/local.ps1` with no GitHub parameters (smoke test with `SMOKE_GITHUB=0`), or `./deploy/local.ps1 -Down`. Only then restore the host. `./deploy/local.ps1 -Down -Purge` also removes the App key secret and the state.

### Verifying M1.6b on the co-located stack

The scenario is in [HOST.md, section 6c](../deploy/HOST.md#6c-the-suiteward--contract-check-m16b); the stack side is the one of M1.6a above, with these differences:
- the stack keeps its M1.6a enrollment, its authenticator entry and V1, so the setup step is not needed unless the stack was purged;
- pull the new `deploy` image and re-run `./deploy/local.ps1` with `-GitHubAppId`, `-GitHubInstallationId` and `-GitHubRepositories` only: the App key secret from M1.6a is reused, so the PEM is not needed again; run the smoke test with `SMOKE_TIER=co-located SMOKE_VERSION=sha-<commit>`;
- `/status` must show `schemaVersion` 14 and no `checks` or `bootstrap` error;
- stop the host's watching first and restore it only after the stack is back without GitHub (HOST.md 6b.1 and 6c.4).

### Verifying M1.7 on the co-located stack

The scenario is in [HOST.md, section 6d](../deploy/HOST.md#6d-promotion-on-merge-m17); the stack side is the one of M1.6a above, with these differences:
- **The stack is purged and set up again.** The host runs HOST.md 6d.3 first and its merges move SuiteWardQ `main` ahead of the stack's V1 (each target keeps its own canonical), so the old V1 would show every later PR as a protected change. Stop the host's watching first (HOST.md 6b.1), then run `./deploy/local.ps1 -Down -Purge` and start again as in steps 2 and 3 of M1.6a, with the PEM and a new authenticator entry: `podman pull ghcr.io/ignisdevne/suiteward:deploy`, `./deploy/local.ps1 -GitHubAppKeyFile <path to the PEM> -GitHubAppId <id> -GitHubInstallationId <id> -GitHubRepositories IgnisDevNE/SuiteWardQ`, then `podman exec -it suiteward /suiteward setup --owner <login> --test-check IgnisDevNE/SuiteWardQ=<check>`. The bootstrap then proposes V1 from the `main` the host left; approve it;
- run the smoke test with `SMOKE_TIER=co-located SMOKE_VERSION=sha-<commit>`;
- `/status` must show `schemaVersion` 15, `integrity` `passed` and no `checks`, `bootstrap` or `merges` error;
- run HOST.md 6d.3 steps 2 and 5, and the integrity tamper of 6d.4 step 5 only with the owner's go-ahead at the time;
- one target at a time: put the stack back without GitHub (`./deploy/local.ps1`) before restoring the host, and read `/status` through the machine (`podman machine ssh --username user "curl -s http://127.0.0.1:8081/status"`).

### Verifying M1.7b on the co-located stack

The scenario is in [HOST.md, section 6e](../deploy/HOST.md#6e-the-explicit-bootstrap-m17b); the stack side is the one of M1.6a above, with these differences:
- **The stack watches its own repository, `IgnisDevNE/SuiteWardQ3`, and the host does not.** The host watches `SuiteWardQ`, `SuiteWardQ-private` and `SuiteWardQ2`, so no host stop is needed and the two targets may run at the same time;
- **The stack is already set up for SuiteWardQ3,** and SuiteWardQ3 has had no PR since, so the explicit bootstrap needs an update, not a purge or a new `setup` (as in M1.6b): `podman pull ghcr.io/ignisdevne/suiteward:deploy`, then `./deploy/local.ps1 -GitHubAppId <id> -GitHubInstallationId <id> -GitHubRepositories IgnisDevNE/SuiteWardQ3`. The App key secret is reused, so the PEM is not needed. Only if the stack was purged since, start it again from nothing: `./deploy/local.ps1 -GitHubAppKeyFile <path to the PEM> -GitHubAppId <id> -GitHubInstallationId <id> -GitHubRepositories IgnisDevNE/SuiteWardQ3`, then `podman exec -it suiteward /suiteward setup --owner <login> --test-check IgnisDevNE/SuiteWardQ3=<check>` with a new authenticator entry;
- run the smoke test with the full 40-hex commit the `Deploy` run built: `Get-Content -Raw deploy/smoke.sh | podman machine ssh --username user "SMOKE_TIER=co-located SMOKE_VERSION=sha-<commit> sh -s"`;
- **there is no image rollback:** once migration `00017` is applied, an older image refuses to start (forward-only migrations). If the new image fails, do not start an older one; capture `podman logs --tail 50 suiteward` and report;
- `/status` must show `schemaVersion` 17, `SuiteWardQ3` with no `bootstrap` error, and no `checks`, `bootstrap` or `merges` error. `no-open-pull-request` and `awaiting-initial-contract` no longer exist;
- run HOST.md 6e.4 on SuiteWardQ3 with the co-located entry's codes: `skip` a PR that adds `.suiteward.yml`, the CI workflow and tests and merge it (no V1), then `propose` on another PR (the branch path) and approve it, so that V1 is promoted at approval. Read `/status` through the machine (`podman machine ssh --username user "curl -s http://127.0.0.1:8081/status"`);
- then put the stack back without GitHub (`./deploy/local.ps1`).

### Verifying M1.7c on the co-located stack

The scenario is in [HOST.md, section 6f](../deploy/HOST.md#6f-lower-latency-m17c); the stack side is the one of M1.7b above, with these differences:
- **The update has no purge and no new `setup`.** The stack keeps its enrollment, its authenticator entry and its canonical versions. Pull the image and re-run `./deploy/local.ps1` with the GitHub id parameters only, as in M1.7b: `podman pull ghcr.io/ignisdevne/suiteward:deploy`, then `./deploy/local.ps1 -GitHubAppId <id> -GitHubInstallationId <id> -GitHubRepositories IgnisDevNE/SuiteWardQ3`. The App key secret is reused, so the PEM is not needed;
- run the smoke test with the full 40-hex commit the `Deploy` run built: `Get-Content -Raw deploy/smoke.sh | podman machine ssh --username user "SMOKE_TIER=co-located SMOKE_VERSION=sha-<commit> sh -s"`;
- **there is no image rollback:** once migration `00018` is applied, an older image refuses to start (forward-only migrations). If the new image fails, do not start an older one; capture `podman logs --tail 50 suiteward` and report;
- `/status` must show `schemaVersion` 18, a `cycleStartedAt` in each `github` entry (it appears after the first cycle under the new image), no `lastError` in `github`, and `outbox.failed` 0;
- run the steps of HOST.md 6f.6 with the co-located entry's codes, the detector lines included. The stack watches only `IgnisDevNE/SuiteWardQ3`, so the host is not stopped;
- then put the stack back without GitHub (`./deploy/local.ps1`).

### M1.8 acceptance on the co-located stack

The scenario is in [HOST.md, section 6g](../deploy/HOST.md#6g-mvp-acceptance-m18); 6g.7 is the stack side, with the co-located differences already written there (a purge of the old co-located state, a fresh install from the install guide on a new test repository, the PR-head bootstrap (a `pull` baseline) through the `/` branch editor link, and the backup rehearsal). Run it from the checkout, in PowerShell 7, as the stack sides above do. The steps, the expected output and the paste-backs stay in HOST.md and the install and backup guides; this entry only points to them.

## Verification boundaries

The offline development safety tests are part of the Windows/Linux foundation CI job. They cover archive integrity, interrupted installation detection, exclusive setup, path/resource separation, ownership rejection, process environment isolation, the shared cache location and lock, and `db-reset` ownership checks. A real database smoke check runs locally through `doctor`/`db-test`.

Domain and application tests need no services, so `./scripts/dev.ps1 tools` and `./scripts/dev.ps1 check` support service-free development. The PostgreSQL and filesystem adapters need a database; run `./scripts/dev.ps1 persistence` before claiming their integration succeeds. The wrapper loads the ignored `database-url.txt` only into the verification process and restores any caller value even after failure. Each database fixture owns a unique schema and cleans only that schema, preserving other tests/checkouts.

`scripts/check-persistence.ps1 -Mode Generated` installs/checks the pinned local sqlc executable, regenerates into a fresh owned staging directory and compares complete, case-sensitive filenames and SHA-256 contents with the versioned output. It leaves tracked files untouched and rejects stale, missing, added or orphan output and generator failures. After intentional SQL changes, regenerate with `./scripts/dev.ps1 sqlc generate`, review and commit the output, then verify freshness. Fresh staging is necessary because in-place regeneration could leave obsolete files undetected.

Integration tests use the `integration` build tag. `scripts/check-go.ps1 -Integration` requires an explicitly supplied `SUITEWARD_TEST_DATABASE_URL`, and `-Race` adds the race detector (`dev.ps1 persistence` passes it); `-Coverage -Integration` retains race detection and produces the real combined coverage report. Linux CI provides an isolated PostgreSQL service. Native Windows integration uses each worktree's Podman database. There is still no GitHub installation flow, execution backend or restore command.

The live GitHub test (`internal/adapters/github/live_test.go`, build tag `live`) runs the production adapter against the real App on a private repository the installation covers; `check` and `persistence` never compile it. It needs `SUITEWARD_TEST_GITHUB_APP_ID`, `SUITEWARD_TEST_GITHUB_INSTALLATION_ID`, `SUITEWARD_TEST_GITHUB_APP_KEY_FILE` (path to the PEM) and `SUITEWARD_TEST_GITHUB_REPO` (`owner/name`, with at least one pull request), plus optionally `SUITEWARD_TEST_GITHUB_API_URL`. Set them in the process environment only, then run `./scripts/dev.ps1 go test -tags=live -count=1 -run '^TestLive$' ./internal/adapters/github/`. It logs statuses, counts and the token identity, never a token, key or response body. `TestLiveProtectionReads` in the same file measures which branch-protection and ruleset reads the read permissions allow: it needs the first three variables plus `SUITEWARD_TEST_GITHUB_REPOS` (comma-separated `owner/name`) and optionally `SUITEWARD_TEST_GITHUB_OWNER_LOGIN`, and logs statuses and shapes only; run it with `-run '^TestLiveProtectionReads$'`. `TestLiveProtectionResults` runs the production protection path (protection token, repository and protection reads, `protection.Evaluate`) and asserts the results listed in `SUITEWARD_TEST_GITHUB_EXPECT` (semicolon-separated `owner/name=<test check>=<kind>[:<finding>,<finding>]`, for example `IgnisDevNE/SuiteWardQ=CI=findings:missing-contract-check,missing-test-check`) with the first three variables; run it with `-run '^TestLiveProtectionResults$'`. `TestLiveWrites` measures the M1.6a comment and check-run writes: it needs the first three variables plus `SUITEWARD_TEST_GITHUB_WRITE_REPO` (`owner/name`), `SUITEWARD_TEST_GITHUB_WRITE_PULL` (the number of a throwaway test pull request) and `SUITEWARD_TEST_GITHUB_WRITE_MARKER` (a random token per run), and it writes one comment and one check run on that pull request, which it never deletes; run it with `-run '^TestLiveWrites$'`. `TestLiveChecks` measures M1.6b check-run behavior (create, `filter=latest` readback, an `in_progress` run after a `success`, the combined status and check-suite views, an unknown head sha) with the same variables as `TestLiveWrites`; it writes several check runs named `SuiteWard / P0 checks` on the head of the test pull request, ending on a completed success run, never deletes anything, and runs with `-run '^TestLiveChecks$'`.

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
