#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$scratchBase = [IO.Path]::GetFullPath((Join-Path (Split-Path $PSScriptRoot -Parent) '.cache/go-tests'))
$scratch = Join-Path $scratchBase ([Guid]::NewGuid().ToString('N'))
$fixtureScripts = Join-Path $scratch 'scripts'
New-Item -ItemType Directory -Path $fixtureScripts -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'check-go.ps1') -Destination (Join-Path $fixtureScripts 'check-go.ps1')

$fixturePackages = @('example.test/project/artifact', 'example.test/project/contract')
$invocation = [pscustomobject]@{ TestArguments = @(); ProfilePath = ''; Commands=@{}; GofmtArguments = @() }

# check-go.ps1 formats only files that exist on disk: a tracked file deleted from the working tree is still listed by git.
New-Item -ItemType Directory -Path (Join-Path $scratch 'internal/domain') -Force | Out-Null
Set-Content -LiteralPath (Join-Path $scratch 'internal/domain/value.go') -Value 'package domain'

# Script-local command fakes exercise PowerShell's real argument parsing without
# requiring Go or producing an application coverage report in foundation CI.
function go {
    Set-Variable -Name LASTEXITCODE -Value 0 -Scope 1
    $invocation.Commands[$args[0]]=@($args)
    switch ($args[0]) {
        'list' { $fixturePackages }
        'vet' { $lint.Events.Add('vet') }
        'build' { }
        'test' {
            $invocation.TestArguments = @($args)
            $profileArguments = @($args | Where-Object { $_ -like '-coverprofile=*' })
            if ('-covermode=atomic' -cnotin $args) { return }
            if ($profileArguments.Count -ne 1) { throw 'Expected one coverage profile destination in the Go invocation.' }
            $profileName = $profileArguments[0].Substring('-coverprofile='.Length)
            $destination = [IO.Path]::GetFullPath((Join-Path (Get-Location).Path $profileName))
            if (-not $destination.StartsWith($scratch + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
                throw 'The fake Go profile destination must stay inside its fixture.'
            }
            $invocation.ProfilePath = $destination
            Set-Content -LiteralPath $destination -Value @('mode: atomic', 'example.test/project/artifact/value.go:1.1,1.2 1 1')
        }
        default { throw "Unexpected Go command: $($args[0])" }
    }
}

function git {
    Set-Variable -Name LASTEXITCODE -Value 0 -Scope 1
    'internal/domain/value.go'
    'internal/domain/deleted.go'
}

function gofmt {
    Set-Variable -Name LASTEXITCODE -Value 0 -Scope 1
    $invocation.GofmtArguments = @($args)
}

# check-go.ps1 dot-sources dev-env.ps1 for the pinned golangci-lint. The fixture ships an empty one and these fakes
# stand in for its checksum-verified installer and the lint binary, so the test never touches the network or the tool cache.
Set-Content -LiteralPath (Join-Path $fixtureScripts 'dev-env.ps1') -Value '# Fixture: the test defines the tool functions.'
$lint = [pscustomobject]@{ Events = [Collections.Generic.List[string]]::new(); ExitCode = 0 }
function Get-DevContext { [pscustomobject]@{ Fixture = $true } }
function Install-ArchiveTool { param($Context, [string]$Name) $lint.Events.Add("install $Name") }
function Get-ToolPath { param($Context, [string]$Name) if ($Name -cne 'golangci-lint') { throw "Unexpected tool: $Name" }; 'Invoke-FakeGolangciLint' }
function Invoke-FakeGolangciLint {
    Set-Variable -Name LASTEXITCODE -Value $lint.ExitCode -Scope 1
    $lint.Events.Add("lint $($args -join ' ')")
}

try {
    try {
        & (Join-Path $fixtureScripts 'check-go.ps1') -Coverage
    } catch {
        throw "Coverage verification rejected a successful Go run: $($_.Exception.Message) Requested profile: '$($invocation.ProfilePath)'."
    }
    if (-not (Test-Path -LiteralPath (Join-Path $scratch 'coverage.out'))) {
        throw 'The Go invocation must produce the exact coverage.out path consumed by CI.'
    }
    if (($invocation.GofmtArguments -join ' ') -cne '-l internal/domain/value.go') {
        throw "gofmt must receive only Go files that exist on disk, not a tracked file deleted from the working tree. Received: $($invocation.GofmtArguments -join ' ')"
    }
    $coverageArguments = @($invocation.TestArguments | Where-Object { $_ -like '-coverpkg=*' })
    if ($coverageArguments.Count -ne 1 -or $coverageArguments[0] -cne '-coverpkg=example.test/project/artifact,example.test/project/contract') {
        throw 'Coverage must receive the discovered module packages as one explicit argument, without a wildcard or extra packages.'
    }
    if (($lint.Events -join ';') -cne 'vet') { throw "Coverage mode must not run lint (the integration-tagged run lints once). Events: $($lint.Events -join ';')" }
    $lint.Events.Clear()
    & (Join-Path $fixtureScripts 'check-go.ps1')
    if (($lint.Events -join ';') -cne 'vet;install golangci-lint;lint run ./...') {
        throw "Default mode must install the pinned golangci-lint after go vet and run it as 'run ./...'. Events: $($lint.Events -join ';')"
    }
    $lint.Events.Clear()
    $lint.ExitCode = 1
    $rejected = $false
    try { & (Join-Path $fixtureScripts 'check-go.ps1') } catch { $rejected = $true }
    if (-not $rejected) { throw 'A failing golangci-lint must fail check-go.ps1.' }
    $lint.ExitCode = 0
    $lint.Events.Clear()
    $savedDatabase = [Environment]::GetEnvironmentVariable('SUITEWARD_TEST_DATABASE_URL', 'Process')
    try {
        Remove-Item -LiteralPath Env:SUITEWARD_TEST_DATABASE_URL -ErrorAction SilentlyContinue
        $rejected = $false
        try { & (Join-Path $fixtureScripts 'check-go.ps1') -Coverage -Integration } catch { $rejected = $true }
        if (-not $rejected) { throw 'Required PostgreSQL verification ran without a connection instead of failing closed.' }
        $env:SUITEWARD_TEST_DATABASE_URL = 'postgresql://fixture:fixture@127.0.0.1:5432/fixture'
        & (Join-Path $fixtureScripts 'check-go.ps1') -Integration
        & (Join-Path $fixtureScripts 'check-go.ps1') -Coverage -Integration
        if (($lint.Events -join ';') -cne 'vet;vet') { throw "Integration modes must not run lint. Events: $($lint.Events -join ';')" }
        foreach($command in @('list','vet','build')) {
            $arguments=$invocation.Commands[$command]
            if($arguments.Count -ne 3 -or $arguments[1] -cne '-tags=integration' -or $arguments[2] -cne './...'){throw "Integration $command must receive the complete build tag as one argument."}
        }
        if (@($invocation.TestArguments | Where-Object { $_ -ceq '-tags=integration' }).Count -ne 1) {
            throw 'The real coverage invocation must include PostgreSQL integration-tagged tests exactly once.'
        }
        foreach ($required in @('-race', '-count=1', '-covermode=atomic')) {
            if ($required -cnotin $invocation.TestArguments) { throw "Integration coverage lost required argument $required." }
        }
    } finally {
        if ($null -eq $savedDatabase) { Remove-Item -LiteralPath Env:SUITEWARD_TEST_DATABASE_URL -ErrorAction SilentlyContinue }
        else { [Environment]::SetEnvironmentVariable('SUITEWARD_TEST_DATABASE_URL', $savedDatabase, 'Process') }
    }
    Write-Output 'Passed 16 Go verification invocation checks. These verify arguments and required database applicability, not adapter behavior or application coverage.'
} finally {
    $resolved = (Resolve-Path -LiteralPath $scratch).Path
    if ($resolved -ne [IO.Path]::GetFullPath($scratch) -or -not $resolved.StartsWith($scratchBase + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Refusing to remove a fixture outside the owned Go test directory.'
    }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
