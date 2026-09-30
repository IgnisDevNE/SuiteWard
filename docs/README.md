# SuiteWard planning documentation

Start with the [project baseline](project-baseline.md) for the product identity and accepted direction.

The [architecture decision index](decisions/README.md) lists the twenty-four ADRs, their scope, and which questions remain open.

The [engineering preparation plan](engineering-plan.md) tracks development practices, infrastructure, project structure, and task design for parallel agent work.

The [delivery plan](plan/README.md) defines one PR per phase, parallel tasks, ownership, dependencies, acceptance, and verification. Read [parallel delivery](plan/parallel-delivery.md) before dispatching work and [foundation readiness](plan/readiness.md) for what is local, hosted, or still pending.

The [development guide](development-guide.md) records accepted conventions for tests, structured logs, errors, and explicit dependency composition.

The [local development guide](local-development.md) explains the project-local tool bootstrap, Podman database, verification commands, and isolated resources for parallel worktrees.

The [CI and coverage guide](ci-and-coverage.md) records the accepted quality gates, prepared Codecov/GitHub Actions configuration, and staged activation requirements.

These files record planning decisions and engineering preparation. Initial CI configuration is active; [PR #3](https://github.com/IgnisDevNE/SuiteWard/pull/3), authored by `ignisdevne[bot]`, delivers the local environment and phase plan. Its current checks and authorized merge determine F0 completion. Application code, deployment configuration, and release licensing notices have not yet been implemented.
