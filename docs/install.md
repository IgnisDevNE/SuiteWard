# Installing SuiteWard

This guide installs SuiteWard once, creates its GitHub App, and runs the owner setup. After that you only approve the pull requests that change protected test files. For the short path, see the [quickstart](quickstart.md). If a coding agent helps you install, give it [agent-install.md](agent-install.md) first.

Two rules apply to every command:

- Pin one release. Replace `vX.Y.Z` with the release you install, everywhere it appears. Do not use a moving tag.
- Replace each value in angle brackets, such as `<owner/name>`, with your own value. Never put a secret, a token or a TOTP code into a file, a log, a pull request or a chat.

## 1. What SuiteWard does and does not do

SuiteWard keeps the `SuiteWard / Contract` check of a pull request that changes the protected test files pending until the owner approves that exact revision, and your ruleset (section 7) blocks the merge: SuiteWard does not block it by itself. When the pull request merges, SuiteWard checks the approval and the integrity of the protected files at the merge commit, and promotes the approved contract as the new canonical version.

SuiteWard does not run your tests, and it does not claim that your CI ran them. A merge is not proof that the tests ran, passed or are correct. Your review and your own test check decide that. The full statement is in [ADR 0014](decisions/0014-merge-triggered-canonical-promotion.md).

## 2. Choose a tier

The tier says where SuiteWard runs compared with your coding agent. The way you install SuiteWard declares the tier. SuiteWard never detects it, and it reports the tier in `/status` and in the check summary.

- **Co-located:** SuiteWard runs on the same machine as the coding agent. The App key, the database and the TOTP secret are then reachable by the agent. SuiteWard states this reduced assurance and does not claim protection it cannot provide. Section 5 installs it: one command on your own machine, the path most users follow.
- **Isolated:** SuiteWard runs on a separate host that the coding agent cannot reach. It can also run in a VM, or under another OS user, when the agent has no access to the container runtime and the keyring. This tier gives the full assurance. Section 10 installs it, for whoever self-hosts on a machine the agent cannot reach.

Neither tier protects against a compromised host administrator. The coding agent must use its own GitHub identity, never your credentials. [ADR 0027](decisions/0027-agent-human-trust-separation.md) gives the reasoning.

Never let two SuiteWard instances watch the same repository; one instance serves up to 20. Two instances post as the same App, and each has its own TOTP secret. One of them then rejects the owner's codes and publishes a failing check.

## 3. Prerequisites

- **Rootless Podman.** Both tiers run rootless: `deploy/local.ps1` refuses a rootful Podman connection, and the isolated units run under an unprivileged account.
- **Co-located PC:** Windows with PowerShell 7.2 or later, run as `pwsh` (not Windows PowerShell 5.1), and a Podman machine that has a rootless connection. The co-located path (section 5) is Windows only: it runs `deploy/local.ps1`.
- **Isolated host:** AlmaLinux 9 (EL9) with systemd user services, and an unprivileged account for SuiteWard that stays logged in through linger. This guide calls it `suiteward`. Other distributions are not covered. The host needs Podman 5.5 or later, with the fix for CVE-2026-57231. A distribution can backport that fix without changing the version, so check the fix itself, as step 10.1 does. SELinux stays enforcing: the units use named volumes, which Podman labels by itself. Each step on this host is marked "as `suiteward`" or "as the administrator" (the steps with `sudo`). Use administrator rights only for the steps marked "as the administrator".
- **An authenticator app** that scans an `otpauth://` URI or accepts a base32 secret.
- **A GitHub account** that owns or administers the repositories, and up to 20 repositories to protect. The owner's login must be a user account, not an organization or a bot.
- **The release source** at `https://github.com/IgnisDevNE/SuiteWard/tree/vX.Y.Z`. Download it, or clone the repository and check out the tag `vX.Y.Z`. The co-located install uses `deploy/local.ps1`; the isolated install uses the files in `deploy/quadlet/`.

## 4. Create the GitHub App

Create the App by hand, and tick each box when it is done:

- [ ] Create a GitHub App under your user account, or under an organization you administer.
- [ ] Set the **Homepage URL**. GitHub requires one, and any URL works, for example the repository's.
- [ ] Leave the webhook inactive: uncheck **Active**. SuiteWard polls GitHub and receives no webhooks.
- [ ] Set the repository permissions to exactly these, and nothing else:
  - Metadata: Read-only (GitHub requires it)
  - Pull requests: Read and write
  - Contents: Read-only
  - Administration: Read-only
  - Checks: Read and write
- [ ] Answer "Where can this GitHub App be installed?" with **Only on this account**.
- [ ] Generate a private key. GitHub downloads a `.pem` file. Keep it somewhere safe until section 5 or 10 stores it. Never commit it. Do this before the next item: the private key must exist before GitHub allows the installation.
- [ ] Install the App on the repositories you want to protect, and no others. The limit is 20.
- [ ] Note the **App ID** (on the App's settings page) and the **installation ID** (the number in the address of the installation's settings page).
- [ ] Optional: upload [`assets/brand/png/suiteward-github-app.png`](../assets/brand/png/suiteward-github-app.png) as the App's logo, with the badge background color `#12202F`, so its comments and checks carry the SuiteWard mark.

Keep these permissions off, for these reasons:

- **Administration: Write** stays off. SuiteWard reads protection and never changes it. You set the ruleset yourself (section 7).
- **Contents: Write** stays off. SuiteWard reads code and never commits. The `.suiteward.yml` declaration is yours to commit.
- **Issues** stays off. Pull request comments are covered by the Pull requests permission.
- **Webhooks** stay off. This release polls GitHub and accepts no inbound webhooks.
- **Any other permission** stays at No access. SuiteWard does not request it.

## 5. Install co-located

Run these steps on the PC that also runs your coding agent, in PowerShell 7.2 or later. Read section 2 first: on this tier the agent can reach the App key, the database and the TOTP secret.

**5.1 Open the release source.** Get the source of `vX.Y.Z` (section 3), and open PowerShell 7.2 or later in its folder.

**5.2 Select the rootless connection.** The stack must run in the Podman machine's rootless store. `deploy/local.ps1` refuses a rootful connection. Every other `podman` command in this guide (pull, exec, logs, backup) must reach the same store, so select the connection for this whole PowerShell session. List the connections:

```
podman system connection list
```

Expect one entry for the rootless connection (its URI holds `/run/user/<number>/podman/podman.sock`, and its name is often `podman-machine-default`) and one for the rootful connection (its URI is `/run/podman/podman.sock`, and its name often ends in `-root`). Select the rootless one:

```
$env:CONTAINER_CONNECTION = '<name of the rootless connection>'
```

Check it:

```
podman info --format '{{.Host.Security.Rootless}}'
```

Expect `true`. Notes:

- Do not change the Podman default connection. The variable lasts for this PowerShell session only: set it again in every new session before you run a `podman` command from this guide.
- `deploy/local.ps1` uses `$env:CONTAINER_CONNECTION` when it is set. With no variable, it picks the machine's only rootless connection, or stops and asks for `-PodmanConnection <name>` when it finds several. It restores your connection settings when it ends.
- A rootful store is a different store: it holds none of the containers, volumes or secrets of this stack.

**5.3 Pull the release image.** Run:

```
podman pull ghcr.io/ignisdevne/suiteward:vX.Y.Z
```

Expect a digest line.

**5.4 Start the stack.** The script creates the network, the volumes and the secrets. It generates the database owner password, the application role password and the MCP token and never prints them. Pass the App key file only the first time; the three GitHub values are needed on every run that should keep GitHub:

```
./deploy/local.ps1 -Image ghcr.io/ignisdevne/suiteward:vX.Y.Z -GitHubAppKeyFile <path to the PEM> -GitHubAppId <app id> -GitHubInstallationId <installation id> -GitHubRepositories '<owner/name>,<owner/name>'
```

The script works in this order: it starts the database and waits until it is healthy, creates the application role `suiteward_app`, runs the one-shot `suiteward migrate` container, and starts the service only when migrate exited with code 0. Expect migrate's JSON log line `database migrated`, then the line `Started suiteward-db and suiteward (ghcr.io/ignisdevne/suiteward:vX.Y.Z), GitHub configured.` and the hints for the smoke test. If migrate fails, the script stops with `suiteward migrate failed with exit code N (output above); the service was not started.`

Notes:

- Two database roles are used. The owner `suiteward` is used only by the migrate container, which changes the schema. The service connects as `suiteward_app`, which cannot change the schema or defeat the immutability triggers. Their URLs are the Podman secrets `suiteward-database-owner-url` and `suiteward-database-url`. The password of `suiteward_app` is sent to the database on standard input, never as an argument.
- Always pass `-Image` with your pinned release. The script has its own default image, and that default is not a release.
- The script reads `-GitHubAppKeyFile` only while the secret `suiteward-github-app-key` does not exist yet. Delete your copy of the `.pem` file after the first run.
- Keep the quotes around `-GitHubRepositories`. Without them, PowerShell reads a comma-separated list as an array and joins it with spaces, and the service then refuses to start with `SUITEWARD_GITHUB_REPOSITORIES: "… …" is not an owner/name pair`.
- The script stores none of the three GitHub values. A run without `-GitHubAppId`, `-GitHubInstallationId` and `-GitHubRepositories` replaces the container with one without GitHub. The state stays.
- The MCP listener serves a local agent's `sync` and `status` tools, nothing else. Its token copy is in `%LOCALAPPDATA%\SuiteWard\mcp-token`, readable by your Windows user only.
- The names and the ports are fixed: HTTP on 8081, MCP on 8091. Run one stack per machine.

**5.5 Check the service through the Podman machine.** The published port may not be reachable from Windows itself, so read it inside the machine. Open the machine as its rootless user, `user`: the default ssh user is `root`, which is the rootful store and shows none of this stack. Run:

```
podman machine ssh --username user "curl -s http://127.0.0.1:8081/readyz"
```

Expect `{"schemaVersion":N,"status":"ready"}`.

```
podman machine ssh --username user "curl -s http://127.0.0.1:8081/status"
```

Expect `"tier":"co-located"` in the output.

**5.6 Run setup.** Section 6 covers it.

To stop the stack and keep its state, run `./deploy/local.ps1 -Down`. Expect the three containers (`suiteward`, `suiteward-migrate` and `suiteward-db`) to be removed while the volumes and secrets stay. Start again with the command of step 5.4; the key file is not needed again.

`./deploy/local.ps1 -Down -Purge` also removes the database, the artifact volume, the secrets (including both database URLs), the MCP token copy and the network. That deletes your TOTP enrollment and every contract version. Use it only to start over, and back up first (section 11, step 11.1, and [the backup runbook](backup.md)).

