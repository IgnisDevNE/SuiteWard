# Offline checks of the deploy files: the quadlet units (migrate before serve, least-privilege secrets, digest pin) and deploy/local.ps1
# (rootless connection only, no password in an argument, migrate before serve). Nothing here talks to a real Podman.
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

foreach ($line in 'Type=oneshot', 'RemainAfterExit=yes', 'Exec=migrate', 'Requires=suiteward-db.container', 'After=suiteward-db.container',
    'Network=suiteward.network', 'ReadOnly=true', 'DropCapability=ALL', 'NoNewPrivileges=true',
    'Environment=SUITEWARD_DATABASE_URL_FILE=/run/secrets/database-url',
    'Environment=SUITEWARD_MIGRATION_DATABASE_URL_FILE=/run/secrets/database-owner-url') {
    Assert-That ($migrate -contains $line) "suiteward-migrate.container lacks $line"
}
Assert-That (Has $migrate 'Secret=suiteward-database-owner-url,type=mount,target=/run/secrets/database-owner-url') 'migrate does not mount the owner URL secret'
Assert-That (Has $migrate 'Secret=suiteward-database-url,type=mount,target=/run/secrets/database-url') 'migrate does not mount the application URL secret'
Assert-That (-not (Has $migrate 'PublishPort=')) 'migrate publishes a port'

foreach ($line in 'Requires=suiteward-migrate.container', 'After=suiteward-migrate.container', 'ReadOnly=true', 'DropCapability=ALL', 'NoNewPrivileges=true') {
    Assert-That ($serve -contains $line) "suiteward.container lacks $line"
}
Assert-That (Has $serve 'Secret=suiteward-database-url,type=mount') 'serve does not mount the application URL secret'
Assert-That (-not ($serve -match 'owner|MIGRATION')) 'serve sees the owner URL'
Assert-That (-not (Has $serve 'AutoUpdate=')) 'serve has AutoUpdate (it is a documented opt-in, not the default)'
$serveImage = @($serve | Where-Object { $_.StartsWith('Image=') })
Assert-That ($serveImage.Count -eq 1 -and $serveImage[0] -match '^Image=ghcr\.io/ignisdevne/suiteward@sha256:(<digest>|[0-9a-f]{64})$') 'serve Image is not pinned by digest'
Assert-That ($serveImage.Count -eq 1 -and (@($migrate | Where-Object { $_.StartsWith('Image=') }) -ceq $serveImage)) 'migrate and serve run different Image lines'

$localPath = Join-Path $root 'deploy/local.ps1'
$local = Get-Lines $localPath
# A password in an argument shows in the process list: nothing that runs a command may mention a password or token variable.
foreach ($line in $local | Where-Object { $_ -match '(Invoke-Podman|& podman|psql)' -and $_ -match '\$\w*(password|token)' }) { $failures.Add("a password or token reaches a command line: $line") }
Assert-That (($local -match 'psql') -match 'exec -i' -and ($local -match 'psql') -match '-f -') 'psql is not fed on stdin (podman exec -i ... psql -f -)'
$text = Get-Content -LiteralPath $localPath -Raw
Assert-That ($text.IndexOf('migrate') -ge 0 -and $text.IndexOf('suiteward-migrate') -lt $text.LastIndexOf('--name suiteward `')) 'migrate does not run before serve'
Assert-That ($text.Contains('podman machine ssh --username user "sh -s"')) 'the smoke instruction does not use the rootless ssh user'

# Behavior with a fake podman (a function the script sees): a rootful connection is refused before anything is created; -Down -Purge removes the owner secret too.
$global:swCalls = [Collections.Generic.List[string]]::new()
$global:swRootless = 'false'
function podman {
    $global:swCalls.Add("$args")
    $global:LASTEXITCODE = 0
    if ("$args" -like 'system connection list*') {
        '[{"Name":"podman-machine-default","URI":"ssh://user@127.0.0.1:1/run/user/1000/podman/podman.sock","IsMachine":true},{"Name":"podman-machine-default-root","URI":"ssh://root@127.0.0.1:1/run/podman/podman.sock","IsMachine":true}]'
    } elseif ("$args" -like 'info*') { $global:swRootless }
}
$savedLocal = $env:LOCALAPPDATA; $savedConnection = $env:CONTAINER_CONNECTION
$env:LOCALAPPDATA = Join-Path ([IO.Path]::GetTempPath()) "suiteward-deploy-test-$([Guid]::NewGuid().ToString('N'))"
try {
    $refused = $null
    try { & $localPath -PodmanConnection podman-machine-default-root -Down } catch { $refused = $_.Exception.Message }
    Assert-That ($refused -match 'rootless') "a rootful connection was not refused (got: $refused)"
    Assert-That (-not ($global:swCalls | Where-Object { $_ -match '^(rm|secret|volume|network|run) ' })) 'podman changed something before the rootful refusal'
    Assert-That ($env:CONTAINER_CONNECTION -ceq 'podman-machine-default-root') 'the requested connection was not the one used'

    $global:swCalls.Clear(); $global:swRootless = 'true'
    & $localPath -Down -Purge
    Assert-That ($env:CONTAINER_CONNECTION -ceq 'podman-machine-default') 'the rootless machine connection was not selected'
    Assert-That (@($global:swCalls | Where-Object { $_ -match '^secret rm .*suiteward-database-owner-url' }).Count -eq 1) '-Down -Purge does not remove the owner URL secret'
} finally {
    Remove-Item -LiteralPath Function:\podman
    $env:LOCALAPPDATA = $savedLocal; $env:CONTAINER_CONNECTION = $savedConnection
}

if ($failures.Count) { throw "Deploy checks failed:`n  $($failures -join "`n  ")" }
Write-Host 'Deploy checks passed.'
