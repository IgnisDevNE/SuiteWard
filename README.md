# SuiteWard

SuiteWard protects an independently governed, canonical test contract. A repository may contain and propose tests, but it cannot redefine its own canonical contract.

M0 domain implementation is underway. The first phase provides deterministic artifact identities, immutable suite/version snapshots, and governing-policy authorization. Repository integration, persistence, promotion workflows, and test execution remain planned capabilities.

Start with the [planning documentation](docs/README.md), [project baseline](docs/project-baseline.md), and [engineering plan](docs/engineering-plan.md).

The [delivery backlog](docs/plan/README.md) breaks M0-M3 into phases with one PR each and tasks designed for parallel work. [Foundation readiness](docs/plan/readiness.md) records verified setup and remaining activation steps.

See [CI and coverage](docs/ci-and-coverage.md) for GitHub Actions checks, Codecov configuration, and staged activation.

The accepted application stack is Go, Chi, PostgreSQL, pgx, sqlc, and River. Development targets Windows, with Windows and Linux CI and local PostgreSQL in Podman. Development tools should be installed within the project wherever practical.

Prepare a checkout with `./scripts/dev.ps1 setup`, verify it with `./scripts/dev.ps1 doctor`, and run checks with `./scripts/dev.ps1 check` in PowerShell 7. See [local development](docs/local-development.md) for pinned tools, isolated worktree databases, and commands.

For domain-only work, use `./scripts/dev.ps1 tools` followed by `./scripts/dev.ps1 check`; these tests need no database or containers.

Distribution is planned as AGPLv3. The final license grant and release notices remain an explicit pre-release decision in [ADR 0009](docs/decisions/0009-agpl-3.0-distribution.md).
