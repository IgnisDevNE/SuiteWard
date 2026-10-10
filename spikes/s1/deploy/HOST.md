# Remote host (isolated target): owner instructions

Host `i-0f556162ae6b59b22`, us-east-2, Ubuntu 24.04 arm64, rootless Podman for the unprivileged `suiteward` user. No inbound ports, no SSH daemon: you administer it through SSM Session Manager from your own workstation. The agent has no AWS access and never sees the App key.

Never paste into chat, a file, a PR, or a log: the App private key, any installation token or JWT, a GitHub or AWS credential, an SSM session token. The key must only ever travel from your terminal into your own SSM session.

## 0. Before the host steps (GitHub, once)

1. The first run of the `S1 publish` workflow creates the GHCR package `suiteward-s1` as private. Make it public (GitHub, owner profile, Packages, `suiteward-s1`, Package settings, Change visibility), so the host needs no registry credential. The image contains no secret. The image carries an `org.opencontainers.image.source` label that links the package to the repository. If the first run fails with `permission_denied: write_package`, open the package's settings, "Manage Actions access", grant the `SuiteWard` repository write access, and re-run the job.
2. When the `promote` job of that run waits in the `remote-poc` environment, approve it. That moves the `deploy` tag, and only to the digest built by that same run. A run waiting for your approval holds the `s1-publish` concurrency group, so a later push queues behind it until you approve or reject. The host cannot start until `ghcr.io/ignisdevne/suiteward-s1:deploy` exists.

## 1. Open a session

From your workstation (AWS CLI with Session Manager plugin, your own credentials):

```
aws ssm start-session --target i-0f556162ae6b59b22 --region us-east-2
```

You land as `ssm-user`. Become the service user and point it at its systemd user manager:

```
sudo loginctl enable-linger suiteward
sudo -u suiteward -H env XDG_RUNTIME_DIR=/run/user/$(id -u suiteward) DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(id -u suiteward)/bus bash
systemctl --user is-system-running
```

The `suiteward` account has a `nologin` shell, so `sudo -iu suiteward` fails with "This account is currently not available"; the command above starts `bash` directly with the user manager's environment (the two variables are already set). Start every new session this way.

`enable-linger` keeps the user manager and the container running with no login session and starts it at boot. `is-system-running` must print `running` (or `degraded`); if the bus socket is missing, wait a few seconds and retry.

## 2. Place the App key and the environment file

Still as `suiteward`. Type the key into the terminal yourself; do not give it to the agent or GitHub. First confirm that SSM Session Manager session logging (Systems Manager, Session Manager, Preferences: S3 and CloudWatch Logs) is off or acceptable to you, because the session transcript is where a pasted key could end up. For every block below that reads from the terminal: run the command, wait until the cursor stops, then paste.

The key goes straight from the terminal into a Podman secret, so no plaintext file is written. Echo is switched off so the PEM is not displayed (and so not logged):

```
stty -echo; cat | podman secret create suiteward-s1-app-key -; stty echo
```

Podman refuses `-` when its stdin is a terminal ("data must be passed into stdin"), so `cat` feeds it through a pipe. Check the line on screen, press Enter, then paste the full PEM (including the BEGIN and END lines), press Enter, then Ctrl-D (twice if the prompt does not come back, which happens when the pasted text had no trailing newline). The terminal shows nothing while echo is off; that is expected, and the trailing `stty echo` turns it back on. Then:

```
podman secret ls
```

If the secret already exists (a re-run), add `--replace` to `podman secret create`. The secret is stored in the `suiteward` user's Podman store (mode 0600, on the encrypted volume) and mounted into the container as `/run/secrets/app-key`. Now the non-secret configuration:

```
mkdir -p ~/.config/suiteward-s1
cat > ~/.config/suiteward-s1/env <<'EOF'
```

Wait until the cursor stops, then paste these lines, including the closing `EOF`:

```
S1_APP_ID=5257122
S1_INSTALLATION_ID=169774099
S1_REPO=IgnisDevNE/SuiteWardQ
S1_OWNER_LOGIN=magalz
EOF
```

## 3. Install the quadlet and enable auto-update

Copy the contents of `spikes/s1/deploy/suiteward-s1.container` (from the repository, on your workstation) into place: run the `cat` command, wait until the cursor stops, paste, then press Ctrl-D on an empty line:

```
mkdir -p ~/.config/containers/systemd
cat > ~/.config/containers/systemd/suiteward-s1.container
```

Ubuntu 24.04 ships Podman 4.9.x, and the quadlet generator skips a unit it cannot parse (you would only see "unit not found"). Check the version and dry-run the generator first; it prints the generated unit or the error:

```
podman --version
/usr/lib/systemd/system-generators/podman-system-generator --user --dryrun
```

