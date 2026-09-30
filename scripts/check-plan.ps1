#Requires -Version 7.2
[CmdletBinding()]
param(
    [string]$PlanPath = (Join-Path $PSScriptRoot '../docs/plan/backlog.json'),
    [switch]$SkipDocuments,
    [switch]$WriteDocs
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-PlanText {
    param($Value, [string]$Location)
    if ($Value -isnot [string] -or [string]::IsNullOrWhiteSpace($Value)) { throw "$Location must be a nonempty string." }
}

function Assert-PlanList {
    param($Value, [string]$Location, [switch]$Required)
    if ($null -eq $Value -or $Value -is [string] -or $Value -is [System.Collections.IDictionary] -or $Value -isnot [System.Collections.IEnumerable]) { throw "$Location must be an array." }
    if ($Required -and @($Value).Count -eq 0) { throw "$Location must not be empty." }
    $seen = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($item in $Value) {
        Assert-PlanText $item $Location
        if (-not $seen.Add($item)) { throw "$Location contains duplicate '$item'." }
    }
}

function New-PlanIndex {
    param($Items, [string]$Location)
    if ($null -eq $Items -or $Items -is [string] -or $Items -is [System.Collections.IDictionary] -or $Items -isnot [System.Collections.IEnumerable]) { throw "$Location must be an array." }
    $index = [System.Collections.Generic.Dictionary[string, object]]::new([StringComparer]::Ordinal)
    foreach ($item in $Items) {
        if ($item -isnot [System.Collections.IDictionary]) { throw "$Location entries must be objects." }
        Assert-PlanText $item['id'] "$Location.id"
        if ($index.ContainsKey($item['id'])) { throw "Duplicate $Location ID '$($item['id'])'." }
        $index.Add($item['id'], $item)
    }
    return ,$index
}

function Assert-PlanReferences {
    param($References, $Index, [string]$Location, [string]$Self = '')
    Assert-PlanList $References $Location
    foreach ($reference in $References) {
        if ($Self -and $reference -ceq $Self) { throw "$Location has a self dependency." }
        if (-not $Index.ContainsKey($reference)) { throw "$Location references unknown ID '$reference'." }
    }
}

function Assert-PlanPath {
    param([string]$Path, [string]$Location)
    $normalized = $Path.Replace('\', '/')
    $traversal = @($normalized -split '/' | Where-Object { $_.Trim() -eq '..' }).Count -gt 0
    if ([IO.Path]::IsPathRooted($Path) -or $normalized.StartsWith('/') -or $normalized -match '[:\x00-\x1f]' -or $traversal -or $normalized.Trim() -eq '.') {
        throw "$Location contains unsafe ownership path '$Path'; use a checkout-relative path or glob."
    }
}

function Assert-PlanDag {
    param($Index, [string[]]$Fields, [string]$Location)
    $finished = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    while ($finished.Count -lt $Index.Count) {
        $advanced = $false
        foreach ($id in $Index.Keys) {
            if ($finished.Contains($id)) { continue }
            $ready = $true
            foreach ($field in $Fields) {
                foreach ($dependency in $Index[$id][$field]) {
                    if (-not $finished.Contains($dependency)) { $ready = $false }
                }
            }
            if ($ready) { [void]$finished.Add($id); $advanced = $true }
        }
        if (-not $advanced) { throw "$Location contains a dependency cycle." }
    }
}

function Assert-Plan {
    param($Plan)
    if ($Plan -isnot [System.Collections.IDictionary] -or $Plan['schema_version'] -ne 1) { throw 'Expected plan schema_version 1.' }
    $phases = New-PlanIndex $Plan['phases'] 'phases'
    $tasks = New-PlanIndex $Plan['tasks'] 'tasks'
    $contracts = New-PlanIndex $Plan['contracts'] 'contracts'
    $gates = New-PlanIndex $Plan['decision_gates'] 'decision_gates'
    if ($phases.Count -eq 0 -or $tasks.Count -eq 0) { throw 'A plan needs phases and tasks.' }
    $filenames = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)

    foreach ($phase in $Plan['phases']) {
        $id = $phase['id']
        # IDs become filenames; otherwise their vocabulary and numbering are unrestricted.
        if ($id -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$' -or $id.EndsWith('.') -or $id -match '^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(\.|$)') { throw "Phase ID '$id' is not a safe document filename." }
        if (-not $filenames.Add($id)) { throw "Phase document IDs collide on Windows: '$id'." }
        foreach ($field in @('milestone', 'title', 'pr_title', 'outcome')) { Assert-PlanText $phase[$field] "$id.$field" }
        Assert-PlanList $phase['exit_criteria'] "$id.exit_criteria" -Required
        Assert-PlanReferences $phase['merge_after'] $phases "$id.merge_after" $id
        Assert-PlanReferences $phase['contracts'] $contracts "$id.contracts"
        Assert-PlanReferences $phase['gates'] $gates "$id.gates"
        if (@($Plan['tasks'] | Where-Object { $_['phase'] -ceq $id }).Count -eq 0) { throw "Phase '$id' has no tasks." }
    }
    foreach ($task in $Plan['tasks']) {
        $id = $task['id']
        foreach ($field in @('phase', 'title', 'kind', 'lane')) { Assert-PlanText $task[$field] "$id.$field" }
        if (-not $phases.ContainsKey($task['phase'])) { throw "$id.phase references unknown ID '$($task['phase'])'." }
        if ($task['kind'] -cnotin @('implementation', 'integration', 'contract')) { throw "$id.kind is invalid." }
        foreach ($field in @('owns', 'acceptance', 'verification')) { Assert-PlanList $task[$field] "$id.$field" -Required }
        foreach ($field in @('shared_files', 'constraints')) { Assert-PlanList $task[$field] "$id.$field" }
        foreach ($path in @($task['owns']) + @($task['shared_files'])) { Assert-PlanPath $path "$id.ownership" }
        Assert-PlanReferences $task['contracts'] $contracts "$id.contracts"
        Assert-PlanReferences $task['gates'] $gates "$id.gates"
        foreach ($field in @('needs_to_start', 'needs_to_merge')) { Assert-PlanReferences $task[$field] $tasks "$id.$field" $id }
    }
    foreach ($contract in $Plan['contracts']) {
        $id = $contract['id']
        foreach ($field in @('phase', 'title', 'freeze_task')) { Assert-PlanText $contract[$field] "$id.$field" }
        Assert-PlanList $contract['requirements'] "$id.requirements" -Required
        if (-not $phases.ContainsKey($contract['phase'])) { throw "$id.phase references unknown ID '$($contract['phase'])'." }
        if (-not $tasks.ContainsKey($contract['freeze_task'])) { throw "$id.freeze_task references unknown ID '$($contract['freeze_task'])'." }
        $owner = $tasks[$contract['freeze_task']]
        if ($owner['phase'] -cne $contract['phase'] -or $owner['contracts'] -cnotcontains $id -or $phases[$contract['phase']]['contracts'] -cnotcontains $id) {
            throw "$id contract ownership must agree with its phase and freeze task."
        }
    }
    foreach ($gate in $Plan['decision_gates']) {
        $id = $gate['id']
        foreach ($field in @('topic', 'owner', 'required_decision')) { Assert-PlanText $gate[$field] "$id.$field" }
        if ($gate['status'] -cnotin @('open', 'accepted')) { throw "$id.status is invalid." }
        Assert-PlanList $gate['acceptance'] "$id.acceptance" -Required
        Assert-PlanReferences $gate['blocks'] $phases "$id.blocks"
    }
    Assert-PlanDag $phases @('merge_after') 'Phase integration'
    Assert-PlanDag $tasks @('needs_to_start') 'Task start'
    Assert-PlanDag $tasks @('needs_to_start', 'needs_to_merge') 'Combined task integration'
}

function Add-PlanLines {
    param($Lines, [string]$Label, $Values)
    [void]$Lines.Add("**${Label}:**")
    [void]$Lines.Add('')
    if (@($Values).Count -eq 0) { [void]$Lines.Add('- None.') }
    else { foreach ($value in $Values) { [void]$Lines.Add("- $value") } }
    [void]$Lines.Add('')
}

function Get-PhaseDocument {
    param($Plan, $Phase)
    $lines = [System.Collections.Generic.List[string]]::new()
    [void]$lines.Add("# $($Phase['id']): $($Phase['title'])")
    [void]$lines.Add('')
    [void]$lines.Add('<!-- Generated from ../backlog.json by scripts/check-plan.ps1 -WriteDocs. Edit the plan, then regenerate. -->')
    [void]$lines.Add('')
    [void]$lines.Add("Milestone: $($Phase['milestone']). This phase produces one pull request.")
    [void]$lines.Add('')
    [void]$lines.Add("PR title: $($Phase['pr_title'])")
    [void]$lines.Add('')
    [void]$lines.Add($Phase['outcome'])
    [void]$lines.Add('')
    Add-PlanLines $lines 'Merge after phases' $Phase['merge_after']
    Add-PlanLines $lines 'Exit criteria' $Phase['exit_criteria']
    Add-PlanLines $lines 'Phase decision gates' $Phase['gates']
    [void]$lines.Add('Task start dependencies permit preparation before phase integration dependencies finish. Open decision gates block the affected implementation or integration; contract and decision discovery can proceed.')
    [void]$lines.Add('')
    [void]$lines.Add('## Contracts')
    [void]$lines.Add('')
    foreach ($id in $Phase['contracts']) {
        $contract = @($Plan['contracts'] | Where-Object { $_['id'] -ceq $id })[0]
        [void]$lines.Add("### $id — $($contract['title'])")
        [void]$lines.Add('')
        [void]$lines.Add("Owner phase: $($contract['phase']). Freeze task: $($contract['freeze_task']).")
        [void]$lines.Add('')
        Add-PlanLines $lines 'Requirements' $contract['requirements']
    }
    [void]$lines.Add('## Tasks')
    [void]$lines.Add('')
    $phaseTasks = @($Plan['tasks'] | Where-Object { $_['phase'] -ceq $Phase['id'] })
    foreach ($task in $phaseTasks) {
        [void]$lines.Add("### $($task['id']) — $($task['title'])")
        [void]$lines.Add('')
        [void]$lines.Add("Kind: $($task['kind']). Lane: $($task['lane']).")
        [void]$lines.Add('')
        foreach ($field in @('owns', 'shared_files', 'contracts', 'needs_to_start', 'needs_to_merge', 'gates', 'acceptance', 'verification', 'constraints')) {
            Add-PlanLines $lines $field $task[$field]
        }
    }
    [void]$lines.Add('## Decision details')
    [void]$lines.Add('')
    $gateIDs = @($Phase['gates']) + @($phaseTasks | ForEach-Object { $_['gates'] })
    foreach ($gate in $Plan['decision_gates']) {
        if ($gateIDs -cnotcontains $gate['id'] -and $gate['blocks'] -cnotcontains $Phase['id']) { continue }
        [void]$lines.Add("### $($gate['id']) — $($gate['topic'])")
        [void]$lines.Add('')
        [void]$lines.Add("Status: $($gate['status']). Owner: $($gate['owner']).")
        [void]$lines.Add('')
        [void]$lines.Add($gate['required_decision'])
        [void]$lines.Add('')
        Add-PlanLines $lines 'Blocks phase integration' $gate['blocks']
        Add-PlanLines $lines 'Acceptance' $gate['acceptance']
    }
    return ($lines -join "`n").TrimEnd() + "`n"
}

function Invoke-PlanCheck {
    param([string]$Path, [switch]$SkipDocuments, [switch]$WriteDocs)
    if ($SkipDocuments -and $WriteDocs) { throw 'Choose SkipDocuments or WriteDocs, not both.' }
    $fullPath = [IO.Path]::GetFullPath($Path)
    $plan = Get-Content -LiteralPath $fullPath -Raw | ConvertFrom-Json -AsHashtable
    Assert-Plan $plan
    if (-not $SkipDocuments) {
        $documents = Join-Path (Split-Path $fullPath -Parent) 'phases'
        if ($WriteDocs) { New-Item -ItemType Directory -Path $documents -Force | Out-Null }
        foreach ($phase in $plan['phases']) {
            $path = Join-Path $documents "$($phase['id']).md"
            $expected = Get-PhaseDocument $plan $phase
            if ($WriteDocs) { [IO.File]::WriteAllText($path, $expected, [Text.UTF8Encoding]::new($false)) }
            elseif (-not (Test-Path -LiteralPath $path) -or (Get-Content -LiteralPath $path -Raw).Replace("`r`n", "`n").Replace("`r", "`n") -cne $expected) {
                throw "Stale or missing phase document: $path. Run scripts/check-plan.ps1 -WriteDocs."
            }
        }
        $expectedNames = @($plan['phases'] | ForEach-Object { "$($_['id']).md" })
        foreach ($file in Get-ChildItem -LiteralPath $documents -Filter '*.md' -File) {
            if ($expectedNames -cnotcontains $file.Name) { throw "Obsolete phase document: $($file.FullName). Review and remove it explicitly." }
        }
    }
    Write-Host "Validated $(@($plan['phases']).Count) phases, $(@($plan['tasks']).Count) tasks, contracts, gates, and dependency graphs."
}

if ($MyInvocation.InvocationName -ne '.') { Invoke-PlanCheck $PlanPath -SkipDocuments:$SkipDocuments -WriteDocs:$WriteDocs }
