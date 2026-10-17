# Installing SuiteWard

This guide installs SuiteWard once, creates its GitHub App, and runs the owner setup. After that you only approve the pull requests that change protected test files.

Two rules apply to every command:

- Pin one release. Replace `vX.Y.Z` with the release you install, everywhere it appears. Do not use a moving tag.
- Replace each value in angle brackets, such as `<owner/name>`, with your own value. Never put a secret, a token or a TOTP code into a file, a log, a pull request or a chat.

## 1. What SuiteWard does and does not do

SuiteWard blocks a pull request that changes the protected test files until the owner approves that exact revision. Until then, its `SuiteWard / Contract` check stays pending. When the pull request merges, SuiteWard checks the approval and the integrity of the protected files at the merge commit, and promotes the approved contract as the new canonical version.

SuiteWard does not run your tests, and it does not claim that your CI ran them. A merge is not proof that the tests ran, passed or are correct. Your review and your own test check decide that. The full statement is in [ADR 0014](decisions/0014-merge-triggered-canonical-promotion.md).

## 2. Choose a tier

The tier says where SuiteWard runs compared with your coding agent. The way you install SuiteWard declares the tier. SuiteWard never detects it, and it reports the tier in `/status` and in the check summary.

- **Isolated:** SuiteWard runs on a separate host that the coding agent cannot reach. It can also run in a VM, or under another OS user, when the agent has no access to the container runtime and the keyring. This tier gives the full assurance. Section 5 installs it.
- **Co-located:** SuiteWard runs on the same machine as the coding agent. The App key, the database and the TOTP secret are then reachable by the agent. SuiteWard states this reduced assurance and does not claim protection it cannot provide. Section 6 installs it.

Neither tier protects against a compromised host administrator. The coding agent must use its own GitHub identity, never your credentials. [ADR 0027](decisions/0027-agent-human-trust-separation.md) gives the reasoning.

Run one SuiteWard instance per repository. Two instances post as the same App, and each has its own TOTP secret. One of them then rejects the owner's codes and publishes a failing check.

## 3. Prerequisites

- **Rootless Podman.** The units for the isolated host were checked with Podman 4.9.3. Section 5 checks your version.
- **Isolated host:** Linux with systemd user services (checked on Ubuntu 24.04), and an unprivileged account for SuiteWard. This guide calls it `suiteward`. Use administrator rights only for the steps marked `sudo`.
- **Co-located PC:** Windows with PowerShell 7.2 or later, run as `pwsh` (not Windows PowerShell 5.1), and a Podman machine.
- **An authenticator app** that scans an `otpauth://` URI or accepts a base32 secret.
- **A GitHub account** that owns or administers the repositories, and up to 20 repositories to protect. The owner's login must be a user account, not an organization or a bot.
- **The release source** at `https://github.com/IgnisDevNE/SuiteWard/tree/vX.Y.Z`. Download it, or clone the repository and check out the tag `vX.Y.Z`. The isolated install uses the files in `deploy/quadlet/`; the co-located install uses `deploy/local.ps1`.

## 4. Create the GitHub App

Create the App by hand, and tick each box when it is done:

- [ ] Create a GitHub App under your user account, or under an organization you administer.
- [ ] Leave the webhook inactive: uncheck **Active**. SuiteWard polls GitHub and receives no webhooks.
- [ ] Set the repository permissions to exactly these, and nothing else:
  - Metadata: Read-only (GitHub requires it)
  - Pull requests: Read and write
  - Contents: Read-only
  - Administration: Read-only
  - Checks: Read and write
- [ ] Generate a private key. GitHub downloads a `.pem` file. Keep it somewhere safe until section 5 or 6 stores it. Never commit it.
- [ ] Install the App on the repositories you want to protect, and no others. The limit is 20.
- [ ] Note the **App ID** (on the App's settings page) and the **installation ID** (the number in the address of the installation's settings page).

Keep these permissions off, for these reasons:

- **Administration: Write** stays off. SuiteWard reads protection and never changes it. You set the ruleset yourself (section 8).
- **Contents: Write** stays off. SuiteWard reads code and never commits. The `.suiteward.yml` declaration is yours to commit.
- **Issues** stays off. Pull request comments are covered by the Pull requests permission.
- **Webhooks** stay off. This release polls GitHub and accepts no inbound webhooks.
- **Any other permission** stays at No access. SuiteWard does not request it.

