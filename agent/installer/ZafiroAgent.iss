; Установщик нового агента Zafiro для ПК клуба (Inno Setup 6, версия закреплена в .github/workflows/agent-release.yml).
; Собирается в CI (windows-latest):
;   ISCC.exe /DAppVersion=1.2.3 /DSourceDir=<папка сборки> ZafiroAgent.iss
; <папка сборки>: Zafiro.Agent.Service.exe (Native AOT, служба Windows под SYSTEM), appsettings.json, root-ca.pem
; (корень CA Zafiro для pinning, AG-3), Zafiro.Agent.UI.exe + Frontend\ (WPF + WebView2, self-contained).
; Перенесено из старого агента: WindowsAgent/installer/ZafiroAgent.iss (страницы обновления, задачи, защита учётной
; записи клиента Protect-ClubPc.ps1). Отличия: служба — отдельный exe (UI запускает служба, когда нужен экран),
; ключ образа бездисковых ПК — в защищённое хранилище службы (DPAPI, AG-10), а не в appsettings.json.
;
; Тихая установка (обновление агентом): /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP- /MERGETASKS="!protect"
; Бездисковый образ (режим правки образа на супер-рабочей станции): /IMAGEKEYFILE=<файл с ключом образа zik_…>

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceDir
  #define SourceDir "..\.work\publish"
#endif

