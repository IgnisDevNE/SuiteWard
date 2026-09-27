# SuiteWard

SuiteWard protects an independently governed, canonical test contract. A repository may contain and propose tests, but it cannot redefine its own canonical contract.

The project is in engineering preparation. Application implementation has not started.

Start with the [planning documentation](docs/README.md), [project baseline](docs/project-baseline.md), and [engineering plan](docs/engineering-plan.md).

Initial GitHub Actions and Codecov configuration is prepared. See [CI and coverage](docs/ci-and-coverage.md) for the current checks and activation steps.

The accepted application stack is Go, Chi, PostgreSQL, pgx, sqlc, and River. Development targets Windows, with Windows and Linux CI and local PostgreSQL in Podman. Development tools should be installed within the project wherever practical.

Distribution is planned as AGPLv3. The final license grant and release notices remain an explicit pre-release decision in [ADR 0009](docs/decisions/0009-agpl-3.0-distribution.md).
