# Offline infrastructure checks; no Podman daemon or installed SDK is required.
#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'dev-env.ps1')
$rootContext = Get-DevContext
$testRoot = Join-Path $rootContext.Cache "dev-tests/$([Guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
$checks = 0
# Archive tools live in a shared per-user cache; tests redirect it to scratch space and never touch the real one.
Install-ArchiveTool $rootContext 'actionlint'
$realActionlint = Split-Path (Get-ToolPath $rootContext 'actionlint') -Parent
$savedToolsDir = $env:SUITEWARD_TOOLS_DIR
$toolsDir = Join-Path $testRoot 'shared tools'
$env:SUITEWARD_TOOLS_DIR = $toolsDir

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

    # Shared tool cache: every checkout resolves the same pinned tools outside its own tree.
    $sharedRoot = [IO.Path]::GetFullPath($toolsDir)
    $b = New-TestCheckout 'checkout b'
    if ($a.Tools -cne $sharedRoot -or $b.Tools -cne $sharedRoot) { throw 'SUITEWARD_TOOLS_DIR did not select the shared tool cache.' }
    if ((Get-ToolPath $a 'go') -cne (Get-ToolPath $b 'go')) { throw 'Checkouts do not share pinned tool installations.' }
    if ($a.Tools.StartsWith($a.Root)) { throw 'Shared tool cache is inside a checkout.' }
    $checks++
    $savedLocal = $env:LOCALAPPDATA; $savedXdg = $env:XDG_CACHE_HOME; $savedHome = $env:HOME
    try {
        Remove-Item Env:SUITEWARD_TOOLS_DIR
        $fakeLocal = Join-Path $testRoot 'fake-local'
        if ($IsWindows) {
            $env:LOCALAPPDATA = $fakeLocal
            if ((Get-DevContext $a.Root).Tools -cne (Join-Path $fakeLocal 'SuiteWard\tools')) { throw 'Windows default tool cache is not %LOCALAPPDATA%\SuiteWard\tools.' }
        } else {
            $env:XDG_CACHE_HOME = $fakeLocal
            if ((Get-DevContext $a.Root).Tools -cne (Join-Path $fakeLocal 'suiteward/tools')) { throw 'Linux default tool cache ignores XDG_CACHE_HOME.' }
            Remove-Item Env:XDG_CACHE_HOME
            $env:HOME = $fakeLocal
            if ((Get-DevContext $a.Root).Tools -cne (Join-Path $fakeLocal '.cache/suiteward/tools')) { throw 'Linux default tool cache does not fall back to $HOME/.cache.' }
        }
    } finally {
        $env:SUITEWARD_TOOLS_DIR = $toolsDir
        $env:LOCALAPPDATA = $savedLocal; $env:XDG_CACHE_HOME = $savedXdg; $env:HOME = $savedHome
        if (-not $savedLocal) { Remove-Item Env:LOCALAPPDATA -ErrorAction SilentlyContinue }
        if (-not $savedXdg) { Remove-Item Env:XDG_CACHE_HOME -ErrorAction SilentlyContinue }
    }
    $checks++
    Expect-Failure { Assert-ToolVersion $a 'sqlc' } 'shared tool cache'
    $checks++

    $sample = Join-Path $testRoot 'sample.zip'
    Set-Content -LiteralPath $sample -Value 'not a trusted archive'
    $actualHash = (Get-FileHash -LiteralPath $sample -Algorithm SHA256).Hash.ToLowerInvariant()
    Assert-ArchiveChecksum $sample $actualHash
    $checks++
    Expect-Failure { Assert-ArchiveChecksum $sample ('0' * 64) } 'Checksum mismatch'
    Expect-Failure { Assert-ArchiveChecksum $sample 'not-a-sha' } 'Invalid pinned SHA-256'
    $checks += 2
    $downloads = Join-Path $a.Tools 'downloads'
    New-Item -ItemType Directory -Path $downloads -Force | Out-Null
    Copy-Item -LiteralPath $sample -Destination (Join-Path $downloads 'sample.zip')
    Expect-Failure { Get-VerifiedArchive $a @{ url = 'https://example.invalid/sample.zip'; sha256 = ('0' * 64) } } 'Checksum mismatch'
    $checks++

    $goPath = Get-ToolPath $a 'go'
    $installDir = Split-Path (Split-Path (Split-Path $goPath -Parent) -Parent) -Parent
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    Expect-Failure { Install-ArchiveTool $a 'go' } 'Incomplete or mismatched'
    $checks++
    $escaping = [pscustomobject]@{ Root = $a.Root; ID = $a.ID; Platform = $a.Platform; Tools = $a.Tools; Cache = $a.Cache; State = $a.State; Suffix = $a.Suffix
        Manifest = @{ sqlc = @{ version = '../../escape'; assets = @{ $a.Platform = @{ url = 'https://example.invalid/sqlc.zip'; sha256 = ('0' * 64) } } } } }
    Expect-Failure { Install-ArchiveTool $escaping 'sqlc' } 'escaped'
    $checks++

    # Concurrent setups from different checkouts serialize on a cache-level lock.
    $cacheLockPath = Join-Path $toolsDir '.setup.lock'
    New-Item -ItemType Directory -Path $toolsDir -Force | Out-Null
    $holder = [IO.File]::Open($cacheLockPath, 'OpenOrCreate', 'ReadWrite', 'None')
    try { Expect-Failure { Invoke-WithToolsLock $a { 'never runs' } -TimeoutSeconds 1 } 'Timed out waiting for the shared tool cache' } finally { $holder.Dispose() }
    $checks++
    $holder = [IO.File]::Open($cacheLockPath, 'OpenOrCreate', 'ReadWrite', 'None')
    $devEnvScript = Join-Path $PSScriptRoot 'dev-env.ps1'
    try {
        $job = Start-ThreadJob -ArgumentList $devEnvScript, $b.Root -ScriptBlock {
            param($script, $root)
            . $script
            Invoke-WithToolsLock (Get-DevContext $root) { 'acquired' } -TimeoutSeconds 60
        }
        if (Wait-Job $job -Timeout 2) { throw 'A second setup ran while the cache lock was held.' }
    } finally { $holder.Dispose() }
    if ((Receive-Job (Wait-Job $job -Timeout 30) -Wait) -cne 'acquired') { throw 'A waiting setup did not acquire the released cache lock.' }
    Remove-Job $job
    $checks++

    # A setup that waited re-checks the cache: a concurrent setup may have installed the tool meanwhile.
    $holder = [IO.File]::Open($cacheLockPath, 'OpenOrCreate', 'ReadWrite', 'None')
    try {
        $job = Start-ThreadJob -ArgumentList $devEnvScript, $b.Root -ScriptBlock {
            param($script, $root)
            . $script
            Install-ArchiveTool (Get-DevContext $root) 'actionlint'
            'installed'
        }
        if (Wait-Job $job -Timeout 2) { throw 'Install ran while the cache lock was held.' }
        $target = Split-Path (Get-ToolPath $b 'actionlint') -Parent
        New-Item -ItemType Directory -Path (Split-Path $target -Parent) -Force | Out-Null
        Copy-Item -LiteralPath $realActionlint -Destination $target -Recurse
    } finally { $holder.Dispose() }
    if ((Receive-Job (Wait-Job $job -Timeout 60) -Wait) -cne 'installed') { throw 'Install did not reuse the tool installed by a concurrent setup.' }
    Remove-Job $job
    if (@(Get-ChildItem -LiteralPath (Join-Path $toolsDir 'downloads') -Filter 'actionlint*').Count) { throw 'Install downloaded a tool that was already present after the lock.' }
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
        foreach ($name in @('GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOTMPDIR')) {
            if (-not [Environment]::GetEnvironmentVariable($name, 'Process').StartsWith($a.Root)) { throw "$name escaped the checkout." }
        }
        foreach ($name in @('GOROOT', 'GOBIN')) {
            if (-not [Environment]::GetEnvironmentVariable($name, 'Process').StartsWith($a.Tools)) { throw "$name escaped the shared tool cache." }
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

    # db-reset removes only this checkout's own container and volume, and verifies both before removing either.
    $script:podmanCalls = [Collections.Generic.List[string]]::new()
    $script:fakeContainers = @{}
    $script:fakeVolumes = @{}
    function podman {
        $script:podmanCalls.Add(($args -join ' '))
        $script:LASTEXITCODE = 0
        $verb = "$($args[0]) $($args[1])"
        if ($verb -eq 'container exists') { $script:LASTEXITCODE = if ($script:fakeContainers.ContainsKey($args[2])) { 0 } else { 1 }; return }
        if ($verb -eq 'container inspect') { return (ConvertTo-Json -AsArray -Depth 6 @(@{ Config = @{ Labels = $script:fakeContainers[$args[2]] } })) }
        if ($verb -eq 'volume exists') { $script:LASTEXITCODE = if ($script:fakeVolumes.ContainsKey($args[2])) { 0 } else { 1 }; return }
        if ($verb -eq 'volume inspect') { return (ConvertTo-Json -AsArray -Depth 6 @(@{ Labels = $script:fakeVolumes[$args[2]] })) }
        if ($verb -eq 'volume rm') { $script:fakeVolumes.Remove($args[2]); return }
        if ($args[0] -eq 'rm') { $script:fakeContainers.Remove($args[-1]); return }
        $script:LASTEXITCODE = 125
    }
    function Get-DestructiveCalls { @($script:podmanCalls | Where-Object { $_ -match '^(rm|stop|volume rm) ' }) }
    $own = @{ 'io.suiteward.dev.owner' = $a.ID; 'io.suiteward.dev.managed' = 'true' }
    $foreign = @{ 'io.suiteward.dev.owner' = $b.ID; 'io.suiteward.dev.managed' = 'true' }
    $stateFiles = @('postgres.env', 'postgres.json', 'database-url.txt')
    function Initialize-ResetScenario($containers, $volumes) {
        $script:podmanCalls.Clear(); $script:fakeContainers = $containers; $script:fakeVolumes = $volumes
        New-Item -ItemType Directory -Path $a.State -Force | Out-Null
        foreach ($file in $stateFiles) { Set-Content -LiteralPath (Join-Path $a.State $file) -Value 'stale' }
    }
    try {
        Initialize-ResetScenario @{ $a.Container = $foreign } @{ $a.Volume = $own }
        Expect-Failure { Remove-DevDatabase $a } 'another checkout'
        if (@(Get-DestructiveCalls).Count) { throw 'Reset removed resources despite a foreign container.' }
        Initialize-ResetScenario @{ $a.Container = $own } @{ $a.Volume = $foreign }
        Expect-Failure { Remove-DevDatabase $a } 'another checkout'
        if (@(Get-DestructiveCalls).Count -or -not $script:fakeContainers.ContainsKey($a.Container)) { throw 'Reset removed the container despite a foreign volume.' }
        Initialize-ResetScenario @{ $a.Container = @{} } @{}
        Expect-Failure { Remove-DevDatabase $a } 'another checkout'
        if (@(Get-DestructiveCalls).Count) { throw 'Reset removed an unlabeled container.' }
        foreach ($file in $stateFiles) { if (-not (Test-Path -LiteralPath (Join-Path $a.State $file))) { throw 'Refused reset deleted local state.' } }
        $checks += 3

        Initialize-ResetScenario @{ $a.Container = $own; 'other-container' = $foreign } @{ $a.Volume = $own; 'other-volume' = $foreign }
        Remove-DevDatabase $a
        $expectedCalls = @("rm --force $($a.Container)", "volume rm $($a.Volume)")
        if ((Compare-Object (Get-DestructiveCalls) $expectedCalls -SyncWindow 0)) { throw "Reset made unexpected removals: $((Get-DestructiveCalls) -join '; ')" }
        if (-not $script:fakeContainers.ContainsKey('other-container') -or -not $script:fakeVolumes.ContainsKey('other-volume')) { throw 'Reset touched another resource.' }
        foreach ($file in $stateFiles) { if (Test-Path -LiteralPath (Join-Path $a.State $file)) { throw "Reset left stale $file." } }
        $checks++
        Initialize-ResetScenario @{} @{}
        Remove-DevDatabase $a
        if (@(Get-DestructiveCalls).Count) { throw 'Reset removed something when nothing existed.' }
        $checks++
        $dbLock = [IO.File]::Open((Join-Path $a.State 'database.lock'), 'OpenOrCreate', 'ReadWrite', 'None')
        try { Expect-Failure { Remove-DevDatabase $a } 'Another database operation is running' } finally { $dbLock.Dispose() }
        $checks++
    } finally { Remove-Item Function:podman, Function:Get-DestructiveCalls, Function:Initialize-ResetScenario }
    Write-Host "Passed $checks development infrastructure checks. No application coverage is produced."
} finally {
    if ($null -eq $savedToolsDir) { Remove-Item Env:SUITEWARD_TOOLS_DIR -ErrorAction SilentlyContinue } else { $env:SUITEWARD_TOOLS_DIR = $savedToolsDir }
    $resolvedTest = [IO.Path]::GetFullPath($testRoot)
    $allowed = [IO.Path]::GetFullPath((Join-Path $rootContext.Cache 'dev-tests')) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolvedTest.StartsWith($allowed, [StringComparison]::OrdinalIgnoreCase)) { throw 'Refusing cleanup outside the test scratch directory.' }
    Remove-Item -LiteralPath $resolvedTest -Recurse -Force
}
