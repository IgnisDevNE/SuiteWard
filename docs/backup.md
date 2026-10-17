# Backup, restore and re-bootstrap

This runbook backs up one SuiteWard instance, restores it from a backup, and rebuilds it from the protected default branch when no backup can be used.

There are two installation tiers. The **isolated** installation runs the quadlets on a Linux host with `systemctl --user`, and its commands come first in each step. The **co-located** installation is the stack that `deploy/local.ps1` starts with Podman on a Windows PC, and its differences are noted under each step. Run the isolated commands as the user that runs the SuiteWard units (`suiteward`). Every command uses the names the installation creates: the containers `suiteward` and `suiteward-db`, the volumes `suiteward-data` and `suiteward-db-data`, and the database and database user `suiteward`.

## 1. What a backup holds

A backup has two parts:

- the database `suiteward`: the owner enrollment (owner identity and TOTP secret), the canonical contracts with their history, the proposals and approvals, and the ledgers of owner commands and merges;
- the artifact volume `suiteward-data`, mounted at `/data`, where the artifacts live under `/data/artifacts`.

> [!WARNING]
> A backup holds the TOTP secret, so anyone with it can approve as the owner. Keep every backup offline and encrypted at rest by your own means. SuiteWard does not encrypt backups.

Encrypted backup archives and a guided restore come after the MVP: see [ADR 0019](decisions/0019-encrypted-backups-and-instance-recovery-key.md), [ADR 0020](decisions/0020-guided-local-cli-recovery.md) and [ADR 0021](decisions/0021-release-backups-and-health-reconciliation.md).

## 2. Back up

Stop the service container, not the database, so that the database and the artifacts come from the same moment.

1. Stop the service.

   ```bash
   systemctl --user stop suiteward.service
   ```

   Expect no output, and `suiteward-db` keeps running. Co-located: `podman stop suiteward`.

2. Dump the database. The dump is plain SQL, so restoring it needs only `psql`.

   ```bash
   mkdir -p ~/suiteward-backup
   ```

   ```bash
   podman exec suiteward-db pg_dump -U suiteward -d suiteward > ~/suiteward-backup/suiteward.sql
   ```

   Expect no output and a file that starts with `--` comment lines. Do not add `-t` to `podman exec`: a terminal changes the bytes. Co-located: `New-Item -ItemType Directory -Force -Path $HOME\suiteward-backup`, then run the dump through `cmd`, which writes the bytes unchanged:

   ```powershell
   cmd /c "podman exec suiteward-db pg_dump -U suiteward -d suiteward > $HOME\suiteward-backup\suiteward.sql"
   ```

3. Export the artifact volume to a tarball.

   ```bash
   podman volume export suiteward-data --output ~/suiteward-backup/suiteward-artifacts.tar
   ```

   Expect no output. Co-located: the same command, with `--output $HOME\suiteward-backup\suiteward-artifacts.tar`.

4. Start the service.

   ```bash
   systemctl --user start suiteward.service
   ```

   Expect no output. Co-located: `podman start suiteward`.

5. Check `/status` and save it next to the files. Its `version` is the version of these files, and the restore is checked against this saved copy.

   ```bash
   curl -sS -o ~/suiteward-backup/status.json http://127.0.0.1:8081/status
   ```

   Expect `status.json` to hold the status object, with `version`, `schemaVersion`, `owner` and `outbox`. Co-located: `curl.exe -sS -o $HOME\suiteward-backup\status.json http://127.0.0.1:8081/status`. If nothing answers on `127.0.0.1:8081` from Windows, the port is published inside the Podman machine, as `deploy/local.ps1` notes when it starts the stack.

6. Keep a copy of the backup folder off this machine, offline and encrypted, as the warning in section 1 says. No command.

## 3. Restore

> [!CAUTION]
> A restore replaces this instance's database and artifacts with the backup. Run it only on an instance whose current data you have decided to replace, and keep the backup files until the last check passes.

