# SuiteWard

SuiteWard protects an independently governed, canonical test contract. A repository may contain and propose tests, but it cannot redefine its own canonical contract.

**Status.** The M0 domain (artifact identities, immutable suite versions, governing-policy authorization, proposals, approval, and promotion rules) and the M1.01 PostgreSQL persistence adapter are merged. Phase R1, a simplification of the plan and the persistence contract, is in progress. The runtime service, GitHub integration, owner identity, and test execution are planned and not yet implemented.

M1 aims to deliver a tamper-evident, human-approved test contract. Approval requires a TOTP code from the verified owner so that, on an isolated installation, an AI agent cannot approve its own changes; co-located installations are labeled with reduced assurance ([ADR 0027](docs/decisions/0027-agent-human-trust-separation.md)). Canonical test execution arrives in M2.

Start with the [planning documentation](docs/README.md), [project baseline](docs/project-baseline.md), and [engineering plan](docs/engineering-plan.md). Open product gates are in the [decision register](docs/plan/decisions.md).

The accepted application stack is Go, Chi, PostgreSQL, pgx, sqlc, and River. Development targets Windows, with Windows and Linux CI and local PostgreSQL in Podman. See [CI and coverage](docs/ci-and-coverage.md) for checks and coverage policy.

Prepare a checkout with `./scripts/dev.ps1 setup`, verify it with `./scripts/dev.ps1 doctor`, and run checks with `./scripts/dev.ps1 check` in PowerShell 7. See [local development](docs/local-development.md) for pinned tools, isolated worktree databases, and commands. Domain-only work needs no database or containers.

Distribution is planned as AGPLv3. The final license grant and release notices remain an explicit pre-release decision in [ADR 0009](docs/decisions/0009-agpl-3.0-distribution.md).
