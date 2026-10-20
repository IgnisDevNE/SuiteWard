# Decision register

- **Updated:** 2026-10-14 (phase M1.6b: D-CHECKS amended with the state table, publication generation and failure rules)

These gates record product and implementation choices that accepted ADRs deliberately left open. They do not reopen accepted ADRs. The integrator can resolve implementation choices within accepted policy; changes to product authority, scope, or release commitments need the project owner. Do not choose an unresolved product policy to unblock a task; record the decision needed and take independent ready work.

## Accepted

### D-BOT: Authorized GitHub publication identity

Accepted 2026-09-30: `ignisdevne[bot]` (App 5028495, installation 163660443). Never publish with a personal account, and never fall back to personal credentials after a bot access denial. Tokens are restricted to SuiteWard and never appear in artifacts or conversation.

### D-RUNTIME: Runtime configuration and lifecycle

Accepted 2026-10-05. A single `suiteward serve` binary runs the API and the worker. Configuration comes from environment variables plus file-mounted secrets. Shutdown is graceful with a timeout. River retries are bounded and end in a visible terminal state.

### D-SYNC: Polling, budget and MCP boundary

Accepted 2026-10-05 as initial defaults; it no longer gates the runtime phase.

- Poll every 60 s with conditional (ETag) requests.
- Use at most 50% of the installation's API rate budget.
- Supported limit: at most 20 repositories.
- MCP is limited to sync and status, authenticated with a local token.

Final numbers come from the S1 walking skeleton.

**Amendment (2026-10-15, after M1.7; owner).** *Latency.*
- **Measured.** The M1.7 verification measured 20 s to 2 min from an event to its check. The M1.7b live run measured about 2 to 3 minutes from a command to its answer. After M1.7c (2026-10-17), on both targets, a command took 16–35 s to its answer, an approval 17–36 s to its check, and a push 12–31 s to its check. Before M1.7c the floor for a command was the D-COMMANDS watermark: the earliest synchronization success minus `JOB_TIMEOUT` minus 30 s, about 90 s. Since M1.7c it is the earliest cycle start in GitHub's clock minus 10 s (D-COMMANDS amendment of 2026-10-16).
- **Approved for phase M1.7c (before M1.8):**
  - chain the jobs, so an approve or revoke, a promotion, or a sync that sees new comments enqueues the next job in the same transaction (as decided then; M1.10 amended [ADR 0005](../decisions/0005-river-background-jobs.md): the outbox carries the transactional guarantee);
  - shorten the synchronization poll to about 20–30 s.
- **Needed first.** A D-COMMANDS decision on the watermark, for example a horizon measured from the cycle's start, because chaining alone cannot beat the watermark.
- **Webhooks** come after the MVP, because they need an inbound route and changes to ADR 0001 and D-SYNC.

