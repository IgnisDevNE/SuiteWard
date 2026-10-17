# Backup, restore and re-bootstrap

This runbook backs up one SuiteWard instance, restores it from a backup, and rebuilds it from the protected default branch when no backup can be used.

There are two installation tiers. The **isolated** installation runs the quadlets on a Linux host with `systemctl --user`, and its commands come first in each step. The **co-located** installation is the stack that `deploy/local.ps1` starts with Podman on a Windows PC, and its differences are noted under each step. Run the isolated commands as the user that runs the SuiteWard units (`suiteward`). Every command uses the names the installation creates: the containers `suiteward` and `suiteward-db`, the volumes `suiteward-data` and `suiteward-db-data`, and the database and database user `suiteward`.

## 1. What a backup holds

A backup has two parts:

- the database `suiteward`: the owner enrollment (owner identity and TOTP secret), the canonical contracts with their history, the proposals and approvals, and the ledgers of owner commands and merges;
- the artifact volume `suiteward-data`, mounted at `/data`, where the artifacts live under `/data/artifacts`.

A backup does not hold what the installation keeps around that data:

- the Podman secrets: the database password and URL (`suiteward-db-password`, `suiteward-database-url`), the GitHub App key (`suiteward-github-app-key`) and the MCP token (`suiteward-mcp-token`);
- `/etc/suiteward/github.env`, the file of the three GitHub settings on the isolated host;
- the quadlet files, in `~/.config/containers/systemd/` on the isolated host.

A restore on a new machine re-creates them first: install the stack as the [install guide](install.md) says for the backup's version (its section 10 for the isolated host, or section 5 co-located), but do not run its `setup` step (section 6), because the restore brings the enrollment back. Only then restore the data (section 3). The database password and the MCP token are generated again there, which is fine because the dump holds no password; your MCP client needs the new token. Co-located, there is no settings file and no quadlet: `deploy/local.ps1` creates the secrets, stores none of the GitHub parameters, and needs the App key file again.

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

   Expect no output and a file that starts with `--` comment lines. Do not add `-t` to `podman exec`: a terminal changes the bytes. Co-located: `New-Item -ItemType Directory -Force -Path $HOME\suiteward-backup`, then run the dump through `cmd`, which writes the bytes unchanged. The host path is quoted inside the `cmd` string, so a profile path with a space works:

   ```powershell
   cmd /c "podman exec suiteward-db pg_dump -U suiteward -d suiteward > `"$HOME\suiteward-backup\suiteward.sql`""
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

5. Wait until the service is ready, then check `/status` and save it next to the files. Its `version` is the version of these files, and the restore is checked against this saved copy.

   ```bash
   curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. The service needs a moment after the start, and the command retries while nothing answers. If it gives up (curl does not retry a connection that is reset), run it again until it answers.

   ```bash
   curl -sS -o ~/suiteward-backup/status.json http://127.0.0.1:8081/status
   ```

   Expect `status.json` to hold the status object, with `version`, `schemaVersion`, `owner` and `outbox`. Co-located: the same two commands with `curl.exe`, and `-o $HOME\suiteward-backup\status.json` for the file. The port is published inside the Podman machine and may not answer from Windows, as `deploy/local.ps1` notes when it starts the stack. A `curl.exe` that cannot connect keeps retrying for minutes: stop it with Ctrl-C, then run both commands through the machine, redirecting on the Windows side to save the file:

   ```powershell
   podman machine ssh "curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz"
   ```

   ```powershell
   podman machine ssh "curl -sS http://127.0.0.1:8081/status" > $HOME\suiteward-backup\status.json
   ```

6. Keep a copy of the backup folder off this machine, offline and encrypted, as the warning in section 1 says. No command.

   Operator note, for a host you reach only through a remote shell that cannot copy files: serve the folder on the host's loopback, forward that port to your PC with your remote-access tool, download each file, then stop both.

   ```bash
   python3 -m http.server <port> --bind 127.0.0.1 --directory ~/suiteward-backup
   ```

   Pick a free local port for the forward: a port already in use locally can reset every connection. Download `suiteward.sql`, `suiteward-artifacts.tar` and `status.json` from `http://127.0.0.1:<local port>/`, then stop the forward and the server (Ctrl-C). The forwarding command depends on your remote-access tool.

## 3. Restore

> [!CAUTION]
> A restore replaces this instance's database and artifacts with the backup. Run it only on an instance whose current data you have decided to replace, and keep the backup files until the last check passes.

