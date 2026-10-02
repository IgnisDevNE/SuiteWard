param([ValidateSet('Report', 'All')][string]$Scope = 'All')
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
    @{ Name = 'null state is not completed evidence'; Mutate = { param($r) $r.state = $null } },
    @{ Name = 'missing completed-report state'; Mutate = { param($r) $r.PSObject.Properties.Remove('state') } },
    @{ Name = 'missing nested report'; Mutate = { param($r) $r.report = $null } },
    @{ Name = 'inconsistent top and nested counts'; Mutate = { param($r) $r.totals.hits = 98 } },
    @{ Name = 'zero lines'; Mutate = { param($r) $r.totals.lines = 0; $r.report.totals.lines = 0 } },
    @{ Name = 'negative counts'; Mutate = { param($r) $r.totals.partials = -1; $r.report.totals.partials = -1 } },
    @{ Name = 'count sum mismatch'; Mutate = { param($r) $r.totals.misses = 2; $r.report.totals.misses = 2 } },
    @{ Name = 'fractional counts'; Mutate = { param($r) $r.totals.hits = 99.5; $r.report.totals.hits = 99.5 } },
    @{ Name = 'numeric strings'; Mutate = { param($r) $r.totals.lines = '100'; $r.report.totals.lines = '100' } },
    @{ Name = 'boolean count'; Mutate = { param($r) $r.totals.partials = $false; $r.report.totals.partials = $false } },
    @{ Name = 'missing count'; Mutate = { param($r) $r.totals.PSObject.Properties.Remove('misses') } },
    @{ Name = 'null count'; Mutate = { param($r) $r.totals.hits = $null; $r.report.totals.hits = $null } },
    @{ Name = 'array-valued count'; Mutate = { param($r) $r.totals.hits = @(99); $r.report.totals.hits = @(99) } },
    @{ Name = 'array-valued identity'; Mutate = { param($r) $r.commitid = @($r.commitid) } },
    @{ Name = 'array-valued state'; Mutate = { param($r) $r.state = @('complete') } },
    @{ Name = 'array-valued nested report'; Mutate = { param($r) $r.report = @($r.report) } },
    @{ Name = 'array-valued totals'; Mutate = { param($r) $r.totals = @($r.totals) } }
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
Assert-Rejected { Test-Policy (New-Report $baseRevision 99 100) @(New-Report $headRevision 99 100) } 'array-valued outer report'
$checks++
if ($Scope -eq 'Report') { Write-Output "Passed $checks Codecov report policy checks."; return }

function New-HttpResponse {
    param([int]$StatusCode, $Report)
    return [pscustomobject]@{ StatusCode = $StatusCode; Content = ($Report | ConvertTo-Json -Depth 10 -Compress) }
}
function New-Transport {
    param([object[]]$Responses)
    $state = [pscustomobject]@{ Calls = [Collections.Generic.List[string]]::new(); Pauses = [Collections.Generic.List[int]]::new(); Responses = $Responses }
    $fetch = {
        param($uri)
        $index = [Math]::Min($state.Calls.Count, $state.Responses.Count - 1)
        $state.Calls.Add([string]$uri)
        $response = $state.Responses[$index]
        if ($response -is [Exception]) { throw $response }
        return $response
    }.GetNewClosure()
    $pause = { param($seconds) $state.Pauses.Add($seconds) }.GetNewClosure()
    return [pscustomobject]@{ State = $state; Fetch = $fetch; Pause = $pause }
}

$pending = New-Report $headRevision 99 100
$pending.state = 'pending'
$complete = New-HttpResponse 200 (New-Report $headRevision 99 100)
$transport = New-Transport @((New-HttpResponse 200 $pending), $complete)
$observed = Wait-CodecovReport -Revision $headRevision -Fetch $transport.Fetch -Pause $transport.Pause
if ($null -eq $observed -or $observed.commitid -cne $headRevision -or $transport.State.Calls.Count -ne 2 -or $transport.State.Pauses.Count -ne 1) {
    throw 'Pending Codecov report must be retried once and return the exact completed report.'
}
if ($transport.State.Calls[0] -cne "https://api.codecov.io/api/v2/github/IgnisDevNE/repos/SuiteWard/commits/$headRevision/") { throw 'Codecov endpoint scope changed.' }
$checks++

$unprocessed = New-Report $headRevision 99 100
$unprocessed.state = $null
$transport = New-Transport @((New-HttpResponse 200 $unprocessed), $complete)
$observed = Wait-CodecovReport -Revision $headRevision -Fetch $transport.Fetch -Pause $transport.Pause
if ($null -eq $observed -or $observed.state -cne 'complete' -or $observed.commitid -cne $headRevision -or $transport.State.Calls.Count -ne 2 -or $transport.State.Pauses.Count -ne 1) {
    throw 'Present null Codecov state must retry until the exact completed report arrives.'
}
$checks++

