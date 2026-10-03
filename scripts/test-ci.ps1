$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'ci.ps1') -Mode Library

$checks = 0
function Assert-Throws {
    param([scriptblock]$Action, [string]$Scenario)
    $failed = $false
    try { & $Action | Out-Null } catch { $failed = $true }
    if (-not $failed) { throw "Expected rejection: $Scenario" }
}

if ((Get-RepositoryMode -HeadFiles @('docs/README.md') -BaseFiles @()) -ne 'foundation') { throw 'Documentation bootstrap misclassified' }
$checks++
if ((Get-RepositoryMode -HeadFiles @('go.mod', 'internal/contract.go') -BaseFiles @('docs/README.md')) -ne 'go') { throw 'First real Go change misclassified' }
$checks++
Assert-Throws { Get-RepositoryMode -HeadFiles @('README.md') -BaseFiles @('go.mod', 'internal/contract.go') } 'removing Go module and source'
$checks++
Assert-Throws { Get-RepositoryMode -HeadFiles @('internal/contract.go') -BaseFiles @() } 'source without a module'
$checks++
Assert-Throws { Get-RepositoryMode -HeadFiles @('go.mod') -BaseFiles @() } 'module without source'
$checks++

function New-Results {
    param([string]$HasGo, [string]$GoResult, [string]$CoverageResult)
    return [pscustomobject]@{
        inspect = [pscustomobject]@{ result = 'success'; outputs = [pscustomobject]@{ go = $HasGo } }
        foundation = [pscustomobject]@{ result = 'success' }
        tdd = [pscustomobject]@{ result = 'success' }
        go = [pscustomobject]@{ result = $GoResult }
        coverage = [pscustomobject]@{ result = $CoverageResult }
    }
}

Assert-CiGate (New-Results 'false' 'skipped' 'skipped')
$checks++
Assert-CiGate (New-Results 'true' 'success' 'success')
$checks++
foreach ($badResult in @('skipped', 'failure', 'cancelled')) {
    Assert-Throws { Assert-CiGate (New-Results 'true' $badResult 'success') } "Go job $badResult"
    Assert-Throws { Assert-CiGate (New-Results 'true' 'success' $badResult) } "coverage job $badResult"
    $checks += 2
}
Assert-Throws { Assert-CiGate (New-Results '' 'skipped' 'skipped') } 'missing classification'
$checks++
$failedFoundation = New-Results 'false' 'skipped' 'skipped'
$failedFoundation.foundation.result = 'failure'
Assert-Throws { Assert-CiGate $failedFoundation } 'failed documentation/configuration verification'
$checks++
$failedTdd = New-Results 'false' 'skipped' 'skipped'
foreach ($badResult in @('failure', 'skipped', 'cancelled')) {
    $failedTdd.tdd.result = $badResult
    Assert-Throws { Assert-CiGate $failedTdd } "TDD evidence job $badResult"
    $checks++
}
$missingTdd = New-Results 'false' 'skipped' 'skipped'
$missingTdd.PSObject.Properties.Remove('tdd')
Assert-Throws { Assert-CiGate $missingTdd } 'missing TDD evidence job'
$checks++

$base = 'a' * 40
$head = 'b' * 40
$event = [pscustomobject]@{ pull_request = [pscustomobject]@{ base = [pscustomobject]@{sha=$base}; head = [pscustomobject]@{sha=$head} } }
$range = Get-TddRevisionRange -EventName 'pull_request' -Event $event
if ($range.Base -cne $base -or $range.Head -cne $head) { throw 'PR evidence must use the event base/head, not the checkout merge revision.' }
$checks++
$range = Get-TddRevisionRange -EventName 'push' -Event ([pscustomobject]@{before=$base;after=$head})
if ($range.Base -cne $base -or $range.Head -cne $head) { throw 'Push evidence must include the entire pushed range.' }
$checks++
$range = Get-TddRevisionRange -EventName 'workflow_dispatch' -CurrentRevision $head -ParentRevision $base
if ($range.Base -cne $base -or $range.Head -cne $head) { throw 'Manual verification must use the requested checkout and its first parent.' }
$checks++
Assert-Throws { Get-TddRevisionRange -EventName 'push' -Event ([pscustomobject]@{before=('0'*40);after=$head}) } 'missing push base'
$checks++
Assert-Throws { Get-TddRevisionRange -EventName 'push' -Event ([pscustomobject]@{before='main';after=$head}) } 'symbolic event revision'
$checks++
Assert-Throws { Get-TddRevisionRange -EventName 'workflow_dispatch' -CurrentRevision $head -ParentRevision '' } 'manual verification without parent'
$checks++
Assert-Throws { Get-TddRevisionRange -EventName 'unknown' } 'unsupported TDD event'
$checks++

# Inspect the scalar status declarations used by this repository. This is a
# local policy regression check, not a YAML parser or Codecov service emulator;
# Codecov's validator and hosted checks verify its interpretation separately.
$coverageYaml = (Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot '../codecov.yml')).Replace("`r", '')
function Get-CoveragePolicy {
    param([string]$Group, [string]$Name)
    $groupPattern = '(?m)^    ' + [regex]::Escape($Group) + ':\n(?<body>(?:^      [^\n]*\n|^\n)*)'
    $groupBody = [regex]::Match($coverageYaml, $groupPattern).Groups['body'].Value
    $statusPattern = '(?m)^      ' + [regex]::Escape($Name) + ':\n(?<body>(?:^        [^\n]*\n)*)'
    $statusBody = [regex]::Match($groupBody, $statusPattern).Groups['body'].Value
    $policy = @{}
    foreach ($field in [regex]::Matches($statusBody, '(?m)^        (?<key>[a-z_]+):[ ]*(?<value>[^\n]*)$')) {
        $key = $field.Groups['key'].Value
        if ($policy.ContainsKey($key)) { throw "Duplicate coverage policy field: $Group/$Name/$key" }
        $policy[$key] = $field.Groups['value'].Value.Trim().Trim('"', "'")
    }
    return $policy
}

