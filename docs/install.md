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

- **Rootless Podman.** The units for the isolated host were checked with Podman 4.9.3. Section 10 checks your version.
- **Co-located PC:** Windows with PowerShell 7.2 or later, run as `pwsh` (not Windows PowerShell 5.1), and a Podman machine. The co-located path (section 5) is Windows only: it runs `deploy/local.ps1`.
- **Isolated host:** Linux with systemd user services (checked on Ubuntu 24.04), and an unprivileged account for SuiteWard. This guide calls it `suiteward`. Each step on this host is marked "as `suiteward`" or "as the administrator" (the steps with `sudo`). Use administrator rights only for the steps marked "as the administrator".
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

**5.2 Pull the release image.** Run:

```
podman pull ghcr.io/ignisdevne/suiteward:vX.Y.Z
```

Expect a digest line.

**5.3 Start the stack.** The script creates the network, the volumes and the secrets. It generates the database password and the MCP token and never prints them. Pass the App key file only the first time; the three GitHub values are needed on every run that should keep GitHub:

```
./deploy/local.ps1 -Image ghcr.io/ignisdevne/suiteward:vX.Y.Z -GitHubAppKeyFile <path to the PEM> -GitHubAppId <app id> -GitHubInstallationId <installation id> -GitHubRepositories '<owner/name>,<owner/name>'
```

Expect the line `Started suiteward-db and suiteward (ghcr.io/ignisdevne/suiteward:vX.Y.Z), GitHub configured.`

Notes:

- Always pass `-Image` with your pinned release. The script has its own default image, and that default is not a release.
- The script reads `-GitHubAppKeyFile` only while the secret `suiteward-github-app-key` does not exist yet. Delete your copy of the `.pem` file after the first run.
- Keep the quotes around `-GitHubRepositories`. Without them, PowerShell reads a comma-separated list as an array and joins it with spaces, and the service then refuses to start with `SUITEWARD_GITHUB_REPOSITORIES: "… …" is not an owner/name pair`.
- The script stores none of the three GitHub values. A run without `-GitHubAppId`, `-GitHubInstallationId` and `-GitHubRepositories` replaces the container with one without GitHub. The state stays.
- The MCP listener serves a local agent's `sync` and `status` tools, nothing else. Its token copy is in `%LOCALAPPDATA%\SuiteWard\mcp-token`, readable by your Windows user only.
- The names and the ports are fixed: HTTP on 8081, MCP on 8091. Run one stack per machine.

**5.4 Check the service through the Podman machine.** The published port may not be reachable from Windows itself, so read it inside the machine. Run:

```
podman machine ssh "curl -s http://127.0.0.1:8081/readyz"
```

Expect `{"schemaVersion":N,"status":"ready"}`.

```
podman machine ssh "curl -s http://127.0.0.1:8081/status"
```

Expect `"tier":"co-located"` in the output.

**5.5 Run setup.** Section 6 covers it.

To stop the stack and keep its state, run `./deploy/local.ps1 -Down`. Expect the two containers to be removed while the volumes and secrets stay. Start again with the command of step 5.3; the key file is not needed again.

`./deploy/local.ps1 -Down -Purge` also removes the database, the artifact volume, the secrets, the MCP token copy and the network. That deletes your TOTP enrollment and every contract version. Use it only to start over, and back up first (section 11, step 11.1, and [the backup runbook](backup.md)).

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

Setup then works through these steps:

1. It looks up your login, which must be a user account, and every configured repository. If one lookup fails, it exits with code 1 before it writes anything.
2. It prints a warning and waits. Press Enter to continue. End of input or Ctrl-C exits with code 1, before any secret is created.
3. It prints the `otpauth://` URI and the base32 secret, once. Scan the URI with your authenticator, or type the secret in.
4. It asks for the current 6-digit code. You get three attempts. Three wrong codes exit with code 1 and write nothing.
5. It stores the owner and the secret, records each repository's test check, and checks protection for each repository (section 7).

