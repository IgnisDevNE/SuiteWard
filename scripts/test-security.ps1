# Proves that the security scan accepts a clean repository, rejects a workflow zizmor flags, and rejects a committed secret without ever printing it. Offline.
#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$testRoot = Join-Path ([IO.Path]::GetTempPath()) "suiteward-security-$([Guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path (Join-Path $testRoot '.github/workflows') -Force | Out-Null
function Invoke-Git { git -C $testRoot -c user.name=test -c user.email=test@example.invalid -c commit.gpgsign=false @args; if ($LASTEXITCODE -ne 0) { throw "git $($args[0]) failed." } }
function Invoke-Scan {
    $transcript = [Collections.Generic.List[string]]::new()
    $passed = $true
    try { & (Join-Path $PSScriptRoot 'check-security.ps1') -Root $testRoot *>&1 | ForEach-Object { $transcript.Add("$_") } } catch { $passed = $false; $transcript.Add("$_") }
    return [pscustomobject]@{ Passed = $passed; Output = $transcript -join "`n" }
}
try {
    Invoke-Git init -q
    $workflow = Join-Path $testRoot '.github/workflows/w.yml'
    $clean = "on: push`npermissions: {}`njobs:`n  a:`n    runs-on: ubuntu-24.04`n    steps:`n      - run: echo ok`n"
    Set-Content -LiteralPath $workflow -Value $clean
    Invoke-Git add -A; Invoke-Git commit -q -m clean
    if (-not (Invoke-Scan).Passed) { throw 'A clean repository was rejected.' }
    # A pull request must not switch the scanners off by adding their own configuration or ignore files.
    foreach ($config in '.gitleaks.toml', '.gitleaksignore', 'zizmor.yml', 'zizmor.yaml', '.github/zizmor.yml', '.github/zizmor.yaml') {
        Set-Content -LiteralPath (Join-Path $testRoot $config) -Value ''
        if ((Invoke-Scan).Passed) { throw "The scan accepted a $config file." }
        Remove-Item -LiteralPath (Join-Path $testRoot $config)
    }
    # An action referenced by a mutable tag is something zizmor flags without network access.
    Set-Content -LiteralPath $workflow -Value ($clean + "      - uses: actions/checkout@v4`n")
    Invoke-Git add -A; Invoke-Git commit -q -m unpinned
    $scan = Invoke-Scan
    if ($scan.Passed -or -not $scan.Output.Contains('unpinned-uses')) { throw 'A workflow with an unpinned action was accepted.' }
    # Neither a configuration outside the tree (the environment) nor an inline comment may suppress a finding.
    $outside = Join-Path $testRoot '../zizmor-outside.yml'
    Set-Content -LiteralPath $outside -Value "rules:`n  unpinned-uses:`n    disable: true`n"
    $env:ZIZMOR_CONFIG = $outside
    try { $scan = Invoke-Scan } finally { Remove-Item Env:ZIZMOR_CONFIG; Remove-Item -LiteralPath $outside }
    if ($scan.Passed -or -not $scan.Output.Contains('unpinned-uses')) { throw 'ZIZMOR_CONFIG suppressed a finding.' }
    Set-Content -LiteralPath $workflow -Value ($clean + "      - uses: actions/checkout@v4 # zizmor: ignore[unpinned-uses]`n")
    $scan = Invoke-Scan
    if ($scan.Passed -or -not $scan.Output.Contains('unpinned-uses')) { throw 'An inline ignore comment suppressed a finding.' }
    Invoke-Git checkout -q -- .
    Invoke-Git revert --no-edit HEAD
    # The secret is built at run time, so this file holds no literal for the real scan to find. It stays in history after the revert.
    $secret = 'ghp_' + (-join (1..36 | ForEach-Object { 'abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ'[(Get-Random -Maximum 62)] }))
    Set-Content -LiteralPath (Join-Path $testRoot 'notes.txt') -Value "token = $secret"
    Invoke-Git add -A; Invoke-Git commit -q -m secret
    Invoke-Git rm -q notes.txt; Invoke-Git commit -q -m 'remove the secret'
    $scan = Invoke-Scan
    if ($scan.Passed) { throw 'A secret that is only in history was accepted.' }
    # A scanner configuration from the environment must not allow the secret either.
    $allow = Join-Path ([IO.Path]::GetTempPath()) "gitleaks-allow-$([Guid]::NewGuid().ToString('N')).toml"
    Set-Content -LiteralPath $allow -Value "[extend]`nuseDefault = true`n[[allowlists]]`npaths = ['''.*''']`n"
    $env:GITLEAKS_CONFIG = $allow
    try { $configured = Invoke-Scan } finally { Remove-Item Env:GITLEAKS_CONFIG; Remove-Item -LiteralPath $allow }
    if ($configured.Passed -or -not $configured.Output.Contains('github-pat')) { throw 'GITLEAKS_CONFIG allowed a secret.' }
    if ($scan.Output.Contains($secret) -or $scan.Output.Contains($secret.Substring(4, 12))) { throw 'The scan printed part of a secret.' }
    Write-Host 'Security scan checks passed.'
} finally { Remove-Item -LiteralPath $testRoot -Recurse -Force }
