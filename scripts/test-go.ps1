#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$scratchBase = [IO.Path]::GetFullPath((Join-Path (Split-Path $PSScriptRoot -Parent) '.cache/go-tests'))
$scratch = Join-Path $scratchBase ([Guid]::NewGuid().ToString('N'))
$fixtureScripts = Join-Path $scratch 'scripts'
New-Item -ItemType Directory -Path $fixtureScripts -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'check-go.ps1') -Destination (Join-Path $fixtureScripts 'check-go.ps1')

$fixturePackages = @('example.test/project/artifact', 'example.test/project/contract')
$invocation = [pscustomobject]@{ TestArguments = @(); ProfilePath = '' }

# Script-local command fakes exercise PowerShell's real argument parsing without
# requiring Go or producing an application coverage report in foundation CI.
function go {
    Set-Variable -Name LASTEXITCODE -Value 0 -Scope 1
    switch ($args[0]) {
        'list' { $fixturePackages }
        'vet' { }
        'build' { }
        'test' {
            $invocation.TestArguments = @($args)
            $profileArguments = @($args | Where-Object { $_ -like '-coverprofile=*' })
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
}

function gofmt {
    Set-Variable -Name LASTEXITCODE -Value 0 -Scope 1
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
    $coverageArguments = @($invocation.TestArguments | Where-Object { $_ -like '-coverpkg=*' })
    if ($coverageArguments.Count -ne 1 -or $coverageArguments[0] -cne '-coverpkg=example.test/project/artifact,example.test/project/contract') {
        throw 'Coverage must receive the discovered module packages as one explicit argument, without a wildcard or extra packages.'
    }
    Write-Output 'Passed 2 Go coverage invocation checks. These verify argument handling, not application coverage.'
} finally {
    $resolved = (Resolve-Path -LiteralPath $scratch).Path
    if ($resolved -ne [IO.Path]::GetFullPath($scratch) -or -not $resolved.StartsWith($scratchBase + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Refusing to remove a fixture outside the owned Go test directory.'
    }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
