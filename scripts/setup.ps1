[CmdletBinding()]
param(
    [string]$Root,
    # Retained for callers of the previous setup; local setup is always unattended.
    [switch]$NonInteractive,
    # Compatibility only: custom launchers are now protected even with this switch.
    [switch]$RefreshLaunchers
)
$ErrorActionPreference = 'Stop'
# Resolve after parameter binding: Windows PowerShell -File can leave
# $PSScriptRoot empty while evaluating parameter default expressions.
if ([string]::IsNullOrWhiteSpace($Root)) {
    if (-not [string]::IsNullOrWhiteSpace($PSScriptRoot)) {
        $Root = Split-Path -Path $PSScriptRoot -Parent
    } else {
        $Root = (Get-Location).Path
    }
}
$Root = [IO.Path]::GetFullPath($Root)
$envPath = Join-Path $Root '.env'
$utf8 = New-Object Text.UTF8Encoding($false)
$fresh = -not (Test-Path -LiteralPath $envPath)
$lines = [Collections.Generic.List[string]]::new()
if (-not $fresh) { $lines.AddRange([IO.File]::ReadAllLines($envPath)) }
$hadNativeSensorSettings = @($lines | Where-Object { $_ -match '^\s*NERVE_SENSOR_TOKEN\s*=' }).Count -gt 0
$scriptDirectory = $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($scriptDirectory)) { $scriptDirectory = Join-Path $Root 'scripts' }
. (Join-Path $scriptDirectory 'setup-common.ps1')
# Fill missing keys only. Preserve comments, custom values and quoting.
foreach ($line in [IO.File]::ReadAllLines((Join-Path $Root '.env.example'))) {
    if ($line -match '^([A-Z][A-Z0-9_]*)=(.*)$') {
        $key, $value = $Matches[1], $Matches[2]
        if (-not ($lines | Where-Object { $_ -match ('^\s*' + $key + '\s*=') })) { Set-Value $key $value }
    }
}
foreach ($key in @('POSTGRES_PASSWORD','NERVE_DISCOVERY_AGENT_TOKEN','NERVE_SENSOR_TOKEN','NERVE_CORE_SIGNING_KEY')) {
    if ((Get-Value $key) -in @('', 'change-me')) { Set-Value $key (New-Secret) }
}
if ((Get-Value 'NERVE_DATABASE_URL') -match ':change-me@') {
    Set-Value 'NERVE_DATABASE_URL' ('postgres://' + (Get-Value 'POSTGRES_USER') + ':' + (Get-Value 'POSTGRES_PASSWORD') + '@postgres:5432/' + (Get-Value 'POSTGRES_DB') + '?sslmode=disable')
}
# Existing installations keep their configured port, even while NERVE is running.
if ($fresh) {
    $port = $null
    foreach ($candidate in @(3000, 0)) {
        $listener = $null
        try {
            if ([Net.Sockets.Socket]::OSSupportsIPv6) {
                $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::IPv6Any, $candidate)
                $listener.Server.DualMode = $true
            } else {
                $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Any, $candidate)
            }
            $listener.ExclusiveAddressUse = $true
            $listener.Start()
            $port = $listener.LocalEndpoint.Port
            break
        } catch [Net.Sockets.SocketException] {
            # Port 0 lets Windows allocate an available, non-reserved ephemeral port.
        } finally { if ($null -ne $listener) { $listener.Stop() } }
    }
    if ($null -eq $port) { throw 'No available web port could be selected. Existing services were not changed.' }
    Set-Value 'NERVE_WEB_PORT' ([string]$port)
    Set-Value 'NERVE_ALLOWED_ORIGIN' ('http://localhost:' + $port)
    Set-Value 'NEXT_PUBLIC_SITE_URL' ('http://localhost:' + $port)
    Set-Value 'NERVE_TRAFFIC_CAPTURE_MODE' 'windows-native'
}
# Upgrade only the previous unconfigured Windows default. External sources are retained.
if (-not $hadNativeSensorSettings -and (Get-Value 'NERVE_NTOPNG_URL') -eq '' -and (Get-Value 'NERVE_TRAFFIC_CAPTURE_MODE') -in @('', 'sensor-required')) {
    Set-Value 'NERVE_TRAFFIC_CAPTURE_MODE' 'windows-native'
}
if ((Get-Value 'NERVE_SENSOR_PORT') -eq '') {
    $sensorListener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    try { $sensorListener.Start(); Set-Value 'NERVE_SENSOR_PORT' ([string]$sensorListener.LocalEndpoint.Port) }
    finally { $sensorListener.Stop() }
}
Save-SetupEnvironment
Write-Host ('[OK] NERVE: http://localhost:' + (Get-Value 'NERVE_WEB_PORT'))
. (Join-Path $scriptDirectory 'setup-launchers.ps1')
Update-SetupLaunchers $Root
Write-Host '[OK] Konfiguration gespeichert. Bestehende Secrets und Daten bleiben erhalten. Start: start.bat'
