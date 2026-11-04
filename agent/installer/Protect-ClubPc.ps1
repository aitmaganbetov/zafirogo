<#
.SYNOPSIS
    (Новый агент, agent/installer: перенос WindowsAgent/tools/Protect-ClubPc.ps1 — служба Zafiro.Agent.Service.exe,
    UI Zafiro.Agent.UI.exe; запреты учётной записи клиента — без изменений.)
    Защищает ПК клуба для Zafiro Agent: клиент работает под ограниченной учётной записью
    и не может отключить агент, редактор реестра, командную строку или PowerShell.

.DESCRIPTION
    Запускать от имени администратора, один раз на каждом ПК (и повторно после обновления агента).
    1. Копирует агент в "C:\Program Files\Zafiro Agent": обычный пользователь не может подменить exe.
    2. Проверяет существующую учётную запись клиента (-ClientUser): без прав администратора.
       Учётные записи Windows скрипт не создаёт: на супер-рабочей станции бездисковой системы новая
       учётная запись попала бы в образ всех ПК клуба. Нет такой учётной записи — запреты пропускаются.
    3. Для этой учётной записи запрещает: диспетчер задач, редактор реестра, командную строку и
       скрипты, PowerShell, окно «Выполнить», панель управления, MMC, msconfig, mshta, wscript/cscript,
       а также лазейки мимо агента: Game Bar (кнопка Xbox), окна залипания и фильтрации клавиш
       (Shift ×5 и т. п.), «Ножницы» по PrtScn, а также смену сетевых настроек: свойства подключения,
       IP-адрес, включение и отключение сетевого адаптера. На всём ПК выключает свайпы от края экрана.
    4. Регистрирует службу Windows «Zafiro Agent» (SYSTEM): она хранит токен ПК в папке, недоступной
       клиенту, и запускает агент снова, если клиент его закрыл. Учётные записи администраторов служба
       не трогает.
    С ключом -Undo снимает все запреты и удаляет службу (файлы агента и учётная запись остаются).

    Обычно скрипт запускает установщик ZafiroAgentSetup (галочка «Защитить учётную запись клиента»;
    имя учётной записи — параметр установщика /CLIENTUSER=Имя, по умолчанию Client).
    Windows применяет запреты к программам, запущенным из проводника; агент защищает служба.

.EXAMPLE
    .\Protect-ClubPc.ps1 -AgentPath D:\ZafiroAgent-win-x64-1.0.0
.EXAMPLE
    .\Protect-ClubPc.ps1 -ClientUser Client -Undo
#>
[CmdletBinding()]
param(
    # Папка со сборкой агента (Zafiro.Agent.Service.exe, Zafiro.Agent.UI.exe и папка Frontend).
    [string] $AgentPath,
    # Существующая учётная запись Windows, под которой играют клиенты (скрипт её не создаёт).
    [string] $ClientUser = 'Client',
    [switch] $Undo
)

$ErrorActionPreference = 'Stop'
$InstallDir = Join-Path $env:ProgramFiles 'Zafiro Agent'
$AgentExe = Join-Path $InstallDir 'Zafiro.Agent.Service.exe'
$ServiceName = 'ZafiroAgent'
$BlockedPrograms = @(
    'cmd.exe', 'powershell.exe', 'powershell_ise.exe', 'pwsh.exe', 'regedit.exe', 'regedt32.exe',
    'taskmgr.exe', 'mmc.exe', 'msconfig.exe', 'mshta.exe', 'wscript.exe', 'cscript.exe', 'control.exe',
    'netsh.exe', 'ipconfig.exe'
)

