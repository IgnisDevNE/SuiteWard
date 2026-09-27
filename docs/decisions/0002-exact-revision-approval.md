# ADR 0002: Human approval of an exact proposal revision

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Product:** SuiteWard
- **Scope:** GitHub approval experience for changes to the canonical test contract.
- **Related:** [ADR 0001: Self-hosted deployment and GitHub synchronization](0001-self-hosted-github-synchronization.md).

## Context

SuiteWard uses agent-triggered MCP synchronization and independent periodic reconciliation. A PR may change between the moment a human comments and the moment SuiteWard reads the comment. Approval must therefore identify the exact proposal revision being authorized.

A repository may contain and propose tests, but it cannot redefine its own canonical test contract. Human approval remains subject to SuiteWard policy and does not replace verification or atomic promotion.

## Decision

The human approves from the PR using this command:

```text
/suiteward approve P42-R3
```

`P42-R3` is an example of a short, explicit proposal revision reference. It resolves to an immutable revision in the context of the connected project and PR. It is not an approval credential or a substitute for checking the author's identity.

The accepted command shape is `/suiteward approve <proposal-revision-reference>`. Exact identifier encoding remains an implementation detail. A bare `/suiteward approve` must not be interpreted as approval of whichever revision happens to be current when processed.

## User flow

1. SuiteWard prepares an immutable revision of the proposed contract change.
2. SuiteWard publishes a summary in the PR, the revision reference, and the complete command ready to copy.
3. An authorized human posts that command as a PR comment.
4. SuiteWard reads the comment through its GitHub adapter, validates identity and permissions, and resolves the reference within that project and PR.
5. SuiteWard records approval for that exact revision and publishes an acknowledgment identifying the approved revision and its copyable withdrawal command, following [ADR 0011](0011-approval-revocation-and-acknowledgments.md).
6. If approved inputs change, SuiteWard creates a new revision and publishes a new summary and command. The new revision requires a new approval.

This flow must work through periodic reconciliation even if no agent invokes the MCP tool.

## Binding and stale references

The short reference must resolve to the immutable proposal identifier, revision, content digests, and applicable approval context. A reference must never be recycled to mean different content or resolved against another project's proposal.

If a command identifies a superseded or otherwise ineligible revision, reject it with a clear explanation and the current revision's review link and command. Never silently redirect it to the newest revision.

The proposal revision and the PR HEAD are distinct concepts. Changes to any input covered by the approval require a new revision. A push must cause SuiteWard to reassess that binding; matching test files alone do not prove that the complete approved context is unchanged. Execution evidence always remains bound to the exact commit and execution inputs it verified.

Resolve and validate the revision at processing time, and enforce the revision's eligibility when recording approval. A concurrent proposal update must not allow approval to be attached to replacement content.

## Authority and replay

- Fetch the comment and author identity from GitHub. Apply SuiteWard's configured approval policy and required permissions; do not infer authority from text or MCP arguments.
- Grant the agent's MCP interface synchronization and status access only. It cannot manufacture human approval or promote a revision directly.
- Process repeated observations of the same comment idempotently. Retries and duplicate jobs must not create additional approval votes or effects.
- Preserve an audit record linking the source comment, authenticated external identity, internal principal, and exact approved revision.
- Record approval separately from promotion. Required verification, policy checks, and atomic canonical updates still apply.

[ADR 0022](0022-contract-change-pr-priority.md) adds `/suiteward prioritize` as a separate human scheduling command. It does not approve a proposal or replace this exact-revision command; waiting or active queue status remains distinct from consent.

## Acceptance criteria for implementation

1. A PR summary supplies a complete copyable command containing its proposal revision reference.
2. An authorized human's valid command approves only the referenced eligible revision.
3. A missing, unknown, cross-project, or superseded reference cannot approve the current revision implicitly.
4. A revision change between comment creation and polling does not transfer the approval to new content.
5. Duplicate observations of a comment do not duplicate approvals or count one principal multiple times.
6. Changes to approved inputs require a new revision and approval; stale verification evidence is not reused for another commit.
7. MCP arguments or an unauthorized commenter cannot satisfy the human approval requirement.
8. Approval alone does not bypass verification or cause an invalid promotion.

## Remaining implementation details

- Exact revision reference encoding and command parser rules.
- Wording and update strategy for PR summaries and acknowledgments.
- Implement revocation, processed comment handling, and durable PR acknowledgments according to [ADR 0011](0011-approval-revocation-and-acknowledgments.md).

These details do not reopen the accepted explicit-revision approval flow.
