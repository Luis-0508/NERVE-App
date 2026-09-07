@echo off
setlocal EnableExtensions DisableDelayedExpansion

cd /d "%~dp0"
if errorlevel 1 (
  echo [NERVE] Das Projektverzeichnis konnte nicht geoeffnet werden.
  call :hold_window
  exit /b 1
)

echo [NERVE] Pruefe die lokale Docker-Laufzeit...
where docker >nul 2>&1
if errorlevel 1 (
  echo [FEHLER] Docker wurde nicht gefunden. Installiere bzw. starte Docker Desktop und versuche es erneut.
  call :hold_window
  exit /b 1
)
echo [OK] Docker CLI wurde gefunden.

docker compose version >nul 2>&1
if errorlevel 1 (
  echo [FEHLER] Docker Compose v2 ist nicht verfuegbar. Aktualisiere Docker Desktop und versuche es erneut.
  call :hold_window
  exit /b 1
)
echo [OK] Docker Compose v2 ist verfuegbar.

docker info >nul 2>&1
if errorlevel 1 (
  echo [FEHLER] Docker Desktop bzw. die Docker Engine ist nicht erreichbar.
  echo          Starte Docker Desktop, warte bis die Engine bereit ist und fuehre start.bat erneut aus.
  call :hold_window
  exit /b 1
)
echo [OK] Docker Engine ist erreichbar.

if not exist ".env.example" (
  echo [FEHLER] .env.example fehlt im Projektroot.
  call :hold_window
  exit /b 1
)

if not exist ".env" (
  copy /Y ".env.example" ".env" >nul
  echo [NERVE] Eine lokale .env wurde aus .env.example erstellt.
  echo [AKTION] Oeffne .env, ersetze insbesondere POSTGRES_PASSWORD=change-me
  echo          durch ein starkes lokales Passwort und starte start.bat danach erneut.
  call :hold_window
  exit /b 2
)
echo [OK] Lokale .env wurde gefunden.

powershell -NoProfile -Command "$line = $null; foreach ($item in [IO.File]::ReadAllLines('.env')) { if ($item -match '^\s*POSTGRES_PASSWORD\s*=') { $line = $item } }; if ($null -eq $line) { exit 1 }; $value = ($line -split '=', 2)[1].Trim().Trim([char]34).Trim([char]39); if ([string]::IsNullOrWhiteSpace($value) -or $value -eq 'change-me') { exit 2 }"
if errorlevel 2 (
  echo [FEHLER] POSTGRES_PASSWORD in .env ist leer oder verwendet noch change-me.
  echo          Setze ein starkes lokales Passwort und starte danach erneut.
  call :hold_window
  exit /b 2
)
if errorlevel 1 (
  echo [FEHLER] POSTGRES_PASSWORD fehlt in .env.
  call :hold_window
  exit /b 2
)
echo [OK] Erforderliche lokale Konfiguration ist gesetzt.

powershell -NoProfile -Command "$line = $null; foreach ($item in [IO.File]::ReadAllLines('.env')) { if ($item -match '^\s*NERVE_DISCOVERY_AGENT_TOKEN\s*=') { $line = $item } }; if ($null -eq $line) { exit 1 }; $value = ($line -split '=', 2)[1].Trim().Trim([char]34).Trim([char]39); if ([string]::IsNullOrWhiteSpace($value) -or $value -eq 'change-me') { exit 2 }"
if errorlevel 2 (
  echo [FEHLER] NERVE_DISCOVERY_AGENT_TOKEN in .env ist leer oder verwendet noch change-me.
  echo          Fuehre setup.bat erneut aus, um das lokale Agent-Token sicher zu erzeugen.
  call :hold_window
  exit /b 2
)
if errorlevel 1 (
  echo [FEHLER] NERVE_DISCOVERY_AGENT_TOKEN fehlt in .env.
  echo          Fuehre setup.bat erneut aus.
  call :hold_window
  exit /b 2
)
echo [OK] Discovery-Agent-Konfiguration ist gesetzt.

