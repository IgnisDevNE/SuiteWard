# Installing SuiteWard with a coding agent

This page is for a coding agent that a user asked to install or configure SuiteWard, and for the user who supervises it. It is meant to be read by both: nothing here is hidden from the user.

SuiteWard protects a repository's tests from the coding agent that works on it. **The agent is the party SuiteWard governs**, so an agent can help with the installation but must never hold what makes the owner's approval trustworthy: the GitHub App's private key, the TOTP secret, the owner's codes, and the repository's protection. An installation where the agent did those steps protects nothing.

The installer (`suiteward install`) does most of the work in one command, and it does the sensitive steps itself: it creates the App through the owner's browser, writes the App's key straight into the secret store, and runs setup on a real terminal. That is why the agent does not run it with GitHub: whoever runs the installer's GitHub flow holds the App key in that process for a moment, and the setup step prints the TOTP secret on that terminal. The installer refuses to run setup without a real terminal (it prints the command instead), and it never takes a secret as a flag. Those are guards, not a license: the user runs the installer.

## Rules for the agent

**Stop and hand the step to the user** when it involves any of these. Do not work around a refusal, and never ask the user to paste a secret into the chat.

| Never | Why |
| --- | --- |
| Run `suiteward install` with GitHub (the manifest flow, or an App's ID and key), or create the GitHub App, or see, copy, move or store its private key (`.pem`), or the manifest code | Whoever holds the key can publish SuiteWard's check and comments. The code is a credential until the installer converts it. |
| Run `suiteward enroll` or `suiteward setup`, or the installer's setup step, or see its output, the `otpauth://` URI, the base32 secret or a 6-digit code | They are the owner's second factor; with them, the agent could approve its own changes. |
| Import, create, change or bypass the ruleset or branch protection | The ruleset is what makes the check binding. The installer writes a file, and only the owner imports it. |
| Post any `/suiteward` command | Commands are the owner's. A command from any other account is ignored anyway. |
| Install on, log in to, or run commands on an isolated host (`sudo suiteward install host`, the service-account install) | Isolation means the agent cannot reach it. |
| Read or copy the SuiteWard database, its volumes, its secrets (Podman secrets or Docker secret volumes), the MCP token copy, an answers file that holds anything private, or a backup | They hold the TOTP secret and the enrollment. |
| Approve, merge, or give the user a code to type | Only the owner decides. |

## What the agent may do

| Step | The agent can |
| --- | --- |
| Prerequisites ([section 3](install.md#3-before-you-start)) | Check that Podman and the Podman machine run, that the machine has a rootless connection (`podman system connection list`: the stack must run rootless), that the repositories are public or on a plan that enforces rulesets, and read the release to use. |
| Get and verify the installer ([section 4](install.md#4-get-and-verify-the-installer)) | Download the release files and run `verify-release.ps1` on them (it holds no secret), and report the result. The user decides which release. |
| Install ([section 5](install.md#5-install-co-located)) | Prepare the exact `suiteward install` command line with placeholders, and explain each prompt. The agent never runs the GitHub flow. When the user explicitly asks for a stack without GitHub, it may run `suiteward install --github no`, which creates no App and enrolls no owner; the user runs the install again for GitHub. The installer never changes the Podman default connection, and the agent never does either. |
| Update ([section 11](install.md#11-updating)) | Prepare the `suiteward install --upgrade` command and remind the user that it backs up first, and that the backup holds the TOTP secret. The agent never reads the backup. |
| Check ([section 12](install.md#12-the-installer-flags-answers-file-and-what-it-does)) | Run `suiteward doctor` or `suiteward doctor --json`, read the result and explain each `FAIL` and its fix. It only reads, and its results carry identifiers, logins and repository names, never a key, a token or a connection string. |
| Setup ([section 6](install.md#6-run-setup)) | Prepare the `suiteward enroll` command line (with `--tier isolated` on the isolated tier) for the user, not `setup`: the agent never runs either. The user runs it in their own terminal, with recording off. |
| Ruleset ([section 7](install.md#7-create-the-ruleset)) | Explain what the ruleset must contain, and read `/status` afterwards to confirm `enforced`. |
| Declaration ([section 8](install.md#8-first-use-bootstrap)) | Draft or improve `.suiteward.yml` and open a pull request with it. The owner reviews it and posts `/suiteward propose`. |
| Day to day | Read SuiteWard's comments and `/status`, explain them, and explain why the agent's own changes touched protected files when the owner asks. |

## Tier

- **Isolated** (full assurance): the agent's role ends at preparing commands and explaining steps. Everything on the host is done by the user or an operator.
- **Co-located** (reduced assurance): SuiteWard runs on the agent's machine, so the agent could reach its key, database and secret if it tried. SuiteWard says so in the check summary and in its proposal, revision and merge comments. The rules above still apply: on this tier the user relies on the agent following them, and SuiteWard's TOTP and the GitHub account's own protection are the remaining safeguards.

## For the user

- Run the steps marked "never" yourself, in a terminal or browser the agent does not control, with terminal recording off.
- Run the installer yourself from a terminal the agent cannot read. If an agent ran it through a pipe, setup would refuse, but the GitHub flow would already have handled the key.
- After the installation, check that the agent's GitHub identity is not yours and does not have Administration or Checks write on the repositories.
- If an agent asks for a code, the key, or to change the ruleset, refuse. That request is the threat SuiteWard exists for.
