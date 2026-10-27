# SuiteWard planning documentation

Start with the [project baseline](project-baseline.md) for the product identity and accepted direction.

The [architecture decision index](decisions/README.md) lists the ADRs, their scope, and which questions remain open.

The [engineering preparation plan](engineering-plan.md) tracks development practices, infrastructure, project structure, and task design for parallel agent work.

The [delivery plan](plan/README.md) is written; [orchestration](plan/orchestration.md) describes how work is delegated and integrated, and the [task brief template](plan/task-brief.md) defines a task. [Delivery history](history.md) says where earlier phase evidence lives.

The [development guide](development-guide.md) records accepted conventions for tests, structured logs, errors, and explicit dependency composition. [Test-first development](tdd.md) states when tests must precede implementation.

The [local development guide](local-development.md) explains the project-local tool bootstrap, Podman database, verification commands, and isolated resources for parallel worktrees.

The [CI and coverage guide](ci-and-coverage.md) records the quality gates, required checks, and Codecov policy.

The [install guide](install.md) installs SuiteWard and its executor, creates its GitHub App, runs the owner setup, and explains canonical execution, the receipt and their limits. The [backup runbook](backup.md) backs up, restores and re-bootstraps an instance. The [quickstart](quickstart.md) is the short co-located path, and [agent-install](agent-install.md) sets what a coding agent may and may not do during an installation.

The [threat model](security/threat-model.md) lists SuiteWard's assets, actors and trust boundaries, and the control and test behind each threat.

The [security policy](../SECURITY.md) says how to report a vulnerability and which versions get fixes.

The [decision register](plan/decisions.md) holds the accepted and open product decisions, and [Orbit](plan/orbit.md) describes the optional executor for queued tasks.

The contracts fix the interfaces: [runtime](contracts/runtime.md), [persistence](contracts/persistence.md) and [GitHub](contracts/github.md). The [S1 spike](spikes/s1-github.md) is the historical record of the GitHub walking skeleton. [deploy/HOST.md](../deploy/HOST.md) is the owner runbook of the isolated host.
