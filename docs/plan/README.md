# Delivery plan

- **Updated:** 2026-10-05 (phase R1)

Each phase produces one PR, integrated with a merge commit after the user's authorization. Work inside a phase is orchestrated as described in [orchestration](orchestration.md): the main session writes [briefs](task-brief.md), `sw-implementer` subagents implement them test-first in their own worktrees, and `sw-reviewer` subagents review them. Open and accepted product choices live in the [decision register](decisions.md).

Phase pages are written by hand and kept short. A task listed on a page becomes a brief when its phase starts; the orchestrator may split or merge tasks when the actual code shows a better boundary, and records the change on the page.

## Completed

| Phase | Outcome |
| --- | --- |
| F0, F0.01, F0.02 | Development environment, CI, initial plan, TDD rule. |
| M0.01–M0.04 | Domain values, proposals and consent, guarded promotion, application coordination. |
| M1.01 | First PostgreSQL and filesystem persistence (replaced in R1). |
| R1 | Simplification: normalized persistence and unit-of-work port, removal of speculative code and process machinery, trust and scope decisions, this roadmap. |

Evidence for the earlier phases lives in [history](../history.md).

## MVP roadmap

The MVP is a self-hosted, tamper-evident, human-approved test contract for one developer working with AI agents on GitHub.

| Phase | Outcome | Depends on |
| --- | --- | --- |
| [S1](phases/S1.md) | Throwaway walking skeleton against real GitHub: App polling one repository, a required check, an approval, a merge, a recorded promotion. Findings settle the GitHub decisions. | R1 |
| [M1.2](phases/M1.2.md) | `suiteward serve`: configuration, River jobs enqueued in the unit-of-work transaction, publication outbox, graceful shutdown. | R1 |
| [M1.3](phases/M1.3.md) | `.suiteward.yml` scope with protected defaults, protected inventory, existing-baseline bootstrap. | R1 |
| [M1.4](phases/M1.4.md) | GitHub App authentication, budgeted ETag polling, PR discovery, local MCP sync/status. | S1, M1.2 |
| [M1.5](phases/M1.5.md) | Owner setup, TOTP enrollment, installation tier, protection verification (including a required project test check). | S1, M1.4 |
| [M1.6](phases/M1.6.md) | `/suiteward approve` and `revoke` with TOTP, integrity assessment, `SuiteWard / Contract` check publication through the outbox. | M1.3, M1.4, M1.5 |
| [M1.7](phases/M1.7.md) | Merge detection and promotion for supported merge methods; explicit failure state for the others. | M1.6 |
| [M1.8](phases/M1.8.md) | MVP acceptance: install guide, backup and re-bootstrap runbook, end-to-end run, dogfooding on SuiteWard. | M1.7 |

M1.2 and M1.3 can run in parallel with S1. S1's findings may change the tasks of M1.4–M1.7; the orchestrator updates those pages before starting them.

## After the MVP

Deferred by the R1 amendments, in no fixed order until the MVP is in use:

- Contract-change priority queue and `/suiteward prioritize` (ADR 0022).
- First-test PR bootstrap path (ADR 0010).
- Automatic protection setup and confirmed repair (ADRs 0016, 0017).
- Encrypted backups, Release storage, contingency PRs and guided restore (ADRs 0019–0021).
- M2: canonical execution. Research on the first execution profile and isolation (D-EXECUTION) may start at any time.
- M3: hardening selected from observed threats.
