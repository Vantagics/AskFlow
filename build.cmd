@echo off
setlocal

REM Read credentials from environment variables (never hardcode secrets!)
if "%DEPLOY_SERVER%"=="" (
    echo [ERROR] DEPLOY_SERVER environment variable not set.
    echo         Set it with: set DEPLOY_SERVER=your.server.host
    exit /b 1
)
if "%DEPLOY_USER%"=="" (
    echo [ERROR] DEPLOY_USER environment variable not set.
    exit /b 1
)
if "%DEPLOY_PASS%"=="" (
    echo [ERROR] DEPLOY_PASS environment variable not set.
    exit /b 1
)
set SERVER=%DEPLOY_SERVER%
set USER=%DEPLOY_USER%
set PASS=%DEPLOY_PASS%
set REMOTE_DIR=/root/vantageselfservice
set BINARY_NAME=helpdesk
set PLINK="C:\Program Files\PuTTY\plink.exe"
set PSCP="C:\Program Files\PuTTY\pscp.exe"

echo ============================================
echo  Helpdesk One-Click Deploy
echo  Target: %USER%@%SERVER%:%REMOTE_DIR%
echo ============================================
echo.

REM --- Step 1: Package ---
echo [1/4] Packaging project files...
if exist deploy.tar.gz del /f deploy.tar.gz
tar -czf deploy.tar.gz --exclude=deploy.tar.gz --exclude=build.cmd --exclude=start.sh --exclude=.git --exclude=.kiro --exclude=.vscode --exclude=*.exe *.go go.mod go.sum internal frontend
if %errorlevel% neq 0 (
    echo [ERROR] Packaging failed!
    exit /b 1
)
echo        Package OK
echo.

REM --- Step 2: Cache host key ---
echo [PREP] Caching server host key...
echo y | %PLINK% -pw %PASS% %USER%@%SERVER% "echo connected" >nul 2>&1
echo        Done
echo.

REM --- Step 3: Upload package and start script ---
echo [2/4] Uploading to server...
%PLINK% -batch -pw %PASS% -no-antispoof %USER%@%SERVER% "mkdir -p %REMOTE_DIR%"
%PSCP% -pw %PASS% -q deploy.tar.gz %USER%@%SERVER%:%REMOTE_DIR%/deploy.tar.gz
if %errorlevel% neq 0 (
    echo [ERROR] Upload failed!
    exit /b 1
)
%PSCP% -pw %PASS% -q start.sh %USER%@%SERVER%:%REMOTE_DIR%/start.sh
echo        Upload OK
echo.

REM --- Step 4: Remote extract, build, and restart ---
echo [3/4] Building on remote server...
%PLINK% -batch -pw %PASS% -no-antispoof %USER%@%SERVER% "cd %REMOTE_DIR% && tar -xzf deploy.tar.gz && rm -f deploy.tar.gz && go build -o %BINARY_NAME% . 2>&1"
if %errorlevel% neq 0 (
    echo [ERROR] Remote build failed!
    exit /b 1
)
echo        Build OK
echo.

echo [4/4] Restarting service...
%PLINK% -batch -pw %PASS% -no-antispoof %USER%@%SERVER% "chmod +x %REMOTE_DIR%/start.sh && bash %REMOTE_DIR%/start.sh"
echo.

REM --- Cleanup ---
del /f deploy.tar.gz 2>nul

echo ============================================
echo  Deploy complete!
echo  URL: http://%SERVER%
echo ============================================

endlocal
