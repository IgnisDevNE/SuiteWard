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
    & (Get-ToolPath $context 'actionlint') -shellcheck= -pyflakes= .github/workflows/ci.yml
    if ($LASTEXITCODE -ne 0) { throw 'Workflow validation failed.' }
    & (Join-Path $PSScriptRoot 'ci.ps1') -Mode Documents
    & (Join-Path $PSScriptRoot 'test-ci.ps1')
    & (Join-Path $PSScriptRoot 'test-go.ps1')
    & (Join-Path $PSScriptRoot 'test-dev.ps1')
    & (Join-Path $PSScriptRoot 'check-plan.ps1')
    & (Join-Path $PSScriptRoot 'test-plan.ps1')
    & (Join-Path $PSScriptRoot 'test-tdd.ps1')
    Write-Host 'Foundation scripts, workflows, documentation, and delivery plan are valid.'
} finally { Pop-Location }
