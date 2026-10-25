# Installing SuiteWard

This guide installs SuiteWard with its installer, the `suiteward` binary: one guided command on your own machine (co-located) or two on a separate host (isolated). The installer creates the stack, creates the GitHub App, writes the ruleset for you to import, and runs the owner setup. After that you only approve the pull requests that change protected test files. For the short path, see the [quickstart](quickstart.md). If a coding agent helps you install, give it [agent-install.md](agent-install.md) first.

You do not need this guide open to run the installer. On a full screen, every question has a page that says what it asks, the choices, which one is recommended and the default. This guide is the reference: the details behind those pages, and what the plain mode (a script, an agent, `--plain`) needs, because the explanations are for the full screen only.

Two rules apply to every command:

- Pin one release. Replace `<tag>` with the release you install, everywhere it appears (the first release that has the installer is `v0.3.0-alpha.1`). Do not use a moving tag. Where a command names the image index digest of the release, replace `<index>` with it. The release notes name it, on the line `Index digest:`.
- Replace each value in angle brackets, such as `<owner/name>`, with your own value. Never put a token or a TOTP secret or code into a file, a log, a pull request or a chat. The only secrets a file may hold are the App's private key (`--github-app-key-file`, or `github-app-key` in the answers file) and a manifest code (`manifest-code` in the answers file), in a file at mode 600 that you delete afterwards.

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
- **A real terminal.** The installer prompts, and setup prints your TOTP secret. A pipe, a script or a coding agent cannot run setup. On Windows and macOS, setup opens in a window of its own (section 6).
- **Co-located PC:** Windows or macOS with a Podman machine that has a rootless connection (`podman machine init`, then `podman machine start`), or Docker Desktop; or Linux with rootless Podman. Co-located on Windows is the path validated live. macOS is covered by the unit tests, which CI runs on a macOS machine against a fake runtime; no real Podman machine or Docker Desktop on a Mac has been tried yet, and the live validation there is pending. Linux is covered by the same unit tests only. Expect rough edges on both. The macOS binaries are not notarized (section 4). Verifying the download is optional (section 4); to do it by hand you need cosign 3.x, PowerShell 7.2 or later and the release source. The one-line install needs `sh`, `curl` or `wget`, and `sha256sum` or `shasum`; with cosign 3.x it also checks the signature.
- **Isolated host:** AlmaLinux 9 (EL9) with systemd user services. Other distributions are not covered, and the isolated commands refuse any system that is not Linux, macOS included. `sudo suiteward install host` checks Podman 5.5 or later with the fix for CVE-2026-57231 and offers to install it (section 10). To create the GitHub App from the host, your PC needs a port forward to the host: the AWS CLI and the Session Manager plugin on AWS, or your own, such as `ssh -L <port>:127.0.0.1:<port> <host>`. Or give an App you made by hand.

## 4. Get and verify the installer

The installer is a release asset. Each binary embeds the signed image index digest of its own release, so the co-located install never takes an image from a tag.

**Verifying the download is optional.** The installer runs without it, and nothing later asks whether you did it. It is worth doing because the binary is the trust root of the install: it creates the secrets, the App's key store and the stack, and a signature proves that the file came from the release workflow of this repository (you still choose the tag). Verifying by hand needs `scripts/verify-release.ps1`, which is in the release source and not among the release files, so it means cloning the repository or downloading its source at the tag. The one-line install (4.1, Linux and macOS) needs no source: it checks the hash itself, and the signature when cosign 3.x is installed. The lightest check, on any system, is to compare the SHA-256 of the file with its line in the release's `SHA256SUMS`: that catches a damaged download, not a file replaced at its source. If you skip every check, you run a binary that nothing proved to be the release's.

There are two ways to get it and check it: the one-line install on Linux and macOS (4.1), and the download by hand with a signature check (4.2), which works on every platform and is the stronger path.

**4.1 The one-line install (Linux and macOS).** `install.sh`, a file of the release, downloads the binary of your platform, checks it and puts it in `~/.local/bin`:

```
curl -fsSL https://github.com/IgnisDevNE/SuiteWard/releases/download/<tag>/install.sh | SUITEWARD_VERSION=<tag> sh
```

Use the same `<tag>` twice, and always set `SUITEWARD_VERSION`. The script is the same file in every release and learns nothing from the address it came from. Without the variable it installs GitHub's latest release, which is never a pre-release. At the time of writing that is `v0.2.0`, which has no installer binaries, so the script stops and says to set `SUITEWARD_VERSION`. Use `v0.4.0-alpha.2`, the first release with the installer this guide describes. `v0.4.0-alpha.1` is the first release whose page lists `install.sh`, and no release built before phase M1.13 has one, `v0.2.0` and the alpha pre-releases `v0.3.0-alpha.1` and `v0.3.0-alpha.2` included (those have four binaries). There is no one-line install on Windows: use 4.2.

The script does this, and nothing else:

- It reads your system and refuses anything but Linux or macOS on amd64 or arm64.
- It downloads `SHA256SUMS` and the binary, and compares the binary's SHA-256 with the line of `SHA256SUMS`, which must list the file exactly once. Over curl, every request is HTTPS only, redirects included. (`SUITEWARD_RELEASE_BASE`, below, can add a `file://` base for the script's tests, and redirects stay HTTPS only.) wget has no such flag, so its redirects are not restricted: prefer curl.
- With cosign 3.x installed, it first verifies the signature of `SHA256SUMS` against the identity that `scripts/verify-release.ps1` fixes: the release workflow of this repository, at a release tag. Without cosign, it says that the signature was **not** verified, prints `SHA256SUMS sha256: <hex>` and the commands that verify the signature on another machine. When the downloader is BusyBox wget (Alpine, some routers), which can skip the check of TLS certificates, it says that too. `SUITEWARD_REQUIRE_COSIGN=1` turns a missing cosign into an error.
- It puts the file in place in one step (mode 755), says where, and tells you when the directory is not on your PATH. It never uses `sudo`, and it never runs the binary or anything else it downloaded: the next command is yours.

| Variable | Meaning | Default |
| --- | --- | --- |
| `SUITEWARD_VERSION` | The release tag, `<tag>` above. | The latest release, never a pre-release. |
| `SUITEWARD_INSTALL_DIR` | The directory that gets `suiteward`. It must be one you can write: the script never asks for more rights. | `$HOME/.local/bin` |
| `SUITEWARD_REQUIRE_COSIGN` | `1` stops when cosign is not installed. `0` or unset goes on without it. Any other value is an error. | unset |
| `SUITEWARD_RELEASE_BASE` | For the script's own tests only: it replaces GitHub as the origin of `SHA256SUMS` and the binary, so never set it for a real install, and never run an install that someone else set it for. The script takes only a base that starts with `https://` or `file://` (with wget, only `https://`) and refuses anything else, `http://` included, before it downloads. With a base, curl takes only https and file, and follows a redirect to https only. | unset |

Know what this proves. Piping a script into a shell runs it before you read it, so it is as trustworthy as GitHub's HTTPS and the tag you named. Without cosign, the binary is checked against a `SHA256SUMS` that came from the same place: that catches a damaged or cut download, not a release file replaced at its source. If BusyBox wget skipped the certificate check, a network attacker could replace both files. With cosign, the binary is checked against the signature of the release workflow, as in 4.2. The script itself is not verified before it runs. To read or verify it first, download it, verify it as 4.2 says (it is listed in `SHA256SUMS` and has its own signature), read it, and run `SUITEWARD_VERSION=<tag> sh install.sh`. The script is one function that its last line calls, so a download that stops half way runs nothing.

On the isolated host, run the script as the administrator's own user, and then copy the binary where the service account can run it: a path inside a home directory is not readable by it, so `sudo install -m 755 ~/.local/bin/suiteward /usr/local/bin/suiteward`. The isolated host needs no cosign, and without it the script prints the `SHA256SUMS sha256: <hex>` line: compare it with the hash that `verify-release.ps1` printed on your PC (4.2) before you run the binary.

**4.2 Download and verify by hand.** The release page `https://github.com/IgnisDevNE/SuiteWard/releases/tag/<tag>` lists these files:

| File | What it is |
| --- | --- |
| `suiteward_<tag>_linux_amd64`, `suiteward_<tag>_linux_arm64`, `suiteward_<tag>_windows_amd64.exe`, `suiteward_<tag>_darwin_amd64`, `suiteward_<tag>_darwin_arm64` | The installer for each platform. A release built before phase M1.13 has no `darwin_amd64`. |
| `install.sh` | The one-line install script (4.1). A release built before phase M1.13 has none. |
| `SHA256SUMS` | The SHA-256 of the five binaries and of `install.sh`. |
| `SHA256SUMS.sigstore.json` and `<file>.sigstore.json` | The keyless cosign signatures of `SHA256SUMS` and of each binary and `install.sh`, made by the release workflow. |

Download `SHA256SUMS`, `SHA256SUMS.sigstore.json` and the binary of your platform into one folder. Get the release source of `<tag>` as well (clone the repository and check out the tag, or download its source): it holds `scripts/verify-release.ps1`. On a machine with PowerShell 7.2 or later (it also runs on Linux and macOS) and cosign 3.x, run from the release source:

```
pwsh ./scripts/verify-release.ps1 -Blob <folder>/SHA256SUMS -Bundle <folder>/SHA256SUMS.sigstore.json
```

Expect `Verified: … was signed by the SuiteWard release workflow.`, and a line `SHA256SUMS sha256: <hex>`: the SHA-256 of the file you verified. Then check that the binary is the one `SHA256SUMS` lists:

```
pwsh ./scripts/verify-release.ps1 -Sums <folder>/SHA256SUMS -File <folder>/suiteward_<tag>_<os>_<arch>
```

