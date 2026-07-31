@echo off
setlocal
cd /d "%~dp0"

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0build-fpk.ps1" %*
set "exitCode=%ERRORLEVEL%"

if not defined CI pause
exit /b %exitCode%
