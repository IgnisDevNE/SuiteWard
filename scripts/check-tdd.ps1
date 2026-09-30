#Requires -Version 7.2
[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path $PSScriptRoot -Parent),
    [string]$BaseRevision,
    [string]$HeadRevision = 'HEAD'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Invoke-TddGit {
    param([string]$RepoRoot, [string[]]$Arguments, [switch]$AllowFailure)
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = 'git'
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $start.StandardOutputEncoding = [Text.UTF8Encoding]::new($false)
    $start.StandardErrorEncoding = [Text.UTF8Encoding]::new($false)
    foreach ($argument in @('--literal-pathspecs', '-C', $RepoRoot) + $Arguments) { $start.ArgumentList.Add($argument) }
    # Checking evidence must not lazily fetch objects or prompt for credentials.
    $start.Environment['GIT_NO_LAZY_FETCH'] = '1'
    $start.Environment['GIT_TERMINAL_PROMPT'] = '0'
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    try {
        $null = $process.Start()
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $output = $stdout.GetAwaiter().GetResult()
        $null = $stderr.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0 -and -not $AllowFailure) {
            throw "TDD evidence git $($Arguments[0]) failed; use a complete local checkout with the requested revisions."
        }
        return @{ Output = $output; ExitCode = $process.ExitCode }
    } finally { $process.Dispose() }
}

function Assert-TddText {
    param($Value, [string]$Location)
    if ($Value -isnot [string] -or [string]::IsNullOrWhiteSpace($Value)) { throw "$Location must be a nonempty string." }
}

function Assert-TddObject {
    param($Value, [string]$Location)
    if ($Value -isnot [Collections.IDictionary]) { throw "$Location must be an object." }
}

function Assert-TddArray {
    param($Value, [string]$Location)
    if ($null -eq $Value -or $Value -is [string] -or $Value -is [Collections.IDictionary] -or $Value -isnot [Collections.IEnumerable]) { throw "$Location must be an array." }
}

function Test-TddInteger {
    param($Value)
    return ($Value -is [int] -or $Value -is [long] -or $Value -is [short] -or $Value -is [byte])
}

function Assert-TddPath {
    param($Value)
    Assert-TddText $Value 'file path'
    $segments = $Value.Split('/')
    if ($Value -match '[:\\\x00-\x1f\x7f]' -or $Value.Trim() -cne $Value -or @($segments | Where-Object { $_ -in @('', '.', '..') }).Count -gt 0) {
        throw "TDD evidence contains unsafe or non-normalized path '$Value'."
    }
}

function Test-TddDocumentationPath {
    param([string]$Path)
    return ($Path -cmatch '\.(md|markdown)$' -or $Path -ceq 'docs/plan/backlog.json' -or $Path -cmatch '^docs/plan/executions/[^/]+\.json$')
}

function Resolve-TddRevision {
    param([string]$RepoRoot, [string]$Revision)
    Assert-TddText $Revision 'revision'
    $result = Invoke-TddGit $RepoRoot @('rev-parse', '--verify', '--end-of-options', "${Revision}^{commit}")
    $resolved = $result.Output.Trim()
    if ($resolved -cnotmatch '^[0-9a-f]{40}$') { throw 'TDD evidence requires a repository with 40-character commit SHAs.' }
    return $resolved
}

function Get-TddJson {
    param([string]$RepoRoot, [string]$Revision, [string]$Path)
    $result = Invoke-TddGit $RepoRoot @('show', "${Revision}:$Path")
    try { $value = $result.Output | ConvertFrom-Json -AsHashtable } catch { throw "$Path at the checked head is not valid JSON." }
    Assert-TddObject $value $Path
    return $value
}

function Get-TddChangedPaths {
    param([string]$RepoRoot, [string]$Base, [string]$Head, [switch]$DeletedOnly)
    $arguments = @('diff', '--no-ext-diff', '--no-textconv', '--no-renames', '--name-only', '-z')
    if ($DeletedOnly) { $arguments += '--diff-filter=D' }
    $result = Invoke-TddGit $RepoRoot ($arguments + @($Base, $Head, '--'))
    return @($result.Output.Split([char]0) | Where-Object { $_.Length -gt 0 })
}

function Assert-TddCommit {
    param([string]$RepoRoot, $Revision)
    if ($Revision -isnot [string] -or $Revision -cnotmatch '^[0-9a-f]{40}$') { throw 'Evidence revisions must be a full 40-character commit SHA.' }
    $result = Invoke-TddGit $RepoRoot @('cat-file', '-t', $Revision) -AllowFailure
    if ($result.ExitCode -ne 0 -or $result.Output.Trim() -cne 'commit') { throw "Evidence revision $Revision must name a local commit object." }
}

