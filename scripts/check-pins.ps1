# Every pinned image reference agrees across the repository, and the Containerfile pins every base image by digest.
#Requires -Version 7.2
param([string]$Root = (Split-Path $PSScriptRoot -Parent))
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$files = @(git -C $Root -c core.quotepath=false ls-files --cached --others --exclude-standard)
if ($LASTEXITCODE -ne 0 -or $files.Count -eq 0) { throw 'Cannot list the repository files.' }
# Copies are grouped by the repository name alone, so a copy whose tag and digest both drifted still disagrees with the others.
$copies = @{}
foreach ($file in $files) {
    $path = Join-Path $Root $file
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { continue }
    foreach ($m in [regex]::Matches((Get-Content -LiteralPath $path -Raw), '(?<name>[\w.\-/]+)(?::(?<tag>[\w.${}\-]+))?@sha256:(?<digest>[a-f0-9]{64})')) {
        $key = $m.Groups['name'].Value.Split('/')[-1]
        if (-not $copies.ContainsKey($key)) { $copies[$key] = [Collections.Generic.List[object]]::new() }
        $copies[$key].Add([pscustomobject]@{ File = $file; Tag = $m.Groups['tag'].Value; Digest = $m.Groups['digest'].Value })
    }
}
foreach ($key in $copies.Keys) {
    $tags = @($copies[$key].Tag | Sort-Object -Unique)
    $digests = @($copies[$key].Digest | Sort-Object -Unique)
    if ($tags.Count -gt 1 -or $digests.Count -gt 1) {
        throw "Copies of the $key image disagree:`n" + (($copies[$key] | ForEach-Object { "  $($_.File): $($_.Tag) $($_.Digest)" }) -join "`n")
    }
}
$containerfile = Join-Path $Root 'deploy/Containerfile'
if (-not (Test-Path -LiteralPath $containerfile)) { throw 'deploy/Containerfile is missing.' }
$text = Get-Content -LiteralPath $containerfile -Raw
$stages = @{}
foreach ($m in [regex]::Matches($text, '(?m)^FROM\s+(?:--\S+\s+)*(?<image>\S+)(?:\s+AS\s+(?<alias>\S+))?\s*$')) {
    $image = $m.Groups['image'].Value
    if ($image -cnotmatch '@sha256:[a-f0-9]{64}$' -and -not $stages.ContainsKey($image)) { throw "The Containerfile base image $image is not pinned by digest." }
    if ($m.Groups['alias'].Success) { $stages[$m.Groups['alias'].Value] = $true }
}
$goArg = [regex]::Match($text, '(?m)^ARG GO_VERSION=(?<v>\S+)\s*$').Groups['v'].Value
if ($goArg -cne (Get-Content -LiteralPath (Join-Path $Root '.go-version') -Raw).Trim()) { throw 'The Containerfile GO_VERSION default differs from .go-version.' }
$postgres = (Get-Content -LiteralPath (Join-Path $Root 'dev/tools.json') -Raw | ConvertFrom-Json -AsHashtable).postgres
if (-not $postgres.image.Contains("postgres:$($postgres.version)-")) { throw 'The pinned PostgreSQL image tag does not match its declared version.' }
Write-Host 'Pinned image references agree.'
