# ADR 0006: Provider-agnostic domain in a modular monolith

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Scope:** Application boundaries and integration strategy.
- **Origin:** Original system-design direction, retained in the SuiteWard decisions.

## Context

CircuitoNE / CircuitoNE-QA is PoC #0 for an external canonical test contract. SuiteWard generalizes that model into a product while keeping the initial developer experience in GitHub.

The MVP needs a cohesive implementation that is straightforward to operate. The product should remain usable with a future managed deployment without making provider identifiers part of its core business rules.

## Decision

Build a modular monolith with three responsibility boundaries:

| Boundary | Responsibility |
| --- | --- |
| Control plane | Projects, canonical suites, versions, proposals, approvals, policies, verification decisions, promotion, and audit. |
| Execution plane | Materialize authorized execution plans, coordinate execution, and collect bound evidence. |
| SCM adapter | Map GitHub repositories, PRs, identities, comments, and checks to application operations and publish their results. |

These are code and authority boundaries, not a requirement for three independent services. API and worker packaging can share a codebase and artifacts. Separate privileges and untrusted execution where the trust model requires it.

Use Go and Chi as recorded in [ADR 0003](0003-go-and-chi.md). Keep HTTP handlers, generated SQL access, GitHub payloads, River jobs, MCP messages, and storage implementations outside the domain model.

Core entities are Project, Suite, SuiteVersion, Manifest, RunnerProfile, ChangeProposal, Approval, Verification, Promotion, Policy, Principal, ExternalIdentity, Attestation, ExecutionPlan, and AuditEvent. Exact aggregate boundaries and persistence layouts remain implementation work.

Use provider-neutral source, revision, change, and identity references. GitHub installation IDs, login names, PR numbers, and check IDs belong in adapter mappings. GitHub is the sole initial SCM implementation. Provider neutrality does not commit the MVP to a plugin framework or a second provider.

Retain the same domain and application services for self-hosted and possible managed operation. Deployment-specific adapters can evolve separately.

## Alternatives and consequences

- **GitHub concepts embedded throughout the domain:** quicker direct wiring, but ties governance rules to one transport and makes future changes harder to isolate.
- **Microservices from the start:** independent deployment units, but more coordination and operational work than the initial scope warrants.
- **Implementing multiple providers immediately:** exercises abstraction early, but expands the MVP without a current user requirement.

The selected design keeps deployment modest and rules explicit. Module boundaries still require discipline; a monolith alone does not isolate candidate code or protect credentials.

## Open details

- Concrete aggregate structure; package organization and dependency direction are now defined in [ADR 0024](0024-single-module-project-structure.md).
- Process packaging and privilege separation.
- Additional SCM adapters, only when a concrete need appears.

The default deployment and local integration are defined in [ADR 0001](0001-self-hosted-github-synchronization.md); execution sequencing is defined in [ADR 0008](0008-progressive-integrity-and-execution.md).