**What a restore loses.** A restore returns the instance to the backup's moment. The approvals, proposals and promotions made after the backup are gone, while the default branch on GitHub may have moved on, so the restored canonical can be behind it. There is no reset command. A canonical that is behind the default branch is fixed by a corrective pull request: open one from the current default branch with any small change outside the protected files, approve the revision its check names, and merge it, and the canonical equals the default branch again (see [ADR 0018](decisions/0018-corrective-pr-rollback-and-audit-history.md)). Back up before any planned change.

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

3. Pin the image to the backup's version. In `~/.config/containers/systemd/suiteward.container`, set the `Image=` line to `Image=ghcr.io/ignisdevne/suiteward:<version>`, check it, then reload the units.

   ```bash
   sed -i 's|^Image=.*|Image=ghcr.io/ignisdevne/suiteward:<version>|' ~/.config/containers/systemd/suiteward.container
   ```

   ```bash
   grep ^Image= ~/.config/containers/systemd/suiteward.container
   ```

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

   Expect `NOTICE` lines that name what `DROP SCHEMA public CASCADE` drops (`drop cascades to ...`). They are not errors. Then expect `DROP SCHEMA` and `CREATE SCHEMA`.

   ```bash
   podman exec -i suiteward-db psql -U suiteward -d suiteward -v ON_ERROR_STOP=1 < ~/suiteward-backup/suiteward.sql
   ```

   Expect `CREATE TABLE`, `COPY` and similar lines, and no `ERROR`. `ON_ERROR_STOP` ends the load at the first error. Co-located: the same two commands in PowerShell, with the load run through `cmd`. The host path is quoted inside the `cmd` string, so a profile path with a space works:

   ```powershell
   cmd /c "podman exec -i suiteward-db psql -U suiteward -d suiteward -v ON_ERROR_STOP=1 < `"$HOME\suiteward-backup\suiteward.sql`""
   ```

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

   Expect no output.

8. Wait until the service is ready, then check `/status` against `status.json`.

   ```bash
   curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. If it gives up, run it again until it answers.

   ```bash
   curl -sS http://127.0.0.1:8081/status
   ```

   Expect the same `version` as in `status.json`, the same `schemaVersion` as before, the same `owner` login, and `outbox.failed` 0. If GitHub is configured, each `github` entry shows `integrity` `passed`; it appears after the first check, which runs when the service starts and then every hour, so wait for it or restart the service. If `integrity` reports `failed`, the artifacts and the database come from different moments: restore both from the same backup. Co-located: `curl.exe` in place of `curl`, or the machine commands of section 2, step 5 when `127.0.0.1:8081` does not answer from Windows.

