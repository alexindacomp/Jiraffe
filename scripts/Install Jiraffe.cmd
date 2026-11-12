@echo off
rem Installs Jiraffe for the current user (no admin rights) from this archive:
rem copies jiraffe.exe to %LOCALAPPDATA%\Programs\Jiraffe, adds Start menu and
rem desktop shortcuts and starts it. After that Jiraffe updates itself.
rem Keep this file ASCII with Windows line endings (CRLF, see .gitattributes).
setlocal
if not exist "%~dp0jiraffe.exe" (
  echo jiraffe.exe was not found next to this file. Extract the whole archive and run it again.
  pause
  exit /b 1
)
if not exist "%~dp0install.ps1" (
  echo install.ps1 was not found next to this file. Extract the whole archive and run it again.
  pause
  exit /b 1
)
set "JIRAFFE_SOURCE=%~dp0jiraffe.exe"
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1"
echo.
pause