function Assert-TddAncestor {
    param([string]$RepoRoot, [string]$Ancestor, [string]$Descendant, [string]$Message)
    $result = Invoke-TddGit $RepoRoot @('merge-base', '--is-ancestor', $Ancestor, $Descendant) -AllowFailure
    if ($result.ExitCode -ne 0) { throw $Message }
}

function Assert-TddCycle {
    param($Cycle, [string]$RepoRoot, [string]$Head)
    Assert-TddObject $Cycle 'cycle'
    Assert-TddText $Cycle['scenario'] 'scenario'
    Assert-TddText $Cycle['refactor'] 'refactor'
    foreach ($stage in @('red', 'green')) {
        Assert-TddObject $Cycle[$stage] $stage
        Assert-TddText $Cycle[$stage]['command'] "$stage.command"
        Assert-TddText $Cycle[$stage]['output'] "$stage.output"
        Assert-TddCommit $RepoRoot $Cycle[$stage]['revision']
    }
    $red = $Cycle['red']
    $green = $Cycle['green']
    if (-not (Test-TddInteger $red['exit_code']) -or $red['exit_code'] -eq 0) { throw 'RED exit_code must be a nonzero integer.' }
    if (-not (Test-TddInteger $green['exit_code']) -or $green['exit_code'] -ne 0) { throw 'GREEN exit_code must be zero.' }
    if ($red['command'] -cne $green['command']) { throw 'RED and GREEN must use the same command.' }
    if ($red['revision'] -ceq $green['revision']) { throw 'RED and GREEN must be distinct commits.' }
    Assert-TddAncestor $RepoRoot $red['revision'] $green['revision'] 'RED must be an ancestor of GREEN.'
    Assert-TddAncestor $RepoRoot $green['revision'] $Head 'GREEN must be an ancestor of the checked head.'
}

function Assert-TddReview {
    param($Review)
    Assert-TddObject $Review 'review'
    foreach ($field in @('author', 'reviewer', 'notes')) { Assert-TddText $Review[$field] "review.$field" }
    if ($Review['author'].Trim() -ieq $Review['reviewer'].Trim()) { throw 'TDD evidence requires an independent reviewer.' }
    if ($Review['outcome'] -cne 'accepted') { throw 'TDD review outcome must be accepted.' }
}

