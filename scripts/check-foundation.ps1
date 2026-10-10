#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
foreach ($script in Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*.ps1' -File -Recurse) {
    $parseErrors = $null
    [Management.Automation.Language.Parser]::ParseFile($script.FullName, [ref]$null, [ref]$parseErrors) | Out-Null
    if ($parseErrors.Count) { throw "PowerShell syntax error in $($script.Name): $($parseErrors[0].Message)" }
}
. (Join-Path $PSScriptRoot 'dev-env.ps1')
$context = Get-DevContext $root
New-Item -ItemType Directory -Path $context.State -Force | Out-Null
$lock = $null
try {
    $lock = [IO.File]::Open((Join-Path $context.State 'setup.lock'), 'OpenOrCreate', 'ReadWrite', 'None')
    Install-ArchiveTool $context 'actionlint'
} finally { if ($lock) { $lock.Dispose() } }
Push-Location -LiteralPath $root
try {
    & (Join-Path $PSScriptRoot 'check-workflows.ps1')
    & (Join-Path $PSScriptRoot 'test-workflows.ps1')
    & (Join-Path $PSScriptRoot 'ci.ps1') -Mode Documents
    & (Join-Path $PSScriptRoot 'test-ci.ps1')
    & (Join-Path $PSScriptRoot 'test-go.ps1')
    & (Join-Path $PSScriptRoot 'test-persistence.ps1')
    & (Join-Path $PSScriptRoot 'test-dev.ps1')
    & (Join-Path $PSScriptRoot 'test-bot-token.ps1')
    & (Join-Path $PSScriptRoot 'test-hooks.ps1')
    $sh = Get-Command sh -ErrorAction SilentlyContinue
    if ($sh) {
        & $sh.Source deploy/smoke_test.sh
        if ($LASTEXITCODE -ne 0) { throw 'The deploy smoke self-test failed' }
    } elseif ($IsWindows) {
        Write-Warning 'sh was not found: the deploy smoke self-test was skipped (the Linux CI job runs it).'
    } else { throw 'sh is required to run the deploy smoke self-test' }
    Write-Host 'Foundation scripts, workflows, and documentation are valid.'
    $global:LASTEXITCODE = 0
} finally { Pop-Location }
