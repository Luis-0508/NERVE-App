@echo off
setlocal EnableExtensions DisableDelayedExpansion

cd /d "%~dp0"
if errorlevel 1 (
  echo [FEHLER] Das Projektverzeichnis konnte nicht geoeffnet werden.
  goto :failed
)

echo ==========================================
echo NERVE - lokale Ersteinrichtung
echo ==========================================
echo.
echo Dieser Assistent erzeugt nur lokale Dateien.
echo Passwoerter werden nicht im Repository gespeichert.
echo.

if not exist "start.example.bat" (
  echo [FEHLER] start.example.bat fehlt im Projektroot.
  goto :failed
)

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$ErrorActionPreference = 'Stop';" ^
  "$root = (Get-Location).Path;" ^
  "$envPath = Join-Path $root '.env';" ^
  "$startTemplate = Join-Path $root 'start.example.bat';" ^
  "$startPath = Join-Path $root 'start.bat';" ^
  "$writeEnv = $true;" ^
  "if (Test-Path -LiteralPath $envPath) {" ^
  "  $answer = Read-Host '.env existiert bereits. Neu erzeugen und ersetzen? [j/N]';" ^
  "  $writeEnv = $answer -match '^(j|ja|y|yes)$';" ^
  "  if (-not $writeEnv) { Write-Host '[INFO] Bestehende .env bleibt unveraendert.' -ForegroundColor Yellow }" ^
  "};" ^
  "if ($writeEnv) {" ^
  "  $port = 3000;" ^
  "  while ($true) {" ^
  "    $rawPort = Read-Host 'Web-Port [3000]';" ^
  "    if ([string]::IsNullOrWhiteSpace($rawPort)) { break };" ^
  "    $candidate = 0;" ^
  "    if ([int]::TryParse($rawPort, [ref]$candidate) -and $candidate -ge 1 -and $candidate -le 65535) { $port = $candidate; break };" ^
  "    Write-Host 'Bitte einen Port zwischen 1 und 65535 eingeben.' -ForegroundColor Red;" ^
  "  };" ^
  "  $bytes = New-Object byte[] 24;" ^
  "  $rng = New-Object Security.Cryptography.RNGCryptoServiceProvider;" ^
  "  try { $rng.GetBytes($bytes) } finally { $rng.Dispose() };" ^
  "  $password = [BitConverter]::ToString($bytes).Replace('-', '').ToLowerInvariant();" ^
  "  $lines = @(" ^
  "    'NERVE_HTTP_ADDR=:8080'," ^
  "    ('NERVE_DATABASE_URL=postgres://nerve:' + $password + '@postgres:5432/nerve?sslmode=disable')," ^
  "    'NERVE_MIGRATIONS_DIR=/app/migrations'," ^
  "    'NERVE_COOKIE_SECURE=false'," ^
  "    'NERVE_TRUST_PROXY=true'," ^
  "    ('NERVE_ALLOWED_ORIGIN=http://localhost:' + $port)," ^
  "    'NERVE_SESSION_TTL=24h'," ^
  "    'NERVE_LOG_LEVEL=info'," ^
  "    ('NEXT_PUBLIC_SITE_URL=http://localhost:' + $port)," ^
  "    ('NERVE_WEB_PORT=' + $port)," ^
  "    'POSTGRES_DB=nerve'," ^
  "    'POSTGRES_USER=nerve'," ^
  "    ('POSTGRES_PASSWORD=' + $password)" ^
  "  );" ^
  "  $utf8 = New-Object Text.UTF8Encoding($false);" ^
  "  [IO.File]::WriteAllText($envPath, (($lines -join [Environment]::NewLine) + [Environment]::NewLine), $utf8);" ^
  "  Write-Host ('[OK] .env wurde fuer http://localhost:' + $port + ' erzeugt.') -ForegroundColor Green;" ^
  "  Write-Host '[OK] Ein zufaelliges Datenbankpasswort wurde sicher in .env gespeichert.' -ForegroundColor Green;" ^
  "};" ^
  "$writeStart = $true;" ^
  "if (Test-Path -LiteralPath $startPath) {" ^
  "  $answer = Read-Host 'start.bat existiert bereits. Aus der aktuellen Vorlage neu erzeugen? [j/N]';" ^
  "  $writeStart = $answer -match '^(j|ja|y|yes)$';" ^
  "  if (-not $writeStart) { Write-Host '[INFO] Bestehende start.bat bleibt unveraendert.' -ForegroundColor Yellow }" ^
  "};" ^
  "if ($writeStart) {" ^
  "  Copy-Item -LiteralPath $startTemplate -Destination $startPath -Force;" ^
  "  Write-Host '[OK] start.bat wurde erzeugt.' -ForegroundColor Green;" ^
  "};" ^
  "Write-Host '';" ^
  "Write-Host '[FERTIG] Starte NERVE jetzt mit start.bat.' -ForegroundColor Cyan;"

if errorlevel 1 goto :failed

echo.
echo [INFO] Druecke eine beliebige Taste, um dieses Fenster zu schliessen.
pause >nul
exit /b 0

:failed
echo.
echo [FEHLER] Die lokale Einrichtung konnte nicht abgeschlossen werden.
echo [INFO] Druecke eine beliebige Taste, um dieses Fenster zu schliessen.
pause >nul
exit /b 1

