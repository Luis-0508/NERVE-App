@echo off
setlocal EnableExtensions DisableDelayedExpansion

cd /d "%~dp0"
if errorlevel 1 exit /b 1

echo [NERVE] Stoppe den lokalen Discovery-Agenten...
powershell -NoProfile -ExecutionPolicy Bypass -Command "$ErrorActionPreference = 'Stop'; $root = (Get-Location).Path; $pidPath = Join-Path $root '.nerve\agent.pid'; $expectedPath = [IO.Path]::GetFullPath((Join-Path $root '.nerve\agent\nerve-agent.exe')); if (-not (Test-Path -LiteralPath $pidPath)) { Write-Host '[INFO] Kein laufender Discovery-Agent registriert.'; exit 0 }; $agentPid = 0; $validPid = [int]::TryParse(([IO.File]::ReadAllText($pidPath).Trim()), [ref]$agentPid) -and $agentPid -gt 0; if (-not $validPid) { Remove-Item -LiteralPath $pidPath -Force; Write-Host '[WARNUNG] Ungueltige Agent-PID-Datei wurde entfernt.' -ForegroundColor Yellow; exit 0 }; $process = Get-Process -Id $agentPid -ErrorAction SilentlyContinue; if ($null -eq $process) { Remove-Item -LiteralPath $pidPath -Force; Write-Host '[INFO] Veraltete Agent-PID-Datei wurde entfernt.'; exit 0 }; $actualPath = $null; try { $actualPath = $process.Path } catch {}; if ([string]::IsNullOrWhiteSpace($actualPath) -or [IO.Path]::GetFullPath($actualPath) -ne $expectedPath) { Remove-Item -LiteralPath $pidPath -Force; Write-Host '[WARNUNG] Die gespeicherte PID gehoert nicht zum NERVE-Agenten. Der fremde Prozess wurde nicht beendet.' -ForegroundColor Yellow; exit 0 }; Stop-Process -Id $agentPid -Force; [void]$process.WaitForExit(5000); Remove-Item -LiteralPath $pidPath -Force; Write-Host '[OK] Discovery-Agent wurde beendet.' -ForegroundColor Green"
if errorlevel 1 echo [WARNUNG] Der Discovery-Agent konnte nicht vollstaendig gestoppt werden.

echo [NERVE] Stoppe Web, API und PostgreSQL...
docker compose down
if errorlevel 1 (
  echo [FEHLER] Der Compose-Stack konnte nicht vollstaendig gestoppt werden.
  pause
  exit /b 1
)

echo [OK] NERVE wurde gestoppt. Die PostgreSQL-Daten bleiben erhalten.
pause