## 5. Install on an isolated host

Run these steps as the `suiteward` account, unless a step says `sudo`. The host runs Podman 4.9.3 and the units from `deploy/quadlet/` in the release source.

**5.1 Check the account's systemd user manager.** Run:

```
systemctl --user is-system-running
```

Expect `running` or `degraded`. If the bus is not ready yet, wait a few seconds and run it again.

**5.2 Let the account's services start at boot.** Run this as an administrator:

```
sudo loginctl enable-linger suiteward
```

Expect no output.

**5.3 Create the database password and its two Podman secrets.** One generated password feeds both secrets, so you never see or type it. The chain stops at the first failure:

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

**5.4 Check the shape of both values without showing them.** Each line prints a count, and both must print `1`:

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

A `0` means a malformed value. Remove both secrets with `podman secret rm suiteward-db-password suiteward-database-url`, then repeat step 5.3. Nothing has started yet. After the first start the database keeps this password in its volume, so replacing a secret later locks the service out.

**5.5 Refuse the metadata address for the service account.** This is the example for a cloud VM. The instance metadata service at `169.254.169.254` hands out the instance's credentials. Rootless containers connect to the network as a process of the service account, so a rule for that account covers them. If your host has no metadata service, skip this step.

Install `nftables` if it is missing (Ubuntu):

```
command -v nft || sudo apt-get install -y nftables
```

Expect a path to `nft`, or the installation output.

Create the rule directory:

```
sudo mkdir -p /etc/suiteward
```

Create the rule file. Run the next command, wait until the cursor stops, paste the block below it, then press Ctrl-D on an empty line:

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

Load the rule at every boot with a unit. `ExecStop` starts with `-`, so a missing table never fails a stop:

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

Then enable it and confirm that the rule is loaded:

```
sudo systemctl daemon-reload
```

```
sudo systemctl enable --now suiteward-imds-block.service
```

```
sudo nft list table inet suiteward_imds
```

Expect the table to list the `reject` rule for `169.254.169.254`. The rule covers IPv4 only. The service account must be refused at that address. Step 5.15 checks that GitHub stays reachable. If the table disappears, for example after a `flush ruleset` in `/etc/nftables.conf`, load it again with `sudo systemctl restart suiteward-imds-block.service`.

**5.6 Write the GitHub settings file.** These three values are not secrets. Run the next command, wait until the cursor stops, paste the three lines with your own values, then press Ctrl-D on an empty line:

```
sudo tee /etc/suiteward/github.env >/dev/null
```

```
SUITEWARD_GITHUB_APP_ID=<app id>
SUITEWARD_GITHUB_INSTALLATION_ID=<installation id>
SUITEWARD_GITHUB_REPOSITORIES=<owner/name>,<owner/name>
```

Expect the file to hold no secret, and the `suiteward` account to be able to read it. The three values are all set or all unset: a file with only some of them stops the service at startup. The repositories are 1 to 20 `owner/name` entries, comma-separated, with no spaces.

**5.7 Store the App's private key as a Podman secret, without echo.** The first line turns echo off, so the key never shows on the screen or in a recorded terminal session. Run it, wait until the cursor stops, paste the whole PEM from your copy, press Enter, then press Ctrl-D. You see nothing while you paste:

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

If the terminal goes silent, type `stty echo` and press Enter. After an aborted paste, run `shred -u ~/app-key.pem` and start this step again. Delete your own copy of the `.pem` file as well.

**5.8 Create the MCP token secret.** The service needs this secret to start, even if you never use an MCP client. It is 32 random bytes in hex:

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

**5.9 Install the five unit files.** Create the directory:

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

After you paste `suiteward.container`, make two edits. Set its `Image=` line to `Image=ghcr.io/ignisdevne/suiteward:vX.Y.Z`, and delete the `AutoUpdate=registry` line. Keep `Environment=SUITEWARD_INSTALLATION_TIER=isolated`. The database unit stays as it is: its image is pinned by digest.

