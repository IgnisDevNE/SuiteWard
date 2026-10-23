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
    param([string]$HasGo, [string]$GoResult, [string]$CoverageResult, [string]$QuadletResult = $CoverageResult)
    return [pscustomobject]@{
        inspect = [pscustomobject]@{ result = 'success'; outputs = [pscustomobject]@{ go = $HasGo } }
        foundation = [pscustomobject]@{ result = 'success' }
        image = [pscustomobject]@{ result = 'success' }
        go = [pscustomobject]@{ result = $GoResult }
        coverage = [pscustomobject]@{ result = $CoverageResult }
        quadlet = [pscustomobject]@{ result = $QuadletResult }
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
foreach ($badResult in @('skipped', 'failure', 'cancelled')) {
    $badImage = New-Results 'false' 'skipped' 'skipped'
    $badImage.image.result = $badResult
    Assert-Throws { Assert-CiGate $badImage } "image build $badResult"
    $checks++
}
$missingImage = New-Results 'false' 'skipped' 'skipped'
$missingImage.PSObject.Properties.Remove('image')
Assert-Throws { Assert-CiGate $missingImage } 'missing image job'
$checks++
# The AlmaLinux quadlet dry run is part of the Go classification: required with Go source, skipped without it.
foreach ($badResult in @('skipped', 'failure', 'cancelled')) {
    Assert-Throws { Assert-CiGate (New-Results 'true' 'success' 'success' $badResult) } "quadlet job $badResult"
    $checks++
}
Assert-Throws { Assert-CiGate (New-Results 'false' 'skipped' 'skipped' 'success') } 'quadlet job ran without Go source'
$checks++
$missingQuadlet = New-Results 'true' 'success' 'success'
$missingQuadlet.PSObject.Properties.Remove('quadlet')
Assert-Throws { Assert-CiGate $missingQuadlet } 'missing quadlet job'
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
foreach($required in @('services:', 'postgres:', 'postgres:18.6-trixie@sha256:74935e72241653ca55e0414067e6d8763aceb8a810eb51b452253ec3dcfc4336', 'pg_isready', 'SUITEWARD_TEST_DATABASE_URL:', './scripts/check-go.ps1 -Coverage -Integration')) {
    if(-not $coverageJob.Contains($required)){throw "Required real PostgreSQL coverage verification is absent: $required"}
    $checks++
}
$goJob=[regex]::Match($workflow,'(?ms)^  go:\n(?<body>.*?)(?=^  [a-z]+:|\z)').Groups['body'].Value
if(-not $goJob.Contains('./scripts/check-persistence.ps1 -Mode Generated')){throw 'CI must verify fresh sqlc output on the Linux and Windows Go runners.'}
$checks++
# macOS runs the Go build, vet and tests; the pinned tool bootstrap (dev-env.ps1) supports Windows and Linux x64 only, so the platform-independent lint and sqlc checks stay on those runners.
if($goJob -notmatch '(?m)^        os: \[ubuntu-24\.04, windows-2025, macos-15\]\s*$'){throw 'The Go job matrix must be ubuntu-24.04, windows-2025 and macos-15.'}
if($goJob -notmatch '(?ms)^      - name: [^\n]*\n        if: runner\.os != ''macOS''\n        run: \./scripts/check-go\.ps1\s*$'){throw 'The default check-go.ps1 step (with golangci-lint) must skip macOS.'}
if($goJob -notmatch '(?ms)^      - name: [^\n]*\n        if: runner\.os == ''macOS''\n        run: \./scripts/check-go\.ps1 -SkipLint\s*$'){throw 'macOS must run check-go.ps1 -SkipLint.'}
if($goJob -notmatch '(?ms)^      - name: Verify fresh generated PostgreSQL queries\n        if: runner\.os != ''macOS''\n'){throw 'The sqlc regeneration check must skip macOS.'}
$checks+=4
# Lint runs once, inside the default-mode Go job (both OSes), from the pinned checksum-verified tool; no unpinned install or third-party action.
if($goJob -notmatch '(?m)^\s+run: \./scripts/check-go\.ps1\s*$'){throw 'The Go job must run check-go.ps1 in default mode, which includes golangci-lint.'}
$checkGoSource=(Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'check-go.ps1')).Replace("`r",'')
foreach($required in @("Install-ArchiveTool `$context 'golangci-lint'", "(Get-ToolPath `$context 'golangci-lint') run ./...")){
    if(-not $checkGoSource.Contains($required)){throw "check-go.ps1 must install and run the pinned golangci-lint: $required"}
}
if($coverageJob.Contains('golangci') -or $workflow -match 'golangci-lint-action|golangci-lint@latest'){throw 'Lint must not run in the coverage job or through an unpinned install or action.'}
if($checkGoSource -match 'go install|@latest'){throw 'check-go.ps1 must not install tools with go install or an unpinned version; tools come from dev/tools.json.'}
$checks+=4
if(-not (Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'check-foundation.ps1')).Contains("'test-persistence.ps1'")){throw 'Foundation must execute the persistence infrastructure behavior checks.'}
$checks++
# Branch protection requires the exact context name; the gate must aggregate every verification job.
$gateJob=[regex]::Match($workflow,'(?ms)^  gate:\n(?<body>.*?)(?=^  [a-z]+:|\z)').Groups['body'].Value
if(-not $gateJob.Contains('name: CI / Gate')){throw 'The gate job must keep the required context name CI / Gate.'}
if(-not $gateJob.Contains('needs: [inspect, foundation, image, go, coverage, quadlet]') -or -not $gateJob.Contains('if: always()')){throw 'The gate must always run and require inspect, foundation, image, go, coverage, and quadlet.'}
$checks++
# The quadlet job renders the units of both installer shapes and dry-runs them through the quadlet generator of the isolated host's OS (AlmaLinux 9).
$quadletJob=[regex]::Match($workflow,'(?ms)^  quadlet:\n(?<body>.*?)(?=^  [a-z]+:|\z)').Groups['body'].Value
if(-not $quadletJob){throw 'The CI workflow has no quadlet job.'}
foreach($required in @('needs: inspect', "if: needs.inspect.outputs.go == 'true'", 'runs-on: ubuntu-24.04', 'timeout-minutes:', 'shell: bash', '-tags quadlet', 'SUITEWARD_QUADLET_OUT', 'dnf -y install podman', 'podman --version', 'QUADLET_UNIT_DIRS', '/usr/lib/systemd/system-generators/podman-system-generator --user --dryrun', 'for shape in github no-github')){
    if(-not $quadletJob.Contains($required)){throw "The quadlet job lacks: $required"}
    $checks++
}
# Every unit the installer renders must come out of the generator as a service.
foreach($service in @('suiteward.service', 'suiteward-migrate.service', 'suiteward-db.service', 'suiteward-network.service', 'suiteward-data-volume.service', 'suiteward-db-data-volume.service')){
    if(-not $quadletJob.Contains($service)){throw "The quadlet job does not require the generated $service"}
    $checks++
}
if($quadletJob -notmatch 'ALMA_IMAGE: docker\.io/library/almalinux:9@sha256:[a-f0-9]{64}'){throw 'The quadlet job must pin AlmaLinux 9 by digest.'}
if($quadletJob -notmatch '(?m)^    permissions:\n      contents: read\n' -or $quadletJob -match 'secrets\.|id-token|: write'){throw 'The quadlet job needs no secret and only contents: read.'}
$checks+=2
# GitHub's pwsh step wrapper exits with $LASTEXITCODE; script tests deliberately run failing native children,
# so a successful foundation run must leave a zero exit code behind.
$foundationSource=(Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'check-foundation.ps1')).Replace("`r",'')
if($foundationSource -notmatch "(?m)^\s*Write-Host 'Foundation scripts, workflows, and documentation are valid\.'\s*\n\s*\`$global:LASTEXITCODE = 0\s*$"){throw 'Foundation must reset the native exit code after all checks succeed.'}
$checks++
Write-Output "Passed $checks CI behavior checks. These verify CI infrastructure, not application coverage."