**Amendment (2026-10-16, phase M1.7c; owner).** *Latency.*
- **Poll.** Synchronization runs every 20 s, paced by its cost.
  - A tick starts no sooner than 18 s after the previous one. The 2 s tolerance absorbs the pickup jitter on the 20 s periodic grid.
  - It also starts no sooner than the previous tick's request count allows at 450 requests a minute. That is half of GitHub's secondary limit of 900 points a minute; a REST GET costs one point (GitHub's REST rate-limit documentation, read 2026-10-16). For example, a tick of 150 requests waits 20 s and one of 420 requests waits 56 s.
  - A cycle costs one request per repository plus one per open PR.
  - The primary budget stays at 50%. A 304 is not counted against it, and the ETag cache of each read client resets only when its token rotates, about once an hour.
  - The limit stays at 20 repositories. At that limit with 20 open PRs each, a tick costs 420 requests, so pacing spaces ticks about 56 s apart. The secondary limit is never crossed.
- **Wake-ups.** A job that may unblock another inserts a one-off run of it **after its own commit**, and the periodic runs stay as the fallback. A wake-up lost to a crash only delays the work to the next periodic run. This revises "in the same transaction" in the amendment of 2026-10-15 (owner, 2026-10-16).
  - **Sync to commands, checks and merges.**
    - A sync tick that observed a change (any response that was not a 304) wakes `github_checks` and `github_merges`.
    - Any tick that recorded a cycle wakes `github_commands`, because a cycle that changed nothing can still move the horizon past a comment observed earlier (whole-phase review, 2026-10-16).
    - A `Date` more than 5 minutes ahead of the local clock fails the cycle. This is only a sanity bound, and the local clock never enters the horizon.
  - **Commands to checks and bootstrap.** A commands run that decided at least one candidate wakes `github_checks` and `github_bootstrap`.
  - **Promotion to checks.** A promotion, by the merges job or by the bootstrap job, wakes `github_checks`.
  - **Queues.** Each of these job kinds runs on its own queue with one worker, so runs never overlap. River requires `running` in every unique state set, so a wake-up cannot coalesce with a run in progress. It is therefore inserted without uniqueness, and one that arrives during a run queues exactly one follow-up.
- **Targets.** Measured from GitHub's own timestamps in the live run:
  - a command answered in under 45 s;
  - an approval reflected in the check in under 60 s;
  - a push reflected in the check in under 45 s.

### D-SCOPE: Declaration and source-inventory semantics

Accepted 2026-10-05.

- The declaration uses gitignore-style include and exclude rules, case-sensitive.
- A symlink or submodule inside the scope makes the declaration invalid; the governing scope stays in force.
  - **Amended 2026-10-17 (owner, M1.9 phase review).** Suggested patterns match at any depth, so this rule made every repository with a link or a submodule unprotectable. A typical case is `AGENTS.md -> CLAUDE.md`. The rule now depends on what the entry is:
    - **A symlink whose target resolves, within the same tree, to a regular file** behaves as a file. It is unsafe only when the scope covers the link's own path. The target is the link's blob, resolved relative to the link's directory, following at most 8 links.
    - **A symlink to a directory, to a path outside the repository, or to nothing** is unchanged: it is unsafe when the scope covers it or may cover a path below it.
    - **A submodule** is outside the scope. Its content is not protected and is never read. The declaration stays valid, and the replies that suggest a declaration warn about every submodule that a pattern would have reached.
    - The target is read only for a link that would otherwise be unsafe. It is resolved on the same tree that is being assessed, so it decides only whether that tree is safe and grants nothing.
  - **Amended 2026-10-19 (owner, M1.9 live run): an accepted link.** An `exclude` pattern naming exactly the link's own path, anchored (`/spec_link`) or with an interior slash, accepts that link. The link no longer makes the declaration invalid, and nothing reached through it is protected.
    - Because it is an exclude, it is a scope reduction and needs the owner's approval like any other.
    - Other excludes, such as a broader pattern or one that only matches below the link, still do not make a link safe.
    - The `invalid-source` comment for a link offers this choice with a warning, after removing the link or pointing it to a file. It does not suggest narrowing the include patterns, because that is a wider reduction the owner decides alone.
- Approval binds the raw declaration bytes plus the normalized rules.
- A candidate omission cannot remove existing protection; reducing the scope requires approval like any other change (ADR 0013).

### D-SCOPE-DEFAULTS: Which files the protected defaults cover

Accepted 2026-10-11 (owner, phase M1.3). The ADR 0013 amendment protects the workflows, the test runner configuration and the scripts that run tests; which runner files exist depends on the project's ecosystem, so SuiteWard does not guess them.

- `.suiteward.yml` is always in scope: no exclude removes it, and changing or deleting it never disables protection (ADR 0013).
- `.github/workflows/**` is a fixed default.
- The declaration has a `runner` list of patterns that the owner fills with the runner configuration and test scripts.
- Once canonical, removing `.github/workflows/**` or a `runner` entry from the scope, by exclusion or by deleting the entry, is a scope reduction and needs approval like any other change.
- The declaration is YAML, parsed with `go.yaml.in/yaml/v3`; its schema is fixed by the M1.3-A2 brief.
- An `exclude` pattern that matches `.suiteward.yml` has no effect on coverage but is still classified as a scope reduction (decided by the integrator at the owner's request, 2026-10-11): the rule-level check is syntactic, and such an edit changes a covered file, so it needs approval either way; an exact rule would need a special case that buys no safety.

### D-SCOPE-DISCOVERY: Suggesting and widening the scope

Accepted 2026-10-17 (owner, M1.9 planning, after the C2 finding in [M1.8](phases/M1.8.md#c2-outcome-dogfooding-2026-10-17)). Discovery stays a suggestion (ADR 0013): only the owner's approval changes the contract.

- **Every suggested change links to GitHub's editor** (owner, 2026-10-19). A comment that asks for a change to `.suiteward.yml` links to the editor for that file on the pull request's branch, or to GitHub's new-file page when the file does not exist yet. This holds even when the comment also carries a review suggestion or a copyable block. A change the web editor cannot make, such as removing a symbolic link, gets no link.

- **Detection is path-only.** The suggestion is built from the file paths of a tree, with no file contents.
  - **Test files.** Patterns follow the frameworks' own conventions and match at any depth (`*_test.go`, `test_*.py`, `*.spec.*`…). There is no longer one pattern per directory.
  - **Test directories.** `testdata/`, `__tests__/`, `__snapshots__/`, `__mocks__/`, `__fixtures__/` and `src/test/` count at any depth. `tests/`, `test/` and `spec/` count at the top level, or when they hold a test-named file.
  - **Runner files.** The runner list holds the files found by name, plus the higher-priority files that a runner reads first, even when they do not exist yet: pytest reads `pytest.toml` before `pytest.ini`. A suggested pattern may therefore match no file.
  - **Test directories, in full:** besides those above, `cypress/` and `src/it/` also count at any depth.
  - **Runner families.** The runner list holds whole families: a file of a family brings in every member. For example, `pytest.ini` brings `conftest.py`, `.coveragerc` brings `.nycrc*` and `.c8rc*`, and `.rspec` brings `spec_helper.rb`.
  - **Vendored directories** never count: `node_modules`, `vendor`, `.venv`, `venv`, `.tox`, `__pycache__`, `bower_components`, `jspm_packages`, `.git`.
  - **The limit** covers include and runner patterns together. When the limit cuts the list, the reply says so.
- **Mixed files.** `package.json`, `pyproject.toml`, `setup.cfg`, `tox.ini`, `Cargo.toml`, `*.csproj` and `CMakeLists.txt` can hold test settings among other content. The reply warns about the ones it finds and never suggests them, because protecting them would put every dependency update behind an approval.
- **The scope notice.** After V1, a PR revision that adds test-like files outside the governing scope gets one comment.
  - The comment lists those files and gives the full declaration to commit, with the edit link to `.suiteward.yml` on the PR branch.
  - When the PR's diff already touches `.suiteward.yml`, it also gets a review comment with a suggestion block, so the owner can use GitHub's "Commit suggestion".
  - The notice is evidence, not authority. It never changes the check, never protects a file and grants nothing.
- **`/suiteward suggest`** (owner only, no code) asks for the same reply on any open PR.
  - Before V1, the reply is the bootstrap reply of a `propose` that finds no declaration.
  - After V1, the reply is the scope notice for the PR head.
  - It is not a command for the check: it never clears `owner-command-rejected`, and a malformed or edited `suggest` never sets it (D-COMMANDS, "A wrong command has no effect").
- **Where a suggested declaration starts from.** The notice and `suggest` start from the **canonical** declaration bytes, never from the PR head's file, and only add patterns. A PR that narrowed the scope therefore never comes back as a suggestion.
  - **The one exception is the review suggestion.** It inserts the new lines into the PR head's own `.suiteward.yml`, on a list line inside a diff hunk, because GitHub accepts review comments only on such lines. This grants nothing either: a narrowing in the head is still listed as a reduction and still needs approval. When no list line falls inside a hunk, there is no review suggestion.
  - **The head's own scope only silences.** A test path that the PR's valid `.suiteward.yml` already covers is not reported. That only suppresses an advisory; the widening still needs approval.
- **Exclusions are respected.** A test-like path that matches an `exclude` of the governing scope is never reported, because adding patterns cannot undo an exclusion.
- **One notice per set.** The notice is keyed by the set of added uncovered paths, so later pushes that add nothing new do not repeat it.
- **Uncovered runner files block** (owner, 2026-10-18, M1.9 live run).
  - **The risk.** A runner file can change how the protected tests run without touching any of them. A root `conftest.py` can skip them, and a `jest.config.js` can narrow `testMatch`. So, unlike an uncovered test file, an uncovered runner file is a bypass, and a notice alone is not enough.
  - **The rule.** After V1, a PR that adds or modifies a file matching a runner family name, outside the governing scope and not excluded, gets the check `in_progress` with reason `runner-uncovered`. It is never `success` until the owner decides.
  - **Protecting the file.** The owner commits the suggested declaration, which adds the pattern, and approves it.
  - **Ignoring the file.** The owner adds the path to `exclude` and approves it. That is a scope reduction, which needs approval anyway, so the decision is recorded in the contract.
  - **What stays non-blocking.** Uncovered test files stay a notice only: they add no bypass of the existing protection, and their detection is heuristic. The runner family names are a short, exact list.
  - Rewriting the YAML can drop the owner's comments. Comments are part of the approved bytes, so the owner sees that change and approves it.
- **Widening is a protected change.** Any edit to `.suiteward.yml` creates a revision that needs approval, comments included. An agent widens the scope with such a PR; it never posts `suggest`.
- **A reduction is detailed.** The revision comment lists the rules and paths a reduction removes.
- **After the MVP:** reading the content of config files (pytest `testpaths`, Jest `testMatch`, Vitest `include`, `phpunit.xml`, `Cargo.toml` `[[test]]`); protecting a single section of a mixed file; listing complete trees for monorepos.

### D-GITHUB-ACCESS: GitHub App permissions and token lifecycle

Accepted 2026-10-11 (owner, from the S1 findings in [s1-github](../spikes/s1-github.md)).

- The App holds Metadata read, Pull requests read and write (write measured 2026-10-13, M1.6a-P0), Contents read, Administration read (since 2026-10-13, for protection verification, D-PROTECTION) and Checks write; no Administration write, no Issues, no webhooks. Reading PR comments needs Pull requests read (Issues read alone is refused).
- Installation tokens are requested with the installed repositories and an explicit, non-empty permission map (an empty map grants everything the App has), the narrowest one per use: synchronization is read-only and requests neither Checks write nor Administration; protection verification requests only Metadata and Administration read.
- A token lives one hour and is reused until five minutes before it expires.
- Acknowledgement comments use Pull requests write, requested only by the comment publisher with `{metadata: read, pull_requests: write}`. M1.6a-P0 measured on 2026-10-13 that a token requesting `pull_requests: read` from the App that holds write is granted exactly read, so synchronization stays read-only, and that the same comment POST with the read map answers 403.
- The GitHub adapter contract is [github](../contracts/github.md).
- **No Contents write, reaffirmed 2026-10-19 (owner, M1.9).** A one-click choice that commits for the owner was declined, because security and isolation are the product's core and Contents write would add a way to fail:
  - it would let the key inject commits into PR branches, run existing workflows on new branches, and create tags;
  - in the managed service, one key would write to every customer;
  - in co-located, the agent reaches the key, so it would gain a second write identity.

  The owner's choices stay copyable blocks, review suggestions (committed with the owner's own credentials), or a change the owner asks the agent to make. Checks write remains only because it is required.

### D-COMMENT-EDIT: One comment is one action

Accepted 2026-10-11 (owner). A command comment is one action: SuiteWard processes the first observed version of each comment id, once. Editing that comment afterwards, or deleting it, does not change the recorded outcome: it neither revokes nor re-processes nor creates another action. Any further action (a revocation, a new approval) is a new comment. S1 observed both edits and deletions ([s1-github](../spikes/s1-github.md), "Comment edit and delete"); M1.4 keeps every comment version and marks deletions, so the history stays visible. M1.6-A implements the rule.

### D-APPROVAL-CONTEXT: Approval binding for implementation-only changes

Accepted 2026-10-05. Consent covers the protected inventory digests, the declaration, the governing policy, and the canonical baseline. An implementation-only push keeps consent but triggers a fresh integrity assessment against the exact new source. A change to any covered input creates a new proposal revision that needs fresh approval.

### D-TRUST: Agent/human trust separation

Accepted 2026-10-05: [ADR 0027](../decisions/0027-agent-human-trust-separation.md). TOTP code on every approve and revoke command, isolated and co-located installation tiers, agents use their own GitHub identity.

### D-IDENTITY: Owner setup and identity

Accepted 2026-10-13 (owner). The owner is bound by a local setup command run on the instance itself (`suiteward setup`, through `podman exec` on the co-located target or the owner's own Session Manager session on the isolated one); no HTTP or MCP surface can set up or change the owner. Setup takes the owner's GitHub login, resolves its numeric account id through the App's installation token, and records the owner as the principal `github:<numeric id>` of kind `Human` in a new policy revision; the login is display only. Proof of identity is access to the instance, which on the isolated tier only the owner has; on the co-located tier the agent can run setup too, which is part of the reduced assurance SuiteWard reports. The trusted installation is the operator-configured `GITHUB_INSTALLATION_ID`; setup confirms it covers every configured repository. Setup also enrolls the TOTP secret (ADR 0027), records the name of the project's required test check, and records the tier the instance had at enrollment: a secret enrolled on a `co-located` instance stays within the agent's reach even if the instance is later declared `isolated`, so the effective tier is `co-located` when either the setting or the enrollment says so, and `/status` says to re-run setup when they differ. Re-running setup replaces the TOTP secret and creates a new policy revision. One owner governs the whole instance (owner answer 2026-10-13): each configured repository is one SuiteWard project, identified by the repository's numeric GitHub id (`github-repo:<id>`, stable across renames), with its own policy owned by that owner and its own required test check (`--test-check owner/name=<check>`). An approval binding includes the policy revision (`contract.NewApprovalBinding`). In M1.5 no Suite exists yet, and setup refuses to change the owner of a project that already has a Suite; Changing the owner of a project that already has a Suite stays refused in the MVP (D-COMMANDS, 2026-10-13), so a changed owner never inherits earlier authority or approvals; re-enrolling the same owner is allowed.

### D-TIER: Installation tier

Accepted 2026-10-13 (owner). The tier is declared by the deployment, not detected: the setting `INSTALLATION_TIER` is `isolated` or `co-located`, default `co-located` (never claim more assurance than configured). The remote quadlet sets `isolated`, `deploy/local.ps1` sets `co-located`. The tier is shown in `/status` and MCP `status`, recorded at setup, and stated in the check summary from M1.6.

### D-PROTECTION: Protection verification

Accepted 2026-10-13 (owner), from the M1.5-P0 measurement on the S1 test repositories (recorded in the [M1.5 page](phases/M1.5.md)). SuiteWard verifies and reports; it never changes protection (ADR 0016 amendment). It reads the effective rules of the default branch (`GET /repos/{r}/rules/branches/{branch}`, Metadata read; GitHub returns only the rules of active rulesets, at repository and organization level, so `evaluate` and `disabled` rulesets never count) and the classic branch protection (`GET /repos/{r}/branches/{branch}/protection`, Administration read: 200 configured, 404 none, 403 unreadable), with a token that requests only Metadata and Administration read. The result is a set of findings evaluated over both sources together (a requirement met by either counts):

- `missing-contract-check`: no required check `SuiteWard / Contract` pinned to the App (ruleset `integration_id` or classic `app_id` equal to the App id; absent or `-1` is not pinned);
- `missing-test-check`: the repository's configured test check is not required (before setup there is no configured test check, so this finding also says to run setup);
- `not-up-to-date`: no source that requires the `SuiteWard / Contract` check also requires branches to be up to date (D-CHECKS: the required-check rule must be strict); when no source requires the contract check, `missing-contract-check` covers it and this finding is not raised;
- `admin-bypass`: classic protection does not enforce for administrators (`enforce_admins` false).

`enforced` means no finding. Two results are exclusive instead: `enforcement-unavailable` (a private repository whose effective rules answer 403: GitHub Free does not enforce there, or the read permission is missing; the instruction names both and SuiteWard never claims protection) and `unknown` (any other read failure, including a token whose granted permissions differ, with the instruction to grant Administration read). A ruleset's bypass list is not visible to the App, with or without Administration read (measured: an existing "Repository admin" bypass answered 0 bypass actors; GitHub returns `bypass_actors` only to a caller with write access to the ruleset, which the App does not request), so every result that relies on a ruleset carries the caveat `ruleset-bypass-unverified` with the manual check that the bypass list is empty. Verification runs at setup and every 15 minutes inside the D-SYNC budget, and `/status` shows the latest result per repository.

### D-COMMANDS: PR commands, identifiers and bootstrap

Accepted 2026-10-13 (owner answers, M1.6a planning), within ADRs 0002, 0010, 0011, 0015 and 0027.

**Who is heard**
- Only the enrolled owner's comments are commands.
- A `/suiteward` comment from any other account is recorded in the command ledger and otherwise ignored: no acknowledgment, no state change, no check effect. This is the ADR 0011 amendment of 2026-10-13.
- SuiteWard's own comments are recognized by the App bot's numeric account id and never parsed.

**A wrong command has no effect** (owner, 2026-10-19, after the M1.9 review). The model is the usual one: a typo or an accidental Enter changes nothing.
- A command that is not recognized, is malformed, or is rejected for any reason gets its acknowledgment, which says it was not accepted, and changes nothing else. The check of the pull request does not change, whatever the verb (`approve`, `revoke`, `propose`, `skip`, `suggest`).
- **The one exception is a wrong confirmation code**: `invalid-code` or `replayed-code`, on any command that carries a code. Only that sets the check `failure` / `owner-command-rejected`, because a failed code is a security signal. A replay means someone reused a code from a public comment.
- This supersedes the earlier rule, where every rejected disposition, or a processed command with a rejected consent result, set `owner-command-rejected`, and the carrier limit for shared rejections.

**Command syntax**
- `/suiteward approve <ref> <code>` and `/suiteward revoke <ref> <code>` (ADR 0027).
- The first line of the comment body that starts with `/suiteward`, outside fenced code blocks, is the command; the rest of the body is ignored.
  - **Amended 2026-10-18 (owner, M1.9 live run).** The prefix and the verb are matched without regard to case, so `/SuiteWard suggest` or `/Suiteward Approve P1-R1 123456` from a phone's auto-capitalization is a command. A missing argument, an extra token or a malformed reference or code is still refused as malformed. The reference stays `P<n>-R<m>` as written.
  - **Amended 2026-10-20 (owner, security review).** A command is read only where GitHub shows it as text. A line inside an HTML comment (from a line that contains `<!--` through the next line that contains `-->`) and a line indented four or more columns (a tab counts as four, measured before trimming) are not commands, as a fenced line is not. It fails closed: an unclosed `<!--` hides the rest of the body, and a `<!-- -->` on a command line hides that line, so such a command is silently `not-command`. Fences keep working as before.
- `<ref>` is `P<n>-R<m>` (ADR 0002 and ADR 0010 examples), resolved inside the PR's project and its single Suite. `<code>` is the 6-digit TOTP code.
- `/suiteward suggest` takes no argument and no code. It asks for a suggested declaration ([D-SCOPE-DISCOVERY](#d-scope-discovery-suggesting-and-widening-the-scope)), grants nothing, and is not counted as the owner's latest command.

**TOTP**
- The code is verified against the comment's `created_at` with a tolerance of one 30-second step either side (owner answer 2026-10-13).
  - **Data for the decision before M1.8.** In the M1.7b live run (2026-10-16), one approval code was refused because of timing, per the owner. The offset was not captured.
  - The commands job logs, for every rejected code, whether a window of 2 steps would have matched and at which offset (`github command code rejected`, `would_match`). The decision reads those lines from the host journal.
  - **Decided 2026-10-17 (owner, M1.8 planning): keep ±1.** The M1.8 acceptance reads the host's `would_match` lines. Only near misses there widen the window to ±2, as a new owner decision.
  - **Confirmed by the M1.8 acceptance (2026-10-17): the window stays ±1.**
    - The host's journal held 20 rejected codes from 2026-10-15 to 2026-10-17, and every one had `would_match` false. These were wrong codes, not late ones: dead authenticator entries and one mistyped code.
    - No near miss was seen.
- A step is consumed once per owner across all commands (ADR 0027). The consumed step records the command that used it, so a retry of the same command after a crash is not a replay.

**Order and binding of a code to its command (ADR 0027)**
- Commands are processed one at a time across all repositories, ordered by the comment's `created_at` and then its comment id.
- The command order given to the domain is the GitHub comment id.
- **Sync watermark.** A comment is processed only once every enrolled repository has completed a successful synchronization cycle that began after the comment was created. "Began after" was approximated as finished at least `JOB_TIMEOUT` plus 30 s of clock skew after `created_at`. Since M1.7c it is measured in GitHub's clock (amendment of 2026-10-16 below), and `JOB_TIMEOUT` no longer delays commands.
  - Without the watermark, an agent using the owner's GitHub identity could copy the owner's code into a command on another repository, sync that repository first, and use the code before the owner's own command.
  - Cost: a repository that cannot synchronize delays every command. `/status` shows the reason.
- **Edited before observed.** A comment whose first observed version was already edited (`updated_at` differs from `created_at`) is not a command. It uses up no step and is acknowledged with the reason.
  - Without this rule, an innocuous comment edited after the owner's code appeared would carry an earlier `created_at` than the owner's command.
- **Result.** With these two rules, the first command that can use a code is the one the owner wrote. That is what ADR 0027 means by a code "bound to the command it authorizes".

**One comment is one action**
- D-COMMENT-EDIT applies: only the first observed version of a comment id is processed, once.

**Identifiers**

| Value | Form |
| --- | --- |
| Project | `github-repo:<repository id>` (D-IDENTITY) |
| Suite | `default`; one per project in the MVP |
| Integration target | `branch:<default branch>` |
| Carrier | `github-pr:<repository id>/<number>` |
| Proposal | `P<n>`, numbered per project from 1 |
| Revision | `R<m>`, numbered per proposal from 1 |
| Version | `V<n>`, numbered per Suite from 1 |
| Source command | `github-comment:<comment id>` |
| Consent operation | `consent:github-comment:<comment id>` |
| Bootstrap operation | `bootstrap:<repository id>:P<n>-R<m>` (since M1.7; `bootstrap:P<n>-R<m>` before) |

The assessment emitter is the service principal `suiteward` (kind Service).

**Bootstrap of an existing repository (ADR 0010)**
1. When a configured repository has an enrolled owner and no Suite, SuiteWard creates its Suite with no canonical, governed by the owner's current policy revision.
2. When that repository has an open PR, SuiteWard:
   - pins the default branch head as the origin;
   - proposes its protected inventory as `P1-R1`, carried by the lowest-numbered open PR;
   - proposes nothing while that inventory is empty: the project keeps awaiting its initial contract and never gets an empty baseline (ADR 0010). ADR 0010's other path, bootstrapping from the first PR that carries tests with a readiness check, is not in M1.6a; the owner moved it into the MVP as phase M1.7b, after M1.7 and before M1.8 (2026-10-14);
   - posts the bootstrap summary in that PR, with the copyable `/suiteward approve P1-R1 <code>`.
3. After the owner's valid approval, SuiteWard re-reads the origin tree, records the integrity assessment for the origin, and bootstraps the first canonical `V1` without waiting for the PR to merge.
4. If the default branch has moved meanwhile, the origin stays pinned. It is not silently moved (ADR 0010).

**One instance per repository**
- Two SuiteWard instances must never watch the same repository.
- Both post as the same App, and each has its own TOTP secret. So one would reject the owner's codes and, from M1.6b, publish `failure`.
- HOST.md says so. A target that is not under verification runs without GitHub settings, or with other repositories.
- The remote host (isolated) watches the test repositories by default (owner answer, 2026-10-13). The co-located stack runs with GitHub only during its own verification.

**Owner change**
- Changing the owner of a project that already has a Suite stays refused in the MVP, as `setup` does since M1.5.
- Re-enrolling the same owner (TOTP rotation) is allowed.
- This amends D-IDENTITY's M1.6 note.

**Amendment (2026-10-16, phase M1.7b rework; owner).** Two commands are added. Both are owner-only, and any other account is ignored as before.
- **`/suiteward propose`.** No reference and no code: it only creates a proposal, which grants nothing.
- **`/suiteward skip <code>`.** No reference, and it carries a TOTP code verified and consumed like `approve`. It lets a PR pass the check before the first contract.

Their dispositions are `proposed`, `propose-refused`, `skipped` and `skip-refused`. They follow the existing serialized order and retry rules. A propose whose GitHub read fails is retried, and recorded `failed` after 5 attempts, like any other candidate. A source that cannot be read as a contract (an invalid declaration, a link, a truncated tree) is deterministic: it is refused at once with reason `invalid-source`, so it never stalls the queue (clarified 2026-10-16). The rules are in D-CHECKS (amendment of 2026-10-16).

**Amendment (2026-10-16, phase M1.7c; owner).** *The sync watermark in GitHub's clock.*
- **The rule is unchanged.** A comment is processed only once every enrolled repository has completed a successful synchronization cycle that began after the comment was created.
- **What changes is how "began after" is measured.**
  - A cycle's start is the HTTP `Date` of that repository's first response in the cycle, the first page of its open pull requests. This is GitHub's own clock, the same one as the comment's `created_at`, so no local clock skew enters.
  - The horizon is the earliest such start among the enrolled repositories, minus a margin of **10 s**. The margin covers GitHub's listing lag and the one-second resolution of `Date`.
  - `JOB_TIMEOUT` and the 30 s skew no longer enter the horizon.
- **The margin is a named constant and has not been measured.** The sync logs the listing lag it can see: a cycle that started after a comment was created but did not list it (`missed_by_ms`, runtime contract "Latency (M1.7c)").
  - This is a detector, not a proof. It sees lag only when a cycle happens to start inside the lag window. The bot's acknowledgments give many samples.
  - The margin is raised if any value comes near 10 s.
- **Fail closed.** A repository that has no recorded start holds the horizon back, as a repository with no successful cycle does today. This is the case before its first cycle under M1.7c. A `Date` that is missing or cannot be parsed fails that cycle; it never falls back to the local clock.

### D-CHECKS: The `SuiteWard / Contract` check

Accepted 2026-10-13 (owner), from the S1 proposals ([s1-github](../spikes/s1-github.md)): one check run named `SuiteWard / Contract` per PR head SHA, published by the App and required with the App's `integration_id` as its source; never `neutral` (it passes a required check); `in_progress` while a contract change awaits approval; one publication per (head SHA, state); only an invalid command authored by the owner sets `failure`; a command from any other account is ignored (D-COMMANDS); the required-check rule must require branches to be up to date. M1.5 verifies the protection side; M1.6 publishes.

**Amendment (2026-10-14, phase M1.6b; owner answers).**

*States.* For each open PR head of a repository with an enrolled owner:

| State | When |
| --- | --- |
| `success` | the head's protected inventory under the canonical scope equals the canonical (an implementation-only change), or the PR's current proposal revision is ready for integration (`CheckPromotionReadiness` answers ready: exact revision approved under the governing policy, against the current canonical, with a passed integrity assessment of this head) |
| `in_progress` | approval missing (including a revision rebound after the canonical or the policy changed); the head cannot be read yet (a transient GitHub or tree failure); the repository has no canonical yet (bootstrap pending, including a default branch without protected files) |
| `failure` | an invalid command authored by the owner on this PR (superseded 2026-10-19 by D-COMMANDS, "A wrong command has no effect": only a wrong or reused code); an invalid declaration or source (an invalid `.suiteward.yml`, a symlink or submodule in scope); a confirmed integrity failure |

`neutral` and `skipped` are never published. Comments by other accounts never affect the check.

*Owner-command failure.* It applies to the PR's current head when the owner's latest command on that PR was rejected (malformed, invalid code, replayed code, unknown proposal, edited before read, or a rejected consent result). It lasts until the next state change for that PR: a later processed command or a new head. (Superseded 2026-10-19 by D-COMMANDS, "A wrong command has no effect": only a wrong or reused code sets it.)

*No enrolled owner.* Nothing is published; the missing required check already blocks merges, and `/status` says why.

*Publication.*
- One check run per (PR, generation). The generation is a per-PR counter that advances when the head or the computed state changes. This replaces "one publication per (head SHA, state)".
- POST only: the latest run with the name wins on GitHub. PATCH is not used.
- A single writer, the `github_checks` job, decides states and enqueues publications. Approve, revoke and promotion are picked up on its next run (every 30 s).
- The publisher posts a message only while its generation is the PR's current one, so a delayed message never overrides a newer state.
- The summary names the state, the proposal revision when there is one, the installation tier and the next action (the copyable approval command when approval is missing); it never claims that tests ran, and it stays below GitHub's output limit.

*No change proposal before a canonical exists.* A PR proposal is created only for a repository that has a canonical, so a PR head can never become canonical through the existing-baseline bootstrap.

**Amendment (2026-10-15, phase M1.7b; owner answers).**

*The bootstrap proposal from a PR.* While the Suite has no canonical and the default branch has no protected files, one PR carries a bootstrap proposal from its exact head: the lowest-numbered open non-fork PR whose head has protected files (ADR 0010, amendment of 2026-10-15).
- It is the only proposal allowed before a canonical. It is a baseline proposal, not a change proposal: its revisions expect no canonical and carry the origin kind `pull`.
- A PR head can become V1 only through a merge. The domain refuses an existing-baseline promotion of a `pull` revision.

*`bootstrap-ready`.* That PR's head gets `success`, titled "Bootstrap ready for integration", when the owner approved its exact current revision and the integrity assessment of that head passed. It says no canonical exists yet. Until then it is `in_progress`, `approval-missing`, with the reference. Every other PR stays `bootstrap-pending`.

*Precedence* (superseded by the amendment of 2026-10-16: the newest proposal wins, and nothing is proposed automatically). Once the default branch has protected files, the existing-baseline path wins: a pending PR-head proposal is superseded, and V1 comes from the pinned default-branch commit. This avoids drift between the canonical and what the branch holds (owner: "the option that avoids conflicts or drift that could be irrecoverable").

**Amendment (2026-10-15, after M1.7; owner).** *One comment per new revision.*
- **When.** When a PR gets a new revision that needs approval, SuiteWard posts one comment with:
  - the protected files added, changed and removed against the canonical, bounded;
  - the base version;
  - the copyable `/suiteward approve P<n>-R<m> <code>`;
  - a line saying the previous revision is superseded.
- **Only then.** PRs with no protected change, and implementation-only pushes, get no comment.
- **Never edited.** Each revision gets a new comment. The comment is evidence, not authority: approving still needs a TOTP code for the exact revision.
- **Schedule.** Phase M1.7b.

**Amendment (2026-10-16, phase M1.7b rework; owner answers).** The first live run showed the automatic bootstrap was unsafe. A first PR without `.suiteward.yml` was proposed with only `.github/workflows/**` protected, so a V1 would have silently left the PR's tests out. The bootstrap is now explicit.

*Before V1.*
- **Default state.** Every open non-fork PR gets `in_progress` with reason `bootstrap-awaiting`, and one comment saying how to start.
- **No automatic proposal.** The automatic carrier choice and the automatic baseline proposal are removed.
- **The owner's two commands decide.**
  - **`/suiteward propose`** (owner only, no code). It creates the bootstrap proposal carried by this PR. The source depends on where `.suiteward.yml` is:
    - on the default branch: a baseline pinned to its head;
    - otherwise in the PR head: a baseline from that head, which becomes V1 only from its exact merge commit;
    - otherwise: no proposal. The reply gives a suggested `.suiteward.yml` built from the source's test-like paths, and a one-click link that opens GitHub's editor on the PR branch with the file filled in.

    The newest proposal of the Suite supersedes earlier unpromoted ones. A protected push after the proposal cancels it: there is no automatic new revision, and the owner proposes again.
  - **`/suiteward skip <code>`** (owner only, with a TOTP code like approve). The PR's check becomes `success` with reason `bootstrap-skipped`, so an infrastructure PR can merge before the first contract.
    - This is not a contract assessment. The summary says so.
    - It applies to the whole PR until V1 exists, and is refused once V1 exists.
- **Order of states for a PR before V1:**
  1. fork: `fork-unsupported`;
  2. the carrier of the Suite's latest bootstrap proposal: approval missing, `bootstrap-ready`, or `bootstrap-awaiting` after a cancel;
  3. skipped: `bootstrap-skipped`;
  4. otherwise: `bootstrap-awaiting`.

  Skip and propose on a fork are refused. Skip on the carrier is refused. Propose on a skipped PR makes it the carrier.
- **Guidance is not a failure.** A refused propose or skip is guidance and never sets the owner-command `failure`.

*SuiteWard stays read-only on code* (D-GITHUB-ACCESS). It never commits the declaration. The link only pre-fills GitHub's editor, and the commit is the owner's.

### D-INTEGRATION: Merge methods and the integrated source

Accepted 2026-10-15 (owner, phase M1.7), from the S1 proposals ([s1-github](../spikes/s1-github.md#merge-methods-and-integrated-source)):

- **Supported methods.** Merge, squash and rebase are supported. They are not told apart.
- **The integrated source.** For each method it is the merged PR's `merge_commit_sha`. The protected inventory is recomputed from that commit's tree and compared with the approved revision, never with the PR head or a moving branch tip.
  - A mismatch keeps the canonical and is reported in the PR.
  - A rebase onto a moved base that brings in another change is such a mismatch.
- **Same SHA.** A merged PR whose `merge_commit_sha` equals that of another merged PR of the repository is not promoted, and the PR says so. An outcome already recorded for the other PR stands.
- **Unsupported or unreadable integration.** A merged PR whose merge commit is still absent or unreadable after a bound gets an explicit state in the PR and no promotion.
- **Forks.** Fork PRs are not supported in the MVP, which fails closed on them. A PR whose head repository differs from the base repository, or has been deleted:
  - gets no proposal;
  - keeps its check `in_progress` with an explanation;
  - is never promoted, and its merge is reported.
- **Missed merges.** Besides the open listing, each repository's recently closed PRs are listed, so a PR opened and merged between two polls is still seen. A merge of the target branch is reported even when SuiteWard never saw the PR open.
- **No merge without an outcome.** Every merge into the configured target of a repository with a canonical ends in one recorded outcome:
  - promoted;
  - corrected;
  - no protected change;
  - blocked, with its reason.

  A blocked merge leaves the canonical unchanged. Recovery is a later PR, which either restores the canonical content or carries the integrated content for a new exact approval (ADR 0018). There is no reset command.

### D-RELEASE: Platforms, versions, license and notices

Accepted 2026-10-17 (owner answers, M1.8 planning), within [ADR 0009](../decisions/0009-agpl-3.0-distribution.md).

- **Platforms.** The MVP ships only the container image `ghcr.io/ignisdevne/suiteward` for `linux/amd64` and `linux/arm64`. A native binary comes after the MVP.
- **Versions.**
  - A release is a `vX.Y.Z` git tag on a commit of `main`, and the release workflow checks that the commit is an ancestor of `main`. A pre-release `vX.Y.Z-rc.N` may sit on a phase branch.
  - The workflow publishes the image tag `vX.Y.Z`. A release that is not a pre-release also moves `latest-stable`; a pre-release never moves it.
  - `sha-<commit>` and `deploy` stay our internal build and promotion tags.
  - **Publishing needs the owner's approval in a protected `release` environment**, as `remote-poc` does for promotion. The bot could push a tag by itself: it has Contents write, and it is the governed party once SuiteWard governs its own tests. So the tag alone is not the authorization.
  - The bot pushes a tag only with the owner's authorization quoting the commit.
  - **Amended 2026-10-17 (owner): only the owner creates release tags.**
    - The workflow that runs on a tag is the one in the tagged commit, so a tag pushed by the bot could carry a `release.yml` without the environment gate.
    - The repository therefore has a tag ruleset, `release-tags-owner-only`, on `v*`. It restricts creating, updating and deleting those tags to repository admins, and the bot has no bypass.
    - The owner creates each release tag on GitHub's Releases page, on the commit the orchestrator names.
    - `v0.1.0-rc.1` was the last tag pushed by the bot, before the ruleset existed.
    - **Still open: this closes the git-tag path only, not the image tags.**
      - The package's Actions access gives the SuiteWard repository the Admin role (checked 2026-10-17).
      - So any workflow on any branch the bot pushes can declare `packages: write` and push `:vX.Y.Z`, `:latest-stable` or `:deploy` with `GITHUB_TOKEN`, without the `release` environment or the `verify` job.
      - **Decided 2026-10-17 (the owner asked for the market standard).** For the MVP, keep publishing with `GITHUB_TOKEN` inside the protected environments, and pin by digest wherever it matters.
        - The install guide offers `Image=…@sha256:<digest>`.
        - The governing host of C2 runs `v0.1.0` pinned by its digest.
      - **The real closure is signing, post-MVP (D-HARDENING-CONTROLS).**
        - The release job signs the image (cosign keyless or GitHub's provenance attestation).
        - Users verify the signature against the `release.yml` workflow on a `refs/tags/v*` ref, so an image pushed by any other workflow or ref fails verification. This needs no extra secret.
- **License.**
  - `AGPL-3.0-only`.
  - The repository carries the official text in `LICENSE`, and the README carries the notice.
  - No per-file headers.
- **Third-party notices.** The image build gathers the license file of every module linked into the binary into `/licenses/THIRD_PARTY_NOTICES`. A module without a license file fails the build.
- **Corresponding source (AGPL section 13).** `/status` reports `source`: the URL of the exact revision the image was built from. The install guide says where the source is. A user who modifies SuiteWard must point `source` at their own modified source.

### D-INSTALL: End-user install, update, backup and dogfooding

Accepted 2026-10-17 (owner answers, M1.8 planning).

- **The end user never needs our dev loop.** They install from the guide, create the GitHub App, run `setup` and create the ruleset once. After that they only approve, with `/suiteward approve`, the PRs that touch protected files. Phase branches, the `remote-poc` promotion and smoke tests are ours alone.
- **The MVP is self-hosted; the managed service comes after it** (owner, 2026-10-17).
  - The owner's target for the end user is the managed service. There the user installs our App, enrolls TOTP and points it at a repository, and all host wiring stays ours.
  - It needs what the self-hosted design does not have: a public App, several owners per instance (D-IDENTITY is one owner), enrollment through a web page with a GitHub login instead of `setup`, and a trust model in which we are the host administrator (ADR 0027). It is recorded as D-MANAGED-SERVICE in Post-MVP.
  - For the MVP, the install guide leads with the co-located path, one command on the user's machine. The isolated host is the operator path: ours, and for whoever self-hosts on a separate machine.
- **Ruleset.** The user creates it, guided by a direct link to the repository's rulesets page and the exact boxes to tick. The App keeps no Administration write, so it can never change its own protection.
- **GitHub App.** The user creates it by hand, from a checklist in the install guide that lists the exact permissions of [D-GITHUB-ACCESS](#d-github-access-github-app-permissions-and-token-lifecycle). The manifest flow is backlog.
- **Updates.** The user pins `vX.Y.Z` and updates when they choose (`podman pull`, then restart). Podman auto-update on `latest-stable` is documented as an opt-in.
- **Backup.** The MVP backup is a plain `pg_dump` plus a tarball of the artifact volume, kept by the user. The runbook warns that it holds the TOTP secret and the enrollment. Encrypted backups and guided restore stay post-MVP (D-ARCHIVE, D-RESTORE).
- **Dogfooding.**
  - SuiteWard governs its own tests on the isolated host, with `CI / Gate` as the test check (the name of the required check of `ci.yml`), after the acceptance run on a test repository.
  - The agent's bot (`ignisdevne`) is then the governed party. Its installation must have neither Checks write nor Administration write.
  - The ruleset requires `SuiteWard / Contract` from the host's runtime App only.
  - A PR that touches protected files then also needs the owner's approval of its current revision.
  - **The governing host follows release tags** (2026-10-17; the owner asked for the best practice).
    - It runs a pinned `vX.Y.Z` and is updated by hand, exactly as the install guide says, never by auto-update from `:deploy`.
    - The reason: an instance that auto-deploys what is merged into the repository it governs would let a merged change replace its own governor.
    - Deployment from `main` (D-DEPLOY) still builds and promotes `:deploy` for verification, but the governing host does not follow it.
  - **What C2 set up** (owner, 2026-10-17):
    - **The host** runs `v0.1.0` pinned by digest, with `SUITEWARD_JOB_TIMEOUT=3m`.
    - **The scope** covers tests and runner. It protects the Go tests and testdata, the script tests, the CI scripts (`scripts/ci.ps1`, `scripts/check-*.ps1`, `scripts/dev-env.ps1`), `.golangci.yml` and `codecov.yml`, so that a PR cannot weaken `CI / Gate` or `codecov/patch` without an approval.
    - **The protection.** `main` keeps its classic protection (`CI / Gate`, `codecov/patch`, up to date, administrators included) and gains the ruleset `suiteward-contract`: `SuiteWard / Contract` pinned to the host's App, `CI / Gate`, up to date, and an empty bypass list. Classic protection only offers checks that ran in the last week, so a check that has never run can only be required through a ruleset.
  - **The isolation gap** (owner, 2026-10-17). This session could reach the host App's private key, which was stored on the owner's PC, and the host itself, through the owner's AWS terminal. Until the owner closes both, SuiteWard's `isolated` tier is not assured for this repository. To close them, without an agent session running, the owner:
    1. rotates the App key and installs the new one on the host;
    2. deletes the PC copy and revokes the old key;
    3. stops leaving an AWS session open where an agent can reach it.
  - **Closed** (owner, 2026-10-17). The owner rotated the App key from terminals outside any agent session, revoked the old key and deleted the PC copies, then logged out of AWS. The `isolated` tier holds again for this repository, as long as the host, its App key and AWS access stay out of agent sessions (orchestration.md).

### D-MIGRATIONS: Migration tooling

Superseded by R1. Goose, pgx and sqlc stay ([ADR 0026](../decisions/0026-versioned-postgresql-migrations.md)); the migrations were reset to a single normalized `00001` (River and the outbox were added in M1.2) described in the [persistence contract](../contracts/persistence.md).

### D-M0-COVERAGE: Coverage policy

Updated by R1. Patch coverage of 90% is required; project coverage is informational. The earlier 99% project floor and non-regression rule are removed. See the [CI guide](../ci-and-coverage.md).

## Open

- **D-DEPLOY:** remote verification host. Partly settled 2026-10-06: AWS EC2 in us-east-2 (cheapest region; close to the GitHub API), `t4g.small` Graviton (2 vCPU, 2 GiB; arm64, so images are multi-arch amd64+arm64), Ubuntu 24.04, 20 GiB encrypted gp3, 2 GiB swap, rootless Podman under an unprivileged `suiteward` user, `cloudflared` installed but not yet configured. No inbound ports and no SSH daemon; administration only through SSM Session Manager (instance role limited to `AmazonSSMManagedInstanceCore`; IMDSv2 is required; the hop limit of 1 does not stop a rootless container, see the IMDS mitigation below). Service access through Cloudflare Tunnel and Cloudflare Access on the owner's domain (service token for MCP sync/status). Deployment is pull-based: CI publishes a multi-arch image to GHCR, promotion to the deploy tag requires the owner's approval in a protected environment, and the host's Podman quadlet uses `AutoUpdate=registry`. Scale-out path: same image on ECS, PostgreSQL to RDS, artifacts to EFS or the S3 adapter. Done 2026-10-06: host `i-0f556162ae6b59b22`; cost budget `suiteward-poc-monthly` (US$25/month measured before credits, email alerts at 80% actual and 100% forecast); Cloudflare tunnel `suiteward-poc` (`09d32d23-e113-48d3-b870-ec1acda0dc69`) running as a system service on the host with hostname `suiteward-poc.magalz.space` answering 404 for everything; the agent's AWS session logged out afterwards. Remaining before S1-D (historical: S1-D has run): a Cloudflare Access application for the hostname (owner, Zero Trust dashboard) before any route reaches a service, the GHCR deployment workflow with its protected environment, and the owner placing the test App key on the host through Session Manager. Settled 2026-10-10 from the S1 findings ([s1-github](../spikes/s1-github.md)): CI publishes `ghcr.io/ignisdevne/suiteward` (tags `sha-<commit>` and `deploy`) with a workflow using `GITHUB_TOKEN`; the package is public so the host holds no registry credential; moving `deploy` requires the owner's approval in the `remote-poc` environment (deployment branch rule: `phase/M1.2`, `phase/M1.4` since M1.4-D and `phase/M1.5` since M1.5; `main` is added when the owner decides); the host adopts it through `AutoUpdate=registry` (the default timer is daily, so smoke runs start one update by hand); access is Session Manager only (no SSH daemon, no Tailscale), plus Cloudflare Access for any HTTP route. PostgreSQL runs as a rootless container beside the service on both targets (the image pinned by digest in CI, a named volume, the password in a Podman secret the owner creates); RDS stays the scale-out path. IMDS: a firewall rule on the host rejects `169.254.169.254` for the `suiteward` user and is re-verified with the S1 check; the SSM agent keeps its access. Cancelling a stale run waiting for approval is a human action. An agent never holds host access; the owner runs host steps in their own Session Manager session. The S1 spike service, its Podman secret and volume are removed from the host by the owner. Decided 2026-10-17 (M1.8): `main` triggers the deployment and is added to the `remote-poc` deployment branches. The promotion approval stays.
- **D-JOB-RETENTION:** how long finished jobs stay visible on `/status`. Until decided, River v0.49.0's cleaner defaults apply (completed and cancelled 24 hours, discarded 7 days); the outbox keeps its terminal rows. Raised by the M1.2 phase review.
- **D-EXECUTION:** first executable profile and execution trust model; decided in M2 before implementation.
- **D-MANAGED-EXECUTION:** future provider-hosted or customer-infrastructure execution; nothing scheduled.
- **D-HARDENING-CONTROLS:** justified future signing, provenance and transparency controls. The first candidate, since 2026-10-17, is signing the release image and verifying it against `release.yml` on a `refs/tags/v*` ref (D-RELEASE).

## Post-MVP

- **D-MANAGED-SERVICE:** the paid managed service (owner's target, 2026-10-17), with a public App, several owners per instance, web enrollment and a hosted trust model (D-INSTALL, ADR 0009).
- **D-QUEUE:** contract-change priority queue ([ADR 0022](../decisions/0022-contract-change-pr-priority.md)).
- **D-ARCHIVE:** encrypted backup archive format ([ADR 0019](../decisions/0019-encrypted-backups-and-instance-recovery-key.md)).
- **D-BACKUP-REMOTE:** Release backups and the 29-day contingency ([ADR 0021](../decisions/0021-release-backups-and-health-reconciliation.md)).
- **D-RESTORE:** guided restore CLI ([ADR 0020](../decisions/0020-guided-local-cli-recovery.md)).

The interim MVP backup is `pg_dump` plus a tarball of the artifact volume, with a runbook that re-bootstraps from the protected main branch.
