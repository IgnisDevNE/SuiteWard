# Offline infrastructure checks; no Podman daemon or installed SDK is required.
#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'dev-env.ps1')
$rootContext = Get-DevContext
$testRoot = Join-Path $rootContext.Cache "dev-tests/$([Guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
$checks = 0

function Expect-Failure {
    param([scriptblock]$Action, [string]$Message)
    $rejected = $false
    try { & $Action | Out-Null } catch {
        if ($_.Exception.Message -notlike "*$Message*") { throw }
        $rejected = $true
    }
    if (-not $rejected) { throw "Expected rejection containing: $Message" }
}

function New-TestCheckout {
    param([string]$Name)
    $path = Join-Path $testRoot $Name
    New-Item -ItemType Directory -Path (Join-Path $path 'dev') -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $rootContext.Root '.go-version') -Destination $path
    Copy-Item -LiteralPath (Join-Path $rootContext.Root 'dev/tools.json') -Destination (Join-Path $path 'dev/tools.json')
    return Get-DevContext $path
}

try {
    $a = New-TestCheckout 'checkout a'
    $b = New-TestCheckout 'checkout b'
    if ($a.ID -eq $b.ID -or $a.Container -eq $b.Container -or $a.Volume -eq $b.Volume -or $a.Cache -eq $b.Cache) { throw 'Separate worktrees share mutable resources.' }
    $checks++
    if ((Get-DevContext ($a.Root + [IO.Path]::DirectorySeparatorChar)).ID -cne $a.ID) { throw 'Trailing slash changed checkout identity.' }
    $checks++
    if ($IsWindows -and (Get-DevContext $a.Root.ToUpperInvariant()).ID -cne $a.ID) { throw 'Windows path casing changed checkout identity.' }
    $checks++
    Push-Location -LiteralPath $testRoot
    try { if ((Get-DevContext).ID -cne $rootContext.ID) { throw 'Current directory changed project resolution.' } }
    finally { Pop-Location }
    $checks++
    Set-Content -LiteralPath (Join-Path $b.Root '.go-version') -Value '0.0.0'
    Expect-Failure { Get-DevContext $b.Root } 'Go version declarations disagree'
    $checks++

    $sample = Join-Path $testRoot 'sample.zip'
    Set-Content -LiteralPath $sample -Value 'not a trusted archive'
    $actualHash = (Get-FileHash -LiteralPath $sample -Algorithm SHA256).Hash.ToLowerInvariant()
    Assert-ArchiveChecksum $sample $actualHash
    $checks++
    Expect-Failure { Assert-ArchiveChecksum $sample ('0' * 64) } 'Checksum mismatch'
    Expect-Failure { Assert-ArchiveChecksum $sample 'not-a-sha' } 'Invalid pinned SHA-256'
    $checks += 2
    $downloads = Join-Path $a.Cache 'downloads'
    New-Item -ItemType Directory -Path $downloads -Force | Out-Null
    Copy-Item -LiteralPath $sample -Destination (Join-Path $downloads 'sample.zip')
    Expect-Failure { Get-VerifiedArchive $a @{ url = 'https://example.invalid/sample.zip'; sha256 = ('0' * 64) } } 'Checksum mismatch'
    $checks++

    $goPath = Get-ToolPath $a 'go'
    $installDir = Split-Path (Split-Path (Split-Path $goPath -Parent) -Parent) -Parent
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    Expect-Failure { Install-ArchiveTool $a 'go' } 'Incomplete or mismatched'
    $checks++
    New-Item -ItemType Directory -Path $a.State -Force | Out-Null
    $lock = [IO.File]::Open((Join-Path $a.State 'setup.lock'), 'OpenOrCreate', 'ReadWrite', 'None')
    try { Expect-Failure { Install-DevTools $a } 'Another setup is running' } finally { $lock.Dispose() }
    $checks++

    Assert-DatabaseOwnership $a @{ 'io.suiteward.dev.owner' = $a.ID; 'io.suiteward.dev.managed' = 'true' }
    Expect-Failure { Assert-DatabaseOwnership $a @{ 'io.suiteward.dev.owner' = $b.ID; 'io.suiteward.dev.managed' = 'true' } } 'another checkout'
    Expect-Failure { Assert-DatabaseOwnership $a @{} } 'another checkout'
    $checks += 3

    $before = @{}
    $names = @('PATH', 'GOROOT', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOBIN', 'GOTMPDIR', 'GOTOOLCHAIN', 'GOENV', 'GOWORK', 'GOFLAGS', 'GOOS', 'GOARCH')
    foreach ($name in $names) { $before[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
    $prior = Enter-DevEnvironment $a
    try {
        if ($env:GOTOOLCHAIN -cne 'local' -or $env:GOENV -cne 'off' -or $env:GOWORK -cne 'off') { throw 'Ambient Go configuration can escape the checkout.' }
        foreach ($name in @('GOROOT', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOBIN', 'GOTMPDIR')) {
            if (-not [Environment]::GetEnvironmentVariable($name, 'Process').StartsWith($a.Root)) { throw "$name escaped the checkout." }
        }
        if (-not $env:PATH.StartsWith((Split-Path $goPath -Parent))) { throw 'Local toolchain is not first in PATH.' }
    } finally { Restore-DevEnvironment $prior }
    foreach ($name in $names) {
        if ([Environment]::GetEnvironmentVariable($name, 'Process') -cne $before[$name]) { throw "$name leaked into the caller environment." }
    }
    $checks++
    $configName = if ($IsWindows) { 'APPDATA' } else { 'XDG_CONFIG_HOME' }
    $configBefore = [Environment]::GetEnvironmentVariable($configName, 'Process')
    Expect-Failure {
        Invoke-ToolConfigScope $a {
            if ([Environment]::GetEnvironmentVariable($configName, 'Process') -cne (Join-Path $a.Cache 'tool-config')) { throw 'Tool configuration escaped the checkout.' }
            throw 'sentinel tool failure'
        }
    } 'sentinel tool failure'
    if ([Environment]::GetEnvironmentVariable($configName, 'Process') -cne $configBefore) { throw 'Tool failure changed caller configuration directory.' }
    $checks++
    $savedConnection = [Environment]::GetEnvironmentVariable('CONTAINER_CONNECTION', 'Process')
    $savedContainerHost = [Environment]::GetEnvironmentVariable('CONTAINER_HOST', 'Process')
    $savedExitCode = Get-Variable -Name LASTEXITCODE -Scope Script -ErrorAction SilentlyContinue
    function podman { $script:LASTEXITCODE = 125; return 'unavailable test engine' }
    try {
        Expect-Failure { Select-DevPodmanConnection $a 'not-a-real-connection' } 'no new selection was saved'
        if (Test-Path -LiteralPath (Join-Path $a.State 'podman-connection.txt')) { throw 'Failed connection was persisted.' }
    } finally {
        Remove-Item Function:podman
        if ($savedExitCode) { $script:LASTEXITCODE = $savedExitCode.Value }
        else { Remove-Variable -Name LASTEXITCODE -Scope Script -ErrorAction SilentlyContinue }
        Restore-DevEnvironment @{ CONTAINER_CONNECTION = $savedConnection; CONTAINER_HOST = $savedContainerHost }
    }
    $checks++
    Write-Host "Passed $checks development infrastructure checks. No application coverage is produced."
} finally {
    $resolvedTest = [IO.Path]::GetFullPath($testRoot)
    $allowed = [IO.Path]::GetFullPath((Join-Path $rootContext.Cache 'dev-tests')) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolvedTest.StartsWith($allowed, [StringComparison]::OrdinalIgnoreCase)) { throw 'Refusing cleanup outside the test scratch directory.' }
    Remove-Item -LiteralPath $resolvedTest -Recurse -Force
}
