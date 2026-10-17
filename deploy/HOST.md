# Remote host (isolated target): owner runbook for M1.2, M1.4, M1.5, M1.6a, M1.6b, M1.7, M1.7b and M1.7c

Host `i-0f556162ae6b59b22`, us-east-2, Ubuntu 24.04 arm64, Podman 4.9.3, rootless for the unprivileged `suiteward` user. No inbound ports and no SSH daemon: you administer it through SSM Session Manager from your own workstation. This runs the product image and its PostgreSQL as two rootless containers from the Quadlet units in `deploy/quadlet/`. It needs three secrets, all Podman secrets mounted as files: a generated database password (section 3, you never type it), the GitHub App's private key and a generated MCP token (section 4a, M1.4). The three non-secret GitHub settings live in a host file you write (section 4a).

Never paste into chat, a file, a PR, or a log: any password or database URL, a GitHub or AWS credential (the App key, the MCP token), an SSM session token, the TOTP `otpauth://` URI, the base32 secret and any TOTP code. The only place the App key and the MCP token go is your own Session Manager terminal on the host, as the steps below say. An agent never holds host access; you run these steps in your own session and paste back only the outputs listed in sections 6 and 6a.

## 0. Before the host steps (GitHub, once)

1. The first run of the `Deploy` workflow creates the GHCR package `suiteward` as private. Make it public (GitHub, owner profile, Packages, `suiteward`, Package settings, Change visibility), so the host needs no registry credential; the image contains no secret. If the first run fails with `permission_denied: write_package`, open the package settings, "Manage Actions access", grant the `SuiteWard` repository write access, and re-run the job.
2. When the `promote` job waits in the `remote-poc` environment, approve it. That moves the `deploy` tag, and only to the digest built by that same run. A run waiting for approval holds the `deploy-publish` concurrency group, so a later push queues behind it. The `remote-poc` environment must list `phase/M1.2`, `phase/M1.4`, `phase/M1.5`, `phase/M1.6a`, `phase/M1.6b` (each phase builds from its branch) and `main` (once merges to `main` should promote) as deployment branches. The host cannot start until `ghcr.io/ignisdevne/suiteward:deploy` exists.
3. M1.6b (section 6c) needs `phase/M1.6b` in that list before its first `promote`; the other branches stay.

## 1. Open a session

```
aws ssm start-session --target i-0f556162ae6b59b22 --region us-east-2
```

You land as `ssm-user`. Become the service user with its systemd user manager (the account has a `nologin` shell, so `sudo -iu` fails):

```
sudo loginctl enable-linger suiteward
r=/run/user/$(id -u suiteward)
sudo -u suiteward -H env XDG_RUNTIME_DIR=$r \
  DBUS_SESSION_BUS_ADDRESS=unix:path=$r/bus bash
systemctl --user is-system-running
```

The lines are short on purpose: long lines pasted into a Session Manager terminal can break. `is-system-running` must print `running` (or `degraded`); if the bus socket is missing, wait a few seconds and retry. Start every new session this way. Every block below runs as `suiteward` unless it says otherwise.

## 2. Remove the S1 spike (once)

```
systemctl --user stop suiteward-s1.service
rm -f ~/.config/containers/systemd/suiteward-s1.container
rm -rf ~/.config/suiteward-s1
rm -rf ~/.config/systemd/user/podman-auto-update.timer.d
systemctl --user daemon-reload
systemctl --user restart podman-auto-update.timer
podman rm -f suiteward-s1
podman secret rm suiteward-s1-app-key
podman volume rm suiteward-s1-data
podman rmi ghcr.io/ignisdevne/suiteward-s1:deploy
```

`daemon-reload` drops the generated `suiteward-s1.service` once its quadlet file is gone. The `rm -rf` of `podman-auto-update.timer.d` removes the five-minute drop-in that S1 added with `systemctl --user edit`; the restart returns the timer to its daily default. Commands for things already gone (a missing secret, no drop-in) print an error that you can ignore. Then `podman ps -a`, `podman secret ls`, `podman volume ls` must show nothing named `suiteward-s1`.

## 3. Create the database secrets

One chained command generates the password and creates both Podman secrets from it, so you never see or type it and the two cannot disagree: `suiteward-db-password` (read by PostgreSQL) and `suiteward-database-url` (the full URL, read by the service). A line ending in `&&` or `|` continues on the next one, and the chain stops at the first failure.

```
pw=$(openssl rand -hex 24) &&
h='suiteward-db:5432/suiteward?sslmode=disable' &&
printf '%s' "$pw" |
  podman secret create suiteward-db-password - &&
printf 'postgres://suiteward:%s@%s' "$pw" "$h" |
  podman secret create suiteward-database-url -
unset pw h
podman secret ls
```

Both names must be listed. Then check the shape of both values without showing them; each `grep -c` prints only a count, and both must print `1` (48 hex characters, and the URL around them):

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

A `0` means a value is malformed: remove both secrets and repeat this section (nothing has started yet, so no volume holds the password). Run the creation once. The database is initialised with the password on its first start and keeps it in the `suiteward-db-data` volume, so replacing a secret later locks the service out; to start over, stop `suiteward.service` and `suiteward-db.service` first (`systemctl --user stop suiteward.service suiteward-db.service`), then remove the volume (`podman volume rm suiteward-db-data`) and BOTH secrets (`podman secret rm suiteward-db-password suiteward-database-url`), then repeat this step. The step is only re-runnable after removing a first secret it created before failing, because `podman secret create` refuses an existing name. `sslmode=disable` is deliberate: the database listens only on the private `suiteward` network and its image carries no certificate.

## 4. Block the metadata service for the `suiteward` user

Do this before the services start. D-DEPLOY says IMDSv2 with hop limit 1 keeps containers from the instance role; S1 showed a rootless container can still obtain a token, because rootless networking makes the connection from a process of the `suiteward` user on the host. Reject `169.254.169.254` for that user.

First the S1 check, as `suiteward` (open a shell as in section 1), before the rule exists. It must print `200`, which shows the check can see the problem:

```
podman run --rm docker.io/curlimages/curl@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777 -s -m 3 -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' -o /dev/null -w '%{http_code}\n' http://169.254.169.254/latest/api/token
```

Then, as `ssm-user` (type `exit` to leave the `suiteward` shell), make sure `nft` exists:

```
command -v nft || sudo apt-get install -y nftables
```

Write the rule. Run the first command, wait until the cursor stops, paste the nft block that follows it, then press Ctrl-D on an empty line:

```
sudo mkdir -p /etc/suiteward
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

Then a unit that loads it at every boot, independent of `/etc/nftables.conf`. It uses the path `command -v nft` reports, not a hardcoded one; `ExecStop` starts with `-` so a missing table never fails a stop:

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
sudo systemctl daemon-reload
sudo systemctl enable --now suiteward-imds-block.service
sudo nft list table inet suiteward_imds
```

Run the curl check from above again as `suiteward`. It must now print `000` (anything but `200`). Then confirm the SSM agent still works, which proves the rule does not touch it: as `ssm-user`, `sudo systemctl is-active snap.amazon-ssm-agent.amazon-ssm-agent.service` prints `active`, and a second `aws ssm start-session` from your workstation still opens.

Limits of the rule: it covers IPv4 only, which is enough because the IPv6 metadata endpoint is off by default on EC2. The table lives in the kernel until reboot and is reloaded by the enabled unit. If `/etc/nftables.conf` on this host starts with `flush ruleset`, restarting `nftables.service` drops the table; load it again with `sudo systemctl restart suiteward-imds-block.service`. Optionally reboot the instance once and repeat `sudo nft list table inet suiteward_imds` to confirm the rule is loaded at boot. Never run commands that print metadata credentials.

## 4a. GitHub and MCP settings (M1.4)

Before the updated `suiteward.container` is installed (section 5; on a host that already runs M1.2, before you replace that file). The unit now mounts two more Podman secrets and reads one env file, and all three are mandatory: a missing secret or a missing file makes `podman` refuse to start the container, which `Restart=always` repeats every five seconds and the journal shows. It does not run silently without GitHub. The GitHub settings are all set or all unset (runtime contract): an env file with only some of them makes `serve` exit at startup, and `smoke.sh` then fails at `readyz` after its timeout.

