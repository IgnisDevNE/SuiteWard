# trivy scans a built image archive and fails on a fixable HIGH or CRITICAL vulnerability, in the OS packages and in the Go binary.
#Requires -Version 7.2
param([Parameter(Mandatory)][string]$Archive)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not (Test-Path -LiteralPath $Archive -PathType Leaf)) { throw "The image archive $Archive does not exist." }
. (Join-Path $PSScriptRoot 'dev-env.ps1')
$context = Get-DevContext (Split-Path $PSScriptRoot -Parent)
Install-ArchiveTool $context 'trivy'
& (Get-ToolPath $context 'trivy') image --input $Archive --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 --no-progress
if ($LASTEXITCODE -ne 0) { throw 'trivy found a fixable HIGH or CRITICAL vulnerability in the image.' }
