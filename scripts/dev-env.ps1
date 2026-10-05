# Shared development helpers. Dot-source from the entry point or infrastructure tests.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Pinned archive tools are shared by all checkouts of this user; caches, state, and the database stay per checkout.
function Get-ToolsRoot {
    if ($env:SUITEWARD_TOOLS_DIR) { return [IO.Path]::GetFullPath($env:SUITEWARD_TOOLS_DIR) }
    if ($IsWindows) {
        if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is not set; set SUITEWARD_TOOLS_DIR to choose the shared tool cache.' }
        return [IO.Path]::GetFullPath((Join-Path $env:LOCALAPPDATA 'SuiteWard\tools'))
    }
    $cacheHome = if ($env:XDG_CACHE_HOME) { $env:XDG_CACHE_HOME } elseif ($env:HOME) { Join-Path $env:HOME '.cache' } else { throw 'HOME is not set; set SUITEWARD_TOOLS_DIR to choose the shared tool cache.' }
    return [IO.Path]::GetFullPath((Join-Path $cacheHome 'suiteward/tools'))
}

function Get-DevContext {
    param([string]$Root = (Split-Path $PSScriptRoot -Parent))
    $rootPath = [IO.Path]::GetFullPath($Root).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    if (-not (Test-Path -LiteralPath (Join-Path $rootPath '.go-version'))) { throw 'Expected a SuiteWard checkout with .go-version.' }
    $identity = if ($IsWindows) { $rootPath.ToLowerInvariant() } else { $rootPath }
    $hash = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($identity))).ToLowerInvariant().Substring(0, 16)
    if ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture -ne 'X64') { throw 'The initial development bootstrap supports Windows/Linux x64.' }
    $platform = if ($IsWindows) { 'windows-amd64' } elseif ($IsLinux) { 'linux-amd64' } else { throw 'Use Windows or Linux with PowerShell 7.' }
    $manifest = Get-Content -LiteralPath (Join-Path $rootPath 'dev/tools.json') -Raw | ConvertFrom-Json -AsHashtable
    if ($manifest.go.version -ne (Get-Content -LiteralPath (Join-Path $rootPath '.go-version') -Raw).Trim()) { throw 'Go version declarations disagree.' }
    return [pscustomobject]@{
        Root = $rootPath; ID = $hash; Platform = $platform; Manifest = $manifest
        Tools = Get-ToolsRoot; Cache = Join-Path $rootPath '.cache'
        State = Join-Path $rootPath '.local/dev'; Container = "suiteward-$hash-postgres"; Volume = "suiteward-$hash-pgdata"
        Suffix = $(if ($IsWindows) { '.exe' } else { '' })
    }
}

function Get-ToolPath {
    param($Context, [string]$Name)
    $version = $Context.Manifest[$Name].version
    $dir = Join-Path $Context.Tools "$Name/$version/$($Context.Platform)"
    $binary = if ($Name -eq 'go') { "go/bin/go$($Context.Suffix)" } else { "$Name$($Context.Suffix)" }
    return Join-Path $dir $binary
}

function Enter-DevEnvironment {
    param($Context)
    $values = @{
        GOROOT = Split-Path (Split-Path (Get-ToolPath $Context 'go') -Parent) -Parent
        GOPATH = Join-Path $Context.Cache 'gopath'; GOMODCACHE = Join-Path $Context.Cache 'go-mod'
        GOCACHE = Join-Path $Context.Cache 'go-build'; GOBIN = Join-Path $Context.Tools 'bin'
        GOTMPDIR = Join-Path $Context.Cache 'go-tmp'; GOTOOLCHAIN = 'local'; GOENV = 'off'; GOWORK = 'off'
        GOFLAGS = ''; GOOS = ''; GOARCH = ''
    }
    $values.PATH = (Split-Path (Get-ToolPath $Context 'go') -Parent) + [IO.Path]::PathSeparator + $env:PATH
    foreach ($name in @('GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOBIN', 'GOTMPDIR')) { New-Item -ItemType Directory -Path $values[$name] -Force | Out-Null }
    $previous = @{}
    foreach ($entry in $values.GetEnumerator()) {
        $previous[$entry.Key] = [Environment]::GetEnvironmentVariable($entry.Key, 'Process')
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
    }
    return $previous
}

