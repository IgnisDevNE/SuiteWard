# ADR 0008: Deliver integrity before canonical execution

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Scope:** Milestone order, assurance levels, and first execution backend.
- **Origin:** Roadmap recovered from the original system design.

## Context

The product can provide value by protecting the canonical test contract before operating an independent execution environment. Making every execution and supply-chain capability a prerequisite would delay that initial value.

Governance and independent execution offer different guarantees and must be described separately.

## Decision

Deliver the product in this order:

| Milestone | Delivery | Exit evidence |
| --- | --- | --- |
| M0: Domain Proof | Model and exercise immutable versions, proposals, exact approvals, policies, promotion, and concurrency. | Executable positive and negative cases for the governing invariants; GitHub and Docker are not prerequisites for the domain proof. |
| M1: GitHub Integrity Vault | Bootstrap, protected inventory, proposals, human approval, integrity checks, promotion, audit, and usable self-hosted operation. Include the App, MCP trigger, and independent polling from ADR 0001. | A connected project can govern and evolve its contract; retries and conflicts preserve state; results explicitly describe integrity assurance. |
| M2: Canonical Execution | ExecutionPlan, executable RunnerProfile, DockerExecutionBackend, isolated candidate execution, trusted observation, and bound execution evidence. | Real failures fail, corrections pass, and candidate-controlled commands or reports cannot replace the trusted verification process. |
| M3: Supply-chain hardening | Add signing, key management, provenance, transparency, and related controls where a demonstrated need justifies them. | Each adopted control has a concrete threat, verification, and operational cost assessment. |

M1 is a usable integrity MVP. It does not require a Docker execution backend. Docker Compose for service packaging is distinct from DockerExecutionBackend for running candidate code.

The first execution backend in M2 is DockerExecutionBackend. SuiteWard coordinates an existing container runtime; it does not build its own runtime or claim that containers alone provide every necessary isolation property.

Keep required assurance explicit:

- **Integrity:** canonical content, governance, approved changes, and promotion provenance.
- **Execution:** integrity plus execution under the required trusted plan, isolation, and evidence collection.

An installation requiring execution must not silently accept integrity-only evidence when its execution backend is unavailable. A policy change follows the existing governance process.

M1 may inspect checks from the customer's existing CI when policy requires them. Record their source and revision without presenting them as SuiteWard-independent execution. Promotion requires post-integration validation of the exact integrated revision: integrity and governance in M1, plus canonical execution when required in M2. [ADR 0014](0014-merge-triggered-canonical-promotion.md) defines the default M1 flow: rely on the project's merge process for test acceptance, preserve exact approval, validate integrated contract integrity, and conditionally promote without executing tests. Optional external CI gates and strict integration coordination remain open.

Authenticate evidence emitters from the start. Model attestations so stronger exportable signatures can be added later; do not require optional KMS, OIDC, or transparency integrations for the initial increment. This deferral does not defer basic access control, backup, or audit.

## Alternatives and consequences

- **Execution-first MVP:** earlier execution assurance, with a larger initial operating and isolation burden.
- **One undifferentiated green check:** simpler wording, but obscures which guarantee was actually established.
- **Mandatory advanced signing in M1:** more upfront deployment requirements before the initial governance workflow is proven.

The chosen sequence exposes useful governance early while making the limits of each release visible.

## Open decisions

- First supported test ecosystem and executable RunnerProfile; deferred to M2 rather than required for the integrity MVP.
- HTTP/E2E with Playwright is a candidate from the design discussion, not an accepted selection.
- Untrusted execution isolation, private dependencies, network access, and secrets.
- Exact external CI gates allowed by M1 policy.
- Which hardening controls become necessary in M3.
- Whether a future managed service executes on provider infrastructure, customer infrastructure, or both.

See [ADR 0006](0006-provider-agnostic-modular-monolith.md) for boundaries and [ADR 0007](0007-canonical-contract-authority.md) for invariant requirements.

