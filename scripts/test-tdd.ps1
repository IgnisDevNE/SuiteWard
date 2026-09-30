#Requires -Version 7.2
[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'check-tdd.ps1')

$scratchBase = [IO.Path]::GetFullPath((Join-Path (Split-Path $PSScriptRoot -Parent) '.cache/tdd-tests'))
$scratch = Join-Path $scratchBase ([Guid]::NewGuid().ToString('N'))
$repo = Join-Path $scratch 'repo'
New-Item -ItemType Directory -Path $repo -Force | Out-Null
$checks = 0

function Invoke-FixtureGit {
    param([string[]]$Arguments)
    $output = & git -C $repo -c core.hooksPath= -c commit.gpgSign=false -c user.name='ignisdevne[bot]' -c user.email='332310975+ignisdevne[bot]@users.noreply.github.com' @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) { throw "Fixture git failed: $($output -join "`n")" }
    return ($output -join "`n").Trim()
}

function Write-FixtureFile {
    param([string]$Path, [string]$Content)
    $destination = Join-Path $repo $Path
    New-Item -ItemType Directory -Path (Split-Path $destination -Parent) -Force | Out-Null
    [IO.File]::WriteAllText($destination, $Content, [Text.UTF8Encoding]::new($false))
}

function New-FixtureCommit {
    param([string]$Message)
    Invoke-FixtureGit @('add', '--all') | Out-Null
    Invoke-FixtureGit @('commit', '--allow-empty', '-m', $Message) | Out-Null
    return Invoke-FixtureGit @('rev-parse', 'HEAD')
}

function Assert-TddRejection {
    param([scriptblock]$Action, [string]$Message)
    $rejected = $false
    try { & $Action | Out-Null } catch {
        if ($_.Exception.Message -notlike "*$Message*") { throw }
        $rejected = $true
    }
    if (-not $rejected) { throw "Expected TDD rejection containing '$Message'." }
}

try {
    Invoke-FixtureGit @('-c', 'init.templateDir=', 'init', '--initial-branch=main') | Out-Null
    Write-FixtureFile 'docs/plan/backlog.json' '{"phases":[{"id":"P0"}],"tasks":[{"id":"P0-A","phase":"P0","tdd":{"mode":"required"}},{"id":"P0-B","phase":"P0","tdd":{"mode":"not_applicable"}}]}'
    Write-FixtureFile 'sample.ps1' 'return 1'
    $base = New-FixtureCommit 'fixture: baseline'
    Write-FixtureFile 'sample.ps1' 'return 2'
    $missingManifest = New-FixtureCommit 'fixture: implementation without evidence'
    Assert-TddRejection { Invoke-TddCheck -RepoRoot $repo -BaseRevision $base -HeadRevision $missingManifest } 'exactly one execution manifest'
    $checks++
    Write-Host "Passed $checks TDD evidence checks. No application coverage is produced."
} finally {
    $resolved = [IO.Path]::GetFullPath($scratch)
    $allowed = $scratchBase.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    $comparison = if ($IsWindows) { [StringComparison]::OrdinalIgnoreCase } else { [StringComparison]::Ordinal }
    if (-not $resolved.StartsWith($allowed, $comparison) -or [IO.Path]::GetFileName($resolved) -notmatch '^[a-f0-9]{32}$') { throw 'Refusing cleanup outside the TDD-test scratch directory.' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
