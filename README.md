<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/brand/svg/suiteward-logo-dark.svg">
    <img src="assets/brand/svg/suiteward-logo.svg" alt="SuiteWard" width="420">
  </picture>
</p>

<p align="center">
  <strong>A human-approved, tamper-evident test contract for repositories where AI agents write the code.</strong>
</p>

<p align="center">
  <a href="https://github.com/IgnisDevNE/SuiteWard/actions/workflows/ci.yml"><img src="https://github.com/IgnisDevNE/SuiteWard/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://codecov.io/gh/IgnisDevNE/SuiteWard"><img src="https://codecov.io/gh/IgnisDevNE/SuiteWard/branch/main/graph/badge.svg" alt="Coverage"></a>
  <a href="https://github.com/IgnisDevNE/SuiteWard/releases/latest"><img src="https://img.shields.io/github/v/release/IgnisDevNE/SuiteWard?color=3DD6A0&labelColor=12202F" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0--only-3DD6A0?labelColor=12202F" alt="License: AGPL-3.0-only"></a>
</p>

<p align="center">
  <a href="docs/quickstart.md">Quickstart</a> ·
  <a href="docs/install.md">Install guide</a> ·
  <a href="docs/agent-install.md">Installing with an agent</a> ·
  <a href="docs/plan/README.md">Roadmap</a>
</p>

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
- **Labeled assurance.** An **isolated** installation, where SuiteWard, its App key, its database and the TOTP secret are out of the agent's reach, gives full assurance. A **co-located** one, next to the agent, is supported, and the check summary and the proposal, revision and merge comments say that its assurance is reduced.

## A pull request, step by step

```mermaid
flowchart LR
    A["Agent opens a PR<br/>that changes a test"] --> B{"Protected files<br/>changed?"}
    B -- no --> G["SuiteWard / Contract<br/>success"]
    B -- yes --> C["Revision comment<br/>P&lt;n&gt;-R&lt;m&gt;: files and reductions"]
    C --> D["Owner reviews the diff<br/>/suiteward approve P&lt;n&gt;-R&lt;m&gt; &lt;TOTP&gt;"]
    D --> G
    G --> E["Merge"]
    E --> F["Merge commit matches<br/>the approved revision"]
    F --> H["New canonical version<br/>V1, V2…"]
```

1. **The first contract.** On a repository with no contract, the owner comments `/suiteward propose`.
   - With no `.suiteward.yml`, SuiteWard replies with a suggested one and an editor link that opens with the file filled in.
   - The owner commits it and posts `/suiteward propose` again.
   - The owner approves with `/suiteward approve P1-R1 <code>` and merges, and V1 is promoted. `/suiteward skip <code>` merges a pull request without starting a contract.
2. **A change to protected files.** An agent opens a pull request that changes a test. SuiteWard posts `SuiteWard contract change P2-R1` with the changed files, and the check waits.
   - The owner reviews the diff of those files and asks the agent why they changed.
   - If the change is right, the owner approves `P2-R1`, and the check turns green.
3. **A test outside the scope.** The pull request also adds `spec/calc_spec.rb`, which the contract does not cover. SuiteWard posts a scope notice with the canonical declaration plus `*_spec.rb` and `/spec/`, ready to commit.
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
| success | No protected change; or the current revision is approved and assessed. A pull request the owner skipped before the first contract is also `success`, with nothing assessed. |
| in_progress | An approval is missing, there is no contract yet, a runner file needs a decision, the revision is no longer current, the pull request comes from a fork, the tree is too large to read, or SuiteWard is still reading. |
| failure | `.suiteward.yml` is invalid or a symbolic link hides what would be protected (`invalid-source`); the canonical failed its integrity check; or a confirmation code was wrong or reused (a valid `approve` or `revoke`, or a new push, clears it). |

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

Patterns are gitignore-style. A pattern with no slash inside it matches at any depth. A symbolic link to a file of the repository is accepted unless the scope covers the link's own path; any other link that a pattern can reach makes the declaration invalid, unless an `exclude` names its exact path. Submodules are outside the scope and never read.

## What it does not do

- **It does not run the tests.** The claim is that the gating contract is the one a human approved, not that it was faithfully executed. Canonical execution in a controlled environment is the next milestone (M2).
- **It does not read file contents to find tests.** A runner configured inside a mixed file (`pyproject.toml`, `package.json`) is reported, not protected.
- **It does not support pull requests from forks.** Their check stays in progress, so the ruleset blocks them (fail closed).
- **It cannot read very large monorepos** whose tree GitHub cannot list in one request.
- **It has no webhooks.** It polls GitHub about every 20 seconds, and the App needs no inbound network access.
- **It never writes to the repository.** The App reads contents and writes only checks and comments. Every commit is the owner's.

## Install

