# S2: installer spike

- **Date:** 2026-10-21/22
- **Phase:** [M1.12](../plan/phases/M1.12.md)
- **Decision:** [D-INSTALLER](../plan/decisions.md#d-installer-the-installer-and-the-first-distributable-version)

The live checks run before the installer's design was locked. The tools ran outside the repository and none is kept. Every App, installation and repository named below was throwaway and is deleted after the spike.

## 1. GitHub App manifest flow

A Go program served an auto-submitting form from a listener on `127.0.0.1:<random port>`. It POSTed a manifest to GitHub's "new App" page with a random `state`, then exchanged the returned code with `POST /app-manifests/{code}/conversions`. The private key stayed in memory, and was neither printed nor written.

| Run | Owner | Manifest | Redirect | Result |
| --- | --- | --- | --- | --- |
| 1 | organization | `hook_attributes: {url: <dummy>, active: false}` | loopback listener | App created; conversion returned the ID, slug, owner, permissions and key |
| 2 | organization | no `hook_attributes` | a port nothing listens on (the code was pasted by hand) | App created; conversion succeeded with the pasted code |
| 3 | personal account | `hook_attributes` inactive | loopback listener | App created under the user |

Findings:

- **Manifest content:**
  - The five permission keys `metadata: read`, `pull_requests: write`, `contents: read`, `administration: read` and `checks: write` are accepted as written, and come back unchanged. No events are subscribed.
  - `hook_attributes` can be omitted: an App without a webhook is created. The installer omits it.
- **Redirect:**
  - A `redirect_url` on `http://127.0.0.1:<port>/callback` works; GitHub redirects the browser there with `code` and `state`.
  - The page asks for the user's authentication (passkey) before creating the App.
- **Paste fallback:** when the browser cannot reach the callback (for example on an isolated host without a port-forward), the address bar of the error page still shows the full callback URL, with `code=` and `state=`. Pasting the code into the CLI completes the conversion.
  - A port the browser blocks (port 9) shows `ERR_UNSAFE_PORT` and still shows the URL. The installer uses its own listener port in both modes, so the forwarded and the pasted case differ only in whether the forward is up.
- **Installation:** after the user installs the App (`https://github.com/apps/<slug>/installations/new`), `GET /app/installations` with the App's JWT returns the installation within one polling interval (5 s), for the organization and for the personal account.
  - It reports `repository_selection: selected`, and the same five permissions.

## 2. Ruleset import

A ruleset JSON in the REST shape was imported through the UI (Settings → Rules → New ruleset → Import a ruleset). It required `SuiteWard / Contract` with `integration_id` set to the App's ID, plus `CI / test` with no integration, on `~DEFAULT_BRANCH`, strict.

- **The import maps it correctly:**
  - `SuiteWard / Contract` is shown bound to the App;
  - `CI / test` shows "Any source";
  - the target is the default branch;
  - "Require branches to be up to date" is set.
- **The App must be installed on the repository first.** Creating the imported ruleset failed with `Invalid parameter required_status_checks: Invalid integration ids` while the App was installed only on another repository. After the repository was added to the installation, the same form created the ruleset.
- **A check that has never run is accepted** as a required check (`CI / test` had never run in that repository).
- **Free plan:** a private repository of an organization on GitHub Free shows "Your rulesets won't be enforced on this private repository until you upgrade this organization account to GitHub Team". SuiteWard already reports this as `enforcement-unavailable`. The installer must say it before the user imports the ruleset, not only in `/status`.

Consequence for the installer, which keeps the planned order:
1. install the App on the chosen repositories;
2. choose the test checks;
3. write the ruleset JSON and ask for the Import;
4. run setup.

## 3. Docker on a Windows PC that also runs Podman

- **The name `docker` is not Docker here.**
  - The `docker` command on the PATH is Podman's shim (`C:\Program Files\RedHat\Podman\docker.cmd`).
  - The default Docker endpoint, the `npipe:////./pipe/docker_engine` pipe, is served by the Podman machine: `docker version` reports server 6.1.4.
  - Docker Desktop's own engine is the `desktop-linux` context (`npipe:////./pipe/dockerDesktopLinuxEngine`).
- **What the installer must do:** never trust the command name. It identifies the engine behind the selected endpoint (server version and OS from `version`/`info`) and tells the user which engine it is about to use.
- **The two engines fight over the pipe.**
  - With the Podman machine running, Docker Desktop fails to start with `setting up docker api proxy listener: open \\.\pipe\docker_engine: Access denied`. The pipe belongs to the Podman machine.
  - Docker Desktop started once it had the pipe. The Podman machine then started on its own pipe (`npipe:////./pipe/podman-machine-default`) and printed how to point `DOCKER_HOST` at it.
  - **For the installer:** whichever engine starts first owns `docker_engine`. The installer and `doctor` report who owns it instead of failing on "access denied", and always address an engine by its own endpoint.
- **Docker Desktop's CLI is not on the PATH** when Podman's shim is installed: `C:\Program Files\Docker\Docker\resources\bin`. Its credential helper (`docker-credential-desktop`) lives in the same directory, and a pull fails with `error getting credentials` unless that directory is on the PATH of the process. The installer runs Docker Desktop's CLI by full path, with that directory on the child's PATH.

Docker Desktop 29.9.2 (`desktop-linux` context), with throwaway `busybox` containers and a non-secret probe value:

| Check | Result |
| --- | --- |
| `docker run --replace` | `unknown flag: --replace`. The installer removes the container, then runs it. |
| A file bind-mounted read-only from a Windows path, readable by uid 65532 and uid 999 | Readable, but **mode `777`, owner `0:0` inside the container**. An owner-only ACL on Windows does not carry into the VM, so every process in Docker's VM could read it. **Rejected.** |
| The secret in a **Docker named volume**, written through stdin by a one-off container (`umask 077; cat > /s/<name>; chown <uid>; chmod 0400`), then mounted read-only | Mode `400`, owner `65532:65532`. Readable by uid 65532 under `--read-only --cap-drop all --security-opt no-new-privileges`; refused for uid 1000 (`Permission denied`). **Chosen.** |
| `docker exec -t` | A TTY is allocated (`/dev/pts/0`), so `exec -it` can carry setup's terminal. |
| `-p 127.0.0.1:18081:8080` | Reachable from Windows on `http://127.0.0.1:18081` (HTTP 200), with no machine ssh. |

**One secret volume per consumer.** migrate and serve both run as uid 65532, so file modes cannot keep the owner URL away from `serve`. On Podman, that separation comes from which secrets are mounted into which container (SH). The same holds on Docker with one volume per consumer, each mounted only into its own container:
- the database password, for the database container;
- the owner URL and the app URL, for migrate;
- the app URL, the App key and the MCP token, for serve.

Checked: the migrate container lists only `database-owner-url`; the serve container lists only `database-url`, and `cat /run/secrets/database-owner-url` there gives `No such file or directory`. The full stack on Docker (postgres reading its password from a volume as uid 999, then migrate, then serve) has not run yet. It is the first acceptance test of I4.

**Podman machine restart.** After the Podman machine was stopped and started again (to let Docker Desktop take the pipe), the co-located stack made by `local.ps1` stayed stopped: both containers showed `Exited`, and `/readyz` did not answer until they were started by hand.
- Rootless Podman honours `--restart always` only while the user's `podman-restart.service` is enabled, and on this machine it is not. So today every reboot of the PC also stops a co-located instance.
- In `podman-machine-default`, the user's systemd manager is `running`, linger is `yes`, and `podman-restart.service` is `disabled`. So both options work:
  - the installer can enable `podman-restart.service` for that user;
  - or it can run the co-located stack as quadlet units inside the machine.
  `doctor` checks whichever is chosen.
- **Not closed by that:** after a Windows reboot, the Podman machine itself is not running until someone starts it. Starting the machine at logon is a separate gap. Until it is decided, `doctor` reports a stopped machine, and the docs say to run `podman machine start`.

**Consequence for D-INSTALLER:** on Docker, the secrets go into named volumes, one volume per consumer (see above), one file per secret, owned by the uid that reads it (65532 for the service, 999 for the database password), mode 0400. They are written from memory through stdin, so **they never touch the Windows disk**. That replaces the private per-user directory in the plan. Docker stays discouraged, for its rootful daemon and because the volume is readable by anyone who controls that daemon.