**Converting a stack from v0.1.x.** A stack started by v0.1.x has one database secret, `suiteward-database-url`, and it holds the owner URL: that stack's service ran as the owner. Run the command of step 5.4 again with the new release's `-Image`, and the script converts it in place. It prints `Upgrading an existing stack: suiteward-database-url held the owner URL. Creating the role suiteward_app and replacing the secret with its URL; the owner URL is now in suiteward-database-owner-url and only the migrate container gets it.` Then it creates the secret `suiteward-database-owner-url`, creates the role with a new password, replaces `suiteward-database-url` by the application URL, and migrates. The data is untouched. A run that was interrupted repeats only what is still missing. Back up first (step 11.1) and select the rootless connection (step 5.2). The conversion works only when the old stack is in the rootless store. v0.1.x started it on your default connection, which can be the rootful one. In that case the script would find no stack and start a new, empty one in the rootless store, and nothing moves the old state over. Do this instead: back up the old stack, stop it with `podman --connection <name of the rootful connection> stop suiteward suiteward-db` (both stacks publish the ports 8081 and 8091), start the new one with step 5.4, and restore the backup into it ([the backup runbook](backup.md), co-located restore).

## 6. Run setup

Setup binds you as the owner and enrolls your TOTP secret. Run it yourself, once, in a terminal on this PC (co-located) or on the host (isolated, as `suiteward` in a shell opened as in step 10.1). It needs a real terminal.

Run setup on the machine that runs the instance you are enrolling: the PC for co-located, the host for isolated. Its `--test-check` options name only the repositories of that instance, and each instance keeps its own owner and secret.

Until setup succeeds, SuiteWard comments nothing and publishes no `SuiteWard / Contract` check, so a pull request shows that required check as waiting. This is expected.

Before you start:

- Have your authenticator app ready.
- Turn off recording of the terminal session. Setup prints the `otpauth://` URI and the base32 secret to the terminal, and a recorded session keeps them. If recording was on, turn it off and run setup again. That replaces the secret, so the old one stops working.

Run setup with your login and one `--test-check` for each configured repository. The check name is the exact name of the required status check that runs your tests, as it appears in that repository's ruleset. Do not use `SuiteWard / Contract`: SuiteWard publishes that check itself. Quote each `--test-check` value, for example `--test-check 'owner/name=CI / test'`: check names often contain spaces and slashes, and an unquoted value would be split by the shell.

```
podman exec -it suiteward /suiteward setup --owner <login> --test-check '<owner/name>=<check name>' --test-check '<owner/name>=<check name>'
```

On the co-located PC, run it in the PowerShell session where you selected the rootless connection (step 5.2), so that `podman exec` finds the stack.

Setup then works through these steps:

1. It looks up your login, which must be a user account, and every configured repository. If one lookup fails, it exits with code 1 before it writes anything.
2. It prints a warning and waits. Press Enter to continue. End of input or Ctrl-C exits with code 1, before any secret is created.
3. It prints the `otpauth://` URI and the base32 secret, once. Scan the URI with your authenticator, or type the secret in. The entry is named `SuiteWard:<login> (<tier> <instance>)`, for example `SuiteWard:octo (isolated 1a2b3c4d)`: `<instance>` is the first 8 characters of this instance's id, which `/status` shows as `instance`, so each instance has its own entry. Setup says the same on stderr. If you type the secret in, give the entry that name yourself.
4. It asks for the current 6-digit code. You get three attempts. Three wrong codes exit with code 1 and write nothing.
5. It stores the owner and the secret, records each repository's test check, and checks protection for each repository (section 7).

A missing, extra or duplicate `--test-check` is a usage error: exit code 2, and nothing is written. Setup exits with code 0 once enrollment succeeded, whatever the protection results.

Afterwards, check the result. On the co-located PC, use the `podman machine ssh --username user` form from step 5.5. On the isolated host, as `suiteward`, run `curl -s http://127.0.0.1:8081/status`. The output shows `owner` with your login and the `protection` result of each repository.

Only a setup that prints the protection result of each repository and exits with code 0 counts. Confirm it with `/status`: `owner.enrolledAt` changes on every successful run, so a value that did not change means that setup did not complete. An interrupted setup can leave an authenticator entry that matches nothing: delete it.

Running setup again replaces the TOTP secret, and the new entry has the same name as the old one of this instance. Before you scan it, delete any existing entry with this exact name. Entries of other instances (another id in the name) stay. An older entry named `SuiteWard:<login>`, with no instance in its name, predates this release: delete it once the new entry's code is accepted. Never paste the setup output, the URI, the secret or a code into a chat, an issue or a pull request.

## 7. Create the ruleset

SuiteWard reports protection and never changes it, so you set the protection of each repository once, on its default branch. Open `https://github.com/<owner>/<repo>/settings/rules` for the repository (replace `<owner>/<repo>`), and create a ruleset (or classic branch protection, at `https://github.com/<owner>/<repo>/settings/branches`) that does the following:

- [ ] Targets the default branch. SuiteWard reads the rules of that branch.
- [ ] Requires the status check `SuiteWard / Contract`, with your SuiteWard App as its source. A check from any other source does not count.
- [ ] Requires your test check, the name you gave to setup.
- [ ] Requires branches to be up to date before merging.
- [ ] Has an empty bypass list. SuiteWard cannot read a ruleset's bypass list, so check it yourself.
- [ ] Enforces the rules for administrators, if the repository uses classic branch protection (`enforce_admins`).

SuiteWard checks protection when you run setup, and then every 15 minutes. `/status` reports each repository's overall result as `enforced`, `findings`, `enforcement-unavailable` or `unknown`; when it is `findings`, the `findings` list names one or more of the codes below. The results mean the following:

| Result or finding | Meaning | What to do |
| --- | --- | --- |
| `enforced` | Nothing is missing. | Nothing. |
| finding `missing-contract-check` | `SuiteWard / Contract` is not required from your App. | Require it, with your App as the source. |
| finding `missing-test-check` | The test check you gave to setup is not required. Before setup, this result appears too. | Require the check, or run setup again. |
| finding `not-up-to-date` | The required checks do not require branches to be up to date. | Turn on the up-to-date requirement. |
| finding `admin-bypass` | Classic protection does not apply to administrators. | Enforce it for administrators. |
| `enforcement-unavailable` | GitHub does not enforce rules on this private repository, or the App cannot read the rules. SuiteWard does not claim protection here. | Either use a GitHub plan that enforces rules on private repositories (GitHub Free does not), or give the App the read permission in section 4. |
| `unknown` | The protection could not be read, for example because the App's token lacks Administration read. | Check the App's permissions against section 4. |
| caveat `ruleset-bypass-unverified` | Shown with a ruleset result. SuiteWard cannot see the bypass list. | Check by hand that the bypass list is empty. |

## 8. First use: bootstrap

A repository has no contract until its first one is approved. Until then, each open pull request shows the `SuiteWard / Contract` check as `in_progress` (GitHub shows "In progress"), reason `bootstrap-awaiting`, with three exceptions: a pull request from a fork shows `fork-unsupported` instead; the pull request carrying the current proposal shows its own state (`in_progress`, `approval-missing` while it waits for your approval, or `success`, `bootstrap-ready` once approved); and a pull request you skipped shows `success`, `bootstrap-skipped`. SuiteWard comments with how to start. It proposes nothing by itself.

On the pull request that should carry the first contract, post one of these commands as the first line of a comment. Only the owner's comments count:

- `/suiteward propose` creates the first proposal from that pull request. It needs no code, and it grants nothing by itself.
- `/suiteward skip <6-digit code>` lets the pull request pass the check before any contract exists. The check then reads "Skipped by the owner before the first contract", and nothing was assessed. SuiteWard refuses this once a contract exists.

Where `.suiteward.yml` is decides the source of the proposal:

1. **On the default branch.** The proposal is pinned to the default branch's head. After you approve it, SuiteWard promotes the first version (V1) by itself, within about a minute, without waiting for a merge.
2. **Only in the pull request's head.** The proposal is that head. Its V1 comes only from this pull request's exact merge commit, after the pull request merges.
3. **Nowhere.** Nothing is proposed. SuiteWard replies with a suggested `.suiteward.yml` and a link. The link opens GitHub's editor on the pull request branch with that file filled in. You review the file and commit it yourself. SuiteWard never commits it. After you commit it, wait about a minute — until the check shows the new head — then comment `/suiteward propose` again.

   The suggestion is built from the file paths of the pull request's tree, never from file contents, and it has two lists:
   - `include:` holds patterns for test files and test directories. They match at any depth, so one pattern such as `*_test.go` covers every directory, and `testdata/` counts wherever it is. Files under vendored directories (`node_modules`, `vendor`, `.venv` and similar) never count.
   - `runner:` holds the test runner files it found, plus the files that the same runner reads before them. Those may not exist yet. For example, when it finds `pytest.ini`, it also lists `pytest.toml`, which pytest reads first. Protecting a file that does not exist yet means that a pull request that adds it needs your approval, so nobody can override your runner settings by creating it.

   The list is limited to 20 patterns, `include` and `runner` together. The runner names it found keep their room, and the `include` patterns give way, so a runner name is never dropped because of a long `include` list. When SuiteWard cut the list, the reply says so. Read the suggestion as a start, and add what is missing before you commit it.

   The reply also warns about mixed files: `package.json`, `pyproject.toml`, `setup.cfg`, `tox.ini`, `Cargo.toml`, `*.csproj` and `CMakeLists.txt`. They can hold test settings among other content. SuiteWard names the ones it found and never suggests them, because protecting them would put every dependency update behind an approval. You decide whether to add one.

   The reply also warns about submodules. A submodule is outside the scope: SuiteWard never reads one, so its contents are not protected, whatever you write in `.suiteward.yml`. When a suggested pattern would reach a submodule (a pattern such as `*_test.go` can reach one that is not vendored), the reply lists it, at most 20, and says that its contents are not protected.

The declaration has `version: 1` and optional `include`, `exclude` and `runner` lists of path patterns, in gitignore style and case-sensitive. A pattern is never folded, but a file whose path differs only in case from a protected file, or that lies in a directory that differs only in case from one where the protected path would be covered, is protected too, because a case-insensitive checkout would put it on the protected file. A tree that spells one name in more than 8 case variants is refused as an invalid source. Any other key makes it invalid:

```yaml
version: 1
include:
  - <test path pattern>
  - "*.spec.ts"
runner:
  - <test runner configuration or script pattern>
```