SuiteWard is self-hosted: one container image (`ghcr.io/ignisdevne/suiteward`, `linux/amd64` and `linux/arm64`) plus PostgreSQL, and a GitHub App. You install it with the `suiteward` installer, a signed release binary for linux/amd64, linux/arm64, windows/amd64, darwin/amd64 and darwin/arm64 that carries the digest of its own image. The [install guide](docs/install.md) covers both tiers:
- **Co-located:** `suiteward install` on the developer's own PC, with Podman (strongly recommended) or Docker (discouraged). It starts the stack, creates the GitHub App through your browser, writes the ruleset for you to import, and runs the owner setup. On a terminal it runs full screen, in the SuiteWard colors, and prints plain lines with `--plain` or no terminal. Windows is validated live; macOS is covered by tests that CI runs on a macOS machine, with its live validation pending (the binaries are not notarized); Linux is covered by tests only.
- **Isolated:** `sudo suiteward install host`, then `suiteward install --tier isolated` as the service account (it creates the GitHub App through a Session Manager port forward, or takes one you made), on an AlmaLinux 9 host (Podman 5.5 or later) under systemd, outbound only, with full assurance.

On Linux and macOS, one command downloads, checks and installs the binary of a release whose page lists `install.sh`:

```sh
curl -fsSL https://github.com/IgnisDevNE/SuiteWard/releases/download/<tag>/install.sh | SUITEWARD_VERSION=<tag> sh
```

Set `SUITEWARD_VERSION` to the same tag: the script is the same file in every release, and without the variable it takes GitHub's latest release, which is never a pre-release. It checks the binary against the release's `SHA256SUMS`, and verifies the signature when cosign is installed ([how it works](docs/install.md#4-get-and-verify-the-installer)). On Windows, or to verify the download by hand, use `scripts/verify-release.ps1` (same section). Check an instance at any time with `suiteward doctor`. The guide also covers TOTP enrollment, the ruleset, upgrades (`suiteward install --upgrade`, which backs up first) and key rotation. [Backup](docs/backup.md) covers backup, restore and a fresh bootstrap.

- **In a hurry?** The [quickstart](docs/quickstart.md) is the short path for each tier.
- **Installing with a coding agent?** Point it to [agent-install.md](docs/agent-install.md). It says what the agent may prepare, and which steps (the installer's GitHub flow and App key, TOTP enrollment, the ruleset, every `/suiteward` command) only you do.

## Status

- **The MVP is feature-complete for a single owner on GitHub,** and accepted on both tiers. SuiteWard governs its own tests: every pull request to this repository that changes a protected file needs the owner's approval.
- **Releases:**
  - `v0.1.0` is the first release, and `v0.1.1` adds scope discovery (M1.9).
  - `v0.1.3` was the last release before the hardening phase.
  - `v0.2.0` (2026-10-22) is the current release and runs on both tiers. It adds a least-privilege database role, `suiteward migrate`, signed releases with provenance and an SBOM, and an AlmaLinux 9 isolated host.
  - The security assessment is done, and the hardening phase ([SH](docs/plan/phases/SH.md)) is merged and released.
  - The installer (phase M1.12) is merged and ships as the pre-release `v0.3.0-alpha.1`; the findings of its validation are fixed for `v0.3.0-alpha.2` in [#53](https://github.com/IgnisDevNE/SuiteWard/pull/53).
  - Phase [M1.13](docs/plan/phases/M1.13.md) adds macOS, the one-line install and the full-screen installer; its change report and encrypted backups are not merged yet.
- **Security:** see [SECURITY.md](SECURITY.md), the [threat model](docs/security/threat-model.md), and [`scripts/verify-release.ps1`](scripts/verify-release.ps1), which verifies a release's signature and provenance.
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

Development targets Windows with PowerShell 7, with Windows, Linux and macOS CI and local PostgreSQL in Podman:
- `./scripts/dev.ps1 setup` prepares a checkout.
- `./scripts/dev.ps1 doctor` verifies it.
- `./scripts/dev.ps1 check` runs the checks.
- `./scripts/dev.ps1 persistence` runs the database tests.

See [local development](docs/local-development.md) and [CI and coverage](docs/ci-and-coverage.md).

Work is test-first: for governance and adapter behavior, the failing test is committed before the implementation.

An AI orchestrator plans the work, subagents implement it in isolated worktrees, and an independent reviewer checks every task. Only the owner approves protected changes and merges into `main`. The rules are in [AGENTS.md](AGENTS.md) and [orchestration](docs/plan/orchestration.md).

## License

SuiteWard is licensed under the GNU Affero General Public License v3.0 only (`AGPL-3.0-only`); see [LICENSE](LICENSE). The image carries the license texts of every third-party module linked into the binary under `/licenses`. See [ADR 0009](docs/decisions/0009-agpl-3.0-distribution.md).
