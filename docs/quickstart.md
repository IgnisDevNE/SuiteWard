# SuiteWard quickstart

This is the short path for one owner on a Windows PC, the **co-located** tier, where SuiteWard runs next to your coding agent and reports reduced assurance. Every step links to the full [install guide](install.md), which has the expected output and the troubleshooting. For full assurance, use the **isolated** tier on a separate Linux host instead ([section 10](install.md#10-running-on-a-separate-host-operators)).

**You need** PowerShell 7.2 or later (`pwsh`), a Podman machine, an authenticator app, and a GitHub user account that administers the repositories (up to 20).

## 1. Create the GitHub App (about 5 minutes, [section 4](install.md#4-create-the-github-app))

- **Create it** under your account, with webhook **inactive**, installable **only on this account**.
- **Repository permissions,** and nothing else:
  - Metadata: Read-only
  - Pull requests: Read and write
  - Contents: Read-only
  - Administration: Read-only
  - Checks: Read and write
- **Generate a private key** (`.pem`), then **install** the App on the repositories to protect.
- **Note** the App ID and the installation ID.

## 2. Start SuiteWard ([section 5](install.md#5-install-co-located))

In the release source folder (`vX.Y.Z`):

```powershell
podman pull ghcr.io/ignisdevne/suiteward:vX.Y.Z
./deploy/local.ps1 -Image ghcr.io/ignisdevne/suiteward:vX.Y.Z -GitHubAppKeyFile <path to the PEM> -GitHubAppId <app id> -GitHubInstallationId <installation id> -GitHubRepositories '<owner/name>,<owner/name>'
podman machine ssh "curl -s http://127.0.0.1:8081/status"
```

- Keep the quotes around the repository list.
- Delete your copy of the `.pem` after the first run.
- Pass the same three GitHub values on every later run.

## 3. Enroll yourself as the owner ([section 6](install.md#6-run-setup))

Turn off terminal recording first. Setup prints your TOTP secret once:

```powershell
podman exec -it suiteward /suiteward setup --owner <login> --test-check '<owner/name>=<name of your test check>'
```

- Use one `--test-check` per repository.
- Scan the URI with your authenticator and type the current code.
- Never paste the output, the secret or a code anywhere.

## 4. Protect the default branch ([section 7](install.md#7-create-the-ruleset))

Create a ruleset on each repository's default branch that:

- requires `SuiteWard / Contract`, with your App as the source;
- requires your test check;
- requires branches to be up to date;
- has an empty bypass list.

`/status` must then show `enforced` for each repository.

## 5. The first contract ([section 8](install.md#8-first-use-bootstrap))

On a pull request that carries tests, comment:

```
/suiteward propose
```

1. With no `.suiteward.yml`, SuiteWard replies with a suggested one and an editor link. Review it, commit it, and comment `/suiteward propose` again.
2. Approve the revision it names with `/suiteward approve P1-R1 <code>` and merge. V1 is promoted.

From then on, every pull request that changes a protected file needs your `/suiteward approve` on its current revision, and `/suiteward suggest` tells you what the contract misses ([section 9](install.md#9-day-to-day)).

## Before you rely on it

- **Back up** with the [backup runbook](backup.md). The backup holds the TOTP secret.
- **Never run two SuiteWard instances on the same repository.**
- **Update** by pulling the new image and running `local.ps1` again with the new `-Image`, after a backup ([section 11](install.md#11-updating)).
- **Agents:** to let a coding agent help with the installation, point it to [agent-install.md](agent-install.md), which says where it must stop.
