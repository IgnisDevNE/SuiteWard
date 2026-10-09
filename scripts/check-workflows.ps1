# Validates every workflow file with actionlint.
#Requires -Version 7.2
param([string]$Directory = (Join-Path (Split-Path $PSScriptRoot -Parent) '.github/workflows'))
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'dev-env.ps1')
$context = Get-DevContext (Split-Path $PSScriptRoot -Parent)
$files = @(Get-ChildItem -LiteralPath $Directory -Filter '*.yml' -File | ForEach-Object FullName)
if (-not $files) { throw "No workflow files in $Directory." }
& (Get-ToolPath $context 'actionlint') -shellcheck= -pyflakes= @files
if ($LASTEXITCODE -ne 0) { throw 'Workflow validation failed.' }