**5.10 Check that Podman reads every unit.** Check the Podman version, then run the generator's dry run:

```
podman --version
```

```
/usr/lib/systemd/system-generators/podman-system-generator --user --dryrun
```

Expect the version (4.9.3 is the checked version) and every generated unit listed with no error. Podman skips a unit it cannot parse, and you would only see "unit not found" later. If the dry run reports an error, stop and fix that line first. Do not guess at a fix.

**5.11 Pull the two images.** Pull the database image by its digest, then the release image:

```
podman pull docker.io/library/postgres:18.6-trixie@sha256:5a5a84b19854a9ffaa54082c166ff4ec27473a361e496e5ea167f298f2da9722
```

```
podman pull ghcr.io/ignisdevne/suiteward:vX.Y.Z
```

Expect a digest line for each pull.

**5.12 Load the units and start the service.** Run:

```
systemctl --user daemon-reload
```

```
systemctl --user start suiteward.service
```

Expect no output. On the very first start the service can fail while the database initialises. Systemd restarts it every five seconds, which is expected.

**5.13 Check both units.** Run:

```
systemctl --user status suiteward-db.service suiteward.service --no-pager
```

```
podman ps
```

Expect both units active, and `suiteward-db` listed as `(healthy)`.

**5.14 Check what the host applies.** Run:

```
podman info --format '{{.Host.NetworkBackend}}'
```

Expect `netavark`. Then run:

```
podman inspect suiteward-db --format '{{.HostConfig.Memory}}'
```

Expect `402653184` (384 MiB). If either value differs, stop and check the Podman setup before you go on.

**5.15 Check that GitHub is reachable from the service network.** Run:

```
podman run --rm --network suiteward docker.io/curlimages/curl@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777 -s -m 10 -o /dev/null -w '%{http_code}\n' https://api.github.com/zen
```

Expect `200`. `403` means the shared unauthenticated GitHub rate limit is used up: wait, then run it again. `000` means the host cannot reach GitHub, so fix the network path before you go on.

**5.16 Check the service.** These commands read the host's loopback port. Expect `{"status":"ok"}` from the first one:

```
curl -s http://127.0.0.1:8081/healthz
```

```
curl -s http://127.0.0.1:8081/readyz
```

Expect `{"status":"ready","schemaVersion":N}`, where `N` is the schema version of this release. A 503 means the database or the schema is not ready. Section 14 covers it.

```
curl -s http://127.0.0.1:8081/status
```

Expect JSON with `"tier":"isolated"`, `outbox.failed` at 0 and `jobs.discarded` at 0. The `github` list fills in after the first synchronization succeeds, and the `owner` field appears after setup (section 7).

The port is published on the host's loopback only. To read `/status` from another machine, forward the port with your own remote-access tool. SuiteWard does not provide one.

## 6. Install co-located

Run these steps on the PC that also runs your coding agent, in PowerShell 7.2 or later. Read section 2 first: on this tier the agent can reach the App key, the database and the TOTP secret.

**6.1 Open the release source.** Get the source of `vX.Y.Z` (section 3), and open PowerShell 7.2 or later in its folder.

**6.2 Pull the release image.** Run:

```
podman pull ghcr.io/ignisdevne/suiteward:vX.Y.Z
```

Expect a digest line.

**6.3 Start the stack.** The script creates the network, the volumes and the secrets. It generates the database password and the MCP token and never prints them. Pass the App key file only the first time; the three GitHub values are needed on every run that should keep GitHub:

```
./deploy/local.ps1 -Image ghcr.io/ignisdevne/suiteward:vX.Y.Z -GitHubAppKeyFile <path to the PEM> -GitHubAppId <app id> -GitHubInstallationId <installation id> -GitHubRepositories <owner/name>,<owner/name>
```

Expect the line `Started suiteward-db and suiteward (ghcr.io/ignisdevne/suiteward:vX.Y.Z), GitHub configured.`

Notes:

