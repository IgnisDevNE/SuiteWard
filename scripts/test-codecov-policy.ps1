$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'check-codecov-policy.ps1') -Mode Library

$checks = 0
$baseRevision = 'a' * 40
$headRevision = 'b' * 40
function Assert-Rejected {
    param([scriptblock]$Action, [string]$Scenario)
    $rejected = $false
    try { & $Action | Out-Null } catch { $rejected = $true }
    if (-not $rejected) { throw "Expected rejection: $Scenario" }
}
function New-Report {
    param([string]$Revision, [long]$Hits, [long]$Lines, [long]$Partials = 0)
    return [pscustomobject]@{
        commitid = $Revision
        state = 'complete'
        ci_passed = $false
        totals = [pscustomobject]@{ lines = $Lines; hits = $Hits; misses = $Lines - $Hits - $Partials; partials = $Partials; coverage = 100; diff = 0 }
        report = [pscustomobject]@{ totals = [pscustomobject]@{ lines = $Lines; hits = $Hits; misses = $Lines - $Hits - $Partials; partials = $Partials; coverage = 100; diff = @(1, 2); messages = 0 } }
    }
}
function Test-Policy {
    param($Base, $Head)
    Assert-CodecovProjectPolicy -BaseReport $Base -HeadReport $Head -BaseRevision $baseRevision -HeadRevision $headRevision
}

foreach ($case in @(
    @{ Name = 'exactly 0.25 percentage points'; BaseHits = 400; BaseLines = 400; HeadHits = 399; HeadLines = 400; Pass = $true },
    @{ Name = 'drop of 0.250001 despite rounded percentage claiming 100'; BaseHits = 100000000; BaseLines = 100000000; HeadHits = 99749999; HeadLines = 100000000; Pass = $false },
    @{ Name = 'exactly 99 percent'; BaseHits = 99; BaseLines = 100; HeadHits = 99; HeadLines = 100; Pass = $true },
    @{ Name = '98.999 percent below the absolute floor'; BaseHits = 99000; BaseLines = 100000; HeadHits = 98999; HeadLines = 100000; Pass = $false },
    @{ Name = 'improving coverage still below floor'; BaseHits = 980; BaseLines = 1000; HeadHits = 985; HeadLines = 1000; Pass = $false },
    @{ Name = 'large decrease while still above floor'; BaseHits = 1000; BaseLines = 1000; HeadHits = 995; HeadLines = 1000; Pass = $false },
    @{ Name = 'actual M0 hosted line counts'; BaseHits = 642; BaseLines = 644; HeadHits = 821; HeadLines = 825; Pass = $true },
    @{ Name = 'cross-products beyond Int64'; BaseHits = [long]::MaxValue; BaseLines = [long]::MaxValue; HeadHits = [long]::MaxValue; HeadLines = [long]::MaxValue; Pass = $true }
)) {
    $baseReport = New-Report $baseRevision $case.BaseHits $case.BaseLines
    $headReport = New-Report $headRevision $case.HeadHits $case.HeadLines
    if ($case.Pass) {
        Test-Policy $baseReport $headReport | Out-Null
    } else {
        Assert-Rejected { Test-Policy $baseReport $headReport } $case.Name
    }
    $checks++
}

# Partial lines do not count as hits. Extra API fields legitimately differ;
# ci_passed is irrelevant while the current CI is waiting on this check.
Test-Policy (New-Report $baseRevision 99 100 1) (New-Report $headRevision 99 100 1) | Out-Null
$checks++
foreach ($case in @(
    @{ Name = 'different source revision'; Mutate = { param($r) $r.commitid = 'c' * 40 } },
    @{ Name = 'pending report'; Mutate = { param($r) $r.state = 'pending' } },
    @{ Name = 'missing nested report'; Mutate = { param($r) $r.report = $null } },
    @{ Name = 'inconsistent top and nested counts'; Mutate = { param($r) $r.totals.hits = 98 } },
    @{ Name = 'zero lines'; Mutate = { param($r) $r.totals.lines = 0; $r.report.totals.lines = 0 } },
    @{ Name = 'negative counts'; Mutate = { param($r) $r.totals.partials = -1; $r.report.totals.partials = -1 } },
    @{ Name = 'count sum mismatch'; Mutate = { param($r) $r.totals.misses = 2; $r.report.totals.misses = 2 } },
    @{ Name = 'fractional counts'; Mutate = { param($r) $r.totals.hits = 99.5; $r.report.totals.hits = 99.5 } },
    @{ Name = 'numeric strings'; Mutate = { param($r) $r.totals.lines = '100'; $r.report.totals.lines = '100' } },
    @{ Name = 'boolean count'; Mutate = { param($r) $r.totals.partials = $false; $r.report.totals.partials = $false } },
    @{ Name = 'missing count'; Mutate = { param($r) $r.totals.PSObject.Properties.Remove('misses') } },
    @{ Name = 'null count'; Mutate = { param($r) $r.totals.hits = $null; $r.report.totals.hits = $null } }
)) {
    $baseReport = New-Report $baseRevision 99 100
    $headReport = New-Report $headRevision 99 100
    & $case.Mutate $headReport
    Assert-Rejected { Test-Policy $baseReport $headReport } $case.Name
    $checks++
}
Assert-Rejected { Test-Policy $null (New-Report $headRevision 99 100) } 'missing exact baseline'
$checks++
Assert-Rejected { Assert-CodecovProjectPolicy -BaseRevision $headRevision -HeadRevision $headRevision -BaseReport (New-Report $headRevision 100 100) -HeadReport (New-Report $headRevision 100 100) } 'identical base and head'
$checks++
Write-Output "Passed $checks Codecov project policy checks."