# Политики «Сетевые подключения»: 0 — запрещено, NC_EnableAdminProhibits = 1 — запреты действуют
# и на членов группы «Операторы настройки сети». Клиент не может открыть свойства подключения,
# сменить IP-адрес, отключить или переименовать адаптер.
$NetworkPolicies = [ordered]@{
    'NC_LanProperties' = 0; 'NC_LanChangeProperties' = 0; 'NC_AllowAdvancedTCPIPConfig' = 0; 'NC_LanConnect' = 0
    'NC_RenameConnection' = 0; 'NC_RenameLanConnection' = 0; 'NC_DeleteConnection' = 0; 'NC_NewConnectionWizard' = 0
    'NC_AdvancedSettings' = 0; 'NC_ShowSharedAccessUI' = 0; 'NC_EnableAdminProhibits' = 1
}
$NetworkPolicyKey = 'Software\Policies\Microsoft\Windows\Network Connections'

function Assert-Administrator {
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Запустите скрипт от имени администратора (правой кнопкой → «Запуск от имени администратора»).'
    }
}

function Install-Agent {
    if (-not $AgentPath) { return }
    $source = Join-Path $AgentPath 'Zafiro.Agent.Service.exe'
    if (-not (Test-Path $source) -or -not (Test-Path (Join-Path $AgentPath 'Zafiro.Agent.UI.exe')) -or -not (Test-Path (Join-Path $AgentPath 'Frontend\index.html'))) {
        throw "В папке $AgentPath нет Zafiro.Agent.Service.exe, Zafiro.Agent.UI.exe и Frontend\index.html. Укажите папку сборки агента."
    }

    Stop-Service $ServiceName -ErrorAction SilentlyContinue # иначе служба сразу запустит старый UI
    Get-Process Zafiro.Agent.UI -ErrorAction SilentlyContinue | Stop-Process -Force
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    Copy-Item -Path (Join-Path $AgentPath '*') -Destination $InstallDir -Recurse -Force
    Write-Host "Агент установлен: $AgentExe"
}

function Install-AgentService {
    if (-not (Test-Path $AgentExe)) {
        Write-Warning "Агент не найден в ${InstallDir}: укажите -AgentPath, чтобы установить его и службу."
        return
    }

    if (-not (Get-Service $ServiceName -ErrorAction SilentlyContinue)) {
        New-Service -Name $ServiceName -BinaryPathName "`"$AgentExe`"" -DisplayName 'Zafiro Agent' -StartupType Automatic `
            -Description 'Связь ПК клуба с Zafiro: сессии, команды, экран блокировки.' | Out-Null
        & sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/5000/restart/5000 | Out-Null
        Write-Host 'Служба Zafiro Agent установлена.'
    }

    # Запреты ниже нужны и без службы: её сбой не должен оставить учётную запись открытой.
    try {
        Start-Service $ServiceName
    }
    catch {
        Write-Warning "Служба Zafiro Agent не запустилась: $($_.Exception.Message). Проверьте журнал событий Windows."
    }
}

function Remove-AgentService {
    if (-not (Get-Service $ServiceName -ErrorAction SilentlyContinue)) { return }
    Stop-Service $ServiceName -Force -ErrorAction SilentlyContinue
    & sc.exe delete $ServiceName | Out-Null
    Write-Host 'Служба Zafiro Agent удалена.'
}

# Учётная запись клиента должна уже быть на ПК: скрипт её не создаёт (в бездисковой системе она
# попала бы в образ всех ПК клуба). Служба агента работает от SYSTEM и без неё.
function Assert-ClientUser {
    $user = Get-LocalUser -Name $ClientUser -ErrorAction SilentlyContinue
    if (-not $user) {
        $existing = (Get-LocalUser | Where-Object { $_.Enabled } | ForEach-Object { $_.Name }) -join ', '
        Write-Warning "Учётной записи $ClientUser нет: запреты не применены, новая учётная запись не создаётся. Служба Zafiro Agent работает. Укажите существующую учётную запись клиента: -ClientUser <имя> (на этом ПК: $existing)."
        return $false
    }

    $admins = Get-LocalGroup -SID 'S-1-5-32-544'
    if (Get-LocalGroupMember -Group $admins | Where-Object { $_.Name -match "\\$([regex]::Escape($ClientUser))$" }) {
        Remove-LocalGroupMember -Group $admins -Member $ClientUser
        Write-Host "$ClientUser убран из администраторов."
    }

    return $true
}

