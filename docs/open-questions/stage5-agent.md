# Этап 5, A1 — новый агент: слой связи (вопросы и решения)

Реализация — `agent/` (решение `agent/ZafiroAgent.slnx`), описание — `docs/agent-protocol.md` §14. Решения приняты по
принципам `docs/DECISIONS.md` (владелец поручил); отменить может только владелец.

## AG-1. Windows не проверена на реальной машине

- [ ] Проверить на Windows 10 и 11 до первого перевода клуба (как пункт «Windows/SChannel и TPM не проверены» в
  `stage2-agent-enroll.md` §8).
  - Контекст: в среде разработки нет Windows. Код Windows компилируется (CI `windows-latest`: сборка, unit-тесты,
    Native AOT `win-x64`), но не исполнялся: служба Windows (`AddWindowsService`), ключ CNG в TPM/Software KSP с
    SChannel (`WindowsCredentialStore`), DPAPI файла учётных данных, именованный канал и проверка SID/сеанса/exe клиента
    (`NamedPipeListener`), `CreateProcessAsUser` для UI (`WindowsUiLauncher`), SMBIOS через `GetSystemFirmwareTable`,
    летучий раздел реестра для `boot_id`, `shutdown.exe`.
  - Решение: принять код как есть; чек-лист проверки — сценарий `ServerScenarioTests` на Windows-ПК против стенда
    (тот же `server.sh`), плюс ручная проверка: подключение UI к каналу из сеанса пользователя, отказ чужому exe.

## AG-2. Открытый ключ подписи обновлений не встроен

- [ ] Сгенерировать ключ выпуска (`go run ./cmd/sign-update -genkey -key-id <id>`, §6) и вписать открытую часть в
  `agent/src/Zafiro.Agent.Service/ServiceOptions.cs` `UpdateKeys.Embedded`.
  - Контекст: решение «Обновления агента» — открытый ключ вшит в агента. Ключа выпуска пока нет (секрет CI
    `AGENT_UPDATE_SIGNING_KEY` создаёт владелец). Пока список пуст, любое `UpdateAvailable` отклоняется
    (`UPDATE_VERIFICATION_FAILED`, неизвестный `key_id`).
  - Сделано: ключи из конфигурации (`Service:DevUpdatePublicKeys`) принимаются только в окружении `Development`
    (тесты); в production — только встроенные.

## AG-3. Корень CA: файл конфигурации, а не ресурс сборки

- [x] Как агент получает корень Zafiro для pinning?
  - Сделано: `Agent:RootCaPem` или `Agent:RootCaFile` (PEM, можно несколько корней на время ротации). Файл кладёт
    установщик в папку программы (запись — только администраторы).
  - Решение: так и оставить до A2; установщик A2 встраивает production-корень (`root-ca.pem` офлайн-CA) в пакет.
    Встраивать в сборку как ресурс не стали: у dev/стенда другой корень, а сборка одна.

## AG-4. Grpc.* 2.85.0 моложе 14 дней

- [x] Правило PD-4 — только версии старше 14 дней; Grpc.Net.Client/Grpc.StatusProto 2.85.0 вышли 26–29.10.2026.
  - Решение: оставить 2.85.0 — она же проверена `tools/proto-csharp-check.sh` и в §12.6 (30.10.2026), задача требует
    совпадения версий. Остальные пакеты выбраны не моложе 14 дней (Microsoft.Extensions.* 10.0.13, а не 10.0.14).
    Дальше версии ведёт Dependabot (cooldown 14 дней, группа `grpc` обновляет Grpc.* и Google.Protobuf вместе;
    `tools/proto-csharp-check.sh` нужно править вместе с `agent/Directory.Packages.props`).

## AG-5. Отзыв сертификата ПК не отзывает токены гостя этого ПК (сервер)

- [ ] Сервер: при отзыве сертификата ПК (`RevokeComputer`, перерегистрация, удаление карточки) отзывать токены гостя
  этого ПК (`guestapi.Tokens.RevokeComputer`).
  - Контекст: найдено интеграционным тестом. `Tokens.SignedInElsewhere` (`server/internal/agent/guestapi/tokens.go`)
    считает гостя «сидящим за ПК», если его токен на другом ПК использовался последние 2 минуты
    (`AgentSignIns::elsewhere()`). После «Отключить ПК» токен того ПК уже неприменим (привязан к серийному номеру
    отозванного сертификата), но ещё до 2 минут блокирует вход гостя на соседнем ПК: `SIGNED_IN_ELSEWHERE`.
  - Рекомендация: отзывать токены в хуке отзыва (`enroll.Options.OnRevoked`). Агент со своей стороны при
    `CERTIFICATE_REVOKED` забывает гостя (событие `GuestSignedOut` отправить уже нельзя — сертификата нет).
    Сервер не менялся (задача A1 — только агент).