function Assert-TddEvidence {
    param($Evidence, $Plan, [string[]]$ChangedPaths, [string]$ManifestPath, [string]$RepoRoot, [string]$Head)
    if (-not (Test-TddInteger $Evidence['schema_version']) -or $Evidence['schema_version'] -ne 1) { throw 'TDD evidence schema_version must be 1.' }
    Assert-TddText $Evidence['phase'] 'phase'
    Assert-TddArray $Plan['phases'] 'plan phases'
    Assert-TddArray $Plan['tasks'] 'plan tasks'
    $phase = $Evidence['phase']
    if (@($Plan['phases'] | Where-Object { $_['id'] -ceq $phase }).Count -ne 1) { throw "TDD evidence references unknown phase '$phase'." }
    if ($ManifestPath -cne "docs/plan/executions/$phase.json") { throw 'Execution manifest filename must match its phase.' }
    Assert-TddArray $Evidence['tasks'] 'evidence tasks'
    $phaseTasks = @($Plan['tasks'] | Where-Object { $_['phase'] -ceq $phase })
    $taskIndex = [Collections.Generic.Dictionary[string, object]]::new([StringComparer]::Ordinal)
    foreach ($task in $phaseTasks) {
        Assert-TddText $task['id'] 'plan task ID'
        if ($taskIndex.ContainsKey($task['id'])) { throw 'Plan has duplicate phase task IDs.' }
        $taskIndex.Add($task['id'], $task)
    }
    $seenTasks = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($record in $Evidence['tasks']) {
        Assert-TddObject $record 'task record'
        Assert-TddText $record['task_id'] 'task_id'
        if (-not $taskIndex.ContainsKey($record['task_id']) -or -not $seenTasks.Add($record['task_id'])) { throw 'Evidence must contain every phase task exactly once.' }
    }
    if ($seenTasks.Count -ne $taskIndex.Count -or $taskIndex.Count -eq 0) { throw 'Evidence must contain every phase task exactly once.' }
    $changed = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($path in $ChangedPaths) { Assert-TddPath $path; $null = $changed.Add($path) }
    $covered = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($record in $Evidence['tasks']) {
        $id = $record['task_id']
        $mode = $record['mode']
        $plannedTdd = $taskIndex[$id]['tdd']
        Assert-TddObject $plannedTdd "plan $id.tdd"
        if ($mode -ceq 'historical' -or $plannedTdd['mode'] -ceq 'historical') { throw 'New historical evidence is not allowed.' }
        if ($mode -cnotin @('required', 'not_applicable') -or $mode -cne $plannedTdd['mode']) { throw "$id evidence mode does not match the plan." }
        Assert-TddArray $record['files'] "$id.files"
        Assert-TddArray $record['cycles'] "$id.cycles"
        Assert-TddReview $record['review']
        if ($mode -ceq 'not_applicable') {
            Assert-TddText $record['reason'] 'reason'
            if (@($record['cycles']).Count -ne 0) { throw 'not_applicable cycles must be empty.' }
        } else {
            if ($null -ne $record['reason'] -and $record['reason'] -cne '') { throw 'Only not_applicable tasks may give a non-applicability reason.' }
            if (@($record['cycles']).Count -eq 0) { throw "$id requires at least one cycle." }
            foreach ($cycle in $record['cycles']) { Assert-TddCycle $cycle $RepoRoot $Head }
        }
        foreach ($path in $record['files']) {
            Assert-TddPath $path
            if ($mode -ceq 'not_applicable' -and -not (Test-TddDocumentationPath $path)) { throw "not_applicable cannot cover executable or configuration file '$path'." }
            if (-not $changed.Contains($path)) { throw "Evidence path '$path' is not a changed file." }
            if (-not $covered.Add($path)) { throw "Changed file '$path' is covered more than once." }
            if ($mode -ceq 'required' -and -not (Test-TddDocumentationPath $path)) {
                $lastGreen = $record['cycles'][-1]['green']['revision']
                $result = Invoke-TddGit $RepoRoot @('diff', '--no-ext-diff', '--no-textconv', '--quiet', $lastGreen, $Head, '--', $path) -AllowFailure
                if ($result.ExitCode -eq 1) { throw "File '$path' changed after its final GREEN; record another reviewed cycle." }
                if ($result.ExitCode -ne 0) { throw "Unable to verify final GREEN for '$path'." }
            }
        }
    }
    foreach ($path in $changed) { if (-not $covered.Contains($path)) { throw "Evidence has an uncovered changed file '$path'." } }
}

function Invoke-TddCheck {
    param([string]$RepoRoot, [string]$BaseRevision, [string]$HeadRevision = 'HEAD')
    if ([string]::IsNullOrWhiteSpace($BaseRevision)) { throw 'BaseRevision is required; pass the phase PR base explicitly.' }
    $head = Resolve-TddRevision $RepoRoot $HeadRevision
    $base = Resolve-TddRevision $RepoRoot $BaseRevision
    $mergeBase = (Invoke-TddGit $RepoRoot @('merge-base', $base, $head)).Output.Trim()
    $changed = @(Get-TddChangedPaths $RepoRoot $mergeBase $head)
    $manifestPattern = '^docs/plan/executions/[^/]+\.json$'
    $deleted = @(Get-TddChangedPaths $RepoRoot $mergeBase $head -DeletedOnly)
    if (@($deleted | Where-Object { $_ -cmatch $manifestPattern }).Count -gt 0) { throw 'A phase cannot delete execution evidence.' }
    $manifests = @($changed | Where-Object { $_ -cmatch $manifestPattern })
    if ($manifests.Count -ne 1) { throw 'A phase PR must change exactly one execution manifest in docs/plan/executions/.' }
    $plan = Get-TddJson $RepoRoot $head 'docs/plan/backlog.json'
    $evidence = Get-TddJson $RepoRoot $head $manifests[0]
    Assert-TddEvidence $evidence $plan $changed $manifests[0] $RepoRoot $head
    Write-Host "TDD evidence accepted for $($evidence['phase']) at $head ($($changed.Count) changed files)."
    Write-Host 'This checks recorded evidence and ancestry, not command execution or authenticated review. Independent review must establish failure relevance and results.'
}

if ($MyInvocation.InvocationName -ne '.') {
    Invoke-TddCheck -RepoRoot $RepoRoot -BaseRevision $BaseRevision -HeadRevision $HeadRevision
}
