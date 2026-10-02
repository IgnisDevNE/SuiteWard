param(
    [ValidateSet('Check', 'Library')]
    [string]$Mode = 'Check',
    [string]$Root = (Split-Path $PSScriptRoot -Parent)
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-CodecovRevision {
    param([string]$Revision)
    if ($Revision -cnotmatch '^[a-f0-9]{40}$' -or $Revision -match '^0+$') {
        throw 'Codecov policy requires an exact, nonzero 40-character commit ID.'
    }
}

function Get-CodecovField {
    param($Object, [string]$Name)
    if ($null -eq $Object -or $null -eq $Object.PSObject.Properties[$Name] -or $null -eq $Object.$Name) {
        throw "Codecov report is missing '$Name'."
    }
    return $Object.$Name
}

function ConvertTo-CodecovCount {
    param($Value)
    if ($null -eq $Value -or $Value.GetType().FullName -cnotin @(
        'System.Byte', 'System.SByte', 'System.Int16', 'System.UInt16',
        'System.Int32', 'System.UInt32', 'System.Int64', 'System.UInt64', 'System.Numerics.BigInteger'
    )) {
        throw 'Codecov line counts must be integers, not strings, booleans, or fractional values.'
    }
    $count = [bigint]$Value
    if ($count -lt 0) { throw 'Codecov line counts cannot be negative.' }
    return $count
}

function ConvertFrom-CodecovReport {
    param($Report, [string]$Revision)
    Assert-CodecovRevision $Revision
    if ((Get-CodecovField $Report 'commitid') -cne $Revision) { throw 'Codecov report commit does not match the requested revision.' }
    if ((Get-CodecovField $Report 'state') -cne 'complete') { throw 'Codecov report is not complete.' }
    $top = Get-CodecovField $Report 'totals'
    $nested = Get-CodecovField (Get-CodecovField $Report 'report') 'totals'
    $counts = @{}
    foreach ($name in @('lines', 'hits', 'misses', 'partials')) {
        $counts[$name] = ConvertTo-CodecovCount (Get-CodecovField $top $name)
        $nestedCount = ConvertTo-CodecovCount (Get-CodecovField $nested $name)
        if ($counts[$name] -ne $nestedCount) { throw "Codecov top-level and report '$name' disagree." }
    }
    if ($counts.lines -le 0 -or $counts.hits + $counts.misses + $counts.partials -ne $counts.lines) {
        throw 'Codecov line counts must have a positive denominator and consistent hit/miss/partial totals.'
    }
    return [pscustomobject]@{
        Revision = $Revision; Lines = $counts.lines; Hits = $counts.hits
        Misses = $counts.misses; Partials = $counts.partials
    }
}

function Assert-CodecovProjectPolicy {
    param($BaseReport, $HeadReport, [string]$BaseRevision, [string]$HeadRevision)
    if ($BaseRevision -ceq $HeadRevision) { throw 'Codecov comparison requires distinct base and head commits.' }
    $base = ConvertFrom-CodecovReport $BaseReport $BaseRevision
    $head = ConvertFrom-CodecovReport $HeadReport $HeadRevision
    # Exact cross-products avoid both overflow and display-percentage rounding.
    # Partial lines are in the denominator, never credited as fully covered hits.
    if (100 * $head.Hits -lt 99 * $head.Lines) { throw 'Project coverage is below the absolute 99% floor.' }
    if (400 * ($base.Hits * $head.Lines - $head.Hits * $base.Lines) -gt $base.Lines * $head.Lines) {
        throw 'Project coverage decreased by more than 0.25 percentage points.'
    }
    return [pscustomobject]@{ Base = $base; Head = $head; Passed = $true }
}

function Get-CodecovRevisionRange {
    param([string]$EventName, $Event, [string]$CurrentRevision, [string]$ParentRevision, [string]$EventRevision)
    return $null
}

function Wait-CodecovReport {
    param([string]$Revision, [scriptblock]$Fetch, [scriptblock]$Pause)
    return $null
}

function Invoke-CodecovProjectPolicy {
    param([string]$Repository, [string]$BaseRevision, [string]$HeadRevision, [scriptblock]$Fetch, [scriptblock]$Pause)
    return $null
}

if ($Mode -eq 'Library') { return }
throw 'The Codecov transport adapter is not implemented yet.'
