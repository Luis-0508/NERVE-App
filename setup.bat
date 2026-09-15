@echo off
setlocal EnableExtensions DisableDelayedExpansion
cd /d "%~dp0"
if errorlevel 1 exit /b 1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\setup.ps1"
if errorlevel 1 (
  echo [FEHLER] Einrichtung nicht abgeschlossen. Bestehende Dienste wurden nicht veraendert.
  pause
  exit /b 1
)
echo [FERTIG] NERVE ist eingerichtet. Starte jetzt start.bat.
timeout /t 5 /nobreak >nul 2>&1
exit /b 0
