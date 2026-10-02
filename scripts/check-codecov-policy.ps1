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
    if ($Object -isnot [pscustomobject] -or $null -eq $Object.PSObject.Properties[$Name] -or $null -eq $Object.$Name) {
        throw "Codecov report is missing '$Name'."
    }
    # Preserve arrays so the schema checks can reject them. PowerShell's
    # pipeline would otherwise unwrap a single-element array into a scalar.
    return ,$Object.$Name
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
    $commit = Get-CodecovField $Report 'commitid'
    $state = Get-CodecovField $Report 'state'
    if ($commit -isnot [string] -or $commit -cne $Revision) { throw 'Codecov report commit does not match the requested revision.' }
    if ($state -isnot [string] -or $state -cne 'complete') { throw 'Codecov report is not complete.' }
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
    . (Join-Path $PSScriptRoot 'ci.ps1') -Mode Library
    if ($EventName -ceq 'workflow_dispatch') {
        Assert-CodecovRevision $EventRevision
        if ($CurrentRevision -cne $EventRevision) { throw 'Manual coverage checkout does not match the event revision.' }
    }
    $range = Get-TddRevisionRange -EventName $EventName -Event $Event -CurrentRevision $CurrentRevision -ParentRevision $ParentRevision
    if ($range.Base -ceq $range.Head) { throw 'Codecov comparison requires distinct base and head commits.' }
    return $range
}

function Wait-CodecovReport {
    param([string]$Revision, [scriptblock]$Fetch, [scriptblock]$Pause)
    Assert-CodecovRevision $Revision
    if ($null -eq $Fetch) {
        $Fetch = {
            param($uri)
            Invoke-WebRequest -Uri $uri -Method Get -Headers @{ Accept = 'application/json' } -SkipHttpErrorCheck -MaximumRedirection 0 -TimeoutSec 15
        }
    }
    if ($null -eq $Pause) { $Pause = { param($seconds) Start-Sleep -Seconds $seconds } }
    $uri = "https://api.codecov.io/api/v2/github/IgnisDevNE/repos/SuiteWard/commits/$Revision/"
    $lastReason = 'report unavailable'
    for ($attempt = 1; $attempt -le 12; $attempt++) {
        $response = $null
        try { $response = & $Fetch $uri } catch {
            $transient = $false
            $cause = $_.Exception
            while ($null -ne $cause) {
                if ($cause -is [Net.Http.HttpRequestException] -or $cause -is [OperationCanceledException] -or $cause -is [TimeoutException]) { $transient = $true }
                $cause = $cause.InnerException
            }
            if (-not $transient) { throw }
            $lastReason = 'transport failure or timeout'
        }
        if ($null -ne $response) {
            $status = $response.StatusCode
            if ($status -eq 200) {
                if ($response.Content -isnot [string]) { throw 'Codecov HTTP body must be JSON text.' }
                $body = ConvertFrom-Json -InputObject $response.Content -Depth 32 -NoEnumerate
                $commit = Get-CodecovField $body 'commitid'
                $state = Get-CodecovField $body 'state'
                if ($commit -isnot [string] -or $commit -cne $Revision) { throw 'Codecov response belongs to a different commit.' }
                if ($state -isnot [string]) { throw 'Codecov response has a malformed state.' }
                if ($state -ceq 'complete') {
                    ConvertFrom-CodecovReport $body $Revision | Out-Null
                    return $body
                }
                if ($state -cne 'pending') { throw "Codecov report has terminal or unsupported state '$state'." }
                $lastReason = 'report is pending'
            } elseif ($status -in @(404, 408, 429) -or ($status -ge 500 -and $status -le 599)) {
                $lastReason = "HTTP $status"
            } else {
                throw "Codecov report request failed with terminal HTTP $status."
            }
        }
        if ($attempt -lt 12) { & $Pause 10 }
    }
    throw "Codecov report $Revision unavailable after 12 attempts ($lastReason)."
}

function Invoke-CodecovProjectPolicy {
    param([string]$Repository, [string]$BaseRevision, [string]$HeadRevision, [scriptblock]$Fetch, [scriptblock]$Pause)
    if ($Repository -cne 'IgnisDevNE/SuiteWard') { throw 'Codecov policy is scoped to IgnisDevNE/SuiteWard.' }
    Assert-CodecovRevision $BaseRevision
    Assert-CodecovRevision $HeadRevision
    if ($BaseRevision -ceq $HeadRevision) { throw 'Codecov comparison requires distinct base and head commits.' }
    $baseReport = Wait-CodecovReport $BaseRevision $Fetch $Pause
    $headReport = Wait-CodecovReport $HeadRevision $Fetch $Pause
    return Assert-CodecovProjectPolicy $baseReport $headReport $BaseRevision $HeadRevision
}

if ($Mode -eq 'Library') { return }
Push-Location -LiteralPath $Root
try {
    if (-not $env:GITHUB_EVENT_PATH) { throw 'Coverage enforcement requires a GitHub event payload.' }
    $event = Get-Content -LiteralPath $env:GITHUB_EVENT_PATH -Raw | ConvertFrom-Json
    $revisions = (git rev-list --parents -n 1 HEAD) -split ' '
    if ($LASTEXITCODE -ne 0 -or $revisions.Count -lt 2) { throw 'Cannot identify the coverage checkout and its parent.' }
    $range = Get-CodecovRevisionRange -EventName $env:GITHUB_EVENT_NAME -Event $event -CurrentRevision $revisions[0] -ParentRevision $revisions[1] -EventRevision $env:GITHUB_SHA
    if ($revisions[0] -cne $range.Head) { throw 'Coverage checkout does not match the exact event head.' }
    $result = Invoke-CodecovProjectPolicy -Repository $env:GITHUB_REPOSITORY -BaseRevision $range.Base -HeadRevision $range.Head
    $message = "Project coverage policy passed: base $($result.Base.Revision) has $($result.Base.Hits)/$($result.Base.Lines) covered lines; head $($result.Head.Revision) has $($result.Head.Hits)/$($result.Head.Lines). Exact fractions satisfy the 99% floor and maximum 0.25-point decrease."
    Write-Output $message
    if ($env:GITHUB_STEP_SUMMARY) { $message | Out-File -LiteralPath $env:GITHUB_STEP_SUMMARY -Append -Encoding utf8 }
} finally { Pop-Location }
