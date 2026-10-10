# Co-located target: runs the same two containers as the quadlets (deploy/quadlet) with `podman run` on this PC.
# Creates the network, volumes and secrets on first use. The database password is generated here, goes straight
# into the two Podman secrets and is never printed or written to a file.
#   ./deploy/local.ps1                                   start with the published image
#   ./deploy/local.ps1 -Image localhost/suiteward:local  start a locally built image
#   ./deploy/local.ps1 -Down                             remove the containers, keep the state
#   ./deploy/local.ps1 -Down -Purge                      also remove the volumes, secrets and network
# Keep the flags in step with the quadlets: read-only root, no capabilities, stop timeouts, memory limits.
#Requires -Version 7.2
param(
    [string]$Image = 'ghcr.io/ignisdevne/suiteward:deploy',
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

if ($Purge -and -not $Down) { throw '-Purge only applies together with -Down.' }
if ($Down) {
    Invoke-Podman rm --force --ignore --time 45 suiteward suiteward-db
    if ($Purge) {
        Invoke-Podman volume rm --force suiteward-data suiteward-db-data
        Invoke-Podman secret rm --ignore suiteward-db-password suiteward-database-url
        Invoke-Podman network rm --force suiteward
    }
    return
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
    --volume suiteward-data:/data `
    --publish 127.0.0.1:8081:8080 `
    $Image | Out-Null

Write-Host "Started suiteward-db and suiteward ($Image). Status: curl http://127.0.0.1:8081/status"
Write-Host 'If curl cannot connect (WSL port forwarding), run the smoke test inside the machine (PowerShell does not accept `<`):'
Write-Host '  cmd /c ''podman machine ssh "sh -s" < deploy\smoke.sh'''
