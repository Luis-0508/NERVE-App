@echo off
setlocal EnableExtensions DisableDelayedExpansion
cd /d "%~dp0"
if errorlevel 1 exit /b 1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\setup-traffic-sensor.ps1"
if errorlevel 1 (
  echo [FEHLER] Optionale Traffic-Einrichtung nicht abgeschlossen.
  pause
  exit /b 1
)
pause
exit /b 0
