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
    foreach ($identity in "$workflow@refs/tags/v1.2.3", "$workflow@refs/tags/v1.2.3-rc.1", "$workflow@refs/tags/v1.2.3-alpha.4") { if ($identity -cnotmatch $regexp) { throw "The release identity $identity is refused." } }
    foreach ($identity in "$workflow@refs/heads/main", "$workflow@refs/tags/v1.2", "$workflow@refs/tags/v1.2.3/x",
        'https://github.com/IgnisDevNE/SuiteWard/.github/workflows/deploy.yml@refs/tags/v1.2.3',
        'https://github.com/Other/SuiteWard/.github/workflows/release.yml@refs/tags/v1.2.3',
        'https://github.com/IgnisDevNE/SuiteWard/xgithub/workflows/release.yml@refs/tags/v1.2.3') {
        if ($identity -cmatch $regexp) { throw "The identity $identity is accepted." }
    }
    # The verify job's tag check and the signing identity must accept the same tag shapes.
    $tagCheck = [regex]::Match((Get-Content -LiteralPath (Join-Path $PSScriptRoot '../.github/workflows/release.yml') -Raw), '\[\[ ! "\$TAG" =~ (?<re>\S+) \]\]').Groups['re'].Value
    if (-not $tagCheck) { throw 'The tag check was not found in release.yml.' }
    foreach ($tag in 'v1.2.3', 'v10.20.30', 'v1.2.3-rc.1', 'v1.2.3-rc.12', 'v1.2.3-alpha.1', 'v0.3.0-alpha.12', 'v1.2.3-alpha', 'v1.2.3-alpha.', 'v1.2.3-alpha.1x', 'v1.2.3-ALPHA.1', 'v1.2.3-alpha.1-rc.1', 'v1.2', 'v1.2.3.4', 'v1.2.3-rc', 'v1.2.3-rc.', 'v1.2.3-beta.1', 'v1.2.3-rc.1x', 'v1.2.3/x', 'vx.y.z', '1.2.3', 'main') {
        if (($tag -cmatch $tagCheck) -ne ("$workflow@refs/tags/$tag" -cmatch $regexp)) { throw "release.yml and verify-release.ps1 disagree about the tag $tag." }
    }
    # The release ships the five installer targets and CI cross-builds the same list; install.sh (the one-line installer) is hashed, signed and uploaded beside the binaries.
    $releaseText = (Get-Content -LiteralPath (Join-Path $PSScriptRoot '../.github/workflows/release.yml') -Raw).Replace("`r", '')
    $targets = 'linux/amd64', 'linux/arm64', 'windows/amd64', 'darwin/amd64', 'darwin/arm64'
    $releaseTargets = @(([regex]::Match($releaseText, 'for target in (?<t>[a-z0-9/ ]+); do').Groups['t'].Value) -split ' ')
    if (($releaseTargets -join ',') -cne ($targets -join ',')) { throw "release.yml builds [$($releaseTargets -join ' ')], expected [$($targets -join ' ')]." }
    $ciTargetList = [regex]::Match((Get-Content -LiteralPath (Join-Path $PSScriptRoot '../.github/workflows/ci.yml') -Raw), 'foreach \(\$target in (?<t>[^)]+)\)').Groups['t'].Value
    $ciTargets = @([regex]::Matches($ciTargetList, "'([a-z0-9/]+)'") | ForEach-Object { $_.Groups[1].Value })
    if (($ciTargets -join ',') -cne ($targets -join ',')) { throw "ci.yml cross-builds [$($ciTargets -join ' ')], release.yml ships [$($targets -join ' ')]." }
    foreach ($required in 'grep -aqF "$DIGEST" "$file"', 'cp scripts/install.sh "$RUNNER_TEMP/dist/install.sh"', 'sha256sum suiteward_* install.sh > SHA256SUMS', 'for file in suiteward_* install.sh SHA256SUMS; do', 'gh release upload "$TAG" suiteward_* install.sh install.sh.sigstore.json SHA256SUMS SHA256SUMS.sigstore.json --clobber') {
        if (-not $releaseText.Contains($required)) { throw "release.yml lacks: $required" }
    }
    # A missing script stops the run in verify, before the owner's approval and the image push, not after an image without installer assets is public.
    $verifyJob = [regex]::Match($releaseText, '(?ms)^  verify:\n(?<body>.*?)(?=^  [a-z]+:|\z)').Groups['body'].Value
    if (-not $verifyJob.Contains('[ -f scripts/install.sh ]')) { throw 'The verify job must fail when scripts/install.sh is missing.' }
    # Blob mode: the same identity and issuer, the bundle and the file passed through, a failing cosign refused.
    Remove-Item -LiteralPath $log
    & $verify -Blob 'suiteward_v1.2.3_linux_amd64' -Bundle 'suiteward_v1.2.3_linux_amd64.sigstore.json' -Cosign $fake *>$null
    $blobArgs = @(Get-Content -LiteralPath $log)
    if ($blobArgs[0] -cne 'verify-blob' -or $blobArgs[-1] -cne 'suiteward_v1.2.3_linux_amd64') { throw "cosign was not called as 'verify-blob ... <file>': $blobArgs" }
    if ($blobArgs[[array]::IndexOf($blobArgs, '--bundle') + 1] -cne 'suiteward_v1.2.3_linux_amd64.sigstore.json') { throw "The bundle was not passed: $blobArgs" }
    if ($blobArgs[[array]::IndexOf($blobArgs, '--certificate-identity-regexp') + 1] -cne $regexp -or $blobArgs[[array]::IndexOf($blobArgs, '--certificate-oidc-issuer') + 1] -cne $issuer) { throw "Blob mode must use the image identity and issuer: $blobArgs" }
    # Sums mode: no cosign; the file must be listed exactly once with its hash.
    $binary = Join-Path $testRoot 'suiteward_v1.2.3_linux_amd64'
    Set-Content -LiteralPath $binary -Value 'binary' -NoNewline
    $hash = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
    $sums = Join-Path $testRoot 'SHA256SUMS'
    Set-Content -LiteralPath $sums -Value "$('b' * 64)  other`n$hash  suiteward_v1.2.3_linux_amd64"
    Remove-Item -LiteralPath $log
    $sumsOutput = & $verify -Sums $sums -File $binary *>&1 | Out-String
    if (Test-Path -LiteralPath $log) { throw 'Sums mode ran cosign.' }
    # The host compares the hash of SHA256SUMS itself with the one the signature covers: it is printed, in this exact shape.
    $sumsHash = (Get-FileHash -LiteralPath $sums -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($sumsOutput -notmatch "(?m)^SHA256SUMS sha256: $sumsHash\r?$") { throw "Sums mode must print 'SHA256SUMS sha256: <hash of the file>': $sumsOutput" }
    # Blob mode prints it too when the signed blob is SHA256SUMS, and only then.
    $blobOutput = & $verify -Blob $sums -Bundle "$sums.sigstore.json" -Cosign $fake *>&1 | Out-String
    if ($blobOutput -notmatch "(?m)^SHA256SUMS sha256: $sumsHash\r?$") { throw "Blob mode must print the hash of a signed SHA256SUMS: $blobOutput" }
    $otherOutput = & $verify -Blob $binary -Bundle "$binary.sigstore.json" -Cosign $fake *>&1 | Out-String
    if ($otherOutput -match 'SHA256SUMS sha256') { throw 'Blob mode printed a SHA256SUMS hash for another file.' }
    Remove-Item -LiteralPath $log
    Set-Content -LiteralPath $sums -Value "$('b' * 64)  suiteward_v1.2.3_linux_amd64"
    Assert-Rejected { & $verify -Sums $sums -File $binary } 'does not match'
    Set-Content -LiteralPath $sums -Value "$hash  other"
    Assert-Rejected { & $verify -Sums $sums -File $binary } 'exactly once'
    Set-Content -LiteralPath $sums -Value "$hash  suiteward_v1.2.3_linux_amd64`n$hash  suiteward_v1.2.3_linux_amd64"
    Assert-Rejected { & $verify -Sums $sums -File $binary } 'exactly once'
    # Every tag the check accepts with a hyphen (-rc.N, -alpha.N) is a pre-release: it must not need main or move latest-stable.
    if ((Get-Content -LiteralPath (Join-Path $PSScriptRoot '../.github/workflows/release.yml') -Raw) -notmatch '(?m)^\s+if \[\[ "\$TAG" == \*-\* \]\]; then\r?\n\s+echo "prerelease=true"') { throw 'release.yml must treat every hyphenated tag as a pre-release.' }
    $env:FAKE_COSIGN_EXIT = '1'
    Assert-Rejected { & $verify -Blob 'f' -Bundle 'f.sigstore.json' -Cosign $fake } 'could not verify'
    Assert-Rejected { & $verify -Reference $digest -Cosign $fake } 'could not verify'
    Assert-Rejected { & $verify -Reference $digest -Cosign (Join-Path $testRoot 'missing') } 'was not found'
    Write-Host 'Release verification checks passed.'
} finally { Remove-Item Env:FAKE_COSIGN_EXIT -ErrorAction SilentlyContinue; Remove-Item -LiteralPath $testRoot -Recurse -Force }
