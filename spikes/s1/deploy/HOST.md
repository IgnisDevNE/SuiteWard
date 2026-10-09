# Remote host (isolated target): owner instructions

Host `i-0f556162ae6b59b22`, us-east-2, Ubuntu 24.04 arm64, rootless Podman for the unprivileged `suiteward` user. No inbound ports, no SSH daemon: you administer it through SSM Session Manager from your own workstation. The agent has no AWS access and never sees the App key.

Never paste into chat, a file, a PR, or a log: the App private key, any installation token or JWT, a GitHub or AWS credential, an SSM session token. The key must only ever travel from your terminal into your own SSM session.

## 0. Before the host steps (GitHub, once)

1. The first run of the `S1 publish` workflow creates the GHCR package `suiteward-s1` as private. Make it public (GitHub, owner profile, Packages, `suiteward-s1`, Package settings, Change visibility), so the host needs no registry credential. The image contains no secret.
2. When the `promote` job of that run waits in the `remote-poc` environment, approve it. That moves the `deploy` tag. The host cannot start until `ghcr.io/ignisdevne/suiteward-s1:deploy` exists.

## 1. Open a session

From your workstation (AWS CLI with Session Manager plugin, your own credentials):

```
aws ssm start-session --target i-0f556162ae6b59b22 --region us-east-2
```

You land as `ssm-user`. Become the service user and point it at its systemd user manager:

```
sudo loginctl enable-linger suiteward
sudo -iu suiteward
export XDG_RUNTIME_DIR=/run/user/$(id -u)
export DBUS_SESSION_BUS_ADDRESS=unix:path=$XDG_RUNTIME_DIR/bus
systemctl --user is-system-running
```

`enable-linger` keeps the user manager and the container running with no login session and starts it at boot. `is-system-running` must print `running` (or `degraded`); if the bus socket is missing, wait a few seconds and retry. Re-run the two `export` lines in every new session.

## 2. Place the App key and the environment file

Still as `suiteward`. Type the key into the terminal yourself; do not give it to the agent or GitHub.

```
umask 077
mkdir -p ~/.config/suiteward-s1
cat > ~/.config/suiteward-s1/app-key.pem
```

Paste the full PEM (including the BEGIN and END lines) into the session, press Enter, then Ctrl-D. Then create the Podman secret and delete the file:

```
podman secret create suiteward-s1-app-key ~/.config/suiteward-s1/app-key.pem
shred -u ~/.config/suiteward-s1/app-key.pem
podman secret ls
```

The secret is stored in the `suiteward` user's Podman store (mode 0600, on the encrypted volume) and mounted into the container as `/run/secrets/app-key`. Now the non-secret configuration:

```
cat > ~/.config/suiteward-s1/env <<'EOF'
S1_APP_ID=5257122
S1_INSTALLATION_ID=169774099
S1_REPO=IgnisDevNE/SuiteWardQ
S1_OWNER_LOGIN=magalz
EOF
```

## 3. Install the quadlet and enable auto-update

Copy the contents of `spikes/s1/deploy/suiteward-s1.container` (from the repository, on your workstation) into place; for example open `cat > ~/.config/containers/systemd/suiteward-s1.container`, paste, Ctrl-D:

```
mkdir -p ~/.config/containers/systemd
cat > ~/.config/containers/systemd/suiteward-s1.container
```

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

As `suiteward` on the host:

```
ss -tlnp
```

Every listener must be on `127.0.0.1` or `::1` (the container's `127.0.0.1:8080` and `cloudflared`'s local metrics). No `0.0.0.0` or `[::]` listener may belong to the container. Also confirm from the AWS console or `aws ec2 describe-security-groups` that the instance's security group has no inbound rules. Established connections of the service:

```
ss -tnp state established
```

Only connections to GitHub (`api.github.com`, port 443) should come from the container. One more check, whether containers can reach the instance metadata service (the host uses IMDSv2 with hop limit 1):

```
podman run --rm docker.io/curlimages/curl -s -m 3 -o /dev/null -w '%{http_code}\n' http://169.254.169.254/latest/meta-data/
```

`000` means blocked; `401` means reachable but token-protected (rootless networking can hide the extra hop). Report which one you see, and never run commands that print metadata credentials.

## What to paste back to the orchestrator

- Output of `systemctl --user status suiteward-s1.service --no-pager`.
- The JSON from `/status`.
- `ss -tlnp` and the three-line result of the metadata check.
- A `journalctl` excerpt (about 30 lines around a poll and around a restart).
- Time observations: how long the first pull and start took, and how long `deploy` promotion took to reach the host.

## Differences from D-DEPLOY and the phase plan

- The phase page (S1-D) and the README target table say status is reached through Tailscale or an SSH tunnel. D-DEPLOY says administration is SSM only with no SSH daemon, so this spike reaches `/status` through SSM port forwarding and adds no Tailscale and no tunnel ingress rule.
- D-DEPLOY plans service access through Cloudflare Tunnel and Cloudflare Access. S1 adds no Cloudflare route (the Access application does not exist yet); `cloudflared` keeps answering 404 for everything.
- The phase page says the image is published to GHCR through the bot. The workflow publishes with the Actions `GITHUB_TOKEN` and `packages: write` (App installation tokens are not documented for ghcr.io); the `ignisdevne[bot]` App is not involved.
- GHCR packages start private. This spike makes the package public so the host stores no registry credential; D-DEPLOY says nothing about visibility. A private package would require a `read:packages` credential on the host.
- Promotion is the `promote` job bound to the `remote-poc` environment, not a separate workflow; no host credential is stored in GitHub, as D-DEPLOY requires.
- Hardening beyond D-DEPLOY: the quadlet also sets a read-only root filesystem, `DropCapability=ALL`, and `NoNewPrivileges`.
