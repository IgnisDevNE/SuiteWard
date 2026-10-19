# Installing SuiteWard with a coding agent

This page is for a coding agent that a user asked to install or configure SuiteWard, and for the user who supervises it. It is meant to be read by both: nothing here is hidden from the user.

SuiteWard protects a repository's tests from the coding agent that works on it. **The agent is the party SuiteWard governs**, so an agent can help with the installation but must never hold what makes the owner's approval trustworthy: the GitHub App's private key, the TOTP secret, the owner's codes, and the repository's protection. An installation where the agent did those steps protects nothing.

## Rules for the agent

**Stop and hand the step to the user** when it involves any of these. Do not work around a refusal, and never ask the user to paste a secret into the chat.

| Never | Why |
| --- | --- |
| Create the GitHub App, or see, copy, move or store its private key (`.pem`) | Whoever holds the key can publish SuiteWard's check and comments. |
| Run `suiteward setup`, or see its output, the `otpauth://` URI, the base32 secret or a 6-digit code | They are the owner's second factor; with them, the agent could approve its own changes. |
| Post any `/suiteward` command | Commands are the owner's. A command from any other account is ignored anyway. |
| Create, change or bypass the ruleset or branch protection | The ruleset is what makes the check binding. |
| Install on, log in to, or run commands on an isolated host | Isolation means the agent cannot reach it. |
| Read or copy the SuiteWard database, its volumes, its Podman secrets or a backup | They hold the TOTP secret and the enrollment. |
| Approve, merge, or give the user a code to type | Only the owner decides. |

## What the agent may do

| Step | The agent can |
| --- | --- |
| Prerequisites | Check PowerShell 7.2+, Podman and the Podman machine, and read the release version to use. |
| GitHub App ([section 4](install.md#4-create-the-github-app)) | Show the user the checklist of permissions and verify, from the user's screenshots or the App's public page, that nothing extra is granted. |
| Co-located start ([section 5](install.md#5-install-co-located)) | Prepare the exact `local.ps1` command with placeholders. Run it only if the user explicitly asks, and never handle the `.pem` itself: the user passes the path. |
| Setup ([section 6](install.md#6-run-setup)) | Prepare the `setup` command line with the owner login and the test check names. The user runs it in their own terminal. |
| Ruleset ([section 7](install.md#7-create-the-ruleset)) | Explain what the ruleset must contain, and read `/status` afterwards to confirm `enforced`. |
| Declaration ([section 8](install.md#8-first-use-bootstrap)) | Draft or improve `.suiteward.yml` and open a pull request with it. The owner reviews it and posts `/suiteward propose`. |
| Day to day | Read SuiteWard's comments and `/status`, explain them, and explain why the agent's own changes touched protected files when the owner asks. |

## Tier

- **Isolated** (full assurance): the agent's role ends at preparing commands and explaining steps. Everything on the host is done by the user or an operator.
- **Co-located** (reduced assurance): SuiteWard runs on the agent's machine, so the agent could reach its key, database and secret if it tried. SuiteWard says so in the check summary and in its proposal, revision and merge comments. The rules above still apply: on this tier the user relies on the agent following them, and SuiteWard's TOTP and the GitHub account's own protection are the remaining safeguards.

## For the user

- Run the steps marked "never" yourself, in a terminal or browser the agent does not control, with terminal recording off.
- After the installation, check that the agent's GitHub identity is not yours and does not have Administration or Checks write on the repositories.
- If an agent asks for a code, the key, or to change the ruleset, refuse. That request is the threat SuiteWard exists for.