foreach ($mutate in @(
    { param($r) $r.PSObject.Properties.Remove('state') },
    { param($r) $r.state = @('pending') },
    { param($r) $r.state = @($null) },
    { param($r) $r.state = $false },
    { param($r) $r.state = 0 },
    { param($r) $r.state = '' },
    { param($r) $r.state = $null; $r.commitid = 'c' * 40 }
)) {
    $bad = New-Report $headRevision 99 100
    & $mutate $bad
    $transport = New-Transport @((New-HttpResponse 200 $bad), $complete)
    Assert-Rejected { Wait-CodecovReport $headRevision $transport.Fetch $transport.Pause } 'malformed or foreign unprocessed report'
    if ($transport.State.Calls.Count -ne 1 -or $transport.State.Pauses.Count -ne 0) { throw 'Malformed or foreign report was retried.' }
    $checks++
}

foreach ($transient in @(404, 408, 429, 500, 503)) {
    $transport = New-Transport @((New-HttpResponse $transient $null), $complete)
    Wait-CodecovReport $headRevision $transport.Fetch $transport.Pause | Out-Null
    if ($transport.State.Calls.Count -ne 2) { throw "HTTP $transient did not retry." }
    $checks++
}
foreach ($errorValue in @([Net.Http.HttpRequestException]::new('Network unavailable'), [Threading.Tasks.TaskCanceledException]::new('Request timed out'))) {
    $transport = New-Transport @($errorValue, $complete)
    Wait-CodecovReport $headRevision $transport.Fetch $transport.Pause | Out-Null
    if ($transport.State.Calls.Count -ne 2) { throw 'Transient transport failure did not retry.' }
    $checks++
}
foreach ($terminal in @(401, 403, 400, 418, 302)) {
    $transport = New-Transport @((New-HttpResponse $terminal $null), $complete)
    Assert-Rejected { Wait-CodecovReport $headRevision $transport.Fetch $transport.Pause } "terminal HTTP $terminal"
    if ($transport.State.Calls.Count -ne 1 -or $transport.State.Pauses.Count -ne 0) { throw 'Terminal response retried.' }
    $checks++
}
foreach ($stateName in @('error', 'skipped', 'unknown')) {
    $bad = New-Report $headRevision 99 100
    $bad.state = $stateName
    $transport = New-Transport @((New-HttpResponse 200 $bad), $complete)
    Assert-Rejected { Wait-CodecovReport $headRevision $transport.Fetch $transport.Pause } "terminal state $stateName"
    if ($transport.State.Calls.Count -ne 1) { throw 'Terminal report state retried.' }
    $checks++
}
foreach ($badResponse in @(
    [pscustomobject]@{ StatusCode = 200; Content = '{broken' },
    (New-HttpResponse 200 (New-Report $baseRevision 99 100)),
    (New-HttpResponse 200 ([pscustomobject]@{ commitid = $headRevision; state = 'complete' }))
)) {
    $transport = New-Transport @($badResponse, $complete)
    Assert-Rejected { Wait-CodecovReport $headRevision $transport.Fetch $transport.Pause } 'corrupt report envelope'
    if ($transport.State.Calls.Count -ne 1) { throw 'Corrupt report was retried or replaced.' }
    $checks++
}
foreach ($unavailable in @((New-HttpResponse 404 $null), (New-HttpResponse 200 $pending), (New-HttpResponse 200 $unprocessed), [Net.Http.HttpRequestException]::new('offline'))) {
    $transport = New-Transport @($unavailable)
    Assert-Rejected { Wait-CodecovReport $headRevision $transport.Fetch $transport.Pause } 'bounded report exhaustion'
    if ($transport.State.Calls.Count -ne 12 -or $transport.State.Pauses.Count -ne 11 -or @($transport.State.Pauses | Where-Object { $_ -ne 10 }).Count) { throw 'Retry bound or pause duration changed.' }
    $checks++
}
$transport = New-Transport @((New-HttpResponse 404 $null))
Assert-Rejected { Invoke-CodecovProjectPolicy -Repository 'IgnisDevNE/SuiteWard' -BaseRevision $baseRevision -HeadRevision $headRevision -Fetch $transport.Fetch -Pause $transport.Pause } 'missing baseline cannot use head as a substitute'
if ($transport.State.Calls.Count -ne 12 -or @($transport.State.Calls | Where-Object { $_ -notlike "*/$baseRevision/" }).Count) { throw 'Baseline failure fetched or substituted another revision.' }
$checks++
$transport = New-Transport @((New-HttpResponse 200 (New-Report $baseRevision 99 100)), $complete)
$observed = Invoke-CodecovProjectPolicy -Repository 'IgnisDevNE/SuiteWard' -BaseRevision $baseRevision -HeadRevision $headRevision -Fetch $transport.Fetch -Pause $transport.Pause
if (-not $observed.Passed -or $observed.Base.Revision -cne $baseRevision -or $observed.Head.Revision -cne $headRevision -or $transport.State.Calls.Count -ne 2) { throw 'Policy did not use both exact completed reports.' }
$checks++
$transport = New-Transport @($complete)
foreach ($badRevision in @('', 'main', ('0' * 40), ('c' * 39))) {
    Assert-Rejected { Wait-CodecovReport $badRevision $transport.Fetch $transport.Pause } 'invalid requested revision'
    $checks++
}
Assert-Rejected { Invoke-CodecovProjectPolicy -Repository 'other/repo' -BaseRevision $baseRevision -HeadRevision $headRevision -Fetch $transport.Fetch -Pause $transport.Pause } 'foreign repository'
if ($transport.State.Calls.Count -ne 0) { throw 'Invalid scope attempted a network request.' }
$checks++

