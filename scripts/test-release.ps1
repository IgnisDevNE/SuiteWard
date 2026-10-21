# Proves that verify-release.ps1 refuses a tag reference, requires the fixed release-workflow identity and issuer, and fails when cosign fails. Offline: cosign is replaced by a script that records its arguments.
#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$testRoot = Join-Path ([IO.Path]::GetTempPath()) "suiteward-release-$([Guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $testRoot | Out-Null
$verify = Join-Path $PSScriptRoot 'verify-release.ps1'
$log = Join-Path $testRoot 'args.txt'
$fake = Join-Path $testRoot 'cosign.ps1'
Set-Content -LiteralPath $fake -Value "Set-Content -LiteralPath '$log' -Value `$args`nexit `$env:FAKE_COSIGN_EXIT"
$digest = 'ghcr.io/ignisdevne/suiteward@sha256:' + ('a' * 64)
function Assert-Rejected {
    param([scriptblock]$Action, [string]$Message)
    try { & $Action *>$null } catch { if ($_.Exception.Message -like "*$Message*") { return }; throw "Wrong rejection. Expected '$Message', got: $($_.Exception.Message)" }
    throw "Expected rejection containing: $Message"
}
try {
    $env:FAKE_COSIGN_EXIT = '0'
    foreach ($bad in 'ghcr.io/ignisdevne/suiteward:v0.2.0', 'ghcr.io/ignisdevne/suiteward', 'ghcr.io/ignisdevne/suiteward@sha256:abc') {
        Assert-Rejected { & $verify -Reference $bad -Cosign $fake } 'by digest'
        if (Test-Path -LiteralPath $log) { throw "cosign ran for $bad." }
    }
    & $verify -Reference $digest -Cosign $fake *>$null
    $cosignArgs = @(Get-Content -LiteralPath $log)
    if ($cosignArgs[0] -cne 'verify' -or $cosignArgs[-1] -cne $digest) { throw "cosign was not called as 'verify <digest>': $cosignArgs" }
    $regexp = $cosignArgs[[array]::IndexOf($cosignArgs, '--certificate-identity-regexp') + 1]
    $issuer = $cosignArgs[[array]::IndexOf($cosignArgs, '--certificate-oidc-issuer') + 1]
    if ($issuer -cne 'https://token.actions.githubusercontent.com') { throw "Unexpected OIDC issuer: $issuer" }
    $workflow = 'https://github.com/IgnisDevNE/SuiteWard/.github/workflows/release.yml'
    foreach ($identity in "$workflow@refs/tags/v1.2.3", "$workflow@refs/tags/v1.2.3-rc.1") { if ($identity -cnotmatch $regexp) { throw "The release identity $identity is refused." } }
    foreach ($identity in "$workflow@refs/heads/main", "$workflow@refs/tags/v1.2", "$workflow@refs/tags/v1.2.3/x",
        'https://github.com/IgnisDevNE/SuiteWard/.github/workflows/deploy.yml@refs/tags/v1.2.3',
        'https://github.com/Other/SuiteWard/.github/workflows/release.yml@refs/tags/v1.2.3',
        'https://github.com/IgnisDevNE/SuiteWard/xgithub/workflows/release.yml@refs/tags/v1.2.3') {
        if ($identity -cmatch $regexp) { throw "The identity $identity is accepted." }
    }
    # The verify job's tag check and the signing identity must accept the same tag shapes.
    $tagCheck = [regex]::Match((Get-Content -LiteralPath (Join-Path $PSScriptRoot '../.github/workflows/release.yml') -Raw), '\[\[ ! "\$TAG" =~ (?<re>\S+) \]\]').Groups['re'].Value
    if (-not $tagCheck) { throw 'The tag check was not found in release.yml.' }
    foreach ($tag in 'v1.2.3', 'v10.20.30', 'v1.2.3-rc.1', 'v1.2.3-rc.12', 'v1.2', 'v1.2.3.4', 'v1.2.3-rc', 'v1.2.3-rc.', 'v1.2.3-beta.1', 'v1.2.3-rc.1x', 'v1.2.3/x', 'vx.y.z', '1.2.3', 'main') {
        if (($tag -cmatch $tagCheck) -ne ("$workflow@refs/tags/$tag" -cmatch $regexp)) { throw "release.yml and verify-release.ps1 disagree about the tag $tag." }
    }
    $env:FAKE_COSIGN_EXIT = '1'
    Assert-Rejected { & $verify -Reference $digest -Cosign $fake } 'could not verify'
    Assert-Rejected { & $verify -Reference $digest -Cosign (Join-Path $testRoot 'missing') } 'was not found'
    Write-Host 'Release verification checks passed.'
} finally { Remove-Item Env:FAKE_COSIGN_EXIT -ErrorAction SilentlyContinue; Remove-Item -LiteralPath $testRoot -Recurse -Force }
