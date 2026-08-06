[CmdletBinding()]
param(
    [switch]$SkipTests
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if ([System.Environment]::OSVersion.Platform -ne [System.PlatformID]::Win32NT) {
    throw 'The Windows installer must be built on Windows 10/11 x64 or a Windows GitHub Actions runner.'
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
$LanguageFile = Join-Path $PSScriptRoot 'languages\ChineseSimplified.isl'
$CompiledLanguageFile = Join-Path $BuildRoot 'ChineseSimplified.isl'
$IconScript = Join-Path $PSScriptRoot 'generate_icon.py'
$IconFile = Join-Path $PSScriptRoot 'stock-analyzer.ico'
$EChartsFile = Join-Path $Root 'static\vendor\echarts.min.js'
$EChartsSha256 = 'bf4a223524e40b77c304bec67e1222cf551f14880cf42c69dc046558e11c07b1'
$LanguageFileSha256 = 'e0b0b350e2245f3c5e65586dfe43d574f6e7f06f2261149aba284954b3fc9a8d'

New-Item -ItemType Directory -Force -Path $BuildRoot, $OutputDir | Out-Null

$Python = Get-Command python -ErrorAction SilentlyContinue
if (-not $Python) {
    throw 'Python was not found. The build machine needs 64-bit Python 3.12 on PATH.'
}

$PythonCheck = & $Python.Source -c "import struct,sys; print('ok' if sys.version_info[:2] == (3,12) and struct.calcsize('P') == 8 else 'bad')"
if ($LASTEXITCODE -ne 0 -or $PythonCheck.Trim() -ne 'ok') {
    throw 'The build machine must use 64-bit Python 3.12.'
}

if (Test-Path $VenvPython) {
    $VenvCheck = & $VenvPython -c "import struct,sys; print('ok' if sys.version_info[:2] == (3,12) and struct.calcsize('P') == 8 else 'bad')"
    if ($LASTEXITCODE -ne 0 -or $VenvCheck.Trim() -ne 'ok') {
        Remove-Item -Recurse -Force $VenvDir
    }
}
if (-not (Test-Path $VenvPython)) {
    & $Python.Source -m venv $VenvDir
    if ($LASTEXITCODE -ne 0) { throw 'Failed to create the Windows build virtual environment.' }
}

$env:PIP_DISABLE_PIP_VERSION_CHECK = '1'
& $VenvPython -m pip install 'pip==26.0.1'
if ($LASTEXITCODE -ne 0) { throw 'Failed to install the pinned pip version.' }
& $VenvPython -m pip install -r $LockFile
if ($LASTEXITCODE -ne 0) { throw 'Failed to install the pinned Windows build dependencies.' }

Push-Location $Root
try {
    if (-not $SkipTests) {
        $TestTemp = Join-Path $BuildRoot 'pytest-temp'
        if (Test-Path $TestTemp) { Remove-Item -Recurse -Force $TestTemp }
        & $VenvPython -m pytest tests -q -p no:cacheprovider "--basetemp=$TestTemp"
        if ($LASTEXITCODE -ne 0) { throw 'Tests failed; installer generation has been stopped.' }
    }

    $ActualEChartsHash = (Get-FileHash -Algorithm SHA256 $EChartsFile).Hash.ToLowerInvariant()
    if ($ActualEChartsHash -ne $EChartsSha256) {
        throw "ECharts checksum validation failed: $ActualEChartsHash"
    }
    $ActualLanguageHash = (Get-FileHash -Algorithm SHA256 $LanguageFile).Hash.ToLowerInvariant()
    if ($ActualLanguageHash -ne $LanguageFileSha256) {
        throw "Inno Setup language file checksum validation failed: $ActualLanguageHash"
    }

    & $VenvPython $IconScript
    if ($LASTEXITCODE -ne 0) { throw 'Failed to generate the application icon.' }

    if (Test-Path $PyInstallerDist) { Remove-Item -Recurse -Force $PyInstallerDist }
    if (Test-Path $PyInstallerWork) { Remove-Item -Recurse -Force $PyInstallerWork }
    & $PyInstaller --noconfirm --clean --distpath $PyInstallerDist --workpath $PyInstallerWork $SpecFile
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path (Join-Path $BundleDir 'StockAnalyzer.exe'))) {
        throw 'PyInstaller packaging failed.'
    }

    $ForbiddenNames = @('etf.db', 'stock_list.json', 'hk_stock_list.json')
    $Forbidden = Get-ChildItem $BundleDir -Recurse -File | Where-Object { $ForbiddenNames -contains $_.Name }
    if ($Forbidden) {
        throw "The application bundle contains personal data or cache files: $($Forbidden.FullName -join ', ')"
    }

    $BundleExe = Join-Path $BundleDir 'StockAnalyzer.exe'
    $BundleSmokeHome = Join-Path $BuildRoot 'bundle-smoke-home'
    if (Test-Path $BundleSmokeHome) { Remove-Item -Recurse -Force $BundleSmokeHome }
    $PreviousAppHome = [Environment]::GetEnvironmentVariable('STOCK_APP_HOME')
    try {
        $env:STOCK_APP_HOME = $BundleSmokeHome
        $BundleSmoke = Start-Process -FilePath $BundleExe -ArgumentList '--smoke-test' -Wait -PassThru
        if ($BundleSmoke.ExitCode -ne 0) {
            throw "Packaged application smoke test failed with exit code $($BundleSmoke.ExitCode)."
        }
    }
    finally {
        if ($null -eq $PreviousAppHome) {
            Remove-Item Env:STOCK_APP_HOME -ErrorAction SilentlyContinue
        }
        else {
            $env:STOCK_APP_HOME = $PreviousAppHome
        }
    }

    $Version = (& $VenvPython -c "from app.version import APP_VERSION; print(APP_VERSION)").Trim()
    if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw "Invalid application version: $Version" }

    # Windows PowerShell and older compiler locales can otherwise interpret a
    # BOM-less UTF-8 script as ANSI and garble the Chinese wizard text.
    $IssText = [IO.File]::ReadAllText($IssFile, [Text.Encoding]::UTF8)
    $Utf8Bom = [Text.UTF8Encoding]::new($true)
    [IO.File]::WriteAllText($CompiledIssFile, $IssText, $Utf8Bom)
    $LanguageText = [IO.File]::ReadAllText($LanguageFile, [Text.Encoding]::UTF8)
    [IO.File]::WriteAllText($CompiledLanguageFile, $LanguageText, $Utf8Bom)

    $IsccCandidates = @(
        (Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 6\ISCC.exe'),
        (Join-Path $env:ProgramFiles 'Inno Setup 6\ISCC.exe')
    )
    $IsccCommand = Get-Command ISCC.exe -ErrorAction SilentlyContinue
    if ($IsccCommand) { $IsccCandidates += $IsccCommand.Source }
    $Iscc = $IsccCandidates | Where-Object { $_ -and (Test-Path $_) } | Select-Object -First 1
    if (-not $Iscc) {
        throw 'Inno Setup 6 was not found. Install it from https://jrsoftware.org/isdl.php'
    }
    Write-Host "Inno Setup compiler: $Iscc"

    & $Iscc "/DMyAppVersion=$Version" "/DSourceDir=$BundleDir" "/DOutputDir=$OutputDir" "/DIconFile=$IconFile" "/DLanguageFile=$CompiledLanguageFile" $CompiledIssFile
    if ($LASTEXITCODE -ne 0) { throw 'Inno Setup compilation failed.' }

    $Installer = Join-Path $OutputDir "StockAnalyzer-Setup-$Version-x64.exe"
    if (-not (Test-Path $Installer)) { throw "Installer output was not found: $Installer" }

    $InstallSmokeDir = Join-Path $BuildRoot 'installed-smoke'
    $InstallSmokeHome = Join-Path $BuildRoot 'installed-smoke-home'
    if (Test-Path $InstallSmokeDir) { Remove-Item -Recurse -Force $InstallSmokeDir }
    if (Test-Path $InstallSmokeHome) { Remove-Item -Recurse -Force $InstallSmokeHome }
    $PreviousAppHome = [Environment]::GetEnvironmentVariable('STOCK_APP_HOME')
    try {
        $InstallArgs = "/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP- /DIR=`"$InstallSmokeDir`""
        $InstallProcess = Start-Process -FilePath $Installer -ArgumentList $InstallArgs -Wait -PassThru
        if ($InstallProcess.ExitCode -ne 0) {
            throw "Silent installer smoke test failed with exit code $($InstallProcess.ExitCode)."
        }

        $InstalledExe = Join-Path $InstallSmokeDir 'StockAnalyzer.exe'
        if (-not (Test-Path $InstalledExe)) {
            throw "Installed executable was not found: $InstalledExe"
        }
        $env:STOCK_APP_HOME = $InstallSmokeHome
        $InstalledSmoke = Start-Process -FilePath $InstalledExe -ArgumentList '--smoke-test' -Wait -PassThru
        if ($InstalledSmoke.ExitCode -ne 0) {
            throw "Installed application smoke test failed with exit code $($InstalledSmoke.ExitCode)."
        }

        $Uninstaller = Get-ChildItem $InstallSmokeDir -Filter 'unins*.exe' -File | Select-Object -First 1
        if (-not $Uninstaller) {
            throw 'Silent installer smoke test did not create an uninstaller.'
        }
        $UninstallProcess = Start-Process -FilePath $Uninstaller.FullName -ArgumentList '/VERYSILENT /SUPPRESSMSGBOXES /NORESTART' -Wait -PassThru
        if ($UninstallProcess.ExitCode -ne 0) {
            throw "Silent uninstaller smoke test failed with exit code $($UninstallProcess.ExitCode)."
        }
        if (Test-Path $InstalledExe) {
            throw 'Silent uninstaller smoke test left the installed executable behind.'
        }
    }
    finally {
        $FallbackUninstaller = Get-ChildItem $InstallSmokeDir -Filter 'unins*.exe' -File -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($FallbackUninstaller) {
            Start-Process -FilePath $FallbackUninstaller.FullName -ArgumentList '/VERYSILENT /SUPPRESSMSGBOXES /NORESTART' -Wait | Out-Null
        }
        if ($null -eq $PreviousAppHome) {
            Remove-Item Env:STOCK_APP_HOME -ErrorAction SilentlyContinue
        }
        else {
            $env:STOCK_APP_HOME = $PreviousAppHome
        }
        if (Test-Path $InstallSmokeDir) { Remove-Item -Recurse -Force $InstallSmokeDir }
    }

    $Hash = (Get-FileHash -Algorithm SHA256 $Installer).Hash.ToLowerInvariant()
    $HashFile = "$Installer.sha256"
    Set-Content -Path $HashFile -Encoding Ascii -Value "$Hash  $([IO.Path]::GetFileName($Installer))"

    Write-Host ''
    Write-Host "Installer: $Installer" -ForegroundColor Green
    Write-Host "Checksum: $HashFile" -ForegroundColor Green
}
finally {
    Pop-Location
}
