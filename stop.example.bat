@echo off
setlocal EnableExtensions DisableDelayedExpansion

cd /d "%~dp0"
if errorlevel 1 exit /b 1

echo [NERVE] Stoppe den lokalen Discovery-Agenten...
powershell -NoProfile -ExecutionPolicy Bypass -File "scripts\manage-discovery-agent.ps1" -Action Stop -Root "%CD%"
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