function Restore-DevEnvironment {
    param([hashtable]$Previous)
    foreach ($entry in $Previous.GetEnumerator()) {
        if ($null -eq $entry.Value) { Remove-Item -LiteralPath "Env:$($entry.Key)" -ErrorAction SilentlyContinue }
        else { [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process') }
    }
}

function Invoke-ToolConfigScope {
    param($Context, [scriptblock]$Action)
    # Go telemetry/configuration uses the OS config directory, not GOROOT or GOENV.
    # Scope that directory only around development tools, never around Podman.
    $name = if ($IsWindows) { 'APPDATA' } else { 'XDG_CONFIG_HOME' }
    $configDir = Join-Path $Context.Cache 'tool-config'
    New-Item -ItemType Directory -Path $configDir -Force | Out-Null
    $saved = [Environment]::GetEnvironmentVariable($name, 'Process')
    try {
        [Environment]::SetEnvironmentVariable($name, $configDir, 'Process')
        & $Action
    } finally { Restore-DevEnvironment @{ $name = $saved } }
}

function Assert-ArchiveChecksum {
    param([string]$Path, [string]$Expected)
    if ($Expected -cnotmatch '^[a-f0-9]{64}$') { throw 'Invalid pinned SHA-256.' }
    if ((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() -cne $Expected) { throw "Checksum mismatch: $([IO.Path]::GetFileName($Path)). Delete the cached archive and retry." }
}

function Get-VerifiedArchive {
    param($Context, $Asset)
    $downloadDir = Join-Path $Context.Tools 'downloads'
    New-Item -ItemType Directory -Path $downloadDir -Force | Out-Null
    $uri = [Uri]$Asset.url
    if ($uri.Scheme -ne 'https') { throw 'Tool downloads require HTTPS.' }
    $target = Join-Path $downloadDir ([IO.Path]::GetFileName($uri.AbsolutePath))
    if (-not (Test-Path -LiteralPath $target)) {
        $partial = "$target.$([Guid]::NewGuid().ToString('N')).partial"
        Write-Host "Downloading $([IO.Path]::GetFileName($target))"
        try {
            Invoke-WebRequest -Uri $uri -OutFile $partial -TimeoutSec 300
            Assert-ArchiveChecksum $partial $Asset.sha256
            Move-Item -LiteralPath $partial -Destination $target
        } finally {
            if (Test-Path -LiteralPath $partial) { Remove-Item -LiteralPath $partial }
        }
    }
    Assert-ArchiveChecksum $target $Asset.sha256
    return $target
}

function Assert-ToolVersion {
    param($Context, [string]$Name)
    $exe = Get-ToolPath $Context $Name
    if (-not (Test-Path -LiteralPath $exe)) { throw "$Name is not installed in the shared tool cache ($($Context.Tools)). Run: ./scripts/dev.ps1 setup" }
    [string[]]$versionArgs = @(switch ($Name) { 'go' { 'version' }; 'sqlc' { 'version' }; 'actionlint' { '-version' }; 'govulncheck' { '-version' } })
    $output = Invoke-ToolConfigScope $Context {
        & $exe @versionArgs 2>&1
        if ($LASTEXITCODE -ne 0) { throw "$Name version check failed." }
    }
    if (($output -join "`n") -notmatch ('(?<![0-9])' + [regex]::Escape($Context.Manifest[$Name].version) + '(?![0-9.])')) { throw "$Name does not report its pinned version." }
}

function Invoke-WithToolsLock {
    param($Context, [scriptblock]$Action, [int]$TimeoutSeconds = 900)
    # One lock per shared cache: setups from different checkouts wait for each other instead of failing.
    New-Item -ItemType Directory -Path $Context.Tools -Force | Out-Null
    $path = Join-Path $Context.Tools '.setup.lock'
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $lock = $null
    while (-not $lock) {
        try { $lock = [IO.File]::Open($path, 'OpenOrCreate', 'ReadWrite', 'None') }
        catch [IO.IOException] {
            if ([DateTime]::UtcNow -ge $deadline) { throw 'Timed out waiting for the shared tool cache lock. Another setup may still be running.' }
            Start-Sleep -Milliseconds 200
        }
    }
    try { & $Action } finally { $lock.Dispose() }
}

function Install-ArchiveTool {
    param($Context, [string]$Name)
    $exe = Get-ToolPath $Context $Name
    $toolDir = Join-Path $Context.Tools "$Name/$($Context.Manifest[$Name].version)/$($Context.Platform)"
    $receipt = Join-Path $toolDir '.ready'
    $asset = $Context.Manifest[$Name].assets[$Context.Platform]
    $toolsPrefix = [IO.Path]::GetFullPath($Context.Tools) + [IO.Path]::DirectorySeparatorChar
    if (-not [IO.Path]::GetFullPath($toolDir).StartsWith($toolsPrefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'Tool installation escaped the shared tool cache.' }
    Invoke-WithToolsLock $Context {
        # Re-check under the lock: a setup from another checkout may have installed the tool while this one waited.
        if (Test-Path -LiteralPath $toolDir) {
            if (-not (Test-Path -LiteralPath $receipt) -or (Get-Content -LiteralPath $receipt -Raw).Trim() -cne $asset.sha256) { throw "Incomplete or mismatched $Name installation at $toolDir. Move it aside and rerun setup." }
            Assert-ToolVersion $Context $Name
            return
        }
        $archive = Get-VerifiedArchive $Context $asset
        $parent = Split-Path $toolDir -Parent
        New-Item -ItemType Directory -Path $parent -Force | Out-Null
        $staging = Join-Path $parent ".install-$([Guid]::NewGuid().ToString('N'))"
        New-Item -ItemType Directory -Path $staging | Out-Null
        # Interrupted staging directories are never treated as usable installations.
        if ($archive.EndsWith('.zip')) { Expand-Archive -LiteralPath $archive -DestinationPath $staging }
        else {
            & tar -xzf $archive -C $staging
            if ($LASTEXITCODE -ne 0) { throw "Cannot extract $Name." }
        }
        $relativeExe = [IO.Path]::GetRelativePath($toolDir, $exe)
        if (-not (Test-Path -LiteralPath (Join-Path $staging $relativeExe))) { throw "Archive did not contain $Name." }
        Set-Content -LiteralPath (Join-Path $staging '.ready') -Value $asset.sha256 -Encoding utf8
        Move-Item -LiteralPath $staging -Destination $toolDir
        Assert-ToolVersion $Context $Name
    }
}

function Install-DevTools {
    param($Context)
    New-Item -ItemType Directory -Path $Context.State -Force | Out-Null
    $lock = $null
    try {
        try { $lock = [IO.File]::Open((Join-Path $Context.State 'setup.lock'), 'OpenOrCreate', 'ReadWrite', 'None') }
        catch { throw 'Another setup is running in this checkout. Wait for it to finish.' }
        foreach ($name in @('go', 'sqlc', 'actionlint')) { Install-ArchiveTool $Context $name }
        $exe = Get-ToolPath $Context 'govulncheck'
        Invoke-WithToolsLock $Context {
            if (-not (Test-Path -LiteralPath $exe)) {
                $toolDir = Split-Path $exe -Parent
                New-Item -ItemType Directory -Path $toolDir -Force | Out-Null
                $savedBin = $env:GOBIN
                $savedProxy = $env:GOPROXY; $savedSum = $env:GOSUMDB; $savedNoSum = $env:GONOSUMDB; $savedPrivate = $env:GOPRIVATE
                try {
                    $env:GOBIN = $toolDir
                    $env:GOPROXY = 'https://proxy.golang.org'; $env:GOSUMDB = 'sum.golang.org'; $env:GONOSUMDB = ''; $env:GOPRIVATE = ''
                    Invoke-ToolConfigScope $Context {
                        & (Get-ToolPath $Context 'go') install "golang.org/x/vuln/cmd/govulncheck@v$($Context.Manifest.govulncheck.version)"
                        if ($LASTEXITCODE -ne 0) { throw 'Could not install pinned govulncheck.' }
                    }
                } finally { Restore-DevEnvironment @{ GOBIN = $savedBin; GOPROXY = $savedProxy; GOSUMDB = $savedSum; GONOSUMDB = $savedNoSum; GOPRIVATE = $savedPrivate } }
            }
        }
        Assert-ToolVersion $Context 'govulncheck'
        if (Test-Path -LiteralPath (Join-Path $Context.Root '.tools')) { Write-Host 'Note: this checkout''s .tools/ directory is no longer used; tools now live in the shared cache. You can delete it.' }
    } finally { if ($lock) { $lock.Dispose() } }
}

function Invoke-PodmanJson {
    param([string[]]$Arguments)
    $output = & podman @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Podman failed: $($Arguments[0])" }
    return ($output -join "`n" | ConvertFrom-Json -AsHashtable)
}

function Assert-DatabaseOwnership {
    param($Context, $Labels)
    if (-not $Labels -or $Labels['io.suiteward.dev.owner'] -cne $Context.ID -or $Labels['io.suiteward.dev.managed'] -cne 'true') { throw 'Refusing to use a database resource owned by another checkout.' }
}

function Select-DevPodmanConnection {
    param($Context, [string]$Requested = '')
    if (-not (Get-Command podman -ErrorAction SilentlyContinue)) { throw 'Podman is a host prerequisite. Install/start Podman, then retry.' }
    New-Item -ItemType Directory -Path $Context.State -Force | Out-Null
    $selectionFile = Join-Path $Context.State 'podman-connection.txt'
    $saved = if (Test-Path -LiteralPath $selectionFile) { (Get-Content -LiteralPath $selectionFile -Raw).Trim() } else { '' }
    if ($Requested -and $saved -and $Requested -cne $saved) { throw 'This checkout already selected a Podman connection. Migrate its database explicitly before changing connections.' }
    $selected = if ($Requested) { $Requested } else { $saved }
    if (-not $selected -and $IsWindows) {
        $connections = @(Invoke-PodmanJson @('system', 'connection', 'list', '--format', 'json'))
        $candidates = @($connections | Where-Object { $_.IsMachine -and $_.URI -match '/run/user/[0-9]+/podman/podman.sock$' })
        if ($candidates.Count -ne 1) { throw 'Select an existing rootless machine connection with -PodmanConnection NAME; setup does not change host defaults.' }
        $selected = $candidates[0].Name
    }
    if ($selected) {
        $env:CONTAINER_CONNECTION = $selected
        Remove-Item Env:CONTAINER_HOST -ErrorAction SilentlyContinue
    }
    $rootless = & podman info --format '{{.Host.Security.Rootless}}'
    if ($LASTEXITCODE -ne 0) { throw 'Podman connection is unavailable; no new selection was saved.' }
    if ($IsWindows -and $rootless -cne 'true') { throw 'Use a running rootless Podman connection on Windows for reliable loopback forwarding.' }
    if ($selected) { Set-Content -LiteralPath $selectionFile -Value $selected -Encoding utf8 }
}

function Get-DevContainer {
    param($Context)
    & podman container exists $Context.Container
    if ($LASTEXITCODE -eq 1) { return $null }
    if ($LASTEXITCODE -ne 0) { throw 'Podman is unavailable. Start the existing Podman machine, then retry.' }
    $info = @(Invoke-PodmanJson @('container', 'inspect', $Context.Container))[0]
    Assert-DatabaseOwnership $Context $info.Config.Labels
    $expectedImage = $Context.Manifest.postgres.image -replace ':[^/:]+@', '@'
    if (($info.Config.Image -replace ':[^/:]+@', '@') -cne $expectedImage) { throw 'Database image differs from the pin. Migrate explicitly; setup will not replace existing data.' }
    $mount = @($info.Mounts | Where-Object { $_.Type -eq 'volume' -and $_.Name -eq $Context.Volume -and $_.Destination -eq '/var/lib/postgresql' })
    if ($mount.Count -ne 1) { throw 'Database does not mount the checkout-owned volume.' }
    return $info
}

function Start-DevDatabase {
    param($Context)
    if (-not (Get-Command podman -ErrorAction SilentlyContinue)) { throw 'Podman is a host prerequisite. Install/start Podman, then retry.' }
    New-Item -ItemType Directory -Path $Context.State -Force | Out-Null
    $lock = $null
    try {
        try { $lock = [IO.File]::Open((Join-Path $Context.State 'database.lock'), 'OpenOrCreate', 'ReadWrite', 'None') }
        catch { throw 'Another database operation is running in this checkout.' }
        $container = Get-DevContainer $Context
        $envFile = Join-Path $Context.State 'postgres.env'
        if (-not $container) {
            & podman volume exists $Context.Volume
            if ($LASTEXITCODE -eq 0) {
                $volume = @(Invoke-PodmanJson @('volume', 'inspect', $Context.Volume))[0]
                Assert-DatabaseOwnership $Context $volume.Labels
                if (-not (Test-Path -LiteralPath $envFile)) { throw 'Existing data volume has no local credential file. Restore the credential; setup will not reset the database.' }
            } elseif ($LASTEXITCODE -eq 1) {
                & podman volume create --label "io.suiteward.dev.owner=$($Context.ID)" --label 'io.suiteward.dev.managed=true' $Context.Volume | Out-Null
                if ($LASTEXITCODE -ne 0) { throw 'Cannot create the checkout database volume.' }
            } else { throw 'Cannot inspect the database volume.' }
            if (-not (Test-Path -LiteralPath $envFile)) {
                $password = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).ToLowerInvariant()
                Set-Content -LiteralPath $envFile -Value "POSTGRES_USER=suiteward`nPOSTGRES_DB=suiteward`nPOSTGRES_PASSWORD=$password`nPOSTGRES_INITDB_ARGS=--auth-host=scram-sha-256" -Encoding utf8NoBOM
                if (-not $IsWindows) { & chmod 600 $envFile; if ($LASTEXITCODE -ne 0) { throw 'Cannot protect local database credentials.' } }
            }
            & podman run --detach --name $Context.Container --label "io.suiteward.dev.owner=$($Context.ID)" --label 'io.suiteward.dev.managed=true' --env-file $envFile --publish '127.0.0.1::5432' --volume "$($Context.Volume):/var/lib/postgresql" --health-cmd 'pg_isready -h 127.0.0.1 -U suiteward -d suiteward' --health-interval 5s --health-timeout 3s --health-retries 12 $Context.Manifest.postgres.image | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'Database start failed; existing volumes are preserved.' }
        } elseif (-not $container.State.Running) {
            & podman start $Context.Container | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'Cannot restart the checkout database.' }
        }
        for ($attempt = 0; $attempt -lt 60; $attempt++) {
            & podman exec $Context.Container pg_isready -q -h 127.0.0.1 -U suiteward -d suiteward 2>$null
            if ($LASTEXITCODE -eq 0) { break }
            if ($attempt -eq 59) { throw 'PostgreSQL did not become ready within 60 seconds.' }
            Start-Sleep -Seconds 1
        }
        $container = Get-DevContainer $Context
        $binding = @($container.NetworkSettings.Ports['5432/tcp'])[0]
        if ($binding.HostIp -ne '127.0.0.1') { throw 'Database must be exposed on loopback only.' }
        if (-not (Test-Path -LiteralPath $envFile)) { throw 'Missing local database credential file.' }
        $passwordLine = @(Get-Content -LiteralPath $envFile | Where-Object { $_.StartsWith('POSTGRES_PASSWORD=') })
        if ($passwordLine.Count -ne 1) { throw 'Invalid local database credential file.' }
        $password = $passwordLine[0].Substring('POSTGRES_PASSWORD='.Length)
        $dbState = [ordered]@{ owner = $Context.ID; container = $Context.Container; volume = $Context.Volume; host = '127.0.0.1'; port = [int]$binding.HostPort; database = 'suiteward'; username = 'suiteward'; image = $Context.Manifest.postgres.image }
        $dbState | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $Context.State 'postgres.json') -Encoding utf8
        $urlFile = Join-Path $Context.State 'database-url.txt'
        Set-Content -LiteralPath $urlFile -Value "postgresql://suiteward:${password}@127.0.0.1:$($binding.HostPort)/suiteward?sslmode=disable" -Encoding utf8NoBOM
        if (-not $IsWindows) { & chmod 600 $urlFile }
        Write-Host "PostgreSQL ready: 127.0.0.1:$($binding.HostPort) ($($Context.Container))"
    } finally { if ($lock) { $lock.Dispose() } }
}

