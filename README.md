# SuiteWard

**A human-approved, tamper-evident test contract for repositories where AI agents write the code.**

SuiteWard lets a repository hold and propose tests, but not redefine the tests that decide whether its own changes are acceptable. That set of files is a versioned, immutable contract, and only the project's verified owner can change it, by approving one exact revision with a one-time code.

## Why it exists

When an AI agent writes both the code and the tests, the same agent can edit the tests that judge its change:
- delete a failing test;
- weaken an assertion;
- skip a file;
- change the runner configuration so a test no longer runs;
- change the CI workflow.

GitHub's CODEOWNERS, branch protection and required reviews answer "who must review a change to these files". SuiteWard answers a narrower question: **is the test contract that gates this code still the one a human approved?**

## What it does

Everything happens in the pull request: comments, commands and one required check. There is no dashboard.

- **A protected scope.** A `.suiteward.yml` file lists the tests and the runner files that form the contract. `.suiteward.yml` itself and `.github/workflows/**` are always protected.
- **A proposal for every protected change.** When a pull request adds, changes or removes a protected file, or narrows the scope, SuiteWard posts a revision comment.
  - The comment lists the files added, modified and removed.
  - For a narrowed scope, it also lists the rules and paths that the narrowing drops.
- **Approval of an exact revision.** The owner approves one revision with `/suiteward approve P<n>-R<m> <code>`.
  - The code is a TOTP from the owner's authenticator app, and each code works once.
  - A new push that changes protected files makes a new revision, which needs its own approval.
  - A push that changes only implementation keeps the approval and triggers a fresh assessment.
- **A required check.** `SuiteWard / Contract` blocks the merge, through the repository's ruleset, until the current revision is approved and assessed.
- **Promotion on merge.** On merge, SuiteWard reads the merge commit's tree and compares it with the approved revision. If they match, it promotes a new canonical version (V1, V2…) in one atomic step. Versions are never rewritten, and SuiteWard checks the canonical's integrity every hour.
- **Scope discovery.** SuiteWard helps keep the scope complete. It never widens it by itself.
  - **First contract.** `/suiteward propose` suggests a declaration. The suggestion is built from file paths only, using the conventions of common frameworks (Go, pytest, Jest, Vitest, RSpec, Maven, Cargo and more) at any depth, plus runner files such as `conftest.py`, `jest.config.*` and `pytest.ini`.
  - **New tests.** A pull request that adds test-like files outside the scope gets a notice, with the updated declaration to commit.
  - **Runner files.** A pull request that adds or changes a runner file outside the scope keeps the check waiting until the owner protects or ignores it. A runner file can change how the protected tests run without touching any of them.
  - **Edit links.** Every suggested change links to GitHub's editor. When the diff shows `.suiteward.yml`, SuiteWard also posts a review suggestion that GitHub's **Commit suggestion** button applies.
  - **Mixed files.** Files that can hold test settings among other content, such as `package.json` and `pyproject.toml`, are named in a warning and never suggested.
- **Evidence, not authority.** Comments and notices explain; they never approve or protect anything. A wrong or unrecognized command gets a reply and changes nothing. The exception is a wrong or reused confirmation code, which fails the check as a security signal.
- **Labeled assurance.** An **isolated** installation, where SuiteWard, its App key, its database and the TOTP secret are out of the agent's reach, gives full assurance. A **co-located** one, next to the agent, is supported, and every comment says that its assurance is reduced.

## A pull request, step by step

1. **The first contract.** On a repository with no contract, the owner comments `/suiteward propose`.
   - With no `.suiteward.yml`, SuiteWard replies with a suggested one and an editor link that opens with the file filled in.
   - The owner commits it and posts `/suiteward propose` again.
   - The owner approves with `/suiteward approve P1-R1 <code>` and merges, and V1 is promoted. `/suiteward skip <code>` merges a pull request without starting a contract.
2. **A change to protected files.** An agent opens a pull request that changes a test. SuiteWard posts `SuiteWard contract change P2-R1` with the changed files, and the check waits.
   - The owner reviews the diff of those files and asks the agent why they changed.
   - If the change is right, the owner approves `P2-R1`, and the check turns green.
3. **A test outside the scope.** The pull request also adds `spec/calc_spec.rb`, which the contract does not cover. SuiteWard posts a scope notice with the canonical declaration plus `*_spec.rb`, ready to commit.
4. **At any time.** `/suiteward suggest` compares the pull request's head with the contract and replies with what is missing. Before the first contract, it compares with the declaration that `propose` would use.

### Commands

| Command | Who | What it does |
| --- | --- | --- |
| `/suiteward propose` | Owner | Proposes the first contract from this pull request, or replies with a suggested declaration when there is none. |
| `/suiteward skip <code>` | Owner | Lets this pull request merge before any contract exists. |
| `/suiteward approve P<n>-R<m> <code>` | Owner | Approves that exact revision. |
| `/suiteward revoke P<n>-R<m> <code>` | Owner | Withdraws an approval before the merge. |
| `/suiteward suggest` | Owner | Replies with the test-like files and runner names the contract misses, and the updated declaration. It needs no code and grants nothing. |

