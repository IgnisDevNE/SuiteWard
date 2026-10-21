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
    # These files could narrow what the scanners see. The tools also run without configuration (below); this is the friendly error.
    foreach ($config in '.gitleaks.toml', '.gitleaksignore', 'zizmor.yml', 'zizmor.yaml', '.github/zizmor.yml', '.github/zizmor.yaml') {
        if (Test-Path -LiteralPath $config) { throw "$config would change what the scanners report; remove it." }
    }
    # Configuration also comes from the environment, and zizmor honors inline ignore comments: run with neither.
    $saved = @{}
    foreach ($name in 'ZIZMOR_CONFIG', 'GITLEAKS_CONFIG', 'GITLEAKS_CONFIG_TOML') {
        $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
        Remove-Item -LiteralPath "Env:$name" -ErrorAction SilentlyContinue
    }
    & (Get-ToolPath $context 'zizmor') --offline --no-progress --no-config --no-ignores .github/workflows
    if ($LASTEXITCODE -ne 0) { throw 'zizmor reported findings in the workflows.' }
    # A shallow checkout hides the history the scan exists to read.
    if ((git rev-parse --is-shallow-repository) -ne 'false') { throw 'The history scan needs a full clone; fetch the full history.' }
    # Never verbose and always redacted; the report is read back only for rule, place and commit, never for a match.
    $report = Join-Path ([IO.Path]::GetTempPath()) "gitleaks-$([Guid]::NewGuid().ToString('N')).json"
    try {
        & (Get-ToolPath $context 'gitleaks') git --redact --ignore-gitleaks-allow --no-banner --no-color --log-level error --report-format json --report-path $report .
        if ($LASTEXITCODE -ne 0) {
            # Exit 1 with a report is a finding; anything else is the tool failing, which must not read as a secret.
            if ($LASTEXITCODE -ne 1 -or -not (Test-Path -LiteralPath $report)) { throw "gitleaks failed (exit $LASTEXITCODE) without a report." }
            foreach ($finding in Get-Content -LiteralPath $report -Raw | ConvertFrom-Json) { Write-Host "gitleaks: $($finding.RuleID) in $($finding.File):$($finding.StartLine) at commit $($finding.Commit)" }
            throw 'gitleaks found a secret in the git history.'
        }
    } finally { if (Test-Path -LiteralPath $report) { Remove-Item -LiteralPath $report } }
    Write-Host 'Workflow audit and history secret scan passed.'
} finally {
    Pop-Location
    if (Test-Path variable:saved) { Restore-DevEnvironment $saved }
}