function Remove-DevDatabase {
    param($Context)
    New-Item -ItemType Directory -Path $Context.State -Force | Out-Null
    $lock = $null
    try {
        try { $lock = [IO.File]::Open((Join-Path $Context.State 'database.lock'), 'OpenOrCreate', 'ReadWrite', 'None') }
        catch { throw 'Another database operation is running in this checkout.' }
        # Verify ownership of everything first; nothing is removed unless both resources belong to this checkout.
        & podman container exists $Context.Container
        $containerExists = switch ($LASTEXITCODE) { 0 { $true } 1 { $false } default { throw 'Podman is unavailable. Start the existing Podman machine, then retry.' } }
        if ($containerExists) { Assert-DatabaseOwnership $Context (@(Invoke-PodmanJson @('container', 'inspect', $Context.Container))[0]).Config.Labels }
        & podman volume exists $Context.Volume
        $volumeExists = switch ($LASTEXITCODE) { 0 { $true } 1 { $false } default { throw 'Cannot inspect the database volume.' } }
        if ($volumeExists) { Assert-DatabaseOwnership $Context (@(Invoke-PodmanJson @('volume', 'inspect', $Context.Volume))[0]).Labels }
        if ($containerExists) {
            & podman rm --force $Context.Container | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'Could not remove the checkout database container.' }
        }
        if ($volumeExists) {
            & podman volume rm $Context.Volume | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'Could not remove the checkout database volume.' }
        }
        foreach ($file in @('postgres.env', 'postgres.json', 'database-url.txt')) {
            $path = Join-Path $Context.State $file
            if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force }
        }
    } finally { if ($lock) { $lock.Dispose() } }
}

