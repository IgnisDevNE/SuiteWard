# Runs one command as the authorized GitHub App bot: ./scripts/bot-token.ps1 -- gh pr create ...
# The installation token exists only in memory and in the child's environment. It is never printed, logged, or stored,
# and this script never changes global git or gh configuration. Failure never falls back to personal credentials.
#Requires -Version 7.2
[CmdletBinding(PositionalBinding = $false)]
param(
    [ValidateSet('Run', 'Library')][string]$Mode = 'Run',
    [string]$Root = (Split-Path $PSScriptRoot -Parent),
    [Parameter(ValueFromRemainingArguments = $true)][string[]]$CommandLine = @()
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function ConvertTo-Base64Url {
    param([byte[]]$Bytes)
    return [Convert]::ToBase64String($Bytes).TrimEnd('=').Replace('+', '-').Replace('/', '_')
}

function New-GitHubAppJwt {
    param([string]$AppId, [string]$PrivateKeyPem, [DateTimeOffset]$Now = [DateTimeOffset]::UtcNow)
    $seconds = $Now.ToUnixTimeSeconds()
    # iat is backdated for clock drift; GitHub allows at most 10 minutes of lifetime.
    $claims = ConvertTo-Json -Compress ([ordered]@{ iat = $seconds - 60; exp = $seconds + 480; iss = $AppId })
    $signingInput = (ConvertTo-Base64Url ([Text.Encoding]::UTF8.GetBytes('{"alg":"RS256","typ":"JWT"}'))) + '.' + (ConvertTo-Base64Url ([Text.Encoding]::UTF8.GetBytes($claims)))
    $rsa = [Security.Cryptography.RSA]::Create()
    try {
        try { $rsa.ImportFromPem($PrivateKeyPem) } catch { throw 'Cannot read the GitHub App private key.' }
        $signature = $rsa.SignData([Text.Encoding]::ASCII.GetBytes($signingInput), [Security.Cryptography.HashAlgorithmName]::SHA256, [Security.Cryptography.RSASignaturePadding]::Pkcs1)
    } finally { $rsa.Dispose() }
    return "$signingInput.$(ConvertTo-Base64Url $signature)"
}

function Resolve-BotCommand {
    param([string[]]$Arguments)
    $command = @($Arguments)
    if ($command.Count -gt 0 -and $command[0] -ceq '--') { $command = @($command | Select-Object -Skip 1) }
    if ($command.Count -eq 0) { throw 'No command given. Usage: ./scripts/bot-token.ps1 -- <command> [args...]' }
    return $command
}

function Get-BotConfig {
    param([string]$Root)
    $path = Join-Path $Root '.local/github-app.json'
    if (-not (Test-Path -LiteralPath $path)) { throw 'GitHub App configuration not found: .local/github-app.json' }
    $json = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
    foreach ($field in @('app_id', 'installation_id', 'private_key_path', 'repository', 'bot_login')) {
        $property = $json.PSObject.Properties[$field]
        if (-not $property -or -not "$($property.Value)".Trim()) { throw ".local/github-app.json is missing $field." }
    }
    foreach ($field in @('app_id', 'installation_id')) {
        if ("$($json.$field)" -cnotmatch '^[0-9]+$') { throw "$field in .local/github-app.json must be numeric." }
    }
    if (("$($json.bot_login)" -replace '\[bot\]$', '') -eq '') { throw 'bot_login in .local/github-app.json must name the App slug.' }
    if ("$($json.repository)" -cnotmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') { throw 'repository in .local/github-app.json must look like owner/name.' }
    $keyPath = [IO.Path]::GetFullPath("$($json.private_key_path)", $Root)
    if (-not (Test-Path -LiteralPath $keyPath -PathType Leaf)) { throw 'The GitHub App private key file was not found.' }
    return [pscustomobject]@{
        AppId = "$($json.app_id)"; InstallationId = "$($json.installation_id)"; KeyPath = $keyPath
        RepositoryName = "$($json.repository)".Split('/')[1]
        Slug = "$($json.bot_login)" -replace '\[bot\]$', ''
    }
}

function Send-GitHubRequest {
    param([string]$Method, [string]$Uri, [hashtable]$Headers, [string]$Body)
    # HTTP errors are read from the status code instead of caught: a failed Invoke-RestMethod leaves an ErrorRecord
    # in $Error whose request message carries the Authorization header.
    $parameters = @{ Method = $Method; Uri = $Uri; Headers = $Headers; TimeoutSec = 30; SkipHttpErrorCheck = $true }
    if ($Body) { $parameters.Body = $Body; $parameters.ContentType = 'application/json' }
    $recorded = $global:Error.Count
    try { $response = Invoke-WebRequest @parameters }
    catch {
        # Transport failures (no response) still produce records that reference the request; drop what this call added.
        for ($extra = $global:Error.Count - $recorded; $extra -gt 0; $extra--) { $global:Error.RemoveAt(0) }
        throw 'HTTP 000'
    }
    $status = [int]$response.StatusCode
    if ($status -lt 200 -or $status -ge 300) { throw "HTTP $status" }
    return $response.Content | ConvertFrom-Json
}

function Invoke-GitHubApi {
    param([scriptblock]$Http, [string]$Method, [string]$Path, [hashtable]$Headers, [string]$Body = '')
    try { return & $Http $Method "https://api.github.com$Path" $Headers $Body }
    catch {
        # Transport errors can echo request headers; report only the operation and an HTTP status.
        $status = if ($_.Exception.Message -cmatch '^HTTP [0-9]{3}$') { " ($($_.Exception.Message))" } else { '' }
        throw "GitHub request failed: $Method $Path$status."
    }
}

function Resolve-BotExecutable {
    param([string]$Name)
    $executable = Get-Command $Name -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $executable) { throw "Command not found: $Name" }
    # cmd.exe re-parses arguments, so quoting is unsafe for batch scripts; only real executables run with the token.
    if ($executable.Source -match '\.(cmd|bat)$') { throw "The command must be a real executable, not a batch script: $Name" }
    return $executable.Source
}

function Get-PropertyValue {
    param($Object, [string]$Name)
    $property = $Object.PSObject.Properties[$Name]
    if ($property) { return $property.Value }
    return $null
}

function Invoke-WithBotToken {
    param(
        [string]$Root = (Split-Path $PSScriptRoot -Parent),
        [string[]]$Command,
        [scriptblock]$Http = { param($Method, $Uri, $Headers, $Body) Send-GitHubRequest $Method $Uri $Headers $Body }
    )
    $Command = Resolve-BotCommand $Command
    $executable = Resolve-BotExecutable $Command[0]
    $config = Get-BotConfig $Root
    $jwt = New-GitHubAppJwt $config.AppId (Get-Content -LiteralPath $config.KeyPath -Raw)
    $headers = @{ Authorization = "Bearer $jwt"; Accept = 'application/vnd.github+json'; 'X-GitHub-Api-Version' = '2022-11-28'; 'User-Agent' = 'suiteward-bot-token' }
    $app = Invoke-GitHubApi $Http GET '/app' $headers
    $appSlug = "$(Get-PropertyValue $app 'slug')"
    if (-not $appSlug -or $appSlug -cne $config.Slug) { throw 'The GitHub App slug does not match bot_login; refusing to continue.' }
    $body = ConvertTo-Json -Compress @{ repositories = @($config.RepositoryName) }
    $installation = Invoke-GitHubApi $Http POST "/app/installations/$($config.InstallationId)/access_tokens" $headers $body
    $token = "$(Get-PropertyValue $installation 'token')"
    $repositories = @(Get-PropertyValue $installation 'repositories')
    if (-not $token -or (Get-PropertyValue $installation 'repository_selection') -cne 'selected' -or $repositories.Count -ne 1 -or "$(Get-PropertyValue $repositories[0] 'name')" -cne $config.RepositoryName) {
        throw 'GitHub did not return an installation token restricted to the configured repository.'
    }

    $start = [Diagnostics.ProcessStartInfo]::new($executable)
    foreach ($argument in ($Command | Select-Object -Skip 1)) { $start.ArgumentList.Add($argument) }
    $start.UseShellExecute = $false
    # Only the child process receives the token; the caller's environment is never modified.
    $start.Environment['GH_TOKEN'] = $token
    $start.Environment['GITHUB_TOKEN'] = $token
    $process = [Diagnostics.Process]::Start($start)
    try { $process.WaitForExit(); return $process.ExitCode } finally { $process.Dispose() }
}

if ($Mode -eq 'Library') { return }
exit (Invoke-WithBotToken -Root $Root -Command $CommandLine)
