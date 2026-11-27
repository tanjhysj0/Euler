@echo off
setlocal
set "SCRIPT_DIR=%~dp0"
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT_DIR%dev.ps1" %*
set "DEV_EXIT_CODE=%ERRORLEVEL%"
endlocal & exit /b %DEV_EXIT_CODE%
