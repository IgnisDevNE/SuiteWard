# Starts the pinned `gopls mcp` for Claude Code's stdio MCP configuration.
# Stdout carries only the server's protocol traffic: diagnostics go to stderr, and nothing is installed here.
#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
try {
    . (Join-Path $PSScriptRoot 'dev-env.ps1')
    $context = Get-DevContext
    $gopls = Get-ToolPath $context 'gopls'
    foreach ($tool in @('go', 'gopls')) {
        if (-not (Test-Path -LiteralPath (Get-ToolPath $context $tool))) {
            [Console]::Error.WriteLine("$tool is not installed in the shared tool cache ($($context.Tools)). Run: ./scripts/dev.ps1 tools")
            exit 1
        }
    }
    # gopls shells out to the pinned Go. Process-scoped environment only; the child inherits it.
    $null = Enter-DevEnvironment $context
    $configName = if ($IsWindows) { 'APPDATA' } else { 'XDG_CONFIG_HOME' }
    $configDir = Join-Path $context.Cache 'tool-config'
    New-Item -ItemType Directory -Path $configDir -Force | Out-Null
    [Environment]::SetEnvironmentVariable($configName, $configDir, 'Process')
    # A direct child process inherits stdin and stdout unchanged; PowerShell's pipeline would re-encode them.
    $info = [Diagnostics.ProcessStartInfo]::new($gopls)
    foreach ($argument in @('mcp') + $args) { $info.ArgumentList.Add($argument) }
    $info.UseShellExecute = $false
    $process = [Diagnostics.Process]::Start($info)
    $process.WaitForExit()
    exit $process.ExitCode
} catch {
    [Console]::Error.WriteLine("gopls-mcp: $($_.Exception.Message)")
    exit 1
}
