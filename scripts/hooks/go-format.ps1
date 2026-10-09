# Claude Code PostToolUse hook for Edit|Write|MultiEdit: formats an edited Go file with the pinned gofmt.
# Never fails the tool call: exit 0 and no stdout, with at most one line on stderr for an internal problem.
#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
try {
    $payload = [Console]::In.ReadToEnd() | ConvertFrom-Json -AsHashtable
    $file = if ($payload -is [hashtable] -and $payload.tool_input -is [hashtable]) { $payload.tool_input.file_path } else { $null }
    if ($file -isnot [string] -or -not $file.EndsWith('.go', [StringComparison]::OrdinalIgnoreCase)) { exit 0 }
    . (Join-Path (Split-Path $PSScriptRoot -Parent) 'dev-env.ps1')
    $context = Get-DevContext (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent)
    $path = [IO.Path]::GetFullPath($file)
    $prefix = $context.Root + [IO.Path]::DirectorySeparatorChar
    $comparison = if ($IsWindows) { [StringComparison]::OrdinalIgnoreCase } else { [StringComparison]::Ordinal }
    if (-not $path.StartsWith($prefix, $comparison) -or -not (Test-Path -LiteralPath $path -PathType Leaf)) { exit 0 }
    # gofmt -w would write through a link to a file that may live outside the repository.
    if ((Get-Item -LiteralPath $path -Force).LinkType) { exit 0 }
    $gofmt = Join-Path (Split-Path (Get-ToolPath $context 'go') -Parent) "gofmt$($context.Suffix)"
    if (-not (Test-Path -LiteralPath $gofmt)) { [Console]::Error.WriteLine('go-format: pinned gofmt is not installed; run ./scripts/dev.ps1 tools'); exit 0 }
    $output = & $gofmt -w $path 2>&1
    if ($LASTEXITCODE -ne 0) { [Console]::Error.WriteLine("go-format: gofmt failed: $((@($output) | Select-Object -First 1))") }
} catch {
    [Console]::Error.WriteLine("go-format: $($_.Exception.Message)")
}
exit 0
