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
$missingFoundation = New-Results 'false' 'skipped' 'skipped'
$missingFoundation.PSObject.Properties.Remove('foundation')
Assert-Throws { Assert-CiGate $missingFoundation } 'missing foundation job'
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

$project = Get-CoveragePolicy 'project' 'default'
$patch = Get-CoveragePolicy 'patch' 'default'
if ($project['informational'] -ne 'true' -or $project['target'] -ne 'auto') { throw 'project/default must be an informational ratchet signal against the base.' }
$checks++
if (@([regex]::Matches($coverageYaml, '(?m)^      [a-z_]+:$')).Count -ne 2) { throw 'Coverage statuses must be exactly project/default and patch/default.' }
$checks++
if ($patch['informational'] -ne 'false') { throw 'patch/default must block a coverage violation (codecov/patch is a required check).' }
if ($patch['if_not_found'] -ne 'failure') { throw 'patch/default must fail when the head coverage report is absent.' }
foreach ($key in @('target', 'threshold')) {
    if (-not $patch.ContainsKey($key)) { throw "patch/default must declare $key explicitly." }
}
foreach ($filter in @('paths', 'flags', 'branches', 'only_pulls')) {
    if ($patch.ContainsKey($filter)) { throw 'patch/default must not narrow the accepted coverage scope or reporting events.' }
}
if (Test-CoveragePolicyDecision -Policy $patch -HeadCoverage $null -BaseCoverage 100) { throw 'patch/default accepted an absent report.' }
$checks++

foreach ($case in @(
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
# Branch protection requires the exact context name; the gate must aggregate every verification job.
$gateJob=[regex]::Match($workflow,'(?ms)^  gate:\n(?<body>.*?)(?=^  [a-z]+:|\z)').Groups['body'].Value
if(-not $gateJob.Contains('name: CI / Gate')){throw 'The gate job must keep the required context name CI / Gate.'}
if(-not $gateJob.Contains('needs: [inspect, foundation, go, coverage]') -or -not $gateJob.Contains('if: always()')){throw 'The gate must always run and require inspect, foundation, go, and coverage.'}
$checks++
Write-Output "Passed $checks CI behavior checks. These verify CI infrastructure, not application coverage."