Quote a pattern that starts with `*` or `!`, such as `"*.spec.ts"` or `"!fixtures/**"`. YAML reads an unquoted leading `*` as an alias and a leading `!` as a tag, not as a literal character, so the pattern would otherwise be invalid YAML.

`.suiteward.yml` itself and `.github/workflows/**` are always protected. Put your test files in `include`, and your test runner configuration and test scripts in `runner`.

**Symbolic links and submodules.** SuiteWard reads the tree of a commit without checking it out, and it treats the two kinds of entry that are not plain files in this way:

- A symbolic link matters only when a pattern of the scope matches the link's own path or could match a path below it. SuiteWard then resolves the link's target inside the same tree, one path component at a time, and follows at most 8 links.
  - A link to a file of the repository is accepted, unless the scope covers the link's own path. If it does, the declaration is invalid (`invalid-source`): protect the file itself.
  - A link to a directory, to a path outside the repository, to nothing (dangling), or a loop of links makes the declaration invalid whenever a pattern can reach it. Replace the link with the files, or, if its content does not need protection, accept the link by adding its exact anchored path to `exclude` (for example `/spec_link`). The `invalid-source` comment shows that declaration. It is a scope reduction, so it needs your approval, and nothing reached through the link is protected.
- A submodule is outside the scope and never makes a declaration invalid. SuiteWard never reads it, so its contents are not protected, and every reply that suggests a declaration warns about the submodules that its patterns would reach.

SuiteWard's proposal comment shows the copyable command. Approve the exact revision it names, for example `/suiteward approve P<n>-R1 <6-digit code>`.

Keep these points in mind:

- A `.suiteward.yml` larger than 64 KiB in a pull request or on the default branch is invalid (`invalid-source`). A real declaration is a few lines.
- A declaration with a pattern of more than 64 segments, or more than 1000 patterns in all, is invalid (`invalid-source`). This also holds for the declaration stored with the canonical: one over a cap fails every assessment until the repository is bootstrapped again.
- A tree that holds a path longer than 4096 bytes, or more than 8 spellings of one name that differ only in case, is invalid (`invalid-source`).
- If a protected file changes after the proposal, SuiteWard cancels the proposal and comments. Post `/suiteward propose` again.
- Pull requests from forks get no proposal. Their check shows `fork-unsupported`, because forks are not supported in this release.
- SuiteWard cannot read a repository whose tree GitHub truncates, which happens with very large monorepos. `propose` refuses it as `invalid-source`, the bootstrap job stops at `origin-not-passed`, and the check of a pull request shows `source-unavailable` (section 9). Such a repository cannot be protected in this release.
- Reading a very large test suite can take longer than the job timeout. SuiteWard reads the protected files eight at a time and keeps what it read, so the default of one minute covers about a thousand files, and a pass that is cut off resumes where it stopped. For a suite far beyond that, raise `SUITEWARD_JOB_TIMEOUT` on the isolated host (section 12). `deploy/local.ps1` has no parameter for it, so the co-located stack keeps the default; for a larger test suite, use the isolated host (section 10) instead.

## 9. Day to day

After the first contract, you only handle pull requests that change protected files.

**Revision comments.** When a pull request gets a new protected revision that needs approval, SuiteWard posts one comment that lists:

- the protected files added, changed and removed against the canonical version, up to a bound;
- the base version;
- the copyable `/suiteward approve P<n>-R<m> <6-digit code>`;
- a line saying which earlier revision this supersedes, if there is one.

Each new revision gets a new comment, and SuiteWard never edits one. A pull request with no protected change, and a push that changes only the implementation, gets no comment. An implementation-only push keeps your approval. A push that changes a protected file creates a new revision that needs its own approval.

**Approve and revoke.** Only the owner's comments are commands. Another account's `/suiteward` comment is ignored without a reply.

- `/suiteward approve P<n>-R<m> <6-digit code>` approves that exact revision.
- `/suiteward revoke P<n>-R<m> <6-digit code>` withdraws that approval.

The command is the first line of the comment that starts with `/suiteward` outside a code block. Nothing else may follow the code on that line. Other lines in the comment are ignored. SuiteWard replies on the pull request with the outcome: approved, revoked, no active approval, or rejected with the reason.

**Codes.**

- A code works once. A step is used up by the first command that uses it, across all commands. A second command needs the next code that your authenticator shows. A replayed code is refused.
- A code is accepted within one 30-second step before or after the comment's creation time. Post the command with a fresh code.
- Post each command once, and do not edit it. SuiteWard processes only the first version of a comment. Editing or deleting it afterwards changes nothing. A command edited before SuiteWard read it is refused. For another action, post a new comment.
- SuiteWard lists comments only on open pull requests, so a command posted after the pull request closed or merged is never seen, and a promotion uses only the consent given before the merge. A command that SuiteWard saw while the pull request was open and had not processed when it closed is processed as usual, except `propose` and `suggest`, which answer `not-open` and do nothing.
- SuiteWard reads new comments about every 20 seconds, so an answer usually comes within a minute.

**What the check states mean.** The check is `SuiteWard / Contract`. Its summary names the state and the next action. GitHub's UI shows `in_progress` as "In progress".

| State and reason | Meaning |
| --- | --- |
| success, `unchanged` | There is no protected change, so there is nothing to approve. |
| success, `ready` | The exact revision is approved and assessed, and it is ready for integration. |
| success, `bootstrap-ready` | The first contract is approved. V1 follows (section 8). |
| success, `bootstrap-skipped` | Skipped by the owner before the first contract. Nothing was assessed. |
| in_progress, `approval-missing` | Approve the current revision with the command in the comment. |
| in_progress, `bootstrap-awaiting` | There is no contract yet. Post `propose` or `skip` (section 8). |
| in_progress, `runner-uncovered` | The pull request adds or changes a test runner file (for example `conftest.py` or `jest.config.js`) that the contract does not cover. Such a file can change how the protected tests run, so the check waits for your decision. SuiteWard's comment offers two ways out, each one click when the pull request's diff shows `.suiteward.yml`: protect the file (add its pattern) or ignore it (add the path to `exclude`). Then approve the new revision. |
| in_progress, `not-current` | This pull request's revision is no longer the current one. Use the newest revision comment. |
| in_progress, `source-unavailable` | GitHub cannot list the whole tree of this revision, so SuiteWard cannot read it. This happens with very large monorepos, which cannot be protected in this release. |
| in_progress, `fork-unsupported` | Pull requests from forks are not supported. |
| in_progress, `assessment-pending` | SuiteWard is still reading the pull request. Wait a few minutes. |
| failure, `invalid-source` | `.suiteward.yml` is invalid, or a symbolic link that the scope reaches points at a directory, outside the repository or nowhere, or the scope covers the link itself (section 8), or it is larger than 64 KiB, or a pattern has more than 64 segments, or it holds more than 1000 patterns; or the tree holds a path longer than 4096 bytes or more than 8 case spellings of one name. Fix it. A submodule never causes this: its contents are not protected. |
| failure, `integrity-failed` | A canonical artifact no longer matches its integrity record. SuiteWard fails the check on every pull request of that repository and does not promote; it does not block merges by itself — your branch protection does that, if you require the check. Restore the artifact from a trusted copy or a backup, never edit the database, and restart the service: it re-verifies at once. On the isolated host, as `suiteward`, run `systemctl --user restart suiteward.service`: the image did not change, so migrate does not run again. On the co-located PC, run the `./deploy/local.ps1` command of step 5.4 again, with the same `-Image` and the three GitHub parameters. |
| failure, `owner-command-rejected` | A confirmation code on your latest command was wrong or reused. Post a valid `approve` or `revoke` with a current code, or push a new head. A later `skip`, `propose` or `suggest` does not clear it. Any other wrong command only gets a reply and changes nothing. |

A success means that the protected files match what the owner approved, except `bootstrap-skipped`, where nothing was assessed. It never means that your tests ran or passed. Your own test check decides that.

**Links and submodules.** Section 8 explains the rules, which hold for every pull request. A link to a file of the repository is accepted unless the scope covers the link's own path. A link to a directory, to a path outside the repository or to nothing makes the check fail with `invalid-source` when a pattern can reach it. Submodules are outside the scope: their contents are not protected, and a reply that suggests a declaration warns about the submodules its patterns would reach.

**At merge.** When a pull request with an approved revision merges, SuiteWard comments naming the new canonical version — or, if it could not promote, a comment saying it did not promote, with the reason (the merged files do not match the approved revision, for example a rebase that brought in another change). Recover with a new pull request: one that restores the protected files of the last canonical version, or one that carries the merged content for a new exact approval ([D-INTEGRATION](plan/decisions.md)). There is no reset command.

### Widening the scope

The scope is the list of paths that `.suiteward.yml` protects. A test directory or a runner file that a pull request adds later is outside it, so nothing protects it. SuiteWard reports such files and never protects them: only your approval changes the contract.

**The scope notice.** When a pull request adds test-like or runner files that the current scope does not cover, SuiteWard posts one comment that:

- lists those files, at most 20 with the total, the mixed files it found and the submodules that a suggested pattern would reach (section 8), and says that the current contract does not protect the files. Submodule contents are never protected;
- gives the full new `.suiteward.yml` in a code block that you can copy, and a link that opens GitHub's editor on the pull request branch;
- says that nothing is protected until you commit the file and approve the revision it creates.

Keep these points in mind:

- The check does not change, and the comment never says that tests ran.
- The comment appears once for each set of new files, not for each push. A later push that adds no new uncovered file posts nothing. A new approved contract that changes the suggested file posts the notice again, once; one that leaves the suggested text as it is posts nothing.
- A path that matches an `exclude` of your scope is never reported: the exclusion is your choice.
- The suggested file starts from the approved `.suiteward.yml` and only adds patterns. It may be re-indented and lose blank lines.
- Comments in `.suiteward.yml` are part of the approved bytes. A pull request that edits only a comment is a protected change and needs your approval. Compare the suggested file with your own before you commit it, because it may have lost a comment.
- A pull request whose own `.suiteward.yml` is valid and already covers the files gets no notice. That widening still needs your approval.
- A declaration that cannot be shown, because it is larger than 8192 bytes (a declaration over 64 KiB is invalid, not just hidden) or holds a character you cannot see (a zero-width or bidirectional control character, for example), is never put in a comment. The comment then lists the new patterns alone, as lines to copy under the `include` and `runner` lists, with the same link to the editor. This holds for the notice, for the reply of `/suiteward suggest` and for the runner comment.

