# Parallel delivery

Each phase in [the backlog](backlog.json) produces **one integration PR**. A milestone contains several phases. The backlog is canonical; GitHub Issues or Projects may mirror it later, but their creation is not required to start development.

## Branches and roles

Use `phase/<phase-id>` for an integration branch and `task/<task-id>` for each worker branch, with a separate worktree. F0 already uses `infra/local-dev`. A task contributes reviewed commits to its phase branch; it does not need a separate PR. Open the phase PR through the authorized bot, against `main`, once it is concrete and reviewable. Later phases may prepare draft branches before their predecessors merge; do not merge them out of dependency order.

The **integrator** owns the phase outcome, temporary shared-file assignments, conflict resolution, combined checks, and PR evidence. **Workers** implement bounded behavior and its tests. A **reviewer** checks the result independently of its author. Review and integration roles may rotate, but an author's self-check is not independent review.

With four agent slots, use up to three workers and one coordinator/reviewer when tasks permit. This is an execution capacity, not a reason to invent three packages or serialize all work behind the coordinator. When fewer tasks are ready, use spare capacity for review or the next independent contract checkpoint.

## Start dependencies and integration dependencies

`needs_to_start` identifies the actual prerequisites for beginning work. `needs_to_merge` identifies tasks whose real implementations/results must be present before that task can be integrated as complete. Every start prerequisite also remains a completion prerequisite. Phase `merge_after` orders phase PR integration into `main`.

A reviewed C0 contract checkpoint freezes only the next consumers' signatures, examples, errors, invariants, and file ownership. It is a deliverable, not automatic approval of every future product choice. It can precede implementation and need not wait for the preceding phase PR to merge. Workers can then implement separate behavior and tests against that agreement.

F0 is the initial foundation exception: complete its environment/GitHub integration before starting product implementation. Early M0 contract/document preparation can proceed, but product implementation tasks also require F0-I. This does not serialize later phase development behind every preceding PR.

Consumers may use small boundary fakes in tests and select prerequisite commits for compilation. They must not ship duplicate declarations, dummy production dependencies, or a framework of unused interfaces. Later-phase work reconciles changes before integration. Combined checks use the actual implementations, even if isolated task development used fakes.

Phase-level decision gates define what must be settled before the phase is complete. Task-level gates block only the affected implementation tasks. Discovery and contract tasks may gather the evidence needed to resolve a gate; an open gate is never permission to select the policy silently.

## Dispatch and ownership

Before dispatch, the integrator records the task ID, assignee, worktree/branch, reviewed contract revision, owned files, and evidence for completed start prerequisites. A task is ready when these prerequisites and its own decision gates are satisfied. A planned dependency is not satisfied merely because its task exists in the backlog.

Ownership lists are initial boundaries, not a mandate to create all listed files. Rename or narrow them at the checkpoint when actual consumers justify a better layout. The integrator coordinates overlaps, particularly `go.mod`, `go.sum`, migrations and sqlc configuration, composition, and CI. Temporarily delegate a shared file to one worker rather than allowing simultaneous uncoordinated edits.

Each task owns its tests, failures, documentation, and observability needed for its behavior. Do not defer all tests to the integration lane. Scope changes require updating the backlog and notifying affected workers. Record a newly discovered dependency rather than hiding a wait in chat.

Use independent checkout-local databases, cache directories, credentials, ports, and temporary data. Workers must not share customer owner credentials or manufacture human approval. Memtrace Fleet may assist after its scope and worktree behavior are validated; local task assignments and versioned contracts remain sufficient to coordinate safely.

## Completion and review

A task is complete when its acceptance scenarios and applicable negative cases pass, the reviewer resolves material findings, actual merge dependencies are integrated, and evidence identifies the exact tested commit. Use [the evidence template](task-evidence-template.md). Contract changes must be reviewed by affected consumers before completion.

The phase integrator verifies the full exit criteria, all task results, current base compatibility, and applicable Windows/Linux CI. Persistence, GitHub, backup, restore, and execution claims require their real-component checks as those components arrive. Coverage percentages do not substitute for authority and concurrency scenarios.

Publish the phase PR through the bot. A green gate is evidence, not merge authority: the user authorizes integration into `main`. Verify the resulting main run and record the PR/merge revision. Only then mark the phase integrated. Local readiness, publication, CI success, and merge are separate states.

## Changes and blockers

For a technical choice within accepted policy, the responsible owner can resolve it and document the evidence. For an unresolved product policy, present the concrete options and required decision to the owner; continue independent work. Do not convert a decision gate into a default merely to keep every worker busy.

If bot credentials or permissions are unavailable, finish the reviewable branch and PR body and report publication as pending. Do not switch to the user's personal account. If a check fails, investigate the failed behavior and rerun affected checks; avoid weakening a gate to obtain green status.
