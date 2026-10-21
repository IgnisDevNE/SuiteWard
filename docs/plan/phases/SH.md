# SH: Security hardening

**Goal:** close what the security-gate assessment found, before the final MVP release. The [threat model](../../security/threat-model.md) lists each control and the test that proves it. The phase ships as `v0.2.0`.

**Decision:** owner request, 2026-10-19 (the security gate of the [roadmap](../README.md)); the findings and the choices below are the owner's, 2026-10-20.

**Rules:**
- **Test first.** Every behavior change commits its failing test before the fix ([TDD rule](../../tdd.md)).
- **Invariants stay.** Canonical immutability, candidate non-authority, exact-revision approval, atomic promotion, and evidence is not authority.
- **The repository is public.** Descriptions of a control say what it does, never how the earlier behavior could have been abused.
- **This repository is governed by SuiteWard.** A change to a protected file needs the owner's approval of the final revision, so changes are batched.

## Owner decisions (2026-10-20)

Recorded in the [decision register](../decisions.md#d-security-review-decisions-of-the-security-review-phase-sh) and in the ADRs they amend:

1. **Commands follow their pull request** (closed pull requests, commands on another pull request): a command first observed after its pull request stopped being open is ignored, and a command applies only to its own pull request (`other-pull`). [ADR 0027](../../decisions/0027-agent-human-trust-separation.md).
2. **Commands are read where GitHub shows text** (D-COMMANDS).
3. **Case-fold collisions are protected and invisible characters are escaped** (D-SCOPE).
4. **Bounded input and reported state** (declaration size, unconfigured repositories): a 64 KiB cap on a declaration read from a pull request, an unconfigured enrolled repository no longer holds back commands and shows in `/status`, and a new canonical is verified right after its promotion.
5. **The service connects as a least-privilege database role**; only `suiteward migrate` holds the owner credential ([ADR 0007](../../decisions/0007-canonical-contract-authority.md)).
6. **The isolated host is AlmaLinux 9 (EL9) only, with Podman 5.5 or later.** Automatic image updates are not supported: a host follows a release by the owner changing the digest.
7. **Release images are signed** (keyless cosign, provenance, SBOM), after [ADR 0008](../../decisions/0008-progressive-integrity-and-execution.md)'s amendment.
8. **The TOTP secret stays unencrypted at rest in the database** for the MVP; encryption with a host key is an M3 candidate (ADR 0027).
9. **`gosec` runs with two SHA-1 exceptions** (RFC 6238 TOTP and git object IDs), no `//nolint` and no path exclusion.
10. **Backlog:** a maintainer command to authorize or refuse the merge of a fork pull request, after a deeper analysis.

## Tasks

- **H1 Commands follow the pull request and what GitHub shows:** closed pull requests, `other-pull`, and a command read only outside fences, HTML comments and indented blocks.
- **H2 Paths and text:** protection of case-fold collisions with protected paths, escaping and refusal of invisible runes, and raw UTF-8 validated before layout.
- **H3 Suggestions and fuzzing:** quoting of YAML special scalars in suggested scope items, a split reduction listing, a corrected size claim (H3), and fuzz targets for the parsers of untrusted input with their regression inputs (H3b).
- **H4 Database role and deployment:** the service as a least-privilege role with `suiteward migrate` as the owner step (H4a); the quadlets and `deploy/local.ps1` running migrate before serve, with digest-pinned units and a rootless co-located stack (H4b); the install, upgrade, backup and host runbooks for AlmaLinux 9 (H4c).
- **H5 Runtime bounds:** HTTP timeouts on both listeners, a publisher panic stored as a classification and a digest, the 64 KiB declaration cap, a skipped unconfigured repository, an integrity check right after a promotion, and the code-offset log removed.
- **H6 Supply chain:** base images and tools pinned by digest with a cross-check, `gosec`, `zizmor`, `gitleaks` and `trivy` in the checks, a weekly fuzz workflow (H6); build provenance, SBOM and keyless signing of the release index, and `latest-stable` moved only after a verified signature (H6b).
- **H7 Rootless co-located stack:** `deploy/local.ps1` selects the rootless connection and refuses a rootful one. It was delivered with H4b.
- **H8 Records:** the ADR amendments, the decision register, the threat model rows, `SECURITY.md` and this page.

## Verification

- `./scripts/dev.ps1 check` and `./scripts/dev.ps1 persistence` on the exact revision.
- `sw-reviewer` reviews each task against its brief, and the advisor reviews the phase before the integration PR.
- The threat model cites a test for each control; a control with no test says so.
- Live: the release candidate on both tiers, as the release plan below says.

## Release plan (v0.2.0)

1. **A release candidate (`v0.2.0-rc.N`)**, created by the owner on the commit the orchestrator names, approved in the `release` environment. It is the first image built, scanned, provenance-attested and signed by the new workflow.
2. **The co-located PC** runs the candidate through `deploy/local.ps1`, which converts the v0.1.x stack: it creates the role, replaces the database secret and keeps the owner URL in its own secret.
3. **A new AlmaLinux 9 isolated host** is installed with the candidate by [install section 10](../../install.md#10-running-on-a-separate-host-operators), and the backup of the existing isolated host (taken on v0.1.3) is restored into it; `migrate` brings the restored schema forward. This replaces the existing Ubuntu isolated host after it is validated.
4. **The final release `v0.2.0`**, after both runs and the owner's authorization, verified with `scripts/verify-release.ps1` before any host pins it.

## Known limits

- **EL9 facts to confirm on the first AlmaLinux 9 host:** the Podman package and its CVE-2026-57231 fix (read from the changelog), rootless networking and user services under linger, SELinux enforcing with named volumes, and the metadata-endpoint block. Each is a check in the install guide; the first host confirms the guide.
- **The signing path first runs on the release candidate tag:** the workflow cannot be exercised on a branch, because the identity is the workflow at a `v*` tag. `v0.1.x` images are unsigned.
- **The release scan covers `linux/amd64`** only; the arm64 image has the same packages and source.
- **Signing does not stop another workflow from pushing a tag.** A host that pins and verifies the digest is protected; one that follows a tag is not ([D-RELEASE](../decisions.md#d-release-platforms-versions-license-and-notices)).
- **The in-place v0.1.x upgrade of a quadlet host** (install 11.5) is not run live: there is no EL9 host on v0.1.x. The conversion logic is exercised on the co-located stack through `deploy/local.ps1`, and the restore of a v0.1.3 backup is exercised on the new host.
- **Accepted:** the unencrypted TOTP secret at rest, the co-located tier's reduced assurance, and the fork pull request merge command (backlog).
