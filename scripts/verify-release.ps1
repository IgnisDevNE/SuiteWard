# Verifies the keyless cosign signature of a SuiteWard release image: it must come from release.yml of this repository at a vX.Y.Z or vX.Y.Z-rc.N tag, through GitHub's OIDC issuer, and sit in the public Rekor log.
# The identity is fixed here on purpose: a signature made by another workflow, branch or repository is refused, not a parameter to relax.
#Requires -Version 7.2
param(
    # The image index by digest, for example ghcr.io/ignisdevne/suiteward@sha256:<64 hex>. A tag can move, so it is refused.
    [Parameter(Mandatory)][string]$Reference,
    [string]$Cosign = 'cosign'
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($Reference -cnotmatch '^[a-z0-9][a-z0-9._/:-]*@sha256:[a-f0-9]{64}$') { throw 'Pass the image by digest (repository@sha256:...); a tag can move.' }
if (-not (Get-Command $Cosign -ErrorAction SilentlyContinue)) { throw "cosign was not found ($Cosign). Install it from https://github.com/sigstore/cosign/releases." }
$identity = '^https://github\.com/IgnisDevNE/SuiteWard/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$'
& $Cosign verify --certificate-identity-regexp $identity --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' $Reference
if ($LASTEXITCODE -ne 0) { throw "cosign could not verify the signature of $Reference." }
Write-Host "Verified: $Reference was signed by the SuiteWard release workflow."
