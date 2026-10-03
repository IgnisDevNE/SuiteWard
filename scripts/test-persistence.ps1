#Requires -Version 7.2
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'check-persistence.ps1') -Mode Library
$checks=0
function Assert-PersistenceRejection {
    param([scriptblock]$Action, [string]$Scenario)
    $failed=$false
    try { & $Action | Out-Null } catch { $failed=$true }
    if (-not $failed) { throw "Generation freshness accepted $Scenario." }
}
$before=@{'models.go'=('a'*64);'governance.sql.go'=('b'*64)}
Assert-GeneratedQueriesCurrent -Before $before -After @{'models.go'=('a'*64);'governance.sql.go'=('b'*64)}
$checks++
Assert-PersistenceRejection { Assert-GeneratedQueriesCurrent -Before $before -After @{'models.go'=('a'*64);'governance.sql.go'=('c'*64)} } 'changed generated content'
$checks++
Assert-PersistenceRejection { Assert-GeneratedQueriesCurrent -Before $before -After @{'models.go'=('a'*64);'governance.sql.go'=('b'*64);'new.go'=('c'*64)} } 'an untracked generated output'
$checks++
Assert-PersistenceRejection { Assert-GeneratedQueriesCurrent -Before $before -After @{'models.go'=('a'*64)} } 'removed generated output'
$checks++
Assert-PersistenceRejection { Assert-GeneratedQueriesCurrent -Before @{} -After @{} } 'absent generated baseline'
$checks++
Assert-PersistenceRejection { Assert-GeneratedQueriesCurrent -Before @{'models.go'='invalid'} -After @{'models.go'='invalid'} } 'invalid digest inventory'
$checks++
Write-Output "Passed $checks generation freshness boundary checks. Real sqlc evidence remains separate."