**A suggestion on the diff.** SuiteWard also posts a review comment, together with the notice, when all of these hold: the pull request changes `.suiteward.yml`, the file in its head is a valid declaration in block style, and the diff shows an item line of the `include:` list or the `runner:` list. The comment sits on that line, with the line and the new patterns as a suggestion. Use GitHub's "Commit suggestion" button to accept it. It leaves out the patterns that the file already lists. A list written as `include: [a, b]`, or a list with no item line in the diff, gets no suggestion: copy the block from the notice instead.

**Ask for it: `/suiteward suggest`.** Post it as the first line of a comment on any open pull request. Only the owner's comments count, and it takes no code.

- After the first contract, SuiteWard replies with the scope notice for that pull request's head. If every test-like file is already in the scope, it says so.
- Before the first contract, it replies with the suggestion of section 8, as `propose` does when it finds no declaration.
- It grants nothing and changes no check.
- SuiteWard can refuse it, and the reply names the reason:
  - `fork`: the pull request comes from a fork, and forks are not supported in this release.
  - `not-open`: the pull request is no longer open.
  - `invalid-source`: SuiteWard cannot read the head well enough. Its tree is invalid or truncated, the approved contract has no declaration, or the reply cannot be written (an unusable branch name, for example).

  If the approved contract fails its integrity check, the command gets no reply: SuiteWard retries it and then records it as failed.

**An agent widens the scope** with a pull request that edits `.suiteward.yml`. The edit creates a protected revision, and you approve it like any other. An agent cannot use `/suiteward suggest`: only the owner's comments are commands.

**A reduction.** When a revision removes rules or paths from the scope, its revision comment lists the rules and the paths that the reduction removes, so you see what would stop being protected before you approve.

### Correcting the canonical

A correction puts an earlier version's protected files back, for example the previous version's.
- **How:** open a pull request whose protected files equal that earlier version's exactly, for example a revert of the last protected merge. Approve its revision like any other, and merge it.
- **What you see:** SuiteWard promotes a new version that holds those files, and its comment says that it is a correction of the current version.
- **What is kept:** the corrected version stays. SuiteWard erases no version ([ADR 0018](decisions/0018-corrective-pr-rollback-and-audit-history.md)).
- **Anything else:** protected files that match no earlier version exactly make an ordinary promotion, not a correction.

### Stop protecting a repository

A repository stays enrolled until setup runs again without it. While it is enrolled but missing from the configuration, SuiteWard skips it: nothing is synchronized or checked there, and the commands of the other repositories keep working. A command it had already observed there can still be decided, and its reply may fail. `/status` and the MCP `status` tool name it in `unconfiguredRepositories`, and the log warns once. Deleting the repository on GitHub has the same effect.
1. Remove the repository from the list: `SUITEWARD_GITHUB_REPOSITORIES` in `/etc/suiteward/github.env` on an isolated host (then restart the service), or `-GitHubRepositories` when you run `deploy/local.ps1` again on a co-located PC.
2. Run setup again (section 6) with one `--test-check` for each repository you keep. Setup replaces the TOTP secret, so delete the entry of this instance from your authenticator app before you scan the new one (the entry with this exact name, `<tier> <instance>` included; entries of other instances stay), then add the new entry. An entry named only `SuiteWard:<login>` predates this release: delete it once the new entry's code is accepted.
3. Comment your command again. A command written before the new setup is ignored and gets no reply.

SuiteWard changes nothing on GitHub: remove the App from the repository, and the ruleset rule that requires `SuiteWard / Contract`, yourself.

## 10. Running on a separate host (operators)

This installs the isolated tier (section 2), unless step 10.9 sets the tier to `co-located`: SuiteWard on a separate host that your coding agent cannot reach, for whoever self-hosts on a machine the agent cannot reach. It gives the full assurance. The host runs AlmaLinux 9, Podman 5.5 or later, and the units from `deploy/quadlet/` in the release source.

There are three container units. `suiteward-db` is PostgreSQL. `suiteward-migrate` is a one-shot `suiteward migrate`: it runs as the database owner, brings the schema to this release's version and grants the application role `suiteward_app` its access. `suiteward` is the service. The service requires the migration, which requires the database, so starting the service always migrates first. The service connects as `suiteward_app` and never holds the owner's credential.

**10.1 Prepare the host, enable linger, then open a shell as `suiteward`.** As the administrator, install Podman and OpenSSL:

```
sudo dnf install -y podman openssl
```

Expect the packages installed or already present. Check the fix for CVE-2026-57231. The version string does not show it, because AlmaLinux backports the fix to its Podman 5.8.2 (advisory ALSA-2026:37123), so read the package's changelog:

```
rpm -q --changelog podman | grep CVE-2026-57231
```

Expect at least one line. If the output is empty, run `sudo dnf upgrade -y podman` and check again. If it is still empty, stop: do not run SuiteWard on this host. Then check the version:

```
podman --version
```

Expect 5.5 or later.

If the account `suiteward` does not exist, create it with no login shell:

```
sudo useradd --create-home --shell /usr/sbin/nologin suiteward
```

Rootless Podman needs a range of subordinate user and group ids for the account. `useradd` allocates them on AlmaLinux 9. Check:

```
grep '^suiteward:' /etc/subuid /etc/subgid
```

Expect one line in each file. If one is missing, add the ranges, choosing numbers that no other account in those files uses:

```
sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 suiteward
```

Let the account's services start at boot, and keep them running without a login:

```
sudo loginctl enable-linger suiteward
```

Expect no output.

The `suiteward` account has a `nologin` shell, so `sudo -iu suiteward` fails. Open a shell as `suiteward` with a working user bus instead:

```
r=/run/user/$(id -u suiteward)
sudo -u suiteward -H env XDG_RUNTIME_DIR=$r \
  DBUS_SESSION_BUS_ADDRESS=unix:path=$r/bus bash
```

Start every new session this way. Each step below is marked "as `suiteward`" or "as the administrator". When a step says "as the administrator", type `exit` first to leave this shell, run that step's commands, then open the `suiteward` shell again with the command above before continuing to the next "as `suiteward`" step.

**10.2 Check the account's systemd user manager (as `suiteward`).** Run:

```
systemctl --user is-system-running
```

Expect `running` or `degraded`. If the bus is not ready yet, wait a few seconds and run it again.

**10.3 Create the database owner password and its two Podman secrets (as `suiteward`).** The owner is the database role `suiteward`, the one PostgreSQL creates at its first start. It owns every object and is used only by the migration unit. One generated password feeds both secrets, so you never see or type it. The chain stops at the first failure:

```
pw=$(openssl rand -hex 24) &&
h='suiteward-db:5432/suiteward?sslmode=disable' &&
printf '%s' "$pw" |
  podman secret create suiteward-db-password - &&
printf 'postgres://suiteward:%s@%s' "$pw" "$h" |
  podman secret create suiteward-database-owner-url -
unset pw h
```

Then list the secrets:

```
podman secret ls
```

Expect `suiteward-db-password` and `suiteward-database-owner-url` in the list. The application role's secret, `suiteward-database-url`, comes in step 10.11, once the database runs.

**10.4 Check the shape of both values without showing them (as `suiteward`).** Each line prints a count, and both must print `1`:

```
s='{{.SecretData}}'
p='[0-9a-f]{48}'
podman secret inspect --showsecret --format "$s" \
  suiteward-db-password | grep -Ecx "$p"
h='@suiteward-db:5432/suiteward[?]sslmode=disable'
podman secret inspect --showsecret --format "$s" \
  suiteward-database-owner-url |
  grep -Ecx "postgres://suiteward:$p$h"
unset s p h
```

A `0` means a malformed value. Remove both secrets with `podman secret rm suiteward-db-password suiteward-database-owner-url`, then repeat step 10.3. Nothing has started yet. After the first start the database keeps this password in its volume, so replacing a secret later locks the owner out.

**10.5 Refuse the metadata address for the service account.** This is the example for a cloud VM. The instance metadata service at `169.254.169.254` hands out the instance's credentials. Rootless containers connect to the network as a process of the service account, so a rule for that account covers them. If your host has no metadata service, skip this step.

As `suiteward`, check the address before the rule exists. Expect `200`, which shows the check can see the problem:

```
podman run --rm docker.io/curlimages/curl@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777 -s -m 3 -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' -o /dev/null -w '%{http_code}\n' http://169.254.169.254/latest/api/token
```

As the administrator (`exit` the `suiteward` shell first): install `nftables` if it is missing. AlmaLinux 9 normally has it, because `firewalld` uses it:

```
command -v nft || sudo dnf install -y nftables
```

Expect a path to `nft`, or the installation output.

Create the rule directory:

```
sudo mkdir -p /etc/suiteward
```

Then create the rule file. Run the next command, wait until the cursor stops, paste the block below it, then press Ctrl-D on an empty line:

```
sudo tee /etc/suiteward/imds-block.nft >/dev/null
```

```
add table inet suiteward_imds
delete table inet suiteward_imds
table inet suiteward_imds {
  chain output {
    type filter hook output priority 0; policy accept;
    meta skuid suiteward ip daddr 169.254.169.254 reject
  }
}
```

Load the rule at every boot with a unit, still as the administrator. `ExecStop` starts with `-`, so a missing table never fails a stop:

```
NFT=$(sudo sh -c 'command -v nft')
sudo tee /etc/systemd/system/suiteward-imds-block.service >/dev/null <<EOF
[Unit]
Description=Reject the instance metadata service for the suiteward user
Before=systemd-user-sessions.service
After=nftables.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=$NFT -f /etc/suiteward/imds-block.nft
ExecStop=-$NFT delete table inet suiteward_imds

[Install]
WantedBy=multi-user.target
EOF
```

Then enable it and confirm that the rule is loaded, still as the administrator:

```
sudo systemctl daemon-reload
```

```
sudo systemctl enable --now suiteward-imds-block.service
```

```
sudo nft list table inet suiteward_imds
```

Expect the table to list the `reject` rule for `169.254.169.254`. The rule covers IPv4 only.

Open a shell as `suiteward` again (step 10.1) and run the same check as before. It must now print `000`, confirming the service account is refused at that address. If it still prints `200`, stop: the rule does not cover the container traffic, and the service must not run on this host until you fix it.

```
podman run --rm docker.io/curlimages/curl@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777 -s -m 3 -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' -o /dev/null -w '%{http_code}\n' http://169.254.169.254/latest/api/token
```

Notes for AlmaLinux 9:

- The rule lives in its own table, `suiteward_imds`. `firewalld` filters inbound traffic and manages only its own table, so it leaves this one alone, and the host needs no `firewalld` change for SuiteWard: its two ports are published on the loopback address only.
- The default `nftables.service` is disabled on AlmaLinux 9. If you enable it and its file, `/etc/sysconfig/nftables.conf`, starts with `flush ruleset`, a reload drops the table. Load it again with `sudo systemctl restart suiteward-imds-block.service`.
- The rule matches the `suiteward` account only, so the rest of the host, including an SSM agent that runs as root, is not affected. If you reach the host through SSM, check that a new session still opens.

