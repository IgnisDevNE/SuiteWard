# Offline checks for bot-token.ps1. No network, no real credentials; GitHub is replaced by an injected HTTP function.
#Requires -Version 7.2
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'bot-token.ps1') -Mode Library
$checks = 0
$testRoot = Join-Path ([IO.Path]::GetTempPath()) "suiteward-bot-token-$([Guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path (Join-Path $testRoot '.local') -Force | Out-Null

function Expect-Failure {
    param([scriptblock]$Action, [string]$Message)
    $rejected = $false
    try { & $Action | Out-Null } catch {
        if ($_.Exception.Message -notlike "*$Message*") { throw "Wrong rejection. Expected '$Message', got: $($_.Exception.Message)" }
        $rejected = $true
    }
    if (-not $rejected) { throw "Expected rejection containing: $Message" }
}

function ConvertFrom-Base64Url([string]$Value) {
    $padded = $Value.Replace('-', '+').Replace('_', '/')
    $padded += '=' * ((4 - $padded.Length % 4) % 4)
    return [Convert]::FromBase64String($padded)
}

function Write-TestConfig {
    param([hashtable]$Overrides = @{}, [switch]$SkipKey)
    $config = [ordered]@{ app_id = '12345'; installation_id = '67890'; private_key_path = '.local/test-app.pem'; repository = 'IgnisDevNE/SuiteWard'; bot_login = 'ignisdevne[bot]' }
    foreach ($key in $Overrides.Keys) { if ($null -eq $Overrides[$key]) { $config.Remove($key) } else { $config[$key] = $Overrides[$key] } }
    $config | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $testRoot '.local/github-app.json') -Encoding utf8NoBOM
    if ($SkipKey) { Remove-Item -LiteralPath (Join-Path $testRoot '.local/test-app.pem') -ErrorAction SilentlyContinue }
    else { Set-Content -LiteralPath (Join-Path $testRoot '.local/test-app.pem') -Value $script:rsa.ExportPkcs8PrivateKeyPem() -Encoding utf8NoBOM }
}

# A fake GitHub that records every request and answers /app and the installation token endpoint.
$script:requests = [Collections.Generic.List[object]]::new()
$script:slug = 'ignisdevne'
$script:tokenResponse = { [pscustomobject]@{ token = 'ghs_FAKE_INSTALLATION_TOKEN_VALUE'; repository_selection = 'selected'; repositories = @([pscustomobject]@{ name = 'SuiteWard' }) } }
$fakeHttp = {
    param($Method, $Uri, $Headers, $Body)
    $script:requests.Add([pscustomobject]@{ Method = $Method; Uri = $Uri; Headers = $Headers; Body = $Body })
    if ($Method -eq 'GET' -and $Uri -eq 'https://api.github.com/app') { return [pscustomobject]@{ slug = $script:slug } }
    if ($Method -eq 'POST' -and $Uri -eq 'https://api.github.com/app/installations/67890/access_tokens') { return (& $script:tokenResponse) }
    throw "Unexpected request: $Method $Uri"
}
$childPrintsLength = @('pwsh', '-NoProfile', '-Command', 'Write-Output ("len=" + $env:GH_TOKEN.Length + "/" + $env:GITHUB_TOKEN.Length)')

