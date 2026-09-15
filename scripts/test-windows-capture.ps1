[CmdletBinding()]
param([string]$DockerDirectory)
$ErrorActionPreference = 'Stop'
if ($DockerDirectory) { $env:PATH = $DockerDirectory + ';' + $env:PATH }
$repo = Split-Path $PSScriptRoot -Parent
$fixture = Join-Path $repo '.nerve\windows capture e2e'
$project = 'nerve-windows-capture-e2e'
# Isolated, reusable installation: never uses the user's .env, database or service.
[void](New-Item -ItemType Directory -Path $fixture -Force)
foreach ($name in @('app','components','lib','public','scripts','deploy','cmd','internal','migrations','native')) {
    Copy-Item -LiteralPath (Join-Path $repo $name) -Destination $fixture -Recurse -Force
}
foreach ($name in @('setup.bat','.env.example','start.example.bat','stop.example.bat','docker-compose.yml','.dockerignore','Dockerfile.api','Dockerfile.web','Dockerfile.agent','Dockerfile.sensor','go.mod','go.sum','package.json','package-lock.json','next-env.d.ts','next.config.ts','postcss.config.mjs','tsconfig.json')) {
    Copy-Item -LiteralPath (Join-Path $repo $name) -Destination $fixture -Force
}
$savedProject = $env:COMPOSE_PROJECT_NAME
$env:COMPOSE_PROJECT_NAME = $project
function Batch([string]$Name, [int]$TimeoutSeconds) {
    $logPath = Join-Path $fixture ($Name+'.'+[Guid]::NewGuid().ToString('N')+'.run.log')
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $env:ComSpec; $info.Arguments = '/d /c ""' + (Join-Path $fixture $Name) + '" > "' + $logPath + '" 2>&1"'
    $info.WorkingDirectory = $env:TEMP; $info.UseShellExecute = $false; $info.CreateNoWindow = $true
    # Suppress only the final cosmetic console pause, not any startup/capture work.
    $info.EnvironmentVariables['NERVE_NO_PAUSE'] = '1'
    $info.RedirectStandardInput = $true
    $p = [Diagnostics.Process]::new(); $p.StartInfo = $info
    try {
        [void]$p.Start()
        if ($Name -ne 'setup.bat') { $p.StandardInput.WriteLine('x'); $p.StandardInput.Close() }
        while (-not $p.WaitForExit(1000)) { if (([DateTime]::Now-$p.StartTime).TotalSeconds -gt $TimeoutSeconds) { throw ($Name+' timeout') } }
        # Explorer/default-browser descendants can retain inherited pipe handles.
        # A file log avoids waiting forever for unrelated browser EOF after CMD exits.
        $stream = [IO.File]::Open($logPath,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::ReadWrite)
        $reader = [IO.StreamReader]::new($stream)
        try { $output = $reader.ReadToEnd() } finally { $reader.Dispose() }
        # Never print build/start output wholesale: verify it has no generated secret.
        $configuration = [IO.File]::ReadAllText((Join-Path $fixture '.env'))
        foreach ($key in @('POSTGRES_PASSWORD','NERVE_DISCOVERY_AGENT_TOKEN','NERVE_SENSOR_TOKEN')) {
            $secret = [regex]::Match($configuration,('(?m)^'+$key+'=([^\r\n]+)')).Groups[1].Value
            if ($secret -and $output.Contains($secret)) { throw 'Credential leak in batch output.' }
        }
        [IO.File]::WriteAllText((Join-Path $fixture ($Name+'.test.log')),$output)
        if ($p.ExitCode -ne 0) { throw ($Name+' failed. See ignored test log.') }
    } finally { if (-not $p.HasExited) { & taskkill.exe /PID $p.Id /T /F | Out-Null }; $p.Dispose() }
}
try {
    Batch 'setup.bat' 30
    $before = [IO.File]::ReadAllText((Join-Path $fixture '.env'))
    Batch 'setup.bat' 30
    if ([IO.File]::ReadAllText((Join-Path $fixture '.env')) -cne $before) { throw 'Second setup changed secrets/configuration.' }
    # Refresh only this test installation's known launchers when rerunning source changes.
    Copy-Item -LiteralPath (Join-Path $fixture 'start.example.bat') -Destination (Join-Path $fixture 'start.bat') -Force
    Copy-Item -LiteralPath (Join-Path $fixture 'stop.example.bat') -Destination (Join-Path $fixture 'stop.bat') -Force
    Write-Host 'TEST: native start.bat; first sensor install requests Windows UAC.'
    Batch 'start.bat' 900
    $port = [regex]::Match($before,'(?m)^NERVE_WEB_PORT=(\d+)').Groups[1].Value
    $sensorPort = [regex]::Match($before,'(?m)^NERVE_SENSOR_PORT=(\d+)').Groups[1].Value
    $base = 'http://localhost:' + $port
    $health = Invoke-RestMethod ('http://127.0.0.1:'+$sensorPort+'/health')
    if ($health.status -ne 'connected') { throw 'Real ETW backend is not connected.' }
    Write-Host ('TEST: ETW connected on '+$health.capture.interfaceName)
    $accountPath = Join-Path $fixture 'test-account.json'
    if (-not (Test-Path -LiteralPath $accountPath)) {
        $bytes = New-Object byte[] 32; $rng = [Security.Cryptography.RandomNumberGenerator]::Create();try{$rng.GetBytes($bytes)}finally{$rng.Dispose()}
        @{email='capture-test@example.invalid';password=([Convert]::ToBase64String($bytes)+'aA1!')} | ConvertTo-Json | Set-Content -LiteralPath $accountPath
        $account = [IO.File]::ReadAllText($accountPath)
        Invoke-RestMethod -Method Post -ContentType application/json -Body $account -Uri ($base+'/api/v1/auth/bootstrap') -SessionVariable session | Out-Null
    } else {
        $account = [IO.File]::ReadAllText($accountPath)
        Invoke-RestMethod -Method Post -ContentType application/json -Body $account -Uri ($base+'/api/v1/auth/login') -SessionVariable session | Out-Null
    }
    # Actual HTTPS download on the Windows host. No ntopng, mock source, packets or fixture flows.
    $downloadEvidence = & curl.exe -4 --fail --silent --show-error --limit-rate 512k --write-out '%{local_ip}|%{remote_ip}|%{size_download}' 'https://download.db-ip.com/free/dbip-country-lite-2026-09.csv.gz' --output (Join-Path $fixture 'real-download.csv.gz')
    if ($LASTEXITCODE -ne 0) { throw 'Real HTTPS download failed.' }
    Start-Sleep -Seconds 3
    $first = Invoke-RestMethod -WebSession $session ($base+'/api/v1/traffic/live')
    [IO.File]::WriteAllText((Join-Path $fixture 'real-snapshot.json'),($first | ConvertTo-Json -Depth 8))
    [IO.File]::WriteAllText((Join-Path $fixture 'real-transfer.txt'),[string]$downloadEvidence)
    if ($first.source -ne 'windows-native' -or $first.summary.activeFlows -lt 1 -or -not ($first.flows | Where-Object { $_.protocol -eq 'TCP' -and $_.bytes -gt 0 -and $_.remotePorts -contains 443 })) { throw 'No real HTTPS flow reached the authenticated Traffic API.' }
    $actualLocal,$actualRemote,$downloaded = $downloadEvidence -split '\|'
    $inbound = @($first.flows | Where-Object { $_.localIp -eq $actualLocal -and $_.remoteIp -eq $actualRemote -and $_.remotePorts -contains 443 -and $_.direction -eq 'inbound' })
    $outbound = @($first.flows | Where-Object { $_.localIp -eq $actualLocal -and $_.remoteIp -eq $actualRemote -and $_.remotePorts -contains 443 -and $_.direction -eq 'outbound' })
    if ($inbound.Count -ne 1 -or $outbound.Count -ne 1 -or $inbound[0].bytes -lt [long]$downloaded -or $outbound[0].bytes -le 0) { throw 'Captured endpoints/directions/bytes do not match the actual HTTPS transfer.' }
    [IO.File]::WriteAllText((Join-Path $fixture 'real-snapshot.json'),($first | ConvertTo-Json -Depth 8))
    Write-Host ('PASS: real Windows flows='+$first.summary.activeFlows+' mapped='+($first.flows | Where-Object {$null -ne $_.remoteLatitude}).Count)
    $second = $null
    & curl.exe --fail --silent --show-error 'https://www.microsoft.com/' --output (Join-Path $fixture 'real-site.html')
    Start-Sleep -Seconds 3
    $second = Invoke-RestMethod -WebSession $session ($base+'/api/v1/traffic/live')
    if (($first.flows | ConvertTo-Json -Depth 5 -Compress) -ceq ($second.flows | ConvertTo-Json -Depth 5 -Compress)) { throw 'Real flow values did not change.' }
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $fixture 'scripts/manage-traffic-sensor.ps1') -Action Stop -Root $fixture
    if ($LASTEXITCODE -ne 0) { throw 'Sensor stop failed.' }
    Start-Sleep -Seconds 12
    try { Invoke-RestMethod -WebSession $session ($base+'/api/v1/traffic/live') | Out-Null; throw 'Stopped sensor still reported connected.' }
    catch { if ($_.Exception.Response.StatusCode.value__ -ne 503) { throw } }
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $fixture 'scripts/manage-traffic-sensor.ps1') -Action Start -Root $fixture
    if ($LASTEXITCODE -ne 0) { throw 'Sensor recovery failed.' }
    $recovered = Invoke-RestMethod -WebSession $session ($base+'/api/v1/traffic/live')
    if ($recovered.source -ne 'windows-native') { throw 'Traffic source did not recover.' }
    $sha = [Security.Cryptography.SHA256]::Create()
    try { $id = [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($fixture.ToLowerInvariant()))).Replace('-','').Substring(0,12) } finally {$sha.Dispose()}
    $service = Get-CimInstance Win32_Service -Filter ("Name='NerveSensor-"+$id+"'")
    $sensorPidBefore = $service.ProcessId
    Batch 'start.bat' 900
    $service = Get-CimInstance Win32_Service -Filter ("Name='NerveSensor-"+$id+"'")
    if ($service.State -ne 'Running') { throw 'Second start failed.' }
    if ($sensorPidBefore -ne $service.ProcessId -or (Get-Process -Id $service.ProcessId).ProcessName -ne 'nerve-sensor') { throw 'Second start replaced/duplicated the sensor process.' }
    if ((Invoke-RestMethod ($base+'/health/discovery')).status -ne 'ok') { throw 'Discovery regression.' }
    Invoke-RestMethod -WebSession $session ($base+'/api/v1/auth/session') | Out-Null
    if (Test-Path -LiteralPath (Join-Path $repo '.nerve/browser-tools/node_modules/playwright/index.mjs')) {
        & node (Join-Path $repo 'scripts/test-windows-map.mjs') $fixture
        if ($LASTEXITCODE -ne 0) { throw 'Real browser map test failed.' }
    } else { Write-Host 'SKIP: Browser check requires the documented ignored Playwright test tool.' }
    Batch 'stop.bat' 60
    if ((Get-Service $service.Name).Status -ne 'Stopped' -or (Get-Process -Id $service.ProcessId -ErrorAction SilentlyContinue)) { throw 'Orphan sensor after stop.bat.' }
    Write-Host 'PASS: native setup/start, real host TCP capture/API/GeoIP, changing values, offline TTL, recovery, second start, discovery, retained account/database and clean stop.'
} finally {
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $fixture 'scripts/manage-traffic-sensor.ps1') -Action Stop -Root $fixture
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $fixture 'scripts/manage-discovery-agent.ps1') -Action Stop -Root $fixture
    & docker compose -p $project --project-directory $fixture down
    $env:COMPOSE_PROJECT_NAME = $savedProject
}
