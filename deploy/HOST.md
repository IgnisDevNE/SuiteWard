# Remote host (isolated target): owner runbook

Host `<instance-id>`, us-east-2, AlmaLinux 9 (EL9) arm64, Podman 5.5 or later (EL9 ships 5.8.2 with the CVE-2026-57231 backport; `sudo suiteward install host` checks the fix itself), rootless for the unprivileged `suiteward` user. Check that `amazon-ssm-agent` is active (`systemctl is-active amazon-ssm-agent.service`) before you rely on Session Manager. SELinux stays enforcing: the units use named volumes, which Podman labels by itself, and no step here bind-mounts a path. No inbound ports and no SSH daemon: you administer it through SSM Session Manager from your own workstation.

The installer, the `suiteward` binary, installs the product image and its PostgreSQL as three rootless containers from the Quadlet units it renders from `deploy/quadlet/`: `suiteward-db`, the one-shot `suiteward-migrate` (the database owner migrates the schema and grants the application role its access) and `suiteward` (the service, which requires the migration and connects as the least-privilege role `suiteward_app`). It creates five Podman secrets, all mounted as files: a generated database owner password and the owner URL, the application role's URL, the GitHub App's private key and a generated MCP token. The three non-secret GitHub settings live in `~/.config/suiteward/github.env`. The images are pinned by digest in the units; there is no automatic update. The install guide ([section 10](../docs/install.md#10-running-on-a-separate-host-operators)) says what each step does and why; this runbook adds only what is specific to this host.

Never paste into chat, a file, a PR, or a log: any password or database URL, a GitHub or AWS credential (the App key, the MCP token), an SSM session token, the TOTP `otpauth://` URI, the base32 secret and any TOTP code. The only place the App key and the MCP token go is your own Session Manager terminal on the host, as the steps below say. An agent never holds host access; you run these steps in your own session and paste back only the outputs listed in section 3.

## 0. Before the host steps (GitHub, once)

1. The first release run creates the GHCR package `suiteward` as private. Make it public (GitHub, owner profile, Packages, `suiteward`, Package settings, Change visibility), so the host needs no registry credential; the image contains no secret. If the run fails with `permission_denied: write_package`, open the package settings, "Manage Actions access", grant the `SuiteWard` repository write access, and re-run the job.
2. The host runs a release image, pinned by its signed index digest in both SuiteWard units. No workflow moves a tag the host follows. A release binary carries the digest of its own release, so the signature you verify in section 1 covers it, and the installer needs no `--image`. If you pass `--image sha256:<index>` anyway (a development build needs it), take the digest from the release notes (the `Index digest:` line of the release run's summary) and verify it on your PC, as [install guide step 10.4](../docs/install.md#10-running-on-a-separate-host-operators) says.
3. Have the AWS CLI and the Session Manager plugin on your PC: the installer creates the GitHub App through your browser, which reaches the host through a port forward (section 2). If you prefer an App made by hand, use the checklist of [install guide step 10.4](../docs/install.md#10-running-on-a-separate-host-operators), and keep its `.pem` until the installer has stored it.

## 1. Open a session and get the installer onto the host

```
aws ssm start-session --target <instance-id> --region us-east-2
```

You land as `ssm-user`. Session Manager copies no files, so download the release files on the host, and verify them against what your PC verified. On your PC, verify `SHA256SUMS` with its signature ([install guide section 4](../docs/install.md#4-get-and-verify-the-installer)). The script prints the line `SHA256SUMS sha256: <hex>`:

```
pwsh ./scripts/verify-release.ps1 -Blob SHA256SUMS -Bundle SHA256SUMS.sigstore.json
```

On the host, as `ssm-user`, download the arm64 binary and `SHA256SUMS`, and compare. The first check must print the hash your PC printed, and the second must print `OK` for the binary:

```
cd "$(mktemp -d)"
curl -fsSLO https://github.com/IgnisDevNE/SuiteWard/releases/download/<tag>/suiteward_<tag>_linux_arm64
curl -fsSLO https://github.com/IgnisDevNE/SuiteWard/releases/download/<tag>/SHA256SUMS
sha256sum SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
sudo install -m 0755 suiteward_<tag>_linux_arm64 /usr/local/bin/suiteward
```

## 2. Install the host

As `ssm-user`, prepare the host once ([install guide step 10.2](../docs/install.md#10-running-on-a-separate-host-operators)):

```
sudo suiteward install host
```

It checks Podman and its CVE fix, creates the `suiteward` account with its subordinate ids and linger, and, because this is an EC2 instance, asks whether to refuse the metadata address `169.254.169.254` for that account: answer yes (`--metadata-rule yes`). D-DEPLOY says IMDSv2 with hop limit 1 keeps containers from the instance role; S1 showed that a rootless container can still obtain a token, because rootless networking makes the connection from a process of the `suiteward` user on the host. The rule covers IPv4 only, which is enough because the IPv6 metadata endpoint is off by default on EC2. It leaves the SSM agent alone, which runs as root: after the step, check that `sudo systemctl is-active amazon-ssm-agent.service` prints `active` and that a second `aws ssm start-session` from your workstation still opens.

Then, still as `ssm-user`, open a shell as `suiteward` with its user bus (the account has a `nologin` shell, so `sudo -iu` fails), as [install guide step 10.3](../docs/install.md#10-running-on-a-separate-host-operators) says:

```
r=/run/user/$(id -u suiteward)
sudo -u suiteward -H env XDG_RUNTIME_DIR=$r DBUS_SESSION_BUS_ADDRESS=unix:path=$r/bus bash
```

In that shell, run the installer with the manifest port pinned, so that you can start the forward first ([install guide step 10.4](../docs/install.md#10-running-on-a-separate-host-operators)):

```
/usr/local/bin/suiteward install --tier isolated --github yes --manifest-port 8765
```

On your PC, in another terminal, forward the port and open `http://127.0.0.1:8765/` in your browser. Use the same number for `portNumber`, `localPortNumber` and the browser: the listener answers 403 to any other Host, and GitHub redirects to `127.0.0.1:8765`.

```
aws ssm start-session --target <instance-id> --region us-east-2 --document-name AWS-StartPortForwardingSession --parameters '{"portNumber":["8765"],"localPortNumber":["8765"]}'
```

The installer then walks through the App, the installation, the repositories, the owner and the test checks. For each repository it writes `~/.config/suiteward/suiteward-ruleset-<owner>-<name>.json` on the host: `cat` it in the session, save the text as a file on your PC and import it ([install guide step 10.4](../docs/install.md#10-running-on-a-separate-host-operators), *The ruleset*). With an App you made by hand, give its values and `--github-app-key-file ~/app-key.pem` instead (paste the key without echo as that step says, and delete the file afterwards). Compare the key fingerprint the installer prints with the one on the App's settings page.

If Session Manager session logging (S3 or CloudWatch) is on for this instance, treat the MCP token as exposed to that log whenever you print it, and replace it once the verification is done.

The installer runs setup at the end on a real terminal (section 4 says what to turn off first), or prints the command. Turn off Session Manager logging before it runs.

## 3. Smoke test and what to paste back

Check the instance first, as `suiteward`:

```
suiteward doctor
```

Expect `ok` on every line. Then the smoke test. The service listens on loopback port 27081 (HTTP) and 27091 (MCP). Copy `deploy/smoke.sh` from the repository into place, one `cat` and a paste, then Ctrl-D, as `suiteward`:

```
cat > ~/smoke.sh
SMOKE_TIER=isolated sh ~/smoke.sh
```

It waits for `/readyz`, checks `/healthz` and `/status` (zero failed and discarded), the `tier` (`SMOKE_TIER=isolated` makes anything but `isolated` fail), the `github` step (it waits up to `SMOKE_GITHUB_TIMEOUT`, 120 s by default, until every repository in the `github` array of `/status` shows `lastSuccess`, and fails at once on a `lastError`; `SMOKE_GITHUB=0` skips only that step, for a stack without GitHub) and the `mcp` step (two requests to `http://127.0.0.1:27091/mcp`, `SMOKE_MCP_BASE`, that never read the token: a POST without a token must answer 401, a POST with `Origin: http://evil.example` 403). Then it runs a probe through the worker, enqueues a probe whose job is delayed by 20 s, restarts the service and checks that the delayed job still ran afterwards, and prints a summary block with a timestamp and elapsed milliseconds per step. That step proves a delayed job survives a restart; it is no proof of outbox durability. To assert the version, run `SMOKE_VERSION=<version> sh ~/smoke.sh`: `/status` reports the release tag as `version`. `SMOKE_BASE` and `SMOKE_MCP_BASE` name other ports. It exits non-zero and names the failing step otherwise.

Paste back: the whole summary block (it holds the `tier`, `github` and `mcp` lines; if any step fails, add `journalctl --user -u suiteward.service -n 50 --no-pager`: log lines carry no secret, and a restart loop shows its cause there); the output of `suiteward doctor`; the output of `systemctl --user status suiteward-db.service suiteward-migrate.service suiteward.service --no-pager`; `podman --version`; `podman inspect suiteward --format '{{.Config.StopTimeout}}'` (must print 45, the contract needs it above the 30 s shutdown timeout); `sudo ss -tlnp` (every listener on `127.0.0.1` or `::1` except the system and SSM ones you recognise; both published ports, 27081 and 27091, show as `rootlessport`); about 30 `journalctl` lines of the service around the restart; and how long the first pull and start took.

## 4. Owner setup

The installer runs `suiteward setup` for you at the end of the install, on a real terminal in your own Session Manager session as `suiteward`, never in an agent's session: proof of identity is access to this host. It runs it only when `/status` shows no owner, so a re-run or an upgrade never replaces the TOTP secret. If it printed `suiteward enroll --tier isolated` instead, run that once yourself, as `suiteward` on a real terminal: it asks for what you leave out and runs setup for you (the repositories are in `~/.config/suiteward/github.env`). To enroll again, for example after losing the authenticator, use the same command: it asks you to type `enroll` before it replaces the secret. The steps are in [install guide section 6](../docs/install.md#6-run-setup). Import the ruleset of each repository ([section 7](../docs/install.md#7-create-the-ruleset)) if you have not, and run `suiteward doctor` again.

**Session Manager logging.** `setup` (which `suiteward enroll --tier isolated` runs for you) prints the `otpauth://` URI and the base32 secret to the terminal, so a logged session records them. Disable Session Manager logging before you run it. If it was on during setup, turn it off and run `suiteward enroll --tier isolated` again: re-running replaces the secret, so the recorded one stops working, and it is safe only with logging off. Paste back only the protection result lines and `/status`, never the setup output or a code.

The per-phase verification scenarios and the MVP acceptance rehearsal that this file once held (M1.5 to M1.8) were removed: they describe flows that no longer exist, such as pushing phase branches, pulling `:deploy`, and the manual install. They remain in git history (the tag `v0.1.3` holds the M1.5 to M1.7c scenarios, `v0.2.0` the M1.8 rehearsal) and in the [phase pages](../docs/plan/phases/); the current behavior is in the [install guide](../docs/install.md) and the [runtime contract](../docs/contracts/runtime.md). The re-bootstrap and the restore are in the [backup runbook](../docs/backup.md).

## 5. Reach `/status`

From your workstation, in a second terminal:

```
aws ssm start-session --target <instance-id> --region us-east-2 --document-name AWS-StartPortForwardingSession --parameters portNumber=27081,localPortNumber=27081
curl http://127.0.0.1:27081/status
```

The MCP listener is forwarded the same way (a second terminal, or a second session). The token is your own to give the client; it never goes in a file of the repository:

```
aws ssm start-session --target <instance-id> --region us-east-2 --document-name AWS-StartPortForwardingSession --parameters portNumber=27091,localPortNumber=27091
```

To read the token once, in your own Session Manager terminal on the host, and put it in your own secret store (your password manager); nothing else keeps a copy:

```
podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-mcp-token
```

The real Claude Code smoke of a phase runs against `http://127.0.0.1:27091/mcp` with that token (`claude mcp add --transport http suiteward http://127.0.0.1:27091/mcp --header "Authorization: Bearer <token>"`, then `tools/list`, `status` and `sync` in a session). An agent never receives the token: you run that client yourself, or tell the orchestrator only the outcome.
