[CmdletBinding()]
param(
    [string]$Root,
    [switch]$NonInteractive,
    [ValidateSet('preserve','sensor-required','external','linux-interface')][string]$TrafficMode = 'preserve',
    [string]$SensorAddress, [string]$CaptureInterface, [string]$NtopngURL, [string]$Username,
    [Security.SecureString]$Password, [string]$InterfaceID, [switch]$RefreshLaunchers
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
function Get-Value([string]$Key) {
    $value = ''
    foreach ($line in $lines) { if ($line -match ('^\s*' + [regex]::Escape($Key) + '\s*=(.*)$')) { $value = $Matches[1].Trim() } }
    if ($value.StartsWith("'") -and $value.EndsWith("'")) { return $value.Substring(1,$value.Length-2).Replace("\'", "'") }
    if ($value.StartsWith('"') -and $value.EndsWith('"')) { return $value.Substring(1,$value.Length-2) }
    return $value
}
function Set-Value([string]$Key, [string]$Value, [switch]$Literal) {
    if ($Value -match '[\r\n\x00]') { throw 'Configuration values must be single-line.' }
    if ($Literal) { $Value = "'" + $Value.Replace("'", "\'") + "'" }
    $found = $false
    for ($i=0; $i -lt $lines.Count; $i++) {
        if ($lines[$i] -match ('^\s*' + [regex]::Escape($Key) + '\s*=')) { $lines[$i] = $Key + '=' + $Value; $found = $true }
    }
    if (-not $found) { $lines.Add($Key + '=' + $Value) }
}
function New-Secret {
    $bytes = New-Object byte[] 32
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    return [BitConverter]::ToString($bytes).Replace('-','').ToLowerInvariant()
}
# Fill missing keys only. Preserve comments, custom values and quoting.
foreach ($line in [IO.File]::ReadAllLines((Join-Path $Root '.env.example'))) {
    if ($line -match '^([A-Z][A-Z0-9_]*)=(.*)$') {
        $key, $value = $Matches[1], $Matches[2]
        if (-not ($lines | Where-Object { $_ -match ('^\s*' + $key + '\s*=') })) { Set-Value $key $value }
    }
}
foreach ($key in @('POSTGRES_PASSWORD','NERVE_DISCOVERY_AGENT_TOKEN')) {
    if ((Get-Value $key) -in @('', 'change-me')) { Set-Value $key (New-Secret) }
}
if ((Get-Value 'NERVE_DATABASE_URL') -match ':change-me@') {
    Set-Value 'NERVE_DATABASE_URL' ('postgres://' + (Get-Value 'POSTGRES_USER') + ':' + (Get-Value 'POSTGRES_PASSWORD') + '@postgres:5432/' + (Get-Value 'POSTGRES_DB') + '?sslmode=disable')
}
if (-not $NonInteractive) {
    if ($fresh) {
        $port = Read-Host 'Web-Port [3000]'
        if ($port) {
            $number = 0
            if (-not [int]::TryParse($port,[ref]$number) -or $number -lt 1 -or $number -gt 65535) { throw 'Invalid web port.' }
            Set-Value 'NERVE_WEB_PORT' $port
            Set-Value 'NERVE_ALLOWED_ORIGIN' ('http://localhost:' + $port)
            Set-Value 'NEXT_PUBLIC_SITE_URL' ('http://localhost:' + $port)
        }
    }
    Write-Host 'Docker Desktop erfasst keine Windows-Netzwerkkarte. Discovery funktioniert ohne Traffic-Sensor.'
    $choice = Read-Host 'Traffic: [Enter] beibehalten/kein Sensor; [e] externes ntopng; [l] Linux-Sensor vorbereiten'
    if ($choice -eq 'e') { $TrafficMode = 'external' }
    elseif ($choice -eq 'l') { $TrafficMode = 'linux-interface' }
    elseif ($choice) { throw 'Unknown traffic setup option.' }
}
if ($TrafficMode -eq 'external') {
    if (-not $NonInteractive) {
        if (-not $NtopngURL) { $NtopngURL = Read-Host 'ntopng-URL aus Sicht des API-Containers (Host: host.docker.internal)' }
        if (-not $Username) { $Username = Read-Host 'ntopng-Benutzer' }
        if (-not $Password) { $Password = Read-Host 'ntopng-Passwort' -AsSecureString }
        if (-not $InterfaceID) { $InterfaceID = Read-Host 'Interface-ID [auto: nur bei genau einem Interface]' }
    }
    $uri = $null
    if (-not [Uri]::TryCreate($NtopngURL,[UriKind]::Absolute,[ref]$uri) -or $uri.Scheme -notin @('http','https') -or $uri.UserInfo -or $uri.Query -or $uri.Fragment) { throw 'Use an http(s) URL without credentials, query or fragment.' }
    if (-not $Username -or -not $Password -or $Password.Length -eq 0) { throw 'Username and password are required.' }
    $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($Password)
    try { Set-Value 'NERVE_NTOPNG_PASSWORD' ([Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr)) -Literal }
    finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr) }
    Set-Value 'NERVE_NTOPNG_URL' $NtopngURL -Literal
    Set-Value 'NERVE_NTOPNG_USERNAME' $Username -Literal
    if (-not $InterfaceID) { $InterfaceID = 'auto' }
    if ($InterfaceID -notmatch '^(auto|[0-9]+)$') { throw 'Interface ID must be auto or a non-negative integer.' }
    Set-Value 'NERVE_NTOPNG_INTERFACE_ID' $InterfaceID
}
if ($TrafficMode -eq 'linux-interface') {
    if (-not $NonInteractive) {
        Write-Host 'Benoetigt nativen Linux-Docker-Host mit physischem NIC. Nicht Docker Desktop/WSL.'
        Write-Host 'HTTP-Port 3001 nur im geschuetzten Managementnetz freigeben; sonst TLS/VPN verwenden.'
        if (-not $SensorAddress) { $SensorAddress = Read-Host 'IPv4-Managementadresse des Linux-Sensors' }
        if (-not $CaptureInterface) { $CaptureInterface = Read-Host 'Linux-Capture-Interface (z.B. enp3s0)' }
    }
    $ip = $null
    if (-not [Net.IPAddress]::TryParse($SensorAddress,[ref]$ip) -or $ip.AddressFamily -ne [Net.Sockets.AddressFamily]::InterNetwork -or $SensorAddress -in @('0.0.0.0','127.0.0.1')) { throw 'An explicit reachable Linux management IPv4 address is required.' }
    if ($CaptureInterface -notmatch '^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,14}$' -or $CaptureInterface -match '^(lo$|docker|veth|br-)') { throw 'Select the Linux capture NIC explicitly.' }
    $bundle = Join-Path $Root '.nerve/sensor'
    New-Item -ItemType Directory -Force -Path $bundle | Out-Null
    foreach ($name in @('compose.yaml','Dockerfile','.dockerignore','entrypoint.sh','run.sh','healthcheck.sh','README.md')) {
        Copy-Item -LiteralPath (Join-Path $PSScriptRoot ('../deploy/sensor/' + $name)) -Destination (Join-Path $bundle $name) -Force
    }
    foreach ($name in @('admin','reader')) {
        $secretPath = Join-Path $bundle ($name + '.password')
        if (-not (Test-Path -LiteralPath $secretPath)) { [IO.File]::WriteAllText($secretPath,(New-Secret),$utf8) }
        if ([IO.File]::ReadAllText($secretPath) -notmatch '^[a-f0-9]{64}$') { throw 'Sensor secret is invalid; existing secrets were not replaced.' }
    }
    [IO.File]::WriteAllText((Join-Path $bundle '.env'),("SENSOR_ADDRESS=$SensorAddress`nCAPTURE_INTERFACE=$CaptureInterface`n"),$utf8)
    Set-Value 'NERVE_NTOPNG_URL' ('http://' + $SensorAddress + ':3001')
    Set-Value 'NERVE_NTOPNG_USERNAME' 'nerve'
    Set-Value 'NERVE_NTOPNG_PASSWORD' ([IO.File]::ReadAllText((Join-Path $bundle 'reader.password'))) -Literal
    Set-Value 'NERVE_NTOPNG_INTERFACE_ID' 'auto'
    Write-Host '[OK] .nerve/sensor sicher zum Linux-Sensor kopieren; dort docker compose up -d --build ausfuehren.'
}
if ($TrafficMode -eq 'sensor-required' -and (Get-Value 'NERVE_NTOPNG_URL')) { throw 'Existing source retained. Clear it explicitly before selecting sensor-required.' }
if ($TrafficMode -ne 'preserve') { Set-Value 'NERVE_TRAFFIC_CAPTURE_MODE' $TrafficMode }
elseif (-not (Get-Value 'NERVE_TRAFFIC_CAPTURE_MODE') -or ((Get-Value 'NERVE_TRAFFIC_CAPTURE_MODE') -eq 'sensor-required' -and (Get-Value 'NERVE_NTOPNG_URL'))) {
    $mode = 'sensor-required'
    if (Get-Value 'NERVE_NTOPNG_URL') { $mode = 'external' }
    Set-Value 'NERVE_TRAFFIC_CAPTURE_MODE' $mode
}
# Atomic replacement prevents a truncated .env on interruption.
$temporary = $envPath + '.setup.tmp'
try {
    [IO.File]::WriteAllText($temporary, (($lines -join "`r`n") + "`r`n"), $utf8)
    if (Test-Path -LiteralPath $envPath) { [IO.File]::Replace($temporary,$envPath,[NullString]::Value) }
    else { [IO.File]::Move($temporary,$envPath) }
} finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary } }
foreach ($name in @('start','stop')) {
    $destination = Join-Path $Root ($name + '.bat')
    $replace = $RefreshLaunchers -or -not (Test-Path -LiteralPath $destination)
    if (-not $replace -and -not $NonInteractive) { $replace = (Read-Host ($name + '.bat aus aktueller Vorlage aktualisieren? [j/N]')) -match '^(j|ja|y|yes)$' }
    if ($replace) { Copy-Item -LiteralPath (Join-Path $Root ($name + '.example.bat')) -Destination $destination -Force }
}
Write-Host '[OK] Konfiguration gespeichert. Bestehende Secrets und Daten bleiben erhalten. Start: start.bat'
