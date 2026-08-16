@echo off
rem 股票分析 Windows 启动器（纯 ASCII，cmd 按 GBK 解析）
rem 单实例 + 仅监听 127.0.0.1 + 起服务后开浏览器
setlocal
cd /d "%~dp0"

set PORT=8000
set URL=http://127.0.0.1:%PORT%/

rem 检查端口是否已被本服务占用（健康检查）
powershell -NoProfile -Command "try { $r = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:%PORT%/api/health' -TimeoutSec 2; if ($r.StatusCode -eq 200 -and $r.Content -like '*stock-analyzer*') { exit 0 } } catch { exit 1 }" >nul 2>&1
if %ERRORLEVEL% EQU 0 (
    echo 服务已在运行，打开页面...
    start "" "%URL%"
    exit /b 0
)

rem 启动后端（新窗口，带数据目录）
set "STOCK_APP_HOME=%LOCALAPPDATA%\StockAnalyzer"
if not exist "%STOCK_APP_HOME%" mkdir "%STOCK_APP_HOME%"
start "StockAnalyzer-Server" /min cmd /c ""%~dp0stockanalyzer-server.exe" --listen 127.0.0.1:%PORT%"

rem 等健康检查通过（最多 30 秒）
set N=0
:waitloop
powershell -NoProfile -Command "try { $r = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:%PORT%/api/health' -TimeoutSec 2; if ($r.StatusCode -eq 200 -and $r.Content -like '*stock-analyzer*') { exit 0 } } catch { exit 1 }" >nul 2>&1
if %ERRORLEVEL% EQU 0 goto open
set /a N+=1
if %N% GEQ 30 goto fail
timeout /t 1 /nobreak >nul
goto waitloop

:open
start "" "%URL%"
exit /b 0

:fail
echo 服务启动失败，请查看日志：%STOCK_APP_HOME%\logs\server.log
pause
exit /b 1
