#Requires -Version 7.2
param([ValidateSet('Library','Generated')][string]$Mode = 'Generated')
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest

function Assert-GeneratedQueriesCurrent {
    param([hashtable]$Before, [hashtable]$After)
    if ($null -eq $Before -or $null -eq $After -or $Before.Count -eq 0) { throw 'Expected a nonempty versioned generated query baseline.' }
    foreach ($inventory in @($Before,$After)) {
        foreach ($entry in $inventory.GetEnumerator()) {
            if ($entry.Key -isnot [string] -or [string]::IsNullOrWhiteSpace($entry.Key) -or $entry.Value -isnot [string] -or $entry.Value -cnotmatch '^[0-9a-f]{64}$') { throw 'Malformed generated query inventory.' }
        }
    }
    if ($Before.Count -ne $After.Count) { throw 'Generated query file inventory differs; regenerate and commit the exact output.' }
    foreach ($entry in $Before.GetEnumerator()) {
        if (-not $After.ContainsKey($entry.Key) -or $After[$entry.Key] -cne $entry.Value) { throw "Generated queries are stale: $($entry.Key)." }
    }
}

function Get-GeneratedQueryInventory {
    param([string]$Directory)
    $inventory=@{}
    if (Test-Path -LiteralPath $Directory) {
        foreach ($file in Get-ChildItem -LiteralPath $Directory -Recurse -File) {
            if ($file.LinkType) { throw 'Generated query output must not contain file links.' }
            $relative=[IO.Path]::GetRelativePath($Directory,$file.FullName).Replace('\','/')
            $inventory[$relative]=(Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        }
    }
    return $inventory
}

function Invoke-GeneratedQueryVerification {
    param([string]$Root, [scriptblock]$Generate)
    $rootPath=[IO.Path]::GetFullPath($Root)
    $configuration=Join-Path $rootPath 'sqlc.yaml'
    if (-not (Test-Path -LiteralPath $configuration -PathType Leaf)) { throw 'Persistence requires versioned sqlc.yaml.' }
    $before=Get-GeneratedQueryInventory (Join-Path $rootPath 'internal/adapters/postgres/internal/dbgen')
    if ($before.Count -eq 0) { throw 'Persistence requires versioned generated queries.' }
    $scratchBase=Join-Path $rootPath '.cache/generated-query-checks'
    $scratch=Join-Path $scratchBase ([Guid]::NewGuid().ToString('N'))
    $output=Join-Path $scratch 'dbgen'
    New-Item -ItemType Directory -Path $output -Force | Out-Null
    try {
        $expected=@{schema='internal/adapters/postgres/migrations';queries='internal/adapters/postgres/queries';out='internal/adapters/postgres/internal/dbgen'}
        $observed=@{schema=0;queries=0;out=0}
        $settings=[IO.File]::ReadAllText($configuration)
        $pattern='(?m)^(?<indent>[ \t]*)(?<key>schema|queries|out):[ \t]*(?<value>[^\r\n]+)\r?$'
        $rewritten=[regex]::Replace($settings,$pattern,[Text.RegularExpressions.MatchEvaluator]{
            param($match)
            $key=$match.Groups['key'].Value
            $value=$match.Groups['value'].Value.Trim().Trim('"', "'")
            if ($value -cne $expected[$key]) { throw "Unsupported sqlc $key path; use the reviewed persistence layout." }
            $observed[$key]++
            $absolute=if($key -eq 'out'){$output}else{Join-Path $rootPath $expected[$key]}
            $match.Groups['indent'].Value+$key+': "'+$absolute.Replace('\','/')+'"'
        })
        if (@($observed.Values | Where-Object {$_ -ne 1}).Count) { throw 'Expected exactly one schema, queries and Go output path in sqlc configuration.' }
        $staged=Join-Path $scratch 'sqlc.yaml'
        [IO.File]::WriteAllText($staged,$rewritten,[Text.UTF8Encoding]::new($false))
        & $Generate $staged $output
        Assert-GeneratedQueriesCurrent -Before $before -After (Get-GeneratedQueryInventory $output)
    } finally {
        $resolved=[IO.Path]::GetFullPath($scratch)
        $boundary=[IO.Path]::GetFullPath($scratchBase)+[IO.Path]::DirectorySeparatorChar
        if (-not $resolved.StartsWith($boundary,[StringComparison]::OrdinalIgnoreCase) -or $resolved -cne [IO.Path]::GetFullPath($scratch)) { throw 'Refusing cleanup outside owned generation staging directory.' }
        if (Test-Path -LiteralPath $resolved) { Remove-Item -LiteralPath $resolved -Recurse -Force }
    }
}

function Invoke-LocalPersistenceVerification {
    param($Context, [scriptblock]$Verify)
    & $Verify
}
if ($Mode -eq 'Library') { return }
$root=Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'dev-env.ps1')
$context=Get-DevContext $root
New-Item -ItemType Directory -Path $context.State -Force | Out-Null
$lock=$null
try {
    $lock=[IO.File]::Open((Join-Path $context.State 'setup.lock'),'OpenOrCreate','ReadWrite','None')
    Install-ArchiveTool $context 'sqlc'
} finally { if($lock){$lock.Dispose()} }
Invoke-GeneratedQueryVerification -Root $root -Generate {
    param($Configuration,$Destination)
    & (Get-ToolPath $context 'sqlc') generate --file $Configuration
    if ($LASTEXITCODE -ne 0) { throw 'sqlc generation failed; no clean-generation result is claimed.' }
}
Write-Output 'Pinned sqlc regeneration exactly matches the versioned output; checkout files were not modified.'
