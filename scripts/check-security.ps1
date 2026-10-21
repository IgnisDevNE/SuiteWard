# zizmor audits the workflows offline and gitleaks scans the whole git history; both are the pinned, checksum-verified tools from dev/tools.json.
#Requires -Version 7.2
param([string]$Root = (Split-Path $PSScriptRoot -Parent))
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'dev-env.ps1')
$context = Get-DevContext (Split-Path $PSScriptRoot -Parent)
Install-ArchiveTool $context 'zizmor'
Install-ArchiveTool $context 'gitleaks'
Push-Location -LiteralPath $Root
try {
    & (Get-ToolPath $context 'zizmor') --offline --no-progress .github/workflows
    if ($LASTEXITCODE -ne 0) { throw 'zizmor reported findings in the workflows.' }
    # A shallow checkout hides the history the scan exists to read.
    if ((git rev-parse --is-shallow-repository) -ne 'false') { throw 'The history scan needs a full clone; fetch the full history.' }
    # Never verbose and always redacted; the report is read back only for rule, place and commit, never for a match.
    $report = Join-Path ([IO.Path]::GetTempPath()) "gitleaks-$([Guid]::NewGuid().ToString('N')).json"
    try {
        & (Get-ToolPath $context 'gitleaks') git --redact --no-banner --no-color --log-level error --report-format json --report-path $report .
        if ($LASTEXITCODE -ne 0) {
            if (Test-Path -LiteralPath $report) {
                foreach ($finding in Get-Content -LiteralPath $report -Raw | ConvertFrom-Json) { Write-Host "gitleaks: $($finding.RuleID) in $($finding.File):$($finding.StartLine) at commit $($finding.Commit)" }
            }
            throw 'gitleaks found a secret in the git history.'
        }
    } finally { if (Test-Path -LiteralPath $report) { Remove-Item -LiteralPath $report } }
    Write-Host 'Workflow audit and history secret scan passed.'
} finally { Pop-Location }
