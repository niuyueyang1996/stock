#ifndef MyAppVersion
  #define MyAppVersion "0.2.0"
#endif
#ifndef SourceDir
  #define SourceDir "..\..\build\windows\dist\StockAnalyzer"
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
#define MyAppExeName "StockAnalyzer.exe"

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
Name: "{autoprograms}\股票持仓分析"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"
Name: "{autodesktop}\股票持仓分析"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"

[Run]
; 去掉 skipifsilent：静默安装（自动更新）完成后也自动启动新版本，更新即生效无需手动打开
; CI 冒烟测试静默安装带 /NOAUTOLAUNCH 跳过自动启动，避免无交互会话里拉起长驻托盘进程导致安装器不返回
Filename: "{app}\{#MyAppExeName}"; Description: "立即启动股票持仓分析"; Flags: nowait postinstall; Check: ShouldAutoLaunch

[Code]
function ShouldAutoLaunch(): Boolean;
begin
  ; Inno 6 移除了 CmdLineParamExists，用 FindCmdLineSwitch（Inno 5/6 兼容）。
  ; CompareCase=True 大小写不敏感，ComparePrefix=True 接受 / 或 - 前缀。
  Result := not FindCmdLineSwitch('NOAUTOLAUNCH', True, True);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ResultCode: Integer;
  OldExe: String;
begin
  Result := '';
  OldExe := ExpandConstant('{app}\{#MyAppExeName}');
  if FileExists(OldExe) then
  begin
    if not Exec(OldExe, '--shutdown', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
      Result := '无法关闭正在运行的股票持仓分析，请从系统托盘退出后重试。'
    else if ResultCode <> 0 then
      Result := '股票持仓分析仍在运行，请从系统托盘退出后重试。';
  end;
end;

function InitializeUninstall(): Boolean;
var
  ResultCode: Integer;
  InstalledExe: String;
begin
  Result := True;
  InstalledExe := ExpandConstant('{app}\{#MyAppExeName}');
  if FileExists(InstalledExe) then
  begin
    if (not Exec(InstalledExe, '--shutdown', '', SW_HIDE, ewWaitUntilTerminated, ResultCode)) or
       (ResultCode <> 0) then
    begin
      if not UninstallSilent then
        MsgBox('无法关闭正在运行的股票持仓分析，请从系统托盘退出后重试。',
          mbError, MB_OK);
      Result := False;
    end;
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  DataDir: String;
begin
  if CurUninstallStep = usPostUninstall then
  begin
    DataDir := ExpandConstant('{localappdata}\StockAnalyzer');
    if (not UninstallSilent) and DirExists(DataDir) and
       (MsgBox('是否同时删除个人数据？' + #13#10 + #13#10 +
         '其中包含持仓、交易、AI 配置、缓存、数据库备份和日志。' + #13#10 +
         '选择“否”可在以后重新安装时继续使用。',
         mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES) then
      DelTree(DataDir, True, True, True);
  end;
end;
