param([string]$Docker = 'docker', [string]$Image = 'nerve-sensor:local-test', [string]$APIImage = 'nerveapp-api:latest')
$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$fixture = Get-ChildItem -LiteralPath (Join-Path $repo '.nerve') -Directory -Filter 'setup-tests-*' |
    Where-Object { Test-Path -LiteralPath (Join-Path $_.FullName '.nerve/sensor/reader.password') } |
    Sort-Object LastWriteTime -Descending | Select-Object -First 1
if (-not $fixture) { throw 'Run scripts/test-setup.ps1 first.' }
$bundle = Join-Path $fixture.FullName '.nerve/sensor'
$name = 'nerve-sensor-test-' + [Guid]::NewGuid().ToString('N').Substring(0,12)
function DockerRun([string[]]$Arguments) {
    $output = & $Docker @Arguments
    if ($LASTEXITCODE -ne 0) { throw 'Sensor test Docker command failed.' }
    return $output
}
try {
    # Explicit lab harness: bypass only the production platform entrypoint to test
    # bootstrap/REST on a bridge namespace. No host NIC, host ports or production volumes.
    DockerRun @('run','-d','--name',$name,'--entrypoint','/opt/nerve/run.sh',
        '--tmpfs','/run/nerve:mode=755','--tmpfs','/var/lib/ntopng',
        '-e','CAPTURE_INTERFACE=eth0','-e','SENSOR_ADDRESS=127.0.0.1',
        '--mount',('type=bind,readonly,source=' + (Join-Path $bundle 'admin.password') + ',target=/run/secrets/admin_password'),
        '--mount',('type=bind,readonly,source=' + (Join-Path $bundle 'reader.password') + ',target=/run/secrets/reader_password'),$Image) | Out-Null
    $healthy = $false
    for ($i=0; $i -lt 30; $i++) {
        & $Docker exec $name /bin/sh -c '/opt/nerve/healthcheck.sh >/dev/null 2>&1' | Out-Null
        if ($LASTEXITCODE -eq 0) { $healthy = $true; break }
        $running = DockerRun @('inspect','--format','{{.State.Running}}',$name)
        if ($running -ne 'true') { throw 'Sensor exited before its healthcheck passed.' }
        Start-Sleep -Seconds 1
    }
    if (-not $healthy) { throw 'Authenticated ntopng healthcheck failed.' }
    $status = DockerRun @('exec',$name,'curl','--silent','--output','/dev/null','--write-out','%{http_code}',
        '--user','admin:admin','http://127.0.0.1:3001/lua/rest/v2/get/ntopng/interfaces.lua')
    if ($status -notin @('302','401','403')) { throw 'Default admin credentials were not rejected.' }
    # Generate a real HTTPS connection in THIS namespace only. No fabricated flows.
    DockerRun @('exec',$name,'curl','--silent','--fail','--max-time','15','--output','/dev/null','https://example.com') | Out-Null
    $reader = [IO.File]::ReadAllText((Join-Path $bundle 'reader.password'))
    $apiEnv = Join-Path $fixture.FullName 'sensor-api-test.env'
    [IO.File]::WriteAllText($apiEnv, ("NERVE_NTOPNG_URL=http://127.0.0.1:3001`nNERVE_NTOPNG_USERNAME=nerve`nNERVE_NTOPNG_PASSWORD=$reader`nNERVE_NTOPNG_INTERFACE_ID=auto`nNERVE_TRAFFIC_CAPTURE_MODE=external`n"))
    $result = (DockerRun @('run','--rm','--network',('container:'+$name),'--env-file',$apiEnv,$APIImage,'traffic-check')) -join "`n"
    $diagnostic = $result | ConvertFrom-Json
    if ($diagnostic.status -ne 'connected' -or $diagnostic.externalFlows -lt 1) { throw 'Real container HTTPS traffic did not reach the NERVE adapter.' }
    if ($result.Contains($reader)) { throw 'Diagnostic leaked credentials.' }
    $badAuth = (& $Docker run --rm --network ('container:'+$name) --env-file $apiEnv -e NERVE_NTOPNG_PASSWORD=invalid-test-password $APIImage traffic-check) -join "`n"
    if ($LASTEXITCODE -ne 1 -or ($badAuth | ConvertFrom-Json).status -ne 'authentication_failed') { throw 'Real authentication failure was not classified correctly.' }
    $logs = (DockerRun @('logs',$name)) -join "`n"
    if ($logs.Contains($reader) -or $logs.Contains([IO.File]::ReadAllText((Join-Path $bundle 'admin.password')))) { throw 'Sensor log leaked credentials.' }
    Write-Host 'PASS: real ntopng bootstrap, read-only REST auth, default-password rejection, authentication failure, healthcheck, real container HTTPS flow to NERVE, no credential leaks.'
    Write-Host 'Scope: isolated Docker namespace only; this is NOT Windows-host or full-LAN capture.'
} catch {
    $testError = $_
    $logs = (& $Docker logs $name 2>&1) -join "`n"
    foreach ($account in @('reader','admin')) {
        $secret = [IO.File]::ReadAllText((Join-Path $bundle ($account + '.password')))
        $logs = $logs.Replace($secret,'[REDACTED]')
    }
    Write-Host $logs
    throw $testError
} finally {
    # Only the unique container created by this test, with tmpfs state, is removed.
    & $Docker rm -f $name 2>$null | Out-Null
}
