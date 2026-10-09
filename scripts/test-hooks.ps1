# Offline checks for the deterministic Claude Code hooks; they run the real scripts as child processes.
#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'dev-env.ps1')
$rootContext = Get-DevContext
# gofmt ships with the pinned Go SDK; installing it here mirrors how check-foundation installs actionlint.
Install-ArchiveTool $rootContext 'go'
$testRoot = Join-Path $rootContext.Cache "hook-tests/$([Guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
$checks = 0
$pwsh = (Get-Process -Id $PID).Path

# A temp checkout copy: the hooks resolve the repository root from their own location.
$checkout = Join-Path $testRoot 'checkout'
New-Item -ItemType Directory -Path (Join-Path $checkout 'dev'), (Join-Path $checkout 'scripts') -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $rootContext.Root '.go-version') -Destination $checkout
Copy-Item -LiteralPath (Join-Path $rootContext.Root 'dev/tools.json') -Destination (Join-Path $checkout 'dev/tools.json')
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'dev-env.ps1') -Destination (Join-Path $checkout 'scripts')
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'hooks') -Destination (Join-Path $checkout 'scripts/hooks') -Recurse
$formatHook = Join-Path $checkout 'scripts/hooks/go-format.ps1'
$failureHook = Join-Path $checkout 'scripts/hooks/repeat-failure.ps1'
$stateDir = Join-Path $testRoot 'hook-state'
$savedStateDir = $env:SUITEWARD_HOOK_STATE_DIR
$env:SUITEWARD_HOOK_STATE_DIR = $stateDir

function Invoke-Hook {
    param([string]$Script, [string]$InputText)
    $errFile = Join-Path $testRoot 'stderr.txt'
    $stdout = ($InputText | & $pwsh -NoProfile -File $Script 2> $errFile) -join "`n"
    return [pscustomobject]@{ ExitCode = $LASTEXITCODE; Stdout = $stdout; Stderr = (Get-Content -LiteralPath $errFile -Raw) }
}

function Assert-Silent {
    param($Result, [string]$What)
    if ($Result.ExitCode -ne 0) { throw "$What exited with $($Result.ExitCode)." }
    if ($Result.Stdout) { throw "$What wrote to stdout: $($Result.Stdout)" }
}

function New-FailureInput {
    param([string]$Session = 'session-1', [string]$ErrorText, [string]$Command = 'go test ./...', [string]$Event = 'PostToolUseFailure')
    return (@{ session_id = $Session; hook_event_name = $Event; tool_name = 'Bash'; tool_input = @{ command = $Command }; error = $ErrorText; is_interrupt = $false } | ConvertTo-Json -Depth 5 -Compress)
}