function Convert-CoverageNumber {
    param([string]$Value)
    return [decimal]::Parse($Value.TrimEnd('%'), [Globalization.CultureInfo]::InvariantCulture)
}

function Test-CoveragePolicyDecision {
    param([hashtable]$Policy, [Nullable[decimal]]$HeadCoverage, [decimal]$BaseCoverage)
    if ($Policy['informational'] -eq 'true') { return $true }
    if ($null -eq $HeadCoverage) { return $Policy['if_not_found'] -ne 'failure' }
    $target = if ($Policy['target'] -eq 'auto') { $BaseCoverage } else { Convert-CoverageNumber $Policy['target'] }
    return $HeadCoverage -ge ($target - (Convert-CoverageNumber $Policy['threshold']))
}

$ratchet = Get-CoveragePolicy 'project' 'default'
$floor = Get-CoveragePolicy 'project' 'floor'
$patch = Get-CoveragePolicy 'patch' 'default'
foreach ($entry in @(@{Name='project/default';Policy=$ratchet}, @{Name='project/floor';Policy=$floor}, @{Name='patch/default';Policy=$patch})) {
    $policy = $entry.Policy
    if ($policy['informational'] -ne 'false') { throw "$($entry.Name) must block a coverage violation; missing/informational status cannot enforce the accepted policy." }
    if ($policy['if_not_found'] -ne 'failure') { throw "$($entry.Name) must fail when the head coverage report is absent." }
    foreach ($key in @('target', 'threshold')) {
        if (-not $policy.ContainsKey($key)) { throw "$($entry.Name) must declare $key explicitly." }
    }
    foreach ($filter in @('paths', 'flags', 'branches', 'only_pulls')) {
        if ($policy.ContainsKey($filter)) { throw "$($entry.Name) must not narrow the accepted coverage scope or reporting events." }
    }
    if (Test-CoveragePolicyDecision -Policy $policy -HeadCoverage $null -BaseCoverage 100) { throw "$($entry.Name) accepted an absent report." }
    $checks++
}

foreach ($case in @(
    @{Name='ratchet allows exactly 0.25 percentage points';Policy=$ratchet;Base=99.75;Head=99.5;Pass=$true},
    @{Name='ratchet rejects a larger decrease';Policy=$ratchet;Base=99.75;Head=99.499;Pass=$false},
    @{Name='ratchet follows a higher actual base';Policy=$ratchet;Base=100;Head=99.6;Pass=$false},
    @{Name='ratchet follows a lower actual base independently of floor';Policy=$ratchet;Base=99;Head=98.75;Pass=$true},
    @{Name='observed M0 report satisfies tolerance';Policy=$ratchet;Base=99.68;Head=99.51;Pass=$true},
    @{Name='absolute floor accepts exactly 99 independently of base';Policy=$floor;Base=100;Head=99;Pass=$true},
    @{Name='absolute floor stops accumulated decreases';Policy=$floor;Base=99.1;Head=98.999;Pass=$false},
    @{Name='patch retains exactly 90 percent';Policy=$patch;Base=100;Head=90;Pass=$true},
    @{Name='patch rejects below 90 independently of a lower base';Policy=$patch;Base=80;Head=89.999;Pass=$false},
    @{Name='patch has no tolerance below 90 percent';Policy=$patch;Base=100;Head=89.999;Pass=$false}
)) {
    $passed = Test-CoveragePolicyDecision -Policy $case.Policy -BaseCoverage $case.Base -HeadCoverage $case.Head
    if ($passed -ne $case.Pass) { throw "Coverage policy changed: $($case.Name); expected pass=$($case.Pass), actual=$passed." }
    $checks++
}
$workflow=(Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot '../.github/workflows/ci.yml')).Replace("`r",'')
$coverageJob=[regex]::Match($workflow,'(?ms)^  coverage:\n(?<body>.*?)(?=^  [a-z]+:|\z)').Groups['body'].Value
foreach($required in @('services:', 'postgres:', 'postgres:18.6-trixie@sha256:5a5a84b19854a9ffaa54082c166ff4ec27473a361e496e5ea167f298f2da9722', 'pg_isready', 'SUITEWARD_TEST_DATABASE_URL:', './scripts/check-go.ps1 -Coverage -Integration')) {
    if(-not $coverageJob.Contains($required)){throw "Required real PostgreSQL coverage verification is absent: $required"}
    $checks++
}
$goJob=[regex]::Match($workflow,'(?ms)^  go:\n(?<body>.*?)(?=^  [a-z]+:|\z)').Groups['body'].Value
foreach($job in @($goJob,$coverageJob)) {
    if(-not $job.Contains('./scripts/check-persistence.ps1 -Mode Generated')){throw 'CI must verify fresh sqlc output on both native Go runners and the real PostgreSQL coverage runner.'}
    $checks++
}
if(-not (Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'check-foundation.ps1')).Contains("'test-persistence.ps1'")){throw 'Foundation must execute the persistence infrastructure behavior checks.'}
$checks++
Write-Output "Passed $checks CI behavior checks. These verify CI infrastructure, not application coverage."
