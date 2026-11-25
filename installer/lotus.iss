; Windows installer for LOTUS, built with Inno Setup 6 (https://jrsoftware.org/isinfo.php).
; Build it with scripts\build-installer.ps1, which compiles lotus.exe first.
;
; What it does:
;   - installs lotus.exe and the examples under C:\Program Files\Lotus
;   - creates the configuration C:\Program Files\Lotus\main.conf and the web
;     directory C:\www with a sample page, but never replaces them on an upgrade
;     and never deletes them on uninstall
;   - registers the "LOTUS" Windows service (start at boot) and starts it
;   - adds Start Menu entries: edit the configuration, start / stop / restart
;   - upgrades stop the running service first; uninstalling removes the service

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceExe
  #define SourceExe "..\bin\lotus.exe"
#endif

[Setup]
AppId={{6F1C2C0E-6B1E-4E42-9B6F-1D2E0A6C9A11}
AppName=LOTUS
AppVersion={#AppVersion}
AppPublisher=LOTUS
DefaultDirName={autopf}\Lotus
DefaultGroupName=LOTUS
DisableProgramGroupPage=yes
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir=..\dist
OutputBaseFilename=lotus-{#AppVersion}-windows-amd64-setup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
LicenseFile=..\LICENSE
UninstallDisplayIcon={app}\lotus.exe
; The logo: put a Windows icon (.ico with 16, 32, 48 and 256 pixel sizes) at
; installer\lotus.ico. Without it the installer is built with Inno's default icon.
#ifexist "lotus.ico"
SetupIconFile=lotus.ico
#endif
; The service holds lotus.exe open; stop it before files are replaced.
CloseApplications=no
; Tell Windows the PATH changed, so terminals opened after the install find "lotus".
ChangesEnvironment=yes

[Tasks]
Name: "addtopath"; Description: "Add LOTUS to the system PATH (so that ""lotus"" works in any terminal)"

[Registry]
Root: HKLM; Subkey: "SYSTEM\CurrentControlSet\Control\Session Manager\Environment"; ValueType: expandsz; ValueName: "Path"; ValueData: "{olddata};{app}"; Tasks: addtopath; Check: NeedsPath(ExpandConstant('{app}'))

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\configs\examples\*"; DestDir: "{app}\examples"; Flags: ignoreversion recursesubdirs
Source: "main.conf"; DestDir: "{app}"; Flags: onlyifdoesntexist uninsneveruninstall
; The web directory is C:\www; it is never replaced and never deleted.
Source: "index.html"; DestDir: "C:\www"; Flags: onlyifdoesntexist uninsneveruninstall

[Icons]
Name: "{group}\Edit LOTUS configuration"; Filename: "{win}\notepad.exe"; Parameters: """{app}\main.conf"""
; No backslash in a shortcut's name after {group}\: Setup reads each one as a folder (that made "Open LOTUS site folder (C:" and error 123).
Name: "{group}\Open LOTUS site folder"; Filename: "C:\www"
Name: "{group}\Open LOTUS admin"; Filename: "{sys}\cmd.exe"; Parameters: "/c start """" http://127.0.0.1:7080/"; Flags: runminimized
Name: "{group}\Open LOTUS log"; Filename: "{win}\notepad.exe"; Parameters: """{app}\logs\lotus.log"""
Name: "{group}\Start LOTUS"; Filename: "{sys}\WindowsPowerShell\v1.0\powershell.exe"; Parameters: "-NoProfile -Command ""Start-Service LOTUS"""; Flags: runminimized
Name: "{group}\Stop LOTUS"; Filename: "{sys}\WindowsPowerShell\v1.0\powershell.exe"; Parameters: "-NoProfile -Command ""Stop-Service LOTUS"""; Flags: runminimized
Name: "{group}\Restart LOTUS"; Filename: "{sys}\WindowsPowerShell\v1.0\powershell.exe"; Parameters: "-NoProfile -Command ""Restart-Service LOTUS"""; Flags: runminimized
Name: "{group}\Uninstall LOTUS"; Filename: "{uninstallexe}"

[Run]
Filename: "{app}\lotus.exe"; Parameters: "config validate ""{app}\main.conf"""; Flags: runhidden waituntilterminated; StatusMsg: "Checking the configuration..."
Filename: "{app}\lotus.exe"; Parameters: "service install --config ""{app}\main.conf"""; Flags: runhidden waituntilterminated; StatusMsg: "Registering the LOTUS service..."
Filename: "{sys}\sc.exe"; Parameters: "start LOTUS"; Flags: runhidden waituntilterminated; StatusMsg: "Starting LOTUS..."
Filename: "http://127.0.0.1:7080/"; Description: "Open the LOTUS admin in the browser"; Flags: postinstall shellexec skipifsilent nowait
Filename: "http://127.0.0.1:8080/"; Description: "Open the sample site in the browser"; Flags: postinstall shellexec skipifsilent nowait unchecked

[UninstallRun]
Filename: "{sys}\sc.exe"; Parameters: "stop LOTUS"; Flags: runhidden waituntilterminated; RunOnceId: "StopLotus"
Filename: "{app}\lotus.exe"; Parameters: "service uninstall"; Flags: runhidden waituntilterminated; RunOnceId: "RemoveLotusService"

[Code]
var
  AdminPage: TInputQueryWizardPage;

// True if dir is not yet one of the entries of the system PATH.
function NeedsPath(dir: string): Boolean;
var
  paths: string;
begin
  if not RegQueryStringValue(HKLM, 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', paths) then
    paths := '';
  Result := Pos(';' + Lowercase(dir) + ';', ';' + Lowercase(paths) + ';') = 0;
end;

// On uninstall, take the folder out of the system PATH again.
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  paths, dir: string;
  p: Integer;
begin
  if CurUninstallStep = usPostUninstall then begin
    dir := ExpandConstant('{app}');
    if RegQueryStringValue(HKLM, 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', paths) then begin
      p := Pos(';' + Lowercase(dir), Lowercase(paths));
      if p > 0 then begin
        Delete(paths, p, Length(dir) + 1);
        RegWriteExpandStringValue(HKLM, 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', paths);
      end;
    end;
  end;
end;

// Before files are copied: stop and remove a service from an earlier install, so
// lotus.exe is not in use and "service install" can register it again.
procedure CurStepChanged(CurStep: TSetupStep);
var
  rc: Integer;
  pwFile: string;
begin
  if CurStep = ssInstall then begin
    Exec(ExpandConstant('{sys}\sc.exe'), 'stop LOTUS', '', SW_HIDE, ewWaitUntilTerminated, rc);
    Sleep(2000);
    Exec(ExpandConstant('{sys}\sc.exe'), 'delete LOTUS', '', SW_HIDE, ewWaitUntilTerminated, rc);
  end;
  // Make the first admin account. The password goes to the program in a file that is deleted at once.
  if (CurStep = ssPostInstall) and (AdminPage <> nil) and (AdminPage.Values[1] <> '') then begin
    pwFile := ExpandConstant('{tmp}\lotus-admin-pw.txt');
    if SaveStringToFile(pwFile, AdminPage.Values[1] + #13#10, False) then begin
      Exec(ExpandConstant('{app}\lotus.exe'),
        'admin passwd "' + AdminPage.Values[0] + '" --password-file "' + pwFile + '" --config "' + ExpandConstant('{app}\main.conf') + '"',
        '', SW_HIDE, ewWaitUntilTerminated, rc);
      DeleteFile(pwFile);
      // The accounts file holds password hashes: only SYSTEM and Administrators may read it.
      Exec(ExpandConstant('{sys}\icacls.exe'),
        '"' + ExpandConstant('{app}\admin-users.txt') + '" /inheritance:r /grant:r *S-1-5-18:F *S-1-5-32-544:F',
        '', SW_HIDE, ewWaitUntilTerminated, rc);
    end;
  end;
end;

// The page that asks for the first admin account. It is left out when the admin already has accounts.
procedure InitializeWizard();
begin
  // {app} is not known yet here, so look in the default install folder.
  if not FileExists(ExpandConstant('{autopf}\Lotus\admin-users.txt')) then begin
    AdminPage := CreateInputQueryPage(wpSelectTasks, 'Web admin account',
      'Choose how you sign in to the LOTUS admin',
      'The admin is at http://127.0.0.1:7080/ and only works on this computer. Leave the password empty to make an account later with "lotus admin passwd NAME".');
    AdminPage.Add('Name:', False);
    AdminPage.Add('Password (at least 10 characters):', True);
    AdminPage.Add('Password again:', True);
    AdminPage.Values[0] := 'admin';
  end;
end;

function NextButtonClick(CurPageID: Integer): Boolean;
begin
  Result := True;
  if (AdminPage <> nil) and (CurPageID = AdminPage.ID) and (AdminPage.Values[1] <> '') then begin
    if Length(AdminPage.Values[1]) < 10 then begin
      MsgBox('The password must be at least 10 characters.', mbError, MB_OK);
      Result := False;
    end else if AdminPage.Values[1] <> AdminPage.Values[2] then begin
      MsgBox('The two passwords are not the same.', mbError, MB_OK);
      Result := False;
    end else if Length(AdminPage.Values[0]) = 0 then begin
      MsgBox('Type a name for the account.', mbError, MB_OK);
      Result := False;
    end;
  end;
end;