Step 10.15 checks that GitHub stays reachable, and that the metadata address is refused from the service network too.

**10.6 Write the GitHub settings file (as the administrator).** Create the rule directory, so this step works even when you skipped 10.5:

```
sudo mkdir -p /etc/suiteward
```

These three values are not secrets. Run the next command, wait until the cursor stops, paste the three lines with your own values, then press Ctrl-D on an empty line:

```
sudo tee /etc/suiteward/github.env >/dev/null
```

```
SUITEWARD_GITHUB_APP_ID=<app id>
SUITEWARD_GITHUB_INSTALLATION_ID=<installation id>
SUITEWARD_GITHUB_REPOSITORIES=<owner/name>,<owner/name>
```

Expect the file to hold no secret, and the `suiteward` account to be able to read it. The three values are all set or all unset: a file with only some of them stops the service at startup. The repositories are 1 to 20 `owner/name` entries, comma-separated, with no spaces.

**10.7 Store the App's private key as a Podman secret, without echo.** Open a shell as `suiteward` again (step 10.1). The first line turns echo off, so the key never shows on the screen or in a recorded terminal session. Run it, wait until the cursor stops, paste the whole PEM from your copy, press Enter, then press Ctrl-D. You see nothing while you paste:

```
trap 'stty echo' INT; stty -echo; (umask 077; cat > ~/app-key.pem); stty echo; trap - INT
```

Create the secret and remove the file:

```
podman secret create suiteward-github-app-key ~/app-key.pem
```

```
shred -u ~/app-key.pem
```

Check the shape without showing it. It must print `1`:

```
podman secret inspect --showsecret \
  --format '{{.SecretData}}' suiteward-github-app-key |
  grep -Ec '^-----BEGIN (RSA )?PRIVATE KEY-----$'
```

Then print the fingerprint of the key that the secret holds. It describes the public part, so it is safe to show:

```
podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-github-app-key | openssl rsa -pubout -outform DER 2>/dev/null | openssl dgst -sha256 -binary | openssl enc -base64
```

Open the App's settings page and find the private key you generated in section 4. The page shows its fingerprint as `SHA256:` followed by base64 text. The two must be the same text. The shape check cannot tell a revoked or wrong key from the right one: that is why you compare the fingerprint ([Rotating the App key](#rotating-the-app-key)). If they differ, run `podman secret rm suiteward-github-app-key`, and repeat this step with the right file.

If the terminal goes silent, type `stty echo` and press Enter. After an aborted paste, run `shred -u ~/app-key.pem` and start this step again. Delete your own copy of the `.pem` file as well.

**10.8 Create the MCP token secret (as `suiteward`).** The service needs this secret to start, even if you never use an MCP client. The MCP listener serves a local agent's `sync` and `status` tools, nothing else. The secret is 32 random bytes in hex:

```
printf '%s' "$(openssl rand -hex 32)" |
  podman secret create suiteward-mcp-token -
```

Check it. It must print `1`:

```
podman secret inspect --showsecret \
  --format '{{.SecretData}}' suiteward-mcp-token |
  grep -Ecx '[0-9a-f]{64}'
```

Print the token only when you set up an MCP client, and keep it in your own password manager. Never paste it into a repository or a chat:

```
podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-mcp-token
```

`podman secret create` refuses a name that exists. To replace a secret, remove it with `podman secret rm <name>`, create it again, and restart the service.

**10.9 Pull the images and install the six unit files (as `suiteward`).** Pull the database image by its digest, then the release image:

```
podman pull docker.io/library/postgres:18.6-trixie@sha256:74935e72241653ca55e0414067e6d8763aceb8a810eb51b452253ec3dcfc4336
```

```
podman pull ghcr.io/ignisdevne/suiteward:vX.Y.Z
```

Expect a digest line for each pull. The units pin the release by its digest, not by its tag, because a tag can be rewritten and a digest cannot. The pinning command below reads the digest of the pulled release image itself. Before you pin it, verify that the release workflow signed that digest. Print the digest on the host:

``````
podman image inspect --format '{{.Digest}}' ghcr.io/ignisdevne/suiteward:vX.Y.Z
``````

Then verify it on any machine that has cosign 3.x, for example your own PC, from the release source (the host does not need cosign):

``````
pwsh ./scripts/verify-release.ps1 -Reference ghcr.io/ignisdevne/suiteward@sha256:<digest>
``````

Expect `Verified: … was signed by the SuiteWard release workflow.` The script runs `cosign verify` with the fixed identity `^https://github\.com/IgnisDevNE/SuiteWard/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$` and the issuer `https://token.actions.githubusercontent.com`; you can run `cosign verify --certificate-identity-regexp <identity> --certificate-oidc-issuer https://token.actions.githubusercontent.com ghcr.io/ignisdevne/suiteward@sha256:<digest>` yourself instead. If it fails, do not pin that digest.

Create the directory:

```
mkdir -p ~/.config/containers/systemd
```

Then create each file from `deploy/quadlet/` in the release source. For each command, run it, wait until the cursor stops, paste that file's content, then press Ctrl-D on an empty line:

```
cat > ~/.config/containers/systemd/suiteward.network
```

```
cat > ~/.config/containers/systemd/suiteward-db-data.volume
```

```
cat > ~/.config/containers/systemd/suiteward-data.volume
```

```
cat > ~/.config/containers/systemd/suiteward-db.container
```

```
cat > ~/.config/containers/systemd/suiteward-migrate.container
```

```
cat > ~/.config/containers/systemd/suiteward.container
```

Both `suiteward-migrate.container` and `suiteward.container` carry the placeholder `Image=ghcr.io/ignisdevne/suiteward@sha256:<digest>`. They must name the same image, so replace the placeholder in both with the digest of the image you pulled. The chain reads the digest first and stops if it is empty, so it can never write an incomplete line:

```
d=$(podman image inspect --format '{{.Digest}}' ghcr.io/ignisdevne/suiteward:vX.Y.Z) && [ -n "$d" ] &&
sed -i "s|^Image=ghcr.io/ignisdevne/suiteward@.*|Image=ghcr.io/ignisdevne/suiteward@$d|" ~/.config/containers/systemd/suiteward-migrate.container ~/.config/containers/systemd/suiteward.container
```

Check the result. The command counts, in each file, the lines that have the form of a digest pin:

```
grep -c '^Image=ghcr.io/ignisdevne/suiteward@sha256:[0-9a-f]\{64\}$' ~/.config/containers/systemd/suiteward-migrate.container ~/.config/containers/systemd/suiteward.container
```

Expect `1` for each of the two files. A `0` means the pin is missing or malformed: run the chain again. Then print the three `Image=` lines and compare by eye:

```
grep ^Image= ~/.config/containers/systemd/*.container
```

Expect the database image, and the same `ghcr.io/ignisdevne/suiteward@sha256:` line in `suiteward-migrate.container` and in `suiteward.container`.

In `suiteward.container`, keep `Environment=SUITEWARD_INSTALLATION_TIER=isolated` only if your coding agent cannot reach this account. Otherwise set it to `co-located` ([ADR 0027](decisions/0027-agent-human-trust-separation.md)).

None of the units has an `AutoUpdate=` line, so nothing changes an image behind your back: you change the pinned digests yourself (section 11). Automatic updates are not supported in this release. The database image stays as it is: a major or minor PostgreSQL upgrade is a deliberate change.

**10.10 Check that Podman reads every unit (as `suiteward`).** Run the generator's dry run:

```
/usr/lib/systemd/system-generators/podman-system-generator --user --dryrun
```

Expect every generated unit listed, with no error: the three services `suiteward-db.service`, `suiteward-migrate.service` and `suiteward.service`, the network `suiteward-network.service`, and the two volumes. Podman skips a unit it cannot parse, and you would only see "unit not found" later. If the dry run reports an error, stop and fix that line first. Do not guess at a fix. If the generator is not at that path, find it with `rpm -ql podman | grep generator`.

**10.11 Load the units, start the database, and create the application role (as `suiteward`).** Load the units, then start the database alone. The role must exist before the migration unit runs, and the migration unit needs its secret:

```
systemctl --user daemon-reload
```

```
systemctl --user start suiteward-db.service
```

Expect no output. Wait until the database is healthy. The first start initialises the database and takes a few seconds. The command returns when it is healthy:

```
until [ "$(podman inspect --format '{{.State.Health.Status}}' suiteward-db)" = healthy ]; do sleep 2; done
```

If it does not return within a minute, press Ctrl-C and read the logs: `journalctl --user -u suiteward-db.service -n 50 --no-pager`.

Now create the application role `suiteward_app`, once, and its URL as the Podman secret `suiteward-database-url`. The password is generated here and travels only through standard input, never as an argument, so it appears in no process list and no shell history. The three `SET` lines keep the password out of the database log, even if the statement fails. The chain stops at the first failure:

```
apw=$(openssl rand -hex 24) &&
{ printf "SET log_statement = 'none';\n"
  printf "SET log_min_error_statement = 'panic';\n"
  printf "SET log_min_duration_statement = -1;\n"
  printf "CREATE ROLE suiteward_app LOGIN PASSWORD '%s';\n" "$apw"; } |
  podman exec -i suiteward-db psql -X -q -v ON_ERROR_STOP=1 -U suiteward -d suiteward -f - &&
h='suiteward-db:5432/suiteward?sslmode=disable' &&
printf 'postgres://suiteward_app:%s@%s' "$apw" "$h" |
  podman secret create suiteward-database-url -
unset apw h
```

Expect one line, the ID of the new secret (not its value). The contract's own example, `\password suiteward_app` in `psql`, asks for the password on a terminal: it would make you type a password that you then have to put into the URL secret, which is why this step generates it instead.

Check the shape of the secret without showing it. It must print `1`:

```
podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-database-url | grep -Ecx 'postgres://suiteward_app:[0-9a-f]{48}@suiteward-db:5432/suiteward[?]sslmode=disable'
```

If the role exists already (an earlier attempt that stopped after the SQL, or a rollback, section 11), the SQL fails with `role "suiteward_app" already exists` and no secret is created. Run the same chain with `ALTER ROLE suiteward_app PASSWORD '%s';` in place of `CREATE ROLE suiteward_app LOGIN PASSWORD '%s';`, which gives the role the new password.