Restore with the **same SuiteWard version** that made the backup. Migrations move forward only: a newer image may migrate the restored database afterwards, but an older image cannot read a newer schema. That version is the `version` value in `status.json`.

Check the backup before anything is replaced:

1. Check that the dump is complete.

   ```bash
   grep -c "PostgreSQL database dump complete" ~/suiteward-backup/suiteward.sql
   ```

   Expect `1`. Any other count means the dump was cut short, and you must not restore from it. Co-located: `Select-String -Path $HOME\suiteward-backup\suiteward.sql -Pattern 'PostgreSQL database dump complete' -Quiet`, which prints `True`.

2. Check that the artifact archive can be read.

   ```bash
   tar -tf ~/suiteward-backup/suiteward-artifacts.tar
   ```

   Expect a list of entries under `artifacts/` and no error. Co-located: `tar -tf $HOME\suiteward-backup\suiteward-artifacts.tar`.

**Isolated restore**

3. Pin the image to the backup's version. In `~/.config/containers/systemd/suiteward.container`, set `Image=ghcr.io/ignisdevne/suiteward:<version>`, then reload the units.

   ```bash
   systemctl --user daemon-reload
   ```

   Expect no output.

4. Stop the service, and make sure the database runs.

   ```bash
   systemctl --user stop suiteward.service
   ```

   ```bash
   systemctl --user start suiteward-db.service
   ```

   Expect no output from either.

5. Empty the database, then load the dump. The application keeps every table in the `public` schema, so emptying that schema empties the database. This deletes the database's current content.

   ```bash
   podman exec suiteward-db psql -U suiteward -d suiteward -v ON_ERROR_STOP=1 -c "DROP SCHEMA public CASCADE" -c "CREATE SCHEMA public"
   ```

   Expect `DROP SCHEMA` and `CREATE SCHEMA`.

   ```bash
   podman exec -i suiteward-db psql -U suiteward -d suiteward -v ON_ERROR_STOP=1 < ~/suiteward-backup/suiteward.sql
   ```

   Expect `CREATE TABLE`, `COPY` and similar lines, and no `ERROR`. `ON_ERROR_STOP` ends the load at the first error. Co-located: the same two commands in PowerShell, with the load run through `cmd /c "podman exec -i suiteward-db psql -U suiteward -d suiteward -v ON_ERROR_STOP=1 < $HOME\suiteward-backup\suiteward.sql"`.

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

   Expect the first command to print `suiteward-data`, and the other two to print nothing. Use `restart`, not `start`: the volume unit stays active after its first run, so `start` does nothing. The unit only creates the volume; it does not start the service. Co-located: there is no volume unit, so use the co-located restore below.

7. Start the service.

   ```bash
   systemctl --user start suiteward.service
   ```

   Expect no output. Give the service a moment before you read `/status`.

8. Check `/status` against `status.json`.

   ```bash
   curl -sS http://127.0.0.1:8081/status
   ```

   Expect the same `version` as in `status.json`, the same `schemaVersion` as before, the same `owner` login, and `outbox.failed` 0. If GitHub is configured, each `github` entry shows `integrity` `passed`; it appears after the first check, which runs when the service starts. If `integrity` reports `failed`, the artifacts and the database come from different moments: restore both from the same backup. Co-located: `curl.exe` in place of `curl`.

**Co-located restore**

The co-located stack creates its volumes only when it starts, and a start also starts the service. So the restore begins with an empty installation of the same version, instead of pinning the image and replacing the volume:

1. Purge the stack, then reinstall the same version from the repository root. This replaces isolated steps 3 and 4 and the volume part of step 6.

   ```powershell
   ./deploy/local.ps1 -Down -Purge
   ```

   ```powershell
   ./deploy/local.ps1 -Image ghcr.io/ignisdevne/suiteward:<version> -GitHubAppKeyFile <path to the App key file> -GitHubAppId <id> -GitHubInstallationId <id> -GitHubRepositories <owner/name>
   ```

   Give the GitHub flags only if this stack uses GitHub. The purge removes the volumes, the secrets and the MCP token. The reinstall needs the App key file again and writes a new MCP token: update your MCP client with the token in `$env:LOCALAPPDATA\SuiteWard\mcp-token`. Expect `Started suiteward-db and suiteward`. The service starts on an empty database and creates its schema, and the next two steps replace that schema.

