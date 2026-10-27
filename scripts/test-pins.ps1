# Proves that the pin checker catches a digest copy that disagrees, a tag that drifted, an unpinned base image and a Go version mismatch. Offline.
#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$testRoot = Join-Path ([IO.Path]::GetTempPath()) "suiteward-pins-$([Guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $testRoot | Out-Null
$a = 'a' * 64
$b = 'b' * 64
function Write-Fixture {
    param([string]$Copy = "docker.io/library/postgres:18.6-trixie@sha256:$a", [string]$From = "golang:`${GO_VERSION}-alpine@sha256:$a", [string]$GoArg = '1.2.3')
    foreach ($dir in 'dev', 'deploy') { New-Item -ItemType Directory -Path (Join-Path $testRoot $dir) -Force | Out-Null }
    Set-Content -LiteralPath (Join-Path $testRoot '.go-version') -Value '1.2.3'
    Set-Content -LiteralPath (Join-Path $testRoot 'dev/tools.json') -Value "{`"postgres`":{`"version`":`"18.6`",`"image`":`"docker.io/library/postgres:18.6-trixie@sha256:$a`"}}"
    Set-Content -LiteralPath (Join-Path $testRoot 'deploy/Containerfile') -Value "ARG GO_VERSION=$GoArg`nFROM --platform=`$BUILDPLATFORM $From AS build`nFROM gcr.io/distroless/static:nonroot@sha256:$a`nCOPY --from=build /x /x`n"
    Set-Content -LiteralPath (Join-Path $testRoot 'copy.txt') -Value "image: $Copy"
}
function Assert-Pins {
    param([string]$Name, [bool]$Pass)
    $accepted = $true
    try { & (Join-Path $PSScriptRoot 'check-pins.ps1') -Root $testRoot *>$null } catch { $accepted = $false }
    if ($accepted -ne $Pass) { throw "Pin checker changed: $Name; expected pass=$Pass." }
}
try {
    git -C $testRoot init -q
    if ($LASTEXITCODE -ne 0) { throw 'Cannot create the fixture repository.' }
    Write-Fixture
    Assert-Pins 'agreeing copies' $true
    New-Item -ItemType File -Path (Join-Path $testRoot 'empty.json') | Out-Null
    Assert-Pins 'an empty file in the repository' $true
    Remove-Item -LiteralPath (Join-Path $testRoot 'empty.json')
    Write-Fixture -Copy "postgres:18.6-trixie@sha256:$b"
    Assert-Pins 'a copy with another digest' $false
    Write-Fixture -Copy "postgres:18.7-trixie@sha256:$a"
    Assert-Pins 'a copy with another tag' $false
    Write-Fixture -Copy "docker.io/library/golang:1.2.3-alpine@sha256:$a"
    Assert-Pins 'a copy of the Go image that writes the version the ARG default gives' $true
    Write-Fixture -Copy "docker.io/library/golang:1.2.4-alpine@sha256:$a"
    Assert-Pins 'a copy of the Go image with another version' $false
    Write-Fixture -From 'golang:${GO_VERSION}-alpine'
    Assert-Pins 'an unpinned base image' $false
    Write-Fixture -GoArg '9.9.9'
    Assert-Pins 'a Go version that differs from .go-version' $false
    Write-Fixture
    Remove-Item -LiteralPath (Join-Path $testRoot 'deploy/Containerfile')
    Assert-Pins 'a missing Containerfile' $false
    Write-Host 'Pin checker checks passed.'
} finally { Remove-Item -LiteralPath $testRoot -Recurse -Force }