A missing, extra or duplicate `--test-check` is a usage error: exit code 2, and nothing is written. Setup exits with code 0 once enrollment succeeded, whatever the protection results.

Afterwards, check the result. On the co-located PC, use the `podman machine ssh` form from section 5.4. On the isolated host, as `suiteward`, run `curl -s http://127.0.0.1:8081/status`. The output shows `owner` with your login and the `protection` result of each repository.

Only a setup that prints the protection result of each repository and exits with code 0 counts. Confirm it with `/status`: `owner.enrolledAt` changes on every successful run, so a value that did not change means that setup did not complete. An interrupted setup can leave an authenticator entry that matches nothing: delete it.

Running setup again replaces the TOTP secret. Delete the old authenticator entry and add the new secret. Never paste the setup output, the URI, the secret or a code into a chat, an issue or a pull request.

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

- If a protected file changes after the proposal, SuiteWard cancels the proposal and comments. Post `/suiteward propose` again.
- Pull requests from forks get no proposal. Their check shows `fork-unsupported`, because forks are not supported in this release.
- SuiteWard cannot read a repository whose tree GitHub truncates, which happens with very large monorepos. `propose` refuses it as `invalid-source`, the bootstrap job stops at `origin-not-passed`, and the check of a pull request shows `source-unavailable` (section 9). Such a repository cannot be protected in this release.
- Reading a very large test suite can take longer than the job timeout. On the isolated host, raise `SUITEWARD_JOB_TIMEOUT` (section 12). `deploy/local.ps1` has no parameter for it, so the co-located stack keeps the default: about 150 protected files read per minute of the job timeout. For a larger test suite, use the isolated host (section 10) instead.

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
| failure, `invalid-source` | `.suiteward.yml` is invalid, or a symbolic link that the scope reaches points at a directory, outside the repository or nowhere, or the scope covers the link itself (section 8). Fix it. A submodule never causes this: its contents are not protected. |
| failure, `integrity-failed` | A canonical artifact no longer matches its integrity record. SuiteWard fails the check on every pull request of that repository and does not promote; it does not block merges by itself — your branch protection does that, if you require the check. Restore the artifact from a trusted copy or a backup, never edit the database, and restart the service: it re-verifies at once. On the isolated host, use the restart command of step 11.3. On the co-located PC, run the `./deploy/local.ps1` command of step 5.3 again, with the same `-Image` and the three GitHub parameters. |
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
- A declaration that cannot be shown, because it is larger than 8192 bytes or holds a character you cannot see (a zero-width or bidirectional control character, for example), is never put in a comment. The comment then lists the new patterns alone, as lines to copy under the `include` and `runner` lists, with the same link to the editor. This holds for the notice, for the reply of `/suiteward suggest` and for the runner comment.

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

A repository stays enrolled until setup runs again without it. While it is enrolled but missing from the configuration, SuiteWard skips it: nothing is synchronized or decided there, and the commands of the other repositories keep working. `/status` and the MCP `status` tool name it in `unconfiguredRepositories`, and the log warns once. Deleting the repository on GitHub has the same effect.
1. Remove the repository from the list: `SUITEWARD_GITHUB_REPOSITORIES` in `/etc/suiteward/github.env` on an isolated host (then restart the service), or `-GitHubRepositories` when you run `deploy/local.ps1` again on a co-located PC.
2. Run setup again (section 6) with one `--test-check` for each repository you keep. Setup replaces the TOTP secret, so add the new entry to your authenticator app and delete the old one.
3. Comment your command again. A command written before the new setup is ignored and gets no reply.

SuiteWard changes nothing on GitHub: remove the App from the repository, and the ruleset rule that requires `SuiteWard / Contract`, yourself.

## 10. Running on a separate host (operators)

This installs the isolated tier (section 2), unless step 10.9 sets the tier to `co-located`: SuiteWard on a separate host that your coding agent cannot reach, for whoever self-hosts on a machine the agent cannot reach. It gives the full assurance. The host runs Podman 4.9.3 and the units from `deploy/quadlet/` in the release source.