try {
    $script:rsa = [Security.Cryptography.RSA]::Create(2048)

    # JWT structure and signature.
    $now = [DateTimeOffset]::FromUnixTimeSeconds(1700000000)
    $jwt = New-GitHubAppJwt -AppId '12345' -PrivateKeyPem $rsa.ExportPkcs8PrivateKeyPem() -Now $now
    $parts = $jwt.Split('.')
    if ($parts.Count -ne 3) { throw 'JWT must have three segments.' }
    $header = [Text.Encoding]::UTF8.GetString((ConvertFrom-Base64Url $parts[0])) | ConvertFrom-Json
    $claims = [Text.Encoding]::UTF8.GetString((ConvertFrom-Base64Url $parts[1])) | ConvertFrom-Json
    if ($header.alg -cne 'RS256' -or $header.typ -cne 'JWT') { throw 'JWT header must declare RS256/JWT.' }
    if ("$($claims.iss)" -cne '12345') { throw 'JWT issuer must be the App ID.' }
    if ($claims.iat -ne 1700000000 - 60) { throw 'JWT iat must be 60 seconds in the past.' }
    if ($claims.exp -le 1700000000 -or $claims.exp - 1700000000 -gt 540) { throw 'JWT exp must be in the future and at most 9 minutes ahead.' }
    $signed = [Text.Encoding]::ASCII.GetBytes("$($parts[0]).$($parts[1])")
    if (-not $rsa.VerifyData($signed, (ConvertFrom-Base64Url $parts[2]), [Security.Cryptography.HashAlgorithmName]::SHA256, [Security.Cryptography.RSASignaturePadding]::Pkcs1)) { throw 'JWT signature does not verify with the public key.' }
    if ($jwt -match '[+/=]') { throw 'JWT must use unpadded base64url.' }
    $checks++

    # Argument handling: an optional leading -- is dropped; a command is required.
    if ((Resolve-BotCommand @('--', 'gh', 'pr', 'list')) -join ' ' -cne 'gh pr list') { throw 'Leading -- was not stripped.' }
    if ((Resolve-BotCommand @('gh', '--version')) -join ' ' -cne 'gh --version') { throw 'Later -- must be preserved for the child.' }
    Expect-Failure { Resolve-BotCommand @() } 'No command'
    Expect-Failure { Resolve-BotCommand @('--') } 'No command'
    $checks += 3

    # Configuration refusals happen before any network request.
    Write-TestConfig -SkipKey
    Remove-Item -LiteralPath (Join-Path $testRoot '.local/github-app.json')
    Expect-Failure { Invoke-WithBotToken -Root $testRoot -Command $childPrintsLength -Http $fakeHttp } 'configuration not found'
    Write-TestConfig -SkipKey
    Expect-Failure { Invoke-WithBotToken -Root $testRoot -Command $childPrintsLength -Http $fakeHttp } 'private key'
    foreach ($field in @('app_id', 'installation_id', 'private_key_path', 'repository', 'bot_login')) {
        Write-TestConfig @{ $field = $null }
        Expect-Failure { Invoke-WithBotToken -Root $testRoot -Command $childPrintsLength -Http $fakeHttp } $field
    }
    Write-TestConfig @{ repository = 'not-a-repository' }
    Expect-Failure { Invoke-WithBotToken -Root $testRoot -Command $childPrintsLength -Http $fakeHttp } 'owner/name'
    Write-TestConfig
    Expect-Failure { Invoke-WithBotToken -Root $testRoot -Command @() -Http $fakeHttp } 'No command'
    if ($script:requests.Count) { throw 'A refusal contacted GitHub.' }
    $checks += 5

    # Slug mismatch refuses before an installation token is requested.
    $script:slug = 'someone-else'
    Expect-Failure { Invoke-WithBotToken -Root $testRoot -Command $childPrintsLength -Http $fakeHttp } 'does not match'
    if (@($script:requests | Where-Object Method -eq 'POST').Count) { throw 'Installation token requested for the wrong App.' }
    $script:slug = 'ignisdevne'
    $script:requests.Clear()
    $checks++

    # A token that is not restricted to the configured repository is refused and never used.
    foreach ($bad in @(
            { [pscustomobject]@{ token = 'ghs_BROAD'; repository_selection = 'all' } },
            { [pscustomobject]@{ token = 'ghs_OTHER'; repository_selection = 'selected'; repositories = @([pscustomobject]@{ name = 'Other' }) } },
            { [pscustomobject]@{ token = ''; repository_selection = 'selected'; repositories = @([pscustomobject]@{ name = 'SuiteWard' }) } })) {
        $script:tokenResponse = $bad
        Expect-Failure { Invoke-WithBotToken -Root $testRoot -Command $childPrintsLength -Http $fakeHttp } 'installation token'
    }
    $script:tokenResponse = { [pscustomobject]@{ token = 'ghs_FAKE_INSTALLATION_TOKEN_VALUE'; repository_selection = 'selected'; repositories = @([pscustomobject]@{ name = 'SuiteWard' }) } }
    $script:requests.Clear()
    $checks++

    # Successful run: the child sees both variables, the caller's environment is restored, and nothing prints the token.
    $env:GH_TOKEN = 'caller-gh-token'; Remove-Item Env:GITHUB_TOKEN -ErrorAction SilentlyContinue
    $output = @(Invoke-WithBotToken -Root $testRoot -Command $childPrintsLength -Http $fakeHttp *>&1 | ForEach-Object { "$_" })
    $tokenLength = 'ghs_FAKE_INSTALLATION_TOKEN_VALUE'.Length
    if ($output -notcontains "len=$tokenLength/$tokenLength") { throw "Child did not receive the token in GH_TOKEN and GITHUB_TOKEN: $($output -join '|')" }
    if ($env:GH_TOKEN -cne 'caller-gh-token') { throw 'Caller GH_TOKEN was not restored.' }
    if (Test-Path Env:GITHUB_TOKEN) { throw 'GITHUB_TOKEN leaked into the caller environment.' }
    Remove-Item Env:GH_TOKEN
    $everything = $output -join "`n"
    if ($everything -match 'ghs_FAKE' -or $everything -match [regex]::Escape($jwt.Substring(0, 10))) { throw 'Output contains a token.' }
    $get = $script:requests | Where-Object Method -eq 'GET'
    $post = $script:requests | Where-Object Method -eq 'POST'
    if (@($get).Count -ne 1 -or @($post).Count -ne 1) { throw 'Expected one /app and one token request.' }
    $bearer = $get.Headers['Authorization']
    if (-not $bearer.StartsWith('Bearer ') -or $bearer.Split('.').Count -ne 3) { throw 'App requests must carry the App JWT.' }
    $body = $post.Body | ConvertFrom-Json
    if (@($body.repositories).Count -ne 1 -or $body.repositories[0] -cne 'SuiteWard') { throw 'Installation token must be restricted to the configured repository.' }
    $checks++

    # The caller's environment is restored when the child fails, and the child's exit code is reported.
    $exit = Invoke-WithBotToken -Root $testRoot -Command @('pwsh', '-NoProfile', '-Command', 'exit 7') -Http $fakeHttp
    if ($exit -ne 7) { throw "Child exit code was not returned: $exit" }
    if (Test-Path Env:GH_TOKEN) { throw 'GH_TOKEN leaked after a failing child.' }
    $checks++

    # A failing HTTP layer reports no secret.
    $leaky = { throw 'transport failed with Authorization: Bearer SECRET-JWT' }
    try { Invoke-WithBotToken -Root $testRoot -Command $childPrintsLength -Http $leaky } catch { $failure = $_.Exception.Message }
    if (-not (Test-Path variable:failure) -or $failure -match 'SECRET-JWT' -or $failure -notmatch 'GitHub') { throw 'HTTP failure text must be replaced by a generic message.' }
    $checks++

    # The script file never persists or prints credentials: it contains no Write-* of token variables and no global config changes.
    $source = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'bot-token.ps1') -Raw
    if ($source -match 'git\s+config|gh\s+auth|Set-Content|Out-File|Add-Content') { throw 'bot-token.ps1 must not write files or change git/gh configuration.' }
    $checks++
    Write-Host "Passed $checks bot-token checks. No network or real credentials were used."
} finally {
    Remove-Item Env:GH_TOKEN -ErrorAction SilentlyContinue
    Remove-Item Env:GITHUB_TOKEN -ErrorAction SilentlyContinue
    if ($script:rsa) { $script:rsa.Dispose() }
    Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
}
