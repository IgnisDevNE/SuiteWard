# ADR 0001: Self-hosted deployment and GitHub synchronization

- **Date:** 2026-09-26
- **Status:** Accepted design decision; not yet implemented.
- **Product:** SuiteWard
- **Scope:** Hosting, artifact storage, GitHub onboarding, local MCP integration, and periodic reconciliation.

## Context

SuiteWard initially serves individual developers working with AI agents. It is open source and self-hosted first, with an architecture that can later support a managed service. Its product language is English.

The installation should require little infrastructure knowledge. Users should connect repositories through a GitHub App and work primarily in GitHub. A forgotten agent tool call must not leave a pull request undiscovered or indefinitely waiting for synchronization.

The canonical contract remains under SuiteWard's control:

> A repository may contain and propose tests, but it cannot redefine its own canonical test contract.

This decision refines the earlier Test Vault System Design v0.1. It replaces mandatory S3-compatible infrastructure and inbound webhook delivery in the default local deployment. It preserves the provider-agnostic domain, immutable suite versions, exact approvals, evidence binding, and atomic promotion.

The [ADR index](README.md) links the accepted architecture, authority, delivery, and license decisions that complement this record.

## Accepted decisions

| Area | Decision |
| --- | --- |
| Hosting | One self-hosted instance can manage multiple connected repositories. The initial control-plane deployment targets one host using Docker Compose. |
| Runtime stack | Go and Chi ([ADR 0003](0003-go-and-chi.md)), PostgreSQL with pgx and sqlc ([ADR 0004](0004-postgresql-pgx-sqlc.md)), and River OSS ([ADR 0005](0005-river-background-jobs.md)). |
| Artifact storage | A dedicated local persistent volume, managed by the supplied deployment configuration. Artifacts remain content-addressed. |
| GitHub integration | A GitHub App associated with the self-hosted instance, installed on the intended GitHub account or organization with selected repository access. |
| Default network model | Outbound connections to GitHub. No public inbound endpoint, purchased domain, tunnel, relay, or SuiteWard-operated cloud service is required for routine synchronization. |
| Fast synchronization | A local MCP tool requests synchronization after an agent opens or updates a PR. |
| Recovery synchronization | An independent periodic scheduler discovers and reconciles relevant GitHub state even when no MCP call occurs. |
| Human approval | A PR comment explicitly names the proposal revision: `/suiteward approve P42-R3`. See [ADR 0002](0002-exact-revision-approval.md). |
| Background processing | MCP requests and scheduled reconciliation use River and the same GitHub request budget. |
| Future options | Webhook ingestion and an S3-compatible storage adapter remain optional extensions. |
| Managed hosting | Reuse the same domain and application services. Execution on provider infrastructure versus customer infrastructure remains open. |

"Local" refers to the machine hosting SuiteWard; it can be a workstation, home server, or VPS. One instance is shared across repositories. This does not require installing SuiteWard separately inside every repository.

## Deployment and storage

The default installation contains the SuiteWard API/control plane, background processing, PostgreSQL, and persistent artifact storage. API and worker process packaging remains an implementation detail. Docker test execution is introduced separately in the execution milestone.