**10.1 Enable linger, then open a shell as `suiteward`.** As the administrator, let the account's services start at boot:

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

**10.3 Create the database password and its two Podman secrets (as `suiteward`).** One generated password feeds both secrets, so you never see or type it. The chain stops at the first failure:

```
pw=$(openssl rand -hex 24) &&
h='suiteward-db:5432/suiteward?sslmode=disable' &&
printf '%s' "$pw" |
  podman secret create suiteward-db-password - &&
printf 'postgres://suiteward:%s@%s' "$pw" "$h" |
  podman secret create suiteward-database-url -
unset pw h
```

Then list the secrets:

```
podman secret ls
```

Expect `suiteward-db-password` and `suiteward-database-url` in the list.

**10.4 Check the shape of both values without showing them (as `suiteward`).** Each line prints a count, and both must print `1`:

```
s='{{.SecretData}}'
p='[0-9a-f]{48}'
podman secret inspect --showsecret --format "$s" \
  suiteward-db-password | grep -Ecx "$p"
h='@suiteward-db:5432/suiteward[?]sslmode=disable'
podman secret inspect --showsecret --format "$s" \
  suiteward-database-url |
  grep -Ecx "postgres://suiteward:$p$h"
unset s p h
```

A `0` means a malformed value. Remove both secrets with `podman secret rm suiteward-db-password suiteward-database-url`, then repeat step 10.3. Nothing has started yet. After the first start the database keeps this password in its volume, so replacing a secret later locks the service out.

**10.5 Refuse the metadata address for the service account.** This is the example for a cloud VM. The instance metadata service at `169.254.169.254` hands out the instance's credentials. Rootless containers connect to the network as a process of the service account, so a rule for that account covers them. If your host has no metadata service, skip this step.

As `suiteward`, check the address before the rule exists. Expect `200`, which shows the check can see the problem:

```
podman run --rm docker.io/curlimages/curl@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777 -s -m 3 -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' -o /dev/null -w '%{http_code}\n' http://169.254.169.254/latest/api/token
```

As the administrator (`exit` the `suiteward` shell first): install `nftables` if it is missing (Ubuntu):

```
command -v nft || sudo apt-get install -y nftables
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

Open a shell as `suiteward` again (step 10.1) and run the same check as before. It must now print `000`, confirming the service account is refused at that address:

```
podman run --rm docker.io/curlimages/curl@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777 -s -m 3 -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' -o /dev/null -w '%{http_code}\n' http://169.254.169.254/latest/api/token
```

Step 10.15 checks that GitHub stays reachable. If the table disappears, for example after a `flush ruleset` in `/etc/nftables.conf`, load it again with `sudo systemctl restart suiteward-imds-block.service`.

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

**10.9 Install the five unit files (as `suiteward`).** Create the directory:

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
cat > ~/.config/containers/systemd/suiteward.container
```

After you paste `suiteward.container`, make two edits. Set its `Image=` line to `Image=ghcr.io/ignisdevne/suiteward:vX.Y.Z`, and delete the `AutoUpdate=registry` line. Keep `Environment=SUITEWARD_INSTALLATION_TIER=isolated` only if your coding agent cannot reach this account. Otherwise set it to `co-located` ([ADR 0027](decisions/0027-agent-human-trust-separation.md)). The database unit stays as it is: its image is pinned by digest. To pin SuiteWard the same way, which is safer because a tag can be rewritten and a digest cannot, use `Image=ghcr.io/ignisdevne/suiteward@sha256:<digest>`. Read the digest after pulling the release tag, with `podman image inspect --format '{{.Digest}}' ghcr.io/ignisdevne/suiteward:vX.Y.Z`.

**10.10 Check that Podman reads every unit (as `suiteward`).** Check the Podman version, then run the generator's dry run:

```
podman --version
```

```
/usr/lib/systemd/system-generators/podman-system-generator --user --dryrun
```

Expect the version (4.9.3 is the checked version) and every generated unit listed with no error. Podman skips a unit it cannot parse, and you would only see "unit not found" later. If the dry run reports an error, stop and fix that line first. Do not guess at a fix.