- Always pass `-Image` with your pinned release. The script has its own default image, and that default is not a release.
- The script reads `-GitHubAppKeyFile` only while the secret `suiteward-github-app-key` does not exist yet. Delete your copy of the `.pem` file after the first run.
- The script stores none of the three GitHub values. A run without `-GitHubAppId`, `-GitHubInstallationId` and `-GitHubRepositories` replaces the container with one without GitHub. The state stays.
- The MCP token copy is in `%LOCALAPPDATA%\SuiteWard\mcp-token`, readable by your Windows user only.
- The names and the ports are fixed: HTTP on 8081, MCP on 8091. Run one stack per machine.

**6.4 Check the service through the Podman machine.** The published port may not be reachable from Windows itself, so read it inside the machine. Run:

```
podman machine ssh "curl -s http://127.0.0.1:8081/readyz"
```

Expect `{"status":"ready","schemaVersion":N}`.

```
podman machine ssh "curl -s http://127.0.0.1:8081/status"
```

Expect `"tier":"co-located"` in the output.

**6.5 Run setup.** Section 7 covers it.

To stop the stack and keep its state, run `./deploy/local.ps1 -Down`. Expect the two containers to be removed while the volumes and secrets stay. Start again with the command of step 6.3; the key file is not needed again.

`./deploy/local.ps1 -Down -Purge` also removes the database, the artifact volume, the secrets, the MCP token copy and the network. That deletes your TOTP enrollment and every contract version. Use it only to start over, and back up first (section 11).

## 7. Run setup

Setup binds you as the owner and enrolls your TOTP secret. Run it yourself, once, in a terminal on the host (isolated) or on this PC (co-located). It needs a real terminal.

Before you start:

- Have your authenticator app ready.
- Turn off recording of the terminal session. Setup prints the `otpauth://` URI and the base32 secret to the terminal, and a recorded session keeps them. If recording was on, turn it off and run setup again. That replaces the secret, so the old one stops working.

Run setup with your login and one `--test-check` for each configured repository. The check name is the exact name of the required status check that runs your tests, as it appears in that repository's ruleset. Do not use `SuiteWard / Contract`: SuiteWard publishes that check itself.

```
podman exec -it suiteward /suiteward setup --owner <login> --test-check <owner/name>=<check name> --test-check <owner/name>=<check name>
```

Setup then works through these steps:

1. It looks up your login, which must be a user account, and every configured repository. If one lookup fails, it exits with code 1 before it writes anything.
2. It prints a warning and waits. Press Enter to continue. End of input or Ctrl-C exits with code 1, before any secret is created.
3. It prints the `otpauth://` URI and the base32 secret, once. Scan the URI with your authenticator, or type the secret in.
4. It asks for the current 6-digit code. You get three attempts. Three wrong codes exit with code 1 and write nothing.
5. It stores the owner and the secret, records each repository's test check, and checks protection for each repository (section 8).

A missing, extra or duplicate `--test-check` is a usage error: exit code 2, and nothing is written. Setup exits with code 0 once enrollment succeeded, whatever the protection results.

Afterwards, check the result. On the isolated host, run `curl -s http://127.0.0.1:8081/status`. On the co-located PC, use the `podman machine ssh` form from section 6.4. The output shows `owner` with your login and the `protection` result of each repository.

Running setup again replaces the TOTP secret. Delete the old authenticator entry and add the new secret. Never paste the setup output, the URI, the secret or a code into a chat, an issue or a pull request.

## 8. Create the ruleset

SuiteWard reports protection and never changes it, so you set the protection of each repository once, on its default branch. Create a ruleset (or classic branch protection) that does the following:

- [ ] Targets the default branch. SuiteWard reads the rules of that branch.
- [ ] Requires the status check `SuiteWard / Contract`, with your SuiteWard App as its source. A check from any other source does not count.
- [ ] Requires your test check, the name you gave to setup.
- [ ] Requires branches to be up to date before merging.
- [ ] Has an empty bypass list. SuiteWard cannot read a ruleset's bypass list, so check it yourself.
- [ ] Enforces the rules for administrators, if the repository uses classic branch protection (`enforce_admins`).

SuiteWard checks protection when you run setup, and then every 15 minutes. `/status` shows the latest result for each repository. The results mean the following:

| Result or code | Meaning | What to do |
| --- | --- | --- |
| `enforced` | Nothing is missing. | Nothing. |
| `missing-contract-check` | `SuiteWard / Contract` is not required from your App. | Require it, with your App as the source. |
| `missing-test-check` | The test check you gave to setup is not required. Before setup, this result appears too. | Require the check, or run setup again. |
| `not-up-to-date` | The required checks do not require branches to be up to date. | Turn on the up-to-date requirement. |
| `admin-bypass` | Classic protection does not apply to administrators. | Enforce it for administrators. |
| `enforcement-unavailable` | GitHub does not enforce rules on this private repository, or the App cannot read the rules. SuiteWard does not claim protection here. | Either use a GitHub plan that enforces rules on private repositories (GitHub Free does not), or give the App the read permission in section 4. |
| `unknown` | The protection could not be read, for example because the App's token lacks Administration read. | Check the App's permissions against section 4. |
| caveat `ruleset-bypass-unverified` | Shown with a ruleset result. SuiteWard cannot see the bypass list. | Check by hand that the bypass list is empty. |

## 9. First use: bootstrap

A repository has no contract until its first one is approved. Until then, each open pull request, except one from a fork, shows the `SuiteWard / Contract` check as pending with the reason `bootstrap-awaiting`. SuiteWard comments with how to start. It proposes nothing by itself.

On the pull request that should carry the first contract, post one of these commands as the first line of a comment. Only the owner's comments count:

- `/suiteward propose` creates the first proposal from that pull request. It needs no code, and it grants nothing by itself.
- `/suiteward skip <6-digit code>` lets the pull request pass the check before any contract exists. The check then reads "Skipped by the owner before the first contract", and nothing was assessed. SuiteWard refuses this once a contract exists.

Where `.suiteward.yml` is decides the source of the proposal:

1. **On the default branch.** The proposal is pinned to the default branch's head. After you approve it, SuiteWard promotes the first version (V1) by itself, within about a minute, without waiting for a merge.
2. **Only in the pull request's head.** The proposal is that head. Its V1 comes only from this pull request's exact merge commit, after the pull request merges.
3. **Nowhere.** Nothing is proposed. SuiteWard replies with a suggested `.suiteward.yml`, built from the test-like paths it found, and a link. The link opens GitHub's editor on the pull request branch with that file filled in. You review the file and commit it yourself. SuiteWard never commits it.

The declaration has `version: 1` and optional `include`, `exclude` and `runner` lists of path patterns, in gitignore style and case-sensitive. Any other key makes it invalid:

```yaml
version: 1
include:
  - <test path pattern>
runner:
  - <test runner configuration or script pattern>
```

`.suiteward.yml` itself and `.github/workflows/**` are always protected. Put your test files in `include`, and your test runner configuration and test scripts in `runner`.

SuiteWard's proposal comment shows the copyable command. Approve the exact revision it names, for example `/suiteward approve P<n>-R1 <6-digit code>`.

Keep these points in mind:

- If a protected file changes after the proposal, SuiteWard cancels the proposal and comments. Post `/suiteward propose` again.
- Pull requests from forks get no proposal. Their check shows `fork-unsupported`, because forks are not supported in this release.
- Reading a very large test suite can take longer than the job timeout. Raise `SUITEWARD_JOB_TIMEOUT` (section 12).

## 10. Day to day

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
- A command on a pull request that is already closed is never processed.
- SuiteWard reads new comments about every 20 seconds, so an answer usually comes within a minute.

**What the check states mean.** The check is `SuiteWard / Contract`. Its summary names the state and the next action.

| State and reason | Meaning |
| --- | --- |
| success, `unchanged` | There is no protected change, so there is nothing to approve. |
| success, `ready` | The exact revision is approved and assessed, and it is ready for integration. |
| success, `bootstrap-ready` | The first contract is approved. V1 follows (section 9). |
| success, `bootstrap-skipped` | Skipped by the owner before the first contract. Nothing was assessed. |
| pending, `approval-missing` | Approve the current revision with the command in the comment. |
| pending, `bootstrap-awaiting` | There is no contract yet. Post `propose` or `skip` (section 9). |
| pending, `not-current` | This pull request's revision is no longer the current one. Use the newest revision comment. |
| pending, `source-unavailable` | GitHub cannot list the whole tree of this revision, so SuiteWard cannot read it. |
| pending, `fork-unsupported` | Pull requests from forks are not supported. |
| pending, `assessment-pending` | SuiteWard is still reading the pull request. Wait a few minutes. |
| failure, `invalid-source` | `.suiteward.yml` is invalid, or a protected path is a symbolic link or a submodule. Fix it. |
| failure, `integrity-failed` | The stored contract no longer matches its integrity record. SuiteWard refuses merges until this is resolved. Read the logs (section 14). |
| failure, `owner-command-rejected` | Your last `approve` or `revoke` was rejected. Post a valid command, or push a new head. |

