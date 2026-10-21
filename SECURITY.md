# Security policy

## Supported versions

Only the latest release receives security fixes. Releases are tagged `vX.Y.Z` and published as the container image `ghcr.io/ignisdevne/suiteward`. Upgrade to the latest release before you report an issue that may already be fixed.

## Report a vulnerability privately

Use GitHub's private vulnerability reporting: open the repository's **Security** tab, choose **Report a vulnerability**, or go to <https://github.com/IgnisDevNE/SuiteWard/security/advisories/new>. Only the maintainers can read the report.

Do not open a public issue, pull request or comment for a vulnerability, and do not post details anywhere else before a fix is released.

Please include:

- the SuiteWard version or image digest, and whether it runs on the isolated or the co-located tier ([install guide](docs/install.md));
- what you found and what it lets an attacker do, in terms of the controls in the [threat model](docs/security/threat-model.md) where you can;
- steps to reproduce, or a proof of concept, with the smallest input that shows it;
- any logs or output, with secrets, tokens and TOTP codes removed.

Never send a real private key, token or TOTP secret.

The maintainers answer in the report; allow a few days for the first answer. When a fix is released, the advisory credits you unless you ask not to be named.

## Scope

The [threat model](docs/security/threat-model.md) lists what SuiteWard defends against and what it does not: the trusted host administrator and the assurance of the co-located tier are out of scope.

Releases from `v0.2.0` are signed. Before you pin a release, verify its signature with `scripts/verify-release.ps1` ([install guide](docs/install.md)).
