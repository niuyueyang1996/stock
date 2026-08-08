@echo off
setlocal
cd /d "%~dp0"

rem ============================================================
rem  Personal ETF Portfolio Analyzer - launcher
rem  Usage:  start.cmd [port]    (default 8000)
rem  NOTE: keep this file pure ASCII - cmd parses GBK, not UTF-8.
rem  Order: use existing .venv first; only need system python
rem  when .venv is missing/old/has no deps.
rem ============================================================

rem ---------- port: arg > env PORT > 8000 ----------
set "DEFAULT_PORT=8000"
if defined PORT set "DEFAULT_PORT=%PORT%"
set "PORT=%~1"
if "%PORT%"=="" set "PORT=%DEFAULT_PORT%"

rem ---------- use ready .venv directly ----------
if not exist ".venv\Scripts\python.exe" goto :need_python
.venv\Scripts\python.exe -c "import sys;sys.exit(0 if sys.version_info>=(3,10) else 1)" >nul 2>&1
if errorlevel 1 goto :need_python
if exist ".venv\Scripts\uvicorn.exe" goto :start
goto :install_deps

rem ---------- locate system Python for venv create/rebuild ----------
:need_python
set "PYTHON_BIN="
where python3.12 >nul 2>&1 && set "PYTHON_BIN=python3.12"
if not defined PYTHON_BIN where python3.11 >nul 2>&1 && set "PYTHON_BIN=python3.11"
if not defined PYTHON_BIN where python3.10 >nul 2>&1 && set "PYTHON_BIN=python3.10"
if not defined PYTHON_BIN set "PYTHON_BIN=python"
if "%PYTHON_BIN%"=="python" goto :check_python
goto :fresh

rem version check lives OUTSIDE any if-block so cmd does not
rem treat "(3,10)" as block-closing parentheses (classic bug).
:check_python
python -c "import sys;sys.exit(0 if sys.version_info>=(3,10) else 1)" >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Need Python 3.10+ ^(code uses X ^| None syntax^). Please install Python 3.12.
    pause
    exit /b 1
)

:fresh
echo [INFO] Creating virtual environment with %PYTHON_BIN%...
%PYTHON_BIN% -m venv .venv
if errorlevel 1 (
    echo [ERROR] Failed to create .venv. Please check your Python install.
    pause
    exit /b 1
)

:install_deps
echo [INFO] Installing dependencies (may take a while on first run)...
.venv\Scripts\python.exe -m pip install -r requirements.txt
if errorlevel 1 (
    echo [ERROR] Failed to install dependencies. Please check your network and rerun.
    pause
    exit /b 1
)

:start
echo Starting app at http://127.0.0.1:%PORT%

rem ---------- port-in-use check ----------
netstat -ano | findstr /c:":%PORT% " | findstr /c:"LISTENING" >nul 2>&1
if not errorlevel 1 (
    echo [ERROR] Port %PORT% is already in use. Another instance may be running.
    echo        Close the old window, or use another port:  start.cmd 9000
    pause
    exit /b 1
)

.venv\Scripts\python.exe -m uvicorn app.main:app --host 127.0.0.1 --port %PORT% --no-access-log
if errorlevel 1 (
    echo [ERROR] App failed to start. See error above.
    pause
    exit /b 1
)
endlocal