set "NERVE_WEB_PORT=3000"
for /f "delims=" %%P in ('powershell -NoProfile -Command "$value = ''; foreach ($item in [IO.File]::ReadAllLines('.env')) { if ($item -match '^\s*NERVE_WEB_PORT\s*=') { $value = ($item -split '=', 2)[1].Trim().Trim([char]34).Trim([char]39) } }; $port = 0; if ([int]::TryParse($value, [ref]$port) -and $port -ge 1 -and $port -le 65535) { [Console]::Write($port) }"') do set "NERVE_WEB_PORT=%%P"
set "NERVE_URL=http://localhost:%NERVE_WEB_PORT%"

docker compose config --quiet
if errorlevel 1 (
  echo [FEHLER] docker-compose.yml oder .env ist ungueltig. Korrigiere die oben gemeldete Konfiguration.
  call :hold_window
  exit /b 1
)
echo [OK] Docker-Compose-Konfiguration ist gueltig.

echo [NERVE] Baue und starte Web, Reverse Proxy, API und PostgreSQL...
docker compose up -d --build
if errorlevel 1 goto :startup_failed

echo [NERVE] Warte auf Web, API und PostgreSQL...
set /a NERVE_ATTEMPT=0

:wait_for_runtime
set /a NERVE_ATTEMPT+=1
powershell -NoProfile -Command "$ErrorActionPreference = 'Stop'; $web = Invoke-WebRequest -UseBasicParsing -TimeoutSec 4 '%NERVE_URL%/'; $live = Invoke-RestMethod -TimeoutSec 4 '%NERVE_URL%/health/live'; $ready = Invoke-RestMethod -TimeoutSec 4 '%NERVE_URL%/health/ready'; $api = Invoke-RestMethod -TimeoutSec 4 '%NERVE_URL%/api/v1'; $status = Invoke-RestMethod -TimeoutSec 4 '%NERVE_URL%/api/v1/status'; $csp = [string]$web.Headers['Content-Security-Policy']; $inlineBootstrapBlocked = $web.Content -match '<script(?![^>]*src=)' -and $csp -notmatch 'script-src[^;]*''unsafe-inline'''; if ($web.StatusCode -ne 200 -or $web.Content -notmatch 'class=.app-shell.' -or $inlineBootstrapBlocked -or $live.status -ne 'ok' -or $ready.status -ne 'ok' -or $api.status -ne 'ok' -or $status.status -ne 'ok' -or $status.database.status -ne 'ok') { exit 1 }" >nul 2>&1
if not errorlevel 1 goto :core_ready
if %NERVE_ATTEMPT% GEQ 60 goto :runtime_failed
timeout /t 2 /nobreak >nul
goto :wait_for_runtime

:core_ready
echo [NERVE] Baue den lokalen Windows-Discovery-Agenten...
if not exist ".nerve\agent" mkdir ".nerve\agent"
if not exist ".nerve\logs" mkdir ".nerve\logs"
del /Q ".nerve\agent\nerve-agent.exe" >nul 2>&1
docker build --file Dockerfile.agent --target artifact --build-arg TARGETOS=windows --build-arg TARGETARCH=amd64 --output "type=local,dest=.nerve/agent" .
if errorlevel 1 (
  echo [FEHLER] Der lokale Discovery-Agent konnte nicht gebaut werden.
  goto :diagnostics
)
echo [OK] Discovery-Agent wurde gebaut.

