# Backup, restore and re-bootstrap

This runbook backs up one SuiteWard instance, restores it from a backup, and rebuilds it from the protected default branch when no backup can be used.

There are two installation tiers, and each section that has commands gives one complete flow for each, so follow only the flow of your tier. The **isolated** installation runs the quadlets on an AlmaLinux 9 host with `systemctl --user`, and its flows are bash, run as the user that runs the SuiteWard units (`suiteward`). The **co-located** installation is the stack that `suiteward install` starts with Podman on a Windows PC, and its flows are PowerShell. They are written for Podman: Docker has no `volume export` and `volume import`, so a Docker stack is not covered here. Every command uses the names the installation creates: the containers `suiteward`, `suiteward-migrate` (the one-shot migration) and `suiteward-db`, the volumes `suiteward-data` and `suiteward-db-data`, and the database and its owner `suiteward`. The service connects as another role, `suiteward_app`.

## 1. What a backup holds

A backup has two parts:

- the database `suiteward`: the owner enrollment (owner identity and TOTP secret), the canonical contracts with their history, the proposals and approvals, and the ledgers of owner commands and merges;
- the artifact volume `suiteward-data`, mounted at `/data`, where the artifacts live under `/data/artifacts`.

A backup does not hold what the installation keeps around that data:

- the Podman secrets: the database owner password and URL (`suiteward-db-password`, `suiteward-database-owner-url`), the URL of the application role (`suiteward-database-url`), the GitHub App key (`suiteward-github-app-key`) and the MCP token (`suiteward-mcp-token`);
- the database roles: `pg_dump` does not carry roles. The owner `suiteward` is created when PostgreSQL initialises its volume, and the deployment creates the application role `suiteward_app`. The dump records the grants to `suiteward_app`, so the role must exist when the dump is loaded;
- `~/.config/suiteward/github.env`, the file of the three GitHub settings on the isolated host, and the installer's settings file, `settings.yaml` (next to it on the isolated host, in the per-user configuration directory of the co-located PC), which records the ports, the image and the GitHub values;
- the quadlet files, in `~/.config/containers/systemd/` on the isolated host.

A restore on a new machine re-creates them first, with the installer, for the backup's version ([install guide](install.md), section 10 for the isolated host, section 5 co-located). Do not let the install run setup, because the restore brings the enrollment back. On the isolated host the install does not run setup. On the co-located PC, install without GitHub first (`suiteward install --image <image of the backup's version> --github no`), so that no App is created and no owner is enrolled. The install creates the owner password and URL, the application role and its URL secret, and the units. Only then restore the data (section 3). The database passwords and the MCP token are generated again there, which is fine because the dump holds no password; your MCP client needs the new token. On the co-located PC, put GitHub back after the restore, with the App values and its key file, as section 3 says. Carry over every setting you changed in the old units, such as an `Environment=SUITEWARD_JOB_TIMEOUT=` line in `suiteward.container`, and use the App key of the same App as the old host: a new install starts from the release's units, without your edits.

> [!WARNING]
> A backup holds the TOTP secret, so anyone with it can approve as the owner. Keep every backup offline and encrypted at rest by your own means. SuiteWard does not encrypt backups.

Encrypted backup archives and a guided restore come after the MVP: see [ADR 0019](decisions/0019-encrypted-backups-and-instance-recovery-key.md), [ADR 0020](decisions/0020-guided-local-cli-recovery.md) and [ADR 0021](decisions/0021-release-backups-and-health-reconciliation.md).

## 2. Back up

Stop the service container, not the database, so that the database and the artifacts come from the same moment. Follow the flow of your tier from its first step to the last.

### Isolated host (bash, as `suiteward`)

The `suiteward` account has a `nologin` shell, so `sudo -iu suiteward` fails. As the administrator, open a shell as `suiteward` with a working user bus, as [step 10.4 of the install guide](install.md#10-running-on-a-separate-host-operators) does:

```bash
r=/run/user/$(id -u suiteward)
sudo -u suiteward -H env XDG_RUNTIME_DIR=$r \
  DBUS_SESSION_BUS_ADDRESS=unix:path=$r/bus bash