The supplied Compose configuration should create and reuse persistent storage automatically. Docker volumes persist independently of individual container lifecycles. [Docker volumes](https://docs.docker.com/engine/storage/volumes/).

Each connected repository maps to its own Project, with separate suites, policies, approvals, and history. Shared infrastructure does not confer cross-project authority. The initial scope is an operator-managed installation, not a public multi-tenant service.

Canonical artifacts live outside protected repositories and candidate execution workspaces. The storage adapter must enforce content identity and must never overwrite the bytes of an existing canonical object. Untrusted execution must not receive write access to the canonical store, database, or GitHub App credentials.

The single-host control-plane default does not settle the production isolation model for untrusted Docker execution. Preserve the execution trust boundary when that milestone is implemented.

Provide a documented backup and restore procedure covering PostgreSQL, all referenced artifacts, and required instance configuration and credentials. A persistent volume alone is not a backup. [ADR 0018](0018-corrective-pr-rollback-and-audit-history.md) requires retention of all historical canonical versions and their reconstructable audit history; backup coverage must include them. [ADR 0019](0019-encrypted-backups-and-instance-recovery-key.md) defines local and encrypted repository copies, one external recovery kit per instance, and a version ledger with shared distinct file contents. [ADR 0020](0020-guided-local-cli-recovery.md) selects guided CLI reconstruction with operational credentials reestablished separately; physical backup format and detailed reconnection mechanisms remain open.

Keep storage behind a small application boundary so an S3-compatible adapter can be introduced without changing domain authority. Implementation of that adapter is deferred until needed.

## GitHub App onboarding

1. Start the SuiteWard instance and open its local setup flow.
2. Register an instance-associated GitHub App using an assisted manifest-based flow.
3. Install the App on the intended account or organization and select repositories.
4. Discover authorized repositories through GitHub's API and create their Project records.
5. Continue the integrated setup: the verified owner reviews and confirms the required protection, and SuiteWard applies and verifies it under [ADR 0016](0016-integrated-protection-onboarding.md).
6. Explicitly approve each project's initial canonical baseline through the bootstrap process in [ADR 0010](0010-repository-bootstrap.md). Enabling protection does not replace this approval.
7. Configure the agent's local MCP connection once.

GitHub supports preconfigured App registration through a manifest and repository selection during installation. App visibility must match the accounts where it will be installed. [App manifests](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest), [App installation](https://docs.github.com/en/apps/using-github-apps/installing-your-own-github-app).

The default local mode has App webhook delivery disabled. API authentication and repository permissions remain necessary. GitHub allows webhook delivery to be deactivated independently. [App configuration](https://docs.github.com/en/apps/maintaining-github-apps/modifying-a-github-app-registration).

The setup flow must work without a publicly reachable webhook endpoint. Validate its browser redirect and headless deployment behavior during onboarding implementation. Use the minimum permissions needed for repository inspection, approval identity checks, comments, SuiteWard checks, and the protection configuration accepted in ADR 0016. Administrative write capability for that setup is accepted; settle the remaining permission matrix against the implemented operations.

Connecting a repository grants integration access. It does not automatically approve the repository's current tests as canonical.

## Local MCP synchronization

The MCP interface is a local adapter to SuiteWard application services. Prefer a stdio bridge for the initial local-agent experience. MCP supports client-launched subprocess communication over stdio. [MCP transports](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports).

Proposed tool names and payloads, to finalize during implementation:

| Tool | Input | Result |
| --- | --- | --- |
| suiteward_sync_pull_request | A reference to a PR in a connected repository, such as its URL | An accepted synchronization identifier and queued or already-pending status |
| suiteward_get_status | A synchronization identifier | Processing state, observed revision when available, and relevant verification/check references |

The agent workflow should invoke synchronization after PR creation and after further pushes. Instructions or a supported client hook provide that behavior; merely exposing an MCP tool does not make the invocation automatic.

The MCP adapter validates caller access and resolves references only within authorized projects and supported GitHub hosts. It is not a general URL-fetching interface. Its credentials grant scoped synchronization/status access, not approval or administrative authority.

The request schedules work durably and returns promptly. SuiteWard then fetches the PR, current revision, comments, and relevant identities directly from GitHub. Agent-supplied fields are hints to validate, not authoritative evidence. The GitHub API exposes PR inspection separately from webhook delivery. [Pull request API](https://docs.github.com/en/rest/pulls/pulls#get-a-pull-request).

A local agent can use this interface without a public endpoint. An agent running in a separate cloud environment needs an explicitly configured route to the instance; that connectivity is not supplied automatically by MCP.

## Independent periodic reconciliation

Run a scheduler independently of the agent and its MCP sessions. It must:

- Discover new PRs across connected repositories, including PRs never mentioned in an MCP call.
- Reconcile tracked PRs, head revisions, relevant approval comments, and closed or merged state.
- Refresh repository access and installation state so additions, removals, and revoked access are handled.
- Recheck effective merge protection, surface relevant changes without repeated notices, and require specific human confirmation for repairs under [ADR 0017](0017-protection-change-detection-and-confirmed-repair.md).
- Persist progress and resume reconciliation after service restarts or temporary GitHub failures.
- Keep incomplete pagination or failed fetches from advancing a successful synchronization checkpoint.
- Schedule work fairly so frequently triggered projects do not starve untouched repositories.

Both MCP and scheduled work enter the same reconciliation path. Coalesce duplicate requests without losing updates that arrive while a job is running; retain a follow-up reconciliation when necessary. Business effects must remain idempotent.

Use a periodic schedule supported by River's open-source core, with persisted application checkpoints and startup recovery. Do not silently introduce a dependency on paid durable-scheduling features.

### GitHub API rules

Use authenticated, scoped requests on a configurable schedule. Share one controlled request budget between MCP-triggered jobs and periodic jobs.

Where supported, cache ETag or Last-Modified values and use conditional GET requests. Authenticated 304 responses avoid primary quota consumption; secondary limits still apply. Keep parameters stable and follow pagination links.

Honor X-Poll-Interval whenever supplied. Respect Retry-After and wait for x-ratelimit-reset when x-ratelimit-remaining is zero. For secondary throttling without a supplied delay, wait at least one minute, then use exponential backoff and bounded retries. Queue requests serially to avoid bursts; pace mutating API calls. Report persistent errors instead of retrying indefinitely.

GitHub prefers webhooks but documents efficient polling when webhooks cannot be used. These are operating constraints, not a promise of a universal safe polling interval. [GitHub REST API best practices](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api).

The numerical default interval remains to be validated against repository count, acceptable delay, and API usage. A request for immediate synchronization must never override GitHub's rate-limit delays.

### Coverage and approval correctness

Reconcile the actual resources needed by the workflow. Do not assume a generic events feed or a single PR timestamp captures every relevant change. Polling current state cannot reconstruct every transient edit or a comment deleted before observation.

Any approval accepted through this path must identify the exact immutable proposal revision and content covered by the approval. Never attach an older approval to whatever HEAD is current when the next poll runs. Revalidate the required identity and permissions through the normal approval service. [ADR 0002](0002-exact-revision-approval.md) defines the accepted comment syntax and handling of superseded revisions.

An observed head change invalidates evidence that no longer matches the evaluated revision. MCP calls and scheduler jobs cannot bypass canonical authority, exact approval, verification, or atomic promotion.

## Availability and user-visible behavior

With SuiteWard running, valid access, and available GitHub quota, an omitted MCP call causes a synchronization delay rather than an indefinite dependency on the agent. Discovery latency includes the configured schedule, queue backlog, API backoff, and processing time.

The instance must remain running to perform work. After downtime, catch up using persisted state. Genuine test failures, missing approvals, and access or infrastructure errors can still prevent completion.

Keep synchronization state separate from verification results. Do not report a successful check merely to unblock a PR when synchronization, approval, or verification is incomplete. Surface the reason and retry state when possible; if GitHub is unreachable, expose it locally until publication can resume.

MCP-triggered synchronization is an acceleration path. Periodic reconciliation is a required part of the local operating model.

## Acceptance criteria for implementation

1. A fresh local installation connects repositories and synchronizes using only outbound GitHub access.
2. A newly opened PR is discovered and processed without any MCP call.
3. Opening or updating a PR followed by an MCP call requests prompt synchronization within the shared rate budget.
4. Repeated triggers and concurrent scheduler work do not duplicate promotions or lose subsequent revisions.
5. Human approval comments are detected without an agent trigger and remain bound to the exact approved revision.
6. Rate-limit responses postpone both ingestion paths correctly without producing a false successful check.
7. Restarting the instance recovers pending work and discovers changes made while it was offline.
8. Removing repository access stops unauthorized processing and produces an actionable local state.
9. A backup restores metadata and every artifact referenced by canonical versions.
10. Local untrusted execution cannot access canonical storage or control-plane credentials.

## Open implementation decisions

- Default polling interval, supported repository counts, and discovery-delay targets.
- Remaining GitHub App permission details and fully local/headless onboarding mechanics; [ADR 0016](0016-integrated-protection-onboarding.md) selects integrated automatic protection setup with administrative write capability.
- Final MCP tool schemas, client configuration, and scoped authentication.
- Implement approval/revocation and PR acknowledgment details under [ADR 0002](0002-exact-revision-approval.md) and [ADR 0011](0011-approval-revocation-and-acknowledgments.md).
- Physical backup format and transient-data retention under ADR 0019, detailed CLI reconstruction/recredentialing mechanisms under ADR 0020, and Release publication/contingency details under [ADR 0021](0021-release-backups-and-health-reconciliation.md); all historical canonical versions and their referenced artifacts are retained under ADR 0018.
- Production execution isolation and, later, managed-hosted versus customer-hosted execution.

These items do not reopen the accepted hosting and synchronization choices.

