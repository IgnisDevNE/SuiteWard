# Co-located target: runs the same two containers as the quadlets (deploy/quadlet) with `podman run` on this PC.
# Creates the network, volumes and secrets on first use. The database password is generated here, goes straight
# into the two Podman secrets and is never printed or written to a file.
#   ./deploy/local.ps1                                   start with the published image, without GitHub (the MCP listener still runs)
#   ./deploy/local.ps1 -Image localhost/suiteward:local  start a locally built image
#   ./deploy/local.ps1 -GitHubAppKeyFile <pem> -GitHubAppId <id> -GitHubInstallationId <id> -GitHubRepositories owner/name[,owner/name]
#                                                        start with the GitHub synchronization
#   ./deploy/local.ps1 -Down                             remove the containers, keep the state
#   ./deploy/local.ps1 -Down -Purge                      also remove the volumes, secrets, the MCP token copy and the network
# GitHub is optional, as in the product: it is configured only when all three non-secret values are given, and they are not stored,
# so give them on every run that should have GitHub (a run without them replaces the container with one without GitHub). The App key
# file is read only when the secret suiteward-github-app-key does not exist yet; it is copied into the secret and not kept elsewhere.
# Without GitHub, run the smoke test with SMOKE_GITHUB=0.
# The MCP token is generated here when its secret does not exist, goes into the secret suiteward-mcp-token and into a copy for the
# client at $env:LOCALAPPDATA\SuiteWard\mcp-token (readable by this user only, outside the repository). It is never printed.
# One stack per machine: the names and the ports 8081 and 8091 are fixed, a second run replaces the first and -Purge removes its volumes.
# Keep the flags in step with the quadlets: read-only root, no capabilities, stop timeouts, memory limits.
# D-TIER: this PC is the co-located tier (the agent shares it with the owner), declared here and never detected.
#Requires -Version 7.2
param(
    [string]$Image = 'ghcr.io/ignisdevne/suiteward:deploy',
    [string]$GitHubAppKeyFile,
    [string]$GitHubAppId,
    [string]$GitHubInstallationId,
    [string]$GitHubRepositories,
    [switch]$Down,
    [switch]$Purge
)
$ErrorActionPreference = 'Stop'
# Same digest as deploy/quadlet/suiteward-db.container.
$postgres = 'docker.io/library/postgres:18.6-trixie@sha256:5a5a84b19854a9ffaa54082c166ff4ec27473a361e496e5ea167f298f2da9722'

function Invoke-Podman {
    & podman @args
    if ($LASTEXITCODE -ne 0) { throw "podman $($args[0]) failed with exit code $LASTEXITCODE" }
}
# Piping a string to a native command appends a Windows line ending to the secret, so write the bytes ourselves.
function New-PodmanSecret($Name, $Value) {
    $info = [Diagnostics.ProcessStartInfo]::new('podman')
    foreach ($arg in 'secret', 'create', $Name, '-') { $info.ArgumentList.Add($arg) }
    $info.RedirectStandardInput = $info.RedirectStandardOutput = $info.RedirectStandardError = $true
    $info.StandardInputEncoding = [Text.UTF8Encoding]::new($false)
    $process = [Diagnostics.Process]::Start($info)
    $process.StandardInput.Write($Value)
    $process.StandardInput.Close()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) { throw "Creating secret $Name failed: $($process.StandardError.ReadToEnd())" }
}
function Test-Podman { & podman @args *> $null; $LASTEXITCODE -eq 0 }

$tokenFile = Join-Path $env:LOCALAPPDATA 'SuiteWard\mcp-token'

if ($Purge -and -not $Down) { throw '-Purge only applies together with -Down.' }
if ($Down) {
    Invoke-Podman rm --force --ignore --time 45 suiteward suiteward-db
    if ($Purge) {
        Invoke-Podman volume rm --force suiteward-data suiteward-db-data
        Invoke-Podman secret rm --ignore suiteward-db-password suiteward-database-url suiteward-github-app-key suiteward-mcp-token
        Invoke-Podman network rm --force suiteward
        Remove-Item -LiteralPath $tokenFile -Force -ErrorAction Ignore
    }
    return
}

# GitHub is all or nothing, like the service's own settings; check before anything is created.
$githubValues = $GitHubAppId, $GitHubInstallationId, $GitHubRepositories
$github = ($githubValues | Where-Object { $_ }).Count -eq 3
if (-not $github -and ($GitHubAppKeyFile -or ($githubValues | Where-Object { $_ }))) {
    throw 'GitHub needs -GitHubAppId, -GitHubInstallationId and -GitHubRepositories together (and -GitHubAppKeyFile when its secret does not exist yet).'
}

if (-not (Test-Podman network exists suiteward)) { Invoke-Podman network create suiteward | Out-Null }
foreach ($volume in 'suiteward-data', 'suiteward-db-data') {
    if (-not (Test-Podman volume exists $volume)) { Invoke-Podman volume create $volume | Out-Null }
}

# Both secrets come from one generated password so they cannot disagree. An existing pair is kept: the database
# was initialised with it, and replacing only one of them would lock the service out.
$haveDb = Test-Podman secret exists suiteward-db-password
$haveUrl = Test-Podman secret exists suiteward-database-url
if ($haveDb -ne $haveUrl) { throw 'Only one of the two secrets exists. Run with -Down -Purge to start over.' }
if (-not $haveDb) {
    $password = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(24)).ToLowerInvariant()
    New-PodmanSecret suiteward-db-password $password
    # sslmode=disable: the database listens only on the private suiteward network and its image has no certificate.
    New-PodmanSecret suiteward-database-url "postgres://suiteward:$password@suiteward-db:5432/suiteward?sslmode=disable"
    Remove-Variable password
}

