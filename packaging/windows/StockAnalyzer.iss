#ifndef MyAppVersion
  #define MyAppVersion "0.2.0"
#endif
#ifndef SourceDir
  #define SourceDir "..\..\build\windows\StockAnalyzer"
#endif
#ifndef OutputDir
  #define OutputDir "..\..\dist\windows"
#endif
#ifndef IconFile
  #define IconFile "stock-analyzer.ico"
#endif
#ifndef LanguageFile
  #define LanguageFile "languages\ChineseSimplified.isl"
#endif

#define MyAppName "股票持仓分析"
; Go 后端单文件：内置 static 前端由 --open-browser 启动后打开浏览器
#define MyAppExeName "stockanalyzer-server.exe"

[Setup]
AppId={{9E56C8BC-7C7D-4890-A21D-21649A12F1C8}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher=StockAnalyzer
DefaultDirName={localappdata}\Programs\StockAnalyzer
DefaultGroupName=股票持仓分析
DisableProgramGroupPage=yes
DisableReadyMemo=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir={#OutputDir}
OutputBaseFilename=StockAnalyzer-Setup-{#MyAppVersion}-x64
SetupIconFile={#IconFile}
UninstallDisplayIcon={app}\{#MyAppExeName}
Compression=lzma2/ultra64
SolidCompression=yes
WizardStyle=modern
CloseApplications=yes
RestartApplications=no

[Languages]
Name: "chinesesimp"; MessagesFile: "{#LanguageFile}"

[Files]
Source: "{#SourceDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{autoprograms}\股票持仓分析"; Filename: "{app}\{#MyAppExeName}"; Parameters: "--open-browser"; WorkingDir: "{app}"
Name: "{autodesktop}\股票持仓分析"; Filename: "{app}\{#MyAppExeName}"; Parameters: "--open-browser"; WorkingDir: "{app}"

[Run]
Filename: "{app}\{#MyAppExeName}"; Parameters: "--open-browser"; Description: "立即启动股票持仓分析"; Flags: nowait postinstall skipifsilent

[Code]
function ShouldAutoLaunch(): Boolean;
begin
  Result := False;
end;
