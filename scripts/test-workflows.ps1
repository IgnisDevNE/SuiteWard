# Proves that workflow validation covers every workflow file and fails on a broken one. Offline.
#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$testRoot = Join-Path ([IO.Path]::GetTempPath()) "suiteward-workflows-$([Guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $testRoot | Out-Null
try {
    $good = "on: push`njobs:`n  a:`n    runs-on: ubuntu-24.04`n    steps:`n      - run: echo ok`n"
    Set-Content -LiteralPath (Join-Path $testRoot 'good.yml') -Value $good
    & (Join-Path $PSScriptRoot 'check-workflows.ps1') -Directory $testRoot
    # The second file is broken (a job needs a job that does not exist); validating only the first file would miss it.
    Set-Content -LiteralPath (Join-Path $testRoot 'z-broken.yml') -Value ($good + "  b:`n    needs: missing`n    runs-on: ubuntu-24.04`n    steps:`n      - run: echo no`n")
    $rejected = $false
    try { & (Join-Path $PSScriptRoot 'check-workflows.ps1') -Directory $testRoot } catch { $rejected = $true }
    if (-not $rejected) { throw 'A broken workflow was accepted.' }
    & (Join-Path $PSScriptRoot 'check-workflows.ps1')
    Write-Host 'Workflow validation checks passed.'
} finally { Remove-Item -LiteralPath $testRoot -Recurse -Force }
