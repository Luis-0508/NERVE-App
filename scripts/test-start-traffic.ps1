# Focused CMD control-flow test; intentionally not a Docker end-to-end test.
$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$fixture = Join-Path $repo ('.nerve/start traffic test ' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $fixture | Out-Null
# Model the real traffic-check contract: missing configuration exits with code 1.
Add-Type -TypeDefinition @'
using System;
public class MissingSensorCLI {
    public static int Main(string[] args) {
        Console.WriteLine("{\"status\":\"not_configured\"}");
        return 1;
    }
}
'@ -OutputAssembly (Join-Path $fixture 'docker.exe') -OutputType ConsoleApplication
$source = [IO.File]::ReadAllText((Join-Path $repo 'start.example.bat')).Replace("`r`n","`n")
$begin = $source.IndexOf("`n:runtime_ready`n")
$end = $source.IndexOf("`n:startup_failed`n")
if ($begin -lt 0 -or $end -le $begin) { throw 'Start completion section not found.' }
$section = $source.Substring($begin,$end-$begin)
# Preserve the actual batch control flow; replace the browser side effect only.
$section = $section.Replace('start "" "%NERVE_URL%"','echo BROWSER_REQUESTED')
$script = "@echo off`nset NERVE_URL=http://localhost:3000`nset NERVE_WEB_PORT=3000`n" + $section + "`n:hold_window`nexit /b 0`n"
$batch = Join-Path $fixture 'completion.bat'
[IO.File]::WriteAllText($batch,$script.Replace("`n","`r`n"))
$info = New-Object Diagnostics.ProcessStartInfo
$info.FileName = $env:ComSpec
$info.Arguments = '/d /c ""' + $batch + '""'
$info.WorkingDirectory = $fixture
$info.UseShellExecute = $false
$info.CreateNoWindow = $true
$info.RedirectStandardOutput = $true
$info.RedirectStandardError = $true
$process = New-Object Diagnostics.Process
$process.StartInfo = $info
try {
    [void]$process.Start()
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit(30000)) { $process.Kill(); throw 'Start completion test timed out.' }
    $output = $stdout.GetAwaiter().GetResult() + $stderr.GetAwaiter().GetResult()
    if ($process.ExitCode -ne 0 -or $output -notmatch 'not_configured' -or $output -notmatch '\[INFO\] Traffic-Sensor noch nicht bereit' -or $output -notmatch 'BROWSER_REQUESTED') { throw 'Missing sensor prevented successful start completion.' }
    Write-Host 'PASS: actual start.bat completion section continues to browser request and exit 0 after missing-sensor exit 1 (CLI/browser test doubles; no Docker E2E claim).'
} finally { $process.Dispose() }
