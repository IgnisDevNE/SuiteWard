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

$localPath = Join-Path $root 'deploy/local.ps1'
$local = Get-Lines $localPath
# A password in an argument shows in the process list. For every `podman run`/`podman exec` of the script (backtick continuations joined) the
# argument list may expand only these variables: nothing else, not even a subexpression, may reach a command line. A psql invocation must
# read its SQL on stdin (`exec -i ... psql ... -f -`) and never take -c.
$allowedArgumentVariables = 'Image', 'postgres', 'null'
function Get-ArgvViolations([string]$Script) {
    $code = ($Script -split '\r?\n' | Where-Object { $_ -notmatch '^\s*#' }) -join "`n"
    $violations = [Collections.Generic.List[string]]::new()
    $stdinPsql = 0
    foreach ($line in ($code -replace '`\r?\n\s*', ' ') -split "`n") {
        $call = [regex]::Match($line, '(?i)(&\s*podman|Invoke-Podman)\s+(run|exec)\b')
        if (-not $call.Success) { continue }
        $invocation = $line.Substring($call.Index)
        foreach ($expansion in [regex]::Matches(($invocation -replace "'[^']*'", "''"), '\$(\{\w+\}|\w+|\()')) {
            $name = $expansion.Value.TrimStart('$').Trim('{', '}')
            if ($name -notin $allowedArgumentVariables) { $violations.Add("$($expansion.Value) reaches a command line: $invocation") }
        }
        if ($invocation -cmatch '\bpsql\b') {
            if ($invocation -cmatch 'exec -i\b.*\bpsql\b.*\s-f -(\s|$)' -and $invocation -cnotmatch '\bpsql\b.*\s(-c|--command)\b') { $stdinPsql++ }
            else { $violations.Add("psql is not fed on stdin (podman exec -i ... psql -f -): $invocation") }
        }
    }
    if (-not $stdinPsql) { $violations.Add('no psql invocation is fed on stdin (podman exec -i ... psql -f -)') }
    $violations
}
$text = Get-Content -LiteralPath $localPath -Raw
foreach ($violation in Get-ArgvViolations $text) { $failures.Add($violation) }
# The check must bite: a password variable on a continuation line, a psql without -f -, and a subexpression are each refused.
Assert-That (@(Get-ArgvViolations $text.Replace('$Image migrate', "--env LEAK=`$appPassword ``$([Environment]::NewLine)    `$Image migrate")).Count -ge 1) 'the argument check accepts --env LEAK=$appPassword on a continuation line'
Assert-That (@(Get-ArgvViolations $text.Replace(' -f -', '')).Count -ge 1) 'the argument check accepts a psql that is not fed on stdin'
Assert-That (@(Get-ArgvViolations $text.Replace('--memory 256m `', '--memory $($appPassword) `')).Count -ge 1) 'the argument check accepts a subexpression in an argument list'
Assert-That ($text -cmatch "SET log_statement = 'none'; SET log_min_error_statement = 'panic'; SET log_min_duration_statement = -1;") 'the role SQL does not switch every kind of statement logging off first'
$migrateAt = $text.IndexOf('--name suiteward-migrate '); $serveAt = $text.IndexOf('--name suiteward --network')
Assert-That ($migrateAt -ge 0 -and $migrateAt -lt $serveAt) 'migrate does not run before serve'
Assert-That ([regex]::IsMatch($text, 'migrate\r?\n\s*if \(\$LASTEXITCODE -ne 0\) \{ throw')) 'a failed migrate does not stop the script'
Assert-That ($text.Contains('podman machine ssh --username user "sh -s"')) 'the smoke instruction does not use the rootless ssh user'

# Behavior with a fake podman (a function the script sees): a rootful connection is refused before anything is created; -Down -Purge removes the owner secret too.
$global:swCalls = [Collections.Generic.List[string]]::new()
$global:swRootless = 'false'
$global:swSeen = [Collections.Generic.List[string]]::new()
function podman {
    $global:swCalls.Add("$args"); $global:swSeen.Add("$env:CONTAINER_CONNECTION")
    $global:LASTEXITCODE = 0
    if ("$args" -like 'system connection list*') {
        '[{"Name":"podman-machine-default","URI":"ssh://user@127.0.0.1:1/run/user/1000/podman/podman.sock","IsMachine":true},{"Name":"podman-machine-default-root","URI":"ssh://root@127.0.0.1:1/run/podman/podman.sock","IsMachine":true}]'
    } elseif ("$args" -like 'info*') { $global:swRootless }
}
$savedLocal = $env:LOCALAPPDATA; $savedConnection = $env:CONTAINER_CONNECTION; $savedHost = $env:CONTAINER_HOST
Remove-Item Env:CONTAINER_CONNECTION -ErrorAction Ignore
$env:CONTAINER_HOST = 'ssh://caller-host'
$env:LOCALAPPDATA = Join-Path ([IO.Path]::GetTempPath()) "suiteward-deploy-test-$([Guid]::NewGuid().ToString('N'))"
try {
    $refused = $null
    try { & $localPath -PodmanConnection podman-machine-default-root -Down } catch { $refused = $_.Exception.Message }
    Assert-That ($refused -match 'rootless') "a rootful connection was not refused (got: $refused)"
    Assert-That (-not ($global:swCalls | Where-Object { $_ -match '^(rm|secret|volume|network|run) ' })) 'podman changed something before the rootful refusal'
    Assert-That ($global:swSeen -ccontains 'podman-machine-default-root') 'the requested connection was not the one used'
    Assert-That ($null -eq $env:CONTAINER_CONNECTION -and $env:CONTAINER_HOST -ceq 'ssh://caller-host') 'a refused run left the caller''s Podman environment changed'

    $global:swSeen.Clear(); $global:swCalls.Clear(); $global:swRootless = 'true'
    & $localPath -Down -Purge
    if ($IsWindows) { Assert-That ($global:swSeen -ccontains 'podman-machine-default') 'the rootless machine connection was not selected' }
    Assert-That ($null -eq $env:CONTAINER_CONNECTION -and $env:CONTAINER_HOST -ceq 'ssh://caller-host') 'the run left the caller''s Podman environment changed'
    Assert-That (@($global:swCalls | Where-Object { $_ -match '^secret rm .*suiteward-database-owner-url' }).Count -eq 1) '-Down -Purge does not remove the owner URL secret'
} finally {
    Remove-Item -LiteralPath Function:\podman
    $env:LOCALAPPDATA = $savedLocal; $env:CONTAINER_CONNECTION = $savedConnection; $env:CONTAINER_HOST = $savedHost
}

if ($failures.Count) { throw "Deploy checks failed:`n  $($failures -join "`n  ")" }
Write-Host 'Deploy checks passed.'
