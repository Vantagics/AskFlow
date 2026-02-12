@echo off
setlocal

REM Server configuration — read from environment variables.
REM Set these before running: DEPLOY_SERVER, DEPLOY_USER, DEPLOY_PASS
if "%DEPLOY_SERVER%"=="" (
    echo [ERROR] DEPLOY_SERVER environment variable not set.
    echo Set it with: set DEPLOY_SERVER=your.server.host
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

echo [5/6] Configuring ffmpeg in config.json...
%PLINK% -batch -pw %PASS% -no-antispoof %USER%@%SERVER% "cd %REMOTE_DIR%/data && if [ -f config.json ]; then sed -i 's/\"ffmpeg_path\": \"[^\"]*\"/\"ffmpeg_path\": \"\/usr\/bin\/ffmpeg\"/' config.json && echo '  Config updated'; fi"
echo.

echo [6/6] Removing nginx cache headers...
%PLINK% -batch -pw %PASS% -no-antispoof %USER%@%SERVER% "sed -i '/# 禁用缓存/,/expires -1;/d' /etc/nginx/conf.d/vantagedata.chat.conf && sed -i '/add_header Cache-Control.*no-store/d; /add_header Pragma.*no-cache/d; /add_header Expires.*0/d; /proxy_no_cache/d; /proxy_cache_bypass/d' /etc/nginx/conf.d/vantagedata.chat.conf && nginx -t && systemctl reload nginx && echo '  Nginx cache disabled'"
echo.

REM --- Cleanup ---
del /f deploy.tar.gz 2>nul

echo ============================================
echo  Deploy complete!
echo  URL: http://%SERVER%
echo ============================================

endlocal
pause
