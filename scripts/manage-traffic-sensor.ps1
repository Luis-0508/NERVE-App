[CmdletBinding()]
param([ValidateSet('Start','Stop','Status','Install')][string]$Action = 'Start', [string]$Root, [string]$OwnerSid)
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($Root)) { $Root = Split-Path -Path $PSScriptRoot -Parent }
$Root = [IO.Path]::GetFullPath($Root).TrimEnd('\')
$sha = [Security.Cryptography.SHA256]::Create()
try { $id = ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($Root.ToLowerInvariant())))).Replace('-','').Substring(0,12) } finally { $sha.Dispose() }
$serviceName = 'NerveSensor-' + $id
$installRoot = Join-Path $env:ProgramFiles ('NERVE\' + $serviceName)
$staging = Join-Path $Root '.nerve\sensor-build'
$service = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
if ($Action -eq 'Stop') {
    if ($null -ne $service -and $service.Status -ne 'Stopped') {
        Stop-Service -Name $serviceName -ErrorAction Stop
        $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(15))
    }
    Write-Host '[OK] Windows Traffic Sensor gestoppt.'
    exit 0
}
$lines = [Collections.Generic.List[string]]::new()
$lines.AddRange([IO.File]::ReadAllLines((Join-Path $Root '.env')))
. (Join-Path $PSScriptRoot 'setup-common.ps1')
if ((Get-Value 'NERVE_TRAFFIC_CAPTURE_MODE') -ne 'windows-native') {
    Write-Host '[INFO] Externe/optionale Traffic-Quelle bleibt unveraendert.'
    exit 0
}
$port = 0
if (-not [int]::TryParse((Get-Value 'NERVE_SENSOR_PORT'),[ref]$port) -or $port -lt 1 -or $port -gt 65535) { throw 'Fuehre setup.bat aus: Sensor-Port fehlt.' }
$token = Get-Value 'NERVE_SENSOR_TOKEN'
if ($token -notmatch '^[a-fA-F0-9]{64}$') { throw 'Fuehre setup.bat aus: Sensor-Token fehlt.' }
$config = [ordered]@{serviceName=$serviceName;apiUrl=('http://127.0.0.1:' + $port + '/v1/snapshot');token=$token} | ConvertTo-Json -Compress
$sha = [Security.Cryptography.SHA256]::Create()
try { $configHash = [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($config))) } finally { $sha.Dispose() }
if ($Action -eq 'Status') {
    try { $health = Invoke-RestMethod -TimeoutSec 5 ('http://127.0.0.1:' + $port + '/health'); $health | ConvertTo-Json -Depth 4; exit 0 }
    catch { Write-Host '[INFO] Traffic capture unavailable. Pruefe Windows-Dienst und Map - Diagnostics.'; exit 1 }
}
if ($Action -eq 'Install') {
    $admin = [Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $admin.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Die Dienstinstallation benoetigt einmalig Administratorrechte.' }
    if ($OwnerSid -notmatch '^S-1-5-21-\d+-\d+-\d+-\d+$') { throw 'Ungueltiger Installationsbenutzer.' }
    if (-not (Test-Path -LiteralPath (Join-Path $staging 'nerve-sensor.exe'))) { throw 'Sensor-Build fehlt.' }
    # Never write elevated binaries into a user-writable directory or through a junction.
    foreach ($path in @((Join-Path $env:ProgramFiles 'NERVE'),$installRoot)) {
        if (Test-Path -LiteralPath $path) {
            if ((Get-Item -LiteralPath $path -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Unerwarteter Link im Installationspfad.' }
        } else { [void](New-Item -ItemType Directory -Path $path) }
        & icacls.exe $path /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-19:(OI)(CI)RX' '*S-1-5-32-545:(OI)(CI)RX' | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Installations-ACL konnte nicht gesetzt werden.' }
    }
    if ($null -ne $service -and $service.Status -ne 'Stopped') { Stop-Service $serviceName; $service.WaitForStatus('Stopped',[TimeSpan]::FromSeconds(15)) }
    # Remove unused debug helpers from any development prototype; not part of capture.
    foreach ($relative in @('amd64\KernelTraceControl.dll','amd64\msdia140.dll','Microsoft.DiaSymReader.Native.amd64.dll','Dia2Lib.dll')) {
        $unused = Join-Path $installRoot $relative
        if (Test-Path -LiteralPath $unused) { Remove-Item -LiteralPath $unused -Force }
    }
    foreach ($file in Get-ChildItem -LiteralPath $staging -Recurse) {
        if ($file.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Unerwarteter Link im Sensor-Build.' }
        $target = Join-Path $installRoot $file.FullName.Substring($staging.Length + 1)
        if (Test-Path -LiteralPath $target) { if ((Get-Item -LiteralPath $target -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Unerwarteter Link im Installationsziel.' } }
        if ($file.PSIsContainer) { [void](New-Item -ItemType Directory -Path $target -Force) }
        else { Copy-Item -LiteralPath $file.FullName -Destination $target -Force }
    }
    $configPath = Join-Path $installRoot 'sensor.json'
    if (-not (Test-Path -LiteralPath $configPath)) { [IO.File]::WriteAllText($configPath, '') }
    & icacls.exe $configPath /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F' '*S-1-5-19:R' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Sensor-Secret konnte nicht geschuetzt werden.' }
    [IO.File]::WriteAllText($configPath, $config, [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText((Join-Path $installRoot 'configuration.sha256'),$configHash)
    if ($null -eq $service) {
        $credential = [PSCredential]::new('NT AUTHORITY\LocalService', [Security.SecureString]::new())
        New-Service -Name $serviceName -DisplayName ('NERVE Windows Traffic Sensor ' + $id) -BinaryPathName ('"' + (Join-Path $installRoot 'nerve-sensor.exe') + '"') -StartupType Manual -Credential $credential | Out-Null
    }
    # Installing user can query/start/stop, but cannot replace/configure the service.
    & sc.exe sdset $serviceName ('D:(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWRPWPLOCRRC;;;' + $OwnerSid + ')') | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Dienstzugriff konnte nicht gesetzt werden.' }
    Start-Service $serviceName
    exit 0
}
$needsInstall = $null -eq $service
if (-not $needsInstall) {
    $manifest = Join-Path $installRoot 'configuration.sha256'
    $needsInstall = -not (Test-Path -LiteralPath $manifest) -or [IO.File]::ReadAllText($manifest) -ne $configHash
}
if (-not $needsInstall) {
    foreach ($file in Get-ChildItem -LiteralPath $staging -File -Recurse) {
        $target = Join-Path $installRoot $file.FullName.Substring($staging.Length + 1)
        if (-not (Test-Path -LiteralPath $target) -or (Get-FileHash -LiteralPath $file.FullName).Hash -ne (Get-FileHash -LiteralPath $target).Hash) { $needsInstall = $true; break }
    }
}
if ($needsInstall) {
    Write-Host '[NERVE] Einmalige Windows-Dienstinstallation/Aktualisierung: Bitte die UAC-Abfrage bestaetigen.'
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $arguments = '-NoProfile -ExecutionPolicy Bypass -File "' + $PSCommandPath + '" -Action Install -Root "' + $Root + '" -OwnerSid "' + $sid + '"'
    try { $process = Start-Process -FilePath powershell.exe -Verb RunAs -WindowStyle Hidden -ArgumentList $arguments -PassThru -Wait }
    catch { Write-Host '[INFO] Windows hat die Sensor-Dienstinstallation nicht bestaetigt oder konnte sie nicht starten. Spaeter start.bat erneut ausfuehren; Core und Discovery bleiben nutzbar.'; exit 1 }
    if ($process.ExitCode -ne 0) { throw 'Sensor-Dienst konnte nicht installiert werden. NERVE Core bleibt nutzbar.' }
} elseif ($service.Status -ne 'Running') { Start-Service $serviceName }
for ($attempt=0; $attempt -lt 10; $attempt++) {
    try { $health = Invoke-RestMethod -TimeoutSec 3 ('http://127.0.0.1:' + $port + '/health'); Write-Host ('[OK] Windows host capture: ' + $health.capture.interfaceName); exit 0 } catch { Start-Sleep -Seconds 1 }
}
Write-Host '[INFO] Traffic capture unavailable. Siehe Map - Diagnostics. NERVE Core und Discovery bleiben nutzbar.'
exit 1