$event = [pscustomobject]@{ pull_request = [pscustomobject]@{ base = [pscustomobject]@{ sha = $baseRevision }; head = [pscustomobject]@{ sha = $headRevision } } }
$range = Get-CodecovRevisionRange -EventName 'pull_request' -Event $event -CurrentRevision ('c' * 40) -EventRevision ('c' * 40)
if ($range.Base -cne $baseRevision -or $range.Head -cne $headRevision) { throw 'Coverage attributed to synthetic merge instead of PR event pair.' }
$checks++
$range = Get-CodecovRevisionRange -EventName 'push' -Event ([pscustomobject]@{ before = $baseRevision; after = $headRevision }) -ParentRevision ('c' * 40)
if ($range.Base -cne $baseRevision -or $range.Head -cne $headRevision) { throw 'Push comparison did not retain event before/after.' }
$checks++
$range = Get-CodecovRevisionRange -EventName 'workflow_dispatch' -CurrentRevision $headRevision -ParentRevision $baseRevision -EventRevision $headRevision
if ($range.Base -cne $baseRevision -or $range.Head -cne $headRevision) { throw 'Manual coverage range changed.' }
$checks++
Assert-Rejected { Get-CodecovRevisionRange -EventName 'workflow_dispatch' -CurrentRevision $headRevision -ParentRevision $baseRevision -EventRevision ('c' * 40) } 'manual checkout does not match event'
$checks++
Assert-Rejected { Get-CodecovRevisionRange -EventName 'push' -Event ([pscustomobject]@{ before = $headRevision; after = $headRevision }) } 'same source compared with itself'
$checks++
Assert-Rejected { Get-CodecovRevisionRange -EventName 'push' -Event ([pscustomobject]@{ before = ('0' * 40); after = $headRevision }) } 'absent push baseline'
$checks++
Assert-Rejected { Get-CodecovRevisionRange -EventName 'unknown' } 'unsupported coverage event'
$checks++
foreach ($eventName in @('pull_request', 'push')) {
    foreach ($field in @('base', 'head')) {
        foreach ($values in @(@($baseRevision), @($baseRevision, $headRevision))) {
            $baseValue = if ($field -eq 'base') { ,$values } else { $baseRevision }
            $headValue = if ($field -eq 'head') { ,$values } else { $headRevision }
            $arrayEvent = if ($eventName -eq 'push') {
                [pscustomobject]@{ before = $baseValue; after = $headValue }
            } else {
                [pscustomobject]@{ pull_request = [pscustomobject]@{ base = [pscustomobject]@{ sha = $baseValue }; head = [pscustomobject]@{ sha = $headValue } } }
            }
            Assert-Rejected { Get-CodecovRevisionRange -EventName $eventName -Event $arrayEvent } "$eventName $field SHA must be a scalar"
            $checks++
        }
    }
}

# Configuration wiring is behavior: the upload and check must bind the same
# measured source and a policy failure must reach the existing required gate.
$workflow = (Get-Content -Raw (Join-Path $PSScriptRoot '../.github/workflows/ci.yml')).Replace("`r", '')
$coverageJob = [regex]::Match($workflow, '(?ms)^  coverage:\n(?<body>.*?)^  gate:').Groups['body'].Value
if ($coverageJob.IndexOf('run: ./scripts/check-codecov-policy.ps1') -le $coverageJob.IndexOf('uses: codecov/codecov-action@')) { throw 'Project policy must run in the coverage job after upload.' }
$checks++
foreach ($setting in @('ref: ${{ github.event.pull_request.head.sha || github.sha }}', 'override_commit: ${{ github.event.pull_request.head.sha || github.sha }}')) {
    if (-not $coverageJob.Contains($setting)) { throw "Coverage source binding missing: $setting" }
    $checks++
}
if ($coverageJob -match 'continue-on-error:\s*true' -or $workflow -notmatch 'needs: \[inspect, foundation, tdd, go, coverage\]') { throw 'Project policy failure must block CI / Gate.' }
$checks++
$foundation = Get-Content -Raw (Join-Path $PSScriptRoot 'check-foundation.ps1')
if (-not $foundation.Contains("'test-codecov-policy.ps1'")) { throw 'Foundation must run the Codecov policy regressions on both platforms.' }
$checks++
Write-Output "Passed $checks Codecov project policy checks."