**The three non-secret settings.** As `ssm-user` (type `exit` to leave the `suiteward` shell). Run the first command, wait until the cursor stops, paste the three lines with the placeholders replaced (the App id and installation id from the App's settings page, the repositories as `owner/name`, comma-separated, 1 to 20, no spaces or quotes), then press Ctrl-D on an empty line:

```
sudo mkdir -p /etc/suiteward
sudo tee /etc/suiteward/github.env >/dev/null
```

```
SUITEWARD_GITHUB_APP_ID=<app id>
SUITEWARD_GITHUB_INSTALLATION_ID=<installation id>
SUITEWARD_GITHUB_REPOSITORIES=<owner/name>,<owner/name>
```

The file holds no secret (the quadlet reads it as `EnvironmentFile=/etc/suiteward/github.env`, readable by the `suiteward` user). Never commit these values.

**The App key.** As `suiteward`. Copy the App's private key (PEM) to the host through your own session. The first line turns the terminal echo off, so the key does not appear on screen (or in a Session Manager log that records output): run it, wait until the cursor stops, paste the whole PEM from the file on your workstation, press Enter, then Ctrl-D (you see nothing while you do). The next command reads that file into the secret and the last one destroys it; the key is never echoed:

```
trap 'stty echo' INT; stty -echo; (umask 077; cat > ~/app-key.pem); stty echo; trap - INT
podman secret create suiteward-github-app-key ~/app-key.pem
shred -u ~/app-key.pem
```

If the terminal goes silent, type `stty echo` and press Enter; after an aborted paste, run `shred -u ~/app-key.pem` and repeat.

Check the shape without showing the value; it must print `1`:

```
podman secret inspect --showsecret \
  --format '{{.SecretData}}' suiteward-github-app-key |
  grep -Ec '^-----BEGIN (RSA )?PRIVATE KEY-----$'
```

**The MCP token.** Generated on the host, 32 random bytes hex-encoded (64 characters, above the 32-byte minimum), straight into the secret. `printf` drops the newline that `openssl` appends; with a newline the secret would be 65 bytes and the client would need it too:

```
printf '%s' "$(openssl rand -hex 32)" |
  podman secret create suiteward-mcp-token -
podman secret inspect --showsecret \
  --format '{{.SecretData}}' suiteward-mcp-token |
  grep -Ecx '[0-9a-f]{64}'
```

It must print `1`. Run each creation once (`podman secret create` refuses an existing name; to replace a secret, `podman secret rm` it, create it again and `systemctl --user restart suiteward.service`).

**Where the client gets the token.** The token never goes in the repository and never in chat. Podman 4.9.3 supports `--showsecret` (section 3 already uses it), so you read it once on the host, in your own Session Manager terminal, and put it in your own secret store (your password manager); nothing else keeps a copy:

```
podman secret inspect --showsecret \
  --format '{{.SecretData}}' suiteward-mcp-token
```

That prints the token to the terminal. If Session Manager session logging (S3 or CloudWatch) is on for this instance, treat the token as exposed to that log, and replace it as above once the verification is done. On the client side `claude mcp add --transport http` sends it as `Authorization: Bearer <token>` (section 7); Claude Code stores that header in its own configuration on your workstation, not in a repository.

## 5. Install the quadlets and start

Copy the five files in `deploy/quadlet/` from the repository (on your workstation) into place, one `cat` per file: run the command, wait until the cursor stops, paste the file's content, then press Ctrl-D on an empty line.

```
mkdir -p ~/.config/containers/systemd
cat > ~/.config/containers/systemd/suiteward.network
cat > ~/.config/containers/systemd/suiteward-db-data.volume
cat > ~/.config/containers/systemd/suiteward-data.volume
cat > ~/.config/containers/systemd/suiteward-db.container
cat > ~/.config/containers/systemd/suiteward.container
```

Podman 4.9.3 skips a unit it cannot parse (you would only see "unit not found"). Dry-run the generator; it prints every generated unit or the error:

```
podman --version
/usr/lib/systemd/system-generators/podman-system-generator --user --dryrun
```

The units use only keys the 4.9 documentation lists (`Secret`, `Environment`, `EnvironmentFile` and `PublishPort` are in the Podman v4.9.3 `podman-systemd.unit(5)` container section); the memory limits and stop timeouts go through `PodmanArgs` because 4.9 has no key for them, and there is no `Notify=healthy`. The dry run is the check. If a key is rejected, remove that line, start anyway, and report which one. Pre-pull the images (a first pull can exceed systemd's start timeout), then start. The service requires the database, which pulls in the network and both volumes:

```
podman pull docker.io/library/postgres:18.6-trixie@sha256:5a5a84b19854a9ffaa54082c166ff4ec27473a361e496e5ea167f298f2da9722
podman pull ghcr.io/ignisdevne/suiteward:deploy
systemctl --user daemon-reload
systemctl --user start suiteward.service
systemctl --user enable --now podman-auto-update.timer
systemctl --user status suiteward-db.service suiteward.service --no-pager
podman ps
```

Two checks that the host honours what the units ask for (the memory limit needs memory cgroup delegation to the user manager):

```
podman info --format '{{.Host.NetworkBackend}}'
podman inspect suiteward-db --format '{{.HostConfig.Memory}}'
```

The first must print `netavark`; the second must print `402653184` (384 MiB). If either differs, report it with the dry-run output; do not improvise a fix. Then re-run the S1 curl check from section 4 with `--network suiteward` added right after `--rm`, which is the network the service itself uses; it must print `000` too.

The service now also needs outbound HTTPS to `https://api.github.com`. The IMDS rule of section 4 rejects only `169.254.169.254` for the `suiteward` user and leaves every other destination alone, so GitHub stays reachable; check it from the same network the service uses (an unauthenticated `GET /zen`, which prints `200`; `403` would mean the shared unauthenticated rate limit is used up, and `000` that the host cannot reach GitHub, so stop and report it):

```
podman run --rm --network suiteward docker.io/curlimages/curl@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777 -s -m 10 -o /dev/null -w '%{http_code}\n' https://api.github.com/zen
```

On a host that already runs M1.2: do section 4a, then `cat` the new `suiteward.container` over the old one (as above), `systemctl --user daemon-reload` and `systemctl --user restart suiteward.service`. The image must be the M1.4 one (`podman pull ghcr.io/ignisdevne/suiteward:deploy`, or `systemctl --user start podman-auto-update.service`): an M1.2 image ignores the new settings and never opens port 8090, so the smoke test fails at `github`.

Generated units start at boot through linger; `enable` is not used on them. If the service fails once while the database is still initialising, `Restart=always` retries every five seconds; that is expected on the very first start. `suiteward-db` must show `(healthy)`. The auto-update timer is daily by default; `systemctl --user start podman-auto-update.service` runs one update now, and `podman auto-update --dry-run` shows what would change. Logs:

```
journalctl --user -u suiteward.service -n 100 --no-pager
journalctl --user -u suiteward-db.service -n 50 --no-pager
```

## 6. Smoke test and what to paste back

The service listens on loopback port 8081 (HTTP) and 8091 (MCP). Copy `deploy/smoke.sh` from the repository into place (same `cat` and paste, then Ctrl-D), as `suiteward`:

```
cat > ~/smoke.sh
sh ~/smoke.sh
```

It waits for `/readyz`, checks `/healthz` and `/status` (zero failed and discarded), then the `tier` step (M1.5: `/status` must report a `tier`; run it as `SMOKE_TIER=isolated sh ~/smoke.sh` on this host so anything but `isolated` fails, locally `SMOKE_TIER=co-located`), then two M1.4 steps. `github` waits (up to `SMOKE_GITHUB_TIMEOUT`, 120 s by default) until every repository in the `github` array of `/status` shows `lastSuccess`; it fails at once on a `lastError` and when `/status` has no `github` array (this host must run with GitHub configured; `SMOKE_GITHUB=0` skips only the step, for a stack started without the GitHub settings). `mcp` sends two requests to `http://127.0.0.1:8091/mcp` (`SMOKE_MCP_BASE`) and never reads the token: a POST without a token must answer 401, a POST with `Origin: http://evil.example` 403. Then it runs a probe through the worker (the outbox relay is a periodic job, so a probe takes from about a second to several seconds to be delivered), enqueues a probe whose job is delayed by 20 s, restarts the service and checks that the delayed job still ran afterwards, and prints a summary block with a timestamp and elapsed milliseconds per step. That step proves a delayed job survives a restart; it does not show whether the probe's outbox message was relayed before or after the restart, so it is no proof of outbox durability. `/status` reports the image version as `sha-<commit>`; to assert it, run `SMOKE_VERSION=sha-<commit> sh ~/smoke.sh`, with the commit the `Deploy` run built (`podman inspect suiteward --format '{{index .Config.Labels "org.opencontainers.image.revision"}}'` prints the commit of the running image; a local build without the version argument reports `dev`). It exits non-zero and names the failing step otherwise. Once a later image has been promoted, run `sh ~/smoke.sh --promote` to also run `podman auto-update` once and report the image digest before and after.

Paste back: the whole summary block (it holds the `tier`, `github` and `mcp` lines; if any step fails, add `journalctl --user -u suiteward.service -n 50 --no-pager`: log lines carry no secret, and a restart loop shows its cause there); the output of `systemctl --user status suiteward-db.service suiteward.service --no-pager`; `podman --version` and the generator dry run (they contain no secret); the one-line IMDS status codes (200 before the rule, 000 after, on both networks); `podman inspect suiteward --format '{{.Config.StopTimeout}}'` (must print 45, the contract needs it above the 30 s shutdown timeout); `sudo ss -tlnp` (every listener on `127.0.0.1` or `::1` except the system and SSM ones you recognise; both published ports, 8081 and 8091, show as `rootlessport`); about 30 `journalctl` lines of the service around the restart; and how long the first pull and start took.

## 6a. Owner setup (M1.5)

Binds you as the owner of the instance and enrolls your TOTP secret (D-IDENTITY). It needs the M1.5 image and quadlet (section 5 upgrade steps: new `suiteward.container`, `daemon-reload`, restart) and a running service; it runs migrations itself, so it also works on a fresh start. Run it once, in your own Session Manager session, as `suiteward` (never an agent's session: proof of identity is access to this host, which only you have). It is interactive, so it needs a real terminal.

1. **Have your TOTP authenticator app ready** (any app that scans an `otpauth://` QR code or takes a base32 secret). If Session Manager session logging (S3 or CloudWatch) is on for this instance, read the warning below before you start.
2. **List the configured repositories and run setup.** The repositories are in `/etc/suiteward/github.env` (no secret in that file):

   ```
   grep SUITEWARD_GITHUB_REPOSITORIES /etc/suiteward/github.env
   podman exec -it suiteward /suiteward setup --owner <your GitHub login> \
     --test-check <owner/name>=<check> --test-check <owner/name>=<check>
   ```

   Exactly one `--test-check` per configured repository: a missing, extra or duplicate one is a usage error (exit 2) and nothing is written. `<check>` is the exact name of the required status check that runs the project's tests, as it appears in that repository's ruleset under required checks (for the `SuiteWard` repository itself, `CI / Gate`). It is not `SuiteWard / Contract`, which SuiteWard publishes and verifies separately. Setup first looks up your login and every repository with the configured installation; if any lookup fails it exits 1 before anything is written.
3. **Enroll the secret.** After the lookups, setup prints a warning and waits for you to press Enter; anything else, end of input or Ctrl-C aborts with exit 1 before the secret is generated, and writes nothing. Then it prints, once, an `otpauth://` URI and the base32 secret. Scan the URI (or type the base32 secret) into the authenticator, then type a current code at the prompt. You get three attempts; three wrong codes exit 1 and write nothing.
4. **Read the protection results.** For each repository setup prints the result of the protection check (`enforced`, `findings`, `enforcement-unavailable` or `unknown`) with its instructions: what to change in the repository's ruleset, or the manual check that a ruleset's bypass list is empty. SuiteWard verifies and reports; it never changes protection. Setup exits 0 once enrollment succeeded, whatever the protection results.

**Session Manager logging.** The URI and the base32 secret are printed to the terminal, so a logged session records them. Disable Session Manager logging before you run setup. If it was on during setup, turn it off and run setup again to rotate the secret: re-running replaces the secret, so the recorded one stops working. The second run is only safe with logging off, because it prints the new URI and secret into the log again.

**What to paste back.** Only the protection result lines of step 4 and the `/status` output. Never the setup output above them: the `otpauth://` URI, the base32 secret and the codes you typed.

**Re-running setup** replaces the TOTP secret (and records a new policy revision), so enroll the new one in the authenticator and delete the old entry.

**Checking the result.** Through the forwarded port (section 7) or on the host, `/status` shows `owner` (`login`, `accountId`, `enrolledAt`) and `"tier": "isolated"`, plus one `protection` entry per repository:

```
curl -s http://127.0.0.1:8081/status
```

If `tierNote` says to re-run setup, the enrollment and the setting disagree on the tier: run setup again.

**Co-located target (this PC).** The same command through the Podman machine's container, `podman exec -it suiteward /suiteward setup --owner <login> --test-check <owner/name>=<check> ...`, with the same repositories you gave `deploy/local.ps1`. That tier is `co-located` (`deploy/local.ps1` declares it): the agent that works on this PC can run setup too, so SuiteWard reports reduced assurance.

## 6b. PR commands and bootstrap (M1.6a)

SuiteWard now posts comments in pull requests and acts on the owner's `/suiteward approve|revoke <ref> <code>` comments ([D-COMMANDS](../docs/plan/decisions.md)). You verify it on `IgnisDevNE/SuiteWardQ` with the owner scenario below, one target at a time. M1.6a adds no quadlet change, no secret and no setting: the image and the App's existing Pull requests: Read and write permission are enough.

### 6b.1 One instance per repository

Two instances must never watch the same repository. Both post as the same App and each has its own TOTP secret, so with two watchers:
- both post an acknowledgment for every command (two comments for one command);
- each checks your code against its own secret, so one of them answers the code meant for the other with an invalid-code acknowledgment.

Rules:
- The host is the default watcher of `SuiteWardQ`. The co-located stack runs with the GitHub settings only while you verify it (section 6b.5).
- Only the target under test has the GitHub settings. Run the other target without GitHub, or stop it.

Without GitHub:
- **On the host:** stop the service. The quadlet always mounts the App key and reads `/etc/suiteward/github.env`, and the GitHub settings are all or none, so a host without GitHub is a stopped service. The database stays up and all state is kept. As `suiteward` (section 1):

  ```
  systemctl --user stop podman-auto-update.timer
  systemctl --user stop suiteward.service
  ```

  Restore it with `systemctl --user start suiteward.service` and `systemctl --user start podman-auto-update.timer`, but only as 6b.5 step 5 says.
- **Locally:** run `./deploy/local.ps1` with none of the GitHub parameters. It replaces the container with one without GitHub and keeps the state. `./deploy/local.ps1 -Down` stops the stack without removing anything. See [local development](../docs/local-development.md#verifying-m16a-on-the-co-located-stack).

### 6b.2 Before you start (owner)

1. Add `phase/M1.6a` to the deployment branches of the `remote-poc` environment (GitHub, repository Settings, Environments). Without it the `promote` job of the `Deploy` run is refused.
2. Close PR #8 (left open by the P0 measurement) before opening the verification PR. The bootstrap proposes the baseline in the lowest-numbered open PR, so #8 would otherwise carry the summary.
3. Keep your authenticator app at hand. It holds one entry per target (host and, later, co-located); use the entry that belongs to the target under test.
4. The `main` branch of the test repository needs a `.suiteward.yml` that covers its test files, because the bootstrap never proposes an empty baseline (ADR 0010). For `SuiteWardQ` the verified content is exactly:

   ```
   version: 1
   include:
     - tests/**
   ```

   The ruleset blocks pushing it, so add it with the ruleset temporarily disabled and re-enable it afterwards. Before the scenario, `/status` must show `protection.result` `enforced` again; the check runs every 15 minutes.
5. **Only on the remote host, once (2026-10-14):** the `c95c1ff` image proposed an empty `P1-R1` on `SuiteWardQ` PR #13 and it was approved; that proposal can never be promoted (the service logs `github bootstrap blocked` every minute) and the declaration does not unstick it, because an approved proposal never moves on. After the declaration is on `main` and the new image runs: reopen PR #13, comment `/suiteward revoke P1-R1 <code>`, wait for the revocation acknowledgment, close PR #13. The next PR you open then gets the bootstrap summary for `P2-R1`.

### 6b.3 Deploy on the host

1. **Before the deploy (done 2026-10-14):** on the running M1.5 image, `/status` showed no `lastError` in `github` and `outbox.failed` 0; the owner also saw both repositories synchronizing, with discarded 0. Repeat the check (`curl -s http://127.0.0.1:8081/status`) if the host was restarted since.
2. Push to `phase/M1.6a`. Approve the `promote` job of the `Deploy` run of the phase head commit (section 0), because older queued runs may exist. That moves `deploy` to the digest that run built.
3. On the host, as `suiteward` (section 1), pull the image and restart. The new image applies migration 00013 at start:

   ```
   podman pull ghcr.io/ignisdevne/suiteward:deploy
   systemctl --user restart suiteward.service
   SMOKE_TIER=isolated SMOKE_VERSION=sha-<commit> sh ~/smoke.sh
   ```

   `<commit>` is the commit the `Deploy` run built. The smoke test must pass.
4. After the deploy, before verifying, check `/status` (section 7, or on the host):

   ```
   curl -s http://127.0.0.1:8081/status
   ```

   Confirm `schemaVersion` is 13, no entry of `github` shows `lastError`, and `outbox.failed` is 0. If any of these fails, stop and report; do not start the scenario.

### 6b.4 The owner scenario, per target

Run it on `SuiteWardQ`, on a fresh PR, with the target under test as the only watcher. The host first, then the co-located stack (6b.5).

1. **Open a fresh PR** on `SuiteWardQ` (any change; it need not touch a protected file). It must be the lowest-numbered open PR, so close any other open PR first (6b.2 step 2).
2. **The bootstrap summary.** It appears up to about 2 to 3 minutes after the PR is opened: the bootstrap reads the PRs of the last 60 s synchronization, then runs every 60 s, and the outbox relay posts the comment. SuiteWard comments on that PR: "SuiteWard bootstrap proposal P1-R1". It shows the pinned origin commit and branch, the number of protected files, the inventory and scope rules (at most 20 of each are listed), the installation tier (`isolated` on the host, `co-located` locally, with a warning about reduced assurance), and a copyable `/suiteward approve P1-R1 <code>`. Review it. Nothing is canonical yet.
3. **Approve.** Comment `/suiteward approve P1-R1 <6-digit code>` with the current code of this target's authenticator entry. The command waits for the synchronization watermark (every repository must complete a synchronization cycle after your comment), about 2 to 3 minutes. Expect, in this order:
   - an approval acknowledgment ("Approval recorded for P1-R1"), in about 2 to 3 minutes;
   - then a "Bootstrap complete" comment, up to a minute later: `P1-R1` is canonical version `V1`.
4. **Replay.** Comment the same command with the same code again, within 30 seconds of the first; a minute at best (a code is accepted only within one 30-second step either side of the comment's time). Expect an acknowledgment that the code was already used, and no change. A later retry fails as invalid-code instead, which is also correct.
5. **Revoke after promotion.** Wait for the next code, then comment `/suiteward revoke P1-R1 <new code>`. Expect an acknowledgment that `P1-R1` is already promoted and the canonical version is unchanged; a revocation never undoes a promotion.
6. **Another account (optional).** Comment `/suiteward approve P1-R1 123456` from an account that is not the owner. Expect no reply: it is only recorded in the ledger.
7. **An edited command.** Comment `/suiteward revoke P1-R1 123456` (a made-up code) and edit the comment within seconds, before the next synchronization reads it. Expect an acknowledgment that it was edited before it was read and not processed. If you instead see an invalid-code acknowledgment, synchronization read it first; try again with a faster edit. The made-up code uses no step.
8. **Check `/status`.** `outbox.delivered` grew by one per comment SuiteWard posted (summary, acknowledgments, promotion), and `outbox.failed` is 0. No `lastError` in `github`.
9. **Close the PR** when this target is done, so that no other instance later reads your commands (6b.5 step 5).

Paste back (no secrets): the PR numbers, what each comment said in one line, and the `/status` output of step 8. Never paste a code, the `otpauth://` URI, a base32 secret, a token or setup output.

### 6b.5 Co-located stack

The stack was purged after M1.5, so it starts from nothing.

1. **Stop the host's watching** ("Without GitHub" in 6b.1), and check that `systemctl --user is-active suiteward.service` prints `inactive`.
2. **Start the stack with GitHub, enroll, verify, then stop its GitHub** as in [local development](../docs/local-development.md#verifying-m16a-on-the-co-located-stack). `suiteward setup` there needs a fresh authenticator entry: scan its URI into a new entry named so you can tell it from the host's. Never reuse the host's secret.
3. **Run the scenario** (6b.4) on a fresh PR, with the co-located entry's codes.
4. **Close the PR**, then put the stack back without GitHub (`./deploy/local.ps1`).
5. **Restore the host** ("Without GitHub" in 6b.1), only after steps 3 and 4: the host reads comments of open PRs created after its own enrollment, and would answer the co-located commands with invalid-code acknowledgments.
6. **Check the host again:** `/status` shows no `lastError` and `outbox.failed` 0 after a few minutes.

Do not reboot the host during this section: a stop is not persistent across a reboot, so the host would come back watching with GitHub.

A target that has bootstrapped keeps its Suite: it posts no new summary, so repeating the scenario needs a fresh database.

### 6b.6 Troubleshooting

- **No summary:** there is no open PR, or the Suite already has a canonical version (it is bootstrapped). A summary also needs a completed enrollment (`/status` shows `owner`).
- **No bootstrap summary while a PR is open:** the default branch has no protected files (the service log says `awaiting an initial contract`). Add `.suiteward.yml` with include rules, see 6b.2.
- **The carrier PR was closed before you approved:** the bootstrap moves to a new proposal on the lowest-numbered open PR, `P2-R1`. Approve the reference in the new summary.
- **A command is never answered:** a repository has never synchronized, so the watermark has no horizon. Read `github` in `/status` (`lastError`, `waitUntil`); the command waits behind it and is processed once it clears. Commands are processed one at a time across all repositories, in comment order.
  If every repository in `/status` shows a recent `lastSuccess` and a command still gets no acknowledgment, the repository list changed after setup: an enrolled repository that is no longer configured (or was renamed) never synchronizes again, and `/status` does not list it. Its old `lastSuccess` stays behind as the horizon, so commands wait behind it and the service log shows no warning (the `has not synchronized yet` warning appears only for a repository that never synchronized). Re-run setup after changing the repository list.
- **An entry in `outbox.failed`:** a comment could not be posted. Read the service journal (`journalctl --user -u suiteward.service -n 100 --no-pager`; lines carry no GitHub text or secret) and report the key it names.
- **An acknowledgment says the code was not accepted:** the clock. The code is checked at the comment's time with a tolerance of one 30-second step either side; check the authenticator's time sync. Also check that you used the entry of this target, not the other one.
- **Two acknowledgments for one command, or an unexpected invalid-code:** two instances watch the repository. Stop one (6b.1).

### 6b.7 Never

No token, code, secret or setup output in tickets, logs or messages. The warning of section 6a stays in force: Session Manager session logging records the `otpauth://` URI and the base32 secret that `setup` prints, so turn it off before running `setup` here as well.

## 6c. The `SuiteWard / Contract` check (M1.6b)

SuiteWard now publishes the `SuiteWard / Contract` check on the head of every open pull request ([D-CHECKS](../docs/plan/decisions.md)). You verify it on `IgnisDevNE/SuiteWardQ` with the owner scenario below, one target at a time. M1.6b adds no quadlet change, no secret and no setting: the App already holds Checks: write. The `/status` fields are in the [runtime contract](../docs/contracts/runtime.md#check-job-and-publisher-m16b).

### 6c.1 Before you start (owner)

1. Add `phase/M1.6b` to the deployment branches of the `remote-poc` environment (GitHub, repository Settings, Environments). Without it the `promote` job of the `Deploy` run is refused.
2. Confirm that `SuiteWardQ` `main` still equals V1's origin, `5998e90`, with 13 protected files (`tests/**` plus `.suiteward.yml`).
3. Close any open test PR (for example #17 from P0), so that each step starts from a fresh PR.
4. Only one target watches a repository at a time (6b.1 still applies).
5. **The required check is now real.** The ruleset requires `SuiteWard / Contract` and `CI / test`. SuiteWardQ has no workflow, so nothing ever reports `CI / test`: every PR stays blocked by it, whatever `SuiteWard / Contract` shows. M1.6b therefore merges nothing; read the merge box instead. Do not add a workflow to make `CI / test` report: `.github/workflows/**` is protected by default, so it would be a protected change against V1.

### 6c.2 Deploy on the host

1. Push to `phase/M1.6b`. Approve the `promote` job of the `Deploy` run of the phase head commit (section 0), then, as `suiteward` (section 1), pull, restart and run the smoke test with the version, as in 6b.3 step 3:

   ```
   podman pull ghcr.io/ignisdevne/suiteward:deploy
   systemctl --user restart suiteward.service
   SMOKE_TIER=isolated SMOKE_VERSION=sha-<commit> sh ~/smoke.sh
   ```

2. Check `/status` (`curl -s http://127.0.0.1:8081/status`). All of these must hold, or stop and report without starting the scenario:
   - `schemaVersion` is 14;
   - no `lastError` in `github`;
   - `outbox.failed` is 0;
   - no `checks` or `bootstrap` error in any entry of `github`;
   - `protection.result` is `enforced`.

### 6c.3 The owner scenario, per target

Run it on `SuiteWardQ` with the target under test as the only watcher. A check appears or updates within about 1 to 2 minutes of a push or a command: synchronization takes up to 60 s and the `github_checks` job runs every 30 s. The `/suiteward approve <ref> <code>` command is in the check's details (open "Details" on the check).

1. **No protected change.** Open a PR that changes only a file outside `tests/`. `SuiteWard / Contract` shows `success`, "No protected change". Read the merge box: it lists `SuiteWard / Contract` as passed and `CI / test` as expected and not reported. Do not merge it; close it when done.
2. **Protected change.** Open a PR that changes `tests/*.txt`. The check is `in_progress`, with the proposal revision `P<n>-R1` and the copyable `/suiteward approve P<n>-R1 <code>` in the summary.
3. **Approve R1.** Comment the command with the current code of this target's authenticator entry. The acknowledgment arrives, then the check turns `success`, "Ready for integration".
4. **Implementation push.** Push a change outside `tests/` to that PR: the new head is still `success`. Consent is kept.
5. **Protected push.** Push a change in `tests/`: `in_progress` with `P<n>-R2`.
6. **Approve R2.** Comment `/suiteward approve P<n>-R2 <new code>`: the check turns `success`.
7. **Revoke R2.** Comment `/suiteward revoke P<n>-R2 <new code>`: the check goes from `success` back to `in_progress`. This is the revoke that blocks again.
8. **Rejected command.** Comment `/suiteward approve P<n>-R2 123456` (an invalid code): the check turns `failure`, "Owner command rejected". The next valid command or push clears it.
9. **The merge box.** At each step, the merge box lists `SuiteWard / Contract` with the state above, next to `CI / test` expected and not reported. **Do not merge the protected-change PR.** Without merge detection (M1.7) the canonical would never catch up, and every later PR would stay a protected change. Close it unmerged.

Each code is used once and posted within about 30 s of changing: wait for the next code before every command.

Paste back (no secrets): the PR numbers, the state and summary line of the check at each step, and `/status` at the end (no `checks` or `bootstrap` error, `outbox.failed` 0, protection `enforced`). Never paste a code, the `otpauth://` URI, a base32 secret, a token or setup output.

### 6c.4 Co-located stack

The same scenario, one target at a time, as in 6b.5: stop the host's watching, start the stack with GitHub ([local development](../docs/local-development.md#verifying-m16b-on-the-co-located-stack)), run 6c.3 with the co-located entry's codes, close the PR, put the stack back without GitHub, restore the host and check it again. The stack keeps its M1.6a enrollment and V1, so no setup is needed unless it was purged.

### 6c.5 Troubleshooting

- **No check on a head:** read `github[].checks` in `/status`, the outbox (`outbox.failed`) and the synchronization's `lastSuccess`; the check waits behind them.
- **A check stays `in_progress` after you approved:** the command may not be processed yet (allow 1 to 2 minutes), or the approval was for another revision than the one in the check. A rejected code shows `failure`, "Owner command rejected" (read the acknowledgment).
- **`failure`, "Invalid contract source":** `.suiteward.yml` is invalid, or a symlink or submodule is in scope.
- **`in_progress`, "Source unavailable":** GitHub truncated the tree.
- **Conflicting checks, or two acknowledgments for one command:** two instances watch the repository. Stop one (6b.1).

### 6c.6 Never

No code, token, setup output or secret in tickets, logs or messages. The warning of section 6a stays in force.

## 6d. Promotion on merge (M1.7)

SuiteWard now notices merged pull requests and promotes the approved protected files to the next canonical version ([D-INTEGRATION](../docs/plan/decisions.md)); it also verifies the canonical artifacts every hour. You verify it on `IgnisDevNE/SuiteWardQ` with the owner scenario below, host first, then the co-located stack (6d.4). M1.7 adds no quadlet change, no secret and no setting. The `/status` fields and the jobs are in the [runtime contract](../docs/contracts/runtime.md#merges-and-integrity-jobs-m17).

### 6d.1 Before you start (owner)

1. Add `phase/M1.7` to the deployment branches of the `remote-poc` environment (GitHub, repository Settings, Environments). Without it the `promote` job of the `Deploy` run is refused.
2. Remove `CI / test` from SuiteWardQ ruleset 24829664, so that only `SuiteWard / Contract` is required. Keep the strict "up to date" rule. SuiteWardQ has no workflow, so `CI / test` never reports and would block every merge.
3. Confirm that merge commits, squash and rebase are all enabled in SuiteWardQ's settings.
4. Close every open test PR.
5. Only one target watches a repository at a time (6b.1).
6. **The two canonicals diverge.** Each target keeps its own canonical. The host runs first, and its merges move SuiteWardQ `main` ahead of the co-located stack's V1. The co-located stack is therefore purged and set up again after the host scenario (6d.4).

7. **Merges from before M1.7.** On its first run the merges job resolves every PR that left the open list before the deploy. One merged after the host's V1 with a protected change would get a "did not promote" comment. SuiteWardQ had no merges during M1.6b, so none is expected; if one appears, recover as in 6d.4 step 7.

### 6d.2 Deploy on the host

1. As in 6c.2: push to `phase/M1.7`, approve the `promote` job of the `Deploy` run of the phase head commit (section 0), then, as `suiteward` (section 1), pull, restart and run the smoke test with the version:

   ```
   podman pull ghcr.io/ignisdevne/suiteward:deploy
   systemctl --user restart suiteward.service
   SMOKE_TIER=isolated SMOKE_VERSION=sha-<commit> sh ~/smoke.sh
   ```

2. Check `/status` (`curl -s http://127.0.0.1:8081/status`). All of these must hold, or stop and report without starting the scenario:
   - `schemaVersion` is 15;
   - no `lastError` in `github`;
   - `outbox.failed` is 0;
   - no `checks`, `bootstrap` or `merges` error in any entry of `github`;
   - `integrity` is `passed` for SuiteWardQ. The integrity job runs at start and then every hour, so it shows within the first run;
   - `protection.result` for SuiteWardQ is `findings` with only `missing-test-check`: the owner removed `CI / test` from the ruleset in 6d.1 step 2, and the enrollment still names it as the test check. That finding is expected during M1.7; any other finding stops the scenario.

### 6d.3 The owner scenario (host)

Run it on `SuiteWardQ` with the target under test as the only watcher. **Allow about 1 to 2 minutes per step**: synchronization takes up to 60 s, the merges job runs every 30 s and the checks job every 30 s. The `/suiteward approve <ref> <code>` command is in the check's details (open "Details" on the check). Each code is used once: wait for the next code before every command.

1. **Implementation-only merge.** Open a PR that changes only a file outside `tests/`. `SuiteWard / Contract` shows `success`. Merge it, with any method. No comment and no new version appear, and `/status` stays clean.
2. **Merge commit.** Open a PR that changes `tests/`. Approve its revision (`/suiteward approve P<n>-R1 <code>`), wait for `success`, then merge with "Create a merge commit". A comment names the new version (`V2` on the host) and the merge commit, and says that SuiteWard checked the approval and the integrity of the protected files and did not run the tests.
3. **Squash, then rebase.** The same as step 2 with "Squash and merge", then with "Rebase and merge" (V3, then V4). Before each merge, update the branch if GitHub asks: the strict rule requires it. An update changes the head, so wait for `success` again; consent is kept when the update changes only files outside `tests/`.
4. **Two approved PRs.** From the current `main`, open two PRs that each change `tests/` and approve both. Merge the first: it is promoted. The second PR's check goes back to `in_progress` with a new revision bound to the new canonical. Approve that revision, update the branch if GitHub asks, and merge it: it is promoted.
5. **Closed without merging.** Close a protected PR without merging it. No comment and no version appear.
6. **Correction.** Open a PR that reverts the most recent protected merge, so that the protected files equal the previous version exactly (any other difference makes it an ordinary promotion), approve it and merge it. The comment says that it is a correction of the current version, and the new version holds the previous version's files.
7. **Final `/status`.** `merges` is absent, `integrity` is `passed`, `outbox.failed` is 0 and `protection` is `findings` with only `missing-test-check` (6d.2).

Paste back (no secrets), for each step: the PR number, the merge method, the first line of the comment, and the state of the check before the merge. Never paste a code, the `otpauth://` URI, a base32 secret, a token or setup output.

### 6d.4 Co-located stack

Only after the host scenario, because the two canonicals diverge (6d.1 step 6).

1. Stop the host's watching ("Without GitHub" in 6b.1) and check that `systemctl --user is-active suiteward.service` prints `inactive`.
2. Run `./deploy/local.ps1 -Down -Purge`, then run `setup` again with a new authenticator entry, as in [local development](../docs/local-development.md#verifying-m17-on-the-co-located-stack). Never reuse the host's secret.
3. Start the stack with GitHub. The bootstrap proposes V1 from the `main` the host left, on the lowest-numbered open PR (6b.4 steps 1 to 3). Approve it.
4. Run 6d.3 steps 2 and 5 with the co-located entry's codes. The new version is `V2`.
5. **Integrity tamper, only with the owner's go-ahead at the time.** Change one byte of a canonical artifact file inside the stack's artifact volume and restart the service, so that the integrity job runs at start. Every open PR's check turns `failure` with the integrity title, and `/status` `integrity` is `failed`. Then purge again, or restore the byte and restart: `integrity` returns to `passed`.
6. Put the stack back without GitHub (`./deploy/local.ps1`) and restore the host ("Without GitHub" in 6b.1), only after steps 4 and 5. Check the host again: `/status` shows no `lastError` and `outbox.failed` 0 after a few minutes.
7. **The host catches up (expected).** The co-located merges of step 4 moved `main` past the host's canonical, which never approved them. Within a few minutes of its return the host posts a "did not promote" comment (`unapproved-change`) on each PR the co-located stack merged with a protected change, and its open PRs show that content as a protected change. This is the divergence reported honestly, not a fault. Recover it with the documented path: open a PR from the current `main` with any small change outside `tests/`, approve its revision on the host (the check shows the protected change against the host's canonical) and merge it. The host promotes the next version, and its canonical equals `main` again. Paste back the PR number and the version.

### 6d.5 Troubleshooting

- **`merges` in `/status`.** A fixed code for the last problem of the merges job for that repository, cleared by the next run that finds nothing wrong:
  - `read-failed`: GitHub could not be read. The job retries every 30 s; check `lastSuccess` and `waitUntil` of the entry.
  - `waiting`: a merged PR has no merge commit yet, or its tree is not readable yet. It is retried until the 30-minute bound below, and no later merge is decided before it.
  - `assessment-conflict`: a different integrity assessment is already stored for that merge commit. The merge was recorded as blocked, with a comment, and is never decided again; the code shows only for the run that decided it.
  - `failed`: a store error. Read the service journal (`journalctl --user -u suiteward.service -n 100 --no-pager`; lines carry no GitHub text or secret) and report it.
- **A blocked merge comment.** SuiteWard did not promote: the merged files do not match the approved revision (for example a rebase onto a moved base that brought in another change), or the merge could not be verified. The comment says that the canonical stays at the named version and gives the reason in plain words. Recover with a new PR that restores the protected files of that version, or one that carries the integrated files for a new exact approval. There is no reset command, and a command posted after the merge is never read.
- **`fork-unsupported`.** A PR from a fork, or from a deleted fork, gets no proposal: its check stays `in_progress` with this reason, and if it is merged it is not promoted. Fork PRs are not supported in the MVP; open the PR from a branch of the repository itself.
- **`integrity` `failed`.** A canonical artifact no longer matches its recorded digest. Every open PR's check shows `failure` with the integrity title and merges are refused (`integrity-failed`) until the verdict returns to `passed`. Restore the file from a trusted copy, or purge and set up again; do not edit the database. The job runs hourly and at start, so a restart re-verifies at once. `unavailable` changes no check.
- **The 30-minute bound.** A merged PR whose merge commit is absent or unreadable is retried until 30 minutes after it was closed. After that SuiteWard records it as unreadable: the PR gets a blocked comment and no version, and later merges are decided after it. While waiting, `merges` shows `waiting`.
- **No comment after a protected merge:** wait the 1 to 2 minutes of 6d.3, then read `github[].merges` in `/status`, the outbox (`outbox.failed`) and the synchronization's `lastSuccess`.
- **Conflicting checks, or two acknowledgments for one command:** two instances watch the repository. Stop one (6b.1).

### 6d.6 Never

No code, token, setup output or secret in tickets, logs or messages. The warning of section 6a stays in force. Do not add a workflow to SuiteWardQ and do not bypass its ruleset to merge: the merges you verify are the ones the ruleset allows.

## 6e. The explicit bootstrap (M1.7b)

SuiteWard bootstraps a repository only when the owner asks ([ADR 0010](../docs/decisions/0010-repository-bootstrap.md), [D-CHECKS and D-COMMANDS](../docs/plan/decisions.md)). Until the first canonical version (V1) exists:
- every open PR gets `in_progress`, `bootstrap-awaiting`, with one notice comment that says how to start or skip, except: a fork PR (`fork-unsupported`), the PR that carries the latest proposal (`approval-missing`, then `success`, `bootstrap-ready` once approved) and a skipped PR (`success`, `bootstrap-skipped`);
- `/suiteward propose` (owner, no code) creates the proposal. The source is the default branch when it has `.suiteward.yml`; otherwise it is the PR head, when the head has the file. Without the file in either place, the reply refuses with `declaration-missing`, suggests a declaration and gives a one-click link to commit it;
- a protected push to a PR-head proposal cancels it (`bootstrap-awaiting`, one cancelled comment) and the owner proposes again; the newest proposal wins;
- `/suiteward skip <code>` lets a PR merge before V1: `success`, `bootstrap-skipped`, nothing assessed;
- V1 comes from the merge commit of a PR-head proposal, or, for a default-branch proposal, at approval, within about one bootstrap period (60 s).

Every new revision of a change PR also gets one `revision` comment. You verify it on two repositories: `IgnisDevNE/SuiteWardQ2` on the host and `IgnisDevNE/SuiteWardQ3` on the co-located stack (6e.4). The rework adds no quadlet change, no secret and no setting; the `/status` fields and the jobs are in the [runtime contract](../docs/contracts/runtime.md#explicit-bootstrap-propose-and-skip-m17b-rework-2026-10-16).

### 6e.1 Before you start (owner)

Already in place, nothing to do:
- `phase/M1.7b` is in the deployment branches of the `remote-poc` environment;
- `IgnisDevNE/SuiteWardQ2` and `IgnisDevNE/SuiteWardQ3` exist, each with its strict ruleset on `main` requiring `SuiteWard / Contract` (pinned to the test App) and `CI / test`;
- the host watches `SuiteWardQ`, `SuiteWardQ-private` and `SuiteWardQ2`, and was re-keyed by the earlier 6e.2 setup (one `--test-check` per repository, a new authenticator entry);
- the co-located stack watches only `SuiteWardQ3`. No repository is watched by both, so the two targets may run at the same time and the 6b.1 rule does not apply.

Owner housekeeping before the deploy:
1. Close the old PR #1 on SuiteWardQ2 without merging it. It carries the automatic `P1` from the first live run, which can never be promoted.
2. Open no PR on SuiteWardQ2 or SuiteWardQ3 until the deploy (6e.2) and, for SuiteWardQ3, the stack update (6e.4) are done.

### 6e.2 Deploy the rework on the host

1. As `suiteward` (section 1), optionally write down the commit of the running image, for reference only (there is no image rollback after step 2):

   ```
   podman inspect suiteward --format '{{index .Config.Labels "org.opencontainers.image.revision"}}'
   ```

2. Push to `phase/M1.7b`. Approve the `promote` job of the `Deploy` run of the phase head commit (section 0), then, as `suiteward`, pull, restart and run the smoke test with the full version, as in 6c.2:

   ```
   podman pull ghcr.io/ignisdevne/suiteward:deploy
   systemctl --user restart suiteward.service
   SMOKE_TIER=isolated SMOKE_VERSION=sha-<commit> sh ~/smoke.sh
   ```

   `<commit>` is the full 40-hex commit the `Deploy` run built. The smoke test must pass. Migration `00017` only widens a CHECK, so it needs no pre-check query. The schema becomes 17.
3. **No new `setup` and no new authenticator entry are needed.** The enrollment of the earlier 6e.2 already includes SuiteWardQ2.
4. Check `/status` (`curl -s http://127.0.0.1:8081/status`). All of these must hold, or stop and report without starting the scenario:
   - `schemaVersion` is 17;
   - no `lastError` in `github`;
   - SuiteWardQ2 shows no `bootstrap` error. The codes the bootstrap job can still report are listed in the [contract](../docs/contracts/runtime.md#explicit-bootstrap-propose-and-skip-m17b-rework-2026-10-16); `no-open-pull-request` and `awaiting-initial-contract` no longer exist;
   - `outbox.failed` is 0.
5. **There is no image rollback.** Once migration `00017` is applied, the database is newer than any older image: it refuses to start (forward-only migrations) and the quadlet restarts it in a loop. If the new image fails, do not start an older one. Capture `journalctl --user -u suiteward.service -n 50 --no-pager` (log lines carry no secret) and report.

### 6e.3 Host scenario on SuiteWardQ2 (the propose path from the PR head)

Allow about 1 to 2 minutes per step: synchronization takes up to 60 s, and the commands, checks and merges jobs run every 30 to 60 s. The `/suiteward approve <ref> <code>` command is in the check's details (open "Details" on the check). Each code is used once: wait for the next code before every command that takes one. **Do not expect fixed proposal numbers.** SuiteWardQ2 already holds `P1` from the automatic run, so the first new proposal is `P2` or later: use the number the comment names.

1. **Awaiting.** Open a PR that adds `.github/workflows/ci.yml` (the `CI / test` workflow used in SuiteWardQ) and one file in `tests/`, **without** `.suiteward.yml`. Expect the check `in_progress`, `bootstrap-awaiting`, plus one notice comment that names `/suiteward propose` and `/suiteward skip <code>`.
2. **Propose without a declaration.** Comment `/suiteward propose`. Expect a refusal with `declaration-missing`, a suggested `.suiteward.yml` and a link. Open the link and commit the file to the PR branch. This shows the suggestion and the link.
3. **Propose.** The command reads the PR head from the last synchronization, so wait about a minute after the commit, until the check shows the new head, before commenting. Then comment `/suiteward propose` again. Expect a bootstrap comment naming the PR head and the next free proposal number, `P<n>-R1`. The check becomes `in_progress` with that reference.
4. **Cancel.** Push another file in `tests/`. Expect the proposal cancelled: one cancelled comment, and the check back at `bootstrap-awaiting`.
5. **Propose again.** Wait for the cancelled comment and about a minute after your push (the check must show the new head), then comment `/suiteward propose`. A new proposal number appears.
6. **Approve.** Approve the revision the check names, with the new host entry. Expect `success`, "Bootstrap ready for integration".
7. **Merge,** by any method. Expect a comment saying V1 was promoted from the merge commit.
8. **Change PR.** Open a PR that changes a file in `tests/`. Expect the `revision` comment and `in_progress`; approve it and merge it: V2.
9. **Regression on SuiteWardQ.** A change PR in `tests/` there, approved with the new host entry and merged, gives V11. This is the first re-enrollment of the same owner while a Suite already has a canonical. A `policy-changed` block, an `approval-missing` after a valid code, or an invalid-code answer to a fresh code is a bug: stop and report.
10. **Final `/status`:** no errors and `outbox.failed` 0.

Paste back (no secrets), for each step: the PR number, the first line of the comment, and the state of the check. Never paste a code, the `otpauth://` URI, a base32 secret, a token or setup output.

### 6e.4 Co-located stack on SuiteWardQ3 (skip, then the branch path)

1. **Update the stack with the new image.** SuiteWardQ3 has had no PR since its setup, so nothing is purged and no new `setup` is needed. Follow the update procedure of [local development](../docs/local-development.md#verifying-m17b-on-the-co-located-stack): pull the image, run `./deploy/local.ps1` again with the GitHub id parameters, then the smoke test from the PowerShell session in the checkout:

   ```
   Get-Content -Raw deploy/smoke.sh | podman machine ssh "SMOKE_TIER=co-located SMOKE_VERSION=sha-<commit> sh -s"
   ```

   As on the host, there is no image rollback after migration `00017`: if the new image fails, do not start an older one; capture `podman logs --tail 50 suiteward` and report.
2. Check `/status` through the machine (`podman machine ssh "curl -s http://127.0.0.1:8081/status"`): `schemaVersion` 17 and no error, as in 6e.2 step 4.

The scenario, with the co-located entry's codes:
1. **Awaiting.** Open a PR that adds `.suiteward.yml` (`version: 1`, `include: [tests/**]`), the CI workflow and one file in `tests/`. Expect `bootstrap-awaiting`.
2. **Skip.** Comment `/suiteward skip <code>`. Expect `success`, `bootstrap-skipped`. Merge it. There is no V1: the merge is before the canonical.
3. **Propose on another PR.** Open another PR (any change) and comment `/suiteward propose`. The default branch now has the declaration, so the proposal is a default-branch baseline carried by this PR. Expect the bootstrap comment and `in_progress` with the reference.
4. **Approve.** Approve it (the command is in the check's details). V1 is promoted within about one bootstrap period (60 s) of the approval, not at merge. Expect a comment that starts "Bootstrap complete: P<n>-R1, approved by <owner>, is now canonical version V1." and, in `/status`, no `bootstrap` error.
5. **Assessed as a change.** The PR is then assessed as a change against V1. If it changed only files outside `tests/`, the check is `success`, "No protected change". If it changed a protected file, you get the `revision` comment and the check is `in_progress` until you approve that revision.

Paste back (no secrets), for each step: the PR number, the first line of the comment, and the state of the check. Never paste a code, the `otpauth://` URI, a base32 secret, a token or setup output.

Cleanup: close any PR you opened and did not merge, then put the stack back without GitHub (`./deploy/local.ps1`).

### 6e.5 Troubleshooting

- **A refused `propose`.** The acknowledgment gives the reason:
  - `canonical-present`: the Suite already has V1; there is nothing to bootstrap.
  - `fork`: a PR from a fork or a deleted fork is not supported; open the PR from a branch of the repository.
  - `not-open`: the PR was closed or merged before the command was decided; propose on an open PR.
  - `base-not-target`: the PR does not target the default branch; propose on a PR into the default branch.
  - `declaration-missing`: neither the default branch nor the PR head has `.suiteward.yml`. Commit the suggested file (the link) and propose again.
  - `invalid-source`: `.suiteward.yml` is invalid, a protected path is a link, or the tree is truncated. It is deterministic, so it is refused and not retried; fix the source and propose again.
- **A refused `skip`.**
  - `carrier`: this PR carries the latest bootstrap proposal; skip another PR.
  - `canonical-present`: V1 exists.
  - `fork`: as above.
  - A refused `skip` has already consumed its code: wait for a fresh code before the next command.
- **A command answered `no-suite`.** It came before the bootstrap job created the Suite for the repository (right after the service starts or the repository is added). Wait a minute or two, then comment the command again (with a fresh code for `skip`).
- **A cancelled proposal.** A protected push changed the coverage of a PR-head proposal. The check is `bootstrap-awaiting` and one cancelled comment names the reference. Comment `/suiteward propose` again. A push that restores the exact coverage makes the revision current again. There is one cancelled comment per revision: if the coverage is restored and then broken again, the check goes back to `bootstrap-awaiting` with no second comment.
- **A `failure` on the carrier after a mistyped `skip` or `propose`.** A bad code or a malformed command on the carrier PR turns its check `failure` (`owner-command-rejected`), whatever the verb. A corrected `propose` does not clear it; a valid approve or a new head does.
- **A merge of a superseded PR.** Only the carrier of the latest PR-head proposal is decided as V1. Any other PR merged before V1, including the carrier of a superseded proposal, is `before-canonical`: no version. The default branch now holds its files, so comment `/suiteward propose` on an open PR (the branch path) when it has `.suiteward.yml`.
- **A `propose` answered `failed`.** A transient GitHub or store failure, after 5 attempts. Comment `/suiteward propose` again.
- **A large source.** The reads run in the single commands job, inside `JOB_TIMEOUT`. About 150 protected files fill one run of the default 1 minute; a larger source times out and ends `failed`. This is a known limit.
- **Conflicting checks, or two acknowledgments for one command:** two instances watch the repository. Stop one (6b.1).

### 6e.6 Never

No code, token, setup output or secret in tickets, logs or messages. The warning of section 6a stays in force. Do not bypass the ruleset to merge: the merges you verify are the ones the ruleset allows.

## 6f. Lower latency (M1.7c)

M1.7c shortens the time from a PR event to its answer: the cycle starts at GitHub's own clock (`Date`), the sync runs every 20 s, and a change wakes the job that acts on it. The normative targets are in the [runtime contract](../docs/contracts/runtime.md#latency-m17c-normative). The intervals are measured from GitHub's own timestamps, read by the bot, so you need no stopwatch: perform the steps and paste back the PR numbers.

### 6f.1 Before you start (owner)

1. Add `phase/M1.7c` to the deployment branches of the `remote-poc` environment (GitHub, repository Settings, Environments). Without it the `promote` job of the `Deploy` run is refused.
2. Nothing else changes: no setup, no new authenticator entry, the same repositories.
3. **Precondition.** `SuiteWardQ2` and `SuiteWardQ3` already hold a canonical version from 6e (V1 or later). Without one, step 1 of 6f.3 shows `bootstrap-awaiting` and step 4 gives no `canonical-present`: stop and report.

### 6f.2 Deploy on the host

1. Push to `phase/M1.7c`. Approve the `promote` job of the `Deploy` run of the phase head commit (section 0). Then, as `suiteward` (section 1), pull, restart and run the smoke test with the full version:

   ```
   podman pull ghcr.io/ignisdevne/suiteward:deploy
   ```

   ```
   systemctl --user restart suiteward.service
   ```

   ```
   SMOKE_TIER=isolated SMOKE_VERSION=sha-<commit> sh ~/smoke.sh
   ```

   `<commit>` is the full 40-hex commit the `Deploy` run built. The smoke test must pass.
2. Migration `00018` adds one column, `cycle_started_at`. As in 6e.2, there is no image rollback after it: once the migration is applied, an older image refuses to start. If the new image fails, do not start an older one. Capture `journalctl --user -u suiteward.service -n 50 --no-pager` (log lines carry no secret) and report.
3. Check `/status` (`curl -s http://127.0.0.1:8081/status`). All of these must hold, or stop and report without starting the scenario:
   - `schemaVersion` is 18;
   - each `github` entry shows `cycleStartedAt`;
   - no entry of `github` shows `lastError`;
   - `outbox.failed` is 0.

   `cycleStartedAt` appears after the first cycle under the new image. Rows from before `00018` have none, and the smoke test's `github` step passes on the older `lastSuccess` alone. If it is still missing, read `/status` again after about 20 s. If it is still missing after a minute, stop and report: commands are held back until it appears.

### 6f.3 Latency on SuiteWardQ2 (host)

Make every file change in this section with GitHub's web editor ("Add file" or the pencil on a file, then commit to the PR branch). Its commit time is GitHub's own clock, so the orchestrator can time a push to its check. A local `git push` carries your computer's clock instead.

Run it on `SuiteWardQ2`, with the host entry for every approval. The `/suiteward approve <ref> <code>` command is in the check's details (open "Details" on the check). Each code is used once: wait for the next code before every command that takes one.

1. **Open a PR.** Open a PR that changes a file in `tests/`. Expect the `revision` comment and the check `in_progress`. Target: the check within 45 s of the push.
2. **Approve.** Approve the revision the check names, with the host entry. Expect the acknowledgment and the check turning `success`. Targets: the acknowledgment within 45 s, and the check within 60 s of the comment.
3. **Push again.** Push another change in `tests/`. Expect a new `revision` comment. Target: within 45 s of the push.
4. **Propose, refused.** Comment `/suiteward propose` with no code. Expect a refusal `canonical-present`. Target: the answer within 45 s. This measures a command without using a code.
5. **Merge.** Approve the current revision the check names, with a fresh code, and wait for `success`. Then merge the PR. Expect V2 or later.

### 6f.4 Regression on SuiteWardQ (host)

Run it on `SuiteWardQ`, with the host entry.

1. Open a PR that changes a file in `tests/`. Expect the `revision` comment and the check `in_progress`.
2. Approve the revision the check names, with the host entry. Wait for `success`.
3. Merge the PR. Expect the comment to name the new version, V12.
4. Check `/status` (`curl -s http://127.0.0.1:8081/status`): no `lastError` in `github`, and `outbox.failed` is 0.

### 6f.5 Listing-lag detector (host)

As `suiteward` (section 1), read the detector lines of the host journal. This is the form of section 6 without its `-n 50`, so that the filter sees every line:

```
journalctl --user -u suiteward.service --no-pager | grep "github comment first observed"
```

Paste back only the lines that carry `missed_by_ms`: they hold ids and times, no text. That field appears only when a cycle that started after a comment did not list it (GitHub's listing lag). The value must stay well under 10 000 ms, the 10 s margin: if any line is near it, stop and report.

### 6f.6 Co-located stack (SuiteWardQ3)

1. **Update the stack as in 6e.4, step 1.** From the checkout, in PowerShell 7, pull the image and run `./deploy/local.ps1` with the GitHub parameters only. The App key secret is reused, so no key file is needed:

   ```
   podman pull ghcr.io/ignisdevne/suiteward:deploy
   ```

   ```
   ./deploy/local.ps1 -GitHubAppId <id> -GitHubInstallationId <id> -GitHubRepositories IgnisDevNE/SuiteWardQ3
   ```

2. **Smoke with the full commit**, as in 6e.4:

   ```
   Get-Content -Raw deploy/smoke.sh | podman machine ssh "SMOKE_TIER=co-located SMOKE_VERSION=sha-<commit> sh -s"
   ```

   `<commit>` is the full 40-hex commit the `Deploy` run built. There is no image rollback after migration `00018`: if the new image fails, do not start an older one. Capture `podman logs --tail 50 suiteward` and report.
3. **Check `/status`** through the machine, with the checks of 6f.2 step 3:

   ```
   podman machine ssh "curl -s http://127.0.0.1:8081/status"
   ```

4. **Run 6f.3 steps 1 to 4 on SuiteWardQ3**, with the co-located entry's codes. Close the PR without merging it.
5. **Read the detector lines** of the co-located stack:

   ```
   podman logs suiteward | grep "github comment first observed"
   ```

   Paste back only the lines that carry `missed_by_ms`, as in 6f.5.
6. **Put the stack back without GitHub.** The host needs no stop, because it watches other repositories:

   ```
   ./deploy/local.ps1
   ```

### 6f.7 Troubleshooting

- **A command not answered within about a minute.** Read `/status`. A repository with `lastError`, or with an old or missing `cycleStartedAt`, holds every command back, as before.
- **`lastError` `decode`.** GitHub's response had no readable `Date`, so the cycle failed closed. Nothing is recorded as a success for that repository.

### 6f.8 Never

No code, token, setup output or secret in tickets, logs or messages. The warning of section 6a stays in force. Do not bypass the ruleset to merge: the merges you verify are the ones the ruleset allows.

**Paste back:** the PR numbers, the first line of each SuiteWard comment, and the detector lines of 6f.5 and 6f.6 that carry `missed_by_ms`. Never a code, a token or setup output.

## 6g. MVP acceptance (M1.8)

M1.8 is the MVP's acceptance: a fresh install, from [the install guide](../docs/install.md) alone, protects a real repository end to end on both verification targets, with the backup and re-bootstrap runbooks rehearsed first ([the M1.8 exit](../docs/plan/phases/M1.8.md); [D-RELEASE, D-INSTALL and D-COMMANDS](../docs/plan/decisions.md)). This is the owner's rehearsal of an end user's experience, not another dev-loop verification: every install step below points to a section of [install.md](../docs/install.md) or [backup.md](../docs/backup.md) and never copies its commands. If following the guide alone needs anything else, record it as a guide gap (6g.8); the acceptance fails until the guide is fixed. Only the release tag, the dev-loop backup rehearsal of the running host, the paste-backs and the TOTP measurement below come from this file.

### 6g.1 Before you start (owner)

1. The orchestrator pushes the tag `v0.1.0-rc.1`, with your authorization quoting the commit ([D-RELEASE](../docs/plan/decisions.md)).
2. Approve the `release` environment run. Expect the image `ghcr.io/ignisdevne/suiteward:v0.1.0-rc.1`, and that `latest-stable` does not move: a pre-release never moves it.
3. Create two new, empty test repositories, one per target. This runbook names them `<host-repo>` and `<local-repo>`; name them yourself. Each needs a `tests/` file, a CI workflow that gives a test check, and a `feature/acceptance` branch. `<host-repo>`'s `main` also holds a `.suiteward.yml` from the start (the example of [install.md section 8](../docs/install.md#8-first-use-bootstrap)): once the ruleset of 6g.5 is on, `main` takes changes only through pull requests. `<local-repo>`'s `main` has none.
4. Create one new GitHub App per target, following [install.md section 4](../docs/install.md#4-create-the-github-app) exactly.

### 6g.2 Backup rehearsal on the current host

Follow [backup.md section 2](../docs/backup.md#2-back-up), then [section 3](../docs/backup.md#3-restore), on the running host. Expect `/status` to match before and after: `version`, `schemaVersion`, each repository's `integrity` `passed`, `outbox.failed` 0, and the same `owner`. Keep this backup: it is the only copy of SuiteWardQ's V12 and SuiteWardQ2's state.

### 6g.3 TOTP offsets

As `suiteward` (section 1), grep the journal for `github command code rejected`, in the journal command form of [6f.5](#6f5-listing-lag-detector-host):

```
journalctl --user -u suiteward.service --no-pager | grep "github command code rejected"
```

Paste back only the `would_match` and `offset` fields; never the repository or comment id beyond what those lines already show. The orchestrator reads them for the [D-COMMANDS](../docs/plan/decisions.md) TOTP decision (keep ±1, widened only on a near miss).

### 6g.4 Purge the host (owner's explicit yes in chat first)

Only after 6g.2 has passed (6g.9). With your explicit yes in chat, follow the purge of [backup.md section 4](../docs/backup.md#4-re-bootstrap-from-the-protected-default-branch), and remove the old App key and env file as that section says.

### 6g.5 Fresh install on the host, from the guide alone

Follow [install.md sections 10, 6, 7 and 8](../docs/install.md#10-running-on-a-separate-host-operators), with `v0.1.0-rc.1` and `<host-repo>`. Record every point where the guide was unclear, wrong or not enough (6g.8).

### 6g.6 End to end on `<host-repo>`

1. **Default-branch bootstrap.** With `.suiteward.yml` already on `main` (6g.1), open a PR from `feature/acceptance`, and comment `/suiteward propose`. Expect a `branch` baseline ([install.md section 8](../docs/install.md#8-first-use-bootstrap)). Approve it, and V1 follows.
2. **A blocked PR.** A PR that changes `tests/` stays `in_progress` until you approve it ([install.md section 9](../docs/install.md#9-day-to-day)).
3. **TOTP approval, then merge, then promotion.** Run 6d.3 steps 2 and 6: a merge commit that gives a new version, then a correction.
4. **Final `/status` clean,** as in 6d.3 step 7.

### 6g.7 Co-located acceptance on `<local-repo>`

Follow [install.md sections 5, 6, 7 and 8](../docs/install.md#5-install-co-located) with `-Image ghcr.io/ignisdevne/suiteward:v0.1.0-rc.1`.

**Bootstrap from the PR head, with the `/` branch editor link.** `<local-repo>` has no `.suiteward.yml` on `main`.
- Open a PR from `feature/acceptance` and comment `/suiteward propose`. Expect the suggestion with its editor link ([install.md section 8](../docs/install.md#8-first-use-bootstrap)).
- Open the link and check that GitHub fills both the file name and the content, on the branch `feature/acceptance`.
- Commit the file. Then, as install.md section 8 says, propose again, approve, and merge. V1 follows.

Run 6g.6 steps 2 to 4 on `<local-repo>`. Then rehearse [backup.md section 5](../docs/backup.md#5-rehearse-on-the-co-located-stack). Then put the stack back without GitHub, as 6f.6 does.

### 6g.8 What to paste back

- the PR numbers;
- the first line of each SuiteWard comment;
- the `/status` before and after for 6g.2;
- the 6g.3 fields;
- the list of guide gaps.

Never a code, a token, an `otpauth://` URI or setup output.

### 6g.9 Never

Keep the warnings of 6e.6, and add: never purge before 6g.2 has passed.

## 7. Reach `/status`

From your workstation, in a second terminal:

```
aws ssm start-session --target i-0f556162ae6b59b22 --region us-east-2 --document-name AWS-StartPortForwardingSession --parameters portNumber=8081,localPortNumber=8081
curl http://127.0.0.1:8081/status
```

M1.4 adds the MCP listener, forwarded the same way (a second terminal, or a second session). The token is your own to give the client; it never goes in a file of the repository:

```
aws ssm start-session --target i-0f556162ae6b59b22 --region us-east-2 --document-name AWS-StartPortForwardingSession --parameters portNumber=8091,localPortNumber=8091
```

The real Claude Code smoke of the phase runs against `http://127.0.0.1:8091/mcp` with the token from section 4a (`claude mcp add --transport http suiteward http://127.0.0.1:8091/mcp --header "Authorization: Bearer <token>"`, then `tools/list`, `status` and `sync` in a session). An agent never receives the token: you run that client yourself, or tell the orchestrator only the outcome.

## 8. A stale run waiting for approval

A `Deploy` run that waits for your approval of `promote` holds the `deploy-publish` group, and later pushes queue behind it. Approve or reject it in the GitHub Actions run page, or cancel it there. That is a human action: nobody else cancels it, and an agent never does.
