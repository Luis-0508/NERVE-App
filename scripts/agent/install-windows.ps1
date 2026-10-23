#Requires -RunAsAdministrator
<#
.SYNOPSIS
Installs or upgrades the NERVE agent as a Windows service.

.EXAMPLE
.\install-windows.ps1 -ApiUrl https://nerve.example/api/v1 -TokenFile .\token

.EXAMPLE
.\install-windows.ps1    # upgrade: keeps configuration and identity

.NOTES
The binary is copied to "%ProgramFiles%\NERVE\Agent" (writable only by
administrators). The service runs as the virtual account NT SERVICE\NERVEAgent,
restarts automatically after failures and keeps its identity in
"%ProgramData%\NERVE\agent".
#>
[CmdletBinding()]
param(
    [string]$ApiUrl,
    [string]$TokenFile,
    [string]$CaFile,
    [switch]$Force,
    [switch]$NoStart,
    [string]$Binary = (Join-Path $PSScriptRoot 'nerve-agent.exe')
)
$ErrorActionPreference = 'Stop'

# A downloaded release names the binary nerve-agent_windows_amd64.exe.
$releaseBinary = Join-Path $PSScriptRoot 'nerve-agent_windows_amd64.exe'
if (-not $PSBoundParameters.ContainsKey('Binary') -and -not (Test-Path -LiteralPath $Binary -PathType Leaf) -and (Test-Path -LiteralPath $releaseBinary -PathType Leaf)) {
    $Binary = $releaseBinary
}
if (-not (Test-Path -LiteralPath $Binary -PathType Leaf)) {
    throw "Agent binary not found: $Binary (use -Binary PATH)."
}
& $Binary version | Out-Null
if ($LASTEXITCODE -ne 0) { throw "$Binary is not a working NERVE agent binary." }

$target = Join-Path $env:ProgramFiles 'NERVE\Agent'
$installed = Join-Path $target 'nerve-agent.exe'
New-Item -ItemType Directory -Force -Path $target | Out-Null

# A running service locks its binary: stop it gracefully before replacing it.
$service = Get-Service -Name 'NERVEAgent' -ErrorAction SilentlyContinue
if ($service -and $service.Status -ne 'Stopped') {
    Write-Host 'Stopping the running NERVE agent service...'
    Stop-Service -Name 'NERVEAgent'
    $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
}
if ((Resolve-Path -LiteralPath $Binary).Path -ne $installed) {
    Copy-Item -LiteralPath $Binary -Destination $installed -Force
}
Write-Host "Installed $installed ($(& $installed version))."

$arguments = @('service', 'install')
if ($ApiUrl) { $arguments += @('--api-url', $ApiUrl) }
if ($TokenFile) { $arguments += @('--token-file', (Resolve-Path -LiteralPath $TokenFile).Path) }
if ($CaFile) { $arguments += @('--ca-file', (Resolve-Path -LiteralPath $CaFile).Path) }
if ($Force) { $arguments += '--force' }
if ($NoStart) { $arguments += '--no-start' }
& $installed @arguments
if ($LASTEXITCODE -ne 0) { throw "nerve-agent service install failed with exit code $LASTEXITCODE." }