[Setup]
AppId={{3B1F7C2A-8E64-4D0B-9A57-6C2E9F41D7A8}
AppName=Zafiro Agent
AppVersion={#AppVersion}
AppPublisher=Zafiro
AppPublisherURL=https://zafiro.kz
DefaultDirName={autopf}\Zafiro Agent
DisableDirPage=yes
DisableProgramGroupPage=yes
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0.17763
OutputDir=..\.work\installer
OutputBaseFilename=ZafiroAgentSetup-{#AppVersion}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
SetupIconFile=..\src\Zafiro.Agent.UI\Assets\zafiro.ico
UninstallDisplayIcon={app}\Zafiro.Agent.UI.exe
UninstallDisplayName=Zafiro Agent
CloseApplications=no
SetupLogging=yes

[Languages]
Name: "ru"; MessagesFile: "compiler:Languages\Russian.isl"

[Tasks]
Name: "protect"; Description: "Защитить существующую учётную запись клиента ({param:CLIENTUSER|Client}): без прав администратора, без диспетчера задач, реестра, командной строки и PowerShell. Новая учётная запись не создаётся"
Name: "desktopicon"; Description: "Значок Zafiro на рабочем столе (лаунчер во время сессии)"
Name: "adminautostart"; Description: "Показывать экран агента и под администратором (если ПК клуба входит в Windows под учётной записью администратора). Выйти для обслуживания — Ctrl+Alt+P и PIN-код"; Flags: unchecked

[Files]
Source: "{#SourceDir}\*"; DestDir: "{app}"; Excludes: "appsettings.json"; Flags: ignoreversion recursesubdirs createallsubdirs
; Настройки службы — только при первой установке: адрес сервера и пр. правит администратор, обновление их не затирает.
Source: "{#SourceDir}\appsettings.json"; DestDir: "{app}"; Flags: onlyifdoesntexist uninsneveruninstall
Source: "Protect-ClubPc.ps1"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
; Ярлык открывает лаунчер во время сессии (UI запускается, показывает лаунчер, закрывается кнопкой «На рабочий стол»).
Name: "{commondesktop}\Zafiro"; Filename: "{app}\Zafiro.Agent.UI.exe"; Parameters: "--launcher"; WorkingDir: "{app}"; Tasks: desktopicon
Name: "{autoprograms}\Zafiro"; Filename: "{app}\Zafiro.Agent.UI.exe"; Parameters: "--launcher"; WorkingDir: "{app}"

[Run]
; Служба создаётся при первой установке; при обновлении — только перенастраивается и запускается снова.
Filename: "{sys}\sc.exe"; Parameters: "create ZafiroAgent binPath= ""\""{app}\Zafiro.Agent.Service.exe\"""" start= auto obj= LocalSystem DisplayName= ""Zafiro Agent"""; Flags: runhidden; StatusMsg: "Регистрация службы Zafiro Agent..."
Filename: "{sys}\sc.exe"; Parameters: "config ZafiroAgent binPath= ""\""{app}\Zafiro.Agent.Service.exe\"""" start= auto obj= LocalSystem"; Flags: runhidden
Filename: "{sys}\sc.exe"; Parameters: "description ZafiroAgent ""Связь ПК клуба с Zafiro: сессии, команды, экран блокировки."""; Flags: runhidden
Filename: "{sys}\sc.exe"; Parameters: "failure ZafiroAgent reset= 86400 actions= restart/5000/restart/5000/restart/5000"; Flags: runhidden
Filename: "{sys}\sc.exe"; Parameters: "start ZafiroAgent"; Flags: runhidden; StatusMsg: "Запуск службы Zafiro Agent..."
Filename: "{sys}\WindowsPowerShell\v1.0\powershell.exe"; Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\Protect-ClubPc.ps1"" -ClientUser ""{param:CLIENTUSER|Client}"""; Tasks: protect; Flags: waituntilterminated runhidden; StatusMsg: "Защита учётной записи клиента..."

[UninstallRun]
; net stop ждёт, пока служба остановится (sc stop возвращается сразу).
Filename: "{sys}\net.exe"; Parameters: "stop ZafiroAgent"; Flags: runhidden; RunOnceId: "StopService"
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM Zafiro.Agent.UI.exe"; Flags: runhidden; RunOnceId: "StopUi"
Filename: "{sys}\sc.exe"; Parameters: "delete ZafiroAgent"; Flags: runhidden; RunOnceId: "DeleteService"

[UninstallDelete]
; Ключ и сертификат ПК, журналы команд и событий, ключ образа: после удаления ПК регистрируется заново.
Type: filesandordirs; Name: "{commonappdata}\Zafiro\Agent"
Type: files; Name: "{app}\appsettings.Production.json"

[Code]
const
  UninstallKey = 'SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{3B1F7C2A-8E64-4D0B-9A57-6C2E9F41D7A8}_is1';

var
  // Версия, которая уже стоит на ПК; пусто — первая установка.
  InstalledVersion: String;

function InitializeSetup(): Boolean;
begin
  InstalledVersion := '';
  RegQueryStringValue(HKLM, UninstallKey, 'DisplayVersion', InstalledVersion);
  Result := True;
end;

function IsUpdate(): Boolean;
begin
  Result := InstalledVersion <> '';
end;

// Задача уже выбрана или снята при прошлой установке (Inno Setup хранит списки в записи об установке).
function TaskDecided(const Name: String): Boolean;
var
  Selected, Deselected: String;
begin
  Selected := '';
  Deselected := '';
  RegQueryStringValue(HKLM, UninstallKey, 'Inno Setup: Selected Tasks', Selected);
  RegQueryStringValue(HKLM, UninstallKey, 'Inno Setup: Deselected Tasks', Deselected);
  Result := (Pos(',' + Name + ',', ',' + Lowercase(Selected) + ',') > 0)
    or (Pos(',' + Name + ',', ',' + Lowercase(Deselected) + ',') > 0);
end;

// Обновление: задачи берутся из прошлой установки, сразу страница с кнопкой «Обновить».
function ShouldSkipPage(PageID: Integer): Boolean;
begin
  Result := IsUpdate() and (PageID = wpSelectTasks)
    and TaskDecided('protect') and TaskDecided('desktopicon') and TaskDecided('adminautostart');
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if (CurPageID <> wpReady) or not IsUpdate() then Exit;
  if InstalledVersion = '{#AppVersion}' then
  begin
    WizardForm.PageNameLabel.Caption := 'Переустановка Zafiro Agent';
    WizardForm.PageDescriptionLabel.Caption := 'Эта версия уже установлена.';
    WizardForm.ReadyLabel.Caption := 'Установлена версия {#AppVersion}. Нажмите «Переустановить», чтобы заменить файлы агента. Регистрация ПК в клубе сохранится.';
    WizardForm.NextButton.Caption := 'Переустановить';
  end
  else
  begin
    WizardForm.PageNameLabel.Caption := 'Обновление Zafiro Agent';
    WizardForm.PageDescriptionLabel.Caption := 'Агент уже установлен на этом ПК.';
    WizardForm.ReadyLabel.Caption := 'Установлена версия ' + InstalledVersion + ', будет установлена {#AppVersion}. Нажмите «Обновить». Регистрация ПК в клубе сохранится, служба запустится снова.';
    WizardForm.NextButton.Caption := 'Обновить';
  end;
end;

// Перед заменой файлов: служба и UI остановлены (иначе exe заняты, а служба сразу запустила бы старый UI).
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Code: Integer;
begin
  Exec(ExpandConstant('{sys}\net.exe'), 'stop ZafiroAgent', '', SW_HIDE, ewWaitUntilTerminated, Code);
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /IM Zafiro.Agent.UI.exe', '', SW_HIDE, ewWaitUntilTerminated, Code);
  Sleep(500);
  Result := '';
end;

// Настройки установщика для службы — appsettings.Production.json (поверх appsettings.json; окружение службы —
// Production): показ экрана под администраторами (галочка adminautostart), бездисковый ПК.
procedure WriteProductionSettings(Diskless: Boolean);
var
  Json, AdminFlag, DisklessFlag: String;
begin
  AdminFlag := 'false';
  if WizardIsTaskSelected('adminautostart') then AdminFlag := 'true';
  DisklessFlag := 'false';
  if Diskless then DisklessFlag := 'true';
  Json := '{' + #13#10 +
    '  "Agent": { "Diskless": ' + DisklessFlag + ' },' + #13#10 +
    '  "Service": { "LaunchUiForAdministrators": ' + AdminFlag + ' }' + #13#10 +
    '}' + #13#10;
  SaveStringToFile(ExpandConstant('{app}\appsettings.Production.json'), Json, False);
end;

// AG-10: ключ образа бездискового ПК (/IMAGEKEYFILE=<файл>) — не в командной строке и не в appsettings.json:
// служба сохраняет его в своё хранилище (DPAPI LocalMachine, папка только для SYSTEM и администраторов),
// читая ключ из stdin (Zafiro.Agent.Service.exe --set-image-key). Файл с ключом — забота администратора образа.
function ImportImageKey(): Boolean;
var
  KeyFile, Cmd: String;
  Code: Integer;
begin
  Result := False;
  KeyFile := ExpandConstant('{param:IMAGEKEYFILE|}');
  if KeyFile = '' then Exit;
  if not FileExists(KeyFile) then
  begin
    Log('image key file not found');
    Exit;
  end;
  Cmd := '/C ""' + ExpandConstant('{app}\Zafiro.Agent.Service.exe') + '" --set-image-key < "' + KeyFile + '""';
  if Exec(ExpandConstant('{cmd}'), Cmd, ExpandConstant('{app}'), SW_HIDE, ewWaitUntilTerminated, Code) and (Code = 0) then
    Result := True
  else
    Log('image key import failed: ' + IntToStr(Code));
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  Diskless: Boolean;
begin
  if CurStep <> ssPostInstall then Exit;
  Diskless := ImportImageKey();
  // Обновление без /IMAGEKEYFILE не снимает отметку бездискового ПК, поставленную прежней установкой.
  if (not Diskless) and FileExists(ExpandConstant('{commonappdata}\Zafiro\Agent\image-key.bin')) then Diskless := True;
  WriteProductionSettings(Diskless);
end;