# Реестр учётной записи клиента: уже загружен, если клиент вошёл в Windows, иначе подгружается из NTUSER.DAT.
function Open-ClientHive {
    $sid = (Get-LocalUser -Name $ClientUser).SID.Value
    if (Test-Path "Registry::HKEY_USERS\$sid") { return @{ Root = "Registry::HKEY_USERS\$sid"; Loaded = $false } }

    $profilePath = (Get-ItemProperty "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\$sid" -ErrorAction SilentlyContinue).ProfileImagePath
    if (-not $profilePath -or -not (Test-Path "$profilePath\NTUSER.DAT")) {
        throw "Профиль $ClientUser ещё не создан. Войдите в эту учётную запись один раз и запустите скрипт снова."
    }

    & reg.exe load "HKU\ZafiroClient" "$profilePath\NTUSER.DAT" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Не удалось открыть реестр учётной записи $ClientUser." }
    return @{ Root = 'Registry::HKEY_USERS\ZafiroClient'; Loaded = $true }
}

function Close-ClientHive($hive) {
    if (-not $hive.Loaded) { return }
    [GC]::Collect()
    [GC]::WaitForPendingFinalizers()
    # Невыгруженный реестр — и клиент войдёт во временный профиль, где запретов нет.
    for ($attempt = 1; $attempt -le 5; $attempt++) {
        & reg.exe unload 'HKU\ZafiroClient' 2>&1 | Out-Null
        if ($LASTEXITCODE -eq 0) { return }
        [GC]::Collect()
        [GC]::WaitForPendingFinalizers()
        Start-Sleep -Seconds 1
    }

    Write-Warning "Реестр учётной записи $ClientUser не выгружен. Перезагрузите ПК, прежде чем клиент войдёт в Windows."
}

function Set-Value($root, $path, $name, $value, $type = 'DWord') {
    $key = Join-Path $root $path
    New-Item -Path $key -Force | Out-Null
    New-ItemProperty -Path $key -Name $name -Value $value -PropertyType $type -Force | Out-Null
}

function Remove-Value($root, $path, $name) {
    $key = Join-Path $root $path
    if (Test-Path $key) { Remove-ItemProperty -Path $key -Name $name -ErrorAction SilentlyContinue }
}

function Protect-Client($root) {
    $system = 'Software\Microsoft\Windows\CurrentVersion\Policies\System'
    $explorer = 'Software\Microsoft\Windows\CurrentVersion\Policies\Explorer'

    Set-Value $root $system 'DisableTaskMgr' 1
    Set-Value $root $system 'DisableRegistryTools' 1
    Set-Value $root 'Software\Policies\Microsoft\Windows\System' 'DisableCMD' 1
    Set-Value $root $explorer 'NoRun' 1
    Set-Value $root $explorer 'NoControlPanel' 1
    Set-Value $root $explorer 'DisallowRun' 1

    $list = Join-Path $root "$explorer\DisallowRun"
    if (Test-Path $list) { Remove-Item $list -Recurse -Force }
    $index = 1
    foreach ($program in $BlockedPrograms) {
        Set-Value $root "$explorer\DisallowRun" "$index" $program 'String'
        $index++
    }

    # Game Bar (кнопка Xbox), окна специальных возможностей по Shift ×5 и т. п., «Ножницы» по PrtScn:
    # они открываются поверх агента и ведут к параметрам Windows.
    Set-Value $root 'Software\Microsoft\Windows\CurrentVersion\GameDVR' 'AppCaptureEnabled' 0
    Set-Value $root 'System\GameConfigStore' 'GameDVR_Enabled' 0
    Set-Value $root 'Software\Microsoft\GameBar' 'UseNexusForGameBarEnabled' 0
    Set-Value $root 'Control Panel\Accessibility\StickyKeys' 'Flags' '506' 'String'
    Set-Value $root 'Control Panel\Accessibility\Keyboard Response' 'Flags' '122' 'String'
    Set-Value $root 'Control Panel\Accessibility\ToggleKeys' 'Flags' '58' 'String'
    Set-Value $root 'Control Panel\Keyboard' 'PrintScreenKeyForSnippingEnabled' 0
    foreach ($policy in $NetworkPolicies.GetEnumerator()) { Set-Value $root $NetworkPolicyKey $policy.Key $policy.Value }

    # Агент запускает служба; своя запись автозапуска дала бы второй запуск.
    Remove-Value $root 'Software\Microsoft\Windows\CurrentVersion\Run' 'ZafiroAgent'
}

