#Requires -Version 7.2
param([ValidateSet('Library','Generated')][string]$Mode = 'Generated')
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
function Assert-GeneratedQueriesCurrent {
    param([hashtable]$Before, [hashtable]$After)
}
function Invoke-GeneratedQueryVerification {
    param([string]$Root, [scriptblock]$Generate)
}
if ($Mode -eq 'Library') { return }
throw 'Generated query verification is not implemented.'
