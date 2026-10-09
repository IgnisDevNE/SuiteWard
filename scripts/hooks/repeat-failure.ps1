# Claude Code hook for PostToolUseFailure (and PostToolUse on Bash): warns when the same failure happens twice in a session.
# A failure is fingerprinted by tool, normalized command or file path, and the last lines of its output with volatile parts removed.
# Counts live in .local/hooks/failures.json (SUITEWARD_HOOK_STATE_DIR overrides the directory). Always exits 0.
#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
$maxSessions = 20
$maxEntries = 100

function Get-NormalizedText {
    param([string]$Text)
    $Text = $Text -replace '\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?', '<time>'
    $Text = $Text -replace '\b\d{1,2}:\d{2}:\d{2}(?:\.\d+)?\b', '<time>'
    $Text = $Text -replace '(?i)[^\s"''()]*[\\/](?:temp|tmp|go-tmp|go-build\d*)(?:[\\/][^\s"''()]*)?', '<tmp>'
    $Text = $Text -replace '(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b', '<id>'
    $Text = $Text -replace '(?i)\b0x[0-9a-f]+\b|\b(?=[0-9a-f]*\d)[0-9a-f]{7,}\b', '<id>'
    $Text = $Text -replace '\b\d+(?:\.\d+)?\s?(?:ms|us|µs|ns|s|sec|secs|seconds|min|mins)\b', '<dur>'
    return ($Text -replace '\s+', ' ').Trim()
}

function Get-FailureText {
    # Returns the failure output for a failing event, or $null when the event is not a failure.
    param($Payload)
    if ($Payload.hook_event_name -ceq 'PostToolUseFailure') {
        if ($Payload.is_interrupt -eq $true) { return $null }
        return [string]$Payload.error
    }
    if ($Payload.hook_event_name -ceq 'PostToolUse' -and $Payload.tool_name -ceq 'Bash') {
        $response = $Payload.tool_response
        if ($response -is [string]) { if ($response -match '^Exit code (?!0\b)\d+') { return $response }; return $null }
        if ($response -isnot [hashtable]) { return $null }
        foreach ($key in @('exitCode', 'exit_code', 'returncode')) {
            if ($response.ContainsKey($key) -and $response[$key] -is [ValueType] -and [int]$response[$key] -ne 0) {
                return ((@($response.stdout, $response.stderr) | Where-Object { $_ }) -join "`n")
            }
        }
    }
    return $null
}

try {
    $payload = [Console]::In.ReadToEnd() | ConvertFrom-Json -AsHashtable
    if ($payload -isnot [hashtable]) { exit 0 }
    $failure = Get-FailureText $payload
    if ($null -eq $failure) { exit 0 }

    $toolInput = if ($payload.tool_input -is [hashtable]) { $payload.tool_input } else { @{} }
    $target = ''
    foreach ($key in @('command', 'file_path', 'path', 'pattern', 'url')) { if ($toolInput[$key] -is [string]) { $target = $toolInput[$key]; break } }
    $target = Get-NormalizedText $target
    # Only the tail matters: cap the text and the line count before the regex-heavy normalization so a huge output stays fast.
    if ($failure.Length -gt 65536) { $failure = $failure.Substring($failure.Length - 65536) }
    $tail = @(($failure -split '\r?\n') | Where-Object { $_.Trim() } | Select-Object -Last 10)
    $lines = @($tail | ForEach-Object { Get-NormalizedText $_ } | Where-Object { $_ } | Select-Object -Last 3 | ForEach-Object { $_.Substring(0, [Math]::Min($_.Length, 200)) })
    $tool = [string]$payload.tool_name
    $key = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes("$tool`n$target`n$($lines -join "`n")"))).ToLowerInvariant().Substring(0, 16)
    $session = if ($payload.session_id -is [string] -and $payload.session_id) { $payload.session_id } else { 'unknown' }

    $stateDir = if ($env:SUITEWARD_HOOK_STATE_DIR) { $env:SUITEWARD_HOOK_STATE_DIR } else { Join-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) '.local/hooks' }
    New-Item -ItemType Directory -Path $stateDir -Force | Out-Null
    $stateFile = Join-Path $stateDir 'failures.json'
    $state = $null
    try { if (Test-Path -LiteralPath $stateFile) { $state = Get-Content -LiteralPath $stateFile -Raw | ConvertFrom-Json -AsHashtable } } catch { $state = $null }
    if ($state -isnot [hashtable] -or $state.sessions -isnot [hashtable]) { $state = @{ sessions = @{} } }

    $now = [DateTime]::UtcNow.ToString('o')
    if ($state.sessions[$session] -isnot [hashtable] -or $state.sessions[$session].entries -isnot [hashtable]) { $state.sessions[$session] = @{ entries = @{} } }
    $entries = $state.sessions[$session].entries
    $count = if ($entries[$key] -is [hashtable] -and $entries[$key].count -is [ValueType]) { [int]$entries[$key].count + 1 } else { 1 }
    $entries[$key] = @{ count = $count; seen = $now }
    $state.sessions[$session].seen = $now

    # Keep the state small: drop the oldest entries and sessions beyond the caps.
    if ($entries.Count -gt $maxEntries) {
        foreach ($old in @($entries.GetEnumerator() | Sort-Object { [string]$_.Value.seen } | Select-Object -First ($entries.Count - $maxEntries))) { $entries.Remove($old.Key) }
    }
    if ($state.sessions.Count -gt $maxSessions) {
        foreach ($old in @($state.sessions.GetEnumerator() | Sort-Object { [string]$_.Value.seen } | Select-Object -First ($state.sessions.Count - $maxSessions))) { $state.sessions.Remove($old.Key) }
    }
    $partial = "$stateFile.$([Guid]::NewGuid().ToString('N')).partial"
    try {
        Set-Content -LiteralPath $partial -Value ($state | ConvertTo-Json -Depth 6 -Compress) -Encoding utf8NoBOM
        Move-Item -LiteralPath $partial -Destination $stateFile -Force
    } finally { if (Test-Path -LiteralPath $partial) { Remove-Item -LiteralPath $partial -Force } }

    if ($count -ge 2) {
        $summary = "${tool}: $($target.Substring(0, [Math]::Min($target.Length, 80)))"
        if ($lines) { $summary += " -> $($lines[-1].Substring(0, [Math]::Min($lines[-1].Length, 80)))" }
        $event = if ($payload.hook_event_name -is [string] -and $payload.hook_event_name) { $payload.hook_event_name } else { 'PostToolUseFailure' }
        $message = "The same failure happened twice: $($summary.TrimEnd('.')). Stop retrying the same approach: identify the root cause and consult the advisor before the next attempt."
        [ordered]@{ hookSpecificOutput = [ordered]@{ hookEventName = $event; additionalContext = $message } } | ConvertTo-Json -Compress
    }
} catch {
    $message = $_.Exception.Message -replace '\s+', ' '
    [Console]::Error.WriteLine("repeat-failure: $($message.Substring(0, [Math]::Min($message.Length, 300)))")
}
exit 0
