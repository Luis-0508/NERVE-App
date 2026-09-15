[CmdletBinding()]
param([string]$Root)
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($Root)) { $Root = Split-Path -Path $PSScriptRoot -Parent }
$Root = [IO.Path]::GetFullPath($Root)
$lines = [Collections.Generic.List[string]]::new()
$lines.AddRange([IO.File]::ReadAllLines((Join-Path $Root '.env')))
. (Join-Path $PSScriptRoot 'setup-common.ps1')
if ((Get-Value 'NERVE_TRAFFIC_CAPTURE_MODE') -ne 'windows-native') {
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'manage-traffic-sensor.ps1') -Action Stop -Root $Root
    if ($LASTEXITCODE -ne 0) { throw 'Vorheriger Windows-Sensor konnte nicht gestoppt werden.' }
    Write-Host '[INFO] Bestehende externe Traffic-Quelle wird verwendet.'; exit 0
}
Push-Location -LiteralPath $Root
try {
    $stateRoot = [IO.Path]::GetFullPath((Join-Path $Root '.nerve'))
    if ((Test-Path -LiteralPath $stateRoot) -and ((Get-Item -LiteralPath $stateRoot -Force).Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Sensor-Zustandsverzeichnis darf kein Link sein.' }
    $stagePath = [IO.Path]::GetFullPath((Join-Path $stateRoot 'sensor-build'))
    if (-not $stagePath.StartsWith($stateRoot + [IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)) { throw 'Ungueltiger Sensor-Buildpfad.' }
    if (Test-Path -LiteralPath $stagePath) {
        if ((Get-Item -LiteralPath $stagePath -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Sensor-Buildpfad darf kein Link sein.' }
        Remove-Item -LiteralPath $stagePath -Recurse -Force
    }
    & docker build --file Dockerfile.sensor --target artifact --output 'type=local,dest=.nerve/sensor-build' .
    if ($LASTEXITCODE -ne 0) { throw 'Windows Traffic Sensor konnte nicht gebaut werden.' }
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'manage-traffic-sensor.ps1') -Action Start -Root $Root
    exit $LASTEXITCODE
} finally { Pop-Location }
