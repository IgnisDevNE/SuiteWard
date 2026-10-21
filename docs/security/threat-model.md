# SuiteWard threat model

- **Updated:** 2026-10-20
- **Status:** Active. Describes the assets, actors, trust boundaries, threats and controls of the released system.
- **Scope:** SuiteWard as installed under [ADR 0027](../decisions/0027-agent-human-trust-separation.md): the service, its GitHub App, its PostgreSQL database and its image and release pipeline.

This document backs the claims of [ADR 0007](../decisions/0007-canonical-contract-authority.md), [ADR 0012](../decisions/0012-mvp-authorization-policy.md) and [ADR 0015](../decisions/0015-required-contract-check.md). A control is cited by `file:line`, and a test by its name.

## 1. Scope and method

Method: STRIDE (spoofing, tampering, repudiation, information disclosure, denial of service, elevation of privilege) applied to each trust boundary of section 5.

Reference lists, cited by identifier in the tables:

| List | Used for | Identifiers |
| --- | --- | --- |
| [OWASP Top 10 CI/CD Security Risks](https://owasp.org/www-project-top-10-ci-cd-security-risks/) | The release pipeline, branch protection and the check | CICD-SEC-1 to CICD-SEC-10 |
| [OWASP ASVS](https://owasp.org/www-project-application-security-verification-standard/) 4.0.3 | The service itself | V2 authentication, V5 validation, V7 error handling and logging, V8 data protection, V13 API, V14 configuration |
| [OWASP Top 10 for LLM Applications 2025](https://genai.owasp.org/llm-top-10/) | The AI agent as an adversary of the repository | LLM01 prompt injection, LLM02 sensitive information disclosure, LLM05 improper output handling, LLM06 excessive agency |
| [OWASP Top 10 for Agentic Applications](https://genai.owasp.org/) | GitHub text written into comments that agents read; agents holding credentials | Goal hijack; identity and privilege abuse |

## 2. Security goal

No change to a protected file reaches the default branch's canonical contract without the owner's TOTP approval of that exact revision ([ADR 0007](../decisions/0007-canonical-contract-authority.md), [ADR 0012](../decisions/0012-mvp-authorization-policy.md), [ADR 0027](../decisions/0027-agent-human-trust-separation.md)). The published `SuiteWard / Contract` check cannot be forged by another GitHub App or by a pull request's own content ([ADR 0015](../decisions/0015-required-contract-check.md)).

Invariants behind the goal: canonical immutability, candidate non-authority, exact-revision approval, atomic promotion, and evidence is not authority ([AGENTS.md](../../AGENTS.md#invariants)).

## 3. Assets

| Asset | Why it matters | Held in |
| --- | --- | --- |
| Canonical contract and its history | The authority every approval and check is judged against | PostgreSQL; artifact volume |
| Approval authority | Turns a comment into an approval: the TOTP secret, the owner binding by numeric account id, and the consumed steps | PostgreSQL |
| GitHub App private key and installation tokens | Publish comments and check runs; read repositories | Podman secret mounted as a file; process memory |
| The `SuiteWard / Contract` check | What branch protection waits for | GitHub check runs of the App |
| Command ledger and outbox | Record every command once and publish each acknowledgment once | PostgreSQL |
| The database | Holds every asset above except the key | PostgreSQL container, private network |
| The image and release pipeline | What runs on the host | GitHub Actions, the registry |

## 4. Actors

| Actor | Capability | Position |
| --- | --- | --- |
| AI agent with repository write access | Pushes branches, opens pull requests, edits tests and `.suiteward.yml`, writes comments, reads GitHub text | Primary adversary (ADR 0027 context) |
| Outside GitHub user | Opens fork pull requests, writes comments and issue text | Adversary |
| Compromised dependency, action or base image | Runs code in CI or in the image | Adversary |
| Co-located process on the developer machine | Runs as the same OS user as SuiteWard (co-located tier) | Reduced assurance, labeled in [install.md](../install.md) section 2 and ADR 0027; the isolated tier gives the full assurance |
| Trusted host administrator | Controls the host, the database and the credentials | Trusted: out of scope (section 7) |
| Owner | Holds the authenticator and the GitHub account bound at setup | Trusted principal |

## 5. Trust boundaries

Two installation tiers share the same image ([ADR 0027](../decisions/0027-agent-human-trust-separation.md); [verification targets](../plan/README.md#verification-targets)):

| Tier | Shape | Assurance |
| --- | --- | --- |
| Isolated | Another OS user, VM or host; outbound-only; status and MCP reached through a forwarded loopback port | Full |
| Co-located | Same machine and user as the agent; SuiteWard reports the tier | Labeled, as published |

| Id | Boundary | Crossing |
| --- | --- | --- |
| B1 | GitHub to SuiteWard | Polling reads of pull requests, comments, trees and check runs; no webhooks |
| B2 | SuiteWard to GitHub | App installation tokens, one per purpose; writes limited to comments and check runs |
| B3 | Owner to SuiteWard | `/suiteward approve <ref> <code>` in a pull request comment |
| B4 | Host listeners | `/status` on loopback; `/mcp` on loopback with a bearer token |
| B5 | SuiteWard to PostgreSQL | Private container network, secret-mounted URL, history tables protected by triggers |
| B6 | CI and release to registry to host | Workflows, owner-approved environments, image promoted by digest |

## 6. STRIDE per boundary

### B1: GitHub to SuiteWard

| STRIDE | Threat and abuse case | Control | Test | Ref |
| --- | --- | --- | --- | --- |
| S | A user other than the owner posts an approval | A command counts only from the enrolled owner's numeric account id; the bot's comments and comments before enrollment are ignored: `internal/app/commands.go:217-227` | `TestCommandsJobRecordsEveryDisposition` (another account, the bot, before enrollment; `internal/app/commands_test.go`) | ASVS V2; CICD-SEC-2 |
| S | A comment is edited after the owner wrote it, or a command is replaced | A comment already edited when first seen is rejected and uses no step: `internal/app/commands.go:201-202` | `TestCommandsJobUsesNoStepForAnEditedCommand`, `TestCommandsJobRecordsEveryDisposition` (`internal/app/commands_test.go`) | ASVS V2; LLM01 |
| T | The pull request rewrites `.suiteward.yml` to narrow what is protected | The head tree is judged by the canonical declaration, not the pull request's own: `internal/application/inventory/inventory.go:109-117`; a scope reduction gets a proposal like a protected change | `TestScopeReductionGetsAProposalLikeAProtectedChange`, `TestAssessHeadRejectsAGoverningScopeThatIsNotTheCanonicalsDigest` (`internal/application/governance/head_test.go`) | CICD-SEC-1; CICD-SEC-4 |
| T | A push after approval changes a protected file | Approval binds the exact revision; a protected push needs a fresh approval: `internal/domain/contract/consent.go:136-166`, `:200-214` | `TestProtectedPushNeedsAFreshApproval` (`internal/application/governance/head_test.go`); `internal/domain/contract/consent_test.go` | CICD-SEC-1 |
| T | A malformed path escapes the declared scope (`..`, backslash, NUL); a symlink stands in for a protected path or hides one below a pattern | Paths are validated: `internal/domain/artifact/manifest.go:76-86`; patterns are parsed and matched by fixed rules: `internal/domain/scope/pattern.go:41-117`; a covered link or a link a pattern may reach below makes the declaration invalid; link resolution is bounded to 8 hops: `internal/application/inventory/inventory.go:144` | `TestManifestRejectsInvalidPathsWithoutRewriting` (`internal/domain/artifact/manifest_test.go`); `TestPatternMatchRejectsInvalidPath` (`internal/domain/scope/pattern_test.go`); `TestBuildKeepsACoveredLinkUnsafeWhateverItPointsAt`, `TestBuildResolvesLinkTargets` (`internal/application/inventory/link_test.go`) | ASVS V5; CICD-SEC-4 |
| T | A tree too large for GitHub's listing hides files | A truncated recursive tree is rejected: `internal/adapters/github/github.go:442-443` | `TestTreeRejectsATruncatedTree` (`internal/adapters/github/github_test.go`) | ASVS V5 |
| S | A fork pull request carries a hostile declaration or tests | Fork pull requests fail closed: not read, not assessed, no proposal: `internal/app/checksjob.go:313` | `TestChecksFailClosedOnAForkPullAndNeverAssessIt` (`internal/app/checksjob_integrity_test.go`) | CICD-SEC-4; CICD-SEC-5 |
| S | Another App publishes a check with the same name | Check runs are read filtered by the App's id: `internal/adapters/github/checks.go:119-133` | `TestChecksRunsQueriesBySHAEscapedNameAndApp` (`internal/adapters/github/checks_test.go`) | CICD-SEC-1; CICD-SEC-2 |
| I | A GitHub answer or comment text reaches a log or an error | In the GitHub adapters and the command job, errors carry typed categories and the path, never the answer text; acknowledgment bodies take no comment text, so a body never echoes a comment or a code: `internal/application/commandtext/commandtext.go:25-26` | `TestGetErrorsNeverCarryTheAnswerText` (`internal/adapters/github/rest/rest_test.go`); `TestUndecodableAnswerNamesThePathNotTheBody` (`internal/adapters/github/github_test.go`) | ASVS V7; CICD-SEC-10 |
| D | An oversized answer exhausts memory or the rate budget | Bodies over the limit are an error, not truncated; one process-wide request budget with pacing | `TestBodiesOverTheLimitAreAnErrorNotTruncated`, `TestBudgetAllowsFractionThenRefusesUntilReset` (`internal/adapters/github/rest/rest_test.go`) | ASVS V13 |
| D | A pull request head carries an oversized `.suiteward.yml` to exhaust the YAML parser | A declaration read from an untrusted tree is capped at 64 KiB and refused as an invalid source; the canonical stored on the instance is not subject to the cap: `maxDeclarationBytes` in `internal/app/bootstrap.go` (`githubTree.Read`) | `TestGitHubTreeCapsTheUntrustedDeclaration`, `TestChecksFailAHeadWhoseDeclarationExceedsTheCap`, `TestChecksAcceptAHeadWhoseDeclarationIsExactlyTheCap` (`internal/app/declaration_cap_test.go`) | ASVS V13 |

### B2: SuiteWard to GitHub

| STRIDE | Threat and abuse case | Control | Test | Ref |
| --- | --- | --- | --- | --- |
| E | A token carries more permission than its use needs | One exact permission map per purpose (read-only sync, read-only protection audit, comment write, check write): `internal/app/github.go:185-193`; a token whose granted map differs from the request is rejected: `internal/adapters/github/appauth/appauth.go:337-339` | `TestTokenFailuresAreErrorsAndNothingIsCached` (wider, narrower and different-level cases, `internal/adapters/github/appauth/appauth_test.go`); `TestSetupMintsTheTwoTokensWithTheirOwnPermissions` (`internal/app/setup_test.go`) | CICD-SEC-2; CICD-SEC-5; ASVS V14 |
| I | A token or key is printed | A token's value is never rendered under any verb; key errors do not echo input | `TestNoTokenValueIsPrintedInAnyPosition`, `TestNewAppRejectsOtherKeysWithoutEchoingInput` (`internal/adapters/github/appauth/appauth_test.go`); `TestErrorsNeverContainTheToken` (`internal/adapters/github/rest/rest_test.go`) | CICD-SEC-6; ASVS V8 |
| T | Repository text posted in a comment injects markup or instructions that an agent or the owner reads | Fences are sized longer than any backtick run in their content; characters that cannot be displayed are shown escaped; a declaration that cannot be shown verbatim is refused: `internal/application/commandtext/render.go:506-516`; fences: `:440-444`; escaping: `:483-496`; the hidden marker ends the body: `:559-580` | `TestADeclarationWithAHiddenCharacterIsRefused` (`internal/application/commandtext/hiddencharacters_test.go`); `TestScopeSuggestionBodyQuotesTheItemsAsTheListDoes` (`internal/application/commandtext/quoting_test.go`) | LLM01; LLM05; agentic goal hijack |
| R | A check or comment is posted twice or lost | Acknowledgments are published from an outbox written in the same transaction as the command; each carries a hidden marker that identifies it (`HasMarker`, `internal/application/commandtext/render.go:577`) | `TestCommandsJobRepeatsEarlierStepsHarmlesslyAfterACrash` (`internal/app/commands_test.go`) | ASVS V7 |
| S | The protection of the default branch does not require the check | The protection audit requires the contract check with the App as its source, and the test check: `internal/application/protection/protection.go:154-160` | `TestEvaluate` (`internal/application/protection/protection_test.go`) | CICD-SEC-1; CICD-SEC-7 |
| I | A publisher panic carries a payload or a secret into the outbox error or the log | A panic is stored and logged as a fixed classification and a short SHA-256 digest of its text, never the text: `publish` in `internal/adapters/river/relay.go` | `TestRelayNeverStoresOrLogsAPanicText` (`internal/adapters/river/security_test.go`) | ASVS V7; ASVS V8 |

### B3: Owner to SuiteWard

| STRIDE | Threat and abuse case | Control | Test | Ref |
| --- | --- | --- | --- | --- |
| S | An agent with the owner's `gh` credentials posts an approval | Every approve and revoke needs a current TOTP code (RFC 6238); the secret is held by the instance. On the isolated tier it is outside the agent's reach: ADR 0027 | `TestCodeMatchesRFC6238Vectors` (`internal/domain/totp/totp_test.go`) | ASVS V2; agentic identity and privilege abuse; LLM06 |
| S | A guessed or timing-probed code | Constant-time comparison of every candidate: `internal/domain/totp/totp.go:104-136` | `TestVerifyToleranceWindow`, `TestVerifyRejectsMalformedCodes` (`internal/domain/totp/totp_test.go`) | ASVS V2 |
| R | A code posted in a public comment is replayed | A step is consumed once per owner across all commands and never purged: `internal/adapters/postgres/owner.go:145-168`; `totp_consumed_steps` rejects update, delete and truncate (migration 00012) | `TestCommandsJobBindsACodeToTheFirstCommandThatUsesIt` (`internal/app/commands_test.go`); `TestOwnerHistoryTablesRejectMutation` (`internal/adapters/postgres/owner_test.go`) | ASVS V2 |
| S | A late or early code is accepted | A code is valid for the steps one either side of the comment's `created_at`, not of processing time: `internal/app/commands.go:28`, `:252` | `TestCommandsJobAcceptsTheCodesOfTheToleranceAndNoOther` (`internal/app/commands_test.go`) | ASVS V2 |
| I | A code or secret reaches a log or an error | Logs record that a code was verified or rejected, with the repository and the comment, never the code or its distance from the comment's step; errors name neither secret nor code | `TestCommandsJobLogsEveryCodeWithoutItsOffsetAndNeverTheCode` (`internal/app/commands_test.go`); `TestErrorsDoNotRevealSecretOrCode` (`internal/domain/totp/totp_test.go`) | ASVS V7; ASVS V8 |
| E | An approval authorizes a different revision than the owner saw | Consent is bound to the exact revision, with eligibility checked against current facts; a superseded approval is rejected without redirecting: `internal/domain/contract/consent.go:136-166` | `TestConsentRejectsSupersededApprovalWithoutRedirecting`, `TestConsentEligibilityRequiresExactCurrentFacts` (`internal/domain/contract/consent_test.go`) | CICD-SEC-1 |
| E | A non-owner revokes, or poisons the owner's command order | Unauthorized revocation withdraws nothing and cannot reserve the owner's order | `TestConsentUnauthorizedRevocationCannotWithdrawOrPoisonOwnerOrdering`, `TestConsentUnauthorizedActorCannotReserveHumanOwnersOrder` (`internal/domain/contract/consent_test.go`) | ASVS V2 |

### B4: Host listeners

| STRIDE | Threat and abuse case | Control | Test | Ref |
| --- | --- | --- | --- | --- |
| S | A web page or local process calls `/mcp` | A request that carries an Origin header must carry a configured origin, else 403; the bearer token is compared in constant time: `internal/adapters/mcp/mcp.go:195-203`; the listener starts only with a token of at least 32 bytes: `internal/config/config.go:290-291`, `internal/adapters/mcp/mcp.go:131-133` | `TestOriginCheck`, `TestRequestsWithoutAValidTokenAreUnauthorized`, `TestTheTokenIsCheckedBeforeMethodAndPath` (`internal/adapters/mcp/mcp_test.go`); `TestLoadMCPToken` (`internal/config/mcp_test.go`) | ASVS V13; ASVS V2 |
| E | Listeners exposed to the network | Defaults bind loopback: `internal/config/config.go:124`, `:288`; the quadlet publishes only `127.0.0.1` ports: `deploy/quadlet/suiteward.container:19-21`; the database has no published port and only containers on the `suiteward` network reach it: `deploy/quadlet/suiteward-db.container:13` | `TestLoadAppliesDefaults` (status listener address; `internal/config/config_test.go`) | ASVS V14; CICD-SEC-7 |
| D | An oversized request | Request bodies are limited to 64 KiB: `internal/adapters/mcp/mcp.go:36`, `:206-210` | `TestBodyLimit` (`internal/adapters/mcp/mcp_test.go`) | ASVS V13 |
| D | A slow or stalled connection holds the listener | Both listeners set read-header, read, write and idle timeouts: `newHTTPServer` in `internal/app/serve.go` | `TestHTTPServersBoundEveryPhaseOfAConnection` (`internal/app/serve_http_test.go`) | ASVS V13 |
| I | The token or backend detail leaks through a response | Backend failures become fixed tool errors; the token is never logged or returned | `TestBackendFailuresAreFixedToolErrors`, `TestTokenIsNeverLoggedOrReturned` (`internal/adapters/mcp/mcp_test.go`) | ASVS V7; ASVS V8 |
| I | A secret is rendered by a log or a JSON encoder | Secret settings print as a fixed placeholder under every verb and encoder: `internal/config/config.go:36-49`; secrets are mounted as files, not environment variables: `deploy/quadlet/suiteward.container:22-28` | `TestLoadReportsAllInvalidValuesAndNeverTheSecret`, `TestLoadSecretFileProblemsNameTheVariableNotTheContent` (`internal/config/config_test.go`) | CICD-SEC-6; ASVS V14 |
| E | The container gains privileges | Read-only root filesystem, all capabilities dropped, no new privileges, non-root user 65532: `deploy/quadlet/suiteward.container:36-38`; `deploy/Containerfile:34` | The co-located flags match: `--read-only --cap-drop all --security-opt no-new-privileges` in `deploy/local.ps1:122`, `:138` | CICD-SEC-7 |
| E | A co-located process reads the key, the database or the secret | Reduced assurance, stated as published: SuiteWard reports the tier and does not claim the protection it cannot provide ([ADR 0027](../decisions/0027-agent-human-trust-separation.md), [install.md](../install.md) section 2) | Isolated tier verified on a remote host ([verification targets](../plan/README.md#verification-targets)) | Agentic identity and privilege abuse |

### B5: SuiteWard to PostgreSQL

| STRIDE | Threat and abuse case | Control | Test | Ref |
| --- | --- | --- | --- | --- |
| T | History rewritten: approvals, consumed steps, proposals | Update, delete and truncate triggers on the history tables reject mutation (`reject_immutable_history`): `internal/adapters/postgres/migrations/00012_owner.sql` and the governance migrations. The service connects as a role that owns nothing (`migrations.GrantApp`: `USAGE` on the schema, `SELECT, INSERT, UPDATE, DELETE` on tables, `USAGE, SELECT` on sequences, `SELECT` on `goose_db_version`), so it cannot disable or drop those triggers, truncate a table or set `session_replication_role`; only `suiteward migrate`, run as the schema owner with a separate connection, changes the schema. Prevented: the service's database credential rewriting history through those paths. Not prevented: that role can still insert into the history tables and change every other table, it can take table locks (an availability attack on the service), and a compromise of the host service account reads every secret on the host, including the owner connection the migration step uses ([persistence contract](../contracts/persistence.md#database-roles-and-grants-sh-h4)) | `TestGovernanceImmutableHistory` (`internal/adapters/postgres/schema_test.go`), as the owner; `TestOwnerHistoryTablesRejectMutation` (`internal/adapters/postgres/owner_test.go`) and the other history tests, whose UPDATE and DELETE run as the application role (only TRUNCATE runs as the owner); `TestTheApplicationRoleCannotDefeatTheImmutabilityTriggers`, `TestTheApplicationRoleCannotRewriteTheSchemaVersion` (`internal/adapters/postgres/roles_test.go`); the conformance, River and application integration suites run as the application role | ASVS V8; CICD-SEC-9 |
| T | A partial write leaves an approval without its record | Enrollment is one transaction | `TestOwnerEnrollWritesNothingWhenAStatementFails`, `TestOwnerConcurrentEnrollmentsSerialize` (`internal/adapters/postgres/owner_test.go`) | ASVS V13 |
| T | A canonical artifact altered on disk | Each canonical artifact is verified against its integrity record; a mismatch fails the check on every pull request of that repository and stops promotion ([install.md](../install.md) troubleshooting table) | `TestChecksFailEveryOpenPullWhenTheCanonicalFailedItsIntegrityAndReadNoTree` (`internal/app/checksjob_integrity_test.go`) | CICD-SEC-9 |
| I | SQL injection through repository text | Queries are typed and generated by sqlc (`sqlc.yaml`, `internal/adapters/postgres/internal/dbgen`; [ADR 0004](../decisions/0004-postgresql-pgx-sqlc.md)) | Fresh sqlc output is verified in CI ([ci-and-coverage.md](../ci-and-coverage.md)) | ASVS V5 |

### B6: CI and release to registry to host

| STRIDE | Threat and abuse case | Control | Test | Ref |
| --- | --- | --- | --- | --- |
| T | A pull request runs with secrets or write access | CI runs on `pull_request`, never `pull_request_target`; `contents: read`; checkouts use `persist-credentials: false`: `.github/workflows/ci.yml:3-10`, `:31` | `.github/workflows/ci.yml` itself; CI gate `CI / Gate` ([ci-and-coverage.md](../ci-and-coverage.md)) | CICD-SEC-4; CICD-SEC-5 |
| T | A tag moved under a third-party action | Every action is pinned by commit SHA; the release and deploy workflows use no third-party action and pin the BuildKit image by digest: `.github/workflows/release.yml:6`, `:23`; `.github/workflows/deploy.yml:3`, `:32` | `.github/workflows/ci.yml` | CICD-SEC-3; CICD-SEC-8 |
| E | An image reaches the host without the owner | The release and the deploy publish steps run in protected environments: `.github/workflows/release.yml:60`, `.github/workflows/deploy.yml:76`; the owner's approval moves the deploy tag only to the built digest: `.github/workflows/deploy.yml:85-88`; the host pulls the image itself and holds no deploy or registry credential ([verification targets](../plan/README.md#verification-targets)) | `deploy/smoke_test.sh` (self-test of the post-deploy smoke check) | CICD-SEC-1; CICD-SEC-5; CICD-SEC-6 |
| T | A known-vulnerable dependency ships | `govulncheck` runs on Linux CI, and a reachable finding fails the build: `.github/workflows/ci.yml:126-128` | CI gate | CICD-SEC-3 |

## 7. Out of scope

- The trusted host administrator: SuiteWard trusts the instance's authorized administrators and does not claim to defeat a malicious one with direct database, storage and credential control ([ADR 0007](../decisions/0007-canonical-contract-authority.md), line 47; [ADR 0012](../decisions/0012-mvp-authorization-policy.md), line 49).
- Denial of service by GitHub itself: an outage or a rate limit delays synchronization and checks; SuiteWard waits and resumes ([install.md](../install.md), status fields `waitUntil` and `lastError`).
- The assurance of the co-located tier beyond what [install.md](../install.md) section 2 states.

## 8. How this document is maintained

- A change to a trust boundary or a control updates this document in the same pull request.
- A `file:line` reference is checked when the cited file changes; a moved control keeps its row and gets its new reference.