Commands from any other account are recorded and ignored. The prefix and the verb are case-insensitive.

### The check

| State | Meaning |
| --- | --- |
| success | No protected change; or the current revision is approved and assessed. |
| in_progress | An approval is missing, there is no contract yet, a runner file needs a decision, or SuiteWard is still reading. |
| failure | `.suiteward.yml` is invalid or a symbolic link hides what would be protected (`invalid-source`); the canonical failed its integrity check; or the last command had a wrong or reused code. |

The full table, with every reason, is in the [install guide](docs/install.md).

### `.suiteward.yml`

```yaml
version: 1
include:            # the tests
  - "**/*_test.go"
  - "**/testdata/**"
runner:             # files that change how the tests run
  - /.golangci.yml
  - scripts/ci.ps1
exclude:            # paths left out on purpose (a reduction the owner approves)
  - /docs/examples/
```

Patterns are gitignore-style. A pattern with no slash inside it matches at any depth. A symbolic link counts by its target. Submodules are outside the scope and never read.

## What it does not do

- **It does not run the tests.** The claim is that the gating contract is the one a human approved, not that it was faithfully executed. Canonical execution in a controlled environment is the next milestone (M2).
- **It does not read file contents to find tests.** A runner configured inside a mixed file (`pyproject.toml`, `package.json`) is reported, not protected.
- **It does not support pull requests from forks.** They fail closed.
- **It cannot read very large monorepos** whose tree GitHub cannot list in one request.
- **It has no webhooks.** It polls GitHub every 20 seconds, and the App needs no inbound network access.
- **It never writes to the repository.** The App reads contents and writes only checks and comments. Every commit is the owner's.

## Install

SuiteWard is self-hosted: one container image (`ghcr.io/ignisdevne/suiteward`, `linux/amd64` and `linux/arm64`) plus PostgreSQL, and a GitHub App that the owner creates. The [install guide](docs/install.md) covers both tiers:
- **Isolated:** a Linux host under systemd, outbound only. This is the recommended tier.
- **Co-located:** Podman on the developer's Windows PC.

The guide also covers TOTP enrollment, the ruleset, upgrades and key rotation. [Backup](docs/backup.md) covers backup, restore and a fresh bootstrap.

## Status

- **The MVP is feature-complete for a single owner on GitHub,** and accepted on both tiers. SuiteWard governs its own tests: every pull request to this repository that changes a protected file needs the owner's approval.
- **Releases:**
  - `v0.1.0` is the first release.
  - `v0.1.1` adds scope discovery (M1.9) and is in release.
  - The final MVP release comes after a security gate: hardening, a heavy pentest of both tiers, and a SecOps review.
- **After the MVP:**
  - canonical test execution (M2);
  - a managed service;
  - encrypted backups and guided restore;
  - a change report for protected files;
  - reading runner settings inside configuration files.

  The roadmap is in the [delivery plan](docs/plan/README.md).

## Architecture

- **One Go binary,** `suiteward`, serves the API and runs the workers: Go, Chi, PostgreSQL with pgx and sqlc, and River for jobs.
- **Artifacts and GitHub access:** canonical artifacts are content-addressed on a dedicated volume. GitHub is reached through a GitHub App, outbound only.
- **Five invariants hold throughout:**
  - canonical immutability;
  - candidate non-authority;
  - exact-revision approval;
  - atomic promotion;
  - evidence kept distinct from authority.
- **Where to read more:** the [ADR index](docs/decisions/README.md) maps the decisions, and the [persistence](docs/contracts/persistence.md) and [runtime](docs/contracts/runtime.md) contracts fix the interfaces.

## Develop

Development targets Windows with PowerShell 7, with Windows and Linux CI and local PostgreSQL in Podman:
- `./scripts/dev.ps1 setup` prepares a checkout.
- `./scripts/dev.ps1 doctor` verifies it.
- `./scripts/dev.ps1 check` runs the checks.
- `./scripts/dev.ps1 persistence` runs the database tests.

See [local development](docs/local-development.md) and [CI and coverage](docs/ci-and-coverage.md).

Work is test-first: for governance and adapter behavior, the failing test is committed before the implementation.

An AI orchestrator plans the work, subagents implement it in isolated worktrees, and an independent reviewer checks every task. Only the owner approves protected changes and merges into `main`. The rules are in [AGENTS.md](AGENTS.md) and [orchestration](docs/plan/orchestration.md).

## License

SuiteWard is licensed under the GNU Affero General Public License v3.0 only (`AGPL-3.0-only`); see [LICENSE](LICENSE). The image carries the license texts of every third-party module linked into the binary under `/licenses`. See [ADR 0009](docs/decisions/0009-agpl-3.0-distribution.md).
