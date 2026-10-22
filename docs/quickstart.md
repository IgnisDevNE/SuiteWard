# SuiteWard quickstart

This is the short path for one owner. The **co-located** tier runs SuiteWard next to your coding agent, on your Windows PC, and reports reduced assurance. The **isolated** tier runs it on a separate AlmaLinux 9 host and gives the full assurance. Every step links to the full [install guide](install.md), which has the details and the troubleshooting.

**You need** a GitHub user account that administers the repositories (up to 20), an authenticator app, a real terminal, and a container runtime. Podman, rootless, is strongly recommended. Docker is allowed on the co-located tier only, and discouraged. GitHub does not enforce rulesets on a private repository on the Free plan: protect a public repository, or use a paid plan ([section 3](install.md#3-before-you-start)).

## Co-located (Windows PC)

On Windows, start the Podman machine first: `podman machine start`. Windows with Podman is the path validated live. macOS and Linux are covered by tests only.

**1. Get and verify the installer** ([section 4](install.md#4-get-and-verify-the-installer)). From the release page of the release you chose, download the binary for your platform, `SHA256SUMS` and `SHA256SUMS.sigstore.json`, then verify them once, with PowerShell 7.2 or later and cosign 3.x, from the release source:

```powershell
pwsh ./scripts/verify-release.ps1 -Blob SHA256SUMS -Bundle SHA256SUMS.sigstore.json
pwsh ./scripts/verify-release.ps1 -Sums SHA256SUMS -File suiteward_<tag>_windows_amd64.exe
```

Rename the binary to `suiteward.exe` and put it on your PATH.

**2. Install** ([section 5](install.md#5-install-co-located)):

```powershell
suiteward install
```

It starts the stack, and it walks you through the rest, on this PC:

- one click in your browser creates the GitHub App, and one more installs it on the repositories to protect;
- you choose the repositories and the test check of each;
- it writes a ruleset file for each repository and tells you where to import it (GitHub, Settings, Rules, New ruleset, Import a ruleset);
- it runs setup: turn off terminal recording first. Setup prints your TOTP secret once. Scan it with your authenticator and type the current code. Never paste the output, the secret or a code anywhere.

**3. Check** ([section 5.7](install.md#5-install-co-located)):

```powershell
suiteward doctor
```

Expect `ok` on every line. A `FAIL` line names the fix. For Docker instead of Podman, read the warning in [section 5](install.md#5-install-co-located) first, then add `--runtime docker --accept-docker` to the install.

## Isolated (AlmaLinux 9 host)

**1. Verify the installer on your PC** as above, copy the binary and `SHA256SUMS` to the host, and check them there ([section 4](install.md#4-get-and-verify-the-installer)):

```
sha256sum --check --ignore-missing SHA256SUMS
```

Copy the binary to `/usr/local/bin/suiteward`. Create the GitHub App by hand, from the checklist in [section 10, step 10.2](install.md#10-running-on-a-separate-host-operators), and note the App ID, the installation ID and the key file.

**2. Prepare the host**, once, as the administrator ([step 10.3](install.md#10-running-on-a-separate-host-operators)):

```
sudo suiteward install host
```

**3. Install the stack** in a shell as the account `suiteward`: paste the App key into `~/app-key.pem` without echo, then run `suiteward install --tier isolated --image sha256:<index>` (from the release notes) with the App values and `--github-app-key-file ~/app-key.pem` ([step 10.4](install.md#10-running-on-a-separate-host-operators) opens the shell and gives the whole command). Then run setup with the command it prints, create the ruleset by hand ([section 7](install.md#7-create-the-ruleset)), and run `suiteward doctor`.

## The first contract ([section 8](install.md#8-first-use-bootstrap))

On a pull request that carries tests, comment:

```
/suiteward propose
```

1. With no `.suiteward.yml`, SuiteWard replies with a suggested one and an editor link. Review it and commit it. Wait about a minute, until the check shows the new head, then comment `/suiteward propose` again.
2. Approve the revision it names with `/suiteward approve P1-R1 <code>` and merge. V1 is promoted.

From then on, every pull request that changes a protected file needs your `/suiteward approve` on its current revision, and `/suiteward suggest` tells you what the contract misses ([section 9](install.md#9-day-to-day)).

`/status` shows `enforced` for each repository within 15 minutes, with the caveat `ruleset-bypass-unverified`: check the bypass list yourself ([section 7](install.md#7-create-the-ruleset)).

## Before you rely on it

- **Back up** with the [backup runbook](backup.md). The backup holds the TOTP secret.
- **Never run two SuiteWard instances on the same repository.**
- **Update** with `suiteward install --upgrade`, which backs up first ([section 11](install.md#11-updating)).
- **Agents:** to let a coding agent help with the installation, point it to [agent-install.md](agent-install.md), which says where it must stop.