2. Stop the service.

   ```powershell
   podman stop suiteward
   ```

3. Empty the database, then load the dump.

   ```powershell
   podman exec suiteward-db psql -U suiteward -d suiteward -v ON_ERROR_STOP=1 -c "DROP SCHEMA public CASCADE" -c "CREATE SCHEMA public"
   ```

   ```powershell
   cmd /c "podman exec -i suiteward-db psql -U suiteward -d suiteward -v ON_ERROR_STOP=1 < $HOME\suiteward-backup\suiteward.sql"
   ```

4. Import the artifacts into the empty volume.

   ```powershell
   podman volume import suiteward-data $HOME\suiteward-backup\suiteward-artifacts.tar
   ```

   Expect no output.

5. Start the service.

   ```powershell
   podman start suiteward
   ```

6. Check `/status` as in isolated step 8, with `curl.exe`.

## 4. Re-bootstrap from the protected default branch

Use this when no backup can be restored. The protected files are already on each repository's default branch, so the first contract is built from them again.

> [!WARNING]
> You lose the version history, the approvals and the ledgers. The protected files themselves stay in git, on the default branch.

1. Purge the instance. Stop both units and remove the volumes. The secrets stay: the database password secret initialises the new database on its first start.

   ```bash
   systemctl --user stop suiteward.service suiteward-db.service
   ```

   ```bash
   podman volume rm --force suiteward-db-data suiteward-data
   ```

   Co-located: `./deploy/local.ps1 -Down -Purge`, which also removes the secrets and the MCP token.

2. Install the same version again. Check that `Image=` in `suiteward.container` names `ghcr.io/ignisdevne/suiteward:<version>`, reload the units and start the service.

   ```bash
   systemctl --user daemon-reload
   ```

   ```bash
   systemctl --user start suiteward.service
   ```

   Expect `/status` to answer once the new database is ready. The quadlet's first start may restart the service once while the database initialises; that is expected. Co-located: run the second command of the co-located restore (section 3, step 1) with the same version and the GitHub flags.

3. Run `setup` again, once for each repository you configured. This starts a new enrollment: remove the old entry from your authenticator app before you scan the new one.

   ```bash
   podman exec -it suiteward /suiteward setup --owner <your GitHub login> --test-check <owner/name>=<check name>
   ```

   Repeat `--test-check` for each repository. Setup prints the TOTP secret and its `otpauth://` URI once, asks for a code, then prints the protection result of each repository. Co-located: the same command.

4. Propose on each repository. On an open pull request whose base is the default branch, comment `/suiteward propose`. The bot replies with the revision to approve, in the form `/suiteward approve P<n>-R<m> <code>`; approve it with the 6-digit code from your authenticator app.

   Forks, closed pull requests and pull requests against another branch are refused. If the reply says the bootstrap is still pending, the Suite does not exist yet: wait about a minute and comment again. Once approved, the repository is protected again at V1, its first version.

5. Read `/status`. Expect `owner` to show the new enrollment, and each `github` entry to show its `protection` result. Co-located: steps 3 to 5 are the same.

## 5. Rehearse on the co-located stack

Rehearse before you rely on a backup. The rehearsal purges the stack, so use a test repository and a stack whose data you can lose. It also confirms that the co-located commands work with your Podman version.

- [ ] Back up (section 2), and copy the backup folder to a second place.
- [ ] Keep `status.json`: it is the before state.
- [ ] Purge, reinstall the same version, and restore (section 3, co-located restore).
- [ ] Compare `/status` with `status.json`: the same `version`, `schemaVersion` and `owner`; `integrity` `passed` in each `github` entry; `outbox.failed` 0.
