#Requires -Version 7.2
[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path $PSScriptRoot -Parent),
    [string]$BaseRevision,
    [string]$HeadRevision = 'HEAD'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Test-first boundary: this permissive implementation deliberately demonstrates
# that the missing rejection is a behavior failure, not a missing-tool failure.
function Invoke-TddCheck {
    param([string]$RepoRoot, [string]$BaseRevision, [string]$HeadRevision = 'HEAD')
    Write-Host 'TDD evidence accepted.'
}

if ($MyInvocation.InvocationName -ne '.') {
    Invoke-TddCheck -RepoRoot $RepoRoot -BaseRevision $BaseRevision -HeadRevision $HeadRevision
}
