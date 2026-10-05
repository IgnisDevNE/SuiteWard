# Stub so the behavior tests can fail for the right reason; implemented in the next commit.
[CmdletBinding(PositionalBinding = $false)]
param([ValidateSet('Run', 'Library')][string]$Mode = 'Run')
function New-GitHubAppJwt { param($AppId, $PrivateKeyPem, $Now) return '' }
function Resolve-BotCommand { param([string[]]$Arguments) return @() }
function Invoke-WithBotToken { param($Root, $Command, $Http) return 0 }
if ($Mode -eq 'Library') { return }