9. Return `Image=` to your update path. Step 3 pinned it to the backup's version, and the service stays on that version until you change the line. The install guide's [Updating](install.md#11-updating) gives both paths: if you update by hand, update when you choose, with a fresh backup first; if you chose automatic updates, put back the `Image=` line that it gives. After any change to the line, reload the units and restart the service:

   ```bash
   systemctl --user daemon-reload
   ```

   ```bash
   systemctl --user restart suiteward.service
   ```

   Expect no output. Co-located: there is nothing to return, because each update names its own `-Image`.

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

3. Empty the database, then load the dump. The load runs through `cmd`, with the host path quoted inside the `cmd` string so a profile path with a space works.

   ```powershell
   podman exec suiteward-db psql -U suiteward -d suiteward -v ON_ERROR_STOP=1 -c "DROP SCHEMA public CASCADE" -c "CREATE SCHEMA public"
   ```

   Expect the `NOTICE` lines, `DROP SCHEMA` and `CREATE SCHEMA` of isolated step 5.

   ```powershell
   cmd /c "podman exec -i suiteward-db psql -U suiteward -d suiteward -v ON_ERROR_STOP=1 < `"$HOME\suiteward-backup\suiteward.sql`""
   ```

   Expect `CREATE TABLE`, `COPY` and similar lines, and no `ERROR`.

4. Import the artifacts into the empty volume.

   ```powershell
   podman volume import suiteward-data $HOME\suiteward-backup\suiteward-artifacts.tar
   ```

   Expect no output.

5. Start the service.

   ```powershell
   podman start suiteward
   ```

6. Wait until the service is ready, then check `/status` as in isolated step 8, with `curl.exe`. If `127.0.0.1:8081` does not answer from Windows, run both commands through the machine, as in section 2, step 5:

   ```powershell
   podman machine ssh "curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz"
   ```

   ```powershell
   podman machine ssh "curl -sS http://127.0.0.1:8081/status"
   ```

## 4. Re-bootstrap from the protected default branch

Use this when no backup can be restored. The protected files are already on each repository's default branch, so the first contract is proposed again from them. The commands and their replies are those of the install guide's [First use: bootstrap](install.md#8-first-use-bootstrap), and the [runtime contract](contracts/runtime.md#explicit-bootstrap-propose-and-skip-m17b-rework-2026-10-16) is normative.

> [!WARNING]
> You lose the version history, the ledgers, the old enrollment and its TOTP secret, and the proposal history. Open pull requests wait for the new contract: their `SuiteWard / Contract` check stays pending (`bootstrap-awaiting`) until V1 exists. The protected files themselves stay in git, on the default branch.

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

   Then wait until the service is ready, as in section 2, step 5.

   ```bash
   curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:8081/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. If it gives up, run it again until it answers. The quadlet's first start may restart the service once while the database initialises; that is expected. Co-located: run the second command of the co-located restore (section 3, step 1) with the same version and the GitHub flags, then wait for readiness with `curl.exe`, or through the machine when `127.0.0.1:8081` does not answer from Windows.

3. Run `setup` once for the instance, not once for each repository: it enrolls one owner for all of them, and every run replaces the TOTP secret. This starts a new enrollment: remove the old entry from your authenticator app before you scan the new one. It needs the GitHub settings in place: the isolated host reads them from `/etc/suiteward/github.env`, and the co-located stack has them only when it was started with its GitHub parameters (step 2).

   ```bash
   podman exec -it suiteward /suiteward setup --owner <your GitHub login> --test-check '<owner/name>=<check name>' --test-check '<owner/name>=<check name>'
   ```

   Give exactly one `--test-check` for each configured repository (those of `SUITEWARD_GITHUB_REPOSITORIES` in `/etc/suiteward/github.env`, or the `-GitHubRepositories` you gave `deploy/local.ps1`), in single quotes as shown, because a check name can hold spaces and slashes. `<check name>` is the exact name of the required status check that runs your tests, as it appears in that repository's ruleset, never `SuiteWard / Contract`. A missing, extra or duplicate `--test-check` is a usage error, and nothing is written.

   Setup is interactive: run it yourself in a real terminal, with any recording of the session turned off, because it prints the TOTP secret and its `otpauth://` URI there. It looks up your login and every repository first, then waits for you to press Enter before it generates the secret. After that it prints the secret and the URI once, asks for a code, and prints the protection result of each repository. Co-located: the same command, in PowerShell, where the single quotes work the same way.

4. Propose on each repository. On an open pull request whose base is the default branch, comment `/suiteward propose`. It needs no code and grants nothing by itself. The source decides the baseline:

   - When the default branch holds `.suiteward.yml`, the proposal is a `branch` baseline, pinned at the default branch's commit, which the proposal comment shows as its origin.
   - Otherwise, when the head of the pull request holds `.suiteward.yml`, the proposal falls back to a `pull` baseline from that head, decided when this pull request merges.
   - When neither holds it, the reply refuses with `declaration-missing`, suggests a file and gives a link to commit it. Commit it, wait until the check shows the new head, then propose again.

   The proposal comment names the revision to approve, with the copyable command `/suiteward approve P<n>-R<m> <code>`. Forks, closed pull requests and pull requests against another branch are refused. If the reply says the bootstrap is still pending, the Suite does not exist yet: wait a minute or two and comment again.

> [!WARNING]
> Before you approve, review the origin commit and the list of protected files in the proposal comment. A `branch` baseline adopts whatever the default branch holds now, including merges that nobody assessed after the state was lost.

5. Approve the revision the proposal comment names, with a fresh 6-digit code from your authenticator app: `/suiteward approve P<n>-R<m> <code>`. `setup` consumed the step it verified, and a code that was already used is refused. A code is spent by the first command that uses it, so wait for the next code before you approve the next repository.

   - A `branch` baseline becomes V1 by itself, with no merge. V1 can take up to about a minute to appear, and the bot then comments `Bootstrap complete: P<n>-R1, approved by <owner>, is now canonical version V1.`
   - A `pull` baseline becomes V1 only from the exact merge commit of that pull request, after it merges. A push that changes a protected file in that pull request cancels the proposal: comment `/suiteward propose` again.

6. Read `/status`. Expect `owner` to show the new enrollment, and each `github` entry to show its `protection` result and no `bootstrap` error. Co-located: steps 3 to 6 are the same, and `/status` may need the machine commands of section 2, step 5.

## 5. Rehearse on the co-located stack

Rehearse before you rely on a backup. The rehearsal purges the stack, so use a test repository and a stack whose data you can lose. It also confirms that the co-located commands work with your Podman version.

- [ ] Back up (section 2), and copy the backup folder to a second place.
- [ ] Keep `status.json`: it is the before state.
- [ ] Purge, reinstall the same version, and restore (section 3, co-located restore).
- [ ] Compare `/status` with `status.json`: the same `version`, `schemaVersion` and `owner`; `integrity` `passed` in each `github` entry; `outbox.failed` 0.