echo [NERVE] Starte den Discovery-Agenten auf diesem Windows-Rechner...
powershell -NoProfile -ExecutionPolicy Bypass -Command "$ErrorActionPreference = 'Stop'; $root = (Get-Location).Path; $pidPath = Join-Path $root '.nerve\agent.pid'; $exePath = (Resolve-Path (Join-Path $root '.nerve\agent\nerve-agent.exe')).Path; if (Test-Path $pidPath) { $oldPid = 0; [void][int]::TryParse(([IO.File]::ReadAllText($pidPath).Trim()), [ref]$oldPid); if ($oldPid -gt 0) { $process = Get-CimInstance Win32_Process -Filter ('ProcessId=' + $oldPid) -ErrorAction SilentlyContinue; if ($null -ne $process -and $process.ExecutablePath -eq $exePath) { Stop-Process -Id $oldPid -Force -ErrorAction SilentlyContinue } } }; $values = @{}; foreach ($line in [IO.File]::ReadAllLines((Join-Path $root '.env'))) { if ($line -match '^\s*#' -or $line -notmatch '=') { continue }; $parts = $line -split '=', 2; $values[$parts[0].Trim()] = $parts[1].Trim().Trim([char]34).Trim([char]39) }; $env:NERVE_DISCOVERY_AGENT_TOKEN = $values['NERVE_DISCOVERY_AGENT_TOKEN']; $env:NERVE_AGENT_API_URL = '%NERVE_URL%/api/v1'; $process = Start-Process -FilePath $exePath -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardOutput (Join-Path $root '.nerve\logs\discovery-agent.log') -RedirectStandardError (Join-Path $root '.nerve\logs\discovery-agent-error.log') -PassThru; [IO.File]::WriteAllText($pidPath, [string]$process.Id); Start-Sleep -Milliseconds 750; if ($process.HasExited) { exit 1 }"
if errorlevel 1 (
  echo [FEHLER] Der lokale Discovery-Agent konnte nicht gestartet werden.
  goto :diagnostics
)

set /a NERVE_AGENT_ATTEMPT=0
:wait_for_agent
set /a NERVE_AGENT_ATTEMPT+=1
powershell -NoProfile -Command "$result = Invoke-RestMethod -TimeoutSec 4 '%NERVE_URL%/health/discovery'; if ($result.status -ne 'ok') { exit 1 }" >nul 2>&1
if not errorlevel 1 goto :runtime_ready
if %NERVE_AGENT_ATTEMPT% GEQ 20 (
  echo [FEHLER] Der lokale Discovery-Agent hat sich nicht innerhalb von 40 Sekunden verbunden.
  goto :diagnostics
)
timeout /t 2 /nobreak >nul
goto :wait_for_agent

:runtime_ready
echo.
echo [BEREIT] NERVE ist vollstaendig erreichbar.
echo          Lokal: %NERVE_URL%

set "NERVE_LAN_IP="
for /f "delims=" %%I in ('powershell -NoProfile -Command "$result = ''; foreach ($config in Get-NetIPConfiguration -ErrorAction SilentlyContinue) { if ($null -ne $config.IPv4DefaultGateway -and $null -ne $config.IPv4Address) { foreach ($address in $config.IPv4Address) { if ($address.IPAddress -notmatch '^(127\.|169\.254\.)') { $result = $address.IPAddress; break } } }; if ($result) { break } }; if ($result) { [Console]::Write($result) }"') do set "NERVE_LAN_IP=%%I"
if defined NERVE_LAN_IP echo          LAN:   http://%NERVE_LAN_IP%:%NERVE_WEB_PORT%
if not defined NERVE_LAN_IP echo          LAN:   Keine verlaessliche aktive IPv4-Adresse erkannt.

echo.
echo [INFO] Status: docker compose ps
echo [INFO] Logs:   docker compose logs --tail 100
echo [INFO] Agent:  .nerve\logs\discovery-agent.log
echo [INFO] Stop:   stop.bat
start "" "%NERVE_URL%"
call :hold_window
exit /b 0

:startup_failed
echo.
echo [FEHLER] Docker Compose konnte den NERVE-Stack nicht starten.
goto :diagnostics

:runtime_failed
echo.
echo [FEHLER] NERVE wurde innerhalb von 120 Sekunden nicht vollstaendig bereit.
echo          Der Browser wird nicht geoeffnet.

:diagnostics
echo.
echo ===== Containerstatus =====
docker compose ps
echo.
echo ===== Relevante Logs =====
docker compose logs --tail 120 postgres api web
echo.
echo ===== Discovery-Agent =====
if exist ".nerve\logs\discovery-agent.log" powershell -NoProfile -Command "Get-Content -Tail 80 '.nerve\logs\discovery-agent.log'"
if exist ".nerve\logs\discovery-agent-error.log" powershell -NoProfile -Command "Get-Content -Tail 80 '.nerve\logs\discovery-agent-error.log'"
call :hold_window
exit /b 1

:hold_window
echo.
echo [INFO] Druecke eine beliebige Taste, um dieses Fenster zu schliessen.
pause >nul
exit /b 0
