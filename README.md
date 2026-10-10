# SuiteWard

SuiteWard keeps the test contract of a software project under the control of a human. A repository may contain and propose tests, but it cannot redefine its own canonical test contract: that contract is versioned, immutable, and changed only by an approval from the project's verified owner.

## Why it exists

When AI agents write both the code and the tests, the tests that decide whether a change is acceptable can be edited by the same agent that wrote the change: a failing test is deleted, an assertion is weakened, or the CI workflow that runs the tests is changed. GitHub offers CODEOWNERS, branch protection, and required reviews, which answer "who must review a change to these files". SuiteWard answers a narrower question: is the test contract that gates the code still the contract a human approved?

## How it works

The target experience lives in GitHub pull requests, comments, and checks, with a small setup surface and no required dashboard.

- **A protected scope.** A `.suiteward.yml` declaration, reviewed in the pull request, lists the tests, test runner configuration, workflows, and scripts that form the contract. A candidate cannot shrink what is protected by editing its own declaration.
- **Proposals and exact approval.** A change to the protected scope becomes a proposal. The owner approves one exact revision with `/suiteward approve <ref> <TOTP code>`. The code comes from the verified owner, so on an isolated installation an agent that controls a GitHub account still cannot approve its own change.
- **A required check.** The `SuiteWard / Contract` check blocks the merge until an eligible approval and an integrity assessment exist. An approval survives pushes that change only implementation; those trigger a fresh assessment instead of dismissing it.
- **Promotion and history.** When the pull request is merged, SuiteWard validates the integrated contract and promotes it atomically as a new canonical version. Every version and its artifacts are kept and never rewritten; a correction goes through a corrective pull request.
- **Labeled assurance.** Isolated installations, where the service and its secrets are out of the agent's reach, give full assurance. Co-located installations are supported and reported with reduced assurance.

The claim of the first milestone (M1) is a tamper-evident, human-approved test contract, not proof that the tests were faithfully executed. Canonical test execution, in a controlled environment instead of candidate-controlled runners, is M2. See the [project baseline](docs/project-baseline.md) and [ADR 0027](docs/decisions/0027-agent-human-trust-separation.md).

## Status

Delivered:

- **Domain (M0).** Artifact identities, immutable suite versions, governing-policy authorization, proposals, consent, and guarded promotion rules.
- **Persistence (M1.01, R1).** PostgreSQL and filesystem adapters behind a normalized unit-of-work port, tested against a real database.
- **GitHub walking skeleton (S1).** An end-to-end spike on both verification targets; the findings are in [s1-github](docs/spikes/s1-github.md).
- **Runtime (M1.2).** `suiteward serve` and `suiteward probe`: configuration from the environment and secret files, migrations on start, River jobs enqueued in the same transaction as the governance write, a publication outbox with a relay, `/healthz`, `/readyz` and `/status`, graceful shutdown, and one container image built in CI, deployed and smoke-tested locally under Podman and on an isolated remote host.

Not implemented yet: the GitHub integration in the service, owner enrollment and TOTP, `.suiteward.yml` scope handling, the approval, check, and promotion flows on GitHub, and canonical test execution. Next are M1.3 (protected scope and baseline bootstrap) and M1.4 (GitHub App authentication and pull request discovery). The roadmap is in the [delivery plan](docs/plan/README.md) and the open product gates are in the [decision register](docs/plan/decisions.md).

## Architecture

One Go binary, `suiteward`, serves the API and runs the workers. The accepted stack is Go, Chi, PostgreSQL with pgx and sqlc, and River for jobs; canonical artifacts are content-addressed on a dedicated volume. The default GitHub integration is a GitHub App with outbound API access and no public webhooks. Five invariants hold throughout: canonical immutability, candidate non-authority, exact-revision approval, atomic promotion, and evidence kept distinct from authority.

The [ADR index](docs/decisions/README.md) maps the decisions; the [persistence](docs/contracts/persistence.md) and [runtime](docs/contracts/runtime.md) contracts fix the interfaces.

## Run and develop

Run the service with `suiteward serve`, which needs `SUITEWARD_DATABASE_URL` (PostgreSQL) and `SUITEWARD_ARTIFACT_DIR` (an absolute path); every setting, endpoint, and exit code is in the [runtime contract](docs/contracts/runtime.md). The image is `ghcr.io/ignisdevne/suiteward`, and [deploy/](deploy/) holds the local Podman stack, the quadlets, and the host runbook.

Development targets Windows, with Windows and Linux CI and local PostgreSQL in Podman. In PowerShell 7, prepare a checkout with `./scripts/dev.ps1 setup`, verify it with `./scripts/dev.ps1 doctor`, and run checks with `./scripts/dev.ps1 check`. Domain-only work needs no database or containers. See [local development](docs/local-development.md) for pinned tools, isolated worktree databases, and commands, and [CI and coverage](docs/ci-and-coverage.md) for the checks and the coverage policy.

## How it is built

Development is test-first for authority, consent, promotion, idempotency, concurrency, and adapter behavior. An AI orchestrator plans and verifies the work, subagents (and optionally the Orbit prompt executor) implement it in isolated worktrees, an independent reviewer checks each task, and only the owner authorizes a merge into `main`. The rules are in [AGENTS.md](AGENTS.md) and [orchestration](docs/plan/orchestration.md); start with the [planning documentation](docs/README.md) and the [engineering plan](docs/engineering-plan.md).

## License

Distribution is planned as AGPLv3. The final license grant and release notices remain an explicit pre-release decision in [ADR 0009](docs/decisions/0009-agpl-3.0-distribution.md).