function Test-DevDatabase {
    param($Context)
    $container = Get-DevContainer $Context
    if (-not $container -or -not $container.State.Running) { throw 'The checkout database is not running. Run: ./scripts/dev.ps1 db-start' }
    $envFile = Join-Path $Context.State 'postgres.env'
    $passwordLine = @(Get-Content -LiteralPath $envFile | Where-Object { $_.StartsWith('POSTGRES_PASSWORD=') })
    if ($passwordLine.Count -ne 1) { throw 'Invalid local database credential file.' }
    $password = $passwordLine[0].Substring('POSTGRES_PASSWORD='.Length)
    $sql = 'BEGIN; CREATE TEMP TABLE suiteward_probe(value integer NOT NULL); INSERT INTO suiteward_probe VALUES (42); SELECT value FROM suiteward_probe; ROLLBACK;'
    # Send the local credential via stdin, never as a command-line argument. TCP forces SCRAM authentication.
    $result = "$password`n$sql" | & podman exec --interactive $Context.Container sh -c 'IFS= read -r PGPASSWORD; export PGPASSWORD; exec psql -X -w -h 127.0.0.1 -U suiteward -d suiteward -v ON_ERROR_STOP=1 -At'
    if ($LASTEXITCODE -ne 0 -or $result -notcontains '42') { throw 'PostgreSQL transactional smoke test failed.' }
    $binding = @($container.NetworkSettings.Ports['5432/tcp'])[0]
    $client = [Net.Sockets.TcpClient]::new()
    try {
        $connect = $client.ConnectAsync('127.0.0.1', [int]$binding.HostPort)
        try { if (-not $connect.Wait(5000)) { throw 'timeout' } }
        catch { throw 'PostgreSQL host port is unreachable. On Windows, use the checkout rootless Podman connection; see docs/local-development.md.' }
        $connect.GetAwaiter().GetResult() | Out-Null
    } finally { $client.Dispose() }
    Write-Host 'PostgreSQL transaction and host port checks passed.'
}