The migration unit refuses a role that is the owner or a member of it, a superuser, has `BYPASSRLS`, owns the schema or is a member of its owner, or owns a table in it. `suiteward_app` is none of them. Outside a rollback to v0.1.x (step 11.6), never use the owner's URL as `suiteward-database-url`.

**10.12 Start the service (as `suiteward`).** Run:

```
systemctl --user start suiteward.service
```

Expect no output. The command starts `suiteward-migrate.service` first. Migrate waits for the database, applies the schema as the owner, grants the role its access and exits with code 0, and only then does the service start. The command returns after that. If it reports `A dependency job for suiteward.service failed`, migrate failed: read its journal (`journalctl --user -u suiteward-migrate.service -n 50 --no-pager`), fix the cause, and see section 14. Migrate is not tried again by itself: the failure stays in `systemctl --user status suiteward-migrate.service` and the journal, and the service stays down. After you fix the cause, run the command of this step again.

**10.13 Check the three units (as `suiteward`).** Run:

```
systemctl --user status suiteward-db.service suiteward-migrate.service suiteward.service --no-pager
```

```
podman ps
```

Expect `suiteward-db` and `suiteward` active (running), and `suiteward-migrate` active (exited): it ran once and stays active, so the service can restart without migrating again. `podman ps` lists `suiteward-db` as `(healthy)` and `suiteward`. It does not list the migration: its container is removed when it exits, and its log is in the journal.

**10.14 Check what the host applies (as `suiteward`).** Run:

```
podman info --format '{{.Host.NetworkBackend}}'
```

Expect `netavark`. Then run:

```
podman inspect suiteward-db --format '{{.HostConfig.Memory}}'
```

Expect `402653184` (384 MiB). If either value differs, stop and check the Podman setup before you go on.

**10.15 Check the network paths from the service network (as `suiteward`).** First, GitHub. Run:

```
podman run --rm --network suiteward docker.io/curlimages/curl@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777 -s -m 10 -o /dev/null -w '%{http_code}\n' https://api.github.com/zen
```

Expect `200`. `403` means the shared unauthenticated GitHub rate limit is used up: wait, then run it again. `000` means the host cannot reach GitHub, so fix the network path before you go on.

Then the metadata address, if you did step 10.5. The service network is not the network that step used, so check it again. It must print `000`. If it prints `200`, stop: the rule does not cover the container traffic.

```
podman run --rm --network suiteward docker.io/curlimages/curl@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777 -s -m 3 -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' -o /dev/null -w '%{http_code}\n' http://169.254.169.254/latest/api/token
```

**10.16 Check the service (as `suiteward`).** These commands read the host's loopback port. Expect `{"status":"ok"}` from the first one:

```
curl -s http://127.0.0.1:8081/healthz
```

```
curl -s http://127.0.0.1:8081/readyz
```

Expect `{"schemaVersion":N,"status":"ready"}`, where `N` is the schema version of this release. A 503 means the database or the schema is not ready. Section 14 covers it.

```
curl -s http://127.0.0.1:8081/status
```

