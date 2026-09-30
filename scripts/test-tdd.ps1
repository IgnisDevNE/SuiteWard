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

function New-EvidenceRevision {
    param([scriptblock]$Change)
    Invoke-FixtureGit @('switch', '--detach', $green) | Out-Null
    $candidate = $evidenceJson | ConvertFrom-Json -AsHashtable
    if ($Change) { & $Change $candidate }
    Write-FixtureFile 'docs/plan/executions/P0.json' ($candidate | ConvertTo-Json -Depth 30)
    return New-FixtureCommit 'fixture: execution evidence'
}

function Test-EvidenceMutation {
    param([scriptblock]$Change, [string]$Message)
    $candidateHead = New-EvidenceRevision $Change
    Assert-TddRejection { Invoke-TddCheck -RepoRoot $repo -BaseRevision $base -HeadRevision $candidateHead } $Message
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

    # RED and GREEN are ordinary reachable commits, not timestamps or branch names.
    Write-FixtureFile 'sample.tests.ps1' 'if ((./sample.ps1) -ne 3) { throw "Expected 3, got 2" }'
    $red = New-FixtureCommit 'fixture: failing behavior assertion'
    Write-FixtureFile 'sample.ps1' 'return 3'
    Write-FixtureFile 'docs/guide.md' '# Fixture guide'
    $green = New-FixtureCommit 'fixture: satisfy behavior assertion'
    Invoke-FixtureGit @('switch', '--detach', $base) | Out-Null
    Write-FixtureFile 'unrelated.txt' 'unrelated branch'
    $detached = New-FixtureCommit 'fixture: unrelated revision'
    $blob = Invoke-FixtureGit @('rev-parse', "${base}:sample.ps1")

    $evidence = @{
        schema_version = 1
        phase = 'P0'
        tasks = @(
            @{
                task_id = 'P0-A'
                mode = 'required'
                files = @('sample.ps1', 'sample.tests.ps1')
                cycles = @(@{
                    scenario = 'Return the required value.'
                    red = @{ revision = $red; command = 'pwsh -NoProfile -File ./sample.tests.ps1'; exit_code = 1; output = 'Expected 3, got 2' }
                    green = @{ revision = $green; command = 'pwsh -NoProfile -File ./sample.tests.ps1'; exit_code = 0; output = 'Behavior assertion passed.' }
                    refactor = 'No further refactoring was needed.'
                })
                review = @{ author = 'implementer'; reviewer = 'reviewer'; outcome = 'accepted'; notes = 'Reviewed the failure relevance and both results.' }
            },
            @{
                task_id = 'P0-B'
                mode = 'not_applicable'
                reason = 'Documentation and the reviewed evidence record only.'
                files = @('docs/guide.md', 'docs/plan/executions/P0.json')
                cycles = @()
                review = @{ author = 'integrator'; reviewer = 'reviewer'; outcome = 'accepted'; notes = 'Documentation-only scope confirmed.' }
            }
        )
    }
    $evidenceJson = $evidence | ConvertTo-Json -Depth 30
    $valid = New-EvidenceRevision {}
    Invoke-TddCheck -RepoRoot $repo -BaseRevision $base -HeadRevision $valid
    $checks++

    Assert-TddRejection { Invoke-TddCheck -RepoRoot $repo -HeadRevision $valid } 'BaseRevision is required'
    $checks++
    Test-EvidenceMutation { param($e) $e.schema_version = 2 } 'schema_version must be 1'
    $checks++
    Test-EvidenceMutation { param($e) $e.phase = 'unknown' } 'unknown phase'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks = @($e.tasks[0]) } 'every phase task exactly once'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks += $e.tasks[0] } 'every phase task exactly once'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].mode = 'historical' } 'historical evidence is not allowed'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].mode = 'not_applicable'; $e.tasks[0].reason = 'Skip tests'; $e.tasks[0].cycles = @() } 'does not match the plan'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[1].files += 'sample.ps1'; $e.tasks[0].files = @('sample.tests.ps1') } 'not_applicable cannot cover executable'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[1].reason = ' ' } 'reason must be a nonempty string'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles = @() } 'requires at least one cycle'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[1].cycles = @($e.tasks[0].cycles[0]) } 'not_applicable cycles must be empty'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles[0].red.output = '' } 'red.output must be a nonempty string'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles[0].red.exit_code = 0 } 'RED exit_code must be a nonzero integer'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles[0].red.exit_code = 1.5 } 'RED exit_code must be a nonzero integer'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles[0].green.exit_code = 1 } 'GREEN exit_code must be zero'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles[0].green.command = 'another command' } 'same command'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles[0].refactor = '' } 'refactor must be a nonempty string'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles[0].red.revision = $green } 'RED and GREEN must be distinct'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles[0].red.revision = 'HEAD' } 'full 40-character commit SHA'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles[0].red.revision = ('a' * 40) } 'must name a local commit object'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles[0].red.revision = $blob } 'must name a local commit object'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles[0].red.revision = $detached } 'RED must be an ancestor of GREEN'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles[0].red.revision = $green; $e.tasks[0].cycles[0].green.revision = $red } 'RED must be an ancestor of GREEN'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].cycles[0].red.revision = $base; $e.tasks[0].cycles[0].green.revision = $detached } 'GREEN must be an ancestor of the checked head'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].files = @('sample.ps1') } 'uncovered changed file'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].files += 'not-changed.ps1' } 'is not a changed file'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].files += 'sample.ps1' } 'covered more than once'
    $checks++
    foreach ($path in @('../outside', '/absolute', 'C:/outside', 'docs\guide.md', 'docs/./guide.md', 'docs//guide.md')) {
        Test-EvidenceMutation { param($e) $e.tasks[0].files += $path } 'unsafe or non-normalized path'
        $checks++
    }
    Test-EvidenceMutation { param($e) $e.tasks[0].review.reviewer = ' IMPLEMENTER ' } 'independent reviewer'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].review.outcome = 'pending' } 'review outcome must be accepted'
    $checks++
    Test-EvidenceMutation { param($e) $e.tasks[0].review.notes = '' } 'review.notes must be a nonempty string'
    $checks++

    # Recorded strings are data. Never run an evidence command or read the
    # working-tree manifest in place of the exact checked head.
    $inert = New-EvidenceRevision {
        param($e)
        $e.tasks[0].cycles[0].red.command = 'throw "Evidence command was executed"'
        $e.tasks[0].cycles[0].green.command = 'throw "Evidence command was executed"'
    }
    Invoke-TddCheck -RepoRoot $repo -BaseRevision $base -HeadRevision $inert
    $checks++
    Write-FixtureFile 'docs/plan/executions/P0.json' '{}'
    Write-FixtureFile 'docs/plan/backlog.json' '{}'
    Invoke-TddCheck -RepoRoot $repo -BaseRevision $base -HeadRevision $valid
    $checks++
    Invoke-FixtureGit @('restore', '--worktree', '--', 'docs/plan/executions/P0.json', 'docs/plan/backlog.json') | Out-Null

    $stale = New-EvidenceRevision {}
    Write-FixtureFile 'sample.ps1' 'return 4'
    $stale = New-FixtureCommit 'fixture: untested implementation change'
    Assert-TddRejection { Invoke-TddCheck -RepoRoot $repo -BaseRevision $base -HeadRevision $stale } 'changed after its final GREEN'
    $checks++
    $stale = New-EvidenceRevision {}
    Write-FixtureFile 'sample.tests.ps1' 'throw "Changed assertion"'
    $stale = New-FixtureCommit 'fixture: untested test change'
    Assert-TddRejection { Invoke-TddCheck -RepoRoot $repo -BaseRevision $base -HeadRevision $stale } 'changed after its final GREEN'
    $checks++

    $multiple = New-EvidenceRevision {}
    Write-FixtureFile 'docs/plan/executions/other.json' '{}'
    $multiple = New-FixtureCommit 'fixture: second execution manifest'
    Assert-TddRejection { Invoke-TddCheck -RepoRoot $repo -BaseRevision $base -HeadRevision $multiple } 'exactly one execution manifest'
    $checks++

    Invoke-FixtureGit @('switch', '--detach', $valid) | Out-Null
    Remove-Item -LiteralPath (Join-Path $repo 'docs/plan/executions/P0.json')
    $deleted = New-FixtureCommit 'fixture: delete execution history'
    Assert-TddRejection { Invoke-TddCheck -RepoRoot $repo -BaseRevision $valid -HeadRevision $deleted } 'cannot delete execution evidence'
    $checks++

    # A documentation-only phase does not invent a RED/GREEN cycle.
    Invoke-FixtureGit @('switch', '--detach', $base) | Out-Null
    Write-FixtureFile 'docs/plan/backlog.json' '{"phases":[{"id":"P0"}],"tasks":[{"id":"P0-B","phase":"P0","tdd":{"mode":"not_applicable"}}]}'
    Write-FixtureFile 'docs/guide.markdown' '# Documentation-only change'
    $docsOnly = @{ schema_version = 1; phase = 'P0'; tasks = @($evidence.tasks[1].Clone()) }
    $docsOnly.tasks[0].files = @('docs/guide.markdown', 'docs/plan/backlog.json', 'docs/plan/executions/P0.json')
    Write-FixtureFile 'docs/plan/executions/P0.json' ($docsOnly | ConvertTo-Json -Depth 30)
    $docsHead = New-FixtureCommit 'fixture: documented non-applicability'
    Invoke-TddCheck -RepoRoot $repo -BaseRevision $base -HeadRevision $docsHead
    $checks++

    Write-Host "Passed $checks TDD evidence checks. No application coverage is produced."
} finally {
    $resolved = [IO.Path]::GetFullPath($scratch)
    $allowed = $scratchBase.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    $comparison = if ($IsWindows) { [StringComparison]::OrdinalIgnoreCase } else { [StringComparison]::Ordinal }
    if (-not $resolved.StartsWith($allowed, $comparison) -or [IO.Path]::GetFileName($resolved) -notmatch '^[a-f0-9]{32}$') { throw 'Refusing cleanup outside the TDD-test scratch directory.' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
