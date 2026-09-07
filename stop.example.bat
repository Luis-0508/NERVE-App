@echo off
setlocal EnableExtensions DisableDelayedExpansion

cd /d "%~dp0"
if errorlevel 1 exit /b 1

echo [NERVE] Stoppe den lokalen Discovery-Agenten...
if exist ".nerve\agent.pid" (
  for /f "usebackq delims=" %%P in (".nerve\agent.pid") do taskkill /PID %%P /T /F >nul 2>&1
  del /Q ".nerve\agent.pid" >nul 2>&1
)

echo [NERVE] Stoppe Web, API und PostgreSQL...
docker compose down
if errorlevel 1 (
  echo [FEHLER] Der Compose-Stack konnte nicht vollstaendig gestoppt werden.
  pause
  exit /b 1
)

echo [OK] NERVE wurde gestoppt. Die PostgreSQL-Daten bleiben erhalten.
pause