# The MCP token is independent of GitHub. Its copy for the client is written first and restricted to this user, so a failure
# after it leaves a file the next run overwrites.
if (-not (Test-Podman secret exists suiteward-mcp-token)) {
    $token = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).ToLowerInvariant()
    New-Item -ItemType Directory -Force -Path (Split-Path $tokenFile) | Out-Null
    New-Item -ItemType File -Force -Path $tokenFile | Out-Null
    & icacls $tokenFile /inheritance:r /grant:r "$($env:USERNAME):F" *> $null
    if ($LASTEXITCODE -ne 0) { throw "Could not restrict $tokenFile to this user." }
    [IO.File]::WriteAllText($tokenFile, $token)
    New-PodmanSecret suiteward-mcp-token $token
    Remove-Variable token
} elseif (-not (Test-Path -LiteralPath $tokenFile)) {
    Write-Warning "The secret suiteward-mcp-token exists but $tokenFile does not. Run with -Down -Purge to generate a new token."
}

$githubArgs = @()
if ($github) {
    if (-not (Test-Podman secret exists suiteward-github-app-key)) {
        if (-not $GitHubAppKeyFile) { throw 'The secret suiteward-github-app-key does not exist: pass -GitHubAppKeyFile.' }
        # .NET resolves a relative path against the process directory, not PowerShell's current location.
        $pem = [IO.File]::ReadAllText((Resolve-Path -LiteralPath $GitHubAppKeyFile).ProviderPath)
        if ($pem -notmatch '-----BEGIN (RSA )?PRIVATE KEY-----') { throw "$GitHubAppKeyFile is not a PEM private key (PKCS#1 or PKCS#8)." }
        New-PodmanSecret suiteward-github-app-key $pem
        Remove-Variable pem
    }
    $githubArgs = '--env', "SUITEWARD_GITHUB_APP_ID=$GitHubAppId", '--env', "SUITEWARD_GITHUB_INSTALLATION_ID=$GitHubInstallationId",
        '--env', "SUITEWARD_GITHUB_REPOSITORIES=$GitHubRepositories",
        '--env', 'SUITEWARD_GITHUB_APP_KEY_FILE=/run/secrets/github-app-key',
        '--secret', 'suiteward-github-app-key,type=mount,target=/run/secrets/github-app-key,uid=65532,mode=0400'
}

Invoke-Podman run --detach --replace --name suiteward-db --network suiteward --restart always `
    --read-only --cap-drop all --security-opt no-new-privileges --user 999:999 `
    --tmpfs /run/postgresql --tmpfs /tmp --memory 384m --stop-timeout 30 `
    --env POSTGRES_USER=suiteward --env POSTGRES_DB=suiteward --env POSTGRES_PASSWORD_FILE=/run/secrets/postgres-password `
    --secret 'suiteward-db-password,type=mount,target=/run/secrets/postgres-password,uid=999,mode=0400' `
    --volume suiteward-db-data:/var/lib/postgresql `
    --health-cmd 'pg_isready -U suiteward -d suiteward' --health-interval 5s --health-timeout 3s --health-retries 12 `
    $postgres | Out-Null

# The quadlets cannot gate on health in Podman 4.9; here the wait is cheap, so the service starts against a ready database.
$deadline = (Get-Date).AddSeconds(90)
while ((& podman inspect --format '{{.State.Health.Status}}' suiteward-db) -ne 'healthy') {
    if ((Get-Date) -gt $deadline) { throw 'suiteward-db did not become healthy within 90 s (podman logs suiteward-db).' }
    Start-Sleep -Seconds 1
}

Invoke-Podman run --detach --replace --name suiteward --network suiteward --restart always `
    --read-only --cap-drop all --security-opt no-new-privileges --memory 256m --stop-timeout 45 `
    --env SUITEWARD_DATABASE_URL_FILE=/run/secrets/database-url `
    --secret 'suiteward-database-url,type=mount,target=/run/secrets/database-url,uid=65532,mode=0400' `
    --env SUITEWARD_MCP_TOKEN_FILE=/run/secrets/mcp-token `
    --secret 'suiteward-mcp-token,type=mount,target=/run/secrets/mcp-token,uid=65532,mode=0400' `
    --env SUITEWARD_INSTALLATION_TIER=co-located `
    @githubArgs `
    --volume suiteward-data:/data `
    --publish 127.0.0.1:8081:8080 --publish 127.0.0.1:8091:8090 `
    $Image | Out-Null

Write-Host "Started suiteward-db and suiteward ($Image), GitHub $($github ? 'configured' : 'not configured')."
Write-Host 'The port is published inside the Podman machine and may not be reachable from this PC (it was not on the WSL machine this was tested on). Run the smoke test inside the machine:'
Write-Host '  Get-Content -Raw deploy/smoke.sh | podman machine ssh "sh -s"'
Write-Host 'Add SMOKE_VERSION=sha-<commit> before sh to also assert the version /status reports, and SMOKE_GITHUB=0 for a start without GitHub.'
Write-Host "The MCP listener is on 127.0.0.1:8091 inside the machine; the token for a client is in $tokenFile (it is not printed)."
