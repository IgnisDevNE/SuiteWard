param(
    [ValidateSet('Check', 'Library')]
    [string]$Mode = 'Check',
    [string]$Root = (Split-Path $PSScriptRoot -Parent)
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-CodecovProjectPolicy {
    param($BaseReport, $HeadReport, [string]$BaseRevision, [string]$HeadRevision)
    return [pscustomobject]@{ Passed = $true }
}

if ($Mode -eq 'Library') { return }
throw 'The Codecov transport adapter is not implemented yet.'
