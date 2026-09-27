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
Write-Output "Passed $checks CI behavior checks. These verify CI infrastructure, not application coverage."
