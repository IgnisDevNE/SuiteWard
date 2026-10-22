# Verifies the keyless cosign signature of a SuiteWard release artifact: it must come from release.yml of this repository at a vX.Y.Z, vX.Y.Z-rc.N or vX.Y.Z-alpha.N tag, through GitHub's OIDC issuer, and sit in the public Rekor log.
# Image mode (-Reference) verifies the image index; blob mode (-Blob and -Bundle) verifies a release file, such as a binary or SHA256SUMS; sums mode (-Sums and -File) compares a binary with a SHA256SUMS you verified in blob mode first.
# The identity is fixed here on purpose: a signature made by another workflow, branch or repository is refused, not a parameter to relax.
#Requires -Version 7.2
[CmdletBinding(DefaultParameterSetName = 'Image')]
param(
    # The image index by digest, for example ghcr.io/ignisdevne/suiteward@sha256:<64 hex>. A tag can move, so it is refused.
    [Parameter(Mandatory, ParameterSetName = 'Image')][string]$Reference,
    [Parameter(Mandatory, ParameterSetName = 'Blob')][string]$Blob,
    # The Sigstore bundle published next to the file, <file>.sigstore.json.
    [Parameter(Mandatory, ParameterSetName = 'Blob')][string]$Bundle,
    [Parameter(Mandatory, ParameterSetName = 'Sums')][string]$Sums,
    [Parameter(Mandatory, ParameterSetName = 'Sums')][string]$File,
    [Parameter(ParameterSetName = 'Image')][Parameter(ParameterSetName = 'Blob')][string]$Cosign = 'cosign'
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($PSCmdlet.ParameterSetName -eq 'Sums') {
    $name = Split-Path -Leaf $File
    $entry = @(Get-Content -LiteralPath $Sums | ForEach-Object { if ($_ -cmatch '^(?<hash>[a-fA-F0-9]{64}) [ *](?<name>.+)$') { $Matches } } | Where-Object { $_.name -ceq $name })
    if ($entry.Count -ne 1) { throw "$Sums must list $name exactly once." }
    if ((Get-FileHash -LiteralPath $File -Algorithm SHA256).Hash -ine $entry[0].hash) { throw "The hash of $File does not match $Sums." }
    Write-Host "Matched: $name is listed in $Sums with its SHA-256."
    return
}
if ($PSCmdlet.ParameterSetName -eq 'Image') { if ($Reference -cnotmatch '^[a-z0-9][a-z0-9._/:-]*@sha256:[a-f0-9]{64}$') { throw 'Pass the image by digest (repository@sha256:...); a tag can move.' } }
if (-not (Get-Command $Cosign -ErrorAction SilentlyContinue)) { throw "cosign was not found ($Cosign). Install it from https://github.com/sigstore/cosign/releases." }
$identity = '^https://github\.com/IgnisDevNE/SuiteWard/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-(rc|alpha)\.[0-9]+)?$'
$issuer = 'https://token.actions.githubusercontent.com'
if ($PSCmdlet.ParameterSetName -eq 'Blob') {
    & $Cosign verify-blob --bundle $Bundle --certificate-identity-regexp $identity --certificate-oidc-issuer $issuer $Blob
    if ($LASTEXITCODE -ne 0) { throw "cosign could not verify the signature of $Blob." }
    Write-Host "Verified: $Blob was signed by the SuiteWard release workflow."
    return
}
& $Cosign verify --certificate-identity-regexp $identity --certificate-oidc-issuer $issuer $Reference
if ($LASTEXITCODE -ne 0) { throw "cosign could not verify the signature of $Reference." }
Write-Host "Verified: $Reference was signed by the SuiteWard release workflow."
