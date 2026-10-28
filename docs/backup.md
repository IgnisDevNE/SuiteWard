# Backup, restore and re-bootstrap

This runbook backs up one SuiteWard instance, restores it from a backup, and rebuilds it from the protected default branch when no backup can be used.

The installer does the first two. `suiteward backup init` makes the recovery key, `suiteward backup` writes an encrypted backup, and `suiteward restore` restores one. They work on both tiers, with Podman and with Docker. They exist from the release that ships phase M1.13 (the first release after `v0.3.0-alpha.2`). An earlier release has none of them: its backups, and the cases the commands do not cover, are in [Appendix A](#appendix-a-backup-and-restore-by-hand).

There are two installation tiers. The **isolated** installation runs the quadlets on an AlmaLinux 9 host with `systemctl --user`. Run its commands as the user that runs the SuiteWard units (`suiteward`), in the shell of [step 10.3 of the install guide](install.md#10-running-on-a-separate-host-operators), and give `--tier isolated` when there is no settings file. The **co-located** installation is the stack that `suiteward install` starts with Podman or Docker on the developer's own PC. Every command uses the names the installation creates: the containers `suiteward`, `suiteward-migrate` (the one-shot migration) and `suiteward-db`, the volumes `suiteward-data` and `suiteward-db-data`, and the database and its owner `suiteward`. The service connects as another role, `suiteward_app`.

## 1. What a backup holds

A backup is one file, `suiteward-backup-<date>-<time>.tar.age` (the time is UTC). It is an [age](https://github.com/FiloSottile/age) file, encrypted to the recovery key of the instance. Its content is a tar of these members:

| Member | What it holds |
| --- | --- |
| `status.json` | `GET /status` of the service when the backup was taken: its release, schema version, instance id, owner login and repositories. A backup taken while the service did not answer (only the safety backup of a restore) holds `{"unavailable": true, "reason": ...}` instead. |
| `suiteward.sql` | The database `suiteward` as plain SQL from `pg_dump`: the owner enrollment (owner identity and TOTP secret), the canonical contracts with their history, the proposals and approvals, and the ledgers of owner commands and merges. |
| `suiteward-artifacts.tar` | The artifact volume `suiteward-data`, mounted at `/data`, where the artifacts live under `/data/artifacts`. |
| `settings.yaml` | The installer's settings file, as a record, when the instance has one. |
| `units/` | On the isolated tier, the unit files as installed, as a record. |
| `manifest.json` | Written last: the release, the image digest, the instance id, the tier, the time, and the size and SHA-256 of every other member. |

A restore checks every member against the manifest before it changes anything. It refuses a member that is missing, extra, reordered or altered, a name that is not on its short list of members, a hash in the manifest that is not 64 lowercase hexadecimal characters, and a file that is cut short. That catches damage. It does not say who wrote the backup ([section 3](#3-restore)).

A restore writes back the database and the artifact volume, and nothing else. The settings and the units in the archive are a record: the restore never writes them. A backup does not hold what the installation keeps around that data:

- the Podman secrets: the database owner password and URL (`suiteward-db-password`, `suiteward-database-owner-url`), the URL of the application role (`suiteward-database-url`), the GitHub App key (`suiteward-github-app-key`), the MCP token (`suiteward-mcp-token`) and the executor's token (`suiteward-executor-token`);
- the executor, which is not part of the instance's data: its token copy (`executor-token`, beside the MCP token copy on the co-located tier, and `/etc/suiteward/executor-token` with a copy in `~/.config/suiteward/` on the isolated host), its program and pin, its task, launch agent or unit, and the Go image of its sandboxes (install guide, sections 5.4 and 10.2). It keeps working through a backup and through a restore: it holds no data of the instance, and the results that it produced are in the database, which the restore replaces;
- the database roles: `pg_dump` does not carry roles. The owner `suiteward` is created when PostgreSQL initialises its volume, and the installer creates the application role `suiteward_app`. The dump records the grants to `suiteward_app`, so the role must exist when the dump is loaded;
- `~/.config/suiteward/github.env`, the file of the three GitHub settings on the isolated host, and the quadlet files, in `~/.config/containers/systemd/` on the isolated host.

A restore on a new machine re-creates them first, with the installer, for the backup's release ([install guide](install.md), section 10 for the isolated host, section 5 co-located). The install creates the owner password and URL, the application role and its URL secret, and the units. Only then run the restore ([section 3](#3-restore)). The restore brings the enrollment back, so the install must not replace it: the installer runs setup only when `/status` shows no owner, and on a new machine there is none yet.
- **Co-located PC:** install without GitHub first (`suiteward install --image <image of the backup's release> --github no`). That creates no App and enrolls no owner. After the restore, run the installer again with the App's values: the restored owner is enrolled, so it skips setup and the TOTP secret stays.
- **Isolated host:** the administrator runs the host step first, `sudo suiteward install host --sha256sums <the verified SHA256SUMS of the backup's release>`, which makes the executor, its token and the copy of it that the service account turns into the service's secret (step 10.2 of the install guide). Then give the install the App's values and key file as step 10.4 says. Run it with standard input from `/dev/null` and every answer as a flag, so that it prints the setup command instead of running it, and do not run that command: setup would enroll an owner in the empty database, and the restore replaces it anyway.

The database passwords, the MCP token and the executor's token are generated again there, which is fine because the dump holds no password; your MCP client needs the new MCP token, and the installer gives the service and the executor the new executor token itself. Give the install the ports and the job timeout the old instance had (`--http-port`, `--mcp-port`, `--job-timeout`), carry over any other setting you changed in the old units by hand, and use the App key of the same App as the old host: a new install starts from the release's units, without your edits.

> [!WARNING]
> A backup holds the TOTP secret, so whoever can open it can approve as the owner. It is encrypted to the recovery key, so the **recovery kit** is what protects it: keep the kit off the machine that holds the backups. Whoever has a backup and the kit reads it. SuiteWard keeps no copy of the private key, and nobody can recover it for you.

Scheduled backups, a separate backup container and storage off the machine (Release assets) are not part of this release ([ADR 0019](decisions/0019-encrypted-backups-and-instance-recovery-key.md), [ADR 0021](decisions/0021-release-backups-and-health-reconciliation.md)): take a backup when you choose, and before every upgrade (the upgrade does it for you).

## 2. Back up

`suiteward backup` takes a backup while the service runs. It needs the service to answer `/status`, because the status belongs in the backup. When the service is down, start it, or take the backup by hand ([Appendix A](#isolated-host-bash-as-suiteward)), which stops the service anyway.

### The recovery key, once

```
suiteward backup init
```

It needs the installer's settings file and a service that answers `/status`: the instance id names the kit. It makes a key pair of the hybrid post-quantum kind of age, ML-KEM-768 with X25519, because a backup lives for years. Then it takes three steps:

1. **Keys.** It reads the instance id from `/status` and makes the pair. A key that is set already stops it, with its fingerprint: replacing it is `--replace` (below).
2. **Kit.** It writes the **recovery kit**, `suiteward-recovery-kit-<instance>.txt` in the current directory, or the file that `--kit PATH` names. The kit is a text file that only your user can read (mode 0600, or an owner-only ACL on Windows). It holds the private key, the line that starts with `AGE-SECRET-KEY-PQ-1`, with comments that name the instance, the public key and its fingerprint. It never overwrites a file: name another with `--kit`, or move the old one away. It warns that the kit is the only way to read a backup, and that whoever has it reads every backup, and asks you to copy it off this computer, for example into a password manager, and to delete this copy. This is the most important warning of the command: on a full screen it is a red box, and in plain mode a warning line. On a terminal you then type `stored` (a full screen first says that this is a confirmation, because nothing can see whether you copied the kit); without a terminal, say it with `--yes`. A kit that was not confirmed is removed, and nothing is changed.
3. **Settings.** It stores only the public key, as `backup_recipient: age1pq1...` in the settings file (about 2000 characters). The private key never reaches the settings, a log or an error.

It prints the fingerprint of the key (`SHA256:...`), which is also in the kit, so that you can tell kits apart. The key is the trusted configuration of the instance. Through SuiteWard, a pull request, a `.suiteward.yml` or an MCP call cannot change who a backup is encrypted to. Whatever runs as your user can still write the settings file, run `suiteward backup init --replace`, or (while no key is set) give `--backup-recipient` to an upgrade: on the co-located tier that includes the coding agent, and on the isolated tier whoever runs as the service account.

`--replace` makes a new key although one is set. The backups made with the old key still need the old kit, so keep it too: the command says so. Replacing the key does not re-encrypt an old backup.

### Take a backup

```
suiteward backup
```

The service keeps running. The command takes four steps, in this order:

1. **Status.** It reads `/status` first, and refuses before it writes anything when the service does not answer.
2. **Dump.** It runs `pg_dump` in the database container as the owner. The dump is held in memory and never written to disk in plaintext.
3. **Artifacts.** It exports the artifact volume. The engine writes that tarball to a path, so it waits in a private directory, `.suiteward-spool-<random>` (mode 0700, or an owner-only ACL on Windows), beside the backups until it is in the archive. The dump is taken before the volume, so the volume holds every artifact the dump names: an artifact is written before the row that names it, and is never deleted. The volume can hold newer artifacts that the dump does not name yet.
4. **Encrypt.** It streams the members into `suiteward-backup-<date>-<time>.tar.age`, with `manifest.json` last. The file is created readable by your user only, before any data is in it, and it never replaces an existing file.

The key is the one in the settings. With none, a terminal is offered to make one, as `backup init` does; anything else refuses and names `suiteward backup init`. `--backup-recipient` belongs to `install --upgrade`, not to this command.

A backup that fails removes the spool and the partial file, and says that nothing was changed. At the end it prints where the backup is, the fingerprint of its key, and the `suiteward restore` command for it. The spool is removed when the backup ends. If a backup or a restore is killed, its private directory stays, with plaintext in it: the next `backup`, `restore` or upgrade removes every `.suiteward-spool-*` and `.suiteward-restore-*` directory in the backup folder and says so. A symbolic link or a file with such a name is not a directory and stays. Two runs at once in one backup folder remove each other's private directory, and the one that loses fails and leaves nothing: run one at a time.

**Where it goes.** `--to DIR` names the folder. The default is a `backups` folder beside the MCP token copy of the co-located tier, which the installer's screens call the access token file (`%LOCALAPPDATA%\SuiteWard\backups` on Windows, `~/Library/Application Support/suiteward/backups` on macOS, `~/.config/suiteward/backups` on Linux), and `~/.config/suiteward/backups` of the service account on the isolated tier. The command makes the folder when it is missing, and refuses a folder that is a symbolic link: a backup is never written through a link. Other flags: `--settings`, `--token-file`, `--tier`, `--runtime`, `--connection` (which instance, as for `install --upgrade`) and `--plain`. An instance that has a settings file of its own gives `--token-file` (the token file its install was given) or `--to`: `install --upgrade` refuses it without `--token-file`, but `backup` does not, and without it takes the folder beside the default token file, which is the main instance's `backups` folder. The file has its own date and time and nothing is overwritten, but that folder then holds the backup of another instance.

### The backup of an upgrade

`suiteward install --upgrade` takes the same encrypted backup before it changes anything ([install guide, section 11](install.md#11-updating)). It needs the recovery key too. It takes it from the settings. Without one, a terminal is offered to make one; otherwise give the public key with `--backup-recipient age1pq1...` (or `backup_recipient` in the answers file), or the upgrade refuses before it starts anything, because a backup is never written in plaintext. A key that differs from the one in use is refused: replacing it is `suiteward backup init --replace`. On the isolated tier the upgrade also keeps the unit files it replaces as plain files in `suiteward-backup-<date>-<time>.units` beside the archive (they hold only the names of secrets), because a rollback copies them back.

### Keep it safe

- **The backup.** Copy it off this machine. It is encrypted, so any place does: another disk, a cloud drive.
- **The kit.** Keep it somewhere else, off the machine and apart from the backups. Whoever holds both reads every backup of the instance.
- **A lost kit.** Nobody can open the backups of its key, SuiteWard included. Make a new key with `suiteward backup init --replace`, take a new backup, and keep that kit. When no instance runs and no kit is left, rebuild from the protected default branch ([section 4](#4-re-bootstrap-from-the-protected-default-branch)).
- **Without SuiteWard.** A backup is an age file. An age tool with post-quantum support (the one that SuiteWard is built with is age 1.3.2) opens it with the kit, `age --decrypt --identity <kit> --output backup.tar <file>`, and `tar -xf backup.tar` unpacks it. That is for reading a backup on a machine with no installer: restore with `suiteward restore`.
- **A rehearsal.** A backup you never restored is a guess ([section 5](#5-rehearse)).

## 3. Restore

> [!CAUTION]
> A restore replaces this instance's database and artifacts with the backup. Run it only on an instance whose current data you have decided to replace. The command backs up what it replaces first, but keep the backup you restore from until the last check passes.

**What a restore loses.** A restore returns the instance to the backup's moment. The approvals, proposals and promotions made after the backup are gone, while the default branch on GitHub may have moved on, so the restored canonical can be behind it. There is no reset command. A canonical that is behind the default branch is fixed by a corrective pull request: open one from the current default branch with any small change outside the protected files, approve the revision its check names, and merge it, and the canonical equals the default branch again (see [ADR 0018](decisions/0018-corrective-pr-rollback-and-audit-history.md)). The restore says so, and gives this fix, only for a backup that names a repository or whose status was not recorded: with no repository there is no default branch to be behind. Back up before any planned change.

**Restore only a backup that you made and kept.** age encrypts to a public key, and the public key is in the settings file: anyone who has it can write an archive that decrypts with your kit, and its hashes then agree with its own manifest. A restore makes the backup authoritative: the owner, the TOTP enrollment and the canonical contracts all come from it. SuiteWard does not check who wrote the backup. The restore refuses a backup whose manifest has a release or a tier that is not a plain name, an image digest that is not `sha256:` and 64 hexadecimal characters (an empty digest is accepted, for a stack whose image was not known), or an instance that is not 8 hexadecimal characters (or `unknown`, for a backup that could not name it), and it never prints the refused value. It also refuses a `status.json` whose version is not a plain name. It drops the control and format characters (bidirectional overrides among them) from what it prints from `status.json`, and turns line breaks and tabs into spaces, so that a line of the text cannot be a line of the output. What those fields say (which instance, which owner, which repositories) is still the backup's own claim, not proof.

**Stop the other instance.** Two instances that use the same GitHub App would publish and promote for the same repositories. A restore of a backup that names a repository, or whose status was not recorded (so that it may name one), warns you; stop the other one before you go on. A backup that names no repository leaves two instances nothing to publish and promote for, and the warning is not given. Nothing in this release checks it.

**Restore with the same release that made the backup.** Migrations move forward only: a newer image may migrate the restored database afterwards, but an older image cannot read a newer schema. The restore refuses a backup of another release, and prints the install command of the backup's release (`suiteward install --image ghcr.io/ignisdevne/suiteward@sha256:<digest>`). The release is the `version` of its `status.json`; a backup that holds no status is known by its image digest. Never leave `suiteward-migrate` on an image that is newer than the restored schema and then run the service on an older one: the two units must name the same image, and that image must not be older than the schema in the dump.

**The application role must exist.** The dump records grants to `suiteward_app`, and the load stops at the first error. The restore refuses a database that lacks the role, so on a new machine install the release of the backup first, which creates it ([section 1](#1-what-a-backup-holds)). It also needs the database container running and healthy.

```
suiteward restore <backup.tar.age> [--kit PATH] [--yes --instance ID] [--backup-dir DIR]
```

| Flag | Meaning |
| --- | --- |
| `<backup.tar.age>` | The backup file. Name it, or `--plaintext-dir`, and not both. |
| `--kit PATH` | The recovery kit file. Without it the command asks for the private key line of the kit, hidden on a terminal (a full screen first has a page that says what to paste, and that `--kit` reads a file instead), or reads it from standard input (a pipe). A key is never an argument: it would show in the process list. |
| `--yes --instance ID` | Without a terminal: confirm the restore. `--instance` is the instance id of the backup, and a different id refuses. A terminal asks you to type the id. |
| `--backup-dir DIR` | Where the backup of the state that is replaced goes (default: the backup folder of [section 2](#take-a-backup)). The restore unpacks the backup there too. |
| `--plaintext-dir DIR` | Restore a directory of an earlier release instead of a file (below). |
| `--settings`, `--token-file`, `--tier`, `--runtime`, `--connection`, `--plain` | Which instance, as for `suiteward backup`: an instance that has a settings file of its own gives `--token-file` too, because without it the default of `--backup-dir` is the folder beside the default token file, the main instance's. A `--settings` file that does not exist stops the restore before it calls the container engine. With no settings file at all, a co-located restore needs `--runtime` and `--connection` (a restore replaces the data, so it never runs on defaults that no settings file named); on the isolated tier, `--tier isolated`. |

The command takes seven steps. The first two change nothing.

1. **Verify.** It checks that the database container runs and that the application role exists. It removes the private directories that a killed backup or restore left. It opens the backup with the kit and reads the whole file twice: once to check every member against the manifest, and once to unpack it into a private directory, `.suiteward-restore-<random>` (mode 0700, or an owner-only ACL on Windows), in the backup folder. The two readings must agree. The unpacked files are plaintext, so the directory is removed when the command ends, on a failure too. It then checks that the dump ends as `pg_dump` ends a dump, that the artifact tar can be listed to its end, and that the release of the backup is the release that runs. All of that happens before the instance is touched.
2. **Preview.** It shows what the backup holds (when it was made, its release and image digest, the instance ID, the installation type, the owner, the repositories and the database version) beside what the instance runs. On a full screen this is a page that says it is a confirmation, with the facts that say what is replaced by what first (`Instance ID`, `Version`, `Owner`, `Repositories`, `Installation type`, `Made`, `Database version`, then the key that the safety backup is locked with, and the backup), then the warnings. It always warns that the restore replaces the database and the saved files (artifacts), and for a backup that names a repository, or whose status was not recorded, adds that the default branch on GitHub may be ahead of the restored approved version (the canonical) and that any other instance that uses the same GitHub App must be stopped. It warns when the backup is from a different instance than the one that runs here (after the restore this instance becomes the backup's, and only the authenticator entry of the backup will work), when the installation types differ (`on this computer` and `on a separate server`), when the backup is an unencrypted folder, and when its status was not recorded. Then you confirm: on a terminal, type the instance id of the backup (the question says that it is a confirmation: the typed id is there so that an Enter out of habit, a script or the wrong instance cannot restore); otherwise pass `--yes --instance <id>`. Anything else ends the command with nothing changed. To check a backup and its kit without restoring, answer no here.
3. **Safety backup.** The state that is replaced is always backed up first, encrypted, to the key in the settings, or to the key of the kit that opens this backup when the settings have none. A service that does not answer is backed up too, with its status recorded as unavailable and the reason. The command prints where that backup is. Going back is `suiteward restore` of that file with its kit.
4. **Stop.** It stops the service. Co-located, the service container is removed, because a volume that a container uses cannot be replaced; isolated, both units stop.
5. **Load.** It empties the database and loads the dump as one transaction: `BEGIN`, drop and create the `public` schema (the application keeps every table in it), the dump, and `COMMIT` as the very last statement. A load that is cut short, because a statement fails, you press Ctrl-C or the input ends, is never committed, and the server rolls it back. Then it deletes the artifact volume, creates it empty and imports the tar (Docker through a throwaway container).
6. **Start.** It runs the migration, which applies the grants to `suiteward_app` again and any migration the dump lacks, then starts the service as the settings describe it, and waits for `/status`.
7. **Compare.** It compares what the service reports with the status of the backup: the release, the database version and the owner. A difference is an error. An integrity check that `failed` is an error too: the saved files (artifacts) and the database are not from the same moment. One that has not passed yet is a note, because it runs when the service starts, after each promotion and every hour: run `suiteward doctor` in a moment.

At the end the command says what it restored, where the backup before the restore is, and to run `suiteward doctor`. For a backup that names a repository, or whose status was not recorded, it also says how to bring the canonical level with the default branch, which a restore can leave behind it: open one corrective pull request from the current default branch (any small change outside the protected files), approve the revision its check names, and merge it (see "What a restore loses" above). On a full screen that line is on the summary page, which the screen keeps when it closes; in plain mode it is a kept line.

**What it does not restore.** The credentials of the installation are not in a backup, so they stay as they were: the database passwords, the MCP token, the executor's token and the GitHub App key, and the settings and the units. The executor is not part of a backup either, and keeps working with the token it has. The owner's TOTP enrollment is the backup's: use the authenticator entry that names the instance of the backup (`SuiteWard:<login> (<tier> <instance>)`). If the instance has no recovery key, the command ends with a warning to run `suiteward backup init`.

**The database log.** `psql` repeats the rows that it fails on, and the rows hold the TOTP secret, so the command never shows its message. The log of the database container may quote rows too, the TOTP secret included: read it as something secret.

**If a step fails.** The message says where the restore stopped, what the instance is in, how to get the service back, and how to run the restore again:

- after **Stop**: the service may be stopped, and nothing else was changed;
- during **Load**: the database was not changed (the load is a single transaction that the server cancels if anything fails, and it commits only after the last line of the dump), and neither were the saved files (artifacts);
- while the saved files (artifacts) are replaced: the database now holds the backup's data, but the saved files were not replaced, or only in part. Run the restore again;
- after **Start**: the database and the saved files are the backup's, but the service did not start;
- after **Compare**: the database and the saved files are the backup's and the service runs, but it does not match the backup.

To start the service again, co-located, run `suiteward install` with the options of this run (it creates the service again from the settings, and keeps the volumes and the secrets); isolated, run `systemctl --user start suiteward.service`. To return to the state before the restore, run `suiteward restore` on the backup that step 3 printed, with its kit.

### A plaintext directory of an earlier release

`suiteward restore --plaintext-dir DIR` restores a directory that holds `suiteward.sql`, `suiteward-artifacts.tar` and `status.json`: the directory that `install --upgrade` of `v0.3.0-alpha.1` and `v0.3.0-alpha.2` made, or one made by hand ([Appendix A](#isolated-host-bash-as-suiteward)). It needs no kit. It warns that nothing is encrypted and nothing verifies who made the files or that they are untouched, and every rule above holds. Four limits:

- The state that is replaced is backed up encrypted, and a plaintext directory has no kit whose key could be used. The instance must have its own recovery key already: run `suiteward backup init` first.
- Its `status.json` must name the instance with 8 hexadecimal characters, as `/status` has since `v0.2.0-rc.1`. A backup from `v0.1.x` does not, and the command refuses it: restore it [by hand](#isolated-restore).
- Its `status.json` must not say `unavailable`. That record, `{"unavailable": true, ...}`, is made only by an encrypted backup of this release, for a service that did not answer, so a directory that says it is not a backup that this command restores, and the command refuses it.
- It needs the release that made it, like any backup.

### Going back after an upgrade

An upgrade that migrated cannot be undone by a restore alone: the backup is of the previous release, and migrations only go forward. The way back is to run the previous release on the instance, and then to restore the backup that the upgrade made. Both steps exist, but the whole way back has not been rehearsed end to end, on either tier or on Docker (an upgrade that also moved the PostgreSQL pin may need more): rehearse it ([section 5](#5-rehearse)) before you rely on it.

- **Co-located (Podman):** remove the stack and its data volumes, install the previous release, and restore. The restore then replaces the empty data. Run every `podman` command in the PowerShell session where you selected the rootless connection ([install guide, step 5.1](install.md#5-install-co-located)), and pass `--connection <name>` to the `suiteward` commands when the install used one. The commands below are for the default instance. An instance that has a settings file of its own adds its `--settings` and `--token-file` to each `suiteward` command, as the way back that the upgrade prints does.

  ```powershell
  suiteward uninstall --yes
  podman volume rm suiteward-data suiteward-db-data
  suiteward install --image ghcr.io/ignisdevne/suiteward@sha256:<previous index>
  suiteward restore <backup.tar.age> --kit <kit>
  ```

  The first command keeps the secrets and the settings, and the install uses them, so it creates the volumes again, starts the database from the kept password and creates the role again. It does not run the GitHub flow, because the settings hold the App and its key secret exists, and it does not run setup: the restore brings the enrollment back. The digest of the previous release is in the upgrade's closing message, and in the restore's refusal. On Docker, remove the two volumes with `docker volume rm`.
- **Isolated** (as `suiteward`, in the shell of [step 10.3 of the install guide](install.md#10-running-on-a-separate-host-operators)): the upgrade kept the unit files that it replaced in `suiteward-backup-<date>-<time>.units`, beside the archive in the backup folder (`~/.config/suiteward/backups` by default). Put them back, reload systemd, stop the service, and restore:

  ```bash
  cp ~/.config/suiteward/backups/suiteward-backup-<date>-<time>.units/* ~/.config/containers/systemd/
  systemctl --user daemon-reload
  systemctl --user stop suiteward.service
  suiteward restore ~/.config/suiteward/backups/suiteward-backup-<date>-<time>.tar.age --kit <kit>
  ```

  Stop the service before the restore: the reload does not restart it, so it would still answer `/status` as the new release, and the restore refuses a backup of another release than the one that answers. With the service stopped, the restore compares the image of the unit files with the backup's. It then stops both units and starts them again.

The executor is not part of the way back. A release before the executor has no executor API, so a running executor gets an error on every lease request and backs off up to a minute between tries. On the co-located tier the first command above (`suiteward uninstall --yes`) already stops it and removes its program and definition, and the previous release installs none. On the isolated host the executor is the administrator's and keeps running: as the administrator, `systemctl --user --machine=suiteward-exec@.host disable --now suiteward-executor.service` stops it until you upgrade again, and the host step of the new release enables and starts it again ([install guide, section 11](install.md#11-updating)).

  The directory holds `suiteward-db.container` too, with the PostgreSQL pin of the old release. When that `Image=` line is the same as the one in the file you replaced, which the plan of the upgrade showed as `PostgreSQL  unchanged` (`v0.2.0` and the alphas pin the same image), nothing else is needed. When the upgrade moved the pin, the database container still runs the new image after the copy: restart it with `systemctl --user restart suiteward-db.service` and wait until it is healthy before the restore. If the pin moved to another major version of PostgreSQL, the old image cannot open a data directory that the new one used, and this way back does not apply.

## 4. Re-bootstrap from the protected default branch

Use this when no backup can be restored. The protected files are already on each repository's default branch, so the first contract is proposed again from them. The commands and their replies are those of the install guide's [First use: bootstrap](install.md#8-first-use-bootstrap), and the [runtime contract](contracts/runtime.md#explicit-bootstrap-propose-and-skip-m17b-rework-2026-10-16) is normative.

> [!WARNING]
> You lose the version history, the ledgers, the old enrollment and its TOTP secret, and the proposal history. Open pull requests wait for the new contract: their `SuiteWard / Contract` check stays pending (`bootstrap-awaiting`) until V1 exists. The protected files themselves stay in git, on the default branch.

Follow the flow of your tier up to its readiness check, then continue with [enroll, propose and approve](#enroll-propose-and-approve-both-tiers), which is the same for both.

### Isolated re-bootstrap

Run the commands in the shell of the `suiteward` account, which [the manual backup in Appendix A](#isolated-host-bash-as-suiteward) opens.

1. Purge the instance. Stop the three units and remove the volumes. The secrets stay: the owner password secret initialises the new database on its first start. The application role lived in the removed database volume, so step 3 creates it again.

   ```bash
   systemctl --user stop suiteward.service suiteward-migrate.service suiteward-db.service
   ```

   ```bash
   podman volume rm --force suiteward-db-data suiteward-data
   ```

2. Install the same version again. Check that both `Image=` lines, in `suiteward-migrate.container` and in `suiteward.container`, name the same digest, the signed image index digest of `ghcr.io/ignisdevne/suiteward:<version>` (step 3 of the [isolated restore](#isolated-restore) says where to find it and how to verify it). Reload the units, and start the database alone.

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

   Then wait until the database is healthy, and run the role chain and the shape check of step 4 of the [isolated restore](#isolated-restore) of Appendix A (its role-creation block):

   ```bash
   until [ "$(podman inspect --format '{{.State.Health.Status}}' suiteward-db)" = healthy ]; do sleep 2; done
   ```

4. Start the service. It runs the migration first, and the migration needs the role:

   ```bash
   systemctl --user start suiteward.service
   ```

   Then wait until the service is ready, as in step 5 of the manual backup in Appendix A.

   ```bash
   curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:<http port>/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. If it gives up, run it again until it answers.

### Co-located re-bootstrap

Run every `podman` command in the PowerShell session where you selected the rootless connection ([install guide, step 5.1](install.md#5-install-co-located)).

1. Purge the stack. This stops the executor and removes its program and definition, and removes the containers, the volumes, the secrets and the settings file, and the MCP token copy and the executor's token copy when they are this instance's: the file that `--token-file` names (the executor's is beside it), else the default token file with the default settings file. An instance that has a settings file of its own gets `--settings` and `--token-file` here, because a purge keeps a default token file that may be another instance's. Before it acts, the purge lists what it deletes and which token file it keeps. The settings file holds the public half of the recovery key, so make a new key with `suiteward backup init` after the reinstall: the old kit still opens the backups made with the old key. The purge asks you to type the instance id of the stack (the `instance` of its `/status`), or takes it as `--instance`, so that it never removes an instance you did not mean to; read it before you purge, because the service is gone afterwards.

   ```powershell
   suiteward uninstall --purge --yes --instance <instance id>
   ```

2. Install the same version again, by the digest of its signed image index (take it from the release notes, verify it with `pwsh ./scripts/verify-release.ps1 -Reference ghcr.io/ignisdevne/suiteward@sha256:<index>` on this PC, with cosign 3.x, and pass it to `--image`), with the App you already have: the installer creates both roles, migrates, starts the service, writes the rulesets (they are already imported: answer the wait with Enter, or pass `--ruleset-imported yes`), and runs setup on a real terminal. The reinstall needs the App key file again, writes a new MCP token (update your MCP client with the token in `$env:LOCALAPPDATA\SuiteWard\mcp-token`), and makes the executor again with a new token of its own. Give no GitHub values only if this stack does not use GitHub.

   ```powershell
   suiteward install --image ghcr.io/ignisdevne/suiteward@sha256:<index> --github-app-id <id> --github-installation-id <id> --github-owner <login> --github-repositories '<owner/name>,<owner/name>' --github-app-key-file <path to the App key file> --test-check '<owner/name>=<check name>' --test-check '<owner/name>=<check name>'
   ```

   Expect `The database update (migration) finished: the database is at the version this release needs.`, then `Started suiteward-db and suiteward (<image>). GitHub is set up.`, and then setup (the next section, step 1, says what it asks).

3. Wait until the service is ready.

   ```powershell
   curl.exe --retry 10 --retry-connrefused -fsS http://127.0.0.1:<http port>/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. If it gives up, run it again until it answers. If `127.0.0.1:<http port>` does not answer from Windows, or `curl.exe` keeps retrying, stop it with Ctrl-C and run the check through the Podman machine, as its rootless user, as in step 5 of the manual backup in Appendix A:

   ```powershell
   podman machine ssh --username user "curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:<http port>/readyz"
   ```

### Enroll, propose and approve (both tiers)

These steps are the same on both tiers. Run the first one in the shell of your flow: the `suiteward` shell of the isolated host, or the PowerShell session of the co-located PC.

1. Run `setup` once for the instance, not once for each repository: it enrolls one owner for all of them, and every run replaces the TOTP secret. This starts a new enrollment: the new entry is named `SuiteWard:<login> (<tier> <instance>)`, and the database is new, so its instance id (shown as `instance` in `/status`) differs from the old one. Before you scan the new entry, delete the old entry of this instance, the one with the old id in its name. Entries of other instances stay. An entry named only `SuiteWard:<login>` predates this release: delete it once the new entry's code is accepted. It needs the GitHub settings in place. On the isolated host they are in `~/.config/suiteward/github.env`. On the co-located PC the installer ran setup at the end of its install when the stack has GitHub (step 2 of the co-located re-bootstrap): then this step is done, and you go on to the next one. If it printed `suiteward enroll` instead, run it here, on a real terminal with session recording off: `suiteward enroll --owner <your GitHub login> --test-check '<owner/name>=<check name>' --test-check '<owner/name>=<check name>'`, with `--tier isolated` on the isolated host. It asks for what you leave out, takes the test checks of the settings, and runs the command below for you; run that one by hand only on a machine that has no `suiteward` binary.

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
   curl -sS http://127.0.0.1:<http port>/status
   ```

   On the co-located PC, in PowerShell:

   ```powershell
   curl.exe -sS http://127.0.0.1:<http port>/status
   ```

   If `127.0.0.1:<http port>` does not answer from Windows, or `curl.exe` keeps retrying, stop it with Ctrl-C and run it through the Podman machine, as its rootless user:

   ```powershell
   podman machine ssh --username user "curl -sS http://127.0.0.1:<http port>/status"
   ```


## 5. Rehearse

Rehearse before you rely on a backup. The restore replaces the data of the instance it runs on, so use a test repository and a stack whose data you can lose. It also confirms that the commands work with your engine and your version. The steps are the same on both tiers.

- [ ] Make the recovery key (`suiteward backup init`) and copy the kit off the machine. Note the fingerprint.
- [ ] Back up (`suiteward backup`) and copy the file to a second place.
- [ ] Check the backup and the kit without changing anything: run `suiteward restore <file> --kit <kit>` and decline at the confirmation. It verifies the whole file, shows what the backup holds beside the instance that runs, and ends with the restore not confirmed and nothing changed.
- [ ] Change something, for example approve a revision, then restore the backup: `suiteward restore <file> --kit <kit>`. It ends with the release, the schema and the owner of the backup reported by the service.
- [ ] Run `suiteward doctor`: `integrity` `passed` in each `github` entry and `outbox.failed` 0 in `/status`.
- [ ] Restore into a second machine that you installed for the release of the backup ([section 1](#1-what-a-backup-holds)), to prove the way back from a lost host.
- [ ] After an upgrade, go back ([section 3](#going-back-after-an-upgrade)): that way has not been rehearsed end to end.

## Appendix A: Backup and restore by hand

These flows are the runbook of the releases before the encrypted backup. Use them when the commands cannot:

- the release has no `suiteward backup` or `suiteward restore` (it is older than phase M1.13);
- the backup is from `v0.1.x`: its status has no instance id, so `suiteward restore --plaintext-dir` refuses it;
- the installer cannot adopt the stack, because it was edited by hand (the commands stop with a diff);
- the service is down and cannot be started, so `suiteward backup` cannot read its status.

They are written for Podman. Docker has no `volume export` and `volume import`, so they do not cover a Docker stack; `suiteward backup` and `suiteward restore` do.

**The HTTP port.** The commands below write it `<http port>`: 27081 by default from the release that made it the default (M1.14), 8081 on a stack that was installed before (an upgrade never moves a port), or the port you gave `--http-port`. It is `http_port` in the settings file, and `podman port suiteward` shows it.

> [!WARNING]
> These flows write the dump and the artifacts in plaintext, and the dump holds the TOTP secret, so anyone with the files can approve as the owner. Keep every file offline and encrypted at rest by your own means: these flows do not encrypt.

Restore with the **same SuiteWard version** that made the backup. Migrations move forward only: a newer image may migrate the restored database afterwards, but an older image cannot read a newer schema. Never leave `suiteward-migrate` on an image that is newer than the restored schema and then run the service on an older one: the two units must name the same image, and that image must not be older than the schema in the dump. That version is the `version` value in `status.json`.

The one exception is a backup made by v0.1.x. That release has no `migrate` command, and the units and the installer of this layout run it, so they cannot start the v0.1.x image. Restore such a backup with the release you upgrade to: its migration brings the restored schema forward. `/status` then shows the new `version` and `schemaVersion`, and the same `owner`.

A directory that `install --upgrade` of an earlier release made holds the same three files: read it where the flows below say `~/suiteward-backup` (isolated) or `$HOME\suiteward-backup` (co-located). What a restore loses, section 3 says.

**Back up by hand.** Stop the service container, not the database, so that the database and the artifacts come from the same moment. Follow the flow of your tier from its first step to the last.

### Isolated host (bash, as `suiteward`)

The `suiteward` account has a `nologin` shell, so `sudo -iu suiteward` fails. As the administrator, open a shell as `suiteward` with a working user bus, as [step 10.3 of the install guide](install.md#10-running-on-a-separate-host-operators) does:

```bash
r=/run/user/$(id -u suiteward)
sudo -u suiteward -H env XDG_RUNTIME_DIR=$r \
  DBUS_SESSION_BUS_ADDRESS=unix:path=$r/bus bash
```

The command opens a new shell and waits for commands, so it prints nothing. Check it with `whoami`, which prints `suiteward`. Run every command of the isolated flows of this appendix and of section 4 in this shell, except the ones that say they run on another machine (the verification of the digest, and the copy off the host). Type `exit` to leave it.

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
   curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:<http port>/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. The service needs a moment after the start, and the command retries while nothing answers. If it gives up (curl does not retry a connection that is reset), run it again until it answers.

   ```bash
   curl -sS -o ~/suiteward-backup/status.json http://127.0.0.1:<http port>/status
   ```

   Expect `status.json` to hold the status object, with `version`, `schemaVersion`, `owner` and `outbox`.

6. Keep a copy of the backup folder off this machine, offline and encrypted, as the warning at the top of this appendix says. How you copy the files depends on how you reach the host. If you reach it only through a remote shell that cannot copy files, serve the folder on the host's loopback, forward that port to your PC with your remote-access tool, download each file, then stop both.

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
   curl.exe --retry 10 --retry-connrefused -fsS http://127.0.0.1:<http port>/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. The service needs a moment after the start, and the command retries while nothing answers.

   ```powershell
   curl.exe -sS -o $HOME\suiteward-backup\status.json http://127.0.0.1:<http port>/status
   ```

   Expect `status.json` to hold the status object, with `version`, `schemaVersion`, `owner` and `outbox`.

   The port is published inside the Podman machine and may not answer from Windows, as the status command that `suiteward install` prints shows. A `curl.exe` that cannot connect keeps retrying for minutes: stop it with Ctrl-C, then run both commands through the Podman machine as its rootless user, `podman machine ssh --username user` (the default ssh user is `root`, which is the rootful store and does not hold this stack). Redirect on the Windows side to save the file:

   ```powershell
   podman machine ssh --username user "curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:<http port>/readyz"
   ```

   ```powershell
   podman machine ssh --username user "curl -sS http://127.0.0.1:<http port>/status" > $HOME\suiteward-backup\status.json
   ```

6. Keep a copy of the backup folder off this machine, offline and encrypted, as the warning at the top of this appendix says. No command.


**Restore by hand.** Follow the flow of your tier. Each one starts by checking the backup before anything is replaced.

### Isolated restore

Run the commands in the shell of the `suiteward` account, which [the manual backup above](#isolated-host-bash-as-suiteward) opens.

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
   curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:<http port>/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. If it gives up, run it again until it answers.

   ```bash
   curl -sS http://127.0.0.1:<http port>/status
   ```

   Expect the same `version` as in `status.json`, the same `schemaVersion` as before, the same `owner` login, and `outbox.failed` 0. If GitHub is configured, each `github` entry shows `integrity` `passed`; it appears after the first check, which runs when the service starts, right after each promotion and then every hour, so wait a moment for it. If `integrity` reports `failed`, the artifacts and the database come from different moments: restore both from the same backup.

9. Return the units to your update path. Step 3 pinned both `Image=` lines to the backup's version, and the stack stays on that version until you change them. The install guide's [Updating](install.md#11-updating) gives the way: `suiteward install --upgrade` backs up first, then moves both `Image=` lines to the new release, migrates and restarts the service. Update when you choose. Never leave `suiteward-migrate` on a newer image than the one the service runs, and never restart `suiteward.service` alone after changing an image: the service refuses a schema that is not the one of its image. Automatic updates are not supported. After step 3 the units are already loaded; if you edited them again, reload them:

   ```bash
   systemctl --user daemon-reload
   ```

   Expect no output.

### Co-located restore

Run every `podman` command in the PowerShell session where you selected the rootless connection ([install guide, step 5.1](install.md#5-install-co-located)).

The installer creates the volumes and the roles only when it runs, and a run also starts the service. So after the two checks, the restore removes the containers and the data volumes, and starts a clean stack of the same version (the release you upgrade to, for a backup from v0.1.x: see the start of this appendix), instead of pinning the image and replacing the volume. The installer creates the roles `suiteward` and `suiteward_app`, migrates, and starts the service. It keeps the secrets and its settings, so a run with no GitHub flags does not create an App and does not run setup: the restore brings the enrollment back.

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

3. Remove the stack's containers and data volumes, then start a clean stack of the same version. Take the index digest of that release from its release notes, verify it with `pwsh ./scripts/verify-release.ps1 -Reference ghcr.io/ignisdevne/suiteward@sha256:<index>` (on this PC, with cosign 3.x; [install guide, step 10.4](install.md#10-running-on-a-separate-host-operators)), and pass it to `--image`. The `version` in `status.json` names the release (the release you upgrade to, for a backup from v0.1.x). The commands below are for the default instance: an instance that has a settings file of its own adds its `--settings` and `--token-file` to `suiteward uninstall` and `suiteward install`.

   ```powershell
   suiteward uninstall --yes
   ```

   ```powershell
   podman volume rm suiteward-data suiteward-db-data
   ```

   ```powershell
   suiteward install --image ghcr.io/ignisdevne/suiteward@sha256:<index>
   ```

   The first command removes the three containers and keeps the secrets and the settings. The second deletes the current data. The install then creates the volumes, starts the database from the kept password, creates the role `suiteward_app` again with a new password, migrates and starts the service on an empty database with the schema of this release, and the next steps replace that data. Expect `The database update (migration) finished: the database is at the version this release needs.`, then `Started suiteward-db and suiteward (<image>). GitHub is set up.` It does not run the GitHub flow: the settings hold the App, and its key secret exists. It ends with a note that no owner is enrolled, and the command to enroll one: ignore it and do not run setup, because the restore brings the owner back.

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

7. Run the install of step 3 again, with the same `--image` (and, on a new PC, with `--github no` again). It replaces the stopped service, runs the migration again (it applies the grants to `suiteward_app` and any migration the dump lacks), and starts the service on the restored data. The secrets and volumes it finds are kept.

   ```powershell
   suiteward install --image ghcr.io/ignisdevne/suiteward@sha256:<index>
   ```

8. Wait until the service is ready, then check `/status` against `status.json`.

   ```powershell
   curl.exe --retry 10 --retry-connrefused -fsS http://127.0.0.1:<http port>/readyz
   ```

   Expect `{"schemaVersion":N,"status":"ready"}`. If it gives up, run it again until it answers.

   ```powershell
   curl.exe -sS http://127.0.0.1:<http port>/status
   ```

   Expect the same `version` as in `status.json`, the same `schemaVersion` as before, the same `owner` login, and `outbox.failed` 0. If GitHub is configured, each `github` entry shows `integrity` `passed`; it appears after the first check, which runs when the service starts, right after each promotion and then every hour, so wait a moment for it. If `integrity` reports `failed`, the artifacts and the database come from different moments: restore both from the same backup.

   If `127.0.0.1:<http port>` does not answer from Windows, or `curl.exe` keeps retrying, stop it with Ctrl-C and run both commands through the Podman machine, as its rootless user, as in step 5 of the manual backup above:

   ```powershell
   podman machine ssh --username user "curl --retry 10 --retry-connrefused -fsS http://127.0.0.1:<http port>/readyz"
   ```

   ```powershell
   podman machine ssh --username user "curl -sS http://127.0.0.1:<http port>/status"
   ```

On a new PC, put GitHub back now: the stack runs without it. Run the installer again with the App's values and key file. The restore brought the enrollment back, so the installer finds an owner in `/status` and skips setup: the TOTP secret stays as it was. `--ruleset-imported yes` skips the wait for a ruleset import: the rulesets are already on GitHub.

```powershell
suiteward install --image ghcr.io/ignisdevne/suiteward@sha256:<index> --github-app-id <id> --github-installation-id <id> --github-owner <login> --github-repositories '<owner/name>' --github-app-key-file <path to the App key file> --test-check '<owner/name>=<check name>' --ruleset-imported yes
```

Expect the installer to print the protection result of each repository and a note that an owner is already enrolled and setup was skipped. Do not run `suiteward enroll`, which it names: enrolling again replaces the TOTP secret. Delete your copy of the key file afterwards. The GitHub values stay in the settings file, so a later install needs none of them.

Nothing else returns to an update path, because `suiteward install --upgrade` moves the release when you choose.