**10.11 Pull the two images (as `suiteward`).** Pull the database image by its digest, then the release image:

```
podman pull docker.io/library/postgres:18.6-trixie@sha256:5a5a84b19854a9ffaa54082c166ff4ec27473a361e496e5ea167f298f2da9722
```

```
podman pull ghcr.io/ignisdevne/suiteward:vX.Y.Z
```

Expect a digest line for each pull.

**10.12 Load the units and start the service (as `suiteward`).** Run:

```
systemctl --user daemon-reload
```

```
systemctl --user start suiteward.service
```

Expect no output. On the very first start the service can fail while the database initialises. Systemd restarts it every five seconds, which is expected.

**10.13 Check both units (as `suiteward`).** Run:

```
systemctl --user status suiteward-db.service suiteward.service --no-pager
```

```
podman ps
```

Expect both units active, and `suiteward-db` listed as `(healthy)`.

**10.14 Check what the host applies (as `suiteward`).** Run:

```
podman info --format '{{.Host.NetworkBackend}}'
```

Expect `netavark`. Then run:

```
podman inspect suiteward-db --format '{{.HostConfig.Memory}}'
```

Expect `402653184` (384 MiB). If either value differs, stop and check the Podman setup before you go on.

**10.15 Check that GitHub is reachable from the service network (as `suiteward`).** Run:

```
podman run --rm --network suiteward docker.io/curlimages/curl@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777 -s -m 10 -o /dev/null -w '%{http_code}\n' https://api.github.com/zen
```

Expect `200`. `403` means the shared unauthenticated GitHub rate limit is used up: wait, then run it again. `000` means the host cannot reach GitHub, so fix the network path before you go on.

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

Pin one release, and update when you choose. Updates are manual unless you opt in, as described below.

**11.1 Back up first.** The service runs database migrations when it starts. After a migration, an older image refuses to start, and there is no rollback to an older image. See [the backup runbook](backup.md). The backup holds the TOTP secret and the enrollment, so store it as a secret.

**11.2 Pull the new release image.** Use the new `vX.Y.Z`. On the isolated host, run it as `suiteward`:

```
podman pull ghcr.io/ignisdevne/suiteward:vX.Y.Z
```

**11.3 Update an isolated host.** Read the release notes first. Compare the new release's `deploy/quadlet/*` files with the units installed in `~/.config/containers/systemd/`, and carry over anything the new release adds: a new secret (as `suiteward`), a new entry in `/etc/suiteward/github.env` (as the administrator), or a new setting in a unit file (as `suiteward`). Then re-apply your two edits to the new `suiteward.container` before you install it: set its `Image=` line to the new tag, and delete its `AutoUpdate=registry` line (unless you opted into automatic updates below). As `suiteward`, reload the units and restart the service:

```
systemctl --user daemon-reload
```

```
systemctl --user restart suiteward.service
```

Expect `/readyz` to answer `ready` again (section 10, step 10.16).

**11.4 Update a co-located PC.** Pull the image (step 11.2), then run `./deploy/local.ps1` with the new `-Image` value and the same three GitHub parameters as before (section 5, step 5.3). A run without them removes GitHub from the stack.

If the new image fails to start, do not start an older image. Read the logs (section 14).

**Automatic updates (opt-in).** Podman can follow the newest stable release. To opt in, as `suiteward`, set `Image=ghcr.io/ignisdevne/suiteward:latest-stable` and `AutoUpdate=registry` in `suiteward.container`, reload the units, and enable the timer:

```
systemctl --user enable --now podman-auto-update.timer
```

The timer checks once a day by default. `latest-stable` moves only for a release that is not a pre-release. A migration in such a release still has no rollback, so keep your backup current.

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

- Co-located PC: run the command of step 5.3 again, with the same `-Image` and the three GitHub parameters, and with `-GitHubAppKeyFile <path of the new PEM>`. The script finds no secret, creates it from that file and starts the stack. Then delete the file with `Remove-Item <path of the new PEM>`.