A success means that the protected files match what the owner approved. It never means that your tests ran or passed. Your own test check decides that.

## 11. Updating

Pin one release, and update when you choose. Updates are manual unless you opt in, as described below.

**11.1 Back up first.** The service runs database migrations when it starts. After a migration, an older image refuses to start, and there is no rollback to an older image. See the backup runbook, `docs/backup.md`. The backup holds the TOTP secret and the enrollment, so store it as a secret.

**11.2 Pull the new release image.** Use the new `vX.Y.Z`:

```
podman pull ghcr.io/ignisdevne/suiteward:vX.Y.Z
```

**11.3 Update an isolated host.** Set the `Image=` line of `~/.config/containers/systemd/suiteward.container` to the new tag, then reload the units and restart the service:

```
systemctl --user daemon-reload
```

```
systemctl --user restart suiteward.service
```

Expect `/readyz` to answer `ready` again (section 5, step 5.16).

**11.4 Update a co-located PC.** Pull the image (step 11.2), then run `./deploy/local.ps1` with the new `-Image` value and the same three GitHub parameters as before (section 6, step 6.3). A run without them removes GitHub from the stack.

If the new image fails to start, do not start an older image. Read the logs (section 14).

**Automatic updates (opt-in).** Podman can follow the newest stable release. To opt in, set `Image=ghcr.io/ignisdevne/suiteward:latest-stable` and `AutoUpdate=registry` in `suiteward.container`, reload the units, and enable the timer:

```
systemctl --user enable --now podman-auto-update.timer
```

The timer checks once a day by default. `latest-stable` moves only for a release that is not a pre-release. A migration in such a release still has no rollback, so keep your backup current.

## 12. Configuration reference

Every setting is an environment variable named `SUITEWARD_<NAME>`. A secret can instead be given as `SUITEWARD_<NAME>_FILE`, the path of a file whose content is the value. Give one form, not both. A variable that is set but empty counts as unset.

Where you set them:

- **Isolated host:** in the `Environment=` and `Secret=` lines of `suiteward.container`, and in `/etc/suiteward/github.env` for the three GitHub values. After an edit, run `systemctl --user daemon-reload` and `systemctl --user restart suiteward.service`.
- **Co-located PC:** `deploy/local.ps1` has parameters only for the image and the GitHub values. The other settings keep their defaults on this PC. Changing one would mean changing the script, which this guide does not cover.

| Setting | Default | Meaning |
| --- | --- | --- |
| `SUITEWARD_DATABASE_URL` (secret, required) | none | The PostgreSQL URL. Use the `_FILE` form. The isolated units mount it from a Podman secret. |
| `SUITEWARD_ARTIFACT_DIR` (required) | none. The image sets `/data/artifacts` | Absolute path of the artifact store. |
| `SUITEWARD_HTTP_ADDR` | `127.0.0.1:8080`. The image sets `0.0.0.0:8080` | Address of the HTTP listener. The units publish it on the host's loopback only. |
| `SUITEWARD_SHUTDOWN_TIMEOUT` | `30s` (1 s to 10 min) | Time allowed for a graceful stop. The container's stop timeout must exceed this by at least 5 s. The units set 45 s, which covers the default. A value of 40 s or more needs a larger stop timeout. |
| `SUITEWARD_LOG_LEVEL` | `info` | One of `debug`, `info`, `warn`, `error`. |
| `SUITEWARD_JOB_WORKERS` | `4` (1 to 64) | Number of background workers. |
| `SUITEWARD_JOB_TIMEOUT` | `1m` (1 s to 1 h) | Time limit of one attempt of a background job. A `propose` reads about 150 protected files per minute of this value, so raise it for a large test suite. |
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

