#Requires -RunAsAdministrator
<#
.SYNOPSIS
Removes the NERVE agent service and binary.

.PARAMETER Purge
Also deletes the identity, configuration and logs in "%ProgramData%\NERVE\agent".
Revoke or remove the agent in NERVE as well.
#>
[CmdletBinding()]
param([switch]$Purge)
$ErrorActionPreference = 'Stop'

$target = Join-Path $env:ProgramFiles 'NERVE\Agent'
$installed = Join-Path $target 'nerve-agent.exe'
if (-not (Test-Path -LiteralPath $installed)) {
    Write-Host "$installed is not installed."
    return
}
$arguments = @('service', 'uninstall')
if ($Purge) { $arguments += '--purge' }
& $installed @arguments
if ($LASTEXITCODE -ne 0) { throw "nerve-agent service uninstall failed with exit code $LASTEXITCODE." }
Remove-Item -LiteralPath $target -Recurse -Force
Write-Host "Removed $target."
