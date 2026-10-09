#Requires -Version 7.2
param(
    [Parameter(Position = 0)]
    [ValidateSet('setup', 'tools', 'doctor', 'check', 'persistence', 'go', 'sqlc', 'actionlint', 'govulncheck', 'gopls', 'lint', 'db-start', 'db-stop', 'db-status', 'db-test', 'db-reset')]
    [string]$Command = 'doctor',
    [string]$PodmanConnection = '',
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$ToolArgs = @()
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'dev-env.ps1')
$context = Get-DevContext
$previous = Enter-DevEnvironment $context
$previous['CONTAINER_CONNECTION'] = [Environment]::GetEnvironmentVariable('CONTAINER_CONNECTION', 'Process')
$previous['CONTAINER_HOST'] = [Environment]::GetEnvironmentVariable('CONTAINER_HOST', 'Process')
Push-Location -LiteralPath $context.Root
try {
    if ($Command -in @('setup', 'doctor', 'db-start', 'db-stop', 'db-status', 'db-test', 'db-reset')) { Select-DevPodmanConnection $context $PodmanConnection }
    switch ($Command) {
        { $_ -in @('setup', 'tools') } {
            Install-DevTools $context
            if ($Command -eq 'setup') { Start-DevDatabase $context; Test-DevDatabase $context }
            Write-Host "Pinned development tools are ready in the shared cache ($($context.Tools))."
        }
        'doctor' {
            foreach ($name in @('go', 'sqlc', 'actionlint', 'govulncheck', 'gopls', 'golangci-lint')) { Assert-ToolVersion $context $name; Write-Host "$name $($context.Manifest[$name].version): OK" }
            $actualRoot = Invoke-ToolConfigScope $context { & (Get-ToolPath $context 'go') env GOROOT; if ($LASTEXITCODE -ne 0) { throw 'Cannot read Go configuration.' } }
            if ([IO.Path]::GetFullPath($actualRoot) -ne [IO.Path]::GetFullPath($env:GOROOT)) { throw 'Go did not select this checkout toolchain.' }
            Test-DevDatabase $context
            Write-Host "Development environment verified for checkout $($context.ID)."
        }
        'check' {
            & (Join-Path $PSScriptRoot 'check-foundation.ps1')
            $files = @(git ls-files --cached --others --exclude-standard)
            if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect checkout files.' }
            . (Join-Path $PSScriptRoot 'ci.ps1') -Mode Library
            if ((Get-RepositoryMode -HeadFiles $files -BaseFiles @()) -eq 'go') {
                Assert-ToolVersion $context 'go'
                Invoke-ToolConfigScope $context { & (Join-Path $PSScriptRoot 'check-go.ps1') }
            } else { Write-Host 'Foundation checks passed. Application Go tests are not applicable yet.' }
        }
        'persistence' {
            . (Join-Path $PSScriptRoot 'check-persistence.ps1') -Mode Library
            Invoke-LocalPersistenceVerification $context {
                & (Join-Path $PSScriptRoot 'check-persistence.ps1') -Mode Generated
                Assert-ToolVersion $context 'go'
                Invoke-ToolConfigScope $context { & (Join-Path $PSScriptRoot 'check-go.ps1') -Integration }
            }
        }
        'db-start' { Start-DevDatabase $context }
        'db-test' { Test-DevDatabase $context }
        'db-reset' {
            # Destroys this checkout's database data only; ownership labels are verified before anything is removed.
            Remove-DevDatabase $context
            Start-DevDatabase $context
            Write-Host 'Checkout database recreated with new credentials; apply migrations as usual.'
        }
        'db-stop' {
            $dbLock = [IO.File]::Open((Join-Path $context.State 'database.lock'), 'OpenOrCreate', 'ReadWrite', 'None')
            try {
                $container = Get-DevContainer $context
                if ($container) { & podman stop $context.Container; if ($LASTEXITCODE -ne 0) { throw 'Could not stop checkout database.' } }
            } finally { $dbLock.Dispose() }
            Write-Host 'Checkout database stopped; its data volume is preserved.'
        }
        'db-status' {
            $container = Get-DevContainer $context
            if ($container) {
                [pscustomobject]@{ Container = $context.Container; Status = $container.State.Status; Ports = $container.NetworkSettings.Ports; Volume = $context.Volume } | ConvertTo-Json -Depth 5
            } else { Write-Host 'No database container for this checkout.' }
        }
        default {
            # `lint` runs the pinned golangci-lint over the whole module unless arguments say otherwise.
            $tool = if ($Command -eq 'lint') { 'golangci-lint' } else { $Command }
            [string[]]$arguments = @(if ($Command -eq 'lint' -and -not $ToolArgs) { 'run'; './...' } else { $ToolArgs })
            Assert-ToolVersion $context $tool
            Invoke-ToolConfigScope $context {
                & (Get-ToolPath $context $tool) @arguments
                if ($LASTEXITCODE -ne 0) { throw "$tool exited with code $LASTEXITCODE." }
            }
        }
    }
} finally {
    Pop-Location
    Restore-DevEnvironment $previous
}