## AG-6. GetTranslations для языка без каталога: NOT_FOUND + VALIDATION_FAILED (сервер)

- [ ] Привести к таблице `common.proto`: `ERROR_CODE_VALIDATION_FAILED` ↔ `INVALID_ARGUMENT`.
  - Контекст: на базе без импортированных переводов `GetTranslations{locale: "ru"}` отвечает
    `NOT_FOUND` с `ErrorInfo.reason = ERROR_CODE_VALIDATION_FAILED` («Язык не найден.»). Агент обрабатывает по
    `ErrorCode`, поэтому не ломается, но код и reason расходятся с контрактом.
  - Рекомендация: `INVALID_ARGUMENT` (неизвестный язык) или пустой ответ `not_modified = false, lines = {}` для
    включённого языка без строк.

## AG-7. CommandResult на мгновенные команды

- [x] Отвечать ли `CommandResult` на команды с `seq = 0` (`BalanceChanged`, чат, счётчики…)?
  - Контекст: `agent.proto` требует ответ для `Ping`; для остальных мгновенных не сказано. Сервер принимает итог любой
    команды (`stream.conn.onResult`), в `agent_commands` пишет только `seq > 0`.
  - Решение: отвечать на все (единообразно, сервер видит доставку в хуке `Results`). Мгновенные редки (несколько на
    действие гостя), нагрузка на 50 000 ПК пренебрежимая. Дедупликация мгновенных — только в памяти (на диск пишутся
    только долговременные — `commands.pb`).

## AG-8. Сведения о ПК без WMI

- [x] Как собирать железо, если WMI (`System.Management`) несовместим с Native AOT?
  - Решение: SMBIOS UUID — `GetSystemFirmwareTable('RSMB')` (тот же UUID, что `Win32_ComputerSystemProduct.UUID`,
    заглушки 00…/FF… отбрасываются, как `WindowsHardwareProbe.GetDeviceUuid`), CPU — реестр `ProcessorNameString`,
    видеоадаптеры — `DriverDesc` класса видеоадаптеров через «; » (как `HardwareInfo.JoinGpuNames`), ОС — реестр
    (`ProductName DisplayVersion (CurrentBuild)`, без «Microsoft »), ОЗУ — `GlobalMemoryStatusEx`, MAC/IP — правила
    `NetworkSelector` (работает, не loopback/туннель, есть шлюз, Ethernet первым). Строки ОС могут отличаться от WMI
    `Caption` по форме («Windows 11 Pro 23H2 (22631)» вместо «Windows 11 Pro») — сервер хранит как текст, сравнения нет.

## AG-9. Токен гостя только в памяти службы

- [x] Переживает ли вход гостя перезапуск службы?
  - Контекст: `guest.proto` (`GuestToken`): «хранится агентом только в памяти и в DPAPI текущего пользователя». В новой
    архитектуре токен держит служба (SYSTEM), UI его не видит.
  - Решение: только память службы. После перезапуска службы (обновление, сбой) гость входит заново; оплаченная сессия
    не теряется — она приходит в `Welcome.session`, и ПК остаётся разблокированным. Хранить токен на диске службы —
    лишняя поверхность (12 часов доступа к балансу).

## AG-10. Ключ образа (бездисковые ПК) в appsettings.json

- [ ] Защитить `Agent:ImageKey` на диске образа.
  - Контекст: ключ образа (Q-037) — многоразовый секрет клуба, кладётся в образ. Сейчас — `appsettings.json` в папке
    программы (читают администраторы и SYSTEM).
  - Рекомендация (A2, установщик): при первом запуске служба перекладывает ключ в DPAPI-файл своей папки и стирает из
    конфигурации; риск утечки образа принят решением Q-037 (ограничители — подсети, короткий сертификат, отзыв ключа).

## AG-11. Тестовое окружение — демо-фикстура визуального сравнения

- [x] Откуда интеграционные тесты берут клуб, владельца, гостя и тариф?
  - Решение: `agent/tests/integration/server.sh` грузит `tools/visual/fixtures/demo.json` (`server/cmd/visual-seed`) в
    отдельную базу (compose `zafirogo-agent-it`, tmpfs, свои порты) и продлевает в копии фикстуры подписку клуба
    (в фикстуре она кончается 01.11.2026, иначе Enroll — `SUBSCRIPTION_INACTIVE`). Код регистрации выдаётся REST
    `POST /api/agent-enrollment/codes` под владельцем — как сотрудник в панели; отзыв — `/revoke`; команда — уведомление
    `/api/club-computers/notify`. Go-хелперы не понадобились, сервер не менялся.