Expect `Matched: … is listed in … with its SHA-256.`, and the same `SHA256SUMS sha256: <hex>` line. If either command fails, do not run the binary. The script fixes the signing identity (`release.yml` of this repository at a release tag, through GitHub's OIDC issuer) and refuses any other. A signature proves that the file came from the release workflow, not that it is the version you meant: you chose the tag.

To check `install.sh` itself before you run it, download it and `install.sh.sigstore.json` as well, and run `pwsh ./scripts/verify-release.ps1 -Blob <folder>/install.sh -Bundle <folder>/install.sh.sigstore.json`.

To run it by name, rename the binary to `suiteward` (`suiteward.exe` on Windows), keep it somewhere only you can change, and put it on your PATH. On Linux and macOS, make it executable first: `chmod +x suiteward`. You can also run it where it is, by its path: on Windows the installer offers to copy itself to a folder on your PATH when it ends (section 5.7), and every command it prints names the program as this run reached it. This guide writes `suiteward` for it. `suiteward version` (also `--version`) prints the version of the binary and the digest of the release image it installs. The binary is the trust root of the install: it creates the secrets and the App, so verify it once and run no other copy. A development build has no embedded digest and needs `--image`.

**macOS and the quarantine flag.** The macOS binaries are not notarized. A file that you download with a browser gets Apple's quarantine flag, and Gatekeeper then refuses to open it. Once you have verified the file, remove the flag: `xattr -d com.apple.quarantine <file>`. A file that `curl` downloads, as the one-line install does, has no such flag.

For the isolated host, verify on your PC as above, then copy the binary and the verified `SHA256SUMS` to the host and compare them there. The host needs neither cosign nor PowerShell:

```
sha256sum --check --ignore-missing SHA256SUMS
```

Expect `suiteward_<tag>_linux_<arch>: OK`. `--ignore-missing` is needed because `SHA256SUMS` also lists the binaries of the other platforms, and the file keeps the name it had on the release page. Then copy the binary to a shared place such as `/usr/local/bin/suiteward`: the service account must be able to run it, and a path inside a home directory is not readable by it.

If your remote shell cannot copy files, download the binary and `SHA256SUMS` on the host with `curl` from the release page. Then compare the `SHA256SUMS sha256: <hex>` line that `verify-release.ps1` printed on your PC with the output of `sha256sum SHA256SUMS` on the host before you run the check above: the two hashes must be the same. [The host runbook](../deploy/HOST.md) shows the commands.

## 5. Install co-located

Run this on the PC that also runs your coding agent. Read section 2 first: on this tier the agent can reach the App key, the database and the TOTP secret. If a coding agent helps you, read [agent-install.md](agent-install.md) first: the agent never runs the install with GitHub, and never runs setup.

**5.1 Start the runtime.** On Windows and macOS, the Podman machine must exist and run (`podman machine init` once, then `podman machine start`). The installer picks the machine's only rootless connection, refuses a rootful one, and never changes your Podman default connection. With several machines, choose one with `--connection <name>`. To see the connections, run `podman system connection list`: the rootless one has `/run/user/<number>/podman/podman.sock` in its URI. The installer records its choice in its settings file, so `suiteward doctor` and a re-run use the same one. Any `podman` command you run by hand against this stack must reach the same store, so run `$env:CONTAINER_CONNECTION = '<name>'` first in that PowerShell session, or `export CONTAINER_CONNECTION=<name>` in a POSIX shell on macOS.

The commands of this guide that run inside the machine write its user as `user`, the user of a Windows machine. On macOS the rootless user of a machine can have another name: the installer and `suiteward doctor` look it up themselves, and the installer prints its commands with it. It is also in the connection's address in `podman system connection list` (`ssh://<user>@…`).

**5.2 Run the installer.**

```
suiteward install
```

It asks only what it cannot decide, and each question also has a flag and an answers-file key (section 12): a question that a flag or the answers file answers is not asked, and has no page. On a real terminal it runs full screen, in the SuiteWard colors, with the steps listed beside the page, from its first question. Each question has a page of its own that explains it (what it is, the choices, the recommended one, the default) and clears what came before; the checks, warnings and kept lines of earlier pages stay. A choice between a few answers is a list: Up and Down move, Enter picks, and the highlighted row is the default. A multiple choice, such as the repositories, has a box per row: Space picks or drops the row under the cursor, and Enter confirms. The hint line at the foot says which keys apply. Ctrl-C at a list or at any other question ends the command with exit code 130, as everywhere else, except at the two offers that come after the work, which count it as No (section 12). Where the terminal cannot read single keys (a system other than Windows, Linux and macOS, or a terminal that refuses the mode), the list is numbered and you type the number, and a multiple choice takes numbers or names, comma-separated. With no terminal, or with `--plain`, it prints plain lines and typed questions instead, with no explanation pages ("The installer's screens", section 12): a question there shows its default in brackets, and Enter takes it. The installer works in this order and changes nothing until the checks pass:

1. **Choices.** On a terminal, it asks for the tier (`co-located`) and the runtime (`podman`; choosing Docker asks you to type `yes` after a page that names its two risks). It then prints `Using <engine> <version> (rootless).`, or `(a rootful daemon)` for Docker: it names the engine it is about to use. Then it asks for the HTTP port (default 27081) and the MCP port (default 27091), both on 127.0.0.1, and the release image. A stack that exists keeps the ports it has: only a new install takes the defaults. The image defaults to the one already installed, else to the release the binary carries. On a full screen the page shows that default as a row of its own, because the reference is long, and the question is just `Release image`: Enter takes the default, as the hint line says. In plain mode the question is `Release image (pinned by digest) [<default>]:`.
2. **Checks.** Both ports are free (inside the Podman machine on Windows and macOS), no stranger's container uses the stack's names, and the database secrets are consistent. It pulls the two images by digest before it removes anything.
3. **GitHub** (section 5.3), if you say yes.
4. **The stack** (section 5.4).
5. **Setup** (section 6), then, on a full screen, a summary that holds the result of `suiteward doctor` (section 5.7).

A stack that the PowerShell start script of v0.2.0 and earlier made is adopted under the same names. Any difference the installer cannot carry over stops the install and prints the diff, so nothing is overwritten silently. A second run keeps the data and the secrets. On Docker it leaves a running, healthy stack with the same settings alone and replaces only what changed or is stopped; on Podman it replaces the containers, with the same settings and data.

**5.3 The GitHub App.** The installer asks `Set up GitHub now (creates a GitHub App in your browser)?` (the default is No). Say yes. Every step below runs on this machine:

1. **Create the App.** The installer first asks which account will own it: `personal` (your own account, the default) or an organization. On a full screen the question is a list of those two, and choosing the organization asks for its login; in plain mode you type `personal` or the login. `--github-account <personal|login>` answers it. For an organization, before the browser opens, the installer asks GitHub, without signing in, whether the account exists and is an organization: a name that GitHub does not know, or a personal account, is refused, and a person is asked again. When GitHub cannot answer (the unauthenticated rate limit of 60 requests an hour for each address, an error of GitHub, no network), the installer warns and goes on, and checks the owner again once the App exists. It does not check that you may create Apps in the organization: that is decided on GitHub's own page. The installer then prints an address on 127.0.0.1, `http://127.0.0.1:<port>/`. Open it in the browser of this machine. It sends the App manifest to GitHub. GitHub asks you to confirm: one click creates the App. The App gets exactly five permissions: Metadata, Contents and Administration read-only, Pull requests and Checks read and write. It has no webhook, it is private (`public: false`), and it is named `suiteward-<random>` unless you rename it. Note the name: if the install cannot finish, you delete that App by hand.
2. **The code.** GitHub redirects your browser to the listener with a one-time code and a random `state` that the installer checks. The installer continues by itself: it converts the code into the App's private key on this machine, and writes the key straight into the secret store, never to disk and never to the terminal. The code passes through your browser, as a downloaded key would: it is valid for one hour, and the installer never prints or draws it.
3. **If the browser cannot reach the listener,** the address bar of the error page still shows the full redirect address. After 2 minutes without an answer, on a terminal (a full screen or plain lines), the installer asks you to paste that address, or only its `code=` value, and press Enter. The question is hidden: nothing you paste is echoed, drawn or kept. Enter with nothing pasted keeps waiting (and takes the answer if it came meanwhile, as when the browser says that SuiteWard received it); the question comes again after another 2 minutes. The wait ends after 15 minutes with an error: run the installer again. The code converts once. With no terminal, the installer waits for the callback only. `--manifest-port <port>` fixes the listener's port (default: any free port). `manifest-code: <code>` in the answers file hands over a code from an earlier attempt, within its hour. It is never a flag, because a flag would show in the process list: the installer refuses `--manifest-code`.
4. **Install the App.** The installer prints `https://github.com/apps/<slug>/installations/new`. Open it, choose the account and give the App access to the repositories to protect, and no others. The installer polls every 5 seconds for up to 10 minutes, and goes on by itself when it sees the installation. It then lists the repositories the App can reach and asks which to protect, at most 20, all of one owner. On a full screen that is a multiple choice with every repository picked when there are 20 or fewer (pick at least 1); in plain mode you type numbers or `owner/name`, comma-separated.
5. **The owner.** Your GitHub username, as in `github.com/<username>`: only that account's `/suiteward` commands and approvals count, and it cannot be an organization or a bot. The default is the App's account when it is a user account.
6. **The test check.** SuiteWard runs no tests: the ruleset of each repository requires your CI's test check beside `SuiteWard / Contract`, so a pull request needs both to pass. For each repository, the installer offers the check runs on the head of the default branch, and you choose one or type the name your CI publishes. On a full screen that is a list of the check runs, with `another name…` as its last row, which asks you to type the name. Enter takes the run that is the only one with `test` in its name (in any case), or the only run there is. With several runs and none or more than one of those names, nothing is highlighted: an Enter is refused with a note until an arrow has picked a row (a numbered list asks again on an empty answer). In plain mode you choose by number or type the name. It never offers a check from a ruleset that does not exist yet. When the repository has no check runs yet, type the name: `suiteward doctor` and `/status` report `missing-test-check` until a check with that name exists. `--test-check 'owner/name=CI / test'` answers it, once for each repository, or give one plain name for a single repository.
7. **The ruleset.** For each repository the installer writes a JSON file, `suiteward-ruleset-<owner>-<name>.json`, into the folder of its settings file (`%APPDATA%\suiteward` on Windows, `~/Library/Application Support/suiteward` on macOS, `~/.config/suiteward` on Linux), and shows where. It requires `SuiteWard / Contract` with your App as the source, and your test check, on the default branch, with branches up to date and an empty bypass list. You import it yourself, because the App has no permission to change rules: open `https://github.com/<owner>/<repo>/settings/rules`, choose **New ruleset**, then **Import a ruleset**, select the file and create it. If an older SuiteWard ruleset (of an earlier App) is there, delete it first: both would apply. For a private repository, the installer first warns that GitHub Free does not enforce rules there (section 3); the install continues.
   - **On a full screen** the installer pauses at a page with one row for each repository, and asks: `I imported it, check now` or `Skip, import later`. The first reads the protection back from GitHub. A repository that is not protected yet (or whose protection cannot be read) stays on the page with what is missing, and the two choices come again; a repository where GitHub does not enforce rules (`enforcement-unavailable`) is not waited for, because no import can help. The second goes on with a warning. The file and the rules page of each repository that is not protected stay in your terminal's scrollback after the screen closes, and the closing checks (section 5.7) report it.
   - **In plain mode and without a terminal** there is no pause page. The installer asks `Press Enter when the rulesets are imported` (the default is yes, and `--ruleset-imported yes` answers it: with no terminal it defaults to yes), then reads the protection back and prints the result of each repository (section 7). A repository that is not protected is reported, and the install goes on: read that result.

In PowerShell, quote a comma-separated list, as in `--github-repositories 'owner/a,owner/b'`: unquoted, PowerShell passes two arguments and the installer stops with a usage error. To use an App you already have, give `--github-app-id`, `--github-installation-id`, `--github-owner` and `--github-repositories`, and the key as `--github-app-key-file <path>` (or `-` for standard input). Create such an App by the checklist in section 10, step 10.4. `--github no` (or `--github-app-id none`) skips GitHub: the stack then runs without it, on both tiers, and a later run with `--github yes` sets it up. Any GitHub value given without an App is refused. On the isolated tier, a run without GitHub is also refused while `~/.config/suiteward/github.env` still names an App, because the service would read that App without its key: empty the file first.

**An App named without its key.** When the settings, the isolated env file (`~/.config/suiteward/github.env`) or the record of an unfinished install name an App whose key secret is not in the secret store (it was purged, or the host was rebuilt), the installer never creates a new App behind your back, and the old one would stay on GitHub. It stops, before any change, and offers two routes:

1. **The App's private key file.** Give `--github-app-key-file <file>`. The installer checks the key with `GET /app` (it must be that App's key; the key of another App is refused), stores it, and goes on. Create a key on the App's settings page if you have none.
2. **A new App.** Give `--github new-app`. The installer names the old App (or Apps) first, with the page that deletes each (the settings only hold the App's id, so it names the lists of Apps of your account and of the organization, and the ID to look for), runs the manifest flow, and prints the same note at the end.

On a terminal, it asks instead, and only the person at the terminal answers (no flag or answers-file key does). A full screen shows a page and a choice between `key file` and `new App`; in plain mode it prints:

```
The App 4242 of the settings has no key in the secret store (suiteward-github-app-key).
  1) give the App's private key file
  2) create a new App (the old one stays on GitHub: delete it at https://github.com/settings/apps (or, if an organization owns it, https://github.com/organizations/<owner>/settings/apps), the App with the ID 4242)
Choose 1 or 2:
```

Without a terminal, the error names both flags. `--github new-app` stops, before it creates anything, while a key secret exists, because the key of the new App cannot be stored over it. The error says whose key the secret holds. If it is the key of the App of the settings, remove the secret (the error gives the command), then run again: that App stops working on this instance, and you delete it on GitHub. If it is the key of an App an earlier run created and recorded, run again without `--github new-app` to take that App up.

**A new App that stops partway.** From the moment a new App exists, its key is the secret and the App is recorded (`settings.yaml.pending-app`). If the run stops after that (the installation does not arrive, Ctrl-C, a prompt), run the installer again with the same answers and without `--github new-app`: the recorded App wins over the App of the settings, so the run takes it up, waits for its installation, and replaces the App in the settings when it succeeds. Nothing creates a third App, and the old App never runs with the new key. While such a record exists, `--github no` and `install --upgrade` refuse (the stack would start with the App of the settings and the key of the recorded one): finish the install first with a plain `suiteward install`, or follow the way out the error names.

**5.4 The stack.** The installer creates the network and the volumes, generates the database owner password, the application role's password and the MCP token, and starts the stack in this order: the database (it waits up to 90 seconds for it to be healthy), the application role `suiteward_app`, the one-shot `suiteward migrate`, then the service, only when migrate succeeded. The service connects as `suiteward_app`, which cannot change the schema; only the migrate container holds the owner's URL. Secrets are written through standard input and never printed. Expect `suiteward migrate finished: the schema is up to date.`, then `Started suiteward-db and suiteward (<image>), GitHub configured.`

The installer also writes a copy of the MCP token for a local agent's client, readable by your user only: `%LOCALAPPDATA%\SuiteWard\mcp-token` on Windows, `suiteward/mcp-token` in your configuration directory elsewhere (`~/Library/Application Support` on macOS, `~/.config` on Linux; `--token-file` changes it). It prints the path, never the token. The MCP listener serves a local agent's `sync` and `status` tools, nothing else.

**5.5 After a restart.** On Podman, the installer enables `podman-restart.service` for the machine's rootless user, so the containers start whenever the machine starts. On a Windows (WSL) machine, Podman creates that user's `~/.config/systemd/user` owned by root, so `systemctl --user enable` is denied; the installer then links the unit as root, which is what `podman machine ssh <machine> 'mkdir -p /home/<user>/.config/systemd/user/default.target.wants && ln -sf /usr/lib/systemd/user/podman-restart.service /home/<user>/.config/systemd/user/default.target.wants/'` does, and checks that `systemctl --user is-enabled podman-restart.service` says `enabled`. It does not start the machine itself: after Windows or macOS restarts, run `podman machine start`. Starting the machine at logon is not part of this release. `suiteward doctor` reports a stopped machine. On Docker, the containers restart when Docker Desktop starts.

**5.6 Setup.** On a real terminal, the installer runs setup for you, as section 6 describes, after it told you to turn off session recording. On a full screen it first shows a page that says what setup does and that the installer never sees the secret, and waits for Enter. On Windows and macOS, setup then opens in a window of its own while the installer waits on its screen; on Linux, over SSH and in plain mode it takes your terminal. It runs setup only when `/status` shows no owner: an owner that is enrolled is never replaced. Without a real terminal, it prints `suiteward enroll` for you to run on one, and stops.

**5.7 Check the instance.** On a full screen the install runs the checks of `suiteward doctor` itself, as its last step, and its summary shows what they found: the counts (`N ok, N warnings, N failures`), and one line with the fix for every result that needs a look (a `WARN`, such as GitHub not configured, or a `FAIL`, such as a ruleset that is not imported yet). A failed check does not fail the install: its exit code stays 0, so read the summary. Ctrl-C while the checks run ends the command with exit code 130; one that came before they started only skips them. In plain mode, and with no terminal, the install does not run them: it prints ``Then check the instance with `suiteward doctor`.`` (an install without GitHub says how to set GitHub up instead), and you run it:

```
suiteward doctor
```

Expect `ok` on every line and exit code 0. On a real terminal the screen draws a `✓` for each `ok`, and the page ends with the counts (`N ok, N warnings, N failures`) and stays until you press Enter. A line that starts with `FAIL` names the problem and its fix (section 12 lists them). A `WARN` line is a pass that needs a look, such as GitHub not being configured: it does not change the exit code. `suiteward doctor --json` prints the same as JSON. It reads the settings file the installer wrote, so with a second instance pass `--settings <path>`.

**The `suiteward` command on Windows.** A downloaded file is not on your PATH, so a command that the installer prints, such as `suiteward doctor`, would not work in a terminal. Every command it prints therefore names the program as this run reached it: `suiteward` where the shell finds this program by that name, else the path of this program, quoted when it needs it (with `&` in front on Windows, which PowerShell needs to run a quoted path). On a full screen, the end of an install or an upgrade also offers to copy the program to `%LOCALAPPDATA%\SuiteWard\bin\suiteward.exe` and to add that folder to the PATH of your user, so that a new terminal finds `suiteward`. The default is Yes; Ctrl-C or the end of the input is a No. The offer is made only when all of these hold: the screen is full, nobody answered for you (`--yes` or `--answers` is not given), the program is a release (a development build is never offered), no copy exists, and the shell does not already find this program by its name. When another `suiteward` is on your PATH, the page names it, the folder goes at the end of the PATH so that program keeps the name, the hints keep naming the path, and the default is No. This terminal keeps the PATH it started with: the summary shows the PowerShell line that adds the folder here, `$env:Path += ';<folder>'`, and new terminals find the command by name. The copy sits in your own profile, so a program that runs as you can replace it (on the co-located tier, that includes the coding agent); it is no safer than the file you ran. Only `suiteward install --upgrade` refreshes the copy, on every screen and also when there is nothing to upgrade, without asking; a copy that a running program holds is renamed to `suiteward.exe.old`, which a later run removes, and a development build leaves it alone and says so. `suiteward uninstall --purge` offers to remove the copy and its PATH entry (the default is No; the offer is not made with `--yes`). Linux and macOS have no such offer: `install.sh` puts the binary in `~/.local/bin` (section 4.1).

**Docker, discouraged.** Install with `--runtime docker`, and confirm with `--accept-docker` (or type `yes` at the prompt that prints the warning). On a PC that also runs Podman, the name `docker` can be Podman's shim, and the default Docker endpoint (the `docker_engine` pipe) belongs to whichever engine started first. So the installer never trusts the name: on Windows it runs Docker Desktop's own CLI by its full path (`C:\Program Files\Docker\Docker\resources\bin\docker.exe`) against the `desktop-linux` context, identifies the engine behind it, refuses Podman's, and says which engine it uses. On macOS it runs the `docker` on your PATH against the same `desktop-linux` context, and refuses Podman's shim the same way. `--connection <context>` selects another Docker context. Docker has no `--replace` and no secret store, so the installer removes a container before it runs it again, and keeps the secrets in one named volume for each container that reads them, mounted read-only, one file for each secret (mode 0400, owned by the uid that reads it), written through standard input. `serve` therefore never sees the owner's URL, and no secret touches the host's disk. Whoever controls the Docker daemon can still read the volumes. Use a port pair of your own (`--http-port`, `--mcp-port`) when a Podman stack runs on the same PC.

**One more instance on the same PC.** The default settings file and token file belong to the first instance. The installer refuses to take them over; give the second one its own `--settings` and `--token-file`, and its own ports.

## 6. Run setup

Setup binds you as the owner and enrolls your TOTP secret. On the co-located tier, the installer runs it for you at the end of the install, on a real terminal: it runs setup in the service container with a terminal, never captures or logs setup's output, and refuses to run it without a real terminal (it prints `suiteward enroll` instead). The isolated install does the same, as `suiteward` in the shell of step 10.3. It never runs unless `/status` shows no owner. Where setup runs depends on the screen:

- **A full screen on Windows or macOS, not over SSH: a window of its own.** A page says what will happen (setup shows the secret once, as text, and the installer never sees it) and waits for Enter. Setup then opens in a new window: a console window of PowerShell on Windows, Terminal on macOS (through a script that deletes itself). The installer waits on its own screen ("waiting for the owner enrollment in the other window"), so it does not look as though it had failed. In the window, add the secret to your authenticator, type the code, then press Enter at `Press Enter to close this window`. The window then clears its screen and its scrollback, so the secret does not stay in it: on Windows the window closes, on macOS the Terminal window stays open and empty, and you close it. The installer learns how setup ended from the exit code that the window leaves, never from what setup printed, and the page it comes back to says that setup finished without repeating anything of it. Ctrl-C at the installer stops the wait, and the command ends as interrupted (exit code 130): on Windows it also closes the window, on macOS you close it yourself. A setup that had already ended well stays a success. The limits: a window that is killed by force clears nothing, so the secret stays on its screen until you close it; and on macOS the installer waits up to 15 minutes for the window, and then says to finish in it or to run `suiteward enroll` again. If no window can be opened, setup runs in the terminal of the installer, as below, and says so.
- **Everywhere else** (a full screen on Linux, an SSH session, the isolated tier, plain mode): setup runs on your own terminal. On a full screen the installer leaves the screen for it, so setup's output is on your normal terminal and the installer's screen keeps none of it; it then waits for you to press Enter, so that you can copy the secret before the screen comes back ("The installer's screens", section 12).

**When the installer runs setup.** After it starts the stack, it reads `/status` (it waits up to about 30 seconds for the service) and acts on what it finds:

- **No owner is enrolled,** and this run knows the owner login and the test checks: on a real terminal it runs setup; without one it prints `suiteward enroll` (section 6, "Enroll again").
- **No owner is enrolled,** and this run does not know them (a re-run that took the App from the settings or the env file): it prints `suiteward enroll`, which asks for the login and takes the test checks from the settings.
- **An owner is enrolled:** it skips setup and the TOTP secret stays as it was, so a re-run, an upgrade, or a restore onto a new machine is safe. It prints `suiteward enroll` for the case where you want to enroll again, for example after losing the authenticator: that is a deliberate command, and it replaces the secret.
- **`/status` cannot be read in time:** it does not run setup, and prints the reason and `suiteward enroll`.

Run setup on the machine that runs the instance you are enrolling: the PC for co-located, the host for isolated. Its `--test-check` options name only the repositories of that instance, and each instance keeps its own owner and secret.

Until setup succeeds, SuiteWard comments nothing and publishes no `SuiteWard / Contract` check, so a pull request shows that required check as waiting. This is expected.

Before you start:

- Have your authenticator app ready.
- Turn off recording of the terminal session. Setup prints the `otpauth://` URI and the base32 secret to its terminal (the window of its own, or yours), and a recorded session keeps them. Make sure that no coding agent can read that terminal. If recording was on, turn it off and run setup again. That replaces the secret, so the old one stops working.

Setup takes your login and one `--test-check` for each configured repository (`suiteward enroll` fills them in, see "Enroll again" below). The check name is the exact name of the required status check that runs your tests, as it appears in that repository's ruleset. Do not use `SuiteWard / Contract`: SuiteWard publishes that check itself. Quote each `--test-check` value, for example `--test-check 'owner/name=CI / test'`: check names often contain spaces and slashes, and an unquoted value would be split by the shell.

```
podman exec -it suiteward /suiteward setup --owner <login> --test-check '<owner/name>=<check name>' --test-check '<owner/name>=<check name>'
```

`suiteward enroll` runs this command for you, on the right engine and connection, so you do not type it. Run it by hand only when `suiteward` is not on the machine: on the co-located PC, in the shell session where you selected the rootless connection (step 5.1), so that `podman exec` finds the stack (on Windows or macOS with Podman, add `--connection <name>`).

Setup then works through these steps:

1. It looks up your login, which must be a user account, and every configured repository. If one lookup fails, it exits with code 1 before it writes anything.
2. It prints a warning and waits. Press Enter to continue. End of input or Ctrl-C exits with code 1, before any secret is created.
3. It prints the `otpauth://` URI and the base32 secret, once. Scan the URI with your authenticator, or type the secret in. The entry is named `SuiteWard:<login> (<tier> <instance>)`, for example `SuiteWard:octo (isolated 1a2b3c4d)`: `<instance>` is the first 8 characters of this instance's id, which `/status` shows as `instance`, so each instance has its own entry. Setup says the same on stderr. If you type the secret in, give the entry that name yourself.
4. It asks for the current 6-digit code. You get three attempts. Three wrong codes exit with code 1 and write nothing.
5. It stores the owner and the secret, records each repository's test check, and checks protection for each repository (section 7).

A missing, extra or duplicate `--test-check` is a usage error: exit code 2, and nothing is written. Setup exits with code 0 once enrollment succeeded, whatever the protection results.

Afterwards, check the result with `suiteward doctor`, or read `/status` at `http://127.0.0.1:<HTTP port>/status` (the HTTP port is 27081 unless you chose another). On the co-located PC the installer prints that address when `/status` answers from this PC, which it did on Windows with a Podman machine when this was checked; when it does not, it prints the command that reads it inside the machine instead (on Windows, `podman machine ssh --username user <machine> "curl -s http://127.0.0.1:<HTTP port>/status"`). On the isolated host, as `suiteward`, run `curl -s http://127.0.0.1:<HTTP port>/status`. The output shows `owner` with your login and the `protection` result of each repository.

Only a setup that prints the protection result of each repository and exits with code 0 counts. Confirm it with `/status`: `owner.enrolledAt` changes on every successful run, so a value that did not change means that setup did not complete. An interrupted setup can leave an authenticator entry that matches nothing: delete it.

Running setup again replaces the TOTP secret, and the new entry has the same name as the old one of this instance. Before you scan it, delete any existing entry with this exact name. Entries of other instances (another id in the name) stay. An older entry named `SuiteWard:<login>`, with no instance in its name, predates this release: delete it once the new entry's code is accepted. Never paste the setup output, the URI, the secret or a code into a chat, an issue or a pull request.

### Enroll again

Enroll again when you lost the authenticator, or when setup ran with session recording on. Run it on a real terminal with session recording off, on the machine that runs the instance:

```
suiteward enroll [--owner <login>] [--test-check '<owner/name>=<check name>' ...] [--settings <path>] [--tier isolated]
```

On the isolated host, run it as `suiteward` in the shell of step 10.3, with `--tier isolated`. Give `--settings` when the install used a settings file of its own. On the co-located tier it needs the settings file, because it replaces the owner's TOTP secret and must not do that to an instance that defaults picked: with none (a stack that `deploy/local.ps1` made), run `suiteward install --upgrade` first, which converts the stack and saves the file. On Linux with no settings file (a host installed by hand), it needs `--tier isolated` (as the service account), as `install --upgrade` does; for a co-located stack, its error says to run `install --upgrade --tier co-located` first, which saves the settings file. The command finds the instance as `install` and `uninstall` do (a stack that this installer did not make, or that was edited, is left alone), reads `/status`, and then runs setup exactly as above, in the window or the terminal of section 6, with its output never captured or logged:

- **It needs a real terminal** on standard input and standard output, because setup prints the TOTP secret. Without one it stops: `enroll needs a terminal: setup prints the TOTP secret`.
- **The service must be running.** When `/status` cannot be read it stops and tells you to start the service: it cannot tell whether an owner is enrolled.
- **The owner login** is `--owner`, else asked, with the enrolled login as the default. It is resolved first, so that the warning below can name it.
- **An owner is enrolled:** it warns that enrolling again replaces the TOTP secret (and the owner `<old>` with `<new>`, when `--owner` is another login), so that the old authenticator entry stops working, and asks you to type `enroll` (on a full screen a page first says that this is a confirmation and that nothing changes until you type it). Anything else, or the end of the input, stops it with nothing changed. There is no flag that answers this question.
- **The test checks** are the `--test-check` values, else the ones of the settings; it asks for a repository that has none. It refuses a check for a repository that the instance does not serve, and `SuiteWard / Contract`, which SuiteWard publishes itself and is never the test check (the install and setup refuse it too).

Then delete the old entry of this instance in your authenticator before you scan the new one, as described above.

## 7. Create the ruleset

SuiteWard reports protection and never changes it, so you set the protection of each repository once, on its default branch. On both tiers, the installer wrote a ruleset file for each repository (`suiteward-ruleset-<owner>-<name>.json`, in the folder of its settings file: `~/.config/suiteward` on the isolated host; section 5.3, step 7, and step 10.4): open `https://github.com/<owner>/<repo>/settings/rules`, choose **New ruleset**, then **Import a ruleset**, and select it. The install waits for the import and reads the protection back (section 5.3, step 7), and the closing checks of a full screen name the file to import for every repository whose protection lacks what the ruleset requires (`suiteward doctor` reports the protection of each repository at any time). The App must be installed on the repository first, or GitHub refuses the import with `Invalid parameter required_status_checks: Invalid integration ids`. On a private repository on GitHub Free, GitHub imports the ruleset but does not enforce it (section 3). Create the rules by hand only when GitHub was skipped, or you did not use the file: open the same address and create a ruleset (or classic branch protection, at `https://github.com/<owner>/<repo>/settings/branches`). Either way, it must do the following:

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
| finding `missing-test-check` | The test check you gave to setup is not required. Before setup, this result appears too. | Require the check, or enroll again with `suiteward enroll` (with `--tier isolated` on the isolated tier). |
| finding `not-up-to-date` | The required checks do not require branches to be up to date. | Turn on the up-to-date requirement. |
| finding `admin-bypass` | Classic protection does not apply to administrators. | Enforce it for administrators. |
| `enforcement-unavailable` | GitHub does not enforce rules on this private repository, or the App cannot read the rules. SuiteWard does not claim protection here. | Either use a GitHub plan that enforces rules on private repositories (GitHub Free does not), or give the App the read permission (the checklist of step 10.4). |
| `unknown` | The protection could not be read, for example because the App's token lacks Administration read. | Check the App's permissions against step 10.4. |
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

**Revision comments.** When a pull request gets a new protected revision that needs approval, SuiteWard posts one comment that states:

- the base version;
- what changed in the protected files, as numbers: the files added, modified and removed; the lines changed, `+N/−M`, counted in some of the files (a binary file, or one that is too large or too different to compare, has no line count); how many files are not shown line by line; and the marker lines seen;
- the changed files with their lines, the most changed lines first, at most 20 (the heading says `20 of N` when there are more);
- the files without a line-by-line diff, at most 10, each with its reason: a binary file, too large to compare, too different to compare, not compared because the work budget of the report ran out, or a diff left out to keep the report within its size budget;
- a link to the full change report (below), with a line that says it opens in a new tab with Ctrl+click (Cmd+click on a Mac, or middle-click): GitHub removes `target="_blank"` from the links of a comment, so no link can force a new tab;
- the copyable `/suiteward approve P<n>-R<m> <6-digit code>`;
- a line saying which earlier revision this supersedes, if there is one.

The comment counts and points out. It never says that the change is right or wrong, and it never says that tests ran. A **marker line** is a line to look at, never a verdict. SuiteWard finds them with simple rules on the lines that changed, by file extension: an assertion line removed (`assert`, `expect(`, `t.Fatal` and the like), a skip line added (`t.Skip`, `it.skip`, `@pytest.mark.skip`), a focus line added (`.only(`), a test declaration removed, and a test file removed. A line that only moved or was reindented gets no marker. Files with other extensions only get the assertion rule. When the revision reduces the scope, a removed file left the protected files and may still exist, so the comment says `removed from the protected files`.

The numbers are counted when the comment is posted, from the revision's stored manifest and the files of both versions that SuiteWard already holds. When they cannot be counted, the comment is posted as it was before this release: it lists the protected files added, modified and removed, up to a bound, and says that the numbers are unknown. SuiteWard never edits a comment, so it stays so. A revision that was recorded before this release has no stored manifest: it gets that list, and no report page.

Each new revision gets a new comment, and SuiteWard never edits one. A pull request with no protected change, and a push that changes only the implementation, gets no comment. An implementation-only push keeps your approval. A push that changes a protected file creates a new revision that needs its own approval.

**The change report.** The link in the comment opens the full report of that revision, one page that your own instance serves at `http://127.0.0.1:<HTTP port>/reports/<owner>/<repo>/P<n>-R<m>`. It shows the revision, its base version and the numbers of the comment, and for every changed protected file its status, its `+N/−M` (or why it has none), its marker lines and its unified diff, with the deleted and added lines. It is one static page in the SuiteWard colors, light and dark, with no script and nothing to load, and it is not an approval. The page stays on your instance: the App never writes contents, so nothing of it goes to GitHub.

- **Where it opens.** The link names the loopback address and the HTTP port of the instance (`SUITEWARD_REPORT_BASE_URL`, which the installer sets from `--http-port`; section 13), so it opens in a browser on the machine that runs the instance. On the co-located PC that is the same PC: on Windows with a Podman machine, the published loopback port answered from Windows when it was checked, so the link opens there. If the port does not answer from your Windows (the installer then prints the command that reads `/status` through the machine, instead of its address), the link does not open, and the numbers in the comment are still there. On the isolated host, forward the host's HTTP port to your PC as for `/status` (step 10.4) and open the link on the forwarded port: the link names the host's port, so forward to the same local port number, or change the port in the address (the page answers on any port).
- **Who can read it.** The page answers only a request whose `Host` is `127.0.0.1`, `localhost` or `[::1]`, so a web page in your browser cannot read it through a name that it made point at 127.0.0.1 (DNS rebinding). That is not a login. Anyone who can reach the HTTP port of the instance can read the reports of the enrolled repositories, which show the protected files of a proposal, as they can read `/status`: on the co-located PC every program that you run, on the isolated host every user of the host.
- **What it reads.** Each request rebuilds the report from the revision's stored manifest, the manifest of its base version, and the bytes of the files that differ, so a revision that a later push superseded still has its page. A file with a side over 1 MiB is not read: the page lists it as too large to compare, with its sizes. A revision whose other files would take more than 32 MiB to read, and a page over 8 MiB, answer `413`. One report is built at a time, and a request waits for its turn up to 20 seconds.
- **When it answers no.** `404`: the repository is not enrolled, the revision is unknown, the revision is the baseline of the first contract (there is nothing to compare it with), or it was recorded before this release. `403`: another `Host`. `503`: the report cannot be built now, with a fixed text; the log of the service has the cause.

The summary of the `SuiteWard / Contract` check states the same numbers (the files, the lines, the files not shown line by line and the marker lines seen) for a revision that has a base version, and says that marker lines are lines to look at, not a verdict. The numbers carry no link and no file name, because a check summary is public.

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
| failure, `integrity-failed` | A canonical artifact no longer matches its integrity record. SuiteWard fails the check on every pull request of that repository and does not promote; it does not block merges by itself — your branch protection does that, if you require the check. Restore the artifact from a trusted copy or a backup, never edit the database, and restart the service: it re-verifies at once. On the isolated host, as `suiteward`, run `systemctl --user restart suiteward.service`: the image did not change, so migrate does not run again. On the co-located PC, restart the service container: `podman restart suiteward` on Podman (with `--connection <name>` if the install used one), `docker restart suiteward` on Docker. (A Docker re-run of `suiteward install` leaves a running, unchanged service alone, so it does not re-verify.) |
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
1. Remove the repository from the list: run `suiteward install --github-repositories '<owner/name>,<owner/name>'` with the repositories you keep (on an isolated host, as `suiteward` in the shell of step 10.3, with `--tier isolated`). The installer verifies the App again, asks the test checks again and writes the rulesets. It skips setup, because an owner is enrolled. A running service keeps the settings it started with, so on an isolated host restart it afterwards with `systemctl --user restart suiteward.service`.
2. Enroll again (section 6, "Enroll again") with one `--test-check` for each repository you keep: `suiteward enroll --test-check '<owner/name>=<check name>' ...` (on an isolated host, with `--tier isolated`). Setup replaces the TOTP secret, so delete the entry of this instance from your authenticator app before you scan the new one (the entry with this exact name, `<tier> <instance>` included; entries of other instances stay), then add the new entry. An entry named only `SuiteWard:<login>` predates this release: delete it once the new entry's code is accepted.
3. Comment your command again. A command written before the new setup is ignored and gets no reply.

SuiteWard changes nothing on GitHub: remove the App from the repository, and the ruleset rule that requires `SuiteWard / Contract`, yourself.

## 10. Running on a separate host (operators)

This installs the isolated tier (section 2): SuiteWard on a separate host that your coding agent cannot reach, for whoever self-hosts on a machine the agent cannot reach. It gives the full assurance. The host runs AlmaLinux 9 and Podman 5.5 or later, rootless, for an unprivileged account that this guide calls `suiteward`. Only Podman is supported here.

The install is split by privilege, and the installer never runs as root. `sudo suiteward install host` does the administrator steps once. `suiteward install --tier isolated`, run as the account `suiteward`, does the rest, with the same GitHub flow as the co-located tier. Each step below says who runs it.

The host runs three container units: `suiteward-db` is PostgreSQL, `suiteward-migrate` is a one-shot `suiteward migrate` that runs as the database owner, and `suiteward` is the service, which requires the migration and connects as the least-privilege role `suiteward_app`. Starting the service always migrates first.

**10.1 Get the installer onto the host (as the administrator).** Verify the binary on your PC and copy it to the host as section 4 says: `sha256sum --check --ignore-missing SHA256SUMS`, then a copy at `/usr/local/bin/suiteward`.

**10.2 Prepare the host (as the administrator).** Run:

```
sudo suiteward install host
```

It asks before it changes anything that you did not ask for, and does these steps in this order:

1. **Podman.** It checks that Podman runs, is 5.5 or later, and has the fix for CVE-2026-57231. A distribution can backport that fix without changing the version, so it reads the package changelog. If something is missing, it asks before it runs `dnf install -y podman` or `dnf upgrade -y podman` (`--yes` answers yes). It refuses to go on when the fix is still missing: do not run SuiteWard on that host.
2. **The account.** It creates `suiteward` with no login shell if it does not exist, gives it subordinate user and group id ranges that no other account uses, and enables linger, so the account's services start at boot and run without a login.
3. **The metadata rule.** On a host where the cloud instance metadata address `169.254.169.254` answers, it asks whether to add the nftables rule that refuses that address for the `suiteward` account (`--metadata-rule yes` or `no`). The metadata service hands out the instance's credentials, and rootless containers connect as a process of the service account, so a rule for that account covers them. The rule loads at every boot, through the unit `suiteward-imds-block.service`, and covers IPv4 only. It never adds the rule without a yes. When nothing answers at that address, it says so and adds nothing. `suiteward doctor` reports an unrefused address.
4. **The account's folder.** It creates `~/.config/suiteward` for `suiteward`, mode 0700, where the GitHub settings and the ruleset files go. A GitHub env file from a hand-made install (`/etc/suiteward/github.env`) is copied there, and the old file stays until the service has moved to the new one.

It ends by printing the command for the next step, with the account's user bus filled in: the path of the binary, then `install --tier isolated`. The command carries `--image sha256:<signed index digest>` only when the binary is a development build, because a release binary carries its own digest. `--answers <file>` gives the answers from a file (section 12).

**10.3 Open a shell as `suiteward` (as the administrator).** The account has a `nologin` shell, so `sudo -iu suiteward` fails. Open a shell with its user bus, and start every new session this way:

```
r=/run/user/$(id -u suiteward)
sudo -u suiteward -H env XDG_RUNTIME_DIR=$r DBUS_SESSION_BUS_ADDRESS=unix:path=$r/bus bash
```

Check it with `whoami`, which prints `suiteward`. The next steps run in that shell, unless they say they run on your PC. Turn off session recording and Session Manager logging before the steps that print a secret: setup prints your TOTP secret, and a logged session keeps it.

**10.4 Install the stack (as `suiteward`).** Run:

```
suiteward install --tier isolated
```

Pass `--tier isolated`: the default tier is `co-located`. The installer asks for what it needs and works in this order. A refusal in the first four steps changes nothing:

1. It refuses to run as root, or as any account but `suiteward`, and checks the account's systemd user manager and linger.
2. It takes the ports and the job timeout from `--http-port`, `--mcp-port` and `--job-timeout` (or the answers file). Given on a re-run, they change the installed stack, the run says what changes, and a running service is restarted to read them; not given, the installed values are kept. It refuses a port that something already listens on (`ss -ltn`), except the ports of this stack while its service runs: every WSL2 distro shares one network with the others and with a Podman machine. Without `ss` it notes that the ports were not checked; any other failure of `ss` stops the install.
3. It adopts an existing install (step 10.6), and decides the image: `--image` if you give it, else the one installed, else the release the binary carries. A development build has no release, so it needs `--image sha256:<index>`: take the index digest from the release notes and verify it first, from the release source on a machine that has cosign 3.x (`pwsh ./scripts/verify-release.ps1 -Reference ghcr.io/ignisdevne/suiteward@sha256:<index>`).
4. It pulls the PostgreSQL image and the release image by digest.
5. **GitHub**, the same flow as section 5.3, with the choice of App described below.
6. It checks that there is an App key to store, and prints its fingerprint when you give the key of an App that exists (a key that the flow created and stored itself is not printed): `SHA256:` followed by base64 text. Compare it with the fingerprint that the App's settings page shows for that key. A wrong or revoked key looks right to every other check, and GitHub calls then stop about an hour later. An install that names an App but has no key for it, given or stored, is refused here, before any change. An install without GitHub needs no key: its service unit mounts none.
7. It writes `~/.config/suiteward/github.env` (the three GitHub values; they are not secrets), then creates the Podman secrets that are missing: the database password and the owner's URL, the MCP token and the App key. It never prints them, and it never replaces a database password that exists, because the database keeps it in its volume.
8. It writes the six unit files into `~/.config/containers/systemd/`, runs the Podman generator's dry run to be sure Podman reads every one of them, and reloads systemd.
9. It starts the database, waits until it is healthy, creates the application role `suiteward_app` with its URL secret, starts the service (which runs the migration first) and checks that the three units are active. It then waits until `/readyz` answers four times in a row, two seconds apart, while systemd counts no restart of the service; a service that keeps restarting fails the install, and the error names `journalctl --user -u suiteward.service`. Only then does it save its settings in `~/.config/suiteward/settings.yaml`.
10. Setup (step 10.5).

*Create the App through the manifest* (`--github yes`, or yes at the prompt). The browser of your PC must reach the installer's listener on the host, which listens on 127.0.0.1 only. Pin the port with `--manifest-port <port>` (for example 8765), so you can start the forward before the installer asks. Without it, the installer picks a free port and prints the forward command with that port. From your PC, with the AWS CLI and the Session Manager plugin:

```
aws ssm start-session --region <region> --target <instance-id> --document-name AWS-StartPortForwardingSession --parameters '{"portNumber":["<port>"],"localPortNumber":["<port>"]}'
```

Leave it open and open `http://127.0.0.1:<port>/` in the browser of your PC. Use the same number for `portNumber` and `localPortNumber`, and for the browser: the listener answers 403 to any other Host, and GitHub redirects to `127.0.0.1:<port>`. On a host that is not on AWS, use your own forward, for example `ssh -L <port>:127.0.0.1:<port> <host>`. From there, the steps are those of section 5.3: confirm the App on GitHub, install it on the repositories to protect, choose the repositories, the owner and the test checks. If the forward is not up, the paste fallback of section 5.3 works in the Session Manager terminal: after 2 minutes the installer asks you to paste, hidden, the address that the browser's error page shows, or its `code=` value. The App key goes from the response of GitHub straight into the Podman secret store of the host.

*Use an App that exists* (`--github-app-id`). Create it by hand first, by the checklist below, and give `--github-app-id <app id> --github-installation-id <installation id> --github-repositories '<owner/name>,<owner/name>' --github-owner <login>`, with one `--test-check 'owner/name=<check name>'` for each repository. The key comes from `--github-app-key-file <path>`, from `-` (standard input; then every other answer must be a flag or in the answers file), or from `github-app-key` in the answers file (mode 600): never as an argument. To paste the key without echo into a file, run this in the shell of step 10.3, wait until the cursor stops, paste the whole PEM, press Enter, then press Ctrl-D. You see nothing while you paste:

```
trap 'stty echo' INT; stty -echo; (umask 077; cat > ~/app-key.pem); stty echo; trap - INT
```

Then give `--github-app-key-file ~/app-key.pem`, and delete the file afterwards in the same shell, `shred -u ~/app-key.pem`, and your own copy of the `.pem`. The App checklist:

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
- [ ] Generate a private key. GitHub downloads a `.pem` file. Keep it somewhere safe until the installer has stored it. Never commit it. Do this before the next item: the private key must exist before GitHub allows the installation.
- [ ] Install the App on the repositories you want to protect, and no others. The limit is 20.
- [ ] Note the **App ID** (on the App's settings page) and the **installation ID** (the number in the address of the installation's settings page).
- [ ] Optional: upload [`assets/brand/png/suiteward-github-app.png`](../assets/brand/png/suiteward-github-app.png) as the App's logo, with the badge background color `#12202F`, so its comments and checks carry the SuiteWard mark.

Keep these permissions off, for these reasons:

- **Administration: Write** stays off. SuiteWard reads protection and never changes it. You import the ruleset yourself (section 7).
- **Contents: Write** stays off. SuiteWard reads code and never commits. The `.suiteward.yml` declaration is yours to commit.
- **Issues** stays off. Pull request comments are covered by the Pull requests permission.
- **Webhooks** stay off. This release polls GitHub and accepts no inbound webhooks.
- **Any other permission** stays at No access. SuiteWard does not request it.

*The ruleset.* For each repository, the flow writes a ruleset file on the host, `~/.config/suiteward/suiteward-ruleset-<owner>-<name>.json`, and prints its path and the Import steps of section 5.3 (step 7), with the Free-plan warning for a private repository. The Import is a click in your browser, so get the file to your PC: print it in the Session Manager session with `cat ~/.config/suiteward/suiteward-ruleset-<owner>-<name>.json`, copy the text and save it as a file on your PC, or copy the file over with your own tool. Then import it. On a full screen the installer pauses at a page with the choices `I imported it, check now` and `Skip, import later`, and reads the protection back when you say so; in plain mode it waits at `Press Enter when the rulesets are imported` and then reads it back. Without a terminal it does not wait: `ruleset-imported` defaults to yes, so give `--ruleset-imported yes` knowingly, and read the protection result.

**10.5 Setup and check (as `suiteward`).** On a real terminal, the installer runs setup for you at the end, after it warns you to turn off session recording: it passes the terminal through and never captures or logs setup's output. Without a real terminal it prints `suiteward enroll --tier isolated` to run on one. Section 6 says what setup does, and when the installer skips it. On a full screen the summary has already run the checks of `suiteward doctor` (section 5.7); otherwise run `suiteward doctor` and expect `ok` on every line.

The ports are published on the host's loopback only (27081 and 27091 unless you chose others; a host installed before M1.14 keeps 8081 and 8091, because an upgrade never moves a port). To read `/status` from your PC, forward the port with your own remote-access tool (on a host that is not on AWS, for example `ssh -L 27081:127.0.0.1:27081 <host>`), or on AWS for example `aws ssm start-session --target <instance-id> --region <region> --document-name AWS-StartPortForwardingSession --parameters portNumber=27081,localPortNumber=27081`. SuiteWard provides no remote access.

**10.6 Re-runs and a host installed by hand.** A second run creates only what is missing, applies the ports and job timeout you give (step 10.4, item 2), and restarts a running service when its ports, job timeout or GitHub settings change. A host from the v0.2.0 install guide is adopted: the installer compares the installed units with the units of the release, carries over the tier, the ports and the job timeout when you give none, and moves the GitHub env file from `/etc/suiteward/github.env` to the account's folder (step 10.2 copies it, and the installer prints the `sudo rm` line for the old file once the service uses the new one). Any other difference stops the run and prints the diff: nothing is overwritten silently. When the settings file holds no GitHub values, the installer reads them from `~/.config/suiteward/github.env`. It parses that file strictly: exactly the three keys once each, a positive App id and installation id, and one to 20 `owner/name` pairs of one owner. A file that is not that stops the install before any change, and the error names the file and the line. So a host with a GitHub App already is never offered a second one. For a release that differs from the installed one, use `install --upgrade` (section 11).

For the manual procedure of the v0.2.0 release, which this guide no longer repeats, see [the install guide at the tag v0.2.0](https://github.com/IgnisDevNE/SuiteWard/blob/v0.2.0/docs/install.md).

## 11. Updating

Pin one release, and update when you choose. Updates are always explicit, by digest. Automatic updates are not supported: Podman's auto-update cannot update the migration unit, so an automatic update would start the service alone on an old schema, which the service refuses, and loop. Never add `AutoUpdate=` to a unit or enable `podman-auto-update.timer`.

Get the new release's installer as section 4 says, and verify it if you choose to (on Linux and macOS, the one-line install replaces the binary in its install directory: run it with the new tag in `SUITEWARD_VERSION`; on the isolated host, copy the new binary to `/usr/local/bin/suiteward` again, as 4.1 says), then run it with `--upgrade`:

```
suiteward install --upgrade
```

On the isolated host, run it as `suiteward` in the shell of step 10.3. The release is the one the binary carries, or `--image sha256:<index>` (a development build needs it). `--yes` answers the question about the upgrade plan only (it never starts a service that is down, see step 2, and never makes a recovery key, see step 3), `--backup-dir <directory>` chooses where the backup file is made, and `--backup-recipient age1pq1...` gives the public recovery key where no terminal can be asked. An unattended `suiteward install --upgrade --yes` therefore needs a recovery key, which releases before M1.13 did not: run `suiteward backup init` once first ([the backup runbook](backup.md#the-recovery-key-once)), or pass `--backup-recipient`. Without a key and without a terminal the upgrade refuses before it changes anything, because a backup is never written in plaintext. The upgrade takes these steps, in order:

1. **It adopts what is installed.** A difference it cannot carry over stops the run, with the diff, before any change. An upgrade keeps the installed ports and job timeout: `--http-port`, `--mcp-port` or `--job-timeout` with another value stop it before any change, and the message names the re-run of `suiteward install` that changes them. The tier comes from the settings file. On Linux with no settings file (a host installed by hand), pass `--tier isolated` as the service account, or `--tier co-located`.
2. **It makes sure the service answers, shows what changes** (the release, and the PostgreSQL pin when it moves) **and asks.** When the stack already runs this release, it says `Nothing to upgrade`. When the service does not answer `/status`, which the backup records, it first asks on a terminal: `The service is not running, so the backup cannot record its status. Start the installed service now?` (a choice between Yes and No on a full screen, with Yes highlighted; `[Y/n]` in plain mode). On a yes it starts the installed stack as it is, without changing the release (isolated: `systemctl --user start suiteward.service`; co-located: `start` on the existing containers, the database first, and nothing is created again), waits up to about 90 seconds for `/status` (at most 60 probes, one a second, within that deadline), and goes on to the plan and its question. A no to the plan after it started the service stops with the note that the service was started and nothing else was changed. On a no, at the end of the input, without a terminal, or when the start does not help, it refuses and names the command that starts the service, or the [manual backup](backup.md#isolated-host-bash-as-suiteward) (Appendix A of the backup runbook; [section 2](backup.md#2-back-up) points to it). `--yes` does not answer this question and never starts the service.
3. **An encrypted backup first,** as one new file, `suiteward-backup-<date>-<time>.tar.age`, in the backup folder, readable by your user only (mode 0600, or an owner-only ACL on Windows, granted to your user's SID). It is the backup that `suiteward backup` makes, with the same members and the same rules ([the backup runbook](backup.md#2-back-up)): `status.json` (the release the files belong to), `suiteward.sql` (a `pg_dump` of the database), `suiteward-artifacts.tar` (the artifact volume), the settings and, on the isolated tier, the unit files. It is encrypted to your recovery key, so the TOTP secret is never in plaintext, and only the kit opens it. The key is the one in the settings. With none, the upgrade settles it before it starts anything, a service that is down included: on a terminal it asks whether to make one, as `suiteward backup init` does (`--yes` does not answer that question); without a terminal it takes `--backup-recipient age1pq1...`, or `backup_recipient` in the answers file, which is stored in the settings when the upgrade is done; and otherwise it refuses. A key that differs from the one in use is refused: replacing it is `suiteward backup init --replace`. The default folder is `backups` beside the MCP token copy on the co-located tier (so `%LOCALAPPDATA%\SuiteWard\backups` on Windows, not the roaming profile, and `~/Library/Application Support/suiteward/backups` on macOS), and `~/.config/suiteward/backups` on the isolated tier. On the isolated tier the unit files that the upgrade replaces are also kept as plain files, which hold only the names of secrets, in the directory `suiteward-backup-<date>-<time>.units` beside the archive. The service must be running: the backup reads its `/status`, so a service that is down is started first (step 2) or the upgrade waits until it is up. A backup that fails leaves nothing behind and changes nothing; when the upgrade had started a service that was down, its message says that the service it started is running and that nothing else was changed. When the stack already runs this release, the upgrade says `Nothing to upgrade` and needs no key.
4. **It pulls the new images, and replaces the release.** Co-located, it recreates the containers as an install does: database, role, migrate, then the service. Isolated, it stops the service, writes the new units, checks them with the generator's dry run, reloads systemd, restarts the database when its image changed, restarts the migration unit (which migrates), starts the service and waits until it is stable, as an install does (step 10.4, item 9). An `/etc` env file of a hand-made install is moved to the account, and the installer prints the `sudo rm` line for the old one.
5. **It checks, and ends with the way back.** On a full screen the summary runs the checks of `suiteward doctor` and shows their counts, as an install does (section 5.7); in plain mode, run `suiteward doctor`. Check that `version` in `/status` is the release you meant. The upgrade also prints the way back for its tier, with your paths filled in: the commands of [the backup runbook](backup.md#going-back-after-an-upgrade). On Windows it refreshes the copy of the program on your PATH, if you made one (section 5.7).

A co-located `suiteward install --image <another release>` over a running stack refuses, and points to `--upgrade`. So does the isolated install for an installed image.

*If a step fails.* The message says how far the upgrade got.

- **Before migrate**, the schema and the data are unchanged, and the message names the backup and the exact commands. Co-located: run `suiteward install` with the options of this run to start the release in the settings again, or `suiteward install --upgrade` to retry. Isolated, with the service stopped: to go back, copy the files of the `.units` directory beside the backup (the message names it) to `~/.config/containers/systemd/`, run `systemctl --user daemon-reload` and `systemctl --user start suiteward.service`. To go on once the cause is fixed, run `systemctl --user daemon-reload`, then `systemctl --user restart suiteward-db.service` (only if the database image changed), then `systemctl --user restart suiteward-migrate.service` and `systemctl --user start suiteward.service`.
- **From migrate on,** migrations only go forward. An older image refuses to start on a newer schema, and nothing migrates it back. The way back is `suiteward restore` of the backup, with its kit, on the previous release, which loses everything after the backup ([the backup runbook](backup.md#going-back-after-an-upgrade); that way has not been rehearsed end to end). Do not start an older image on the upgraded database. On the isolated tier the units then name the new release while the settings still name the old one, so a second `--upgrade` says `Nothing to upgrade`: restore the backup instead of retrying.

An upgrade from a release before M1.13 changes four things you will see. The backup is encrypted and needs a recovery key (above), so an unattended `install --upgrade --yes` fails until you make one. Ctrl-C and SIGTERM end the installer commands with exit code 130 (section 12). The service gets `SUITEWARD_REPORT_BASE_URL`, so the revision comments of new revisions link their report; a Docker re-run of `suiteward install` over such a stack keeps a running service without it, and only `install --upgrade` adds it (section 12, "What the installer does"). And the revisions recorded before the upgrade have no stored manifest, so a comment of theirs that is delivered after the upgrade shows no numbers, and their report page answers 404.

Read the release notes first. An instance on v0.1.1 or older upgrades to v0.1.3 first, and then to v0.2.0: later releases no longer read the comment payloads that v0.1.1 or older queued. A co-located stack that the PowerShell start script of v0.2.0 and earlier made is converted by `--upgrade`, under the same names, even when it already runs this release. An isolated host from v0.1.x has no migration unit, and `--upgrade` does not convert it: follow [the v0.2.0 guide](https://github.com/IgnisDevNE/SuiteWard/blob/v0.2.0/docs/install.md) to v0.2.0 first.

### Uninstalling

```
suiteward uninstall
```

It first says which instance it is about to remove (the instance id that `/status` shows, the engine, and the settings file), asks (`--yes` skips the question), then removes the stack and keeps the data. On a full screen a page names the instance and says that nothing is removed until you confirm; a plain uninstall asks Yes or No (a choice with No highlighted), and a purge shows one red statement of what it deletes (the database, the artifacts, the secrets and the settings of that instance) and asks one question, to type the instance id (below):

- **Co-located:** the three containers (`suiteward`, `suiteward-migrate` and `suiteward-db`). The volumes, the secrets and the settings stay: run `suiteward install` again to start the stack. `--purge` also removes the volumes (the database and the artifacts), the secrets, the network, the MCP token copy and the settings file.
- **Isolated,** run as `suiteward` in the shell of step 10.3: it stops the services and deletes the six unit files. `--purge` also removes the secrets, the volumes, the network, `~/.config/suiteward/github.env` and the settings file, and stops any SuiteWard service that an earlier uninstall left running without its unit file (alpha.1 left the network service so, and the next install then failed with `network not found`). It never touches `/etc`: when the installed units read `/etc/suiteward/github.env`, it prints the `sudo rm` line for an administrator. With no settings file on Linux, a host installed by hand needs `--tier isolated`.

`--purge` deletes your TOTP enrollment and every contract version, so back up first (`suiteward backup`, [the backup runbook](backup.md)). It keeps every backup in the backup folder, those of `suiteward backup`, of `--upgrade` and of `suiteward restore` alike, but it removes the settings file, and with it the public half of the recovery key: after a reinstall, make a new key (`suiteward backup init`), and keep the old kit, which still opens the old backups. It removes the record of an unfinished GitHub App (`settings.yaml.pending-app`, see Troubleshooting). The command leaves a stack that this installer did not make, or whose units or containers were edited, alone. On Docker, it uses the runtime and connection that the install recorded. SuiteWard changes nothing on GitHub: delete the App, and the ruleset rule that requires `SuiteWard / Contract`, yourself. The account `suiteward`, the metadata rule and Podman stay on the host; remove them with the administrator's own tools if you no longer need them.

**`--purge` needs the instance id, as `suiteward restore` does.** On a terminal it asks you to type the id of the instance it names. Where nothing can ask (a script, an agent's session) or with `--yes`, pass it: `suiteward uninstall --purge --yes --instance <id>`, the 8 characters shown as `instance` in `/status` (`http://127.0.0.1:<http port>/status`, 27081 by default). A wrong id removes nothing. When the service does not answer, the id is read from the database; when neither names it (the usual state after a plain `suiteward uninstall`, which leaves no database container), the id is `unknown` (the line that names the instance adds what the database answered), which you type or pass on purpose. With no settings file and the database volume `suiteward-db-data` still there, `--yes` refuses `unknown`, because no id would be checked against the data it deletes: type the word on a terminal, or pass `--settings` with the settings file of the instance (a plain uninstall keeps it). With no database volume left, `--yes --instance unknown` works. A plain `suiteward uninstall` keeps its yes-or-no question.

On Windows, a purge on a full screen ends with an offer to remove the copy of the program and its PATH entry that an install made (section 5.7), when there is one: the default is No, `--yes` does not ask, and a copy that a running program holds is renamed to `suiteward.exe.old`, which you delete by hand.

**The settings file decides which instance this is.** A file that you name with `--settings` must exist: a mistyped path stops the command (exit 1, naming the path) before it calls the container engine, in every command that reads the file (`uninstall`, `enroll`, `backup init`, `backup`, `restore`, `install --upgrade` and `doctor`). Only a plain `install`, which writes the file, may name one that is not there yet. With no `--settings` and no file at the default path, `enroll` on the co-located tier refuses and points to `suiteward install --upgrade`, and `uninstall` and `restore` do not run on the defaults of this PC, which may be another instance: name the engine, `--runtime podman --connection <name>` (`--connection=` for the default one), as the way to remove a stack whose settings were never saved, for example after an install that failed early; or give `--tier isolated` as the service account. The file must also hold both ports (`http_port` and `mcp_port`, which the installer always writes): a file without them is refused, because a default port would move a stack that exists to another port at the next run. A stack keeps its ports across releases; only a new install takes the defaults, 27081 and 27091.

### Rotating the App key

Rotate the key when it may have been seen by something it should not, for example by a coding agent. Do it yourself, from a terminal that no agent can reach. A co-located install gives the agent access to the key anyway (section 2): there, rotation helps against a past exposure and a key that left the machine, not against an agent that still shares your PC user. The installer has no command for rotation: once the key secret is gone, a run with the key file (`--github-app-key-file`) verifies that App again, and a run without it stops and offers the key file or a new App (section 5.3). Replace the secret by hand.

**On macOS,** run the co-located commands below in your own shell instead of PowerShell. Select the connection with `export CONTAINER_CONNECTION=<name>` (step 5.1). In R.5, create the secret with `podman secret create suiteward-github-app-key - < <path of the new PEM>` and delete the file with `rm`. Run the `openssl` pipelines of R.4 and R.6 as they are, with your machine's user in place of `user` in `podman machine ssh --username` (step 5.1). macOS is not validated live yet, so check each fingerprint as the steps say.

The order matters. After you replace the key, the service keeps working for up to an hour on installation tokens that it already holds. A wrong key therefore shows up late, when every GitHub call stops. So check the fingerprint at each step, and delete the old key last.

**R.1 Generate a new key.** On the App's settings page, under **Private keys**, generate a private key. GitHub downloads a `.pem` file and shows the key's fingerprint, `SHA256:` followed by base64 text. Note it. Leave the old key in place.

**R.2 Stop the service and remove the old secret.**

- Isolated host, as `suiteward` (a shell opened as in step 10.3):

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

**R.3 Put the new key in a file.** On the isolated host, paste it without echo, as in step 10.4 (*Use an App that exists*). On the PC, use the `.pem` that GitHub downloaded.

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

Every command prints its flags with `--help`. The exit code is 0 on success, 1 on a failure, and 2 on a usage error. `install`, `install host`, `uninstall`, `enroll`, `backup init`, `backup`, `restore` and `doctor` exit with 130 when you interrupt them (Ctrl-C, or SIGTERM): the message says what the step left behind and how to clean it up (`doctor --json` keeps the code of its checks). Any error that ends a command after the interrupt is reported that way, so a restore that you interrupt during the load of the dump exits 130 too, with the message of where it stopped; the database stays as it was, because the load is one transaction that is committed only at the end ([the backup runbook](backup.md#3-restore)). `suiteward version` (also `--version`) prints two lines, the version of the binary and `image: <digest>`, the release image it installs by default (`none (development build)` for a development build, and `not recorded in this binary` for the binary inside the release image, which is built before its digest exists). It reads no setting, and any argument after it is a usage error (exit code 2).

### Commands and flags

`suiteward install`:

| Flag | Meaning |
| --- | --- |
| `--answers PATH` | Read answers from a YAML file. A flag wins over the file. |
| `--settings PATH` | The installer settings file (default: `suiteward/settings.yaml` in your per-user configuration directory). A file that you name must exist, except for a plain `install`, which writes it (see "Uninstalling"). An empty value (an unset shell variable) is a usage error, never the default file. |
| `--token-file PATH` | Where the copy of the MCP token for a client goes (co-located). |
| `--tier` | `co-located` (default) or `isolated`. The isolated install passes it, and `--upgrade` on Linux with no settings file needs it. |
| `--runtime` | `podman` (default, recommended) or `docker` (co-located only). |
| `--accept-docker` | Confirm that Docker may be used although Podman is recommended. |
| `--connection` | The Podman connection, or the Docker context (default: the rootless Podman machine, or Docker Desktop's). |
| `--image` | The release image pinned by digest: a bare `sha256:<64 hex>` (the release image `ghcr.io/ignisdevne/suiteward`), or `repo[:tag]@sha256:<64 hex>`, never a tag alone. This holds on every command that takes it. The isolated tier runs `ghcr.io/ignisdevne/suiteward` only; a local image is for co-located. Default on both tiers: the installed image, else the release the binary carries. A development build carries none, and needs it. With `--upgrade`: the release to move to. |
| `--http-port`, `--mcp-port` | The loopback ports of the two listeners (defaults 27081 and 27091; a stack that exists keeps its own). On a re-run they change the installed stack; `install --upgrade` refuses a change on both tiers. |
| `--job-timeout` | A Go duration for `SUITEWARD_JOB_TIMEOUT` (the service only). On a re-run it changes the installed stack; `install --upgrade` refuses a change on both tiers. |
| `--github` | `yes`: create the GitHub App now. `no`: skip GitHub. `new-app`: create a new App although the settings (or the isolated env file, or the record of an earlier run) name one: the old App stays on GitHub, and the installer prints where to delete it. It stops when the key secret of the old App still exists: remove that secret first. |
| `--github-account` | The account that owns the new App: `personal`, or an organization login. |
| `--manifest-port` | The port of the manifest listener on 127.0.0.1 (section 5.3). The manifest code is not a flag: it is `manifest-code` in the answers file, or pasted at the prompt. |
| `--github-app-id` | The ID of an App that exists, or `none` to skip GitHub. |
| `--github-installation-id` | The installation of that App. |
| `--github-owner` | The GitHub login of the instance owner. |
| `--github-repositories` | A comma-separated `owner/name` list, at most 20. |
| `--github-app-key-file` | A file with the App's private key (PEM), or `-` for standard input. Read only while the key secret is missing. Also the way out when the settings (or the env file, or the record of an unfinished App) name an App whose key secret is gone: the key must be that App's, which `GET /app` checks. |
| `--test-check` | The test check of a repository, `OWNER/NAME=CHECK`. Repeat it, or give one plain name for a single repository. |
| `--ruleset-imported` | `yes` once the ruleset of each repository is imported: it skips the wait. Without a terminal it defaults to yes. |
| `--upgrade` | Back up (encrypted), then move to the release the binary carries, or `--image` (section 11). |
| `--yes` | With `--upgrade`: do not ask before backing up and upgrading. It does not start a service that is down, and it does not make a recovery key: those are questions of their own (section 11). |
| `--backup-dir` | With `--upgrade`: the folder the backup file is made in (default: `backups` beside the MCP token copy, or in `~/.config/suiteward` on the isolated tier). |
| `--backup-recipient` | With `--upgrade`, where no terminal can be asked: the public recovery key, `age1pq1...` (about 2000 characters), which the backup is encrypted to and which is stored in the settings. The answers-file key is `backup_recipient`. Another key than the one in the settings is refused: use `suiteward backup init --replace`. Any other command refuses it. |
| `--plain` | Print plain lines instead of the full-screen window, also on a terminal (see "The installer's screens"). |

`sudo suiteward install host`: `--answers PATH`, `--yes` (answer yes to installing Podman with `dnf`), `--metadata-rule yes|no` and `--plain`.

`suiteward uninstall`: `--purge`, `--yes`, `--instance ID` (only with `--purge`, or it is a usage error: the instance id, which `--yes` needs and a terminal asks you to type), `--settings`, `--token-file`, `--tier`, `--runtime`, `--connection` and `--plain`, as above. On the isolated tier, give `--tier isolated` when there is no settings file; on the co-located tier, give `--runtime` and `--connection` then.

`suiteward enroll`: `--owner LOGIN`, `--test-check OWNER/NAME=CHECK` (repeat it), `--settings PATH`, `--tier` (give `--tier isolated` as the service account) and `--plain`. It needs a real terminal and a running service, and asks you to type `enroll` before it replaces the TOTP secret of an enrolled owner (section 6, "Enroll again").

`suiteward backup init`: `--kit PATH`, `--yes`, `--replace`, `--settings PATH`, `--token-file`, `--tier`, `--runtime`, `--connection` and `--plain`. It writes the recovery kit and stores the public key in the settings. `--kit` names the kit file (default: `suiteward-recovery-kit-<instance>.txt` in the current directory), and a file is never overwritten. `--yes` says, where no terminal can ask you to type `stored`, that the kit is copied off this machine. `--replace` replaces a key that is set. It needs the settings file and a service that answers `/status`.

`suiteward backup`: `--to DIR` (default: the backup folder of section 11), `--settings PATH`, `--token-file`, `--tier`, `--runtime`, `--connection` and `--plain`. It takes the encrypted backup of the running instance, with the key of the settings.

`suiteward restore <backup.tar.age>`: `--kit PATH`, `--yes` with `--instance ID`, `--backup-dir DIR`, `--plaintext-dir DIR` (instead of the file, for the directory of an earlier release), `--settings PATH`, `--token-file`, `--tier`, `--runtime`, `--connection` and `--plain`. The key of the kit is never an argument: without `--kit` it is pasted hidden, or piped in. On the isolated tier, give `--tier isolated` when there is no settings file; on the co-located tier, give `--runtime` and `--connection` then. [The backup runbook](backup.md) says what each one does.

`suiteward doctor`: `--json`, `--settings PATH` and `--plain`.

### The installer's screens

Every installer command (`install`, `install --upgrade`, `install host`, `uninstall`, `enroll`, `backup init`, `backup`, `restore` and `doctor`) draws its output on one console, in one of two modes. Both run the same steps in the same order, with the same flags, answers-file keys and exit codes. What differs is what a person sees: the explanations of the questions are for the full screen only.

- **Full screen.** On a real terminal, the command takes over the window, as an editor does: a title bar, the list of its steps with the current one marked (from the first question), the page of the step, progress bars for the waits, a spinner for work of unknown length, and a last page that waits for Enter, so that you can read it before the window closes. For an install, an upgrade, `backup init`, `backup` and `restore` that last page is a summary; for `uninstall`, `install host`, `enroll`, `doctor` and an upgrade with nothing to do it is the page the command ended on, with `Enter to finish` at the foot. It uses the SuiteWard colors, and the nearest of the 256 colors where the terminal has no 24-bit color.
- **Pages.** A step starts a clean page, and so does each question that a flag or the answers file does not answer: its title, the explanation (what the question is, the choices and what each costs, the recommended one, the default) and the question at the foot, set apart by a rule. What must outlive a page is a check, a warning or a kept line (see "After the screen closes"), or a row of the summary. A page that is taller than the window is trimmed from its end, in this order: blank lines, then the explanation (behind the line `… enlarge the window to read the rest`), then whole warnings. The title, the question and a critical warning are never trimmed, so enlarge the window to read all of a page. A **critical warning** is one of the few that nothing may hide among the others, such as the one on the recovery kit: on a full screen it is a red box, and in plain mode it is a warning line.
- **Questions.** A choice among a few answers is a list. Up and Down move the highlight (past an end it goes round), Enter picks it, and the default is highlighted first. A multiple choice, such as the repositories to protect, has a box in each row: Space picks or drops the row, Enter confirms, and a count that the question does not allow is refused with a line under the list. A yes or no question is a list of Yes and No. Enter takes the highlighted row: No for a question that must never be answered by habit (such as setting up GitHub), and Yes for three questions that exist to help (starting a stopped service before an upgrade, making a recovery key where a backup needs one, and the copy of the program on the PATH). A list can have no default, as the list of check runs of a repository whose runs give no better guess than another: nothing is highlighted, and Enter is refused, with a note, until an arrow has picked a row. A text answer is typed, and Enter takes the default that the question states; when the default is a long reference (the release image), the page shows it as a row and the hint line says that Enter takes it. The hint line at the foot names the keys that apply to the open question. Where the terminal cannot read single keys, the list is numbered and you type the number (`Choose [1]:`), and a multiple choice takes numbers or names, comma-separated. A flag or an answers-file key answers a question with no list, and plain mode types it, `label (a or b) [default]:`, where Enter takes the default.
- **A script on a pseudo-terminal.** A script that drives a command through a pseudo-terminal (`ssh -t`, `expect`) gets the full screen and its final Enter. Give `--plain` to get the lines and no wait.
- **Plain lines.** The command prints lines with no escape sequence, as it did before the screens existed, with no explanation pages and no closing summary: an install ends by naming `suiteward doctor` as the next command, and a question that needs a person is the typed one. It does so when standard input or standard output is not a terminal (a pipe, a script, a coding agent, CI), when the terminal is smaller than 60 columns by 20 rows, when `NO_COLOR` is set (to any value, an empty one too) or `TERM` is `dumb`, when a Windows console cannot take escape sequences, and when you give `--plain`. `plain: "yes"` in the answers file does the same for `install` and `install host`; the other commands take only the flag. `doctor --json` never draws.
- **The steps of each command.**
  - `install`, co-located: Environment, Checks, GitHub, Start, Owner (setup), Done.
  - `install`, isolated: Checks, Images, GitHub, Units, Start, Owner (setup), Done.
  - `install --upgrade`: Plan, Backup, Images, Replace, Check.
  - `uninstall`: Confirm, Remove.
  - `install host`: Podman, Account, Metadata, Next.
  - `enroll`: Instance, Owner.
  - `backup init`: Keys, Kit, Settings.
  - `backup`: Status, Dump, Artifacts, Encrypt.
  - `restore`: Verify, Preview, Safety backup, Stop, Load, Start, Compare.
  - `doctor`: one page of checks.
- **A failure.** When a command fails on the full screen, the window does not vanish. The page the command stopped on stays, the title bar says `failed` and the step it failed in is marked (a command that fails before its first step has none to mark), the title bar word, the step and the error are red, the error is written under the page, wrapped to the window and marked with `!`, and the hint offers Enter. Press Enter to close the screen: the error is then printed on your terminal as the line `suiteward: <error>`, with the exit code the command always had (1). Plain output (`--plain`, a pipe, no terminal), an interrupt (Ctrl-C, exit code 130) and a usage error (exit code 2) show no such page and wait for nothing. `doctor`'s failed checks are results and not an error: it keeps its one page with the counts. The text of the error is the text that stderr gets, with the credentials of a URL hidden.
- **Setup is outside the frame.** Setup prints your TOTP secret, so the installer never draws it and never holds it. On Windows and macOS it opens setup in a window of its own and waits on a screen of its own, with a page before and a page after that say what happens. Elsewhere (Linux, SSH, the isolated tier) it leaves the full screen, prints the warning to turn off session recording and runs setup on your own terminal, then waits for Enter so that the secret stays in front of you until you have copied it, and the screen comes back. Section 6 has both. The screen draws none of setup's output and does not repeat it when it closes.
- **Ctrl-C.**
  - At a question (a list, a multiple choice or a typed answer), or while a command works, Ctrl-C ends the command at once with exit code 130 and the line `interrupted`, on a Windows console too. `doctor` does the same while it checks (`doctor --json` keeps the code of its checks), and so does an install or an upgrade while its closing checks run: the summary shows what there is, and the command ends as interrupted.
  - A prompt that hides what you type and is read by the terminal's own no-echo reader, such as a pasted App key or the recovery key of a restore, cannot be interrupted while it reads: press Enter first. The paste of the manifest code (section 5.3, step 3) is hidden too, and Ctrl-C does stop it.
  - Ctrl-C at the Enter that ends setup, the summary or the last page, during the short wait for the service that comes just before a summary (the summary may then lack its Instance and Release rows), or before the closing checks have started, only skips the wait or the checks, because the work is done (the `Doctor` row then reads `skipped (Ctrl-C)`; one that interrupts the checks reads `interrupted` and the command exits 130): the exit code is the command's own, and `doctor` keeps the exit code of its checks. The offers that come after the work, the copy of the program on the PATH and the removal of that copy at a purge, count a Ctrl-C as No, and the command still ends with exit code 0; a Ctrl-C at the copy offer also skips the closing checks that follow it (`Doctor: skipped (Ctrl-C)`).
  - Ctrl-C at the page of an error (above) only closes it, and the exit code stays the command's own.
- **After the screen closes.** The command leaves the full-screen window and prints on your normal terminal what you must not lose, so it stays in the scrollback: every check and warning of the run, of every step, in the order they were written, with the `✓` and `!` marks the screen drew (a `FAIL` or `WARN` of `doctor` keeps its word beside the `!`), and the lines the command kept. After an install or an upgrade, the summary follows: the tier, the runtime, the instance id (`Instance`) and the version of the service (`Release`), the GitHub App and its repositories, the owner, where to read `/status`, the token and settings files, an upgrade's swapped image as `Image: <digest> (was <digest>)`, on Windows the copy of the program, and the result of the closing `suiteward doctor` (its counts, and one warning with its fix for each result that needs a look); an upgrade also keeps the way back for its tier. On a window too small for the whole summary, the screen keeps the rows `Tier`, `Instance`, `Status` and `Doctor` and trims the rest behind a line that says so, the warnings first (from the end), then the other rows; the lines kept for your terminal are never trimmed, so everything is there after the screen closes. After `backup init`, `backup` and `restore`, the summary follows and the kept lines name what you must keep: where the recovery kit is and to keep it off the machine, which key a backup is encrypted to, where the backup is and the `suiteward restore` command for it, and, after a restore, where the backup of the state before it is and how to bring the canonical level with the default branch ([the backup runbook](backup.md#3-restore)). After `uninstall`, `install host`, `enroll`, `doctor` and an upgrade with nothing to do, it is the last page as it was held (the command to run next, what was removed, the results of the checks and their counts). After a failure it is the page the command stopped on, and then the error.

### The answers file

A flat YAML file in which every key is a flag name, except `github-app-key` and `manifest-code`, which exist only here because they are secrets, and every value is a plain scalar. The installer reads the answer from the flag, else from the file, else it asks at the terminal. When it cannot ask (standard input is not a terminal), it takes the default, or stops and names the missing flag. On Linux and macOS, the file must be readable by you only (`chmod 600`): the installer refuses a file that others can read. Windows has no such check, so keep the file in a folder only you can read. A secret never goes in a flag, where it would show in the process list: the installer refuses `--manifest-code`, so give a manifest code as `manifest-code` in the answers file, and the App's key as `--github-app-key-file`, or as `github-app-key` (the PEM) in the answers file. Example, without any secret:

```yaml
tier: co-located
runtime: podman
http-port: 27081
mcp-port: 27091
github: "yes"
github-account: personal
github-repositories: owner/name
github-owner: octo
test-check: "owner/name=CI / test"
ruleset-imported: "yes"
```

`test-check.owner/name` sets the check of one repository. Never put the TOTP secret or a TOTP code in this file. The App's private key (only as `github-app-key`) and a manifest code (only as `manifest-code`) may be in it, only at mode 600: delete the file afterwards.

### What the installer does, for review

The installer replaces about 11 manual commands on the co-located tier and about 62 on the isolated tier. This list maps its steps to the manual procedure that older releases documented, and says why. [The v0.2.0 guide](https://github.com/IgnisDevNE/SuiteWard/blob/v0.2.0/docs/install.md) has that procedure.

| Step | Why |
| --- | --- |
| Refuses a rootful Podman connection, and never runs as root. | A rootless store keeps the containers away from the host's root. Isolated splits the administrator steps from the service account's. |
| Identifies the engine behind the runtime and prints it. | The name `docker` can be Podman's shim, and the default Docker endpoint can belong to Podman's machine. |
| Pulls images by digest only, and refuses a tag. | A tag can be rewritten and a digest cannot. The binary carries the signed digest of its release. |
| Checks both ports before it changes anything. A new install takes 27081 and 27091 and a stack that exists keeps its own. | A refusal must leave a running stack alone. The defaults are off the crowded 8080-8099 range and below the ephemeral ports of Linux (32768-60999) and Windows (49152 and up), where an outgoing connection can hold a port. |
| Creates the network, the two volumes and the database owner secrets, with a generated password. | The password is never typed, shown or passed as an argument. |
| Starts the database, creates the role `suiteward_app`, runs `suiteward migrate`, and only then starts the service. | The service runs as a role that owns nothing and cannot change the schema or the immutability triggers. Only the one-shot migrate container holds the owner's URL. |
| Runs every container read-only, with all capabilities dropped and `no-new-privileges`, the service and migrate as user 65532 and the database as 999, with the published ports on 127.0.0.1 only. | Defence in depth: the listeners are never reachable from the network. |
| Creates the App through the manifest flow, with exactly five permissions, no webhook, `public: false`. | No Contents write and no Administration write: SuiteWard never commits and never changes protection. |
| Converts the manifest code on the machine that keeps the key, with a random `state`, and writes the key straight into the secret store. | The key never touches disk or the terminal. The code passes through your browser, as a downloaded key would. |
| Writes the ruleset JSON, waits for your Import and reads the protection back. | The App has no permission to change rules. A rule change stays a human click for each repository. |
| Runs setup with the real terminal and never captures its output; on Windows and macOS it runs setup in a window of its own that clears itself, and elsewhere it leaves the full screen first, so that the screen never draws or repeats the secret. It refuses to run setup without a terminal, and skips it when an owner is enrolled or `/status` cannot be read. | Setup prints the TOTP secret. A coding agent must not read it, and a second setup would replace the secret of the enrolled owner. |
| Gives the service `SUITEWARD_REPORT_BASE_URL=http://127.0.0.1:<HTTP port>`, on both tiers, with the port you chose. | It is the host's loopback address where the unit or container publishes the service's HTTP port (the one you chose). The installer never publishes that port beyond 127.0.0.1. A stack of an earlier release lacks the variable and is adopted as it is: `install --upgrade` adds it, and so does a Podman re-run of `install`, which replaces both containers. A Docker re-run keeps a running service of an unchanged image as it is, so that service stays without the variable, and its comments have no link, until `install --upgrade`. |
| `--upgrade` backs up the database, the artifact volume and the status first, as one encrypted file that only the holder of the recovery kit opens. | Migrations only go forward, so the backup is the only way back, and it holds the TOTP secret, so it is never written in plaintext. A run with no key and no terminal refuses before it changes anything. |
| `suiteward backup init` makes a post-quantum key pair, writes the private key only into a kit file that only you can read and that it never overwrites, and stores only the public key in the settings. | The instance needs the public key to encrypt, and never the private one: whoever reads the settings cannot read a backup. The kit is the owner's to keep off the machine, and it is removed if you do not confirm that you copied it. |
| `suiteward backup` dumps the database in memory, spools the artifact volume in a private directory, and writes one encrypted file, while the service keeps running. | The dump holds the TOTP secret, so it is never a file in plaintext. The dump comes first, so the volume holds every artifact that the dump names. |
| `suiteward restore` verifies the whole backup, previews it, backs up the state it replaces, stops the service, loads the dump in one transaction, replaces the volume, migrates, starts and compares `/status`. | A restore replaces data, so it checks everything before it touches anything, always keeps a way back, and never leaves a half-loaded database: the transaction commits only after the last line of the dump. |
| Writes its settings to a file of its own. | `doctor` and the next run read it back, and nothing else is silently overwritten. |
| On Windows, offers to copy itself to `%LOCALAPPDATA%\SuiteWard\bin` and to add that folder to the PATH of the user, and refreshes that copy on `--upgrade`. | Every command it prints must work in the terminal of the user. The offer is a question with a default, the copy is the user's own, and nothing is changed without a yes. |
| Enables `podman-restart.service` for the rootless user (Podman). | A rootless `--restart always` needs it after the machine restarts. |
| Isolated: checks Podman 5.5 or later with the CVE fix, makes the account, its subordinate ids and linger, and offers the metadata rule. | Rootless Podman needs the ranges, the services must outlive a login, and a metadata service hands out instance credentials. |
| Isolated: writes six unit files and checks them with the generator's dry run. | Podman skips a unit it cannot parse, and you would only see "unit not found" later. |

### What `suiteward doctor` checks

`doctor` only reads. It prints `ok <name>`, `WARN <name>: <what needs a look> — <the fix>` or `FAIL <name>: <what is wrong> — <the fix>`, and exits non-zero when anything failed: a warning does not. The install and the upgrade run the same checks as the last step of their summary on a full screen, and show the counts and the fix of each result that needs a look; they never fail because a check failed. With `--json`, a warning has `"warning": true`. It runs `suiteward check --json` inside the service container and merges its results. On an instance installed without GitHub, the database checks run, and `github.app`, `github.installation`, `github.permissions`, `github.repositories` and `owner.enrolled` are warnings; run the install with `--github yes` to set GitHub up.

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
| `network`, `memory` (isolated) | The Podman network backend is not `netavark`, or the database is not limited to 384 MiB. The fix text says what to do: install netavark and set it in `containers.conf`, or check `PodmanArgs=--memory=384m` in `suiteward-db.container`, restart the unit and check that `podman inspect` prints 402653184. |
| `metadata` (isolated) | `169.254.169.254` answers from the service network. As an administrator, run `sudo suiteward install host --metadata-rule yes`, then doctor again. |
| `github.app`, `github.installation`, `github.permissions`, `github.repositories` | GitHub refuses the App key, the installation is not the App's, the installation's permissions are not exactly the five, or a repository is not covered or not readable. Check the App ID and the key against the App's settings page. |
| `protection.<owner/name>` | The repository's protection is not `enforced`. Its fix names the finding (section 7). |
| `owner.enrolled` | Setup has not run. Run it (section 6). |
| `database.schema`, `database.role` | The schema is not at this release's version, or the service's role is a superuser, can create roles or bypasses row-level security. |
| `instance.check` | `suiteward check` could not run in the service container. Start the service. |

## 13. Configuration reference

Every setting is an environment variable named `SUITEWARD_<NAME>`. A secret can instead be given as `SUITEWARD_<NAME>_FILE`, the path of a file whose content is the value. Give one form, not both. A variable that is set but empty counts as unset.

Where you set them:

- **Isolated host:** the installer renders the units, with the ports and the job timeout of `--http-port`, `--mcp-port` and `--job-timeout` when they are given. It carries the tier, the two ports and the job timeout over from the installed units, and any other edit of a unit stops the next install with a diff. The three GitHub values are in `~/.config/suiteward/github.env`. After a hand edit, run `systemctl --user daemon-reload` and `systemctl --user restart suiteward.service` as `suiteward`. After a change in `suiteward-migrate.container`, or to an `Image=` line, restart `suiteward-migrate.service` instead.
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
| `SUITEWARD_GITHUB_API_URL` | `https://api.github.com` | The GitHub API address. Keep the default for github.com. It must be `https`, except that `http` is accepted for a loopback host (for a local test server). |
| `SUITEWARD_INSTALLATION_TIER` | `co-located` | `isolated` or `co-located`. Set the tier you installed. SuiteWard never claims more than you set. The isolated units set `isolated`. |
| `SUITEWARD_REPORT_BASE_URL` | none | The loopback origin of the instance, `http://127.0.0.1:<port>` or `http://localhost:<port>`, with the port that the HTTP listener is published on at the machine that runs the instance (`--http-port`, 27081 by default), not the port inside the container that `SUITEWARD_HTTP_ADDR` names (no path, query or user info; a trailing slash is dropped). The change report link of a revision comment starts with it. Anything else stops the service at startup, and the value is not echoed. Without it, a revision comment has no link. The installer sets it on both tiers from `--http-port`. |
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

- **Isolated host (as `suiteward`):** `curl -s http://127.0.0.1:<HTTP port>/status` (27081 by default, 8081 on a host installed before M1.14), then `journalctl --user -u suiteward.service -n 100 --no-pager`, for the migration `journalctl --user -u suiteward-migrate.service -n 50 --no-pager`, and for the database `journalctl --user -u suiteward-db.service -n 50 --no-pager`. The migration container is removed when it exits, so `podman logs` cannot show it.
- **Co-located PC:** the installer prints where to read `/status` at its end: `http://127.0.0.1:<HTTP port>/status` (27081 by default, or the port of your stack) when it answers from this PC. When it does not (the published port of a Podman machine may not answer from Windows itself), it prints the command that reads it inside the machine, `podman machine ssh --username user <machine> "curl -s http://127.0.0.1:<HTTP port>/status"`; on macOS that is the same command with your machine's user (step 5.1). Then `podman logs --tail 50 suiteward` (with the rootless connection selected, step 5.1), or the engine's own `logs` command on Docker. The migration runs in the foreground of `suiteward install`, which shows its output.

On AlmaLinux, `journalctl --user` can report that no journal files were opened: user journals are readable only to members of `systemd-journal`. Read the service's own log instead, with `podman logs --since 30m suiteward` (and `suiteward-migrate` or `suiteward-db`).

If the log shows `github_checks` ending in `context deadline exceeded` again and again, then `budget_exhausted`: a first assessment of a large test suite takes longer than `SUITEWARD_JOB_TIMEOUT`, and every cancelled attempt reads the files again and spends the API budget. Raise `SUITEWARD_JOB_TIMEOUT` (section 13: one minute covers about a thousand protected files, read eight at a time). Read `budget` in `/status` (or the MCP `status` tool): `budget.remaining` is what is left and `budget.reset` is when requests resume. The budget recovers when GitHub's hourly window resets, and the queued check is then published by itself.

These are the `/status` fields you read:

- `tier` is the effective tier. `tierNote` appears when the setting and the enrollment disagree. Run `suiteward enroll` (section 6; `--tier isolated` on the isolated tier).
- `owner` shows the enrolled login. It is absent until setup succeeds.
- `budget` is the GitHub API budget: `budget.remaining` is the number of requests left in the current hourly window, and `budget.reset` is when the window resets. It is absent until GitHub has answered once, and when GitHub is not configured.
- `unconfiguredRepositories` lists the enrolled repositories that the configuration does not name. It is absent when there are none.
- `github` has one entry per repository:
  - `lastSuccess` is the last successful synchronization.
  - `lastError` is the last synchronization error, as short text without secrets. If `waitUntil` is set, GitHub asked SuiteWard to wait because of a rate limit, and SuiteWard resumes by itself. Otherwise, check that the App is installed on that repository, and that the App ID, the installation ID and the key are the ones from step 10.4.
  - `protection` shows the results of section 7.
  - `checks` is the last error while SuiteWard published that repository's check. It is absent when there is none.
  - `bootstrap` shows why the first contract is waiting or stopped. `read-failed` means GitHub could not be read, and SuiteWard retries. `repository-changed` means the configured name now points at a different repository from the one enrolled, so check the name. `origin-not-passed` means the default branch's contract could not be read as a passing source. `assessment-conflict` means two assessments of one source disagree. `blocked-<reason>` means a block for the stated reason. `failed` means the bootstrap failed for good. Read the logs for the last three. The field is absent when nothing is wrong.
  - `merges` shows merge decisions. `waiting` means GitHub has not yet reported the merge commit, and SuiteWard retries. `read-failed`, `assessment-conflict` and `failed` need the logs.
  - `integrity` is `passed`, `failed` or `unavailable` for the current version. It appears after the first integrity check, which runs when the service starts, right after each promotion and then every hour, so a canonical shows its `integrity` within moments of its promotion. `failed` means SuiteWard fails the check on every pull request of that repository and does not promote until the problem is resolved; it does not block merges by itself — your branch protection does that, if you require the check. `unavailable` changes no check.
- `outbox.failed` counts messages that SuiteWard could not deliver to GitHub after its retries. It should be 0. Read the logs for the repository. A refused write is often a missing App permission, so compare with step 10.4. If the failed message was a check, SuiteWard sends it again by itself about 15 minutes after the cause is fixed. Until then a pull request can lack its required `SuiteWard / Contract` check. Push a new commit to that pull request to get a new check at once.
- `jobs.discarded` counts background jobs that failed for good. Read the logs.
- `version`, `schemaVersion` and `source` identify the release and the database schema that run. `instance` is the first 8 characters of the instance id, the same as in the name of the authenticator entry that setup creates (`SuiteWard:<login> (<tier> <instance>)`), so you can match an entry to an instance.

Common problems:

- **The installer says that the connection is rootful, or asks for `--connection`.** The stack must run rootless. Select the machine's rootless connection with `--connection <name>` (step 5.1) and run it again. The installer never changes your Podman default.
- **The installer says that `docker` is not Docker, or that the engine behind it is Podman.** On a PC with both, `docker` can be Podman's shim (`C:\Program Files\RedHat\Podman\docker.cmd`), and the default endpoint (`npipe:////./pipe/docker_engine`) can be served by the Podman machine. Use Podman, or give Docker Desktop's own context with `--connection desktop-linux`. The installer runs Docker Desktop's CLI by its full path, `C:\Program Files\Docker\Docker\resources\bin\docker.exe`.
- **Docker Desktop fails to start with `open \\.\pipe\docker_engine: Access denied`.** Whichever engine starts first owns the `docker_engine` pipe, and the Podman machine had it. Stop the Podman machine, start Docker Desktop, then start the machine again: it then uses its own pipe. The installer and `doctor` address each engine by its own endpoint, so they do not depend on the pipe.
- **A Docker pull fails with `error getting credentials`.** Docker Desktop's credential helper lives in `C:\Program Files\Docker\Docker\resources\bin`. The installer puts that folder on the PATH of the commands it runs. For a pull you run by hand, add it to your PATH.
- **After Windows or macOS restarts, nothing answers.** The Podman machine does not start at logon in this release. Run `podman machine start`: the containers then come back by themselves, and `suiteward doctor` reports the machine until you do.
- **The one-line install prints a 404 error from curl, or stops with a message that names `SUITEWARD_VERSION`.**
  - A 404 before any message of the script means the release has no `install.sh` (no release built before phase M1.13 has one). `curl -f` wrote nothing, so the shell received nothing and ran nothing. The pipe's exit status is the shell's, not curl's, so read the message.
  - A message that names `SUITEWARD_VERSION` comes from the script itself. The release does not exist or has no `SHA256SUMS`, or its `SHA256SUMS` does not list `suiteward_<tag>_<os>_<arch>` exactly once, or that binary cannot be downloaded, or GitHub refused the anonymous lookup of the latest release.
  - In both cases, give the tag of a release whose page lists `install.sh` and the binary of your system (section 4.1). Nothing was installed.
- **The one-line install says the signature was not verified, or that cosign could not verify it.** Without cosign the script installs on the hash of `SHA256SUMS` alone and prints how to verify the signature elsewhere; install cosign 3.x to have it checked, or set `SUITEWARD_REQUIRE_COSIGN=1` to refuse to go on without it. When cosign is there and fails, the script stops and installs nothing: the file is not the one the release workflow signed, or cosign is older than 3.x. Do not run a binary that was not verified.
- **macOS refuses to open `suiteward` and says that Apple could not check it.** Gatekeeper quarantined a browser download (section 4.2). Verify the file, then run `xattr -d com.apple.quarantine <file>`.
- **A port is in use.** The installer names the flag: choose another with `--http-port` or `--mcp-port`. Docker Desktop's VM shares a network namespace with the Podman machine, so two stacks on one PC need different ports.
- **A command says that `http_port` or `mcp_port` is not set in the settings file.** The installer always writes both ports, and it refuses a file without them: a default would move a stack that exists to another port at the next run. Add the ports the stack publishes (`podman ps` shows them) to the file, or install again with `--http-port` and `--mcp-port`.
- **The install ended with exit code 0, but its summary shows failures.** The closing checks of a full screen report what needs a look and never fail the install. Read each line: it names the fix, such as the ruleset to import. `suiteward doctor` runs the checks again.
- **A command that the installer printed says that `suiteward` is not found (Windows).** The program is not on your PATH, and the installer names it by the path it was run from when it is not. Run it by that path, or accept the copy to your PATH that an install or an upgrade offers on a full screen (section 5.7).
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
- **An install failed after it created the GitHub App.** The installer records that App in `<settings path>.pending-app`: beside `settings.yaml` (`%APPDATA%\suiteward` on Windows, `~/.config/suiteward` on Linux, including the isolated account's). The record holds the App's id, slug and owner, never the key, which is in the secret store as `suiteward-github-app-key`. Run the installer again with `--github yes`: it takes up that App, with no second App, and waits for the installation. If the App is gone from GitHub (404 on `GET /app`), the error names the record, the secret and the command that removes it: delete the App on GitHub, the record and the secret, then run again. If only its key secret is gone, the error offers the two routes of section 5.3 (an App named without its key): give `--github-app-key-file <file>` with a key of that App, which takes the App up, or `--github new-app`, which creates a new App, replaces the record and prints the page that deletes the old one. Running with `--github-app-id`, `--github-installation-id` and the key file works too. A run with `--github no` keeps the record: when the settings name no App it prints a note on how to take the App up or throw it away, and when they name another App it refuses (see section 5.3, "A new App that stops partway"). A successful install removes the record, and so does `uninstall --purge`.
- **The browser cannot reach the manifest listener, or the manifest wait timed out.** After 2 minutes without an answer, on a terminal, the installer asks you to paste the address of the error page, or its `code=` value (the question is hidden; Enter with nothing pasted keeps waiting; section 5.3, step 3). The wait ends after 15 minutes: run the installer again, because GitHub's code lives one hour and converts once. On the isolated tier, start the Session Manager port forward that the installer printed first, and pin the port with `--manifest-port` (step 10.4).
- **GitHub says `Invalid integration ids` when you import the ruleset.** The App must be installed on the repository first. Add the repository to the installation, then import the file again.
- **Setup exits with code 2.** The `--test-check` list does not match the configured repositories. Give exactly one per repository, with no duplicates.
- **Setup exits with code 1.** A lookup failed. The owner login may not be a user account, the App may not be installed on every configured repository, or GitHub could not be reached. Nothing was written.
- **`propose` was refused.** The acknowledgment names the reason: `canonical-present` (the repository already has a contract; `propose` only works before the first one), `fork` (pull requests from forks are not supported), `not-open` (the pull request is no longer open), `base-not-target` (the pull request's base branch is not the repository's default branch, so merging it could never decide the first contract), `declaration-missing` (no `.suiteward.yml` was found on the default branch or the pull request head; see the suggested file and link, section 8), or `invalid-source` (`.suiteward.yml` is invalid, a symbolic link that the scope reaches points at a directory, outside the repository or nowhere (section 8), or GitHub truncated the tree, which happens with very large monorepos and cannot be protected in this release).
- **A command gets no reply.** Check that the first line starts with `/suiteward`, outside a code block, that it comes from your account, that you did not edit it, and that the pull request is still open. Another account's command gets no reply by design.
- **A repository gets no reply and no checks,** `/status` lists it in `unconfiguredRepositories`, and the log says `github commands skip an enrolled repository that is not configured in GITHUB_REPOSITORIES`. A repository you removed from the configuration, or deleted on GitHub, is still enrolled, and SuiteWard skips it. See [Stop protecting a repository](#stop-protecting-a-repository), or add it back to the configuration.
- **An approval is rejected.** The reply gives the reason: an invalid, replayed or expired code, or an unknown revision. Use a fresh code, and check the reference against the newest revision comment.
- **The check shows `approval-missing` after a push.** The push changed a protected file, which creates a new revision. Approve the revision named in the newest comment.
- **The new image does not start after an update.** Do not start an older image, because after a migration it refuses to run. Read the logs of the migration and of the service and report them to the project. To return to the earlier state, restore your backup, as described in [the backup runbook](backup.md#going-back-after-an-upgrade).
- **`suiteward install --upgrade` or `suiteward backup` refuses because no recovery key is set.** A backup is encrypted and never written in plaintext, and nothing could ask you to make a key. Run `suiteward backup init` on a terminal and keep the kit off the machine. For an unattended upgrade, give `--backup-recipient age1pq1...` (the public key) or `backup_recipient` in the answers file. `--yes` never makes a key. `suiteward backup` takes its key from the settings only, so its message names only `suiteward backup init`; the message of an upgrade names `--backup-recipient` too.
- **`suiteward backup init` says that a recovery key is set, or that the kit exists.** A key that is set is replaced only with `--replace` (the backups made with the old key still need the old kit). A kit file is never overwritten: name another with `--kit`, or move the old one away.
- **`suiteward backup` or `backup init` says that the service does not answer `/status`.** The status belongs in the backup, and the instance id names the kit. Start the service (the message names the command) and run the command again, or take the [backup by hand](backup.md#isolated-host-bash-as-suiteward). `suiteward restore` does not need the service: it needs the database container to run and be healthy, and it backs up an instance that does not answer with its status recorded as unavailable. A plaintext directory is the exception: its release can be compared only when the service answers.
- **`suiteward restore` refuses because the backup is of another release.** The message names the release of the backup and the `suiteward install --image ...` command for it. Install that release first ([the backup runbook](backup.md#3-restore)), then run the restore again.
- **`suiteward restore` says that the identity does not open the backup.** The kit is not the one of the key that the backup was encrypted to. The fingerprint (`SHA256:...`) is in the kit and in the output of `suiteward backup`. A message that the backup is invalid means that it is damaged, cut short, or not a SuiteWard backup: take another copy.
- **A restore stopped after it stopped the service.** The message says where it stopped, how to get the service back, how to run the restore again and where the backup of the state before it is ([the backup runbook](backup.md#3-restore)).
- **A directory `.suiteward-spool-...` or `.suiteward-restore-...` is in the backup folder.** A backup or a restore was killed and left a private copy of the data in plaintext. The next `suiteward backup`, `restore` or upgrade removes it and says so; you can remove it yourself at once.
- **The link of a revision comment does not open, or answers 404.** The link opens on the machine that runs the instance (section 9): on the isolated host, forward the HTTP port first. A 404 means that the repository is not enrolled, the revision is unknown, it is the baseline of the first contract, or it was recorded before this release. The comment of a revision of a stack that has no `SUITEWARD_REPORT_BASE_URL` has no link at all (section 12, "What the installer does").
- **A revision comment says that the numbers are unknown.** SuiteWard could not count the changed lines when it posted the comment: the revision was recorded before this release, or a file of it could not be read from the store. The service log has the cause (`the change report is unavailable, so the numbers are left out`). The comment lists the protected files instead, and has no link. SuiteWard never edits a comment, so it stays.
