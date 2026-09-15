param([string]$DockerDirectory)
$ErrorActionPreference = 'Stop'
if ($DockerDirectory) { $env:PATH = $DockerDirectory + ';' + $env:PATH }
$repo = Split-Path $PSScriptRoot -Parent
$project = 'nerve-start-test-' + [Guid]::NewGuid().ToString('N').Substring(0,12)
$fixture = Join-Path $repo ('.nerve/' + $project + ' with spaces')
New-Item -ItemType Directory -Path $fixture | Out-Null
# Only source/build inputs; never copy an existing .env, state or local launcher.
foreach ($name in @('app','components','lib','public','scripts','deploy','cmd','internal','migrations')) {
    Copy-Item -LiteralPath (Join-Path $repo $name) -Destination $fixture -Recurse
}
foreach ($name in @('setup.bat','.env.example','start.example.bat','stop.example.bat','docker-compose.yml','.dockerignore','Dockerfile.api','Dockerfile.web','Dockerfile.agent','go.mod','go.sum','package.json','package-lock.json','next-env.d.ts','next.config.ts','postcss.config.mjs','tsconfig.json')) {
    Copy-Item -LiteralPath (Join-Path $repo $name) -Destination $fixture
}
$savedProject = $env:COMPOSE_PROJECT_NAME
$env:COMPOSE_PROJECT_NAME = $project
$process = $null
try {
    & (Join-Path $fixture 'scripts/setup.ps1')
    $configuration = [IO.File]::ReadAllText((Join-Path $fixture '.env'))
    $port = [regex]::Match($configuration,'(?m)^NERVE_WEB_PORT=(\d+)').Groups[1].Value
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $env:ComSpec
    $info.Arguments = '/d /c ""' + (Join-Path $fixture 'start.bat') + '""'
    $info.WorkingDirectory = $env:TEMP
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardInput = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $info
    [void]$process.Start()
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    # start.bat retains its final window-closing pause; setup.bat has none.
    $process.StandardInput.WriteLine('x')
    $process.StandardInput.Close()
    while (-not $process.WaitForExit(1000)) {
        # Keep long Docker builds visible to the caller; no fake Docker/agent/browser.
        if (([DateTime]::Now - $process.StartTime).TotalSeconds -gt 600) { throw 'Native start.bat timed out.' }
    }
    $output = $stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()
    foreach ($key in @('POSTGRES_PASSWORD','NERVE_DISCOVERY_AGENT_TOKEN')) {
        $secret = [regex]::Match($configuration,('(?m)^'+$key+'=([^\r\n]+)')).Groups[1].Value
        if ($secret) { $output = $output.Replace($secret,'[REDACTED]') }
    }
    if ($process.ExitCode -ne 0 -or $output -notmatch '\[BEREIT\]' -or $output -notmatch '\[INFO\] Traffic-Sensor noch nicht bereit') {
        Write-Host $output
        throw 'Native start.bat failed without a sensor.'
    }
    $discovery = Invoke-RestMethod ('http://localhost:' + $port + '/health/discovery')
    if ($discovery.status -ne 'ok') { throw 'Real Windows discovery agent did not connect.' }
    Write-Host 'PASS: unmodified start.bat, real Docker builds/start, native Windows agent install/start/heartbeat, browser launch command, missing sensor INFO and exit 0.'
} finally {
    if ($process -and -not $process.HasExited) { & taskkill.exe /PID $process.Id /T /F | Out-Null }
    if ($process) { $process.Dispose() }
    & (Join-Path $fixture 'scripts/manage-discovery-agent.ps1') -Action Stop -Root $fixture
    # Unique test project and test database only. Existing NERVE resources are untouched.
    & docker compose -p $project --project-directory $fixture down -v
    $env:COMPOSE_PROJECT_NAME = $savedProject
}
