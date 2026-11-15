; TailConnect Windows installer (Inno Setup 6). Built by scripts/build-windows.sh:
;   ISCC /DAppVersion=0.2.0 /DBuildDir=<dir with exes + icon> /DOutDir=<dist> tailconnect.iss
; Per-user install: no administrator rights needed.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif

[Setup]
AppId={{6F2C4A1E-3B7D-4E59-9A0C-2D8E5B1F7C43}
AppName=TailConnect
AppVersion={#AppVersion}
AppVerName=TailConnect {#AppVersion}
AppPublisher=TailConnect contributors
AppPublisherURL=https://github.com/imbikramsaha/TailConnect
AppSupportURL=https://github.com/imbikramsaha/TailConnect/issues
DefaultDirName={localappdata}\Programs\TailConnect
DisableProgramGroupPage=yes
DisableDirPage=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir={#OutDir}
OutputBaseFilename=TailConnect-{#AppVersion}-windows-setup
SetupIconFile={#BuildDir}\tailconnect.ico
UninstallDisplayIcon={app}\TailConnectApp.exe
UninstallDisplayName=TailConnect
LicenseFile=..\..\LICENSE
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
CloseApplications=force
RestartApplications=yes
ChangesEnvironment=yes
MinVersion=10.0.17763

[Tasks]
Name: "startup"; Description: "Start TailConnect when I sign in"

[Files]
Source: "{#BuildDir}\TailConnectApp.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#BuildDir}\tailconnect.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\LICENSE"; DestDir: "{app}"; DestName: "LICENSE.txt"

[Icons]
Name: "{autoprograms}\TailConnect"; Filename: "{app}\TailConnectApp.exe"

[Registry]
; Same value the app's own "Start at login" setting writes.
; Not re-applied by silent updates (the user may have turned it off in the app).
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "TailConnect"; ValueData: """{app}\TailConnectApp.exe"" --background"; Tasks: startup; Flags: uninsdeletevalue; Check: not WizardSilent

[Run]
Filename: "{app}\TailConnectApp.exe"; Parameters: "--background"; Description: "Open TailConnect"; Flags: nowait postinstall

[UninstallRun]
Filename: "{app}\tailconnect.exe"; Parameters: "quit"; Flags: runhidden; RunOnceId: "QuitTailConnect"
; Remove paired devices' keys from authorized_keys and the ~/.ssh/config Include.
Filename: "{app}\tailconnect.exe"; Parameters: "cleanup"; Flags: runhidden; RunOnceId: "CleanupTailConnect"

[Code]
const EnvKey = 'Environment';

function PathHas(Dir: string): Boolean;
var Paths: string;
begin
  if not RegQueryStringValue(HKCU, EnvKey, 'Path', Paths) then
    Paths := '';
  Result := Pos(';' + Uppercase(Dir) + ';', ';' + Uppercase(Paths) + ';') > 0;
end;

procedure CurStepChanged(CurStep: TSetupStep);
var Paths, Dir: string;
begin
  // Put tailconnect.exe on the user's PATH for the command line and ssh.
  if CurStep = ssPostInstall then begin
    Dir := ExpandConstant('{app}');
    if not PathHas(Dir) then begin
      if not RegQueryStringValue(HKCU, EnvKey, 'Path', Paths) then Paths := '';
      if (Paths <> '') and (Copy(Paths, Length(Paths), 1) <> ';') then Paths := Paths + ';';
      RegWriteExpandStringValue(HKCU, EnvKey, 'Path', Paths + Dir);
    end;
  end;
end;

// Remove exactly our directory (with or without a trailing backslash) from
// the user's Path, leaving every other entry untouched.
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var Paths, Dir, Entry, Kept: string; P: Integer; Changed: Boolean;
begin
  if CurUninstallStep <> usPostUninstall then exit;
  Dir := Uppercase(RemoveBackslashUnlessRoot(ExpandConstant('{app}')));
  if not RegQueryStringValue(HKCU, EnvKey, 'Path', Paths) then exit;
  Kept := '';
  Changed := False;
  while Paths <> '' do begin
    P := Pos(';', Paths);
    if P = 0 then begin
      Entry := Paths;
      Paths := '';
    end else begin
      Entry := Copy(Paths, 1, P - 1);
      Delete(Paths, 1, P);
    end;
    if Uppercase(RemoveBackslashUnlessRoot(Trim(Entry))) = Dir then
      Changed := True
    else if Entry <> '' then begin
      if Kept <> '' then Kept := Kept + ';';
      Kept := Kept + Entry;
    end;
  end;
  if Changed then RegWriteExpandStringValue(HKCU, EnvKey, 'Path', Kept);
end;
