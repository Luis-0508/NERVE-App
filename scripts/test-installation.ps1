param([string]$Docker = 'docker')
$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$project = 'nerve-install-test-' + [Guid]::NewGuid().ToString('N').Substring(0,12)
$fixture = Join-Path $repo ('.nerve/' + $project)
New-Item -ItemType Directory -Path $fixture | Out-Null
foreach ($name in @('.env.example','start.example.bat','stop.example.bat')) { Copy-Item -LiteralPath (Join-Path $repo $name) -Destination $fixture }
& "$PSScriptRoot/setup.ps1" -Root $fixture -NonInteractive
$path = Join-Path $fixture '.env'
$original = [IO.File]::ReadAllText($path)
$port = [regex]::Match($original,'(?m)^NERVE_WEB_PORT=(\d+)').Groups[1].Value
$override = Join-Path $fixture 'compose.test.yaml'
[IO.File]::WriteAllText($override,"services:`n  api:`n    image: nerveapp-api:latest`n  web:`n    image: nerveapp-web:latest`n")
$argsBase = @('compose','-p',$project,'--env-file',$path,'-f',(Join-Path $repo 'docker-compose.yml'),'-f',$override)
function Compose([string[]]$Arguments) {
    & $Docker @argsBase @Arguments | Out-Host
    if ($LASTEXITCODE -ne 0) { throw 'Isolated installation command failed.' }
}
try {
    Compose @('up','-d','--no-build','--wait','--wait-timeout','90')
    $base = 'http://localhost:' + $port
    $password = [Guid]::NewGuid().ToString('N') + 'Test!'
    $body = @{email='installation-test@example.invalid';password=$password} | ConvertTo-Json -Compress
    Invoke-WebRequest -UseBasicParsing -Uri ($base+'/api/v1/auth/bootstrap') -Method Post -ContentType application/json -Body $body -SessionVariable browser | Out-Null
    function CheckMissingSensor {
        try { Invoke-WebRequest -UseBasicParsing -Uri ($base+'/api/v1/traffic/live') -WebSession $browser | Out-Null; throw 'Expected missing sensor status.' }
        catch {
            if (-not $_.Exception.Response -or [int]$_.Exception.Response.StatusCode -ne 503) { throw }
            $response = $_.ErrorDetails.Message | ConvertFrom-Json
            if ($response.error.code -ne 'traffic_source_not_configured' -or $response.error.message -ne 'Capture sensor required' -or $response.capture.mode -ne 'sensor-required') { throw 'Fresh installation lacks honest capture diagnostics.' }
        }
    }
    CheckMissingSensor
    $before = [IO.File]::ReadAllText($path)
    & "$PSScriptRoot/setup.ps1" -Root $fixture -NonInteractive
    if ([IO.File]::ReadAllText($path) -cne $before) { throw 'Setup changed existing installation secrets.' }
    Compose @('up','-d','--no-build','--force-recreate','--wait','--wait-timeout','90','api','web')
    $session = Invoke-RestMethod -Uri ($base+'/api/v1/auth/session') -WebSession $browser
    if (-not $session.authenticated) { throw 'Account/session lost across recreation.' }
    CheckMissingSensor
    Write-Host 'PASS: fresh isolated Compose installation, authenticated missing-sensor UI response, repeated setup, service recreation and database/session persistence.'
} finally {
    # Unique test project only; never target the normal NERVE project or its volumes.
    Compose @('down','-v')
}
