@echo off
echo Restarting AskFlow service...
taskkill /F /IM askflow.exe 2>nul
timeout /t 2 /nobreak >nul
start "" askflow.exe
echo Service restarted!
pause