Uncertain: `Secret=...,type=mount,target=,uid=,mode=` and `AutoUpdate=registry` are old quadlet features, but I could not confirm that 4.9 accepts every key used (`NoNewPrivileges`, `ReadOnly`, `DropCapability`, `EnvironmentFile`); the dry run is the check. If a key is rejected, remove that line from the quadlet and report which one.

Then:

```
systemctl --user daemon-reload
systemctl --user start suiteward-s1.service
systemctl --user enable --now podman-auto-update.timer
systemctl --user status suiteward-s1.service --no-pager
```

The generated service is started by the quadlet's `[Install]` section at boot through linger; `enable` is not used on generated units. Optional, to measure promotion latency: shorten the timer with `systemctl --user edit podman-auto-update.timer` and set

```
[Timer]
OnCalendar=
OnCalendar=*:0/5
```

then `systemctl --user restart podman-auto-update.timer`. `podman auto-update --dry-run` shows what would change; `systemctl --user start podman-auto-update.service` runs one update now.

## 4. Logs

```
journalctl --user -u suiteward-s1.service -n 100 --no-pager
journalctl --user -u suiteward-s1.service -f
```

The program logs JSON lines on stdout (which is what the journal holds). They contain no key, token, or request headers by contract; skim before pasting anyway.

## 5. Reach `/status`

The status port is published on the host's loopback only. From your workstation, in a second terminal:

```
aws ssm start-session --target i-0f556162ae6b59b22 --region us-east-2 --document-name AWS-StartPortForwardingSession --parameters portNumber=8080,localPortNumber=8080
curl http://127.0.0.1:8080/status
```

## 6. Check that it is outbound-only

Run these as `ssm-user` (type `exit` to leave the `suiteward` shell), because `sudo` is needed to see process names:

```
sudo ss -tlnp
```

Every listener must be on `127.0.0.1` or `::1`. The published status port appears as a loopback listener owned by `rootlessport` (Podman's port forwarder), not by the container; `cloudflared` shows its local metrics port. Any `0.0.0.0` or `[::]` listener other than the system and SSM ones you recognize must be explained. Also confirm from the AWS console or `aws ec2 describe-security-groups` that the instance's security group has no inbound rules. Established connections:

```
sudo ss -tnp state established
```

This shows IP addresses, not hostnames: the service's outbound connections should be to GitHub API addresses on port 443 (compare with `dig +short api.github.com`). One more check, whether containers can reach the instance metadata service (the host uses IMDSv2 with hop limit 1). As `suiteward`:

```
podman run --rm docker.io/curlimages/curl@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777 -s -m 3 -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' -o /dev/null -w '%{http_code}\n' http://169.254.169.254/latest/api/token
```

The image is pinned by digest (`curlimages/curl`, resolved 2026-10-09). The command prints one line, the HTTP status. `000` (or anything other than `200`) means blocked. `200` means a container can obtain a metadata token, which contradicts D-DEPLOY (rootless networking can hide the extra hop from the hop limit); report it, do not fix it, and never run commands that print metadata credentials.

## What to paste back to the orchestrator

- Output of `systemctl --user status suiteward-s1.service --no-pager`.
- The JSON from `/status`.
- `sudo ss -tlnp` and the one-line status code of the metadata check.
- The output of `podman --version` and the generator dry run (it contains no secret).
- A `journalctl` excerpt (about 30 lines around a poll and around a restart).
- Time observations: how long the first pull and start took, and how long `deploy` promotion took to reach the host.

## Differences from D-DEPLOY and the phase plan

- The phase page (S1-D) and the README target table say status is reached through Tailscale or an SSH tunnel. D-DEPLOY says administration is SSM only with no SSH daemon, so this spike reaches `/status` through SSM port forwarding and adds no Tailscale and no tunnel ingress rule.
- D-DEPLOY plans service access through Cloudflare Tunnel and Cloudflare Access. S1 adds no Cloudflare route (the Access application does not exist yet); `cloudflared` keeps answering 404 for everything.
- The phase page says the image is published to GHCR through the bot. The workflow publishes with the Actions `GITHUB_TOKEN` and `packages: write` (App installation tokens are not documented for ghcr.io); the `ignisdevne[bot]` App is not involved.
- GHCR packages start private. This spike makes the package public so the host stores no registry credential; D-DEPLOY says nothing about visibility. A private package would require a `read:packages` credential on the host.
- Promotion is the `promote` job bound to the `remote-poc` environment, not a separate workflow; no host credential is stored in GitHub, as D-DEPLOY requires.
- The `remote-poc` environment cannot enable "prevent self-review": the owner is its only required reviewer and also triggers the pushes, so the approval is the owner confirming their own run. The control is the owner's deliberate click, not separation of duties.
- Hardening beyond D-DEPLOY: the quadlet also sets a read-only root filesystem, `DropCapability=ALL`, and `NoNewPrivileges`.