try {
    # go-format: only .go files inside the repository are formatted.
    $unformatted = "package sample`nfunc   Add(a,b int)int{return a+b}`n"
    $formatted = "package sample`n`nfunc Add(a, b int) int { return a + b }`n"
    $goFile = Join-Path $checkout 'internal/sample/sample.go'
    New-Item -ItemType Directory -Path (Split-Path $goFile -Parent) -Force | Out-Null
    [IO.File]::WriteAllText($goFile, $unformatted)
    $result = Invoke-Hook $formatHook (@{ hook_event_name = 'PostToolUse'; tool_name = 'Edit'; tool_input = @{ file_path = $goFile } } | ConvertTo-Json -Compress)
    Assert-Silent $result 'go-format on a Go file'
    if ([IO.File]::ReadAllText($goFile) -cne $formatted) { throw 'go-format did not format the Go file.' }
    $checks++

    $textFile = Join-Path $checkout 'notes.txt'
    [IO.File]::WriteAllText($textFile, $unformatted)
    $result = Invoke-Hook $formatHook (@{ tool_name = 'Write'; tool_input = @{ file_path = $textFile } } | ConvertTo-Json -Compress)
    Assert-Silent $result 'go-format on a non-Go file'
    if ([IO.File]::ReadAllText($textFile) -cne $unformatted) { throw 'go-format touched a non-Go file.' }
    $checks++

    $outside = Join-Path $testRoot 'outside.go'
    [IO.File]::WriteAllText($outside, $unformatted)
    $result = Invoke-Hook $formatHook (@{ tool_name = 'Write'; tool_input = @{ file_path = $outside } } | ConvertTo-Json -Compress)
    Assert-Silent $result 'go-format outside the repository'
    if ([IO.File]::ReadAllText($outside) -cne $unformatted) { throw 'go-format touched a file outside the repository.' }
    $result = Invoke-Hook $formatHook (@{ tool_name = 'Write'; tool_input = @{ file_path = (Join-Path $checkout 'missing.go') } } | ConvertTo-Json -Compress)
    Assert-Silent $result 'go-format on a missing file'
    $checks++

    foreach ($bad in @('{ not json', '', '[]')) {
        Assert-Silent (Invoke-Hook $formatHook $bad) 'go-format on malformed input'
        Assert-Silent (Invoke-Hook $failureHook $bad) 'repeat-failure on malformed input'
    }
    $checks++

    # repeat-failure: the second identical failure in a session warns; volatile parts do not make failures differ.
    $first = New-FailureInput -ErrorText "Exit code 1`nFAIL github.com/x/y 0.412s`nbuild failed at 2026-10-09T10:11:12Z in C:\Users\me\AppData\Local\Temp\go-build1234\b001 (id 0a1b2c3d4e5f)"
    $second = New-FailureInput -ErrorText "Exit code 1`nFAIL github.com/x/y 1.907s`nbuild failed at 2026-10-09T10:15:48Z in C:\Users\me\AppData\Local\Temp\go-build9876\b001 (id 99aabbccddee)"
    Assert-Silent (Invoke-Hook $failureHook $first) 'first failure'
    $checks++
    $result = Invoke-Hook $failureHook $second
    if ($result.ExitCode -ne 0) { throw 'repeat-failure did not exit 0 on a repeat.' }
    $output = $result.Stdout | ConvertFrom-Json
    if ($output.hookSpecificOutput.hookEventName -cne 'PostToolUseFailure') { throw 'Repeat output has the wrong hookEventName.' }
    $context = $output.hookSpecificOutput.additionalContext
    if ($context -notmatch '^The same failure happened twice: .+\. Stop retrying the same approach: identify the root cause and consult the advisor before the next attempt\.$') { throw "Unexpected additionalContext: $context" }
    $checks++

    $different = New-FailureInput -ErrorText "Exit code 2`ncannot find package github.com/x/z"
    Assert-Silent (Invoke-Hook $failureHook $different) 'a different failure'
    Assert-Silent (Invoke-Hook $failureHook (New-FailureInput -ErrorText "Exit code 1`nFAIL github.com/x/y 0.1s`nbuild failed" -Command 'go vet ./...')) 'the same output from another command'
    $checks++

    Assert-Silent (Invoke-Hook $failureHook ($first -replace 'session-1', 'session-2')) 'the same failure in another session'
    $result = Invoke-Hook $failureHook ($second -replace 'session-1', 'session-2')
    if (-not $result.Stdout) { throw 'Session two did not count its own repeat.' }
    $checks++

    # PostToolUse on Bash only counts a response that reports a non-zero exit.
    $successResponse = @{ session_id = 'session-3'; hook_event_name = 'PostToolUse'; tool_name = 'Bash'; tool_input = @{ command = 'go build ./...' }; tool_response = @{ stdout = 'ok'; stderr = ''; interrupted = $false } } | ConvertTo-Json -Depth 5 -Compress
    Assert-Silent (Invoke-Hook $failureHook $successResponse) 'a successful Bash response (1)'
    Assert-Silent (Invoke-Hook $failureHook $successResponse) 'a successful Bash response (2)'
    $failedResponse = @{ session_id = 'session-3'; hook_event_name = 'PostToolUse'; tool_name = 'Bash'; tool_input = @{ command = 'go build ./...' }; tool_response = @{ stdout = ''; stderr = 'main.go:3:1: syntax error'; exitCode = 1 } } | ConvertTo-Json -Depth 5 -Compress
    Assert-Silent (Invoke-Hook $failureHook $failedResponse) 'the first non-zero Bash response'
    $output = (Invoke-Hook $failureHook $failedResponse).Stdout | ConvertFrom-Json
    if ($output.hookSpecificOutput.hookEventName -cne 'PostToolUse') { throw 'PostToolUse repeat output has the wrong hookEventName.' }
    $checks++

    # A huge failure output stays fast: only the tail is normalized, and the fingerprint still comes from the last lines.
    $noise = (1..300000 | ForEach-Object { "verbose output line $_ with some filler text to make the log large" }) -join "`n"
    $bigFirst = New-FailureInput -Session 'session-big' -Command 'go test -v ./...' -ErrorText "Exit code 1`n$noise`nFAIL github.com/x/y 0.5s`npanic: boom at 2026-10-09T10:00:00Z"
    $bigSecond = New-FailureInput -Session 'session-big' -Command 'go test -v ./...' -ErrorText "Exit code 1`n$noise`nFAIL github.com/x/y 9.5s`npanic: boom at 2026-10-09T11:30:45Z"
    $timer = [Diagnostics.Stopwatch]::StartNew()
    Assert-Silent (Invoke-Hook $failureHook $bigFirst) 'a very large first failure'
    $result = Invoke-Hook $failureHook $bigSecond
    $timer.Stop()
    if (-not $result.Stdout) { throw 'A very large repeated failure was not fingerprinted from its last lines.' }
    if ($timer.Elapsed.TotalSeconds -gt 5) { throw "Two very large failures took $([int]$timer.Elapsed.TotalSeconds) seconds; the hook must normalize only the output tail." }
    $checks++

    # Long internal errors are truncated before they reach stderr.
    [IO.File]::WriteAllText((Join-Path $stateDir 'failures.json'), '{"sessions":{}}')
    $result = Invoke-Hook $failureHook ('{"hook_event_name":"PostToolUseFailure","tool_name":"Bash","error":"x","tool_input":' + ('[' * 5000))
    if ($result.ExitCode -ne 0 -or $result.Stdout -or $result.Stderr.Length -gt 400) { throw "An internal error was not contained and truncated (stderr length $($result.Stderr.Length))." }
    $checks++

    # go-format never writes through a link; a hard link stands in where symlinks need a privilege the host does not grant.
    $linkTarget = Join-Path $testRoot 'link-target.go'
    [IO.File]::WriteAllText($linkTarget, $unformatted)
    $link = Join-Path $checkout 'internal/sample/linked.go'
    try { New-Item -ItemType SymbolicLink -Path $link -Target $linkTarget | Out-Null }
    catch { New-Item -ItemType HardLink -Path $link -Target $linkTarget | Out-Null }
    Assert-Silent (Invoke-Hook $formatHook (@{ tool_name = 'Write'; tool_input = @{ file_path = $link } } | ConvertTo-Json -Compress)) 'go-format on a link'
    if ([IO.File]::ReadAllText($linkTarget) -cne $unformatted) { throw 'go-format wrote through a link.' }
    $checks++
    # A corrupt state file is reset, not fatal; state stays inside the override directory.
    $stateFile = Join-Path $stateDir 'failures.json'
    if (-not (Test-Path -LiteralPath $stateFile)) { throw 'Hook state was not written to SUITEWARD_HOOK_STATE_DIR.' }
    [IO.File]::WriteAllText($stateFile, '{ corrupt')
    Assert-Silent (Invoke-Hook $failureHook $different) 'a failure with a corrupt state file'
    $null = Get-Content -LiteralPath $stateFile -Raw | ConvertFrom-Json
    $checks++
    if (Test-Path -LiteralPath (Join-Path $checkout '.local/hooks')) { throw 'Tests wrote to the checkout hook state.' }
    $checks++
    Write-Host "Passed $checks hook checks."
} finally {
    if ($null -eq $savedStateDir) { Remove-Item Env:SUITEWARD_HOOK_STATE_DIR -ErrorAction SilentlyContinue } else { $env:SUITEWARD_HOOK_STATE_DIR = $savedStateDir }
    $resolvedTest = [IO.Path]::GetFullPath($testRoot)
    $allowed = [IO.Path]::GetFullPath((Join-Path $rootContext.Cache 'hook-tests')) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolvedTest.StartsWith($allowed, [StringComparison]::OrdinalIgnoreCase)) { throw 'Refusing cleanup outside the test scratch directory.' }
    Remove-Item -LiteralPath $resolvedTest -Recurse -Force
}
