$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$fixture = Join-Path $repo ('.nerve/batch setup test ' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path (Join-Path $fixture 'scripts') | Out-Null
foreach ($file in @('setup.bat','.env.example','start.example.bat','stop.example.bat')) {
    Copy-Item -LiteralPath (Join-Path $repo $file) -Destination $fixture
}
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'setup.ps1') -Destination (Join-Path $fixture 'scripts/setup.ps1')
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'setup-common.ps1') -Destination (Join-Path $fixture 'scripts/setup-common.ps1')

function Invoke-BatchSetup([switch]$DirectFile) {
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $env:ComSpec
    $info.Arguments = '/d /c ""' + (Join-Path $fixture 'setup.bat') + '""'
    if ($DirectFile) {
        $info.FileName = 'powershell.exe'
        $info.Arguments = '-NoProfile -ExecutionPolicy Bypass -File "' + (Join-Path $fixture 'scripts/setup.ps1') + '" -NonInteractive'
    }
    # Deliberately start elsewhere: setup.bat must resolve its own directory.
    $info.WorkingDirectory = $env:TEMP
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardInput = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $info
    try {
        [void]$process.Start()
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        # Keep stdin open but send NOTHING: an accidental Read-Host/pause must hang
        # and fail, rather than accepting EOF as a default answer.
        if (-not $process.WaitForExit(30000)) { $process.Kill(); throw 'Native batch setup timed out.' }
        $output = $stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0 -or $output -match 'Split-Path\s*:|\[FEHLER\]') {
            throw ('Native batch setup failed: ' + $output)
        }
        if ($output -match 'Web-Port|Traffic:|Linux-Sensor|ntopng|enp3s0') { throw 'Normal setup exposed optional configuration prompts.' }
    } finally { $process.Dispose() }
}

# Force an occupied preferred port without touching existing listeners.
$occupied = [Net.Sockets.TcpListener]::new([Net.IPAddress]::IPv6Any,3000)
$occupied.Server.DualMode = $true
$occupied.ExclusiveAddressUse = $true
$reserved = $false
try { $occupied.Start(); $reserved = $true } catch [Net.Sockets.SocketException] { }
try { Invoke-BatchSetup } finally { $occupied.Stop() }
$envPath = Join-Path $fixture '.env'
if (-not (Test-Path -LiteralPath $envPath)) { throw 'Batch setup did not create .env in its own directory.' }
foreach ($file in @('start.bat','stop.bat')) {
    if (-not (Test-Path -LiteralPath (Join-Path $fixture $file))) { throw 'Batch setup did not create its launcher.' }
}
$before = [IO.File]::ReadAllText($envPath)
if ($reserved -and $before -match '(?m)^NERVE_WEB_PORT=3000\r?$') { throw 'Setup selected occupied port 3000.' }
$port = [regex]::Match($before,'(?m)^NERVE_WEB_PORT=(\d+)').Groups[1].Value
if (-not $port -or -not $before.Contains('NERVE_ALLOWED_ORIGIN=http://localhost:'+$port) -or -not $before.Contains('NEXT_PUBLIC_SITE_URL=http://localhost:'+$port)) { throw 'Port and URLs are inconsistent.' }
if ($before -notmatch '(?m)^NERVE_TRAFFIC_CAPTURE_MODE=windows-native\r?$') { throw 'Fresh setup did not select windows-native.' }
if ($before -notmatch '(?m)^POSTGRES_PASSWORD=[a-f0-9]{64}\r?$' -or $before -notmatch '(?m)^NERVE_DISCOVERY_AGENT_TOKEN=[a-f0-9]{64}\r?$') { throw 'Fresh secrets missing.' }
$startBefore = [IO.File]::ReadAllText((Join-Path $fixture 'start.bat'))
$stopBefore = [IO.File]::ReadAllText((Join-Path $fixture 'stop.bat'))
# Existing: keep a configured source and custom values without asking anything.
$before = $before.Replace('NERVE_TRAFFIC_CAPTURE_MODE=windows-native','NERVE_TRAFFIC_CAPTURE_MODE=external')
$before = [regex]::Replace($before,'(?m)^NERVE_NTOPNG_URL=\r?$', 'NERVE_NTOPNG_URL=http://existing-sensor:3001' + "`r")
$before = [regex]::Replace($before,'(?m)^NERVE_NTOPNG_PASSWORD=\r?$', "NERVE_NTOPNG_PASSWORD='existing-test-only'`r")
$before += "# custom installation`r`nCUSTOM_VALUE='retain-this'`r`n"
[IO.File]::WriteAllText($envPath,$before)
$runningInstallation = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Any,[int]$port)
try {
    $runningInstallation.Start()
    Invoke-BatchSetup
} finally { $runningInstallation.Stop() }
if ([IO.File]::ReadAllText($envPath) -cne $before) { throw 'Second batch setup changed existing configuration/secrets.' }
if ([IO.File]::ReadAllText((Join-Path $fixture 'start.bat')) -cne $startBefore -or [IO.File]::ReadAllText((Join-Path $fixture 'stop.bat')) -cne $stopBefore) { throw 'Second batch setup changed retained launchers.' }
# Without the batch's cd, the file must still resolve its project via PSScriptRoot.
Invoke-BatchSetup -DirectFile
if ([IO.File]::ReadAllText($envPath) -cne $before) { throw 'Direct -File invocation changed existing configuration.' }
if ($reserved) {
    # After releasing our own listener, a second fresh setup should prefer 3000.
    $previous = $fixture
    $fixture += ' preferred port'
    New-Item -ItemType Directory -Path (Join-Path $fixture 'scripts') | Out-Null
    foreach ($file in @('setup.bat','.env.example','start.example.bat','stop.example.bat')) { Copy-Item -LiteralPath (Join-Path $previous $file) -Destination $fixture }
    foreach ($file in @('setup.ps1','setup-common.ps1')) { Copy-Item -LiteralPath (Join-Path $previous ('scripts/' + $file)) -Destination (Join-Path $fixture 'scripts') }
    Invoke-BatchSetup
    if ([IO.File]::ReadAllText((Join-Path $fixture '.env')) -notmatch '(?m)^NERVE_WEB_PORT=3000\r?$') { throw 'Fresh setup did not prefer available port 3000.' }
}
Write-Host 'PASS: native setup.bat without any input, occupied-port fallback, consistent URLs, existing traffic/custom values, second run, path with spaces, unrelated working directory, unchanged secrets and launchers.'