```

The command opens a new shell and waits for commands, so it prints nothing. Check it with `whoami`, which prints `suiteward`. Run every command of the isolated flows of sections 2 to 4 in this shell, except the ones that say they run on another machine (the verification of the digest, and the copy off the host). Type `exit` to leave it.

1. Stop the service.

   ```bash
   systemctl --user stop suiteward.service
   ```

   Expect no output. `suiteward-db` keeps running, and `suiteward-migrate.service` stays active (it has finished its work and does not run again when the service starts).

2. Dump the database as its owner, `suiteward`, which the command below uses over the container's local socket. The dump is plain SQL, so restoring it needs only `psql`.

   The dump holds the TOTP secret, so the directory is readable only by `suiteward`:

   ```bash
   mkdir -p ~/suiteward-backup && chmod 700 ~/suiteward-backup
   ```

   ```bash
   podman exec suiteward-db pg_dump -U suiteward -d suiteward > ~/suiteward-backup/suiteward.sql
   ```

   Expect no output and a file that starts with `--` comment lines. Do not add `-t` to `podman exec`: a terminal changes the bytes.

3. Export the artifact volume to a tarball.

   ```bash
   podman volume export suiteward-data --output ~/suiteward-backup/suiteward-artifacts.tar
   ```

   Expect no output. The export writes the archive world-readable even under `umask 077`, so restrict it:

   ```bash
   chmod 600 ~/suiteward-backup/suiteward-artifacts.tar
   ```

   Expect no output.

4. Start the service.

   ```bash
   systemctl --user start suiteward.service
   ```

   Expect no output.

5. Wait until the service is ready, then check `/status` and save it next to the files. Its `version` is the version of these files, and the restore is checked against this saved copy.

   ```bash
   curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. The service needs a moment after the start, and the command retries while nothing answers. If it gives up (curl does not retry a connection that is reset), run it again until it answers.

   ```bash
   curl -sS -o ~/suiteward-backup/status.json http://127.0.0.1:8081/status
   ```

   Expect `status.json` to hold the status object, with `version`, `schemaVersion`, `owner` and `outbox`.

6. Keep a copy of the backup folder off this machine, offline and encrypted, as the warning in section 1 says. How you copy the files depends on how you reach the host. If you reach it only through a remote shell that cannot copy files, serve the folder on the host's loopback, forward that port to your PC with your remote-access tool, download each file, then stop both.

   > [!WARNING]
   > While the server and the forward run, any local user of either machine can read the files, and `suiteward.sql` holds the TOTP secret. Do this only on machines only you use, and stop both as soon as the download ends.

   Start the server in the host shell. It stays in the foreground, and the shell waits until you stop it with Ctrl-C:

   ```bash
   python3 -m http.server <port> --bind 127.0.0.1 --directory ~/suiteward-backup
   ```

   The forwarding command depends on your remote-access tool. With AWS Systems Manager, for example, and `<port>` set to 8000, run this on the machine that has the AWS CLI, the Session Manager plugin and your credentials (quoted for a POSIX shell or PowerShell 7.3 or later). It stays open while the forward works:

   ```
   aws ssm start-session --region <region> --target <instance-id> --document-name AWS-StartPortForwardingSession --parameters '{"portNumber":["8000"],"localPortNumber":["18000"]}'
   ```

   Pick a free local port for the forward (18000 here): a port already in use locally can reset every connection. In a second terminal on the same machine, download each file, once for `suiteward.sql`, `suiteward-artifacts.tar` and `status.json` (`curl.exe` on Windows):

   ```
   curl -fsS -o <file> http://127.0.0.1:18000/<file>
   ```

   Then stop the forward (Ctrl-C in its terminal) and the server (Ctrl-C in the host shell), and compare the sizes of the downloaded files with `ls -l ~/suiteward-backup` on the host.

### Co-located PC (PowerShell)

