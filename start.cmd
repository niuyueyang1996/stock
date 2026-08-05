@echo off
setlocal
cd /d "%~dp0"

rem Default port: first argument wins, then %PORT%, then 8000.
set "DEFAULT_PORT=8000"
if defined PORT set "DEFAULT_PORT=%PORT%"
set "PORT=%~1"
if "%PORT%"=="" set "PORT=%DEFAULT_PORT%"

rem Locate a suitable Python: code uses 'X | None' syntax, needs 3.10+, prefer 3.12.
set "PYTHON_BIN="
where python3.12 >nul 2>&1 && set "PYTHON_BIN=python3.12"
if not defined PYTHON_BIN (
    where python3.11 >nul 2>&1 && set "PYTHON_BIN=python3.11"
)
if not defined PYTHON_BIN (
    where python3.10 >nul 2>&1 && set "PYTHON_BIN=python3.10"
)
if not defined PYTHON_BIN set "PYTHON_BIN=python"
if "%PYTHON_BIN%"=="python" (
    python -c "import sys; sys.exit(0 if sys.version_info >= (3,10) else 1)" >nul 2>&1
    if errorlevel 1 (
        echo [ERROR] Need Python 3.10+ (code uses 'X ^| None' syntax). Please install Python 3.12.
        pause
        exit /b 1
    )
)

rem Create/rebuild/install-deps as needed:
rem   - no .venv           -> create
rem   - version < 3.10     -> rebuild with %PYTHON_BIN%
rem   - version ok, no deps -> install deps only
if not exist ".venv\Scripts\python.exe" goto :fresh
.venv\Scripts\python.exe -c "import sys; sys.exit(0 if sys.version_info >= (3,10) else 1)" >nul 2>&1
if errorlevel 1 goto :rebuild
if exist ".venv\Scripts\uvicorn.exe" goto :start
goto :install_deps

:rebuild
echo [WARN] .venv Python version is too old (need 3.10+). Rebuilding with %PYTHON_BIN%...
rmdir /s /q .venv

:fresh
echo [INFO] Creating virtual environment with %PYTHON_BIN%...
%PYTHON_BIN% -m venv .venv
if errorlevel 1 (
    echo [ERROR] Failed to create .venv. Please check your Python install.
    pause
    exit /b 1
)

:install_deps
echo [INFO] Installing dependencies (this may take a while on first run)...
.venv\Scripts\python.exe -m pip install -r requirements.txt
if errorlevel 1 (
    echo [ERROR] Failed to install dependencies. Please check your network and rerun.
    pause
    exit /b 1
)

:start
echo Starting app at http://127.0.0.1:%PORT%
.venv\Scripts\python.exe -m uvicorn app.main:app --host 0.0.0.0 --port %PORT%
endlocal
