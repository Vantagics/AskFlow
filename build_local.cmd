@echo off
setlocal enabledelayedexpansion

echo ================================================
echo   Askflow 本地构建脚本
echo   编译 + NSIS 打包
echo ================================================
echo.

REM 设置颜色输出（可选）
set "GREEN=[92m"
set "RED=[91m"
set "YELLOW=[93m"
set "NC=[0m"

REM ====================================
REM 1. 环境检�?
REM ====================================
echo [1/6] 检查构建环�?..

REM 检�?Go
where go >nul 2>&1
if %errorlevel% neq 0 (
    echo %RED%[错误]%NC% 未找�?Go，请安装 Go 并添加到 PATH
    echo 下载地址: https://go.dev/dl/
    exit /b 1
)
for /f "tokens=3" %%i in ('go version') do set GO_VERSION=%%i
echo       �?Go %GO_VERSION%

REM 检�?NSIS
set "NSIS_PATH=C:\Program Files (x86)\NSIS\makensis.exe"
if not exist "%NSIS_PATH%" (
    echo %RED%[错误]%NC% 未找�?NSIS
    echo 请安�?NSIS 3.0 或更高版�?
    echo 下载地址: https://nsis.sourceforge.io/Download
    exit /b 1
)
for /f "tokens=2 delims=v" %%i in ('"%NSIS_PATH%" /VERSION') do set NSIS_VERSION=%%i
echo       �?NSIS v%NSIS_VERSION%

REM 检查前端文�?
if not exist "frontend\dist\index.html" (
    echo %YELLOW%[警告]%NC% 前端构建文件不存�?
    echo 请先构建前端: cd frontend ^&^& npm run build
    exit /b 1
)
echo       �?前端文件存在

echo.

REM ====================================
REM 2. 创建构建目录
REM ====================================
echo [2/6] 准备构建目录...

if not exist "build" mkdir build
if not exist "build\dist" mkdir build\dist
if not exist "build\installer" mkdir build\installer

REM 清理旧文�?
if exist "build\dist\askflow.exe" del /q "build\dist\askflow.exe"
if exist "build\dist\frontend" rmdir /s /q "build\dist\frontend"
if exist "build\installer\askflow-installer.exe" del /q "build\installer\askflow-installer.exe"

echo       �?构建目录准备完成

echo.

REM ====================================
REM 3. 编译 Go 程序
REM ====================================
echo [3/6] 编译 Windows 可执行文�?..

set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=1

REM 添加版本信息（可选）
for /f "tokens=2 delims==" %%i in ('git rev-parse --short HEAD 2^>nul') do set GIT_COMMIT=%%i
if "%GIT_COMMIT%"=="" set GIT_COMMIT=unknown

echo       - 目标平台: Windows AMD64
echo       - Git Commit: %GIT_COMMIT%

go build -ldflags "-s -w" -o build\dist\askflow.exe .
if %errorlevel% neq 0 (
    echo %RED%[错误]%NC% 编译失败
    exit /b 1
)

REM 显示文件大小
for %%F in (build\dist\askflow.exe) do set SIZE=%%~zF
set /a SIZE_MB=!SIZE!/1024/1024
echo       �?编译成功 (大小: !SIZE_MB! MB)

echo.

REM ====================================
REM 4. 复制前端文件
REM ====================================
echo [4/6] 复制前端资源...

xcopy /s /e /y /q frontend\dist\* build\dist\frontend\dist\ >nul
if %errorlevel% neq 0 (
    echo %RED%[错误]%NC% 复制前端文件失败
    exit /b 1
)

REM 统计文件数量
for /f %%A in ('dir /b /s /a-d build\dist\frontend\dist ^| find /c /v ""') do set FILE_COUNT=%%A
echo       �?已复�?%FILE_COUNT% 个前端文�?

echo.

REM ====================================
REM 5. 检�?LICENSE 文件
REM ====================================
echo [5/6] 检查许可证文件...

if not exist "LICENSE" (
    echo %YELLOW%[警告]%NC% LICENSE 文件不存在，创建占位�?..
    echo MIT License > LICENSE
    echo Copyright ^(c^) 2026 Vantage >> LICENSE
    echo. >> LICENSE
    echo Permission is hereby granted, free of charge... >> LICENSE
)
echo       �?LICENSE 文件存在

echo.

REM ====================================
REM 6. 构建 NSIS 安装�?
REM ====================================
echo [6/6] 构建 NSIS 安装程序...

if not exist "build\installer\askflow.nsi" (
    echo %RED%[错误]%NC% NSIS 脚本不存�? build\installer\askflow.nsi
    exit /b 1
)

"%NSIS_PATH%" /V2 build\installer\askflow.nsi
if %errorlevel% neq 0 (
    echo %RED%[错误]%NC% NSIS 构建失败
    exit /b 1
)

REM 显示安装包大�?
for %%F in (build\installer\askflow-installer.exe) do set INSTALLER_SIZE=%%~zF
set /a INSTALLER_SIZE_MB=!INSTALLER_SIZE!/1024/1024
echo       �?安装包构建成�?(大小: !INSTALLER_SIZE_MB! MB)

echo.

REM ====================================
REM 构建完成
REM ====================================
echo ================================================
echo   构建完成�?
echo ================================================
echo.
echo 输出文件:
echo   1. 可执行文�? build\dist\askflow.exe
echo   2. 安装程序:   build\installer\askflow-installer.exe
echo.
echo 接下来可�?
echo   - 运行测试: build\dist\askflow.exe help
echo   - 安装服务: build\installer\askflow-installer.exe
echo   - 分发安装包到目标服务�?
echo.

REM 询问是否测试运行
set /p RUN_TEST="是否运行 askflow.exe help 测试? (Y/N): "
if /i "%RUN_TEST%"=="Y" (
    echo.
    echo ================================================
    echo   测试运行
    echo ================================================
    build\dist\askflow.exe help
)

endlocal
pause
