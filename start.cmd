@echo off
setlocal
cd /d "%~dp0"

rem Default port: first argument wins, then %PORT%, then 8000.
set "DEFAULT_PORT=8000"
if defined PORT set "DEFAULT_PORT=%PORT%"
set "PORT=%~1"
if "%PORT%"=="" set "PORT=%DEFAULT_PORT%"

if not exist ".venv\Scripts\python.exe" (
    echo [ERROR] .venv not found. First run:
    echo   python -m venv .venv
    echo   .venv\Scripts\python -m pip install -r requirements.txt
    pause
    exit /b 1
)

echo Starting app at http://127.0.0.1:%PORT%
.venv\Scripts\python.exe -m uvicorn app.main:app --host 0.0.0.0 --port %PORT%
endlocal
