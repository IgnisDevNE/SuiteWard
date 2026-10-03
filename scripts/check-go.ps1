param([switch]$Coverage, [switch]$Integration)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = Split-Path $PSScriptRoot -Parent
Push-Location -LiteralPath $root
try {
    if ($Integration -and [string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable('SUITEWARD_TEST_DATABASE_URL', 'Process'))) {
        throw 'PostgreSQL integration requires SUITEWARD_TEST_DATABASE_URL; refusing an unverified adapter run'
    }
    $buildTags = @(if ($Integration) { '-tags=integration' })
    $packages = @(go list @buildTags ./...)
    if ($LASTEXITCODE -ne 0 -or $packages.Count -eq 0) { throw 'Expected real Go packages; refusing an empty verification' }
    $sources = @(git ls-files --cached --others --exclude-standard '*.go' | Where-Object { $_ -cnotmatch '^vendor/' } | Sort-Object -Unique)
    if ($LASTEXITCODE -ne 0 -or $sources.Count -eq 0) { throw 'No Go source files in the checkout' }
    $unformatted = @(gofmt -l @sources)
    if ($LASTEXITCODE -ne 0) { throw 'gofmt failed' }
    if ($unformatted.Count -gt 0) { throw "Run gofmt on: $($unformatted -join ', ')" }
    go vet @buildTags ./...
    if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
    go build @buildTags ./...
    if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
    if ($Coverage) {
        $testArguments = @(
            'test', '-count=1', '-race', '-covermode=atomic'
            $buildTags
            "-coverpkg=$($packages -join ',')", '-coverprofile=coverage.out', './...'
        )
        go @testArguments
        if ($LASTEXITCODE -ne 0) { throw 'Race/coverage tests failed' }
        if (-not (Test-Path -LiteralPath 'coverage.out')) { throw 'No coverage report was produced' }
        $report = @(Get-Content -LiteralPath 'coverage.out')
        if ($report.Count -lt 2 -or $report[0] -ne 'mode: atomic') { throw 'Empty or invalid coverage report' }
    } else {
        go test -count=1 @buildTags ./...
        if ($LASTEXITCODE -ne 0) { throw 'Go tests failed' }
    }
} finally {
    Pop-Location
}
