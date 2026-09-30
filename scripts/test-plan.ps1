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
