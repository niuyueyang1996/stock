[CmdletBinding()]
param(
    [switch]$SkipTests
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if ([System.Environment]::OSVersion.Platform -ne [System.PlatformID]::Win32NT) {
    throw 'Windows 安装包必须在 Windows 10/11 x64 或 Windows GitHub Actions runner 上构建。'
}

$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$BuildRoot = Join-Path $Root 'build\windows'
$VenvDir = Join-Path $BuildRoot '.venv'
$VenvPython = Join-Path $VenvDir 'Scripts\python.exe'
$PyInstaller = Join-Path $VenvDir 'Scripts\pyinstaller.exe'
$PyInstallerDist = Join-Path $BuildRoot 'dist'
$PyInstallerWork = Join-Path $BuildRoot 'work'
$BundleDir = Join-Path $PyInstallerDist 'StockAnalyzer'
$OutputDir = Join-Path $Root 'dist\windows'
$LockFile = Join-Path $PSScriptRoot 'requirements-windows.lock'
$SpecFile = Join-Path $PSScriptRoot 'StockAnalyzer.spec'
$IssFile = Join-Path $PSScriptRoot 'StockAnalyzer.iss'
$CompiledIssFile = Join-Path $BuildRoot 'StockAnalyzer.iss'
$IconScript = Join-Path $PSScriptRoot 'generate_icon.py'
$IconFile = Join-Path $PSScriptRoot 'stock-analyzer.ico'
$EChartsFile = Join-Path $Root 'static\vendor\echarts.min.js'
$EChartsSha256 = 'bf4a223524e40b77c304bec67e1222cf551f14880cf42c69dc046558e11c07b1'

New-Item -ItemType Directory -Force -Path $BuildRoot, $OutputDir | Out-Null

$Python = Get-Command python -ErrorAction SilentlyContinue
if (-not $Python) {
    throw '未找到 Python。构建机需要安装 64 位 Python 3.12，并将 python 加入 PATH。目标用户电脑不需要安装 Python。'
}

$PythonCheck = & $Python.Source -c "import struct,sys; print('ok' if sys.version_info[:2] == (3,12) and struct.calcsize('P') == 8 else 'bad')"
if ($LASTEXITCODE -ne 0 -or $PythonCheck.Trim() -ne 'ok') {
    throw '构建机必须使用 64 位 Python 3.12。'
}

if (Test-Path $VenvPython) {
    $VenvCheck = & $VenvPython -c "import struct,sys; print('ok' if sys.version_info[:2] == (3,12) and struct.calcsize('P') == 8 else 'bad')"
    if ($LASTEXITCODE -ne 0 -or $VenvCheck.Trim() -ne 'ok') {
        Remove-Item -Recurse -Force $VenvDir
    }
}
if (-not (Test-Path $VenvPython)) {
    & $Python.Source -m venv $VenvDir
    if ($LASTEXITCODE -ne 0) { throw '创建 Windows 构建虚拟环境失败。' }
}

$env:PIP_DISABLE_PIP_VERSION_CHECK = '1'
& $VenvPython -m pip install 'pip==26.0.1'
if ($LASTEXITCODE -ne 0) { throw '安装锁定的 pip 版本失败。' }
& $VenvPython -m pip install -r $LockFile
if ($LASTEXITCODE -ne 0) { throw '安装锁定的 Windows 构建依赖失败。' }

Push-Location $Root
try {
    if (-not $SkipTests) {
        $TestTemp = Join-Path $BuildRoot 'pytest-temp'
        if (Test-Path $TestTemp) { Remove-Item -Recurse -Force $TestTemp }
        & $VenvPython -m pytest tests -q -p no:cacheprovider "--basetemp=$TestTemp"
        if ($LASTEXITCODE -ne 0) { throw '测试失败，已停止生成安装包。' }
    }

    $ActualEChartsHash = (Get-FileHash -Algorithm SHA256 $EChartsFile).Hash.ToLowerInvariant()
    if ($ActualEChartsHash -ne $EChartsSha256) {
        throw "ECharts 文件校验失败：$ActualEChartsHash"
    }

    & $VenvPython $IconScript
    if ($LASTEXITCODE -ne 0) { throw '生成应用图标失败。' }

    if (Test-Path $PyInstallerDist) { Remove-Item -Recurse -Force $PyInstallerDist }
    if (Test-Path $PyInstallerWork) { Remove-Item -Recurse -Force $PyInstallerWork }
    & $PyInstaller --noconfirm --clean --distpath $PyInstallerDist --workpath $PyInstallerWork $SpecFile
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path (Join-Path $BundleDir 'StockAnalyzer.exe'))) {
        throw 'PyInstaller 打包失败。'
    }

    $ForbiddenNames = @('etf.db', 'stock_list.json', 'hk_stock_list.json')
    $Forbidden = Get-ChildItem $BundleDir -Recurse -File | Where-Object { $ForbiddenNames -contains $_.Name }
    if ($Forbidden) {
        throw "安装程序目录包含个人数据或缓存：$($Forbidden.FullName -join ', ')"
    }

    $Version = (& $VenvPython -c "from app.version import APP_VERSION; print(APP_VERSION)").Trim()
    if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw "应用版本格式无效：$Version" }

    # Windows PowerShell and older compiler locales can otherwise interpret a
    # BOM-less UTF-8 script as ANSI and garble the Chinese wizard text.
    $IssText = [IO.File]::ReadAllText($IssFile, [Text.Encoding]::UTF8)
    $Utf8Bom = [Text.UTF8Encoding]::new($true)
    [IO.File]::WriteAllText($CompiledIssFile, $IssText, $Utf8Bom)

    $IsccCandidates = @(
        (Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 6\ISCC.exe'),
        (Join-Path $env:ProgramFiles 'Inno Setup 6\ISCC.exe')
    )
    $IsccCommand = Get-Command ISCC.exe -ErrorAction SilentlyContinue
    if ($IsccCommand) { $IsccCandidates += $IsccCommand.Source }
    $Iscc = $IsccCandidates | Where-Object { $_ -and (Test-Path $_) } | Select-Object -First 1
    if (-not $Iscc) {
        throw '未找到 Inno Setup 6。请先安装：https://jrsoftware.org/isdl.php'
    }

    & $Iscc "/DMyAppVersion=$Version" "/DSourceDir=$BundleDir" "/DOutputDir=$OutputDir" "/DIconFile=$IconFile" $CompiledIssFile
    if ($LASTEXITCODE -ne 0) { throw 'Inno Setup 编译失败。' }

    $Installer = Join-Path $OutputDir "StockAnalyzer-Setup-$Version-x64.exe"
    if (-not (Test-Path $Installer)) { throw "未找到安装包：$Installer" }
    $Hash = (Get-FileHash -Algorithm SHA256 $Installer).Hash.ToLowerInvariant()
    $HashFile = "$Installer.sha256"
    Set-Content -Path $HashFile -Encoding Ascii -Value "$Hash  $([IO.Path]::GetFileName($Installer))"

    Write-Host ''
    Write-Host "安装包：$Installer" -ForegroundColor Green
    Write-Host "校验文件：$HashFile" -ForegroundColor Green
}
finally {
    Pop-Location
}
