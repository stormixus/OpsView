[Setup]
AppName=OpsView Agent
AppVersion={#MyAppVersion}
DefaultDirName={autopf}\OpsView Agent
DefaultGroupName=OpsView Agent
OutputDir=.
OutputBaseFilename=opsview-agent-setup
Compression=lzma2
SolidCompression=yes
PrivilegesRequired=lowest
UninstallDisplayIcon={app}\opsview-agent.exe
SetupIconFile=tray.ico
WizardStyle=modern
; Silent auto-update: close the running agent (restart manager) so its .exe can be
; replaced, and don't let Setup restart it — we relaunch it ourselves (see [Run]).
CloseApplications=yes
RestartApplications=no

[Files]
Source: "opsview-agent.exe"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\OpsView Agent"; Filename: "{app}\opsview-agent.exe"
Name: "{group}\Uninstall OpsView Agent"; Filename: "{uninstallexe}"

[Tasks]
Name: "autostart"; Description: "Windows 시작 시 자동 실행"; GroupDescription: "추가 옵션:"

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "OpsViewAgent"; ValueData: """{app}\opsview-agent.exe"""; Flags: uninsdeletevalue; Tasks: autostart

[Run]
; Interactive install: offer to launch (skipped during silent auto-update).
Filename: "{app}\opsview-agent.exe"; Description: "OpsView Agent 실행"; Flags: nowait postinstall skipifsilent
; Silent auto-update: always relaunch the agent after replacing it.
Filename: "{app}\opsview-agent.exe"; Flags: nowait; Check: IsSilentInstall

[Code]
function IsSilentInstall: Boolean;
begin
  Result := WizardSilent;
end;

[UninstallRun]
Filename: "taskkill"; Parameters: "/F /IM opsview-agent.exe"; Flags: runhidden; RunOnceId: "KillAgent"

[UninstallDelete]
Type: filesandordirs; Name: "{app}"
