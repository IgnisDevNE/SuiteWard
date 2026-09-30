#Requires -Version 7.2
[CmdletBinding()]
param([string]$PlanPath = (Join-Path $PSScriptRoot '../docs/plan/backlog.json'))

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'check-plan.ps1') -PlanPath $PlanPath
$source = Get-Content -LiteralPath $PlanPath -Raw
$baseline = $source | ConvertFrom-Json -AsHashtable
Assert-Plan $baseline
if (@($baseline['tasks']).Count -lt 2) { throw 'Dependency regression checks need at least two real tasks.' }

$scratchBase = [IO.Path]::GetFullPath((Join-Path (Split-Path $PSScriptRoot -Parent) '.cache/plan-tests'))
$scratch = Join-Path $scratchBase ([Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $scratch -Force | Out-Null
$checks = 0

function Assert-PlanRejection {
    param([scriptblock]$Action, [string]$Message)
    $rejected = $false
    try { & $Action | Out-Null } catch {
        if ($_.Exception.Message -notlike "*$Message*") { throw }
        $rejected = $true
    }
    if (-not $rejected) { throw "Expected plan rejection containing '$Message'." }
}

function Test-PlanMutation {
    param([scriptblock]$Change, [string]$Message)
    $candidate = $source | ConvertFrom-Json -AsHashtable
    & $Change $candidate
    Assert-PlanRejection { Assert-Plan $candidate } $Message
}

try {
    Test-PlanMutation { param($p) $p.Remove('tdd_policy') } 'tdd_policy must be an object'
    $checks++
    Test-PlanMutation { param($p) $p['tdd_policy'] = 'optional' } 'tdd_policy must be an object'
    $checks++
    Test-PlanMutation { param($p) $p['tdd_policy']['document'] = 'docs/other-policy.md' } 'tdd_policy.document must be docs/tdd.md'
    $checks++
    foreach ($historicalPhases in @(@(), @('F0', 'M0.01'), @('M0.01'))) {
        Test-PlanMutation { param($p) $p['tdd_policy']['historical_phases'] = $historicalPhases } 'tdd_policy.historical_phases must contain only F0'
        $checks++
    }
    Test-PlanMutation { param($p) $p['tasks'][0].Remove('tdd') } 'tdd must be an object'
    $checks++
    Test-PlanMutation { param($p) $p['tasks'][0]['tdd'] = 'required' } 'tdd must be an object'
    $checks++
    foreach ($mode in @($null, '', 'optional', 'Required')) {
        Test-PlanMutation { param($p) $p['tasks'][0]['tdd']['mode'] = $mode } 'tdd.mode is invalid'
        $checks++
    }
    foreach ($reason in @($null, '', '   ', 42)) {
        Test-PlanMutation { param($p) $p['tasks'][0]['tdd']['reason'] = $reason } 'tdd.reason must be a nonempty string'
        $checks++
    }
    $futureTaskID = @($baseline['tasks'] | Where-Object { $_['phase'] -cne 'F0' -and $_['kind'] -ceq 'implementation' })[0]['id']
    Test-PlanMutation {
        param($p)
        $task = @($p['tasks'] | Where-Object { $_['id'] -ceq $futureTaskID })[0]
        $task['tdd'] = @{ mode = 'historical'; reason = 'Pretend a future implementation predates adoption.' }
    } 'historical TDD is restricted to completed phase F0'
    $checks++
    foreach ($behaviorPath in @('internal/core/value.go', 'scripts/check.ps1', '.github/workflows/ci.yml', 'docs/example.ps1', 'docs/settings.json')) {
        Test-PlanMutation {
            param($p)
            $task = @($p['tasks'] | Where-Object { $_['id'] -ceq $futureTaskID })[0]
            $task['tdd'] = @{ mode = 'not_applicable'; reason = 'Claim behavior has no TDD obligation.' }
            $task['owns'] = @($behaviorPath)
            $task['shared_files'] = @()
        } 'not_applicable implementation tasks must own only documentation'
        $checks++
    }
    Test-PlanMutation {
        param($p)
        $task = @($p['tasks'] | Where-Object { $_['id'] -ceq $futureTaskID })[0]
        $task['tdd'] = @{ mode = 'not_applicable'; reason = 'Hide behavior behind a documentation task.' }
        $task['owns'] = @('docs/review.md')
        $task['shared_files'] = @('scripts/check.ps1')
    } 'not_applicable implementation tasks must own only documentation'
    $checks++
    foreach ($kind in @('contract', 'integration', 'implementation')) {
        $candidate = $source | ConvertFrom-Json -AsHashtable
        $task = @($candidate['tasks'] | Where-Object { $_['id'] -ceq $futureTaskID })[0]
        $task['kind'] = $kind
        $task['tdd'] = @{ mode = 'not_applicable'; reason = 'Reviewed contract, aggregation, or documentation only.' }
        $task['owns'] = @('docs/plan/review.md', 'README.md', 'notes.markdown', 'docs/plan/backlog.json', 'docs/plan/executions/F0.01.json')
        $task['shared_files'] = @('AGENTS.md')
        Assert-Plan $candidate
        $task['tdd']['mode'] = 'required'
        Assert-Plan $candidate
        $checks++
    }
    Test-PlanMutation { param($p) $p['tasks'] += $p['tasks'][0].Clone() } 'Duplicate tasks ID'
    $checks++
    Test-PlanMutation { param($p) $p['tasks'][0]['needs_to_start'] = @('missing-' + [Guid]::NewGuid().ToString('N')) } 'references unknown ID'
    $checks++
    Test-PlanMutation {
        param($p)
        $p['tasks'][0]['needs_to_start'] = @($p['tasks'][1]['id'])
        $p['tasks'][1]['needs_to_start'] = @($p['tasks'][0]['id'])
    } 'dependency cycle'
    $checks++
    Test-PlanMutation {
        param($p)
        $p['tasks'][0]['needs_to_start'] = @($p['tasks'][1]['id'])
        $p['tasks'][1]['needs_to_start'] = @()
        $p['tasks'][1]['needs_to_merge'] = @($p['tasks'][0]['id'])
    } 'dependency cycle'
    $checks++
    Test-PlanMutation { param($p) $p['tasks'][0]['needs_to_start'] = @($p['tasks'][0]['id']) } 'self dependency'
    $checks++
    Test-PlanMutation { param($p) $p['tasks'][0]['acceptance'] = @() } 'acceptance must not be empty'
    $checks++
    Test-PlanMutation { param($p) $p['tasks'][0]['verification'] = @() } 'verification must not be empty'
    $checks++
    Test-PlanMutation { param($p) $p['tasks'][0]['owns'] = @() } 'owns must not be empty'
    $checks++
    foreach ($unsafe in @('../outside', '/absolute', 'C:\outside', 'docs/../../outside')) {
        Test-PlanMutation { param($p) $p['tasks'][0]['owns'] = @($unsafe) } 'unsafe ownership path'
        $checks++
    }
    if (@($baseline['phases']).Count -ge 2) {
        Test-PlanMutation {
            param($p)
            $p['phases'][0]['merge_after'] = @($p['phases'][1]['id'])
            $p['phases'][1]['merge_after'] = @($p['phases'][0]['id'])
        } 'dependency cycle'
        $checks++
    }
    if (@($baseline['contracts']).Count -gt 0) {
        Test-PlanMutation { param($p) $p['contracts'][0]['freeze_task'] = 'missing-' + [Guid]::NewGuid().ToString('N') } 'freeze_task references unknown ID'
        $checks++
        Test-PlanMutation {
            param($p)
            $contract = $p['contracts'][0]
            $owner = @($p['tasks'] | Where-Object { $_['id'] -ceq $contract['freeze_task'] })[0]
            $owner['contracts'] = @($owner['contracts'] | Where-Object { $_ -cne $contract['id'] })
        } 'contract ownership'
        $checks++
    }

    $copy = Join-Path $scratch 'backlog.json'
    [IO.File]::WriteAllText($copy, $source, [Text.UTF8Encoding]::new($false))
    Invoke-PlanCheck $copy -WriteDocs
    Invoke-PlanCheck $copy
    $checks++
    foreach ($phase in $baseline['phases']) {
        $rendered = Get-PhaseDocument $baseline $phase
        if (-not $rendered.Contains('[TDD policy](../../tdd.md)')) { throw "Phase $($phase['id']) omits its TDD policy link." }
        foreach ($task in $baseline['tasks'] | Where-Object { $_['phase'] -ceq $phase['id'] }) {
            $tddLine = "TDD: **$($task['tdd']['mode'])**. $($task['tdd']['reason'])"
            $taskHeading = "### $($task['id']) — $($task['title'])"
            $taskStart = $rendered.IndexOf($taskHeading, [StringComparison]::Ordinal)
            if ($taskStart -lt 0) { throw "Phase $($phase['id']) omits task $($task['id'])." }
            $taskEnd = $rendered.IndexOf("`n### ", $taskStart + $taskHeading.Length, [StringComparison]::Ordinal)
            $taskDocument = if ($taskEnd -lt 0) { $rendered.Substring($taskStart) } else { $rendered.Substring($taskStart, $taskEnd - $taskStart) }
            if (-not $taskDocument.Contains($tddLine)) { throw "Phase $($phase['id']) omits TDD mode/reason for task $($task['id'])." }
        }
    }
    $checks++
    $document = Join-Path $scratch "phases/$($baseline['phases'][0]['id']).md"
    $expected = [IO.File]::ReadAllText($document)
    [IO.File]::WriteAllText($document, $expected.Replace("`n", "`r`n"), [Text.UTF8Encoding]::new($false))
    Invoke-PlanCheck $copy
    $checks++
    [IO.File]::WriteAllText($document, $expected + "`nUnreviewed stale text.`n", [Text.UTF8Encoding]::new($false))
    Assert-PlanRejection { Invoke-PlanCheck $copy } 'Stale or missing phase document'
    $checks++
    Invoke-PlanCheck $copy -WriteDocs
    Remove-Item -LiteralPath $document
    Assert-PlanRejection { Invoke-PlanCheck $copy } 'Stale or missing phase document'
    $checks++
    Write-Host "Passed $checks planning integrity checks. No application coverage is produced."
} finally {
    $resolved = [IO.Path]::GetFullPath($scratch)
    $allowed = $scratchBase.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    $comparison = if ($IsWindows) { [StringComparison]::OrdinalIgnoreCase } else { [StringComparison]::Ordinal }
    if (-not $resolved.StartsWith($allowed, $comparison) -or [IO.Path]::GetFileName($resolved) -notmatch '^[a-f0-9]{32}$') { throw 'Refusing cleanup outside the planning-test scratch directory.' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