**R.7 Check the secret's fingerprint again.** It must be the same text as in R.1.

- Isolated host, as `suiteward`:

  ```
  podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-github-app-key | openssl rsa -pubout -outform DER 2>/dev/null | openssl dgst -sha256 -binary | openssl enc -base64
  ```

- Co-located PC, in Git Bash. The Podman machine has no OpenSSL, so the secret goes out to the PC's:

  ```
  podman machine ssh "podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-github-app-key" | openssl rsa -pubout -outform DER 2>/dev/null | openssl dgst -sha256 -binary | openssl enc -base64
  ```

**R.8 Confirm that GitHub accepts the key.** The service starts without a cached token, so its first reads already use the new key. Wait about a minute, and read `/status` twice, a minute apart (section 14 shows how). The `lastSuccess` of each repository must advance, and no repository may show a `lastError`. That is the proof. The log line `bot account could not be resolved` appears only when SuiteWard processes an owner command, so its absence proves nothing. If you do see it, the key is wrong. This prints the lines it finds:

- Isolated host, as `suiteward`:

  ```
  journalctl --user -u suiteward.service --since '10 minutes ago' --no-pager | grep 'bot account could not be resolved'
  ```

- Co-located PC:

  ```
  podman machine ssh "podman logs --since 10m suiteward 2>&1 | grep 'bot account could not be resolved'"
  ```

**R.9 Delete the old key.** Only now, on the App's settings page, delete the old private key. Then delete every other copy of a `.pem` file, including the one GitHub downloaded in R.1.

If R.5 or R.7 shows a different fingerprint, or R.8 shows no advance or a `lastError`, do not delete the old key. Stop the service, and repeat from R.3.

## 12. Configuration reference

Every setting is an environment variable named `SUITEWARD_<NAME>`. A secret can instead be given as `SUITEWARD_<NAME>_FILE`, the path of a file whose content is the value. Give one form, not both. A variable that is set but empty counts as unset.

Where you set them:

- **Isolated host:** in the `Environment=` and `Secret=` lines of `suiteward.container` (as `suiteward`), and in `/etc/suiteward/github.env` for the three GitHub values (as the administrator). After an edit, run `systemctl --user daemon-reload` and `systemctl --user restart suiteward.service` as `suiteward`.
- **Co-located PC:** `deploy/local.ps1` has parameters only for the image and the GitHub values. The other settings keep their defaults on this PC. Changing one would mean changing the script, which this guide does not cover.

| Setting | Default | Meaning |
| --- | --- | --- |
| `SUITEWARD_DATABASE_URL` (secret, required) | none | The PostgreSQL URL. Use the `_FILE` form. The isolated units mount it from a Podman secret. |
| `SUITEWARD_ARTIFACT_DIR` (required) | none. The image sets `/data/artifacts` | Absolute path of the artifact store. |
| `SUITEWARD_HTTP_ADDR` | `127.0.0.1:8080`. The image sets `0.0.0.0:8080` | Address of the HTTP listener. The units publish it on the host's loopback only. |
| `SUITEWARD_SHUTDOWN_TIMEOUT` | `30s` (1 s to 10 min) | Time allowed for a graceful stop. The container's stop timeout must exceed this by at least 5 s. The units set 45 s, which covers the default. A value of 40 s or more needs a larger stop timeout. |
| `SUITEWARD_LOG_LEVEL` | `info` | One of `debug`, `info`, `warn`, `error`. |
| `SUITEWARD_JOB_WORKERS` | `4` (1 to 64) | Number of background workers. |
| `SUITEWARD_JOB_TIMEOUT` | `1m` (1 s to 1 h) | Time limit of one attempt of a background job. A `propose` reads about 150 protected files per minute of this value, so raise it for a large test suite. Isolated host only: `deploy/local.ps1` has no parameter for it, so the co-located stack always keeps the default (section 8). |
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

