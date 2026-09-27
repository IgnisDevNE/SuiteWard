param(
    [ValidateSet('Inspect', 'Documents', 'Gate', 'Library')]
    [string]$Mode = 'Documents',
    [string]$Root = (Split-Path $PSScriptRoot -Parent)
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Get-RepositoryMode {
    param([string[]]$HeadFiles, [string[]]$BaseFiles)
    $hasModule = $HeadFiles -ccontains 'go.mod'
    $hadModule = $BaseFiles -ccontains 'go.mod'
    $hasSource = @($HeadFiles | Where-Object { $_ -cmatch '\.go$' -and $_ -cnotmatch '^vendor/' }).Count -gt 0
    $hadSource = @($BaseFiles | Where-Object { $_ -cmatch '\.go$' -and $_ -cnotmatch '^vendor/' }).Count -gt 0
    if (($hadModule -or $hadSource) -and (-not $hasModule -or -not $hasSource)) {
        throw 'Go source/module was removed. CI cannot silently return to documentation-only mode.'
    }
    if ($hasModule -ne $hasSource) {
        throw 'A Go change requires both a root go.mod and real Go source files.'
    }
    if ($hasModule) { return 'go' }
    return 'foundation'
}

function Assert-CiGate {
    param($Results)
    foreach ($required in @('inspect', 'foundation')) {
        if ($Results.$required.result -ne 'success') { throw "$required did not succeed" }
    }
    $hasGo = $Results.inspect.outputs.go
    if ($hasGo -cnotin @('true', 'false')) { throw 'Missing or invalid Go classification' }
    $expected = if ($hasGo -ceq 'true') { 'success' } else { 'skipped' }
    foreach ($required in @('go', 'coverage')) {
        if ($Results.$required.result -ne $expected) { throw "$required must be $expected" }
    }
}

if ($Mode -eq 'Library') { return }
Push-Location -LiteralPath $Root
try {
    if ($Mode -eq 'Inspect') {
        $headFiles = @(git ls-files)
        if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect tracked files' }
        $baseFiles = @()
        $baseSha = ''
        if ($env:GITHUB_EVENT_PATH) {
            $event = Get-Content -LiteralPath $env:GITHUB_EVENT_PATH -Raw | ConvertFrom-Json
            if ($env:GITHUB_EVENT_NAME -eq 'pull_request') {
                $baseSha = $event.pull_request.base.sha
            } elseif ($env:GITHUB_EVENT_NAME -eq 'push') {
                $baseSha = $event.before
            }
        }
        if (-not $baseSha -or $baseSha -match '^0+$') {
            $parents = (git rev-list --parents -n 1 HEAD) -split ' '
            if ($LASTEXITCODE -ne 0) { throw 'Cannot determine commit history' }
            if ($parents.Count -gt 1) { $baseSha = $parents[1] } else { $baseSha = '' }
        }
        if ($baseSha) {
            if ($baseSha -notmatch '^[a-fA-F0-9]{40,64}$') { throw 'Invalid base commit' }
            $baseFiles = @(git ls-tree -r --name-only $baseSha)
            if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect base commit; refusing to guess CI applicability' }
        }
        $repositoryMode = Get-RepositoryMode -HeadFiles $headFiles -BaseFiles $baseFiles
        $hasGo = ($repositoryMode -eq 'go').ToString().ToLowerInvariant()
        if ($env:GITHUB_OUTPUT) { "go=$hasGo" | Out-File -LiteralPath $env:GITHUB_OUTPUT -Append -Encoding utf8 }
        $message = if ($hasGo -eq 'true') { 'Go build, tests, race, security, and coverage are required.' } else { 'Engineering foundation only: no application Go source exists. Go tests and coverage are not applicable.' }
        Write-Output $message
        if ($env:GITHUB_STEP_SUMMARY) { $message | Out-File -LiteralPath $env:GITHUB_STEP_SUMMARY -Append -Encoding utf8 }
    } elseif ($Mode -eq 'Documents') {
        $files = @(git ls-files '*.md')
        if ($LASTEXITCODE -ne 0 -or $files.Count -eq 0) { throw 'Cannot find tracked documentation' }
        $linkCount = 0
        foreach ($file in $files) {
            $content = Get-Content -LiteralPath $file -Raw
            foreach ($match in [regex]::Matches($content, '\]\(([^)]+)\)')) {
                $target = $match.Groups[1].Value
                if ($target -match '^(https?://|mailto:|#)') { continue }
                $target = ($target -split '#')[0]
                if (-not (Test-Path -LiteralPath (Join-Path (Split-Path (Join-Path $Root $file)) $target))) {
                    throw "Broken local documentation link: $file -> $target"
                }
                $linkCount++
            }
        }
        Write-Output "Validated $($files.Count) documents and $linkCount local links."
    } elseif ($Mode -eq 'Gate') {
        if (-not $env:NEEDS_JSON) { throw 'Missing CI job results' }
        Assert-CiGate -Results ($env:NEEDS_JSON | ConvertFrom-Json)
        Write-Output 'Every applicable CI verification succeeded.'
    }
} finally {
    Pop-Location
}
