param([string]$ComposeBinary = 'docker', [switch]$Standalone)
$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$prefix = @('compose')
if ($Standalone) { $prefix = @() }
function Compose([string[]]$Arguments) {
    $output = & $ComposeBinary @prefix @Arguments
    if ($LASTEXITCODE -ne 0) { throw 'Compose validation failed.' }
    return $output
}
# Templates contain placeholders only. Never print interpolated configuration.
Compose @('--env-file', (Join-Path $repo '.env.example'), '-f', (Join-Path $repo 'docker-compose.yml'), 'config', '--quiet') | Out-Null
$fixture = Get-ChildItem -LiteralPath (Join-Path $repo '.nerve') -Directory -Filter 'setup-tests-*' |
    Where-Object { Test-Path -LiteralPath (Join-Path $_.FullName '.nerve/sensor/compose.yaml') } |
    Sort-Object LastWriteTime -Descending | Select-Object -First 1
if (-not $fixture) { throw 'Run scripts/test-setup.ps1 first.' }
$model = (Compose @('--env-file', (Join-Path $fixture.FullName '.env'), '-f', (Join-Path $repo 'docker-compose.yml'), 'config', '--format', 'json')) -join "`n" | ConvertFrom-Json
if ($model.services.api.environment.NERVE_TRAFFIC_CAPTURE_MODE -ne 'linux-interface' -or $model.services.api.environment.NERVE_NTOPNG_INTERFACE_ID -ne 'auto') { throw 'Capture environment not passed to API.' }
if ($model.services.web.environment.NERVE_NTOPNG_PASSWORD -or $model.services.web.build.args.NERVE_NTOPNG_PASSWORD) { throw 'Frontend credential leak.' }
$reader = [IO.File]::ReadAllText((Join-Path $fixture.FullName '.nerve/sensor/reader.password'))
if ($model.services.api.environment.NERVE_NTOPNG_PASSWORD -cne $reader) { throw 'Compose changed the sensor password.' }
if ($model.services.PSObject.Properties.Name -contains 'ntopng') { throw 'Desktop must not silently start a capture container.' }
$bundle = Join-Path $fixture.FullName '.nerve/sensor'
$sensor = (Compose @('--env-file', (Join-Path $bundle '.env'), '-f', (Join-Path $bundle 'compose.yaml'), 'config', '--format', 'json')) -join "`n" | ConvertFrom-Json
if ($sensor.services.ntopng.network_mode -ne 'host' -or -not $sensor.services.ntopng.healthcheck -or $sensor.services.ntopng.ports) { throw 'Unexpected sensor network or health configuration.' }
Write-Host 'PASS: Compose templates, generated setup, API environment, secret round-trip, frontend isolation and Linux sensor configuration.'