Run every `podman` command in the PowerShell session where you selected the rootless connection ([install guide, step 5.1](install.md#5-install-co-located)).

1. Stop the service.

   ```powershell
   podman stop suiteward
   ```

   Expect `suiteward`.

2. Dump the database as its owner, `suiteward`. The dump is plain SQL, so restoring it needs only `psql`. Create the folder, then run the dump through `cmd`, which writes the bytes unchanged. The host path is quoted inside the `cmd` string, so a profile path with a space works.

   ```powershell
   New-Item -ItemType Directory -Force -Path $HOME\suiteward-backup
   ```

   ```powershell
   cmd /c "podman exec suiteward-db pg_dump -U suiteward -d suiteward > `"$HOME\suiteward-backup\suiteward.sql`""
   ```

   Expect no output from the dump and a file that starts with `--` comment lines. Do not add `-t` to `podman exec`: a terminal changes the bytes.

3. Export the artifact volume to a tarball.

   ```powershell
   podman volume export suiteward-data --output $HOME\suiteward-backup\suiteward-artifacts.tar
   ```

   Expect no output.

4. Start the service.

   ```powershell
   podman start suiteward
   ```

   Expect `suiteward`.

5. Wait until the service is ready, then check `/status` and save it next to the files. Its `version` is the version of these files, and the restore is checked against this saved copy.

   ```powershell
   curl.exe --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. The service needs a moment after the start, and the command retries while nothing answers.

   ```powershell
   curl.exe -sS -o $HOME\suiteward-backup\status.json http://127.0.0.1:8081/status
   ```

   Expect `status.json` to hold the status object, with `version`, `schemaVersion`, `owner` and `outbox`.

   The port is published inside the Podman machine and may not answer from Windows, as the status command that `suiteward install` prints shows. A `curl.exe` that cannot connect keeps retrying for minutes: stop it with Ctrl-C, then run both commands through the Podman machine as its rootless user, `podman machine ssh --username user` (the default ssh user is `root`, which is the rootful store and does not hold this stack). Redirect on the Windows side to save the file:

   ```powershell
   podman machine ssh --username user "curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz"
   ```

   ```powershell
   podman machine ssh --username user "curl -sS http://127.0.0.1:8081/status" > $HOME\suiteward-backup\status.json
   ```

6. Keep a copy of the backup folder off this machine, offline and encrypted, as the warning in section 1 says. No command.

## 3. Restore

> [!CAUTION]
> A restore replaces this instance's database and artifacts with the backup. Run it only on an instance whose current data you have decided to replace, and keep the backup files until the last check passes.

**What a restore loses.** A restore returns the instance to the backup's moment. The approvals, proposals and promotions made after the backup are gone, while the default branch on GitHub may have moved on, so the restored canonical can be behind it. There is no reset command. A canonical that is behind the default branch is fixed by a corrective pull request: open one from the current default branch with any small change outside the protected files, approve the revision its check names, and merge it, and the canonical equals the default branch again (see [ADR 0018](decisions/0018-corrective-pr-rollback-and-audit-history.md)). Back up before any planned change.

Restore with the **same SuiteWard version** that made the backup. Migrations move forward only: a newer image may migrate the restored database afterwards, but an older image cannot read a newer schema. Never leave `suiteward-migrate` on an image that is newer than the restored schema and then run the service on an older one: the two units must name the same image, and that image must not be older than the schema in the dump. That version is the `version` value in `status.json`.

The one exception is a backup made by v0.1.x. That release has no `migrate` command, and the units and the installer of this layout run it, so they cannot start the v0.1.x image. Restore such a backup with the release you upgrade to: its migration brings the restored schema forward. `/status` then shows the new `version` and `schemaVersion`, and the same `owner`.

Follow the flow of your tier. Each one starts by checking the backup before anything is replaced.

### Isolated restore

Run the commands in the shell of the `suiteward` account, which [section 2](#isolated-host-bash-as-suiteward) opens.

1. Check that the dump is complete.

   ```bash
   grep -c "PostgreSQL database dump complete" ~/suiteward-backup/suiteward.sql
   ```

   Expect `1`. Any other count means the dump was cut short, and you must not restore from it.

2. Check that the artifact archive can be read.

   ```bash
   tar -tf ~/suiteward-backup/suiteward-artifacts.tar
   ```

   Expect a list of entries under `artifacts/` and no error.

3. Pin both units to the backup's version, by the digest of its signed image index. The version is the `version` value in `status.json` (the release you upgrade to, for a backup from v0.1.x). The release notes of every release name the index digest (the owner adds it when publishing). The log of the release workflow's step "Sign the image index, verify the signature, then move latest-stable", also prints it, in the line `Verified: ghcr.io/ignisdevne/suiteward@sha256:<index> was signed by the SuiteWard release workflow.` Do not take it from `podman image inspect --format '{{.Digest}}'`: that prints the digest of the platform manifest Podman pulled, which is not the signed one. Verify it on your PC, or any machine with cosign 3.x, as [step 10.4 of the install guide](install.md#10-running-on-a-separate-host-operators) says (the host does not need cosign). This command runs on that machine, from the release source, not in the host shell:

   ```
   pwsh ./scripts/verify-release.ps1 -Reference ghcr.io/ignisdevne/suiteward@sha256:<index>
   ```

   If it fails, do not pin that digest. A signature proves that the digest came from the release workflow, not that it is the version you want: after the service starts (step 8), check that `version` in `/status` is the one in `status.json`. Then, on the host, pull the image by the digest. The index is multi-arch, so the same line works on amd64 and arm64:

   ```bash
   podman pull ghcr.io/ignisdevne/suiteward@sha256:<index>
   ```

   Set the digest, replacing `<index>`, then set the `Image=` line of BOTH `suiteward-migrate.container` and `suiteward.container` to it. They must name the same image. The chain refuses with a message unless `d` has the form of a digest, so it never writes an incomplete line:

   ```bash
   d=sha256:<index>
   ```

   ```bash
   if [[ "$d" =~ ^sha256:[0-9a-f]{64}$ ]]; then
     sed -i "s|^Image=ghcr.io/ignisdevne/suiteward[:@].*|Image=ghcr.io/ignisdevne/suiteward@$d|" ~/.config/containers/systemd/suiteward-migrate.container ~/.config/containers/systemd/suiteward.container
   else
     echo "Refused: d is not sha256: and 64 hex digits"
   fi
   ```

   Check them, then reload the units:

   ```bash
   grep -c '^Image=ghcr.io/ignisdevne/suiteward@sha256:[0-9a-f]\{64\}$' ~/.config/containers/systemd/suiteward-migrate.container ~/.config/containers/systemd/suiteward.container
   ```

   ```bash
   systemctl --user daemon-reload
   ```

   Expect `1` for each of the two files from the `grep`, and no output from the others. A `0` means a pin is missing or malformed: run the chain again. A line `Refused: ...` means `d` was not set to a digest, and the files are unchanged, so an old pin is still in place and the `grep` also prints `1`: set `d` and run the chain again. The sed command names the two files: never run it on every `*.container` file, because it would also touch the database unit.

   If you pulled by tag instead, `podman image inspect --format '{{json .RepoDigests}}' ghcr.io/ignisdevne/suiteward:<version>` must list `ghcr.io/ignisdevne/suiteward@sha256:<index>`.

4. Stop the service and the migration, and make sure the database runs. Stopping the migration unit as well makes the start in step 7 run it again.

   ```bash
   systemctl --user stop suiteward.service suiteward-migrate.service
   ```

   ```bash
   systemctl --user start suiteward-db.service
   ```

   Expect no output from either. Then check that the application role exists. The dump records grants to it, and the load in step 5 stops at the first error, so a missing role must be created first:

   ```bash
   podman exec suiteward-db psql -U suiteward -d suiteward -tAc "SELECT 1 FROM pg_roles WHERE rolname = 'suiteward_app'"
   ```

   Expect `1`. On the host that made the backup, the role is still in the cluster. On a new machine, the install of section 1 created it. If the output is empty, create the role and its URL secret with the chain below, after `podman secret rm suiteward-database-url` (the secret exists but names a role with another password). The password is generated here and travels only through standard input, never as an argument, so it appears in no process list and no shell history. The three `SET` lines keep it out of the database log, even if the statement fails. The chain stops at the first failure and prints only the ID of the new secret:

   ```bash
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

   If the role exists already and only the secret is missing, run the same chain with `ALTER ROLE suiteward_app PASSWORD '%s';` in place of `CREATE ROLE suiteward_app LOGIN PASSWORD '%s';`. Check the secret's shape without showing it. It must print `1`:

   ```bash
   podman secret inspect --showsecret --format '{{.SecretData}}' suiteward-database-url | grep -Ecx 'postgres://suiteward_app:[0-9a-f]{48}@suiteward-db:5432/suiteward[?]sslmode=disable'
   ```

5. Empty the database, then load the dump. The application keeps every table in the `public` schema, so emptying that schema empties the database. This deletes the database's current content.

   ```bash
   podman exec suiteward-db psql -U suiteward -d suiteward -v ON_ERROR_STOP=1 -c "DROP SCHEMA public CASCADE" -c "CREATE SCHEMA public"
   ```

   Expect `NOTICE` lines that name what `DROP SCHEMA public CASCADE` drops (`drop cascades to ...`). They are not errors. Then expect `DROP SCHEMA` and `CREATE SCHEMA`.

   ```bash
   podman exec -i suiteward-db psql -U suiteward -d suiteward -v ON_ERROR_STOP=1 < ~/suiteward-backup/suiteward.sql
   ```

   Expect `CREATE TABLE`, `COPY` and similar lines, and no `ERROR`. `ON_ERROR_STOP` ends the load at the first error.

6. Replace the artifact volume. Remove it, let the Quadlet unit for `suiteward-data.volume` create it empty, then import the tarball into the empty volume.

   ```bash
   podman volume rm --force suiteward-data
   ```

   ```bash
   systemctl --user restart suiteward-data-volume.service
   ```

   ```bash
   podman volume import suiteward-data ~/suiteward-backup/suiteward-artifacts.tar
   ```

   Expect the first command to print `suiteward-data`, and the other two to print nothing. Use `restart`, not `start`: the volume unit stays active after its first run, so `start` does nothing. The unit only creates the volume; it does not start the service.

7. Run the migration, then start the service. The migration unit applies the grants to `suiteward_app` again, which the restore replaced, and it applies any migration the dump lacks (a backup from v0.1.x gets all of them here). The service starts only after the migration exited with code 0.

   ```bash
   systemctl --user start suiteward-migrate.service
   ```

   ```bash
   systemctl --user start suiteward.service
   ```

   Expect no output from either. If the first one fails, read its journal (`journalctl --user -u suiteward-migrate.service -n 50 --no-pager`) and see the [install guide's troubleshooting](install.md#15-troubleshooting).

8. Wait until the service is ready, then check `/status` against `status.json`.

   ```bash
   curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. If it gives up, run it again until it answers.

   ```bash
   curl -sS http://127.0.0.1:8081/status
   ```

   Expect the same `version` as in `status.json`, the same `schemaVersion` as before, the same `owner` login, and `outbox.failed` 0. If GitHub is configured, each `github` entry shows `integrity` `passed`; it appears after the first check, which runs when the service starts, right after each promotion and then every hour, so wait a moment for it. If `integrity` reports `failed`, the artifacts and the database come from different moments: restore both from the same backup.

9. Return the units to your update path. Step 3 pinned both `Image=` lines to the backup's version, and the stack stays on that version until you change them. The install guide's [Updating](install.md#11-updating) gives the way: `suiteward install --upgrade` backs up first, then moves both `Image=` lines to the new release, migrates and restarts the service. Update when you choose. Never leave `suiteward-migrate` on a newer image than the one the service runs, and never restart `suiteward.service` alone after changing an image: the service refuses a schema that is not the one of its image. Automatic updates are not supported. After step 3 the units are already loaded; if you edited them again, reload them:

   ```bash
   systemctl --user daemon-reload
   ```

   Expect no output.

### Co-located restore

Run every `podman` command in the PowerShell session where you selected the rootless connection ([install guide, step 5.1](install.md#5-install-co-located)).

The installer creates the volumes and the roles only when it runs, and a run also starts the service. So after the two checks, the restore removes the containers and the data volumes, and starts a clean stack of the same version (the release you upgrade to, for a backup from v0.1.x: see the start of this section), instead of pinning the image and replacing the volume. The installer creates the roles `suiteward` and `suiteward_app`, migrates, and starts the service. It keeps the secrets and its settings, so a run with no GitHub flags does not create an App and does not run setup: the restore brings the enrollment back.

1. Check that the dump is complete.

   ```powershell
   Select-String -Path $HOME\suiteward-backup\suiteward.sql -Pattern 'PostgreSQL database dump complete' -Quiet
   ```

   Expect `True`. Anything else means the dump was cut short, and you must not restore from it.

2. Check that the artifact archive can be read.

   ```powershell
   tar -tf $HOME\suiteward-backup\suiteward-artifacts.tar
   ```

   Expect a list of entries under `artifacts/` and no error.

3. Remove the stack's containers and data volumes, then start a clean stack of the same version. Take the index digest of that release from its release notes, verify it with `pwsh ./scripts/verify-release.ps1 -Reference ghcr.io/ignisdevne/suiteward@sha256:<index>` (on this PC, with cosign 3.x; [install guide, section 4](install.md#4-get-and-verify-the-installer)), and pass it to `--image`. The `version` in `status.json` names the release (the release you upgrade to, for a backup from v0.1.x).

   ```powershell
   suiteward uninstall --yes
   ```

   ```powershell
   podman volume rm suiteward-data suiteward-db-data
   ```

   ```powershell
   suiteward install --image ghcr.io/ignisdevne/suiteward@sha256:<index>
   ```

   The first command removes the three containers and keeps the secrets and the settings. The second deletes the current data. The install then creates the volumes, starts the database from the kept password, creates the role `suiteward_app` again with a new password, migrates and starts the service on an empty database with the schema of this release, and the next steps replace that data. Expect `suiteward migrate finished: the schema is up to date.`, then `Started suiteward-db and suiteward`. It does not run the GitHub flow: the settings hold the App, and its key secret exists.

   On a new PC, there are no secrets or settings to keep: install without GitHub, with `suiteward install --image ghcr.io/ignisdevne/suiteward@sha256:<index> --github no`, and put GitHub back after step 8 (below).

4. Stop the service.

   ```powershell
   podman stop suiteward
   ```

   Expect `suiteward`.

5. Empty the database, then load the dump. The load runs through `cmd`, with the host path quoted inside the `cmd` string so a profile path with a space works. Emptying the `public` schema empties the database, and this deletes the database's current content.

   ```powershell
   podman exec suiteward-db psql -U suiteward -d suiteward -v ON_ERROR_STOP=1 -c "DROP SCHEMA public CASCADE" -c "CREATE SCHEMA public"
   ```

   Expect `NOTICE` lines that name what `DROP SCHEMA public CASCADE` drops (`drop cascades to ...`). They are not errors. Then expect `DROP SCHEMA` and `CREATE SCHEMA`.

   ```powershell
   cmd /c "podman exec -i suiteward-db psql -U suiteward -d suiteward -v ON_ERROR_STOP=1 < `"$HOME\suiteward-backup\suiteward.sql`""
   ```

   Expect `CREATE TABLE`, `COPY` and similar lines, and no `ERROR`. `ON_ERROR_STOP` ends the load at the first error.

6. Import the artifacts into the empty volume.

   ```powershell
   podman volume import suiteward-data $HOME\suiteward-backup\suiteward-artifacts.tar
   ```

   Expect no output.

7. Run the install of step 3 again, with the same `--image`. It replaces the stopped service, runs the migration again (it applies the grants to `suiteward_app` and any migration the dump lacks), and starts the service on the restored data. The secrets and volumes it finds are kept.

   ```powershell
   suiteward install --image ghcr.io/ignisdevne/suiteward@sha256:<index>
   ```

8. Wait until the service is ready, then check `/status` against `status.json`.

   ```powershell
   curl.exe --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. If it gives up, run it again until it answers.

   ```powershell
   curl.exe -sS http://127.0.0.1:8081/status
   ```

   Expect the same `version` as in `status.json`, the same `schemaVersion` as before, the same `owner` login, and `outbox.failed` 0. If GitHub is configured, each `github` entry shows `integrity` `passed`; it appears after the first check, which runs when the service starts, right after each promotion and then every hour, so wait a moment for it. If `integrity` reports `failed`, the artifacts and the database come from different moments: restore both from the same backup.

   If `127.0.0.1:8081` does not answer from Windows, or `curl.exe` keeps retrying, stop it with Ctrl-C and run both commands through the Podman machine, as its rootless user, as in section 2, step 5:

   ```powershell
   podman machine ssh --username user "curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz"
   ```

   ```powershell
   podman machine ssh --username user "curl -sS http://127.0.0.1:8081/status"
   ```

On a new PC, put GitHub back now: the stack runs without it. Run the installer again with the App's values and key file, and with standard input redirected from `NUL`, so that it cannot run setup (setup needs a real terminal, and the restore brought the enrollment back). `--ruleset-imported yes` skips the wait for a ruleset import: the rulesets are already on GitHub.

```powershell
cmd /c "suiteward install --image ghcr.io/ignisdevne/suiteward@sha256:<index> --github-app-id <id> --github-installation-id <id> --github-owner <login> --github-repositories <owner/name> --github-app-key-file <path to the App key file> --test-check <owner/name>=<check name> --ruleset-imported yes < NUL"
```

Expect the installer to print the protection result of each repository and the setup command it did not run. Do not run that command: running setup replaces the TOTP secret. Delete your copy of the key file afterwards. The GitHub values stay in the settings file, so a later install needs none of them.

Nothing else returns to an update path, because `suiteward install --upgrade` moves the release when you choose.

## 4. Re-bootstrap from the protected default branch

Use this when no backup can be restored. The protected files are already on each repository's default branch, so the first contract is proposed again from them. The commands and their replies are those of the install guide's [First use: bootstrap](install.md#8-first-use-bootstrap), and the [runtime contract](contracts/runtime.md#explicit-bootstrap-propose-and-skip-m17b-rework-2026-10-16) is normative.

> [!WARNING]
> You lose the version history, the ledgers, the old enrollment and its TOTP secret, and the proposal history. Open pull requests wait for the new contract: their `SuiteWard / Contract` check stays pending (`bootstrap-awaiting`) until V1 exists. The protected files themselves stay in git, on the default branch.

Follow the flow of your tier up to its readiness check, then continue with [enroll, propose and approve](#enroll-propose-and-approve-both-tiers), which is the same for both.

### Isolated re-bootstrap

Run the commands in the shell of the `suiteward` account, which [section 2](#isolated-host-bash-as-suiteward) opens.

1. Purge the instance. Stop the three units and remove the volumes. The secrets stay: the owner password secret initialises the new database on its first start. The application role lived in the removed database volume, so step 3 creates it again.

   ```bash
   systemctl --user stop suiteward.service suiteward-migrate.service suiteward-db.service
   ```

   ```bash
   podman volume rm --force suiteward-db-data suiteward-data
   ```

2. Install the same version again. Check that both `Image=` lines, in `suiteward-migrate.container` and in `suiteward.container`, name the same digest, the signed image index digest of `ghcr.io/ignisdevne/suiteward:<version>` (step 3 of the isolated restore says where to find it and how to verify it). Reload the units, and start the database alone.

   ```bash
   grep ^Image= ~/.config/containers/systemd/suiteward-migrate.container ~/.config/containers/systemd/suiteward.container
   ```

   ```bash
   systemctl --user daemon-reload
   ```

   ```bash
   systemctl --user start suiteward-db.service
   ```

3. Create the application role again. The new role gets a new password, so remove the old URL secret first:

   ```bash
   podman secret rm suiteward-database-url
   ```

   Then wait until the database is healthy, and run the role chain and the shape check of step 4 of the isolated restore above (its role-creation block):

   ```bash
   until [ "$(podman inspect --format '{{.State.Health.Status}}' suiteward-db)" = healthy ]; do sleep 2; done
   ```

4. Start the service. It runs the migration first, and the migration needs the role:

   ```bash
   systemctl --user start suiteward.service
   ```

   Then wait until the service is ready, as in section 2, step 5.

   ```bash
   curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. If it gives up, run it again until it answers.

### Co-located re-bootstrap

Run every `podman` command in the PowerShell session where you selected the rootless connection ([install guide, step 5.1](install.md#5-install-co-located)).

1. Purge the stack. This removes the containers, the volumes, the secrets, the MCP token copy and the settings file.

   ```powershell
   suiteward uninstall --purge --yes
   ```

2. Install the same version again, by the digest of its signed image index (take it from the release notes, verify it with `pwsh ./scripts/verify-release.ps1 -Reference ghcr.io/ignisdevne/suiteward@sha256:<index>` on this PC, with cosign 3.x, and pass it to `--image`), with the App you already have: the installer creates both roles, migrates, starts the service, writes the rulesets (they are already imported: answer the wait with Enter, or pass `--ruleset-imported yes`), and runs setup on a real terminal. The reinstall needs the App key file again and writes a new MCP token: update your MCP client with the token in `$env:LOCALAPPDATA\SuiteWard\mcp-token`. Give no GitHub values only if this stack does not use GitHub.

   ```powershell
   suiteward install --image ghcr.io/ignisdevne/suiteward@sha256:<index> --github-app-id <id> --github-installation-id <id> --github-owner <login> --github-repositories <owner/name>,<owner/name> --github-app-key-file <path to the App key file> --test-check '<owner/name>=<check name>' --test-check '<owner/name>=<check name>'
   ```

   Expect `suiteward migrate finished: the schema is up to date.`, then `Started suiteward-db and suiteward`, and then setup (the next section, step 1, says what it asks).

3. Wait until the service is ready.

   ```powershell
   curl.exe --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. If it gives up, run it again until it answers. If `127.0.0.1:8081` does not answer from Windows, or `curl.exe` keeps retrying, stop it with Ctrl-C and run the check through the Podman machine, as its rootless user, as in section 2, step 5:

   ```powershell
   podman machine ssh --username user "curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz"
   ```

### Enroll, propose and approve (both tiers)

These steps are the same on both tiers. Run the first one in the shell of your flow: the `suiteward` shell of the isolated host, or the PowerShell session of the co-located PC.

1. Run `setup` once for the instance, not once for each repository: it enrolls one owner for all of them, and every run replaces the TOTP secret. This starts a new enrollment: the new entry is named `SuiteWard:<login> (<tier> <instance>)`, and the database is new, so its instance id (shown as `instance` in `/status`) differs from the old one. Before you scan the new entry, delete the old entry of this instance, the one with the old id in its name. Entries of other instances stay. An entry named only `SuiteWard:<login>` predates this release: delete it once the new entry's code is accepted. It needs the GitHub settings in place. On the isolated host they are in `~/.config/suiteward/github.env`. On the co-located PC the installer ran setup at the end of its install when the stack has GitHub (step 2 of the co-located re-bootstrap): then this step is done, and you go on to the next one. If it printed the command instead, run it here.

   ```
   podman exec -it suiteward /suiteward setup --owner <your GitHub login> --test-check '<owner/name>=<check name>' --test-check '<owner/name>=<check name>'
   ```

   Give exactly one `--test-check` for each configured repository (on the isolated host, those of `SUITEWARD_GITHUB_REPOSITORIES` in `~/.config/suiteward/github.env`; on the co-located PC, the `--github-repositories` you gave the installer), in single quotes as shown, because a check name can hold spaces and slashes. `<check name>` is the exact name of the required status check that runs your tests, as it appears in that repository's ruleset, never `SuiteWard / Contract`. A missing, extra or duplicate `--test-check` is a usage error, and nothing is written.

   Setup is interactive: run it yourself in a real terminal, with any recording of the session turned off, because it prints the TOTP secret and its `otpauth://` URI there. It looks up your login and every repository first, then waits for you to press Enter before it generates the secret. After that it prints the secret and the URI once, asks for a code, and prints the protection result of each repository. The command is the same in bash and in PowerShell, where the single quotes work the same way.

2. Propose on each repository. On an open pull request whose base is the default branch, comment `/suiteward propose`. It needs no code and grants nothing by itself. The source decides the baseline:

   - When the default branch holds `.suiteward.yml`, the proposal is a `branch` baseline, pinned at the default branch's commit, which the proposal comment shows as its origin.
   - Otherwise, when the head of the pull request holds `.suiteward.yml`, the proposal falls back to a `pull` baseline from that head, decided when this pull request merges.
   - When neither holds it, the reply refuses with `declaration-missing`, suggests a file and gives a link to commit it. Commit it, wait until the check shows the new head, then propose again.

   The proposal comment names the revision to approve, with the copyable command `/suiteward approve P<n>-R<m> <code>`. Forks, closed pull requests and pull requests against another branch are refused. If the reply says the bootstrap is still pending, the Suite does not exist yet: wait a minute or two and comment again.

> [!WARNING]
> Before you approve, review the origin commit and the list of protected files in the proposal comment. A `branch` baseline adopts whatever the default branch holds now, including merges that nobody assessed after the state was lost.

3. Approve the revision the proposal comment names, with a fresh 6-digit code from your authenticator app: `/suiteward approve P<n>-R<m> <code>`. `setup` consumed the step it verified, and a code that was already used is refused. A code is spent by the first command that uses it, so wait for the next code before you approve the next repository.

   - A `branch` baseline becomes V1 by itself, with no merge. V1 can take up to about a minute to appear, and the bot then comments `Bootstrap complete: P<n>-R1, approved by <owner>, is now canonical version V1.`
   - A `pull` baseline becomes V1 only from the exact merge commit of that pull request, after it merges. A push that changes a protected file in that pull request cancels the proposal: comment `/suiteward propose` again.

4. Read `/status`. Expect `owner` to show the new enrollment, and each `github` entry to show its `protection` result and no `bootstrap` error. On the isolated host, in the `suiteward` shell:

   ```bash
   curl -sS http://127.0.0.1:8081/status
   ```

   On the co-located PC, in PowerShell:

   ```powershell
   curl.exe -sS http://127.0.0.1:8081/status
   ```

   If `127.0.0.1:8081` does not answer from Windows, or `curl.exe` keeps retrying, stop it with Ctrl-C and run it through the Podman machine, as its rootless user:

   ```powershell
   podman machine ssh --username user "curl -sS http://127.0.0.1:8081/status"
   ```

## 5. Rehearse on the co-located stack

Rehearse before you rely on a backup. The rehearsal purges the stack, so use a test repository and a stack whose data you can lose. It also confirms that the co-located commands work with your Podman version. Every step is the co-located flow, in PowerShell.

- [ ] Back up ([section 2, co-located PC](#co-located-pc-powershell)), and copy the backup folder to a second place.
- [ ] Keep `status.json`: it is the before state.
- [ ] Purge, reinstall the same version, and restore ([section 3, co-located restore](#co-located-restore)).
- [ ] Compare `/status` with `status.json`: the same `version`, `schemaVersion` and `owner`; `integrity` `passed` in each `github` entry; `outbox.failed` 0.
