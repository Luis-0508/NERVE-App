$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$fixture = Join-Path $repo ('.nerve/batch setup test ' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path (Join-Path $fixture 'scripts') | Out-Null
foreach ($file in @('setup.bat','.env.example','start.example.bat','stop.example.bat')) {
    Copy-Item -LiteralPath (Join-Path $repo $file) -Destination $fixture
}
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'setup.ps1') -Destination (Join-Path $fixture 'scripts/setup.ps1')

function Invoke-BatchSetup([string[]]$Answers, [switch]$DirectFile) {
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
        foreach ($answer in $Answers) { $process.StandardInput.WriteLine($answer) }
        $process.StandardInput.Close()
        if (-not $process.WaitForExit(30000)) { $process.Kill(); throw 'Native batch setup timed out.' }
        $output = $stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0 -or $output -match 'Split-Path\s*:|\[FEHLER\]') {
            throw ('Native batch setup failed: ' + $output)
        }
    } finally { $process.Dispose() }
}

# Fresh: default web port, no sensor, final pause. Never start containers/agents.
Invoke-BatchSetup @('', '', 'x')
$envPath = Join-Path $fixture '.env'
if (-not (Test-Path -LiteralPath $envPath)) { throw 'Batch setup did not create .env in its own directory.' }
foreach ($file in @('start.bat','stop.bat')) {
    if (-not (Test-Path -LiteralPath (Join-Path $fixture $file))) { throw 'Batch setup did not create its launcher.' }
}
$before = [IO.File]::ReadAllText($envPath)
if ($before -notmatch '(?m)^POSTGRES_PASSWORD=[a-f0-9]{64}\r?$' -or $before -notmatch '(?m)^NERVE_DISCOVERY_AGENT_TOKEN=[a-f0-9]{64}\r?$') { throw 'Fresh secrets missing.' }
$startBefore = [IO.File]::ReadAllText((Join-Path $fixture 'start.bat'))
$stopBefore = [IO.File]::ReadAllText((Join-Path $fixture 'stop.bat'))
# Existing: retain source and both launchers, final pause.
Invoke-BatchSetup @('', 'n', 'n', 'x')
if ([IO.File]::ReadAllText($envPath) -cne $before) { throw 'Second batch setup changed existing configuration/secrets.' }
if ([IO.File]::ReadAllText((Join-Path $fixture 'start.bat')) -cne $startBefore -or [IO.File]::ReadAllText((Join-Path $fixture 'stop.bat')) -cne $stopBefore) { throw 'Second batch setup changed retained launchers.' }
# Without the batch's cd, the file must still resolve its project via PSScriptRoot.
Invoke-BatchSetup -Answers @() -DirectFile
if ([IO.File]::ReadAllText($envPath) -cne $before) { throw 'Direct -File invocation changed existing configuration.' }
Write-Host 'PASS: native cmd.exe -> setup.bat -> PowerShell -File, fresh and existing configuration, path with spaces, unrelated working directory, unchanged secrets and launchers.'
