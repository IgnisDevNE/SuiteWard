param([switch]$Coverage, [switch]$Integration, [switch]$Race, [switch]$SkipLint)

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
    # git still lists a tracked file that was deleted from disk but not yet from the index; gofmt would fail on it.
    # core.quotepath=false keeps non-ASCII names unquoted so the existence filter does not drop them.
    $sources = @(git -c core.quotepath=false ls-files --cached --others --exclude-standard '*.go' | Where-Object { $_ -cnotmatch '^vendor/' -and (Test-Path -LiteralPath $_ -PathType Leaf) } | Sort-Object -Unique)
    if ($LASTEXITCODE -ne 0 -or $sources.Count -eq 0) { throw 'No Go source files in the checkout' }
    # In batches: the whole list passes the 32,767-character command line of Windows, and gofmt then fails to start.
    $unformatted = @(for ($i = 0; $i -lt $sources.Count; $i += 200) {
            $batch = @($sources[$i..([Math]::Min($i + 199, $sources.Count - 1))])
            gofmt -l @batch
            if ($LASTEXITCODE -ne 0) { throw 'gofmt failed' }
        })
    if ($unformatted.Count -gt 0) { throw "Run gofmt on: $($unformatted -join ', ')" }
    # The sandbox-tagged tests need a container engine, so they are vetted here and run by hand (internal/executor).
    $vetTags = @(if ($Integration) { $buildTags } else { '-tags=sandbox' })
    go vet @vetTags ./...
    if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
    if ($SkipLint -and -not $Coverage -and -not $Integration) {
        # The macOS CI runner: the pinned-tool bootstrap (dev-env.ps1) supports Windows and Linux x64 only, and the Linux and Windows jobs lint the same sources.
        Write-Host 'Skipping golangci-lint: no pinned build for this platform; the Linux and Windows jobs lint the same sources.'
    } elseif (-not $Coverage -and -not $Integration) {
        # The pinned, checksum-verified golangci-lint from dev/tools.json; .golangci.yml also lints the integration-tagged files, so the other modes skip this.
        . (Join-Path $PSScriptRoot 'dev-env.ps1')
        $context = Get-DevContext
        Install-ArchiveTool $context 'golangci-lint'
        & (Get-ToolPath $context 'golangci-lint') run ./...
        if ($LASTEXITCODE -ne 0) { throw 'golangci-lint reported findings' }
    }
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
        $raceTag = @(if ($Race) { '-race' })
        go test -count=1 @raceTag @buildTags ./...
        if ($LASTEXITCODE -ne 0) { throw 'Go tests failed' }
    }
} finally {
    Pop-Location
}
