# Offline checks of the deploy files: the quadlet units (migrate before serve, least-privilege secrets, digest pin).
# The co-located stack is started by `suiteward install`, which has Go tests over the same flag sets (internal/installer). Nothing here talks to a real Podman.
#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = Split-Path $PSScriptRoot -Parent
$failures = [Collections.Generic.List[string]]::new()
function Assert-That($Condition, $Message) { if (-not $Condition) { $failures.Add($Message) } }
# The lines that count: comments and blanks are not configuration.
function Get-Lines($Path) { if (-not (Test-Path -LiteralPath $Path)) { $failures.Add("$Path is missing"); return @() }; @(Get-Content -LiteralPath $Path | Where-Object { $_.Trim() -and $_.TrimStart()[0] -ne '#' } | ForEach-Object { $_.Trim() }) }

$quadlet = Join-Path $root 'deploy/quadlet'
$migrate = @(Get-Lines (Join-Path $quadlet 'suiteward-migrate.container'))
$serve = @(Get-Lines (Join-Path $quadlet 'suiteward.container'))
function Has([string[]]$Lines, $Prefix) { @($Lines | Where-Object { $_.StartsWith($Prefix) }).Count -gt 0 }

foreach ($line in 'Type=oneshot', 'RemainAfterExit=yes', 'Exec=migrate', 'Requires=suiteward-db.service', 'After=suiteward-db.service',
    'Network=suiteward.network', 'ReadOnly=true', 'DropCapability=ALL', 'NoNewPrivileges=true',
    'Environment=SUITEWARD_DATABASE_URL_FILE=/run/secrets/database-url',
    'Environment=SUITEWARD_MIGRATION_DATABASE_URL_FILE=/run/secrets/database-owner-url') {
    Assert-That ($migrate -contains $line) "suiteward-migrate.container lacks $line"
}
Assert-That (Has $migrate 'Secret=suiteward-database-owner-url,type=mount,target=/run/secrets/database-owner-url') 'migrate does not mount the owner URL secret'
Assert-That (Has $migrate 'Secret=suiteward-database-url,type=mount,target=/run/secrets/database-url') 'migrate does not mount the application URL secret'
Assert-That (-not (Has $migrate 'PublishPort=')) 'migrate publishes a port'
# A failed migrate fails closed: a restart loop would hide a real failure (missing role, a failed migration) while serve's start job has already failed.
foreach ($key in 'Restart=', 'RestartSec=', 'StartLimitIntervalSec=', 'AutoUpdate=') {
    Assert-That (-not (Has $migrate $key)) "migrate has $key (a failed migrate must leave the service down)"
}

foreach ($line in 'Requires=suiteward-migrate.service', 'After=suiteward-migrate.service', 'ReadOnly=true', 'DropCapability=ALL', 'NoNewPrivileges=true') {
    Assert-That ($serve -contains $line) "suiteward.container lacks $line"
}
Assert-That (Has $serve 'Secret=suiteward-database-url,type=mount') 'serve does not mount the application URL secret'
Assert-That (-not ($serve -match 'owner|MIGRATION')) 'serve sees the owner URL'
Assert-That (-not (Has $serve 'AutoUpdate=')) 'serve has AutoUpdate (it is a documented opt-in, not the default)'
$serveImage = @($serve | Where-Object { $_.StartsWith('Image=') })
Assert-That ($serveImage.Count -eq 1 -and $serveImage[0] -match '^Image=ghcr\.io/ignisdevne/suiteward@sha256:(<digest>|[0-9a-f]{64})$') 'serve Image is not pinned by digest'
Assert-That ($serveImage.Count -eq 1 -and (@($migrate | Where-Object { $_.StartsWith('Image=') }) -ceq $serveImage)) 'migrate and serve run different Image lines'

if ($failures.Count) { throw "Deploy checks failed:`n  $($failures -join "`n  ")" }
Write-Host 'Deploy checks passed.'