- **Isolated host (as `suiteward`):** `curl -s http://127.0.0.1:8081/status`, then `journalctl --user -u suiteward.service -n 100 --no-pager`, and for the database `journalctl --user -u suiteward-db.service -n 50 --no-pager`.
- **Co-located PC:** `podman machine ssh "curl -s http://127.0.0.1:8081/status"`, then `podman logs --tail 50 suiteward`.

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
  - `integrity` is `passed`, `failed` or `unavailable` for the current version. It appears after the first integrity check, which runs when the service starts and then every hour, so a canonical created after the start shows no `integrity` for up to an hour: wait, or restart the service. `failed` means SuiteWard fails the check on every pull request of that repository and does not promote until the problem is resolved; it does not block merges by itself — your branch protection does that, if you require the check. `unavailable` changes no check.
- `outbox.failed` counts messages that SuiteWard could not deliver to GitHub after its retries. It should be 0. Read the logs for the repository. A refused write is often a missing App permission, so compare with section 4. If the failed message was a check, SuiteWard sends it again by itself about 15 minutes after the cause is fixed. Until then a pull request can lack its required `SuiteWard / Contract` check. Push a new commit to that pull request to get a new check at once.
- `jobs.discarded` counts background jobs that failed for good. Read the logs.
- `version`, `schemaVersion` and `source` identify the release and the database schema that run.

Common problems:

- **The service does not start on the isolated host.** Podman refuses to start the container while a secret or `/etc/suiteward/github.env` is missing, and the journal shows which. The service restarts every five seconds until it starts. A unit that Podman cannot parse is not generated at all. Run the dry run in step 10.10.
- **`/readyz` answers 503.** The database does not answer, or the schema is not ready. Check that `suiteward-db` is healthy with `podman ps` (on the isolated host, as `suiteward`), and read its logs.
- **The journal says `github commands stopped: the bot account could not be resolved`, with a status of 401.** GitHub refuses the App key that is installed, for example because it was revoked or is not the key you meant. All GitHub calls (reads, checks and comments) stop within about an hour, when the tokens that the service already holds expire. This line is an early signal after a restart, not the first symptom. Follow [Rotating the App key](#rotating-the-app-key), and compare the fingerprints.
- **Setup exits with code 2.** The `--test-check` list does not match the configured repositories. Give exactly one per repository, with no duplicates.
- **Setup exits with code 1.** A lookup failed. The owner login may not be a user account, the App may not be installed on every configured repository, or GitHub could not be reached. Nothing was written.
- **`propose` was refused.** The acknowledgment names the reason: `canonical-present` (the repository already has a contract; `propose` only works before the first one), `fork` (pull requests from forks are not supported), `not-open` (the pull request is no longer open), `base-not-target` (the pull request's base branch is not the repository's default branch, so merging it could never decide the first contract), `declaration-missing` (no `.suiteward.yml` was found on the default branch or the pull request head; see the suggested file and link, section 8), or `invalid-source` (`.suiteward.yml` is invalid, a symbolic link that the scope reaches points at a directory, outside the repository or nowhere (section 8), or GitHub truncated the tree, which happens with very large monorepos and cannot be protected in this release).
- **A command gets no reply.** Check that the first line starts with `/suiteward`, outside a code block, that it comes from your account, that you did not edit it, and that the pull request is still open. Another account's command gets no reply by design.
- **A repository gets no reply and no checks,** `/status` lists it in `unconfiguredRepositories`, and the log says `github commands skip an enrolled repository that is not configured in GITHUB_REPOSITORIES`. A repository you removed from the configuration, or deleted on GitHub, is still enrolled, and SuiteWard skips it. See [Stop protecting a repository](#stop-protecting-a-repository), or add it back to the configuration.
- **An approval is rejected.** The reply gives the reason: an invalid, replayed or expired code, or an unknown revision. Use a fresh code, and check the reference against the newest revision comment.
- **The check shows `approval-missing` after a push.** The push changed a protected file, which creates a new revision. Approve the revision named in the newest comment.
- **The new image does not start after an update.** Do not start an older image, because after a migration it refuses to run. Read the logs and report them to the project. To return to the earlier state, restore your backup, as described in [the backup runbook](backup.md).