Expect JSON with `"tier"` set to the tier you chose (section 10, step 10.9's `SUITEWARD_INSTALLATION_TIER`), `outbox.failed` at 0 and `jobs.discarded` at 0. The `github` list fills in after the first synchronization succeeds, and the `owner` field appears after setup (section 6).

The port is published on the host's loopback only. To read `/status` from another machine, forward the port with your own remote-access tool. SuiteWard does not provide one.

The host install is done: continue with setup (section 6), the ruleset (section 7) and bootstrap (section 8).

## 11. Updating

Pin one release, and update when you choose. Updates are always manual, by digest: automatic updates are not supported in this release (see the end of this section).

An update always goes through the migration. The service never changes the schema, and it refuses a schema that is older or newer than its image: it exits with `schema version X, this build requires Y` and restarts in a loop. So after you change an image, never restart `suiteward.service` alone. Restart `suiteward-migrate.service`, which migrates and then restarts the service.

**11.1 Back up first.** `suiteward migrate` changes the schema before the service starts, and migrations only go forward: an older image refuses to start on a newer schema, and nothing migrates it back. The only way back is the backup, so take one now. See [the backup runbook](backup.md). The backup holds the TOTP secret and the enrollment, so store it as a secret.

**11.2 Pull the new release image.** Use the new `vX.Y.Z`. On the isolated host, run it as `suiteward`:

```
podman pull ghcr.io/ignisdevne/suiteward:vX.Y.Z
```

**11.3 Update an isolated host.** Read the release notes first. An instance on v0.1.1 or older upgrades to v0.1.3 first, and lets its queued comments go out, before any later release: later releases no longer read the comment payloads that v0.1.1 or older queued. Compare the new release's `deploy/quadlet/*` files with the units installed in `~/.config/containers/systemd/`, and carry over anything the new release adds: a new secret (as `suiteward`), a new entry in `/etc/suiteward/github.env` (as the administrator), or a new setting in a unit file (as `suiteward`). Keep a copy of the units you replace, in case you roll back:

```
cp -r ~/.config/containers/systemd ~/systemd-units-$(date +%Y%m%d-%H%M%S)
```

Verify the signature of the digest you pulled, as step 10.9 says (`podman image inspect --format '{{.Digest}}' ghcr.io/ignisdevne/suiteward:vX.Y.Z`, then `pwsh ./scripts/verify-release.ps1 -Reference ghcr.io/ignisdevne/suiteward@sha256:<digest>` on a machine with cosign 3.x). Then change BOTH `Image=` lines to the digest of the image you pulled in step 11.2, the one in `suiteward-migrate.container` and the one in `suiteward.container`. They must stay identical, because the migration must run the same release as the service. The chain reads the digest and stops if it is empty:

```
d=$(podman image inspect --format '{{.Digest}}' ghcr.io/ignisdevne/suiteward:vX.Y.Z) && [ -n "$d" ] &&
sed -i "s|^Image=ghcr.io/ignisdevne/suiteward[:@].*|Image=ghcr.io/ignisdevne/suiteward@$d|" ~/.config/containers/systemd/suiteward-migrate.container ~/.config/containers/systemd/suiteward.container
```

```
grep -c '^Image=ghcr.io/ignisdevne/suiteward@sha256:[0-9a-f]\{64\}$' ~/.config/containers/systemd/suiteward-migrate.container ~/.config/containers/systemd/suiteward.container
```

Expect `1` for each of the two files. A `0` means the pin is missing or malformed: run the chain again. Reload the units, and restart the migration unit:

```
systemctl --user daemon-reload
```

```
systemctl --user restart suiteward-migrate.service
```

Expect no output. The command returns after migrate exited with code 0. Because `suiteward.service` requires the migration, the restart also restarts the service, on the new image. Check both units:

```
systemctl --user status suiteward-migrate.service suiteward.service --no-pager
```

Expect `suiteward-migrate` active (exited) and `suiteward` active (running). If the service is not running, start it with `systemctl --user start suiteward.service`. Expect `/readyz` to answer `ready` again, with the new `schemaVersion` (section 10, step 10.16).

**11.4 Update a co-located PC.** Pull the image (step 11.2, with the rootless connection of step 5.2 selected), then run `./deploy/local.ps1` with the new `-Image` value and the same three GitHub parameters as before (section 5, step 5.4). The script migrates before it starts the service. A run without the GitHub parameters removes GitHub from the stack. If the stack comes from v0.1.x, the script also converts it (section 5, "Converting a stack from v0.1.x").

**11.5 Upgrade an isolated host from v0.1.x.** v0.1.x ran the service as the database owner and migrated inside it. This release splits the roles, so the first upgrade needs three extra steps before the update of step 11.3: the owner URL moves to its own secret, the role `suiteward_app` is created with a new secret, and the migration unit is installed. This guide covers AlmaLinux 9 only. A v0.1.x host on another distribution is not covered: install a new host as in section 10, then restore your backup there ([the backup runbook](backup.md)). An instance on v0.1.1 or older upgrades to v0.1.3 first, so that its queued comments drain, and then to v0.2.0, because v0.2.0 no longer reads comment payloads queued by v0.1.1 or older. On the AlmaLinux 9 host, as `suiteward`:

1. Back up, as in step 11.1, and keep the copy of the units (the `cp` command of step 11.3). Pull the release (step 11.2).
2. Stop the service. The database keeps running. If the v0.1.x docs had you enable automatic updates, disable the timer too, and keep it disabled (also before a rollback to v0.1.x): Podman cannot update the migration unit, so an automatic update would start the service alone on an old schema and loop. Disabling it when it was never enabled does no harm:

   ```
   systemctl --user stop suiteward.service
   ```

   ```
   systemctl --user disable --now podman-auto-update.timer
   ```

3. Copy the owner URL into its own secret. In v0.1.x, the secret `suiteward-database-url` holds the owner URL. The command reads it into a variable and writes it without a trailing newline. It prints the ID of the new secret, not its value:

   ```
   u=$(podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-database-url) &&
   printf '%s' "$u" | podman secret create suiteward-database-owner-url -
   unset u
   ```

4. Check that it has the shape of an owner URL. It must print `1`. A `0` means the secret holds something else: stop and do not continue.

   ```
   podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-database-owner-url | grep -Ecx 'postgres://suiteward:[0-9a-f]{48}@suiteward-db:5432/suiteward[?]sslmode=disable'
   ```

5. Remove the old secret, so that step 10.11 can create the application one under the same name:

   ```
   podman secret rm suiteward-database-url
   ```

6. Create the role and the new `suiteward-database-url`: run the chain and the check of step 10.11, which follow the wait for the healthy database. The database is already running. Do not run the earlier commands of step 10.11 again.
7. Install the migration unit. Create `~/.config/containers/systemd/suiteward-migrate.container` and overwrite `suiteward.container`, from the new release's `deploy/quadlet/`, as in step 10.9: run `cat >` for each, paste, press Ctrl-D. Keep the `SUITEWARD_INSTALLATION_TIER` line your old unit had. The new `suiteward.container` has no `AutoUpdate=` line: the units are pinned by digest and nothing moves them by itself.
8. Pin both units to the digest of the image you pulled, with the chain of step 11.3 and its check (it reads the digest itself, so do it in this step).
9. Run the dry run of step 10.10, then reload and start. The new units are not active yet, so start the service, which starts the migration first:

   ```
   systemctl --user daemon-reload
   ```

   ```
   systemctl --user start suiteward.service
   ```

10. Check the units and the service as in steps 10.13 and 10.16. `/status` shows the new `version` and `schemaVersion`.

**11.6 Roll back.** Migrations only go forward, so there is no downgrade. You return to the pre-upgrade state by restoring the pre-upgrade backup with the old image. Everything after that backup is lost ([the backup runbook](backup.md)). Use it when the new image does not work and you cannot fix it. Do not start an older image on the upgraded database: it refuses to run.

On an isolated host, as `suiteward`:

1. Stop the service and the migration:

   ```
   systemctl --user stop suiteward.service suiteward-migrate.service
   ```

2. When you roll back to v0.1.x, put the old units back: the dated copy you made in step 11.3, or step 1 of 11.5, holds them (`ls -d ~/systemd-units-*` lists the copies). Remove the migration unit, and copy the old `suiteward.container` over the new one:

   ```
   rm ~/.config/containers/systemd/suiteward-migrate.container
   ```

   ```
   cp ~/systemd-units-<timestamp>/suiteward.container ~/.config/containers/systemd/suiteward.container
   ```

   When you roll back to an earlier release of this layout, put its digest into both `Image=` lines instead (step 11.3).
3. When you roll back to v0.1.x, put the owner URL back into `suiteward-database-url`: that release runs the service as the owner and migrates inside it. The second command prints the ID of the new secret, not its value.

   ```
   podman secret rm suiteward-database-url
   ```

   ```
   u=$(podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-database-owner-url) &&
   printf '%s' "$u" | podman secret create suiteward-database-url -
   unset u
   ```

4. Reload the units:

   ```
   systemctl --user daemon-reload
   ```

5. Restore the data from the pre-upgrade backup, with steps 5 and 6 of [the runbook's isolated restore](backup.md#3-restore) (empty the database and load the dump, replace the artifact volume). The database is running, and it holds the roles: `suiteward_app` stays in the cluster, so a later upgrade finds it, and step 10.11 tells you what to run then.

6. Start the service. A v0.1.x release migrates inside its own process, as the owner; a release of this layout runs the migration unit first, on its own image. Then check `/readyz` and `/status` against the `status.json` of the backup (backup runbook, section 3, step 8):

   ```
   systemctl --user start suiteward.service
   ```

On the co-located PC, v0.1.x's `deploy/local.ps1` cannot run this stack any more, because the stack's `suiteward-database-url` is the application URL now. Purge the stack with `./deploy/local.ps1 -Down -Purge`, check out the old release's source, start it with that release's `deploy/local.ps1`, and restore the backup into it as that release's runbook says.

**Automatic updates are not supported.** The isolated tier updates only by digest, as step 11.3 says. Podman's auto-update cannot update the migration unit, because its container exists only while it runs. An automatic update of the service alone would start the new image on the old schema, which it refuses, and loop. Do not add AutoUpdate= to a unit, and do not enable podman-auto-update.timer.

### Rotating the App key

Rotate the key when it may have been seen by something it should not, for example by a coding agent. Do it yourself, from a terminal that no agent can reach. On the isolated host, that is a shell you opened as in step 10.1. A co-located install gives the agent access to the key anyway (section 2). There, rotation helps against a past exposure, a key that left the machine, and not against an agent that still shares your PC user.

The order matters. After you replace the key, the service keeps working for up to an hour on installation tokens that it already holds. A wrong key therefore shows up late, when every GitHub call stops. So you check the fingerprint at each step, and you delete the old key last.

**R.1 Generate a new key.** On the App's settings page, under **Private keys**, generate a private key. GitHub downloads a `.pem` file and shows the key's fingerprint, `SHA256:` followed by base64 text. Note it. Leave the old key in place.

**R.2 Stop the service.**

- Isolated host, as `suiteward`:

  ```
  systemctl --user stop suiteward.service
  ```

- Co-located PC:

  ```
  ./deploy/local.ps1 -Down
  ```

**R.3 Remove the old secret.** Run this on the host as `suiteward`, or on the PC:

```
podman secret rm suiteward-github-app-key
```

**R.4 Put the new key in a file.**

- Isolated host, as `suiteward`: paste it without echo, as in step 10.7. Run the command, wait until the cursor stops, paste the whole PEM, press Enter, then press Ctrl-D:

  ```
  trap 'stty echo' INT; stty -echo; (umask 077; cat > ~/app-key.pem); stty echo; trap - INT
  ```

- Co-located PC: use the `.pem` file that GitHub downloaded, and its path below.

**R.5 Check the file's fingerprint** before you use it. It must be the text you noted in R.1.

- Isolated host, as `suiteward`:

  ```
  openssl rsa -in ~/app-key.pem -pubout -outform DER 2>/dev/null | openssl dgst -sha256 -binary | openssl enc -base64
  ```

- Co-located PC: in Git Bash, run the same command with the path of your `.pem` file in place of `~/app-key.pem`, written as Git Bash accepts it, for example `/c/Users/<you>/Downloads/<file>.pem`.

If it differs, delete the file (`shred -u ~/app-key.pem` on the host, `Remove-Item <path of the new PEM>` on the PC), and repeat R.4 with the right key.

**R.6 Create the secret, and start the service.**

- Isolated host, as `suiteward`:

  ```
  podman secret create suiteward-github-app-key ~/app-key.pem
  ```

  ```
  shred -u ~/app-key.pem
  ```

  ```
  systemctl --user start suiteward.service
  ```

- Co-located PC: run the command of step 5.4 again, with the same `-Image` and the three GitHub parameters, and with `-GitHubAppKeyFile <path of the new PEM>`. The script finds no secret, creates it from that file and starts the stack. Then delete the file with `Remove-Item <path of the new PEM>`.

**R.7 Check the secret's fingerprint again.** It must be the same text as in R.1.

- Isolated host, as `suiteward`:

  ```
  podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-github-app-key | openssl rsa -pubout -outform DER 2>/dev/null | openssl dgst -sha256 -binary | openssl enc -base64
  ```

- Co-located PC, in Git Bash. The Podman machine has no OpenSSL, so the secret goes out to the PC's:

  ```
  podman machine ssh --username user "podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-github-app-key" | openssl rsa -pubout -outform DER 2>/dev/null | openssl dgst -sha256 -binary | openssl enc -base64
  ```

**R.8 Confirm that GitHub accepts the key.** The service starts without a cached token, so its first reads already use the new key. Wait about a minute, and read `/status` twice, a minute apart (section 14 shows how). The `lastSuccess` of each repository must advance, and no repository may show a `lastError`. That is the proof. The log line `bot account could not be resolved` appears only when SuiteWard processes an owner command, so its absence proves nothing. If you do see it, the key is wrong. This prints the lines it finds:

- Isolated host, as `suiteward`:

  ```
  journalctl --user -u suiteward.service --since '10 minutes ago' --no-pager | grep 'bot account could not be resolved'
  ```

- Co-located PC:

  ```
  podman machine ssh --username user "podman logs --since 10m suiteward 2>&1 | grep 'bot account could not be resolved'"
  ```

**R.9 Delete the old key.** Only now, on the App's settings page, delete the old private key. Then delete every other copy of a `.pem` file, including the one GitHub downloaded in R.1.

If R.5 or R.7 shows a different fingerprint, or R.8 shows no advance or a `lastError`, do not delete the old key. Stop the service, and repeat from R.3.

## 12. Configuration reference

Every setting is an environment variable named `SUITEWARD_<NAME>`. A secret can instead be given as `SUITEWARD_<NAME>_FILE`, the path of a file whose content is the value. Give one form, not both. A variable that is set but empty counts as unset.

Where you set them:

- **Isolated host:** in the `Environment=` and `Secret=` lines of `suiteward.container` (as `suiteward`), and in `/etc/suiteward/github.env` for the three GitHub values (as the administrator). The two database URLs are also in `suiteward-migrate.container`. After an edit, run `systemctl --user daemon-reload` and `systemctl --user restart suiteward.service` as `suiteward`. After a change in `suiteward-migrate.container`, or to an `Image=` line, restart `suiteward-migrate.service` instead (section 11).
- **Co-located PC:** `deploy/local.ps1` has parameters only for the image and the GitHub values. The other settings keep their defaults on this PC. Changing one would mean changing the script, which this guide does not cover.

| Setting | Default | Meaning |
| --- | --- | --- |
| `SUITEWARD_DATABASE_URL` (secret, required) | none | The PostgreSQL URL of the application role `suiteward_app`, never the owner. Use the `_FILE` form. The isolated units mount it from the Podman secret `suiteward-database-url`. |
| `SUITEWARD_MIGRATION_DATABASE_URL` (secret) | none | The PostgreSQL URL of the database owner. Only `suiteward migrate` connects with it, and it requires it; `serve`, `setup` and `probe` refuse to start when it is set, so never put it in the service's unit. Use the `_FILE` form. Only the migration unit mounts it, from the Podman secret `suiteward-database-owner-url`: the service never gets it. |
| `SUITEWARD_ARTIFACT_DIR` (required) | none. The image sets `/data/artifacts` | Absolute path of the artifact store. |
| `SUITEWARD_HTTP_ADDR` | `127.0.0.1:8080`. The image sets `0.0.0.0:8080` | Address of the HTTP listener. The units publish it on the host's loopback only. |
| `SUITEWARD_SHUTDOWN_TIMEOUT` | `30s` (1 s to 10 min) | Time allowed for a graceful stop. The container's stop timeout must exceed this by at least 5 s. The units set 45 s, which covers the default. A value of 40 s or more needs a larger stop timeout. |
| `SUITEWARD_LOG_LEVEL` | `info` | One of `debug`, `info`, `warn`, `error`. |
| `SUITEWARD_JOB_WORKERS` | `4` (1 to 64) | Number of background workers. |
| `SUITEWARD_JOB_TIMEOUT` | `1m` (1 s to 1 h) | Time limit of one attempt of a background job. A `propose` or a check reads about a thousand protected files per minute of this value (eight at a time, and what was read is kept for the next attempt); raise it only for a very large test suite. Isolated host only: `deploy/local.ps1` has no parameter for it, so the co-located stack always keeps the default (section 8). |
| `SUITEWARD_JOB_MAX_ATTEMPTS` | `5` (1 to 25) | Attempts before a background job is marked discarded. |
| `SUITEWARD_OUTBOX_MAX_ATTEMPTS` | `8` (1 to 25) | Attempts to deliver a message to GitHub before it is marked failed. |
| `SUITEWARD_OUTBOX_POLL_INTERVAL` | `5s` (1 s to 1 h) | How often the relay looks for messages to send. |
| `SUITEWARD_OUTBOX_LEASE` | `1m` (at least 5 s) | Lease on a message while it is being sent. |
| `SUITEWARD_GITHUB_APP_ID` | none | Your App ID, a positive integer. The four GitHub settings are all set or all unset. |
| `SUITEWARD_GITHUB_APP_KEY` (secret) | none | The App's private key in PEM form. Use the `_FILE` form. |
| `SUITEWARD_GITHUB_INSTALLATION_ID` | none | The installation ID, a positive integer. |
| `SUITEWARD_GITHUB_REPOSITORIES` | none | `owner/name` entries, comma-separated, 1 to 20, no duplicates. |
| `SUITEWARD_GITHUB_API_URL` | `https://api.github.com` | The GitHub API address. Keep the default for github.com. It must be `https`. |
| `SUITEWARD_INSTALLATION_TIER` | `co-located` | `isolated` or `co-located`. Set the tier you installed. SuiteWard never claims more than you set. The isolated units set `isolated`. |
| `SUITEWARD_MCP_ADDR` | `127.0.0.1:8090`. The image sets `0.0.0.0:8090` | Address of the MCP listener. It starts only when `SUITEWARD_MCP_TOKEN` is set. |
| `SUITEWARD_MCP_TOKEN` (secret) | none | The MCP bearer token, at least 32 bytes. Use the `_FILE` form. |
| `SUITEWARD_MCP_ALLOWED_ORIGINS` | empty | Browser origins allowed to call MCP, comma-separated. Clients that are not browsers send no origin. |

## 13. Source code

SuiteWard is licensed under `AGPL-3.0-only`. The license text is in the repository's `LICENSE` file. The image carries that text as `/licenses/LICENSE`, and the license of every module it links as `/licenses/THIRD_PARTY_NOTICES`.

`/status` reports the URL of the exact source revision the binary was built from, as `source`. The project chose this as its pointer to the source ([D-RELEASE](plan/decisions.md#d-release-platforms-versions-license-and-notices)). For a release, that is `https://github.com/IgnisDevNE/SuiteWard/tree/vX.Y.Z`. If `source` is absent, the build set none.

`/status` is reachable only over the loopback address of the host, so it does not by itself satisfy section 13 of the AGPL for remote users. If you modify SuiteWard, you must offer the corresponding source of your modified version where your users interact with it, and point `source` at that source. Build the image yourself with the `SOURCE` build argument, alongside `VERSION`:

```
podman build -f deploy/Containerfile --build-arg VERSION=sha-<commit> --build-arg SOURCE=<url of your modified source> -t localhost/suiteward:local .
```

`/licenses` in the built image holds the license and the third-party notices, whatever `SOURCE` you build with.

## 14. Troubleshooting

Start with `/status` and the logs.

- **Isolated host (as `suiteward`):** `curl -s http://127.0.0.1:8081/status`, then `journalctl --user -u suiteward.service -n 100 --no-pager`, for the migration `journalctl --user -u suiteward-migrate.service -n 50 --no-pager`, and for the database `journalctl --user -u suiteward-db.service -n 50 --no-pager`. The migration container is removed when it exits, so `podman logs` cannot show it.
- **Co-located PC (rootless connection selected, step 5.2):** `podman machine ssh --username user "curl -s http://127.0.0.1:8081/status"`, then `podman logs --tail 50 suiteward`. The migration runs in the foreground of `./deploy/local.ps1`, which shows its output.

These are the `/status` fields you read:

- `tier` is the effective tier. `tierNote` appears when the setting and the enrollment disagree. Run setup again (section 6).
- `owner` shows the enrolled login. It is absent until setup succeeds.
- `unconfiguredRepositories` lists the enrolled repositories that the configuration does not name. It is absent when there are none.
- `github` has one entry per repository:
  - `lastSuccess` is the last successful synchronization.
  - `lastError` is the last synchronization error, as short text without secrets. If `waitUntil` is set, GitHub asked SuiteWard to wait because of a rate limit, and SuiteWard resumes by itself. Otherwise, check that the App is installed on that repository, and that the App ID, the installation ID and the key are the ones from section 4.
  - `protection` shows the results of section 7.
  - `checks` is the last error while SuiteWard published that repository's check. It is absent when there is none.
  - `bootstrap` shows why the first contract is waiting or stopped. `read-failed` means GitHub could not be read, and SuiteWard retries. `repository-changed` means the configured name now points at a different repository from the one enrolled, so check the name. `origin-not-passed` means the default branch's contract could not be read as a passing source. `assessment-conflict` means two assessments of one source disagree. `blocked-<reason>` means a block for the stated reason. `failed` means the bootstrap failed for good. Read the logs for the last three. The field is absent when nothing is wrong.
  - `merges` shows merge decisions. `waiting` means GitHub has not yet reported the merge commit, and SuiteWard retries. `read-failed`, `assessment-conflict` and `failed` need the logs.
  - `integrity` is `passed`, `failed` or `unavailable` for the current version. It appears after the first integrity check, which runs when the service starts, right after each promotion and then every hour, so a canonical shows its `integrity` within moments of its promotion. `failed` means SuiteWard fails the check on every pull request of that repository and does not promote until the problem is resolved; it does not block merges by itself — your branch protection does that, if you require the check. `unavailable` changes no check.
- `outbox.failed` counts messages that SuiteWard could not deliver to GitHub after its retries. It should be 0. Read the logs for the repository. A refused write is often a missing App permission, so compare with section 4. If the failed message was a check, SuiteWard sends it again by itself about 15 minutes after the cause is fixed. Until then a pull request can lack its required `SuiteWard / Contract` check. Push a new commit to that pull request to get a new check at once.
- `jobs.discarded` counts background jobs that failed for good. Read the logs.
- `version`, `schemaVersion` and `source` identify the release and the database schema that run. `instance` is the first 8 characters of the instance id, the same as in the name of the authenticator entry that setup creates (`SuiteWard:<login> (<tier> <instance>)`), so you can match an entry to an instance.

Common problems:

- **The service does not start on the isolated host.** Podman refuses to start a container while a secret it names (`suiteward-database-url`, `suiteward-database-owner-url` for the migration, the GitHub key or the MCP token) or `/etc/suiteward/github.env` is missing, and the journal shows which. The service restarts every five seconds until it starts. It also needs the migration to have exited with code 0 (`systemctl --user status suiteward-migrate.service`). A unit that Podman cannot parse is not generated at all. Run the dry run in step 10.10.
- **The service restarts in a loop and its journal says `schema version X, this build requires Y`.** The service refuses a schema that is older than its image, and it never migrates. Someone changed an image and restarted `suiteward.service` alone, or the migration has not run. As `suiteward`, restart the migration unit, which migrates and restarts the service: `systemctl --user restart suiteward-migrate.service`. If the message says the database was migrated by a newer release, the image is older than the schema: use the newer release, or restore a backup (step 11.6). Never start an older image on the database.
- **`suiteward-migrate.service` fails.** Read its journal: `journalctl --user -u suiteward-migrate.service -n 50 --no-pager`. The unit is not retried by itself: the service stays down until you fix the cause and run `systemctl --user start suiteward.service`. The usual causes are these:
  - The database role `suiteward_app` does not exist: create it, as step 10.11 says. A fresh database volume has no role.
  - The message says the service must connect as a least-privilege role: `suiteward-database-url` names a role that owns the schema or a table in it, is a member of the owner, a superuser or has `BYPASSRLS`, for example the owner's URL. Replace the secret by the URL of `suiteward_app` (step 10.11).
  - The message says the database is not reachable after 30 attempts, or that it refused the owner connection: the database is not running or not healthy (`podman ps`), or `suiteward-database-owner-url` holds a password that the database does not have.
  - The message starts with `migrate the database`: a migration failed. Report the message and do not edit the database. The schema is left as the failed migration left it, so restore your backup before you try another image (step 11.6).
- **The service stays down after a power loss.** If PostgreSQL's crash recovery takes longer than the migration's wait, the migration fails closed and the service stays down. Wait until the database accepts connections (`podman ps` shows `suiteward-db` healthy), then run `systemctl --user start suiteward.service`.
- **On a co-located PC, `./deploy/local.ps1` says that the connection is rootful, or asks for `-PodmanConnection`.** The stack must run rootless. Select the machine's rootless connection (step 5.2) and run the script again.
- **`/readyz` answers 503.** The database does not answer, or the schema is not ready. Check that `suiteward-db` is healthy with `podman ps` (on the isolated host, as `suiteward`), and read its logs.
- **The journal says `github commands stopped: the bot account could not be resolved`, with a status of 401.** GitHub refuses the App key that is installed, for example because it was revoked or is not the key you meant. All GitHub calls (reads, checks and comments) stop within about an hour, when the tokens that the service already holds expire. This line is an early signal after a restart, not the first symptom. Follow [Rotating the App key](#rotating-the-app-key), and compare the fingerprints.
- **Setup exits with code 2.** The `--test-check` list does not match the configured repositories. Give exactly one per repository, with no duplicates.
- **Setup exits with code 1.** A lookup failed. The owner login may not be a user account, the App may not be installed on every configured repository, or GitHub could not be reached. Nothing was written.
- **`propose` was refused.** The acknowledgment names the reason: `canonical-present` (the repository already has a contract; `propose` only works before the first one), `fork` (pull requests from forks are not supported), `not-open` (the pull request is no longer open), `base-not-target` (the pull request's base branch is not the repository's default branch, so merging it could never decide the first contract), `declaration-missing` (no `.suiteward.yml` was found on the default branch or the pull request head; see the suggested file and link, section 8), or `invalid-source` (`.suiteward.yml` is invalid, a symbolic link that the scope reaches points at a directory, outside the repository or nowhere (section 8), or GitHub truncated the tree, which happens with very large monorepos and cannot be protected in this release).
- **A command gets no reply.** Check that the first line starts with `/suiteward`, outside a code block, that it comes from your account, that you did not edit it, and that the pull request is still open. Another account's command gets no reply by design.
- **A repository gets no reply and no checks,** `/status` lists it in `unconfiguredRepositories`, and the log says `github commands skip an enrolled repository that is not configured in GITHUB_REPOSITORIES`. A repository you removed from the configuration, or deleted on GitHub, is still enrolled, and SuiteWard skips it. See [Stop protecting a repository](#stop-protecting-a-repository), or add it back to the configuration.
- **An approval is rejected.** The reply gives the reason: an invalid, replayed or expired code, or an unknown revision. Use a fresh code, and check the reference against the newest revision comment.
- **The check shows `approval-missing` after a push.** The push changed a protected file, which creates a new revision. Approve the revision named in the newest comment.
- **The new image does not start after an update.** Do not start an older image, because after a migration it refuses to run. Read the logs of the migration and of the service and report them to the project. To return to the earlier state, roll back (step 11.6): restore your backup, as described in [the backup runbook](backup.md).
