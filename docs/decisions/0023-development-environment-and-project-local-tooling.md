# ADR 0023: Windows development, cross-platform CI, and project-local tooling

- **Date:** 2026-09-26
- **Status:** Accepted; local tool and database bootstrap implemented. See [local development](../local-development.md) for commands and remaining verification boundaries.
- **Product:** SuiteWard
- **Scope:** SuiteWard's own development environment and tool installation policy.
- **Related:** [ADR 0003](0003-go-and-chi.md), [ADR 0004](0004-postgresql-pgx-sqlc.md), and the [engineering preparation plan](../engineering-plan.md).

## Context

The development machine runs Windows and already has Git, GitHub CLI, Memtrace, and a working Podman machine. The application has not been implemented, and Go was not found on the current Windows PATH.

The project will be developed by agents working in isolated branches and working copies. Setup should be reproducible without changing the environment of unrelated projects. The user requires tools to be installed inside the project whenever practical; global installation is reserved for genuine host-level needs.

## Decision

### Development and verification environments

- Develop SuiteWard natively on Windows.
- Build and run portable automated tests on both Windows and Linux in CI.
- Run the local PostgreSQL test database in the existing Podman environment. Windows processes connect to its published local port.
- Keep M0 domain tests independent of a database or container runtime. Introduce PostgreSQL integration tests when implementing the persistence adapter.
- Run the Go race detector as a required Linux CI verification once concurrent code and its tests exist. Windows race detection is optional unless its additional compiler prerequisites are deliberately provisioned.

For the initial CI layout, use a Linux service container for PostgreSQL integration tests. GitHub Actions service containers require a Linux runner; a Windows job must not assume the same service configuration works. Windows-to-PostgreSQL behavior can also be exercised locally against Podman. If Windows database integration coverage is later required in CI, provision an explicit supported database setup for that job rather than silently skipping it.

Sources: [GitHub PostgreSQL service containers](https://docs.github.com/en/actions/tutorials/use-containerized-services/create-postgresql-service-containers), [Go race detector requirements](https://go.dev/doc/articles/race_detector#Requirements).

This is a development and verification decision. It does not establish the production support matrix or change the M2 DockerExecutionBackend choice.

### Project-local tools

- Install new development tools within ignored project directories wherever a supported local installation is available. This includes Go itself, generators, linters, and migration tools selected later.
- Bootstrap Go from an official binary archive, with an explicit version and checksum verification. Windows ZIP distributions make a global MSI installation unnecessary for this workflow. [Official Go downloads](https://go.dev/dl/).
- Keep executable downloads, tool installations, build caches, module caches, and disposable development output out of version control. Commit version declarations, integrity information, setup logic, and configuration needed to reproduce them.
- Scope `PATH` additions and Go locations such as `GOBIN`, `GOPATH`, `GOMODCACHE`, and `GOCACHE` to the development process and its children. Do not persist SuiteWard settings with `go env -w`, user shell-profile edits, or system-wide environment changes.
- Use Go's module-managed tool support where appropriate, or explicitly pinned local binaries when a tool's supported distribution requires that approach. Do not use floating `latest` downloads in reproducible setup. [Go tool dependencies](https://go.dev/doc/modules/managing-dependencies#tool-dependencies), [Go environment variables](https://pkg.go.dev/cmd/go#hdr-Environment_variables).
- Control toolchain selection and automatic downloads so commands use the declared version and project-scoped storage. Merely declaring a minimum Go version is not an exact version pin.
- Reuse existing host infrastructure where appropriate. Install a new global component only when a supported project-local alternative cannot meet its actual requirements, and document that exception.

The Podman engine and its virtual machine are host infrastructure. Images and database volumes remain in Podman's managed storage; project-local tooling does not mean those resources physically live in the repository directory. Their configuration and test-resource ownership remain specific to SuiteWard.

Existing global tools are not automatically removed or replaced. In particular, the existing Memtrace binary does not authorize configuring other repositories or expanding SuiteWard's index scope.

### Multiple worktrees and CI

- Bootstrap must resolve explicit project/worktree paths and work from a fresh checkout. It must not depend on one agent's current directory or an untracked installation accidentally available in another checkout.
- Keep generated files, test results, and mutable test resources isolated per worktree or job. Shared tool installations, if introduced, must be versioned, protected from concurrent updates, and explicitly scoped to SuiteWard.
- Give concurrent database tests isolated databases or equivalent namespaces. Use separate ports where separate containers are needed; one agent must not reset another agent's database or stop its container.
- CI uses the same declared tool versions and verification behavior in its ephemeral runner environment. It does not inherit paths, credentials, or installations from the developer's computer.

## Consequences

- Contributors get a reproducible toolchain with limited changes to their host environment.
- Setup requires local launch/bootstrap commands and integrity checks; repository-local installation is not achieved solely by adding binaries to an ignored directory.
- Tool downloads and caches consume disk space. Cache sharing and cleanup must preserve active worktrees and keep Windows and Linux artifacts distinct.
- Cross-platform tests must cover relevant path, filesystem, and process behavior. A successful Windows test run alone does not prove Linux behavior, or vice versa.
- PostgreSQL transaction and concurrency guarantees require tests against PostgreSQL itself; in-memory substitutes only verify the domain's expected contract.

## Verification required during engineering setup

- A fresh checkout can prepare its pinned tools without permanent host environment changes.
- Development commands select the project toolchain even when a different global Go version is present.
- Bootstrap rejects an archive whose checksum does not match the declared value and handles interrupted downloads without treating partial tools as ready.
- Windows and Linux CI both build and run the portable test suite; Linux also performs the selected race and PostgreSQL integration checks when those capabilities exist.
- Two worktrees can run relevant tests without modifying each other's output or database state.

## Open implementation details

- Migration tooling; Go, PostgreSQL, sqlc, actionlint, and govulncheck are pinned in `dev/tools.json`.
- Shared tool caches, if later needed; current installations and mutable caches are isolated per checkout.
- PostgreSQL integration and sqlc freshness jobs when persistence exists; initial CI names, reporting, and activation are defined in the [CI guide](../ci-and-coverage.md).
- Supported production operating systems and any additional Windows database integration job.

These details do not reopen Windows development, Windows/Linux CI, local Podman PostgreSQL, or the preference for project-local installation.

## Amendment (2026-10-05, phase R1): shared tool cache

Pinned tools are installed once into a shared, checksum-verified per-user cache outside the checkout. Go caches, state, and the PostgreSQL database stay per checkout, so worktrees remain isolated without each downloading and verifying every tool.
