# Installing SuiteWard

This guide installs SuiteWard with its installer, the `suiteward` binary: one guided command on your own machine (co-located) or two on a separate host (isolated). The installer creates the stack, creates the GitHub App, writes the ruleset for you to import, and runs the owner setup. After that you only approve the pull requests that change protected test files. For the short path, see the [quickstart](quickstart.md). If a coding agent helps you install, give it [agent-install.md](agent-install.md) first.

Two rules apply to every command:

- Pin one release. Replace `<tag>` with the release you install, everywhere it appears (the first release that has the installer is `v0.3.0-alpha.1`). Do not use a moving tag. Where a command names the image index digest of the release, replace `<index>` with it. The release notes name it, on the line `Index digest:`.
- Replace each value in angle brackets, such as `<owner/name>`, with your own value. Never put a secret, a token or a TOTP code into a file, a log, a pull request or a chat.

## 1. What SuiteWard does and does not do

SuiteWard keeps the `SuiteWard / Contract` check of a pull request that changes the protected test files pending until the owner approves that exact revision, and your ruleset (section 7) blocks the merge: SuiteWard does not block it by itself. When the pull request merges, SuiteWard checks the approval and the integrity of the protected files at the merge commit, and promotes the approved contract as the new canonical version.

SuiteWard does not run your tests, and it does not claim that your CI ran them. A merge is not proof that the tests ran, passed or are correct. Your review and your own test check decide that. The full statement is in [ADR 0014](decisions/0014-merge-triggered-canonical-promotion.md).

## 2. Choose a tier

The tier says where SuiteWard runs compared with your coding agent. The way you install SuiteWard declares the tier. SuiteWard never detects it, and it reports the tier in `/status` and in the check summary.

- **Co-located:** SuiteWard runs on the same machine as the coding agent. The App key, the database and the TOTP secret are then reachable by the agent. SuiteWard states this reduced assurance and does not claim protection it cannot provide. Section 5 installs it with one command, the path most users follow.
- **Isolated:** SuiteWard runs on a separate host that the coding agent cannot reach, or in a VM or under another OS user when the agent has no access to the container runtime and the keyring. This tier gives the full assurance. Section 10 installs it, for whoever self-hosts on a machine the agent cannot reach.

Neither tier protects against a compromised host administrator. The coding agent must use its own GitHub identity, never your credentials. [ADR 0027](decisions/0027-agent-human-trust-separation.md) gives the reasoning.

Never let two SuiteWard instances watch the same repository; one instance serves up to 20. Two instances post as the same App, and each has its own TOTP secret. One of them then rejects the owner's codes and publishes a failing check.

## 3. Before you start

- **A GitHub account** that owns or administers the repositories, and up to 20 repositories to protect. The account that owns the instance must be a user account, not an organization or a bot. The App can belong to your personal account or to an organization you administer.
- **GitHub Free and private repositories.** GitHub does not enforce rulesets on a private repository of an account or organization on the Free plan. SuiteWard cannot protect such a repository: `/status` and `suiteward doctor` report `enforcement-unavailable`. Use a public repository, or a paid plan (GitHub Team). The installer warns before the ruleset step and continues.
- **An authenticator app** that scans an `otpauth://` URI or accepts a base32 secret.
- **A container runtime.**
  - **Podman, rootless, is strongly recommended**, and is the only runtime of the isolated tier. A rootful Podman connection is refused.
  - **Docker is allowed on the co-located tier only, and discouraged.** Its daemon is rootful by default, and Docker has no secret store outside Swarm, so the installer keeps the secrets in named volumes that anyone who controls the Docker daemon can read. Podman keeps them in its own rootless secret store. Section 5 says how to choose Docker anyway.
- **A real terminal.** The installer prompts, and setup prints your TOTP secret there. A pipe, a script or a coding agent cannot run setup.
- **Co-located PC:** Windows with a Podman machine that has a rootless connection (`podman machine init`, then `podman machine start`), or Docker Desktop. Co-located on Windows is the path validated live. macOS and Linux are covered by build and unit tests with a fake runtime only: expect rough edges there. PowerShell 7.2 or later and cosign 3.x are needed only to verify the download (section 4).
- **Isolated host:** AlmaLinux 9 (EL9) with systemd user services. Other distributions are not covered. `sudo suiteward install host` checks Podman 5.5 or later with the fix for CVE-2026-57231 and offers to install it (section 10).

## 4. Get and verify the installer

The installer is a release asset. Each binary embeds the signed image index digest of its own release, so the co-located install never takes an image from a tag. Verify the download once, before you run it.

The release page `https://github.com/IgnisDevNE/SuiteWard/releases/tag/<tag>` lists these files:

| File | What it is |
| --- | --- |
| `suiteward_<tag>_linux_amd64`, `suiteward_<tag>_linux_arm64`, `suiteward_<tag>_windows_amd64.exe`, `suiteward_<tag>_darwin_arm64` | The installer for each platform. |
| `SHA256SUMS` | The SHA-256 of the four binaries. |
| `SHA256SUMS.sigstore.json` and `<binary>.sigstore.json` | The keyless cosign signatures of `SHA256SUMS` and of each binary, made by the release workflow. |

Download `SHA256SUMS`, `SHA256SUMS.sigstore.json` and the binary of your platform into one folder. Get the release source of `<tag>` as well (clone the repository and check out the tag, or download its source): it holds `scripts/verify-release.ps1`. On a machine with PowerShell 7.2 or later and cosign 3.x, run from the release source:

```
pwsh ./scripts/verify-release.ps1 -Blob <folder>/SHA256SUMS -Bundle <folder>/SHA256SUMS.sigstore.json
```

Expect `Verified: … was signed by the SuiteWard release workflow.` Then check that the binary is the one `SHA256SUMS` lists:

```
pwsh ./scripts/verify-release.ps1 -Sums <folder>/SHA256SUMS -File <folder>/suiteward_<tag>_<os>_<arch>
```