`/status` reports the URL of the exact source revision the binary was built from, as `source`. For a release, that is `https://github.com/IgnisDevNE/SuiteWard/tree/vX.Y.Z`. If `source` is absent, the build set none.

If you modify SuiteWard and run the modified version, you must point `source` at your modified source. The AGPL requires this in its section 13.

## 14. Troubleshooting

Start with `/status` and the logs.

- **Isolated host:** `curl -s http://127.0.0.1:8081/status`, then `journalctl --user -u suiteward.service -n 100 --no-pager`, and for the database `journalctl --user -u suiteward-db.service -n 50 --no-pager`.
- **Co-located PC:** `podman machine ssh "curl -s http://127.0.0.1:8081/status"`, then `podman logs --tail 50 suiteward`.

These are the `/status` fields you read:

- `tier` is the effective tier. `tierNote` appears when the setting and the enrollment disagree. Run setup again (section 7).
- `owner` shows the enrolled login. It is absent until setup succeeds.
- `github` has one entry per repository:
  - `lastSuccess` is the last successful synchronization.
  - `lastError` is the last synchronization error, as short text without secrets. If `waitUntil` is set, GitHub asked SuiteWard to wait because of a rate limit, and SuiteWard resumes by itself. Otherwise, check that the App is installed on that repository, and that the App ID, the installation ID and the key are the ones from section 4.
  - `protection` shows the results of section 8.
  - `checks` is the last error while SuiteWard published that repository's check. It is absent when there is none.
  - `bootstrap` shows why the first contract is waiting or stopped. `read-failed` means GitHub could not be read, and SuiteWard retries. `repository-changed` means the configured name now points at a different repository from the one enrolled, so check the name. `origin-not-passed` means the default branch's contract could not be read as a passing source. `assessment-conflict` means two assessments of one source disagree. `blocked-<reason>` means a block for the stated reason. `failed` means the bootstrap failed for good. Read the logs for the last three. The field is absent when nothing is wrong.
  - `merges` shows merge decisions. `waiting` means GitHub has not yet reported the merge commit, and SuiteWard retries. `read-failed`, `assessment-conflict` and `failed` need the logs.
  - `integrity` is `passed`, `failed` or `unavailable` for the current version. `failed` means SuiteWard refuses merges until the problem is resolved. `unavailable` changes no check.
- `outbox.failed` counts messages that SuiteWard could not deliver to GitHub after its retries. It should be 0. Read the logs for the repository. A refused write is often a missing App permission, so compare with section 4.
- `jobs.discarded` counts background jobs that failed for good. Read the logs.
- `version`, `schemaVersion` and `source` identify the release and the database schema that run.

Common problems:

- **The service does not start on the isolated host.** Podman refuses to start the container while a secret or `/etc/suiteward/github.env` is missing, and the journal shows which. The service restarts every five seconds until it starts. A unit that Podman cannot parse is not generated at all. Run the dry run in step 5.10.
- **`/readyz` answers 503.** The database does not answer, or the schema is not ready. Check that `suiteward-db` is healthy with `podman ps`, and read its logs.
- **Setup exits with code 2.** The `--test-check` list does not match the configured repositories. Give exactly one per repository, with no duplicates.
- **Setup exits with code 1.** A lookup failed. The owner login may not be a user account, the App may not be installed on every configured repository, or GitHub could not be reached. Nothing was written.
- **A command gets no reply.** Check that the first line starts with `/suiteward`, outside a code block, that it comes from your account, that you did not edit it, and that the pull request is still open. Another account's command gets no reply by design.
- **An approval is rejected.** The reply gives the reason: an invalid, replayed or expired code, or an unknown revision. Use a fresh code, and check the reference against the newest revision comment.
- **The check shows `approval-missing` after a push.** The push changed a protected file, which creates a new revision. Approve the revision named in the newest comment.
- **The new image does not start after an update.** Do not start an older image, because after a migration it refuses to run. Read the logs and report them to the project. To return to the earlier state, restore your backup, as described in the backup runbook, `docs/backup.md`.
