@echo off
setlocal enabledelayedexpansion

echo ================================================
echo   Askflow Windows Installer Builder
echo ================================================
echo.

REM Check if Go is installed
where go >nul 2>&1
if %errorlevel% neq 0 (
    echo [ERROR] Go is not installed or not in PATH
    exit /b 1
)

REM Check if NSIS is installed
set NSIS_PATH=C:\Program Files (x86)\NSIS\makensis.exe
if not exist "%NSIS_PATH%" (
    echo [ERROR] NSIS not found at: %NSIS_PATH%
    echo Please install NSIS from https://nsis.sourceforge.io/
    exit /b 1
)

REM Create build directories
echo [1/5] Creating build directories...
if not exist build\dist mkdir build\dist
if not exist build\installer mkdir build\installer

REM Build executable
echo [2/5] Building askflow.exe for Windows...
set GOOS=windows
set GOARCH=amd64
go build -o build\dist\askflow.exe .
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build executable
    exit /b 1
)
echo       âœ?Executable built successfully

REM Copy frontend files
echo [3/5] Copying frontend files...
if not exist build\dist\frontend\dist mkdir build\dist\frontend\dist
xcopy /s /e /y /q frontend\dist\* build\dist\frontend\dist\ >nul
if %errorlevel% neq 0 (
    echo [ERROR] Failed to copy frontend files
    exit /b 1
)
echo       âœ?Frontend files copied

REM Check for LICENSE file
echo [4/5] Checking for LICENSE file...
if not exist LICENSE (
    echo [WARNING] LICENSE file not found, creating placeholder...
    echo MIT License > LICENSE
)
echo       âœ?LICENSE file present

REM Build installer
echo [5/5] Building NSIS installer...
"%NSIS_PATH%" /V2 build\installer\askflow.nsi
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build installer
    exit /b 1
)
echo       âœ?Installer built successfully

REM Show results
echo.
echo ================================================
echo   Build Complete!
echo ================================================
echo.
echo Installer: build\installer\askflow-installer.exe
echo.
echo You can now distribute this installer to install
echo Askflow as a Windows service.
echo.

endlocal