Expect `Matched: … is listed in … with its SHA-256.` If either command fails, do not run the binary. The script fixes the signing identity (`release.yml` of this repository at a release tag, through GitHub's OIDC issuer) and refuses any other. A signature proves that the file came from the release workflow, not that it is the version you meant: you chose the tag.

Rename the binary to `suiteward` (`suiteward.exe` on Windows), keep it somewhere only you can change, and put it on your PATH. This guide writes `suiteward` for it. The binary is the trust root of the install: it creates the secrets and the App, so verify it once and run no other copy. A development build has no embedded digest and needs `--image`.

For the isolated host, verify on your PC as above, then copy the binary and the verified `SHA256SUMS` to the host and compare them there. The host needs neither cosign nor PowerShell:

```
sha256sum --check --ignore-missing SHA256SUMS
```

Expect `suiteward_<tag>_linux_<arch>: OK`. `--ignore-missing` is needed because `SHA256SUMS` also lists the binaries of the other platforms, and the file keeps the name it had on the release page. Then copy the binary to a shared place such as `/usr/local/bin/suiteward`: the service account must be able to run it, and a path inside a home directory is not readable by it.

If your remote shell cannot copy files, download the binary and `SHA256SUMS` on the host with `curl` from the release page. Then print the SHA-256 of `SHA256SUMS` on your PC, after you verified its signature (`(Get-FileHash SHA256SUMS -Algorithm SHA256).Hash` in PowerShell), and check that `sha256sum SHA256SUMS` on the host prints the same hash (in lowercase) before you run the check above. [The host runbook](../deploy/HOST.md) shows the commands.

## 5. Install co-located

Run this on the PC that also runs your coding agent. Read section 2 first: on this tier the agent can reach the App key, the database and the TOTP secret. If a coding agent helps you, read [agent-install.md](agent-install.md) first: the agent never runs the install with GitHub, and never runs setup.

**5.1 Start the runtime.** On Windows, the Podman machine must be running (`podman machine start`). The installer picks the machine's only rootless connection, refuses a rootful one, and never changes your Podman default connection. With several machines, choose one with `--connection <name>`. To see the connections, run `podman system connection list`: the rootless one has `/run/user/<number>/podman/podman.sock` in its URI. The installer records its choice in its settings file, so `suiteward doctor` and a re-run use the same one. Any `podman` command you run by hand against this stack must reach the same store, so run `$env:CONTAINER_CONNECTION = '<name>'` first in that PowerShell session.

**5.2 Run the installer.**

```
suiteward install
```

It asks only what it cannot decide, and each question also has a flag and an answers-file key (section 12). It works in this order and changes nothing until the checks pass:

1. **Choices.** The tier (`co-located`), the runtime (`podman`), the HTTP port (default 8081) and the MCP port (default 8091), both on 127.0.0.1, and the release image. The image defaults to the one already installed, else to the release the binary carries. It prints `Using <engine> <version> (rootless).`, or `(a rootful daemon)` for Docker: it names the engine it is about to use.
2. **Checks.** Both ports are free (inside the Podman machine on Windows), no stranger's container uses the stack's names, and the database secrets are consistent. It pulls the two images by digest before it removes anything.
3. **GitHub** (section 5.3), if you say yes.
4. **The stack** (section 5.4).
5. **Setup** (section 6), and a final `suiteward doctor` hint.

A stack that the PowerShell start script of v0.2.0 and earlier made is adopted under the same names. Any difference the installer cannot carry over stops the install and prints the diff, so nothing is overwritten silently. A second run creates only what is missing.

**5.3 The GitHub App.** The installer asks `Set up GitHub now (creates a GitHub App in your browser)?`. Say yes. Every step below runs on this machine:

1. **Create the App.** The installer prints an address on 127.0.0.1, `http://127.0.0.1:<port>/`. Open it in the browser of this machine. It sends the App manifest to GitHub. GitHub asks you to confirm: one click creates the App. It belongs to your personal account, or to an organization when you answer `--github-account <login>` (the default is `personal`). The App gets exactly five permissions: Metadata, Contents and Administration read-only, Pull requests and Checks read and write. It has no webhook, it is private (`public: false`), and it is named `suiteward-<random>` unless you rename it. Note the name: if the install cannot finish, you delete that App by hand.
2. **The code.** GitHub redirects your browser to the listener with a one-time code and a random `state` that the installer checks. The installer converts the code into the App's private key on this machine, and writes the key straight into the secret store, never to disk and never to the terminal. The code passes through your browser, as a downloaded key would: it is valid for one hour, and the installer never prints it.
3. **If the browser cannot reach the listener,** the address bar of the error page still shows the full redirect address. Paste that address, or only its `code=` value, into the terminal and press Enter. `--manifest-port <port>` fixes the listener's port (default: any free port). `--manifest-code <code>` hands over a code from an earlier attempt, within its hour.
4. **Install the App.** The installer prints `https://github.com/apps/<slug>/installations/new`. Open it, choose the account and give the App access to the repositories to protect, and no others. The installer polls every 5 seconds for up to 10 minutes. It lists the repositories the App can reach and asks which to protect: numbers or `owner/name`, at most 20, all of one owner.
5. **The owner.** The GitHub login that enrolls (default: the App's account when it is a user account).
6. **The test check.** For each repository, the installer offers the check runs on the head of the default branch, and you choose one or type the name your CI publishes. It never offers a check from a ruleset that does not exist yet. When the repository has no check runs yet, type the name: `suiteward doctor` and `/status` report `missing-test-check` until a check with that name exists. `--test-check 'owner/name=CI / test'` answers it, once for each repository, or give one plain name for a single repository.
7. **The ruleset.** For each repository the installer writes a JSON file, `suiteward-ruleset-<owner>-<name>.json`, into the folder of its settings file (`%APPDATA%\suiteward` on Windows, `~/.config/suiteward` on Linux), and prints its path. It requires `SuiteWard / Contract` with your App as the source, and your test check, on the default branch, with branches up to date and an empty bypass list. You import it yourself, because the App has no permission to change rules: open `https://github.com/<owner>/<repo>/settings/rules`, choose **New ruleset**, then **Import a ruleset**, select the file and create it. For a private repository, the installer first warns that GitHub Free does not enforce rules there (section 3); the install continues. Press Enter when every ruleset is imported. The installer then reads the protection back and prints the result of each repository (section 7).

To use an App you already have, give `--github-app-id`, `--github-installation-id`, `--github-owner` and `--github-repositories`, and the key as `--github-app-key-file <path>` (or `-` for standard input). Create such an App by the checklist in section 10, step 10.2. `--github no` (or `--github-app-id none`) skips GitHub: the stack then runs without it, and a later run with `--github yes` sets it up. Any GitHub value given without an App is refused.

**5.4 The stack.** The installer creates the network and the volumes, generates the database owner password, the application role's password and the MCP token, and starts the stack in this order: the database (it waits up to 90 seconds for it to be healthy), the application role `suiteward_app`, the one-shot `suiteward migrate`, then the service, only when migrate succeeded. The service connects as `suiteward_app`, which cannot change the schema; only the migrate container holds the owner's URL. Secrets are written through standard input and never printed. Expect `suiteward migrate finished: the schema is up to date.`, then `Started suiteward-db and suiteward (<image>), GitHub configured.`

The installer also writes a copy of the MCP token for a local agent's client, readable by your user only: `%LOCALAPPDATA%\SuiteWard\mcp-token` on Windows, `suiteward/mcp-token` in your configuration directory elsewhere (`--token-file` changes it). It prints the path, never the token. The MCP listener serves a local agent's `sync` and `status` tools, nothing else.

**5.5 After a restart.** On Podman, the installer enables `podman-restart.service` for the machine's rootless user, so the containers start whenever the machine starts. It does not start the machine itself: after Windows restarts, run `podman machine start`. Starting the machine at logon is not part of this release. `suiteward doctor` reports a stopped machine. On Docker, the containers restart when Docker Desktop starts.

**5.6 Setup.** On a real terminal, the installer runs setup for you, as section 6 describes, after it told you to turn off session recording. Without a real terminal, it prints the command to run yourself and stops.

**5.7 Check the instance.**

```
suiteward doctor
```

Expect `ok` on every line and exit code 0. A line that starts with `FAIL` names the problem and its fix (section 12 lists them). `suiteward doctor --json` prints the same as JSON. It reads the settings file the installer wrote, so with a second instance pass `--settings <path>`.

**Docker, discouraged.** Install with `--runtime docker`, and confirm with `--accept-docker` (or type `yes` at the prompt that prints the warning). On a PC that also runs Podman, the name `docker` can be Podman's shim, and the default Docker endpoint (the `docker_engine` pipe) belongs to whichever engine started first. So the installer never trusts the name: on Windows it runs Docker Desktop's own CLI by its full path (`C:\Program Files\Docker\Docker\resources\bin\docker.exe`) against the `desktop-linux` context, identifies the engine behind it, refuses Podman's, and says which engine it uses. `--connection <context>` selects another Docker context. Docker has no `--replace` and no secret store, so the installer removes a container before it runs it again, and keeps the secrets in one named volume for each container that reads them, mounted read-only, one file for each secret (mode 0400, owned by the uid that reads it), written through standard input. `serve` therefore never sees the owner's URL, and no secret touches the Windows disk. Whoever controls the Docker daemon can still read the volumes. Use a port pair of your own (`--http-port`, `--mcp-port`) when a Podman stack runs on the same PC.

**One more instance on the same PC.** The default settings file and token file belong to the first instance. The installer refuses to take them over; give the second one its own `--settings` and `--token-file`, and its own ports.

## 6. Run setup

Setup binds you as the owner and enrolls your TOTP secret. On the co-located tier, the installer runs it for you at the end of the install, on a real terminal: it passes your terminal through to the service container, never captures or logs setup's output, and refuses to run it without one (it prints the command instead). On the isolated tier, you run it yourself, once, as `suiteward` in a shell opened as in step 10.4. It needs a real terminal.

Run setup on the machine that runs the instance you are enrolling: the PC for co-located, the host for isolated. Its `--test-check` options name only the repositories of that instance, and each instance keeps its own owner and secret.

Until setup succeeds, SuiteWard comments nothing and publishes no `SuiteWard / Contract` check, so a pull request shows that required check as waiting. This is expected.

Before you start:

- Have your authenticator app ready.
- Turn off recording of the terminal session. Setup prints the `otpauth://` URI and the base32 secret to the terminal, and a recorded session keeps them. If recording was on, turn it off and run setup again. That replaces the secret, so the old one stops working.

Run setup with your login and one `--test-check` for each configured repository. The check name is the exact name of the required status check that runs your tests, as it appears in that repository's ruleset. Do not use `SuiteWard / Contract`: SuiteWard publishes that check itself. Quote each `--test-check` value, for example `--test-check 'owner/name=CI / test'`: check names often contain spaces and slashes, and an unquoted value would be split by the shell.

```
podman exec -it suiteward /suiteward setup --owner <login> --test-check '<owner/name>=<check name>' --test-check '<owner/name>=<check name>'
```

The installer prints the exact command for your engine. On the co-located PC, run it in the PowerShell session where you selected the rootless connection (step 5.1), so that `podman exec` finds the stack.

Setup then works through these steps:

1. It looks up your login, which must be a user account, and every configured repository. If one lookup fails, it exits with code 1 before it writes anything.
2. It prints a warning and waits. Press Enter to continue. End of input or Ctrl-C exits with code 1, before any secret is created.
3. It prints the `otpauth://` URI and the base32 secret, once. Scan the URI with your authenticator, or type the secret in. The entry is named `SuiteWard:<login> (<tier> <instance>)`, for example `SuiteWard:octo (isolated 1a2b3c4d)`: `<instance>` is the first 8 characters of this instance's id, which `/status` shows as `instance`, so each instance has its own entry. Setup says the same on stderr. If you type the secret in, give the entry that name yourself.
4. It asks for the current 6-digit code. You get three attempts. Three wrong codes exit with code 1 and write nothing.
5. It stores the owner and the secret, records each repository's test check, and checks protection for each repository (section 7).

A missing, extra or duplicate `--test-check` is a usage error: exit code 2, and nothing is written. Setup exits with code 0 once enrollment succeeded, whatever the protection results.

Afterwards, check the result with `suiteward doctor`, or read `/status`: on the co-located PC with the command the installer printed (on Windows with Podman, `podman machine ssh --username user <machine> "curl -s http://127.0.0.1:8081/status"`), on the isolated host, as `suiteward`, with `curl -s http://127.0.0.1:8081/status`. The output shows `owner` with your login and the `protection` result of each repository.

Only a setup that prints the protection result of each repository and exits with code 0 counts. Confirm it with `/status`: `owner.enrolledAt` changes on every successful run, so a value that did not change means that setup did not complete. An interrupted setup can leave an authenticator entry that matches nothing: delete it.

Running setup again replaces the TOTP secret, and the new entry has the same name as the old one of this instance. Before you scan it, delete any existing entry with this exact name. Entries of other instances (another id in the name) stay. An older entry named `SuiteWard:<login>`, with no instance in its name, predates this release: delete it once the new entry's code is accepted. Never paste the setup output, the URI, the secret or a code into a chat, an issue or a pull request.

## 7. Create the ruleset

SuiteWard reports protection and never changes it, so you set the protection of each repository once, on its default branch. On the co-located tier, the installer wrote a ruleset file for each repository (`suiteward-ruleset-<owner>-<name>.json`, section 5.3, step 7): open `https://github.com/<owner>/<repo>/settings/rules`, choose **New ruleset**, then **Import a ruleset**, and select it. The App must be installed on the repository first, or GitHub refuses the import with `Invalid parameter required_status_checks: Invalid integration ids`. On a private repository on GitHub Free, GitHub imports the ruleset but does not enforce it (section 3). Otherwise, and on the isolated tier, open the same address and create a ruleset (or classic branch protection, at `https://github.com/<owner>/<repo>/settings/branches`) by hand. Either way, it must do the following:

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
| `enforcement-unavailable` | GitHub does not enforce rules on this private repository, or the App cannot read the rules. SuiteWard does not claim protection here. | Either use a GitHub plan that enforces rules on private repositories (GitHub Free does not), or give the App the read permission (step 10.2). |
| `unknown` | The protection could not be read, for example because the App's token lacks Administration read. | Check the App's permissions against step 10.2. |
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
- Reading a very large test suite can take longer than the job timeout. SuiteWard reads the protected files eight at a time and keeps what it read, so the default of one minute covers about a thousand files, and a pass that is cut off resumes where it stopped. For a suite far beyond that, raise `SUITEWARD_JOB_TIMEOUT` (section 13): on the co-located PC with `--job-timeout` of `suiteward install`, on the isolated host with an `Environment=SUITEWARD_JOB_TIMEOUT=<value>` line in `suiteward.container`, which the installer carries over.

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
| failure, `integrity-failed` | A canonical artifact no longer matches its integrity record. SuiteWard fails the check on every pull request of that repository and does not promote; it does not block merges by itself — your branch protection does that, if you require the check. Restore the artifact from a trusted copy or a backup, never edit the database, and restart the service: it re-verifies at once. On the isolated host, as `suiteward`, run `systemctl --user restart suiteward.service`: the image did not change, so migrate does not run again. On the co-located PC, run `suiteward install` again with no other flag: it replaces the service container. |
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
1. Remove the repository from the list. On a co-located PC, run `suiteward install --github-repositories '<owner/name>,<owner/name>'` with the repositories you keep: it asks the test checks again, writes the rulesets, and runs setup (step 2). On an isolated host, as `suiteward`, remove it from the `SUITEWARD_GITHUB_REPOSITORIES` line of `~/.config/suiteward/github.env` and from `repositories` in `~/.config/suiteward/settings.yaml` (the installer rewrites the first file from the second), then run `systemctl --user restart suiteward.service`.
2. Run setup again (section 6) with one `--test-check` for each repository you keep. Setup replaces the TOTP secret, so delete the entry of this instance from your authenticator app before you scan the new one (the entry with this exact name, `<tier> <instance>` included; entries of other instances stay), then add the new entry. An entry named only `SuiteWard:<login>` predates this release: delete it once the new entry's code is accepted.
3. Comment your command again. A command written before the new setup is ignored and gets no reply.

SuiteWard changes nothing on GitHub: remove the App from the repository, and the ruleset rule that requires `SuiteWard / Contract`, yourself.

## 10. Running on a separate host (operators)

This installs the isolated tier (section 2): SuiteWard on a separate host that your coding agent cannot reach, for whoever self-hosts on a machine the agent cannot reach. It gives the full assurance. The host runs AlmaLinux 9 and Podman 5.5 or later, rootless, for an unprivileged account that this guide calls `suiteward`. Only Podman is supported here.

The install is split by privilege, and the installer never runs as root. `sudo suiteward install host` does the administrator steps once. `suiteward install`, run as the account `suiteward`, does the rest. Each step below says who runs it.

The host runs three container units: `suiteward-db` is PostgreSQL, `suiteward-migrate` is a one-shot `suiteward migrate` that runs as the database owner, and `suiteward` is the service, which requires the migration and connects as the least-privilege role `suiteward_app`. Starting the service always migrates first.

**10.1 Get the installer onto the host (as the administrator).** Verify the binary on your PC and copy it to the host as section 4 says: `sha256sum --check --ignore-missing SHA256SUMS`, then a copy at `/usr/local/bin/suiteward`.

**10.2 Create the GitHub App by hand (on your PC).** The isolated installer takes an App that exists: it does not create one. <!-- I9: the isolated installer runs no manifest flow at 7755d60 (installIsolated gets no Env); rewrite this step if the flow is wired -->Tick each box when it is done:

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
- [ ] Generate a private key. GitHub downloads a `.pem` file. Keep it somewhere safe until step 10.4 stores it. Never commit it. Do this before the next item: the private key must exist before GitHub allows the installation.
- [ ] Install the App on the repositories you want to protect, and no others. The limit is 20.
- [ ] Note the **App ID** (on the App's settings page) and the **installation ID** (the number in the address of the installation's settings page).
- [ ] Optional: upload [`assets/brand/png/suiteward-github-app.png`](../assets/brand/png/suiteward-github-app.png) as the App's logo, with the badge background color `#12202F`, so its comments and checks carry the SuiteWard mark.

Keep these permissions off, for these reasons:

- **Administration: Write** stays off. SuiteWard reads protection and never changes it. You import the ruleset yourself (section 7).
- **Contents: Write** stays off. SuiteWard reads code and never commits. The `.suiteward.yml` declaration is yours to commit.
- **Issues** stays off. Pull request comments are covered by the Pull requests permission.
- **Webhooks** stay off. This release polls GitHub and accepts no inbound webhooks.
- **Any other permission** stays at No access. SuiteWard does not request it.

**10.3 Prepare the host (as the administrator).** Run:

```
sudo suiteward install host
```

It asks before it changes anything that you did not ask for, and does these steps in this order:

1. **Podman.** It checks that Podman runs, is 5.5 or later, and has the fix for CVE-2026-57231. A distribution can backport that fix without changing the version, so it reads the package changelog. If something is missing, it asks before it runs `dnf install -y podman` or `dnf upgrade -y podman` (`--yes` answers yes). It refuses to go on when the fix is still missing: do not run SuiteWard on that host.
2. **The account.** It creates `suiteward` with no login shell if it does not exist, gives it subordinate user and group id ranges that no other account uses, and enables linger, so the account's services start at boot and run without a login.
3. **The metadata rule.** On a host where the cloud instance metadata address `169.254.169.254` answers, it asks whether to add the nftables rule that refuses that address for the `suiteward` account (`--metadata-rule yes` or `no`). The metadata service hands out the instance's credentials, and rootless containers connect as a process of the service account, so a rule for that account covers them. The rule loads at every boot, through the unit `suiteward-imds-block.service`, and covers IPv4 only. It never adds the rule without a yes. When nothing answers at that address, it says so and adds nothing. `suiteward doctor` reports an unrefused address.
4. **The account's folder.** It creates `~/.config/suiteward` for `suiteward`, mode 0700, where the GitHub settings go. A GitHub env file from a hand-made install (`/etc/suiteward/github.env`) is copied there, and the old file stays until the next step has moved the units to the new one.

It ends by printing the command for the next step, with the account's user bus filled in. `--answers <file>` gives the answers from a file (section 12).

**10.4 Install the stack (as `suiteward`).** The account has a `nologin` shell, so `sudo -iu suiteward` fails: run the installer through `sudo -u suiteward -H` with the account's user bus, as the previous step printed. Put the App's key into a file readable only by you, without echo: run this, wait until the cursor stops, paste the whole PEM, press Enter, then press Ctrl-D. You see nothing while you paste:

```
trap 'stty echo' INT; stty -echo; (umask 077; cat > ~/app-key.pem); stty echo; trap - INT
```

Then run the installer, with the tier, the digest of the release and the App's values. Pass `--tier isolated`: the default tier is `co-located`. <!-- I9: the command that `install host` prints omits --tier isolated; verify it when that message is fixed -->

```
r=/run/user/$(id -u suiteward)
sudo -u suiteward -H env XDG_RUNTIME_DIR=$r DBUS_SESSION_BUS_ADDRESS=unix:path=$r/bus \
  /usr/local/bin/suiteward install --tier isolated --image sha256:<index> \
  --github-app-id <app id> --github-installation-id <installation id> \
  --github-repositories '<owner/name>,<owner/name>' --github-app-key-file /home/suiteward/app-key.pem
```

`--image` is required on a fresh install: the isolated install does not read the digest embedded in the binary. Take the index digest from the release notes and verify it first, from the release source on a machine that has cosign 3.x (the host does not need it):

```
pwsh ./scripts/verify-release.ps1 -Reference ghcr.io/ignisdevne/suiteward@sha256:<index>
```

Expect `Verified: … was signed by the SuiteWard release workflow.` The GitHub values are all given or all left out: the service stops at startup when only some are set. The installer works in this order, and a refusal before step 4 changes nothing:

1. It refuses to run as root, or as any account but `suiteward`, and checks the account's systemd user manager and linger.
2. It adopts an existing install (section 10.6), and checks the image and every input.
3. It prints the fingerprint of the App key, `SHA256:` followed by base64 text. Compare it with the fingerprint that the App's settings page shows for that key. A wrong or revoked key looks right to every other check, and GitHub calls then stop about an hour later.
4. It creates the Podman secrets that are missing: the database password and the owner's URL, the MCP token and the App key. It never prints them, and it never replaces a database password that exists, because the database keeps it in its volume.
5. It writes `~/.config/suiteward/github.env` (the three GitHub values; they are not secrets).
6. It pulls the PostgreSQL image and the release image by digest.
7. It writes the six unit files into `~/.config/containers/systemd/`, runs the Podman generator's dry run to be sure Podman reads every one of them, and reloads systemd.
8. It starts the database, waits until it is healthy, creates the application role `suiteward_app` with its URL secret, starts the service (which runs the migration first) and checks that the three units are active.
9. It saves its settings in `~/.config/suiteward/settings.yaml`.

Then delete the key file: `shred -u ~/app-key.pem`, as `suiteward`, and delete your own copy of the `.pem`.

**10.5 Setup, ruleset and check.** The isolated installer does not write the ruleset and does not run setup. It prints the setup command. As `suiteward`, in a shell opened as in step 10.4, with session recording off:

- run setup (section 6), with `podman exec -it suiteward /suiteward setup --owner <login> --test-check '<owner/name>=<check name>'`;
- create the ruleset of each repository by hand (section 7);
- run `suiteward doctor` and expect `ok` on every line.

The port is published on the host's loopback only (8081 and 8091). To read `/status` from your PC, forward the port with your own remote-access tool, for example `aws ssm start-session --target <instance-id> --region <region> --document-name AWS-StartPortForwardingSession --parameters portNumber=8081,localPortNumber=8081`. SuiteWard provides no remote access.

**10.6 A host installed by hand.** A host from the v0.2.0 install guide is adopted. The installer compares the installed units with the units of the release it installs, carries over the tier, the ports and the job timeout, and moves the GitHub env file from `/etc/suiteward/github.env` to the account's folder (step 10.3 copies it). Any other difference stops the run and prints the diff: nothing is overwritten silently. For a release that differs from the installed one, use `install --upgrade` (section 11).

For the manual procedure of the v0.2.0 release, which this guide no longer repeats, see [the install guide at the tag v0.2.0](https://github.com/IgnisDevNE/SuiteWard/blob/v0.2.0/docs/install.md).

## 11. Updating

Pin one release, and update when you choose. Updates are always explicit, by digest. Automatic updates are not supported: Podman's auto-update cannot update the migration unit, so an automatic update would start the service alone on an old schema, which the service refuses, and loop. Never add `AutoUpdate=` to a unit or enable `podman-auto-update.timer`.

Download and verify the new release's installer as section 4 says, then run it with `--upgrade`: <!-- I7: verify flags -->

```
suiteward install --upgrade
```

On the isolated host, run it as `suiteward` as in step 10.4, with `--tier isolated` and `--image sha256:<index>` of the new release. The upgrade takes these steps, in order:

1. **A backup first,** into a private directory (mode 700, with mode 600 files): a `pg_dump` of the database and an export of the artifact volume. It holds the TOTP secret, so keep it as a secret. [The backup runbook](backup.md) describes the same two parts, and how to restore them.
2. It pulls the new image by digest and rewrites the units.
3. It restarts the database when its image pin changed, migrates, and starts the service.
4. Then run `suiteward doctor`. Check that `version` in `/status` is the release you meant.

Migrations only go forward. An older image refuses to start on a newer schema, and nothing migrates it back. So the way back is the backup: restore it with the old release ([the backup runbook](backup.md#3-restore)), as everything after the backup is lost. Do not start an older image on the upgraded database.

Read the release notes first. An instance on v0.1.1 or older upgrades to v0.1.3 first, and then to v0.2.0, and only then to a release that has the installer: later releases no longer read the comment payloads that v0.1.1 or older queued. A stack that the PowerShell start script of v0.2.0 and earlier made is converted by the installer, under the same names. An instance older than v0.2.0 follows [the v0.2.0 guide](https://github.com/IgnisDevNE/SuiteWard/blob/v0.2.0/docs/install.md) to v0.2.0 first.

### Uninstalling

```
suiteward uninstall
```

On a co-located PC, it asks first, then removes the three containers (`suiteward`, `suiteward-migrate` and `suiteward-db`) and keeps the volumes, the secrets and the settings: run `suiteward install` again to start the stack. `--purge` also removes the volumes (the database and the artifacts), the secrets, the network, the MCP token copy and the settings file. That deletes your TOTP enrollment and every contract version: back up first ([the backup runbook](backup.md)). `--yes` skips the question. It leaves a stack that this installer did not make, or that was edited, alone. On Docker it uses the same runtime and connection that the install recorded.

On the isolated host, run `suiteward uninstall` as `suiteward`. <!-- I7: verify flags --> SuiteWard changes nothing on GitHub: delete the App, and the ruleset rule that requires `SuiteWard / Contract`, yourself. The account `suiteward`, the metadata rule and Podman stay on the host; remove them with the administrator's own tools if you no longer need them.

### Rotating the App key

Rotate the key when it may have been seen by something it should not, for example by a coding agent. Do it yourself, from a terminal that no agent can reach. A co-located install gives the agent access to the key anyway (section 2): there, rotation helps against a past exposure and a key that left the machine, not against an agent that still shares your PC user. The installer has no command for rotation: a re-run with a key file can create a new App or run setup again. Replace the secret by hand.

The order matters. After you replace the key, the service keeps working for up to an hour on installation tokens that it already holds. A wrong key therefore shows up late, when every GitHub call stops. So check the fingerprint at each step, and delete the old key last.

**R.1 Generate a new key.** On the App's settings page, under **Private keys**, generate a private key. GitHub downloads a `.pem` file and shows the key's fingerprint, `SHA256:` followed by base64 text. Note it. Leave the old key in place.

**R.2 Stop the service and remove the old secret.**

- Isolated host, as `suiteward` (a shell opened as in step 10.4):

  ```
  systemctl --user stop suiteward.service
  podman secret rm suiteward-github-app-key
  ```

- Co-located PC on Podman, in the PowerShell session where you selected the rootless connection (step 5.1). The first command removes the containers and keeps the secrets:

  ```
  suiteward uninstall --yes
  podman secret rm suiteward-github-app-key
  ```

- Co-located PC on Docker: this release has no procedure. The key is a file in a named volume that only the installer writes. Use Podman, or back up, `suiteward uninstall --purge`, install again and restore.

**R.3 Put the new key in a file.** On the isolated host, paste it without echo, as in step 10.4. On the PC, use the `.pem` that GitHub downloaded.

**R.4 Check the file's fingerprint** before you use it. It must be the text you noted in R.1.

- Isolated host, as `suiteward`:

  ```
  openssl rsa -in ~/app-key.pem -pubout -outform DER 2>/dev/null | openssl dgst -sha256 -binary | openssl enc -base64
  ```

- Co-located PC: in Git Bash, run the same command with the path of your `.pem` file in place of `~/app-key.pem`, written as Git Bash accepts it, for example `/c/Users/<you>/Downloads/<file>.pem`.

If it differs, delete the file (`shred -u ~/app-key.pem` on the host, `Remove-Item <path of the new PEM>` on the PC), and repeat R.3 with the right file.

**R.5 Create the secret, and start the service.**

- Isolated host, as `suiteward`:

  ```
  podman secret create suiteward-github-app-key ~/app-key.pem
  shred -u ~/app-key.pem
  systemctl --user start suiteward.service
  ```

- Co-located PC on Podman:

  ```
  Get-Content -Raw <path of the new PEM> | podman secret create suiteward-github-app-key -
  suiteward install
  Remove-Item <path of the new PEM>
  ```

  The second command runs without GitHub flags: it finds the key secret, so it does not run the GitHub flow, and it creates the containers again.

**R.6 Check the secret's fingerprint again.** It must be the text of R.1.

- Isolated host, as `suiteward`:

  ```
  podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-github-app-key | openssl rsa -pubout -outform DER 2>/dev/null | openssl dgst -sha256 -binary | openssl enc -base64
  ```

- Co-located PC, in Git Bash. The Podman machine has no OpenSSL, so the secret goes out to the PC's:

  ```
  podman machine ssh --username user "podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-github-app-key" | openssl rsa -pubout -outform DER 2>/dev/null | openssl dgst -sha256 -binary | openssl enc -base64
  ```

**R.7 Confirm that GitHub accepts the key.** The service starts without a cached token, so its first reads already use the new key. Run `suiteward doctor`: `github.app` and `github.installation` must be `ok`. Then wait about a minute and read `/status` twice, a minute apart (section 15 shows how). The `lastSuccess` of each repository must advance, and no repository may show a `lastError`. That is the proof. The log line `bot account could not be resolved` appears only when SuiteWard processes an owner command, so its absence proves nothing. If you do see it, the key is wrong. This prints the lines it finds:

- Isolated host, as `suiteward`:

  ```
  podman logs --since 10m suiteward 2>&1 | grep 'bot account could not be resolved'
  ```

- Co-located PC:

  ```
  podman machine ssh --username user "podman logs --since 10m suiteward 2>&1 | grep 'bot account could not be resolved'"
  ```

**R.8 Delete the old key.** Only now, on the App's settings page, delete the old private key. Then delete every other copy of a `.pem` file, including the one GitHub downloaded in R.1.

If R.4 or R.6 shows a different fingerprint, or R.7 shows no advance or a `lastError`, do not delete the old key. Stop the service, and repeat from R.2.

## 12. The installer: flags, answers file and what it does

Every command prints its flags with `--help`. The exit code is 0 on success, 1 on a failure, and 2 on a usage error.

### Commands and flags

`suiteward install`:

| Flag | Meaning |
| --- | --- |
| `--answers PATH` | Read answers from a YAML file. A flag wins over the file. |
| `--settings PATH` | The installer settings file (default: `suiteward/settings.yaml` in your per-user configuration directory). |
| `--token-file PATH` | Where the copy of the MCP token for a client goes (co-located). |
| `--tier` | `co-located` (default) or `isolated`. |
| `--runtime` | `podman` (default, recommended) or `docker` (co-located only). |
| `--accept-docker` | Confirm that Docker may be used although Podman is recommended. |
| `--connection` | The Podman connection, or the Docker context (default: the rootless Podman machine, or Docker Desktop's). |
| `--image` | The release image pinned by digest, `ghcr.io/ignisdevne/suiteward@sha256:<64 hex>`. Co-located default: the installed image, else the release the binary carries. Isolated: required, and `sha256:<64 hex>` is enough. |
| `--http-port`, `--mcp-port` | The loopback ports of the two listeners (defaults 8081 and 8091). Co-located only. |
| `--job-timeout` | A Go duration for `SUITEWARD_JOB_TIMEOUT` (the service only). Co-located only. |
| `--github` | `yes`: create the GitHub App now. `no`: skip GitHub. Co-located only. |
| `--github-account` | The account that owns the new App: `personal`, or an organization login. |
| `--manifest-port`, `--manifest-code` | The port of the manifest listener, and a code from the manifest redirect (section 5.3). |
| `--github-app-id` | The ID of an App that exists, or `none` to skip GitHub. |
| `--github-installation-id` | The installation of that App. |
| `--github-owner` | The GitHub login of the instance owner. Co-located only. |
| `--github-repositories` | A comma-separated `owner/name` list, at most 20. |
| `--github-app-key-file` | A file with the App's private key (PEM), or `-` for standard input. Read only while the key secret is missing. |
| `--test-check` | The test check of a repository, `OWNER/NAME=CHECK`. Repeat it, or give one plain name for a single repository. Co-located only. |
| `--ruleset-imported` | `yes` once the ruleset of each repository is imported: it skips the wait. Co-located only. |
| `--upgrade` | Back up, then move to the new release (section 11). |

`sudo suiteward install host`: `--answers PATH`, `--yes` (answer yes to installing Podman with `dnf`) and `--metadata-rule yes|no`.

`suiteward uninstall`: `--purge`, `--yes`, `--settings`, `--token-file`, `--tier`, `--runtime` and `--connection`, as above.

`suiteward doctor`: `--json` and `--settings PATH`.

### The answers file

A flat YAML file in which every key is a flag name and every value is a plain scalar. The installer reads the answer from the flag, else from the file, else it asks at the terminal. When it cannot ask (standard input is not a terminal), it takes the default, or stops and names the missing flag. On Linux and macOS, the file must be readable by you only (`chmod 600`): the installer refuses a file that others can read. Windows has no such check, so keep the file in a folder only you can read. A secret never goes in a flag, where it would show in the process list: give the App's key as `--github-app-key-file`. Example, without any secret:

```yaml
tier: co-located
runtime: podman
http-port: 8081
mcp-port: 8091
github: "yes"
github-account: personal
github-repositories: owner/name
github-owner: octo
test-check: "owner/name=CI / test"
ruleset-imported: "yes"
```

`test-check.owner/name` sets the check of one repository. Never put the TOTP secret, a code or a key in this file.

### What the installer does, for review

The installer replaces about 11 manual commands on the co-located tier and about 62 on the isolated tier. This list maps its steps to the manual procedure that older releases documented, and says why. [The v0.2.0 guide](https://github.com/IgnisDevNE/SuiteWard/blob/v0.2.0/docs/install.md) has that procedure.

| Step | Why |
| --- | --- |
| Refuses a rootful Podman connection, and never runs as root. | A rootless store keeps the containers away from the host's root. Isolated splits the administrator steps from the service account's. |
| Identifies the engine behind the runtime and prints it. | The name `docker` can be Podman's shim, and the default Docker endpoint can belong to Podman's machine. |
| Pulls images by digest only, and refuses a tag. | A tag can be rewritten and a digest cannot. The binary carries the signed digest of its release. |
| Checks both ports before it changes anything. | A refusal must leave a running stack alone. |
| Creates the network, the two volumes and the database owner secrets, with a generated password. | The password is never typed, shown or passed as an argument. |
| Starts the database, creates the role `suiteward_app`, runs `suiteward migrate`, and only then starts the service. | The service runs as a role that owns nothing and cannot change the schema or the immutability triggers. Only the one-shot migrate container holds the owner's URL. |
| Runs every container read-only, with all capabilities dropped and `no-new-privileges`, as user 65532, with the published ports on 127.0.0.1 only. | Defence in depth: the listeners are never reachable from the network. |
| Creates the App through the manifest flow, with exactly five permissions, no webhook, `public: false`. | No Contents write and no Administration write: SuiteWard never commits and never changes protection. |
| Converts the manifest code on the machine that keeps the key, with a random `state`, and writes the key straight into the secret store. | The key never touches disk or the terminal. The code passes through your browser, as a downloaded key would. |
| Writes the ruleset JSON and waits for your Import. | The App has no permission to change rules. A rule change stays a human click for each repository. |
| Runs setup with the real terminal and never captures its output; refuses to run it without one. | Setup prints the TOTP secret. A coding agent must not read it. |
| Writes its settings to a file of its own. | `doctor` and the next run read it back, and nothing else is silently overwritten. |
| Enables `podman-restart.service` for the rootless user (Podman). | A rootless `--restart always` needs it after the machine restarts. |
| Isolated: checks Podman 5.5 or later with the CVE fix, makes the account, its subordinate ids and linger, and offers the metadata rule. | Rootless Podman needs the ranges, the services must outlive a login, and a metadata service hands out instance credentials. |
| Isolated: writes six unit files and checks them with the generator's dry run. | Podman skips a unit it cannot parse, and you would only see "unit not found" later. |

### What `suiteward doctor` checks

`doctor` only reads. It prints `ok <name>`, or `FAIL <name>: <what is wrong> — <the fix>`, and exits non-zero when anything failed. It runs `suiteward check --json` inside the service container and merges its results.

| Name | A FAIL means |
| --- | --- |
| `settings` | There is no settings file, or it is invalid. Run `suiteward install`, or pass `--settings`. |
| `machine` | The Podman machine does not exist, is starting or is not running. Run `podman machine start`. |
| `runtime` | The engine does not answer, the connection is rootful (select the rootless one), or `docker` is not Docker. Set `connection` in the settings file. |
| `containers` (co-located) | `suiteward-db` or `suiteward` is not running, or the database is not healthy. Read the container's log, fix the cause and start it. |
| `restart` | The containers or units would not come back after a restart: a restart policy is not `always`, or `podman-restart.service` is not enabled for the rootless user (co-located), or a unit lacks `WantedBy=default.target` (isolated). Enable the service as the fix names. |
| `units`, `linger` (isolated) | A unit is not in its expected state, or the account does not linger. Read the journal, or run `sudo loginctl enable-linger suiteward`. |
| `pins` | The release images of the units or containers are not one digest, or the database image is not the pinned one. Run `suiteward install --upgrade`, or undo the edit it names. |
| `ports`, `healthz`, `status` | `/readyz`, `/healthz` or `/status` does not answer on the recorded port, or `/status` reports a failed outbox message or a discarded job. Read the service log. |
| `network`, `memory` (isolated) | The Podman network backend is not `netavark`, or the database is not limited to 384 MiB. |
| `metadata` (isolated) | `169.254.169.254` answers from the service network. Run `sudo suiteward install host` and answer yes to the metadata rule. |
| `github.app`, `github.installation`, `github.permissions`, `github.repositories` | GitHub refuses the App key, the installation is not the App's, the installation's permissions are not exactly the five, or a repository is not covered or not readable. Check the App ID and the key against the App's settings page. |
| `protection.<owner/name>` | The repository's protection is not `enforced`. Its fix names the finding (section 7). |
| `owner.enrolled` | Setup has not run. Run it (section 6). |
| `database.schema`, `database.role` | The schema is not at this release's version, or the service's role is a superuser, can create roles or bypasses row-level security. |
| `instance.check` | `suiteward check` could not run in the service container. Start the service. |

## 13. Configuration reference

Every setting is an environment variable named `SUITEWARD_<NAME>`. A secret can instead be given as `SUITEWARD_<NAME>_FILE`, the path of a file whose content is the value. Give one form, not both. A variable that is set but empty counts as unset.

Where you set them:

- **Isolated host:** the installer renders the units. It carries the tier, the two ports and the job timeout over from the installed units, and any other edit of a unit stops the next install with a diff. The three GitHub values are in `~/.config/suiteward/github.env`. After a hand edit, run `systemctl --user daemon-reload` and `systemctl --user restart suiteward.service` as `suiteward`. After a change in `suiteward-migrate.container`, or to an `Image=` line, restart `suiteward-migrate.service` instead.
- **Co-located PC:** `suiteward install` has flags for the ports (`--http-port`, `--mcp-port`), the image, and `--job-timeout` (`SUITEWARD_JOB_TIMEOUT`, for the service container only). The GitHub values come from its GitHub flow. The other settings keep their defaults on this PC. Pass `--job-timeout` on every run that should have it: a run without it keeps the saved value.

| Setting | Default | Meaning |
| --- | --- | --- |
| `SUITEWARD_DATABASE_URL` (secret, required) | none | The PostgreSQL URL of the application role `suiteward_app`, never the owner. Use the `_FILE` form. The isolated units mount it from the Podman secret `suiteward-database-url`. |
| `SUITEWARD_MIGRATION_DATABASE_URL` (secret) | none | The PostgreSQL URL of the database owner. Only `suiteward migrate` connects with it, and it requires it; `serve`, `setup` and `probe` refuse to start when it is set, so never put it in the service's unit. Use the `_FILE` form. Only the migration unit mounts it, from the Podman secret `suiteward-database-owner-url`: the service never gets it. |
| `SUITEWARD_ARTIFACT_DIR` (required) | none. The image sets `/data/artifacts` | Absolute path of the artifact store. |
| `SUITEWARD_HTTP_ADDR` | `127.0.0.1:8080`. The image sets `0.0.0.0:8080` | Address of the HTTP listener. The units publish it on the host's loopback only. |
| `SUITEWARD_SHUTDOWN_TIMEOUT` | `30s` (1 s to 10 min) | Time allowed for a graceful stop. The container's stop timeout must exceed this by at least 5 s. The units set 45 s, which covers the default. A value of 40 s or more needs a larger stop timeout. |
| `SUITEWARD_LOG_LEVEL` | `info` | One of `debug`, `info`, `warn`, `error`. |
| `SUITEWARD_JOB_WORKERS` | `4` (1 to 64) | Number of background workers. |
| `SUITEWARD_JOB_TIMEOUT` | `1m` (1 s to 1 h) | Time limit of one attempt of a background job. A `propose` or a check reads about a thousand protected files per minute of this value (eight at a time, and what was read is kept for the next attempt); raise it only for a very large test suite. On the isolated host, set it in `suiteward.container`; on the co-located PC, pass `--job-timeout <value>` (for example `3m`) to `suiteward install` (section 5). |
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

## 14. Source code

SuiteWard is licensed under `AGPL-3.0-only`. The license text is in the repository's `LICENSE` file. The image carries that text as `/licenses/LICENSE`, and the license of every module it links as `/licenses/THIRD_PARTY_NOTICES`.

`/status` reports the URL of the exact source revision the binary was built from, as `source`. The project chose this as its pointer to the source ([D-RELEASE](plan/decisions.md#d-release-platforms-versions-license-and-notices)). For a release, that is `https://github.com/IgnisDevNE/SuiteWard/tree/vX.Y.Z`. If `source` is absent, the build set none.

`/status` is reachable only over the loopback address of the host, so it does not by itself satisfy section 13 of the AGPL for remote users. If you modify SuiteWard, you must offer the corresponding source of your modified version where your users interact with it, and point `source` at that source. Build the image yourself with the `SOURCE` build argument, alongside `VERSION`:

```
podman build -f deploy/Containerfile --build-arg VERSION=sha-<commit> --build-arg SOURCE=<url of your modified source> -t localhost/suiteward:local .
```

`/licenses` in the built image holds the license and the third-party notices, whatever `SOURCE` you build with.

## 15. Troubleshooting

Start with `suiteward doctor`: it names what is wrong and the fix, and it reads nothing it should not (section 12). Then read `/status` and the logs.

- **Isolated host (as `suiteward`):** `curl -s http://127.0.0.1:8081/status`, then `journalctl --user -u suiteward.service -n 100 --no-pager`, for the migration `journalctl --user -u suiteward-migrate.service -n 50 --no-pager`, and for the database `journalctl --user -u suiteward-db.service -n 50 --no-pager`. The migration container is removed when it exits, so `podman logs` cannot show it.
- **Co-located PC:** the installer prints the command that reads `/status` at its end. On Windows with Podman it is `podman machine ssh --username user <machine> "curl -s http://127.0.0.1:8081/status"`, because the published port may not answer from Windows itself. Then `podman logs --tail 50 suiteward` (with the rootless connection selected, step 5.1), or the engine's own `logs` command on Docker. The migration runs in the foreground of `suiteward install`, which shows its output.

On AlmaLinux, `journalctl --user` can report that no journal files were opened: user journals are readable only to members of `systemd-journal`. Read the service's own log instead, with `podman logs --since 30m suiteward` (and `suiteward-migrate` or `suiteward-db`).

If the log shows `github_checks` ending in `context deadline exceeded` again and again, then `budget_exhausted`: a first assessment of a large test suite takes longer than `SUITEWARD_JOB_TIMEOUT`, and every cancelled attempt reads the files again and spends the API budget. Raise `SUITEWARD_JOB_TIMEOUT` (section 13; `3m` covered about 250 protected files). Read `budget` in `/status` (or the MCP `status` tool): `budget.remaining` is what is left and `budget.reset` is when requests resume. The budget recovers when GitHub's hourly window resets, and the queued check is then published by itself.

These are the `/status` fields you read:

- `tier` is the effective tier. `tierNote` appears when the setting and the enrollment disagree. Run setup again (section 6).
- `owner` shows the enrolled login. It is absent until setup succeeds.
- `unconfiguredRepositories` lists the enrolled repositories that the configuration does not name. It is absent when there are none.
- `github` has one entry per repository:
  - `lastSuccess` is the last successful synchronization.
  - `lastError` is the last synchronization error, as short text without secrets. If `waitUntil` is set, GitHub asked SuiteWard to wait because of a rate limit, and SuiteWard resumes by itself. Otherwise, check that the App is installed on that repository, and that the App ID, the installation ID and the key are the ones from step 10.2.
  - `protection` shows the results of section 7.
  - `checks` is the last error while SuiteWard published that repository's check. It is absent when there is none.
  - `bootstrap` shows why the first contract is waiting or stopped. `read-failed` means GitHub could not be read, and SuiteWard retries. `repository-changed` means the configured name now points at a different repository from the one enrolled, so check the name. `origin-not-passed` means the default branch's contract could not be read as a passing source. `assessment-conflict` means two assessments of one source disagree. `blocked-<reason>` means a block for the stated reason. `failed` means the bootstrap failed for good. Read the logs for the last three. The field is absent when nothing is wrong.
  - `merges` shows merge decisions. `waiting` means GitHub has not yet reported the merge commit, and SuiteWard retries. `read-failed`, `assessment-conflict` and `failed` need the logs.
  - `integrity` is `passed`, `failed` or `unavailable` for the current version. It appears after the first integrity check, which runs when the service starts, right after each promotion and then every hour, so a canonical shows its `integrity` within moments of its promotion. `failed` means SuiteWard fails the check on every pull request of that repository and does not promote until the problem is resolved; it does not block merges by itself — your branch protection does that, if you require the check. `unavailable` changes no check.
- `outbox.failed` counts messages that SuiteWard could not deliver to GitHub after its retries. It should be 0. Read the logs for the repository. A refused write is often a missing App permission, so compare with step 10.2. If the failed message was a check, SuiteWard sends it again by itself about 15 minutes after the cause is fixed. Until then a pull request can lack its required `SuiteWard / Contract` check. Push a new commit to that pull request to get a new check at once.
- `jobs.discarded` counts background jobs that failed for good. Read the logs.
- `version`, `schemaVersion` and `source` identify the release and the database schema that run. `instance` is the first 8 characters of the instance id, the same as in the name of the authenticator entry that setup creates (`SuiteWard:<login> (<tier> <instance>)`), so you can match an entry to an instance.

Common problems:

- **The installer says that the connection is rootful, or asks for `--connection`.** The stack must run rootless. Select the machine's rootless connection with `--connection <name>` (step 5.1) and run it again. The installer never changes your Podman default.
- **The installer says that `docker` is not Docker, or that the engine behind it is Podman.** On a PC with both, `docker` can be Podman's shim (`C:\Program Files\RedHat\Podman\docker.cmd`), and the default endpoint (`npipe:////./pipe/docker_engine`) can be served by the Podman machine. Use Podman, or give Docker Desktop's own context with `--connection desktop-linux`. The installer runs Docker Desktop's CLI by its full path, `C:\Program Files\Docker\Docker\resources\bin\docker.exe`.
- **Docker Desktop fails to start with `open \\.\pipe\docker_engine: Access denied`.** Whichever engine starts first owns the `docker_engine` pipe, and the Podman machine had it. Stop the Podman machine, start Docker Desktop, then start the machine again: it then uses its own pipe. The installer and `doctor` address each engine by its own endpoint, so they do not depend on the pipe.
- **A Docker pull fails with `error getting credentials`.** Docker Desktop's credential helper lives in `C:\Program Files\Docker\Docker\resources\bin`. The installer puts that folder on the PATH of the commands it runs. For a pull you run by hand, add it to your PATH.
- **After Windows restarts, nothing answers.** The Podman machine does not start at logon in this release. Run `podman machine start`: the containers then come back by themselves, and `suiteward doctor` reports the machine until you do.
- **A port is in use.** The installer names the flag: choose another with `--http-port` or `--mcp-port`. Docker Desktop's VM shares a network namespace with the Podman machine, so two stacks on one PC need different ports.
- **The installer stops with a diff, or says that a container of the stack's name exists that it did not make.** It never overwrites a stack it cannot adopt. Compare the diff with what you changed by hand, remove the stranger, or give a second instance its own `--settings`, `--token-file` and ports.
- **The service does not start on the isolated host.** Podman refuses to start a container while a secret it names (`suiteward-database-url`, `suiteward-database-owner-url` for the migration, the GitHub key or the MCP token) or `~/.config/suiteward/github.env` is missing, and the journal shows which. The service restarts every five seconds until it starts, and it needs the migration to have exited with code 0 (`systemctl --user status suiteward-migrate.service`). A unit that Podman cannot parse is not generated at all: run `/usr/lib/systemd/system-generators/podman-system-generator --user --dryrun` (find it with `rpm -ql podman | grep generator`), or run the installer again, which does that check.
- **The service restarts in a loop and its log says `schema version X, this build requires Y`.** The service refuses a schema that is older than its image, and it never migrates. Someone changed an image and restarted the service alone, or the migration has not run. On the isolated host, as `suiteward`, run `systemctl --user restart suiteward-migrate.service`, which migrates and restarts the service. On a co-located PC, run `suiteward install`, which migrates before it starts the service. If the message says the database was migrated by a newer release, the image is older than the schema: use the newer release, or restore a backup ([the backup runbook](backup.md)). Never start an older image on the database.
- **`suiteward-migrate.service` fails (isolated), or `suiteward install` stops at migrate.** Read the migration's journal (isolated) or the output above the failure. The unit is not retried by itself: the service stays down until you fix the cause and run the install again (or `systemctl --user start suiteward.service`). The usual causes are these:
  - The database role `suiteward_app` does not exist: run the installer again, which creates it. A fresh database volume has no role.
  - The message says the service must connect as a least-privilege role: `suiteward-database-url` names a role that owns the schema or a table in it, is a member of the owner, a superuser or has `BYPASSRLS`, for example the owner's URL. Run the installer again after you remove that secret, so that it makes the application role's one.
  - The message says the database is not reachable after 30 attempts, or that it refused the owner connection: the database is not running or not healthy, or `suiteward-database-owner-url` holds a password that the database does not have.
  - The message starts with `migrate the database`: a migration failed. Report the message and do not edit the database. The schema is left as the failed migration left it, so restore your backup before you try another image ([the backup runbook](backup.md)).
- **The service stays down after a power loss.** If PostgreSQL's crash recovery takes longer than the migration's wait, the migration fails closed and the service stays down. Wait until the database accepts connections (`podman ps` shows `suiteward-db` healthy), then start the service again: `systemctl --user start suiteward.service` on the isolated host, `suiteward install` on a co-located PC.
- **`/readyz` answers 503.** The database does not answer, or the schema is not ready. Check that `suiteward-db` is healthy with `podman ps` (on the isolated host, as `suiteward`), and read its logs.
- **The log says `github commands stopped: the bot account could not be resolved`, with a status of 401.** GitHub refuses the App key that is installed, for example because it was revoked or is not the key you meant. All GitHub calls (reads, checks and comments) stop within about an hour, when the tokens that the service already holds expire. This line is an early signal after a restart, not the first symptom. Follow [Rotating the App key](#rotating-the-app-key), and compare the fingerprints.
- **The browser cannot reach the manifest listener, or the manifest wait timed out.** Paste the code or the whole address from the error page into the terminal (section 5.3), or run the installer again: GitHub's code lives one hour. On the isolated tier, the App is made by hand (step 10.2).
- **GitHub says `Invalid integration ids` when you import the ruleset.** The App must be installed on the repository first. Add the repository to the installation, then import the file again.
- **Setup exits with code 2.** The `--test-check` list does not match the configured repositories. Give exactly one per repository, with no duplicates.
- **Setup exits with code 1.** A lookup failed. The owner login may not be a user account, the App may not be installed on every configured repository, or GitHub could not be reached. Nothing was written.
- **`propose` was refused.** The acknowledgment names the reason: `canonical-present` (the repository already has a contract; `propose` only works before the first one), `fork` (pull requests from forks are not supported), `not-open` (the pull request is no longer open), `base-not-target` (the pull request's base branch is not the repository's default branch, so merging it could never decide the first contract), `declaration-missing` (no `.suiteward.yml` was found on the default branch or the pull request head; see the suggested file and link, section 8), or `invalid-source` (`.suiteward.yml` is invalid, a symbolic link that the scope reaches points at a directory, outside the repository or nowhere (section 8), or GitHub truncated the tree, which happens with very large monorepos and cannot be protected in this release).
- **A command gets no reply.** Check that the first line starts with `/suiteward`, outside a code block, that it comes from your account, that you did not edit it, and that the pull request is still open. Another account's command gets no reply by design.
- **A repository gets no reply and no checks,** `/status` lists it in `unconfiguredRepositories`, and the log says `github commands skip an enrolled repository that is not configured in GITHUB_REPOSITORIES`. A repository you removed from the configuration, or deleted on GitHub, is still enrolled, and SuiteWard skips it. See [Stop protecting a repository](#stop-protecting-a-repository), or add it back to the configuration.
- **An approval is rejected.** The reply gives the reason: an invalid, replayed or expired code, or an unknown revision. Use a fresh code, and check the reference against the newest revision comment.
- **The check shows `approval-missing` after a push.** The push changed a protected file, which creates a new revision. Approve the revision named in the newest comment.
- **The new image does not start after an update.** Do not start an older image, because after a migration it refuses to run. Read the logs of the migration and of the service and report them to the project. To return to the earlier state, restore your backup, as described in [the backup runbook](backup.md).
