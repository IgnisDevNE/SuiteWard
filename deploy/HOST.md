# Remote host (isolated target): owner runbook for M1.2

Host `i-0f556162ae6b59b22`, us-east-2, Ubuntu 24.04 arm64, Podman 4.9.3, rootless for the unprivileged `suiteward` user. No inbound ports and no SSH daemon: you administer it through SSM Session Manager from your own workstation. This runs the product image and its PostgreSQL as two rootless containers from the Quadlet units in `deploy/quadlet/`. The only secret it needs is a generated database password, and you never type it.

Never paste into chat, a file, a PR, or a log: any password or database URL, a GitHub or AWS credential, an SSM session token. An agent never holds host access; you run these steps in your own session and paste back only the outputs listed in section 6.

## 0. Before the host steps (GitHub, once)

1. The first run of the `Deploy` workflow creates the GHCR package `suiteward` as private. Make it public (GitHub, owner profile, Packages, `suiteward`, Package settings, Change visibility), so the host needs no registry credential; the image contains no secret. If the first run fails with `permission_denied: write_package`, open the package settings, "Manage Actions access", grant the `SuiteWard` repository write access, and re-run the job.
2. When the `promote` job waits in the `remote-poc` environment, approve it. That moves the `deploy` tag, and only to the digest built by that same run. A run waiting for approval holds the `deploy-publish` concurrency group, so a later push queues behind it. The `remote-poc` environment must list `phase/M1.2` (and `main`, once merges to `main` should promote) as deployment branches. The host cannot start until `ghcr.io/ignisdevne/suiteward:deploy` exists.

## 1. Open a session

```
aws ssm start-session --target i-0f556162ae6b59b22 --region us-east-2
```

You land as `ssm-user`. Become the service user with its systemd user manager (the account has a `nologin` shell, so `sudo -iu` fails):

```
sudo loginctl enable-linger suiteward
sudo -u suiteward -H env XDG_RUNTIME_DIR=/run/user/$(id -u suiteward) DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(id -u suiteward)/bus bash
systemctl --user is-system-running
```

`is-system-running` must print `running` (or `degraded`); if the bus socket is missing, wait a few seconds and retry. Start every new session this way. Every block below runs as `suiteward` unless it says otherwise.

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

One step generates the password and creates both Podman secrets from it, so you never see or type it and the two cannot disagree: `suiteward-db-password` (read by PostgreSQL) and `suiteward-database-url` (the full URL, read by the service).

```
pw=$(openssl rand -hex 24) && printf '%s' "$pw" | podman secret create suiteward-db-password - && printf 'postgres://suiteward:%s@suiteward-db:5432/suiteward?sslmode=disable' "$pw" | podman secret create suiteward-database-url - ; unset pw
podman secret ls
```

Both names must be listed. Run this once. The database is initialised with the password on its first start and keeps it in the `suiteward-db-data` volume, so replacing a secret later locks the service out; to start over, stop `suiteward.service` and `suiteward-db.service` first (`systemctl --user stop suiteward.service suiteward-db.service`), then remove the volume (`podman volume rm suiteward-db-data`) and BOTH secrets (`podman secret rm suiteward-db-password suiteward-database-url`), then repeat this step. The step is only re-runnable after removing a first secret it created before failing, because `podman secret create` refuses an existing name. `sslmode=disable` is deliberate: the database listens only on the private `suiteward` network and its image carries no certificate.

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

The units use only keys the 4.9 documentation lists; the memory limits and stop timeouts go through `PodmanArgs` because 4.9 has no key for them, and there is no `Notify=healthy`. The dry run is the check. If a key is rejected, remove that line, start anyway, and report which one. Pre-pull the images (a first pull can exceed systemd's start timeout), then start. The service requires the database, which pulls in the network and both volumes:

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

Generated units start at boot through linger; `enable` is not used on them. If the service fails once while the database is still initialising, `Restart=always` retries every five seconds; that is expected on the very first start. `suiteward-db` must show `(healthy)`. The auto-update timer is daily by default; `systemctl --user start podman-auto-update.service` runs one update now, and `podman auto-update --dry-run` shows what would change. Logs:

```
journalctl --user -u suiteward.service -n 100 --no-pager
journalctl --user -u suiteward-db.service -n 50 --no-pager
```

## 6. Smoke test and what to paste back

The service listens on loopback port 8081. Copy `deploy/smoke.sh` from the repository into place (same `cat` and paste, then Ctrl-D), as `suiteward`:

```
cat > ~/smoke.sh
sh ~/smoke.sh
```

It waits for `/readyz`, checks `/healthz` and `/status` (zero failed and discarded), runs a probe through the worker (the outbox relay is a periodic job, so a probe takes from about a second to several seconds to be delivered), enqueues a probe whose job is delayed by 20 s, restarts the service and checks that the delayed job still ran afterwards, and prints a summary block with a timestamp and elapsed milliseconds per step. That step proves a delayed job survives a restart; it does not show whether the probe's outbox message was relayed before or after the restart, so it is no proof of outbox durability. `/status` reports the image version as `sha-<commit>`; to assert it, run `SMOKE_VERSION=sha-<commit> sh ~/smoke.sh`, with the commit the `Deploy` run built (`podman inspect suiteward --format '{{index .Config.Labels "org.opencontainers.image.revision"}}'` prints the commit of the running image; a local build without the version argument reports `dev`). It exits non-zero and names the failing step otherwise. Once a later image has been promoted, run `sh ~/smoke.sh --promote` to also run `podman auto-update` once and report the image digest before and after.

Paste back: the whole summary block; the output of `systemctl --user status suiteward-db.service suiteward.service --no-pager`; `podman --version` and the generator dry run (they contain no secret); the one-line IMDS status codes (200 before the rule, 000 after, on both networks); `podman inspect suiteward --format '{{.Config.StopTimeout}}'` (must print 45, the contract needs it above the 30 s shutdown timeout); `sudo ss -tlnp` (every listener on `127.0.0.1` or `::1` except the system and SSM ones you recognise; the published port shows as `rootlessport`); about 30 `journalctl` lines of the service around the restart; and how long the first pull and start took.

## 7. Reach `/status`

From your workstation, in a second terminal:

```
aws ssm start-session --target i-0f556162ae6b59b22 --region us-east-2 --document-name AWS-StartPortForwardingSession --parameters portNumber=8081,localPortNumber=8081
curl http://127.0.0.1:8081/status
```

## 8. A stale run waiting for approval

A `Deploy` run that waits for your approval of `promote` holds the `deploy-publish` group, and later pushes queue behind it. Approve or reject it in the GitHub Actions run page, or cancel it there. That is a human action: nobody else cancels it, and an agent never does.
