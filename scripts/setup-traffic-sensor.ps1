[CmdletBinding()]
param(
    [string]$Root,
    [switch]$NonInteractive,
    [ValidateSet('preserve','sensor-required','external','linux-interface')][string]$TrafficMode = 'preserve',
    [string]$SensorAddress, [string]$CaptureInterface, [string]$NtopngURL, [string]$Username,
    [Security.SecureString]$Password, [string]$InterfaceID
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
$scriptDirectory = $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($scriptDirectory)) { $scriptDirectory = Join-Path $Root 'scripts' }
. (Join-Path $scriptDirectory 'setup-common.ps1')
if ($fresh) { throw 'Run setup.bat first to create the local NERVE installation.' }
if (-not $NonInteractive -and $TrafficMode -eq 'preserve') {
    Write-Host 'Optionale Traffic-Erweiterung. Fuer die normale Windows-Installation nicht erforderlich.'
    $choice = Read-Host 'Traffic: [Enter] beibehalten; [e] externes ntopng; [l] Linux-Sensor vorbereiten'
    if ($choice -eq 'e') { $TrafficMode = 'external' }
    elseif ($choice -eq 'l') { $TrafficMode = 'linux-interface' }
    elseif ($choice) { throw 'Unknown traffic setup option.' }
}
if ($TrafficMode -eq 'preserve') { Write-Host '[INFO] Bestehende Traffic-Konfiguration bleibt unveraendert.'; return }
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
Save-SetupEnvironment
Write-Host '[OK] Traffic-Konfiguration gespeichert. Starte NERVE mit start.bat neu.'