# Свайпы от края сенсорного экрана (центр уведомлений, переключение окон) — для всего ПК.
function Set-EdgeSwipe([bool] $allowed) {
    $key = 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\EdgeUI'
    if ($allowed) { Remove-Value 'HKLM:\' 'SOFTWARE\Policies\Microsoft\Windows\EdgeUI' 'AllowEdgeSwipe'; return }
    New-Item -Path $key -Force | Out-Null
    New-ItemProperty -Path $key -Name 'AllowEdgeSwipe' -Value 0 -PropertyType DWord -Force | Out-Null
}

function Unprotect-Client($root) {
    $system = 'Software\Microsoft\Windows\CurrentVersion\Policies\System'
    $explorer = 'Software\Microsoft\Windows\CurrentVersion\Policies\Explorer'

    Remove-Value $root $system 'DisableTaskMgr'
    Remove-Value $root $system 'DisableRegistryTools'
    Remove-Value $root 'Software\Policies\Microsoft\Windows\System' 'DisableCMD'
    Remove-Value $root $explorer 'NoRun'
    Remove-Value $root $explorer 'NoControlPanel'
    Remove-Value $root $explorer 'DisallowRun'
    $list = Join-Path $root "$explorer\DisallowRun"
    if (Test-Path $list) { Remove-Item $list -Recurse -Force }
    Remove-Value $root 'Software\Microsoft\Windows\CurrentVersion\Run' 'ZafiroAgent'
    Remove-Value $root 'Software\Microsoft\Windows\CurrentVersion\GameDVR' 'AppCaptureEnabled'
    Remove-Value $root 'System\GameConfigStore' 'GameDVR_Enabled'
    Remove-Value $root 'Software\Microsoft\GameBar' 'UseNexusForGameBarEnabled'
    # Значения Windows по умолчанию.
    Set-Value $root 'Control Panel\Accessibility\StickyKeys' 'Flags' '510' 'String'
    Set-Value $root 'Control Panel\Accessibility\Keyboard Response' 'Flags' '126' 'String'
    Set-Value $root 'Control Panel\Accessibility\ToggleKeys' 'Flags' '62' 'String'
    Remove-Value $root 'Control Panel\Keyboard' 'PrintScreenKeyForSnippingEnabled'
    foreach ($policy in $NetworkPolicies.Keys) { Remove-Value $root $NetworkPolicyKey $policy }
}

Assert-Administrator
if ($Undo) {
    Remove-AgentService
    Set-EdgeSwipe $true
    if (-not (Get-LocalUser -Name $ClientUser -ErrorAction SilentlyContinue)) {
        Write-Host "Учётной записи $ClientUser нет: снимать с неё запреты не нужно." -ForegroundColor Green
        return
    }
}
else {
    Install-Agent
    Install-AgentService
    Set-EdgeSwipe $false
    if (-not (Assert-ClientUser)) { return }
}

$hive = Open-ClientHive
try {
    if ($Undo) {
        Unprotect-Client $hive.Root
        Write-Host "Запреты для $ClientUser сняты, служба агента удалена." -ForegroundColor Green
    }
    else {
        Protect-Client $hive.Root
        Write-Host "Готово: учётная запись $ClientUser защищена, служба запускает агент при входе." -ForegroundColor Green
        Write-Host 'Запреты вступят в силу при следующем входе клиента в Windows.'
    }
}
finally {
    Close-ClientHive $hive
}
