# Протокол агента ПК (gRPC, `zafiro.agent.v1`)

Контракт: `proto/agent/v1/` — `common.proto`, `enrollment.proto`, `agent.proto` (шапка с общими правилами),
`guest.proto`, `shop.proto`, `social.proto`. Генерация Go — `server/gen/agent/v1` (пакет `agentv1`).
Инвентаризация старого API — `docs/parts/20-api.md`. Открытые вопросы — `docs/open-questions/api.md`.

Совместимость со старым HTTP API не нужна (ТЗ, «Принятые решения» п. 3): агенты будут переустановлены.
Бизнес-логика за RPC остаётся прежней (те же сервисы Laravel → Go), меняется только слой связи.

## 1. Сервисы

| Сервис | Транспорт | Токен гостя | Назначение |
|---|---|---|---|
| `EnrollmentService` | TLS (Enroll), mTLS (RenewCertificate) | — | регистрация ПК по одноразовому коду, продление сертификата |
| `AgentService` | mTLS | — (VerifyStaffPin — опционально) | стрим `Connect`, переводы, PIN сотрудника, картинки клуба (`GetImage`) |
| `GuestAuthService` | mTLS | только `Logout` | вход, регистрация, QR-вход, восстановление пароля |
| `GuestService` | mTLS | да | профиль, тарифы, минуты, пакеты, сессия, пересадка, журнал, новости |
| `ShopService` | mTLS | `PlaceOrder` | товары, заказ к ПК |
| `SocialService` | mTLS | да | друзья, перевод баланса, чат |

Все сервисы обслуживает служба агента (Windows-служба, SYSTEM): приватный ключ сертификата доступен только ей.
UI (WPF/WebView2) общается со службой по именованному каналу, как сейчас (`VaultProtocol`), но вместо
пересылки HTTP служба выполняет типизированные вызовы gRPC.

## 2. Поток подключения

```
Служба агента                                   Сервер
  │  (нет сертификата)                             │
  │── Enroll(code, CSR, DeviceInfo) ──────────────▶│  TLS без клиентского сертификата
  │◀── cert, CA chain, computer_id, club_id ───────│
  │                                                │
  │══ mTLS ═══ Connect() ══════════════════════════│
  │── Hello(protocol_version=1, agent_version,     │
  │         last_command_seq, boot_id, device) ───▶│  проверка сертификата (не отозван), версии
  │◀── Welcome(server_time, config, session,       │
  │            translations_version, status_interval)
  │◀── Command(seq=N+1..)  (досылка пропущенных)    │
  │── CommandResult(...) ─────────────────────────▶│
  │── Status(state, session, load) ───────────────▶│  каждые 60 с и при изменении
  │◀── Command(StartSession / BalanceChanged / …) ─│  push в момент события
  │── AgentEvent(event_id, SessionEnded …) ───────▶│
  │◀── EventAck(event_id) ─────────────────────────│
```

Правила (подробно — шапка `agent.proto`):

- **Hello** обязателен первым; без него через 10 с сервер закрывает стрим (`INVALID_ARGUMENT`).
- **Welcome** — полный снимок: после reconnect агенту не нужны отдельные запросы конфигурации, сессии, баланса.
- **Команды**: `command_id` (UUID v7) — идемпотентность; `seq` — порядок и досылка. Долговременные команды
  (сессия, блокировка, питание, сообщение, обновление, техрежим, WoL, выход гостя) имеют `seq > 0` и
  хранятся сервером до `expires_at`; мгновенные (`Ping`, `BalanceChanged`, `GuestCountersChanged`,
  `ChatMessageReceived`, `TranslationsChanged`, `ConfigChanged`, `QrLoginApproved`, `Drain`) — `seq = 0`.
- Агент сохраняет `last_command_seq` на диск до отправки `CommandResult`; в `Hello` сообщает его — сервер
  досылает всё с большим `seq`. Повтор `command_id` → `CommandResult(DUPLICATE)` без повторного выполнения.
- **События агента** — «как минимум один раз»: журнал на диске до `EventAck`, дедупликация по `event_id`.
- **Один стрим на ПК**: новый `Connect` того же сертификата закрывает прежний (`ABORTED`, reason `replaced`).
- **Reconnect**: full jitter, `delay = random(0, min(60 с, 2^attempt с))`; `Drain` при плановом перезапуске
  инстанса — переподключение в случайный момент окна (по умолчанию 30 с), чтобы 50 000 ПК не пришли разом.
- **Keepalive**: HTTP/2 PING от клиента каждые 30 с (таймаут 10 с); сервер — `EnforcementPolicy.MinTime = 20 с`.

### Heartbeat и запись в БД

Сейчас почти каждый heartbeat (30 с) пишет `club_computers` в транзакции с блокировкой строки
(`app/Services/ClubComputerSessions.php`, `heartbeat()`/`needsWrite()`; разбор — `docs/parts/20-api.md`, раздел 7).
Новое правило:

- онлайн = открыт стрим `Connect` (регистрация в Valkey: `pc:<computer_id>` → инстанс, TTL 90 с, продлевается
  каждым `Status`), без записи в Postgres;
- `Status` хранится в памяти инстанса + Valkey (для карты клуба и WebSocket админки); в Postgres пишется только
  смена состояния (`agent_state`, сессия, вход/выход гостя) и железо, если изменилось (`Hello.device`);
- история онлайна (если нужна для отчётов) — агрегатами в фоновом воркере, не построчно;
- завершение сессии по времени — воркер сервера (как `club-computers:expire-sessions` каждую минуту в Laravel, `routes/console.php`, строка 34), а не heartbeat;
  агенту уходит команда `EndSession`.

### Маршрутизация команд между инстансами (до 50 000 ПК)

Команду порождает любой инстанс (панель, касса, воркер). Она пишется в Postgres (`agent_commands`:
`computer_id, seq, command_id, payload, expires_at, delivered_at, result`), затем публикуется в NATS
`agent.cmd.<club_id>.<computer_id>`; инстанс, держащий стрим ПК, подписан на свои ПК и отправляет её.
Если ПК не в сети — команда дождётся `Hello` (досылка по `seq`) или истечёт. `seq` выдаётся
последовательностью на ПК (`UPDATE … RETURNING`), пропуски допустимы.

## 3. mTLS и сертификаты

- Собственный CA Zafiro: корневой (офлайн) → промежуточный «agent-issuing» (онлайн, ключ в KMS/HSM или
  хотя бы в секрете вне репозитория). Корень встроен в агент (pinning), системное хранилище Windows не используется.
- Клиентский ключ: ECDSA P-256, генерируется на ПК как неэкспортируемый машинный ключ CNG
  (Microsoft Platform Crypto Provider — TPM, если есть; иначе Software Key Storage Provider), доступ только SYSTEM.
- Сертификат: `CN=computer:<computer_id>`, `O=club:<club_id>`, SAN URI `zafiro://club/<club_id>/computer/<computer_id>`,
  EKU `clientAuth`, срок 30 дней, продление после 2/3 срока (`RenewCertificate`) с новым ключом.
- Сервер на каждом рукопожатии (`VerifyPeerCertificate`) проверяет цепочку, срок и статус серийного номера
  (таблица `agent_certificates` + кэш в Valkey/памяти, инвалидация через NATS).
- Идентичность ПК берётся только из сертификата. `computer_id` своего ПК в запросах не передаётся — это
  закрывает класс ошибок `wrong_computer` (старые проверки `X-Agent-Token` в `AgentTariffController::purchase` и др.).

### Регистрация по одноразовому коду

1. Сотрудник с правом управления ПК в панели нажимает «Подключить ПК» (можно сразу выбрать зону/карточку)
   и получает код: 8 символов `A–Z` без `I/O` и `2–9`, срок 15 минут, одноразовый, хранится хешем.
2. Сотрудник вводит код на экране агента; служба генерирует ключ и CSR, вызывает `Enroll`.
3. Сервер проверяет код (лимиты как `agent-enroll`: 5/мин на (IP, UUID), 300/мин на IP), подписку клуба,
   лимит ПК тарифа SaaS; находит карточку по MAC (затем UUID — как `ClubComputerRegistrar::find`) или создаёт
   в зоне «Не назначена»; отзывает прежние сертификаты этой карточки; выпускает сертификат; пишет аудит.
4. Ответ — сертификат, цепочка, `computer_id`, `club_id`, адрес `AgentService`.

Ключ установки клуба (64 символа, многоразовый) и вход сотрудника на ПК для его подключения
(`/api/clubs/resolve`, `/api/club-computers/resolve|register`, `/api/agent/enroll`) больше не нужны.
Исключение — бездисковые ПК: регистрация по «ключу образа» клуба (решение Q-037), §12.8.

### Отзыв сертификата

- Причины: «Отключить ПК» в панели, удаление карточки, повторный `Enroll` той же карточки, подозрение
  на компрометацию, отзыв всех ПК клуба (окончание договора).
- Отзыв = запись `revoked_at` + событие в NATS: инстанс, держащий стрим ПК, закрывает его
  (`UNAUTHENTICATED`, `ERROR_CODE_CERTIFICATE_REVOKED`); новые рукопожатия отклоняются.
- Агент на `CERTIFICATE_REVOKED` удаляет ключ и сертификат и показывает «ПК не подключён к клубу».
- CRL/OCSP не нужны: проверка статуса — на нашем же сервере, срок сертификата короткий.

## 4. Токен гостя

- Выдаётся `Login` / `Register` / `WatchQrLogin(APPROVED)`; непрозрачная случайная строка (≥ 256 бит),
  в БД — хеш; привязан к серийному номеру сертификата ПК — на другом ПК не принимается.
- Передаётся в метаданных `authorization: Bearer …`. Срок — до 12 часов (предложение; в Laravel 30 дней —
  см. open-questions). Выход, смена пароля, блокировка аккаунта отзывают токены; ПК получает `SignOutGuest`.
- Правило «один ПК на клиента» (`AgentSignIns`) сохраняется: `ERROR_CODE_SIGNED_IN_ELSEWHERE`.

## 5. Идемпотентность денежных RPC

`idempotency_key` обязателен в: `PurchaseTariff`, `PurchaseMinutes`, `PlayWholeBalance`, `StartPackage`, `EndSession`,
`TransferSeat`, `PlaceOrder`, `TransferBalance`, `SendChatMessage`. Ключ — UUID, генерирует агент на одно
действие пользователя и повторяет его при сетевых повторах. Сервер хранит `(computer_id, idempotency_key)` →
хеш запроса + ответ 24 часа (поведение как у `/api/v1`, `app/Http/Middleware/Api/ApiIdempotency.php`).
Этот же ключ — ключ идемпотентности записи в журнале денег (ledger), поэтому повтор не может списать дважды
даже при гонке двух инстансов.

## 6. Обновления агента (Ed25519)

Сейчас: служба сообщает `can_update`, сервер в ответе heartbeat отдаёт `agent_update {version, url, sha256}`,
когда ПК свободен (`app/Services/AgentUpdate.php`, `offerFor()`; `WindowsAgent/ZafiroAgent/Service/AgentUpdater.cs`
проверяет только SHA-256). Подлинность держится только на TLS к zafiro.kz.

Новое (`Command.update_available`, `UpdateAvailable`):

1. CI подписывает выпуск: Ed25519 по строке
   `"zafiro-agent-update/v1\n" + version + "\n" + sha256 + "\n" + size_bytes + "\n" + channel`.
   Приватный ключ — только в секретах CI (или KMS), не в репозитории и не на сервере приложения.
2. Сервер рассылает `UpdateAvailable` (с `expires_at`) ПК, где `capabilities.self_update = true`,
   постепенно (процент ПК клуба / канал), и только в `IDLE`, если `install_now = false`.
3. Служба: проверяет подпись встроенным открытым ключом `key_id` → скачивает по HTTPS с разрешённого хоста →
   проверяет размер и SHA-256 → дополнительно Authenticode-подпись установщика (если выпуск подписан) →
   ставит. Любое несовпадение — `UPDATE_VERIFICATION_FAILED`, файл удаляется.
4. Ротация ключа: в агенте список открытых ключей; новый ключ добавляется выпуском, подписанным старым.
5. Итог — `AgentEvent.update_result`.

Решение «Обновления агента» (docs/DECISIONS.md): **подпись Ed25519 обязательна**; ключ подписи — только в
CI-секретах, открытый ключ вшит в агента.

**Проверка на сервере** (`server/internal/agent/commands`, `update.go`). Сервер ничего не подписывает и ключа
подписи не знает. Перед рассылкой (`Sender.Send`) команда `UpdateAvailable` проверяется:

- `Validate` (всегда): `version` и `channel` непустые и без переводов строк, `url` — `https://хост`, `sha256` —
  64 hex-символа в нижнем регистре, `size_bytes > 0`, `signature_ed25519` — ровно 64 байта, `key_id` непустой;
- `VerifyUpdate`: `key_id` есть среди открытых ключей конфигурации `agent.update.public_keys`, подпись сходится
  (`ed25519.Verify` по строке выше — `commands.UpdateSigningMessage`). Нет ключей в конфигурации — обновления не
  рассылаются вовсе (`ErrInvalidCommand`). Это отсекает неподписанный или испорченный выпуск до того, как он уйдёт
  на 50 000 ПК; агент всё равно проверяет подпись сам своим встроенным ключом.

Настройка: `agent.update.public_keys` (env `ZAFIRO_AGENT_UPDATE_PUBLIC_KEYS`) — список `key_id:base64`, где base64 —
32 байта открытого ключа Ed25519 (не секрет; несколько — на время ротации), например
`ZAFIRO_AGENT_UPDATE_PUBLIC_KEYS=2026-10:Ab3…=,2027-04:Xy9…=`. Неверный формат — ошибка запуска.

**Как CI подписывает выпуск** (Этап 4, H4): workflow `.github/workflows/agent-release.yml` и утилита
`server/cmd/sign-update` (Go, тест `main_test.go`: подписанный манифест проходит `commands.VerifyUpdate`, изменённый
байт установщика, поля манифеста или подписи — нет).

1. **Ключ** — один раз, офлайн, на доверенной машине:
   - `openssl genpkey -algorithm ed25519 -out agent-update-<key_id>.pem` (приватный, PEM PKCS#8) и открытый
     `openssl pkey -in agent-update-<key_id>.pem -pubout -outform DER | tail -c 32 | base64`;
   - или `cd server && go run ./cmd/sign-update -genkey -key-id <key_id>` — печатает base64 seed (приватный) и строку
     `<key_id>:<base64>` для конфигурации.

   `key_id` — `[A-Za-z0-9._-]{1,32}`, например `2026-10`. Приватный ключ кладётся **только** в секрет окружения
   GitHub `agent-release` (`AGENT_UPDATE_SIGNING_KEY`; окружение — с обязательным подтверждением выпуска и
   ограничением веток/тегов), копия — в офлайн-хранилище. В репозиторий, на сервер приложения и в журналы он не
   попадает (утилита читает его только из переменной окружения и не печатает).
2. **Открытый ключ** `<key_id>:<base64>` (не секрет):
   - в конфигурацию сервера `agent.update.public_keys` (`ZAFIRO_AGENT_UPDATE_PUBLIC_KEYS`, несколько — через запятую
     на время ротации) — по нему кабинет суперадмина проверяет манифест, а `Sender.Send` — команду;
   - в переменные репозитория `AGENT_UPDATE_KEY_ID` (`<key_id>`) и `AGENT_UPDATE_PUBLIC_KEY` (`<key_id>:<base64>`) —
     workflow проверяет свежую подпись этим ключом до публикации (ошибка, если секрет и открытый ключ не пара);
   - в список ключей агента (`key_id` → ключ) в исходниках агента.
3. **Выпуск**: запуск вручную (`version`, `channel`, `installer_url`) или тег `agent-v<версия>` (адрес установщика —
   переменная `AGENT_INSTALLER_URL` с `{version}`). Сборка установщика — заготовка, пока агент C# не перенесён:
   установщик скачивается по https; позже шаг заменит сборка на `windows-latest` с тем же путём артефакта.
4. **Подпись**: `go run ./cmd/sign-update -file <установщик> -version <v> -channel <c> -key-id <id>
   [-expect-public-key <id>:<base64>]` — sha256 (hex, нижний регистр) и `size_bytes` файла, Ed25519 («чистый», без
   предхеширования) по строке (UTF-8, LF, без завершающего перевода строки)
   `zafiro-agent-update/v1\n<version>\n<sha256>\n<size_bytes>\n<channel>`, самопроверка `VerifyUpdate`.
   Результат — `<установщик>.update.json`:

   ```json
   {"version": "1.2.0", "sha256": "<64 hex>", "size_bytes": 12345678, "channel": "stable",
    "key_id": "2026-10", "signature_ed25519": "<base64, 64 байта>", "url": "https://…/ZafiroAgentSetup-1.2.0.exe"}
   ```

   `url` — справочно; сервер берёт адрес из шаблона установщика и манифест — по адресу `<установщик>.update.json`.
5. **Публикация**: установщик и манифест — артефакт job и выпуск `agent-v<версия>` (создаётся или дополняется).
   Суперадмин указывает версию в «Релиз агента» и включает автообновление: сервер скачивает манифест, проверяет
   подпись и только тогда рассылает `UpdateAvailable`.
6. **Ротация**: новый ключ — в `agent.update.public_keys` сервера и в агента (выпуском, подписанным старым ключом);
   затем секрет CI и переменные репозитория переключаются на новый; старый открытый ключ убирается после перехода
   всех агентов. Утечка ключа — убрать его открытую часть из конфигурации сервера (неподписанные новым ключом
   выпуски перестают приниматься) и выпустить агента с новым списком ключей.

Шаблон адреса установщика в кабинете (`installerURL`, `superadmin/release.go`) указывает на выпуски старого
репозитория (`AgentUpdate::INSTALLER_URL`); пока агент собирается там, манифест нужно класть рядом с установщиком
в том же выпуске (docs/open-questions/stage4-h4.md H4-2).

## 7. Ошибки

- unary: gRPC-код + `google.rpc.ErrorInfo{reason: "ERROR_CODE_…", domain: "agent.zafiro.kz", metadata}`;
  при валидации — `google.rpc.BadRequest`, при лимитах — `google.rpc.RetryInfo`, текст — `google.rpc.LocalizedMessage`
  (язык из `zafiro-locale`, если он включён на платформе, иначе язык клуба ПК — §15.3). Go: `google.golang.org/genproto/googleapis/rpc/errdetails` + `status.WithDetails`;
  C#: `Grpc.StatusProto` (пакет `Google.Api.CommonProtos`) — `RpcException.GetRpcStatus()`.
- В proto `google.rpc.*` не импортируется (нет зависимости от googleapis в buf): детали передаются в
  `grpc-status-details-bin`, а `ErrorCode` — общий словарь.
- Соответствие ErrorCode ↔ gRPC-код — в комментариях `common.proto`.

## 8. Лимиты частоты

Переносятся с `throttle:*` старого API (таблица — `docs/parts/20-api.md`, 1.1) и указаны у каждого RPC.
Ключ «ПК» теперь — `computer_id` из сертификата (а не хеш `X-Agent-Token`/IP), счётчики — в Valkey.
Ошибка — `RESOURCE_EXHAUSTED` + `ERROR_CODE_RATE_LIMITED` + `RetryInfo`.

## 9. Версионирование

- `zafiro.agent.v1` меняется только совместимо (новые поля/значения/RPC; `reserved` для удалённых).
- `Hello.protocol_version` — минорная версия внутри v1; `Welcome.min/max_protocol_version` — что
  поддерживает сервер. `AgentCapabilities` — флаги возможностей вместо сравнения версий агента
  (сейчас `ComputerPower::AGENT_VERSION = 0.0.66`, `SERVICE_AGENT_VERSION = 0.0.72`, `MESSAGE_AGENT_VERSION = 0.0.73`).
- Несовместимое — `zafiro.agent.v2` параллельно с v1.
- `buf breaking` (правило FILE) против ветки `main` — в CI, когда появится первый выпуск агента на gRPC.

## 10. Таблица «старый маршрут → новый RPC»

| Старый маршрут (`routes/api.php`) | Новый RPC / сообщение |
|---|---|
| `POST /api/clubs/resolve` | `EnrollmentService.Enroll` (одноразовый код вместо ключа установки) |
| `POST /api/club-computers/resolve` | `EnrollmentService.Enroll` |
| `POST /api/club-computers/register` | `EnrollmentService.Enroll` |
| `POST /api/agent/enroll` | `EnrollmentService.Enroll` (бездисковые — см. open-questions) |
| `POST /api/agent/token/rotate` | `EnrollmentService.RenewCertificate` |
| `POST /api/agent/heartbeat` | `AgentService.Connect`: `Hello`/`Status` → `Welcome`/`Command` |
| `POST /api/club-computers/{computer}/heartbeat` | `AgentService.Connect` |
| ответ heartbeat: `power_command` restart/shutdown/service | `Command.reboot` / `shutdown` / `enter_service_mode` |
| ответ heartbeat: `staff_message` | `Command.show_message` |
| ответ heartbeat: `wake_macs` | `Command.wake_on_lan` |
| ответ heartbeat: `agent_update` | `Command.update_available` (+ подпись Ed25519) |
| ответ heartbeat: `session`, `session_expired` | `Welcome.session`, `Command.start_session` / `end_session` |
| ответ heartbeat: `club` (лого, языки, `guest_features`, `after_session`, `idle_logout_minutes`) | `Welcome.config`, `Command.config_changed` |
| ответ heartbeat: `club.translations_version` | `Welcome.translations_version`, `Command.translations_changed` |
| ответ heartbeat: `customer` (баланс, заявки, чат) | `Command.balance_changed`, `guest_counters_changed`, `chat_message_received` |
| запрос heartbeat: `fresh_boot` | `Hello.boot_id` |
| запрос heartbeat: `can_update`, `commands` | `Hello.capabilities` |
| `GET /api/agent/translations/{locale}` | `AgentService.GetTranslations` |
| `POST /api/agent/exit-pin` | `AgentService.VerifyStaffPin` |
| `POST /api/agent/user/login` | `GuestAuthService.Login` |
| `POST /api/auth/login` (запасной путь агента) | `GuestAuthService.Login` |
| `POST /api/agent/register` | `GuestAuthService.Register` |
| `POST /api/agent/qr-login` | `GuestAuthService.CreateQrLogin` |
| `POST /api/agent/qr-login/poll` | `GuestAuthService.WatchQrLogin` (stream) + `Command.qr_login_approved` |
| `POST /api/auth/password/code` | `GuestAuthService.RequestPasswordResetCode` |
| `POST /api/auth/password/reset` | `GuestAuthService.ResetPassword` |
| `POST /api/auth/logout` | `GuestAuthService.Logout` |
| `GET /api/me` | `GuestService.GetProfile` (+ `BalanceChanged`) |
| `GET /api/tariffs` | `GuestService.ListTariffs` |
| `POST /api/tariffs/{tariff}/purchase` | `GuestService.PurchaseTariff` |
| `GET /api/guest/minutes` | `GuestService.GetMinuteOffer` |
| `POST /api/guest/minutes/purchase` | `GuestService.PurchaseMinutes` |
| «На весь баланс» (`main.js` minuteWholeBuy → purchaseMinutes) | `GuestService.PlayWholeBalance` (минуты считает сервер, §15.1) |
| автобиллинг (`main.js` autoBillingTick → purchaseMinutes) | воркер сервера `agent:auto-billing` + `GuestService.GetAutoBilling`, `Command.auto_billing_changed` (§15.1) |
| `GET /club-logos/…`, `/club-signin-images/…`, `/news-images/…` (агент качал сам) | `AgentService.GetImage` (§15.2) |
| `GET /api/guest/packages` | `GuestService.ListPackages` |
| `POST /api/guest/packages/{package}/start` | `GuestService.StartPackage` |
| `POST /api/club-computers/{computer}/session/end` | `GuestService.EndSession` |
| `GET /api/guest/seats` | `GuestService.GetSeatMap` |
| `POST /api/guest/seat-transfer` | `GuestService.TransferSeat` |
| `GET /api/guest/history` | `GuestService.ListHistory` |
| `GET /api/guest/news` | `GuestService.ListNews` |
| `POST /api/guest/profile/name` | `GuestService.UpdateName` |
| `POST /api/guest/profile/password` | `GuestService.ChangePassword` |
| `GET /api/products` | `ShopService.ListProducts` |
| `POST /api/guest/shop/purchase` | `ShopService.PlaceOrder` |
| `GET /api/guest/friends` | `SocialService.ListFriends` |
| `POST /api/guest/friends` | `SocialService.AddFriend` |
| `POST /api/guest/friends/{friendship}/accept` | `SocialService.AcceptFriend` |
| `POST /api/guest/friends/{friendship}/remove` | `SocialService.RemoveFriend` |
| `POST /api/guest/friends/{friendship}/transfer` | `SocialService.TransferBalance` (`password`) |
| `POST /api/guest/friends/{friendship}/transfer/code` | `SocialService.RequestTransferCode` |
| `POST /api/guest/friends/{friendship}/transfer/confirm` | `SocialService.TransferBalance` (`telegram_code`) |
| `GET /api/guest/chat` | `SocialService.ListChatMessages` + `Command.chat_message_received` |
| `POST /api/guest/chat` | `SocialService.SendChatMessage` |

Не переносятся в контракт агента:

| Маршрут | Почему |
|---|---|
| `GET /api/guest/telegram` | заглушка для агентов до 0.0.57 |
| `GET /api/auth/me`, `GET /api/auth/clubs`, `POST /api/auth/switch-club` | только мобильные приложения (отложены) |
| `GET/POST /api/guest/qr-login/{code}` | подтверждение QR с телефона — мобильное приложение / веб `/pc-login/{code}` |
| `GET /api/settings/{section}/{key}` | вызывающих нет (см. open-questions) |
| `/api/staff/*` (24) | приложение сотрудника (отложено) |
| `/api/v1/*` (17) | интеграционный API — отдельное решение (см. open-questions) |

## 11. Генерация и проверка

```bash
# инструменты (версии закреплены)
go install github.com/bufbuild/buf/cmd/buf@v1.75.0
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2

buf lint                       # стиль STANDARD (кроме PACKAGE_DIRECTORY_MATCH, см. buf.yaml)
buf generate                   # → server/gen/agent/v1/*.pb.go
cd server && go build ./gen/...   # google.golang.org/grpc v1.85.0, google.golang.org/protobuf v1.36.12

tools/proto-csharp-check.sh    # C#: Grpc.Tools 2.85.0 → генерация во временный каталог,
                               # сборка net10.0 с Google.Protobuf 3.36.2 + Grpc.Net.Client 2.85.0
                               # (локальный dotnet SDK 10 или Docker mcr.microsoft.com/dotnet/sdk:10.0)
```

В агенте (C#) рекомендуется подключать `.proto` через `Grpc.Tools` в csproj (`<Protobuf Include=… GrpcServices="Client" />`),
а не коммитить сгенерированный код.

## 12. Реализация mTLS

Код: `server/internal/pki` (CA, CSR, выпуск), `server/internal/agent/enroll` (коды, `Enroll`, `RenewCertificate`, отзыв,
кэш сертификатов), `server/internal/grpc` (пакет `grpcserver`: TLS, перехватчики, ошибки), `server/internal/agent/identity`
(идентичность ПК в контексте), миграция `db/migrations/0009_agent_enrollment.sql`, запросы `db/queries/agent_enroll.sql`.
Вопросы — `docs/open-questions/stage2-agent-enroll.md`.

### 12.1. Алгоритмы

- **ECDSA P-256 + SHA-256** — и у CA, и у клиента. Агент на .NET 10 под Windows: `SslStream` (через `SocketsHttpHandler`
  в `Grpc.Net.Client`) использует SChannel, который принимает клиентские сертификаты ECDSA P-256/P-384 и RSA;
  `CertificateRequest` строит CSR для `ECDsa`/`RSA`; ключ CNG P-256 бывает и программным, и в TPM (Microsoft Platform
  Crypto Provider). **Ed25519 не подходит**: ни SChannel, ни `X509Certificate2`/`CertificateRequest` его не поддерживают.
- Сервер принимает в CSR только **ECDSA P-256** или **RSA 3072–8192** (как в `enrollment.proto`). RSA 2048, P-384,
  Ed25519, подпись SHA-1/MD5, чужая подпись CSR → `ERROR_CODE_CSR_INVALID`.
- **TLS 1.2 минимум**, только ECDHE + AEAD (AES-GCM, ChaCha20-Poly1305), кривые X25519/P-256/P-384, ALPN `h2`.
  TLS 1.3 не обязателен: SChannel Windows 10 (большинство ПК клубов) его не поддерживает. Session tickets включены —
  массовое переподключение после `Drain` обходится без полной проверки подписей.

### 12.2. Сертификат ПК

| Поле | Значение |
|---|---|
| Subject | `CN=computer:<computer_id>, O=club:<club_id>` (Subject из CSR игнорируется) |
| SAN | URI `zafiro://club/<club_id>/computer/<computer_id>` — источник идентичности, CN сверяется с ним |
| Серийный номер | случайные 128 бит, в базе — hex нижним регистром (`agent_certificates.serial`) |
| Срок | `NotBefore = now − 5 мин` (часы ПК), `NotAfter = now + 30 дней` (настройка), не дольше срока CA |
| `renew_after` | `now + 2/3` срока |
| Расширения | `KeyUsage = digitalSignature` (+ `keyEncipherment` для RSA), `EKU = clientAuth`, `CA:false` |


CA: офлайн-корень → онлайн выпускающий CA «agent-issuing». Сервер грузит сертификат выпускающего CA (с цепочкой до
корня) и его ключ из файлов-секретов (`pki.Options.CACertFile/CAKeyFile`), в репозитории их нет. В dev
(`pki.Options.DevDir`) при первом запуске генерируются корень, выпускающий CA и серверный сертификат gRPC
(подписан тем же корнем) — каталог `0700`, файлы `0600`. Серверный сертификат в production — из файлов
(`ServerCertFile/ServerKeyFile`). Ротация выпускающего CA: `pki.LoadCAFromPEM(..., extraTrust...)` — прежний CA
остаётся в `ClientCAs`, пока живут его сертификаты (≤ 30 дней).

### 12.3. Проверка на сервере

- Один listener, `tls.Config.ClientAuth = VerifyClientCertIfGiven`, `ClientCAs` = выпускающие CA. Присланный
  сертификат Go проверяет на рукопожатии (цепочка, срок, `clientAuth`): чужой CA или истёкший → обрыв TLS
  (`UNAVAILABLE` у клиента), поэтому агент сам смотрит на свой `not_after` и при истечении идёт в `Enroll`.
- Перехватчик (unary + stream): методы без сертификата — только из явного списка (`EnrollmentService/Enroll`).
  Для остальных: проверенная на рукопожатии цепочка обязательна → срок (соединение HTTP/2 живёт дольше сертификата) →
  идентичность из SAN URI → статус серийного номера в `CertCache` → `identity.Identity{ClubID, ComputerID, CertSerial}`
  в контексте. Для `Enroll` идентичность не ставится, даже если сертификат прислан.
- `CertCache` — allow-list действующих сертификатов в памяти инстанса (50 000 ПК ≈ 100 000 строк, единицы МБ):
  полная загрузка при старте и раз в 10 минут (ловит удаления), инкрементально раз в 5 секунд по
  `agent_certificates.changed_at` с перекрытием 2 минуты; неизвестный серийный номер (выпущен другим инстансом)
  читается из базы. Отзыв на своём инстансе действует сразу; на других — через ≤ 5 с или сразу, если подписать
  `CertCache.MarkRevoked` на событие NATS (`enroll.Options.OnRevoked` публикует). `CertCache.OnRevoked` —
  подписка для закрытия стрима `Connect`, открытого отозванным сертификатом (агент A3; по серийному номеру, см. §13.3).
- Удаление карточки ПК отзывает её сертификаты триггером БД (`computer_deleted`), кто бы ни удалял.

| Ситуация | gRPC | `ErrorInfo.reason` |
|---|---|---|
| нет клиентского сертификата на защищённом методе | `UNAUTHENTICATED` | `ERROR_CODE_CERTIFICATE_REQUIRED` (нужна регистрация) |
| сертификат истёк за время жизни соединения | `UNAUTHENTICATED` | `ERROR_CODE_CERTIFICATE_EXPIRED` |
| отозван / удалён / не совпадает с записью | `UNAUTHENTICATED` | `ERROR_CODE_CERTIFICATE_REVOKED` |
| база недоступна при проверке неизвестного серийного номера | `UNAVAILABLE` | `ERROR_CODE_INTERNAL` |

Все ошибки — `google.rpc.Status` с `ErrorInfo{domain: "agent.zafiro.kz"}` + `LocalizedMessage` (русский ключ перевода);
валидация — ещё `BadRequest`, лимиты — `RetryInfo`.

### 12.4. Лимиты транспорта (`grpcserver.DefaultLimits`)

| Параметр | Значение | Почему |
|---|---|---|
| `MaxRecvMsgSize` / `MaxSendMsgSize` | 1 МБ / 8 МБ | сообщения агента маленькие; самый большой ответ — словарь переводов |
| `MaxConcurrentStreams` | 16 на соединение | один `Connect` + параллельные unary гостя |
| keepalive enforcement | `MinTime = 20 с`, `PermitWithoutStream` | агент пингует раз в 30 с (§2) |
| keepalive сервера | ping после 60 с тишины, таймаут 20 с | мёртвый ПК освобождается за ~80 с (TTL присутствия 90 с) |
| `MaxConnectionAge` | не задан | долгий стрим; перебалансировка — `Drain` с jitter |
| окно HTTP/2 | фиксированно 64 КБ (без роста BDP до 4 МБ) | память при 10–25 тыс. соединений на инстанс |
| буферы чтения/записи | 16 КБ (запись — общий буфер) | по умолчанию 32+32 КБ × 25 000 = 1,6 ГБ |
| `ConnectionTimeout` | 10 с | медленное рукопожатие не держит горутину и дескриптор |

### 12.5. Регистрация, продление, отзыв

- **Код** выдаёт сотрудник с ролью owner/admin/manager/operator (как `ClubComputerPolicy::register`, кассир — нет;
  режим просмотра суперадмином — нет): 8 символов `A–Z` без `I/O` + `2–9`, показ `ABCD-EFGH`, 15 минут. В базе —
  только `HMAC-SHA256(секрет сервера, код)` (40 бит кода не выдерживают перебора sha256 без секрета); код не пишется
  ни в лог, ни в аудит. Варианты: «новый ПК» (по желанию — зона) или «Перерегистрировать» конкретную карточку
  (прежние сертификаты отзываются сразу).
- **Enroll**: лимиты `agent-enroll` (5/мин на IP+UUID, 300/мин на IP; неверный/истёкший код — +1 к неудачам IP,
  после 20/мин — `RATE_LIMITED`). Транзакция: поиск кода по хешу (политика RLS `enroll_code_lookup` видит только строку
  с этим хешем) → `app.club_id` клуба кода → атомарное гашение (`UPDATE … WHERE used_at IS NULL … RETURNING`:
  второй Enroll с тем же кодом ждёт блокировку и получает `ENROLLMENT_CODE_EXPIRED`) → блокировка клуба →
  подписка (иначе `SUBSCRIPTION_INACTIVE`, код не гасится) → карточка по правилам `ClubComputerRegistrar`
  (MAC, затем UUID без MAC; новая — лимит ПК тарифа, зона/«Не назначена», вместимость, номер = max + 1) →
  железо → отзыв прежних сертификатов карточки (`reenrolled`) → выпуск → аудит (`security`, `agent.enrolled`).
- **RenewCertificate** (под mTLS): 10/мин на ПК; новый CSR обязан быть на **новом** ключе; железо сверяется с
  карточкой как `AuthenticateAgentDevice::samePc` (`DEVICE_MISMATCH`); подписка не проверяется (Q-039). Прежний
  сертификат действует до первого вызова с новым (тогда `superseded`) или до своего `not_after`.
- **Отзыв**: «Отключить ПК» (`RevokeComputer`), «Перерегистрировать» (`Reregister`), удаление карточки (триггер),
  повторный Enroll, отзыв ключа образа (§12.8). Агент на `CERTIFICATE_REVOKED` удаляет ключ и сертификат.

### 12.6. Агент на .NET 10: ключ, CSR, канал (без кода)

1. **Ключ.** Служба (SYSTEM) создаёт **именованный постоянный** машинный ключ CNG: `CngKey.Create` с алгоритмом
   `ECDsaP256`, `CngKeyCreationParameters { Provider = MicrosoftPlatformCryptoProvider (TPM), при ошибке —
   MicrosoftSoftwareKeyStorageProvider; KeyCreationOptions = MachineKey; ExportPolicy = None }`, имя вида
   `Zafiro.Agent.<guid>`. Именованный ключ обязателен: SChannel не умеет клиентскую аутентификацию с эфемерным
   ключом (`ECDsa.Create()` + `CopyWithPrivateKey` работает на Linux/OpenSSL, но на Windows падает в `SslStream`).
2. **CSR.** `new ECDsaCng(key)` → `new CertificateRequest("CN=zafiro-agent", ecdsa, HashAlgorithmName.SHA256)` →
   `CreateSigningRequestPem()`. Subject любой — сервер его заменяет. В сервер уходит только CSR, ключ не покидает ПК.
3. **Enroll** — по отдельному каналу **без** клиентского сертификата (с просроченным сертификатом TLS не поднимется).
4. **Сертификат.** `X509Certificate2.CreateFromPem(certificate_pem)` → `CopyWithPrivateKey(ecdsa)`. Сертификат (PEM)
   и имя ключа — в `%ProgramData%\Zafiro` с ACL только для SYSTEM; ключ остаётся в CNG/TPM. Проверить, что
   `ca_chain_pem` заканчивается встроенным корнем (pinning).
5. **Канал mTLS.** `SocketsHttpHandler`: `SslOptions.ClientCertificates = { cert }`, проверка сервера —
   `CertificateChainPolicy { TrustMode = CustomRootTrust, CustomTrustStore = { встроенный корень },
   RevocationMode = NoCheck }` (системное хранилище не используется), `KeepAlivePingDelay = 30 с`,
   `KeepAlivePingTimeout = 10 с`, `PooledConnectionIdleTimeout = Infinite`; `GrpcChannel.ForAddress("https://"+agent_endpoint)`.
6. **Продление** после `renew_after` (+ jitter до 24 ч): новый именованный ключ → CSR → `RenewCertificate` по текущему
   каналу → новый канал с новым сертификатом → после первого успешного вызова удалить старый ключ (`CngKey.Delete`).
7. **Ошибки**: `RpcException.GetRpcStatus()?.GetDetail<ErrorInfo>()?.Reason` (пакет `Grpc.StatusProto`).
   `CERTIFICATE_REVOKED` → удалить ключ и сертификат, экран «ПК не подключён к клубу»; `CERTIFICATE_EXPIRED` и `CERTIFICATE_REQUIRED` → Enroll (бездисковый ПК — с ключом образа, §12.8).

**Проверено (30.10.2026):** клиент .NET 10 (`mcr.microsoft.com/dotnet/sdk:10.0`, Linux, `Grpc.Net.Client 2.85.0`,
`Grpc.StatusProto 2.85.0`) против Go-сервера (`internal/agent/enroll`, dev-PKI): CSR `CertificateRequest` (P-256,
SHA-256) принят; Enroll → сертификат `CN=computer:1, O=club:1, SAN URI zafiro://club/1/computer/1`; повтор кода →
`ENROLLMENT_CODE_EXPIRED` с русским текстом; Renew тем же ключом → `CSR_INVALID`; Renew по mTLS новым ключом → новый
сертификат; после первого вызова новым сертификатом прежний → `CERTIFICATE_REVOKED`. **Не проверено:** Windows/SChannel
и ключ в TPM (нет Windows-машины) — пункт в `open-questions/stage2-agent-enroll.md`.

### 12.7. Настройки сервера (подключает `cmd/zafiro`)

| Ключ | Тип | По умолчанию | Секрет |
|---|---|---|---|
| `agent.grpc.listen` | string | `:8443` | нет |
| `agent.grpc.endpoint` (адрес для `EnrollResponse.agent_endpoint`) | string | — | нет |
| `agent.pki.ca_cert_file` / `ca_key_file` | path | — (в production обязательны) | ключ — да |
| `agent.pki.server_cert_file` / `server_key_file` | path | — (в production обязательны) | ключ — да |
| `agent.pki.dev_dir` | path | пусто (только dev) | содержит ключи |
| `agent.enroll.code_pepper` | string ≥ 32 байт | — | да |
| `agent.enroll.code_ttl` | duration | `15m` | нет |
| `agent.enroll.diskless_cert_lifetime` (сертификат по ключу образа, §12.8) | duration | `24h` | нет |
| `agent.update.public_keys` (открытые ключи подписи обновлений, §6) | список `key_id:base64` | пусто — обновления не рассылаются | нет |
| `agent.cert.lifetime` | duration | `720h` | нет |
| `agent.cert_cache.refresh` / `full_reload` | duration | `5s` / `10m` | нет |
| `database.platform_url` (пул `zafiro_platform` для кэша сертификатов) | DSN | — | да |

### 12.8. Бездисковые ПК: ключ образа

Решение Q-037 (docs/DECISIONS.md). Бездисковый ПК (CCBoot, iCafe8 и т.п.) теряет ключ и сертификат при каждой
перезагрузке, вводить одноразовый код каждый раз нельзя. Реализация: `server/internal/agent/enroll/image_key.go`,
миграция `0013_agent_image_keys.sql`, запросы `db/queries/agent_image_keys.sql`.

- **Ключ образа** — многоразовый секрет клуба `zik_` + 32 символа base64url (192 бита). Создают, видят список и
  отзывают роли owner и admin (как «Ключ установки клуба» в старом `settings/club.blade.php`), не в режиме
  просмотра суперадмином. Полный ключ показывается один раз при создании; в базе — `HMAC-SHA256(agent.enroll.code_pepper,
  ключ)` и первые 12 символов для показа; в аудите — только начало. У ключа: название, 1–16 подсетей (CIDR,
  `/0` запрещён), необязательный срок, отзыв. Ключ кладётся в образ (конфигурация службы агента).
- **Подсети** — адрес, который видит сервер (адрес TCP-соединения; обычно внешний IP клуба). За балансировщиком
  нужен PROXY protocol (см. open-questions/stage2-agent-enroll.md §8).
- **Enroll** с `EnrollRequest.image_key` (вместо `enrollment_code`, оба сразу — `VALIDATION_FAILED`): те же лимиты,
  что у кодов (неверный, истёкший ключ или чужая сеть — +1 к неудачам IP). `device.mac_address` обязателен.
  Транзакция: поиск ключа по хешу (политика RLS `image_key_lookup`) → `app.club_id` → ключ не отозван и не истёк
  (`ENROLLMENT_CODE_EXPIRED`), IP в подсетях (`ENROLLMENT_CODE_INVALID`) → подписка клуба → карточка по MAC:
  - **MAC известен** (есть карточка с этим MAC) — железо, отзыв прежних сертификатов (`reenrolled`), сертификат на
    24 часа (`agent.enroll.diskless_cert_lifetime`), `issued_via = image_key`, `diskless = true`, аудит `agent.enrolled`;
  - **MAC новый** — заявка в `agent_pending_computers` (повтор обновляет её: железо, IP, `attempts`), ответ
    `FAILED_PRECONDITION` + `ERROR_CODE_PENDING_APPROVAL` + `RetryInfo` (30 с). Агент повторяет Enroll через
    `retry_delay`. Нерешённых заявок на клуб — не больше 200 (иначе `RATE_LIMITED`);
  - **ПК отклонён** — `PERMISSION_DENIED` + `ENROLLMENT_CODE_INVALID`.
- **Подтверждение** — роли owner/admin/manager/operator (как коды): `ApprovePending` создаёт карточку по правилам
  `ClubComputerRegistrar::register` (лимит ПК тарифа, зона — выбранная или «Не назначена», вместимость, номер)
  или берёт существующую с этим MAC; следующий Enroll ПК — «MAC известен». `RejectPending` — отказ; отклонённый ПК
  можно подтвердить позже. Аудит: `agent.pending_computer.created|approved|rejected`.
- **Продление** сертификата бездискового ПК (`RenewCertificate`) — тоже на 24 часа и только пока ключ образа,
  по которому выдан сертификат, действует и IP в его подсетях. После перезагрузки ПК просто снова вызывает Enroll.
- **Отзыв ключа** (`RevokeImageKey`) сразу отзывает все действующие сертификаты, выданные по нему (включая
  продлённые), — ПК теряют связь; новый Enroll — `ENROLLMENT_CODE_EXPIRED`. Аудит `agent.image_key.created|revoked`.
- **Риск** (принят решением Q-037): утечка образа = возможность выдать себя за ПК клуба с известным MAC из сети
  клуба. Ограничители: подсети, короткий сертификат, отзыв ключа, подтверждение новых MAC.

Сервисные методы для панели (REST — при переносе экрана ПК): `CreateImageKey`, `ListImageKeys`, `RevokeImageKey`,
`ListPending`, `ApprovePending`, `RejectPending`.

## 13. Нагрузка сервера (стрим `Connect`, присутствие, команды)

Реализация: `server/internal/agent/stream` (стрим), `presence` (Valkey + запись изменений в Postgres + фоновая
проверка), `commands` (очередь и маршрутизация), миграция `0010_agent_stream.sql`, запросы `db/queries/agent_presence.sql`.
Открытые вопросы — `docs/open-questions/stage2-agent-stream.md`.

### 13.1. Где живёт состояние ПК

| Что | Где | Когда пишется |
|---|---|---|
| Кто держит стрим (`<instance>\|<gen>`) | Valkey `zafiro:agent:pc:<computer>`, TTL 90 с | при подключении; продлевается тикером инстанса пачкой (раз в TTL/3) и каждым `Status` |
| Снимок (`presence.State`: онлайн, версия, возможности, состояние, сессия, гость, загрузка, IP) | память инстанса + Valkey hash `zafiro:agent:club:<club>` | каждый принятый `Status` (не чаще 1 в 2 с) |
| Срок проверки | Valkey zset `zafiro:agent:online` (`<club>:<computer>` → крайний срок) | вместе с владением; при закрытии стрима — «сейчас + grace» |
| `status = 'online'/'offline'`, `last_seen_at` | Postgres `club_computers` | только переходы: подключение (если было не `online`), офлайн после grace/TTL |
| `agent_state` (+ снятие `staff_*` для idle/locked/in_session, как `changes()`) | Postgres | смена состояния, дебаунс 5 с (итоговое значение) |
| `agent_version`, `agent_boot_id`, `remote_ip`, железо, `device_name`, `hardware_reported_at` | Postgres | в `Hello`, только изменившиеся поля |
| Долговременные команды (`seq > 0`) и их итог | Postgres `agent_commands` (+ счётчик `agent_command_counters`) | постановка, `ACCEPTED`, итог |
| «Заменяемые последним» (`BalanceChanged`, `GuestCountersChanged`) | Valkey `zafiro:agent:last:<computer>:<kind>`, TTL 5 мин | при отправке; досылаются после `Welcome` |
| Дедупликация `AgentEvent.event_id` | Valkey `zafiro:agent:evt:<computer>:<event_id>`, TTL 24 ч | после успешной обработки события |

Сравнение со старым: `ClubComputerSessions::heartbeat()` писал `club_computers` (транзакция + `SELECT … FOR UPDATE`),
если что-то изменилось **или `last_seen_at` старше `SEEN_WRITE_SECONDS = 20 с`**. Heartbeat шёл раз в 30 с (> 20 с),
поэтому почти каждый heartbeat писал в MySQL — ≈ 2 UPDATE в минуту на ПК, ≈ 1 700 UPDATE/с на 50 000 ПК, плюс
heartbeat службы (60 с) и экрана входа (15 с). Теперь ПК с неизменным состоянием не пишет в Postgres ничего
(замер ниже: 0 строк за минуту на 10 000 ПК); онлайн — открытый стрим, а не `last_seen_at` моложе 90 с.

### 13.2. Офлайн и «зависшие» ПК

- Закрытие стрима → снимок `online = false`, срок «сейчас + `OfflineGrace`» (15 с). Переподключился в пределах grace
  (обрыв Wi-Fi, `Drain`, перезапуск службы) — в Postgres ничего не пишется.
- Падение инстанса → владение не продлевается и истекает через TTL 90 с.
- `presence.Sweeper` (один исполнитель — лок в Valkey; раз в 5 с) забирает из zset ПК с истёкшим сроком без живого
  владельца (атомарно, Lua) и пишет `status = 'offline'`. Раз в 5 минут — сверка `status = 'online'` в Postgres с Valkey
  (ПК без следа в Valkey два прохода подряд → `offline`; спасает от потери данных Valkey), истечение невыполненных команд
  (`status = 'expired'`) и удаление строк `agent_commands` старше 7 дней.
- Гонка «офлайн записан, а ПК уже переподключился» исключена порядком: новое соединение берёт владение в Valkey
  **до** чтения карточки из Postgres; Sweeper после записи `offline` проверяет владельца и при наличии возвращает `online`.

### 13.3. Один стрим на ПК

Подключение берёт владение Lua-скриптом с новым поколением `gen` (монотонно, не меньше времени Valkey в мкс). Прежнее
соединение закрывается `ABORTED "replaced"`: на том же инстансе — сразу, на другом — сообщением `kick{gen}` в NATS
на subject того инстанса; закрывается только соединение с меньшим `gen`, поэтому при двух одновременных подключениях
выживает ровно одно (новейшее). Закрывшееся старое соединение не снимает присутствие нового (сравнение владельца в Lua).
Отзыв сертификата — `stream.Service.Kick(club, pc, serial, codes.Unauthenticated, ERROR_CODE_CERTIFICATE_REVOKED, …)`
(`kick{serial}` в NATS): поток закрывается, только если открыт сертификатом с этим серийным номером (`identity.CertSerial`).
Прежний сертификат при продлении отзывается (`superseded`) при первом использовании нового — к этому моменту агент уже
подключён с новым, и отзыв «по ПК» закрыл бы новый поток (агент получил бы `CERTIFICATE_REVOKED` и удалил новый ключ).
«Отключить ПК», «Перерегистрировать», удаление карточки, повторный Enroll и отзыв ключа образа отзывают все неотозванные
сертификаты ПК, так что сертификат открытого потока всегда среди них. Пустой `serial` — закрыть поток ПК безусловно.

### 13.4. Команды

- `Commands.Send` (`commands.Sender.Send`): `command_id` — UUID v7, `issued_at` — сервер, `expires_at` — по виду
  (питание и WoL 120 с, сообщение 300 с — как `ComputerPower`; `StartSession` — до конца сессии; `EndSession`, `Lock`,
  `Unlock`, `SignOutGuest` — 10 мин; `UpdateAvailable` — 24 ч). Долговременная команда сначала сохраняется
  (seq выдаётся `INSERT … ON CONFLICT DO UPDATE … RETURNING` в той же транзакции, поэтому порядок коммитов = порядок seq),
  затем отправитель смотрит владельца в Valkey и публикует конверт в NATS на **subject инстанса**
  `zafiro.agent.inst.<instance>` (одна подписка на инстанс; не `agent.cmd.<club>.<computer>` из §2 — см. вопрос AS-1).
- Инстанс-владелец кладёт команду в очередь соединения; учёт `seq` и постановка в очередь — под одной блокировкой,
  поэтому агент получает долговременные команды строго по возрастанию `seq` (если через NATS пришла `N+2` раньше `N+1`,
  соединение досылает из Postgres по порядку).
- После `Hello`: если последний выданный seq больше `last_command_seq` — досылка неистёкших невыполненных команд;
  иначе запроса к очереди нет. Если агент знает seq больше выданного (база из копии) — счётчик поднимается.
- Нет `CommandResult` за 30 с — повтор (до 3 раз, тот же `command_id`). `ACCEPTED` останавливает повторы.
- Команда, которую агент не умеет (`AgentCapabilities`), не отправляется и получает `FAILED + COMMAND_UNSUPPORTED`.

### 13.5. Замер (нагрузочный тест)

`server/internal/agent/stream/load_test.go`, теги `integration load` (не в CI):

```bash
cd server && go test -tags 'integration load' -run '^TestLoad$' -v -timeout 30m ./internal/agent/stream/
# ZAFIRO_LOAD_PCS (10000), ZAFIRO_LOAD_STATUS_EVERY (30s), ZAFIRO_LOAD_WINDOW (60s), ZAFIRO_LOAD_REPORT (путь JSON)
```

Сервер — отдельный процесс (тот же тестовый бинарник), gRPC без TLS на 127.0.0.1 с лимитами `grpcserver.DefaultLimits`,
идентичность ПК — тестовый перехватчик. Агенты — 10 000 отдельных TCP-соединений по одному стриму. Postgres 18.6,
Valkey 9.1.3, NATS 2.15.2 в Docker; всё (включая клиентов) на одной машине: 4 vCPU, 16 ГБ, Go 1.27.3.

| Показатель (10 000 стримов, Status раз в 30 с, окно 61 с) | Значение |
|---|---|
| Записей в Postgres за окно (`club_computers.updated_at`, `agent_commands`) / `Recorder.Writes()` | **0 / 0** |
| Status за окно | 20 364 (≈ 333/с) |
| CPU процесса сервера | 17–21 % одного ядра (3 прогона) |
| RSS процесса сервера | ≈ 620 МБ всего, **≈ 61 КБ на стрим** |
| Куча Go / стеки горутин на стрим | ≈ 25 КБ / ≈ 34 КБ |
| Горутин на стрим | **5,0** (2 стрима: отправка и чтение; 3 — транспорт HTTP/2 соединения) |
| Подключение 10 000 ПК | 45 с (≈ 220/с; клиенты, сервер и базы делят 4 vCPU) |
| 1 000 команд `ShowMessage` с «другого инстанса» (последовательные `Send`) | 2,2–2,5 с на все; последняя доставлена через ≤ 2,5 с от первой отправки (≈ 2,3 мс на `Send`: транзакция Postgres + Valkey + NATS) |

Оценка на 50 000 ПК (Status по умолчанию раз в 60 с): ≈ 830 Lua-вызовов Valkey в секунду, ≈ 3 ГБ RAM на все инстансы
под стримы без TLS (≈ 4,5 ГБ с mTLS — замер ниже), ≈ 0,5 ядра суммарно на статусы.
Postgres — только переходы (подключения, смены состояния, команды), не статусы. Узкое место массового переподключения —
чтение карточки ПК при `Hello` (1 запрос) и запись `online` после офлайна; `Drain` с окном 30 с растягивает 50 000
подключений до ≈ 1 700/с на кластер.

**Замер с mTLS** (Этап 4, H4): `ZAFIRO_LOAD_TLS=1.2|1.3` — серверный TLS `grpcserver.TLSConfig` (как в production:
`VerifyClientCertIfGiven`, ECDHE + AEAD, h2), dev-PKI во временном каталоге, у каждого ПК свой клиентский сертификат
ECDSA P-256 от выпускающего CA (`pki.CA.Issue`), версия TLS клиента ограничена указанной (агенты на Windows 10 —
TLS 1.2). Идентичность ПК — по-прежнему тестовый перехватчик (проверка отзыва в замер не входит).

```bash
cd server && ZAFIRO_LOAD_PCS=5000 ZAFIRO_LOAD_TLS=1.2 go test -tags 'integration load' -run '^TestLoad$' -v -timeout 30m ./internal/agent/stream/
```

5 000 стримов, Status раз в 30 с, окно 61 с; та же машина (4 vCPU, 16 ГБ; клиенты, сервер, базы и параллельные
сборки других задач на ней же), три прогона подряд:

| Показатель | без TLS | mTLS, TLS 1.2 | mTLS, TLS 1.3 |
|---|---|---|---|
| RSS процесса сервера (всего) | 329 МБ | 468 МБ | 501 МБ |
| **RSS на стрим** | **≈ 62 КБ** | **≈ 90 КБ** | **≈ 97 КБ** |
| Куча Go на стрим | ≈ 26 КБ | ≈ 62 КБ | ≈ 63 КБ |
| Стеки горутин на стрим / горутин на стрим | ≈ 35 КБ / 5,0 | ≈ 35 КБ / 5,0 | ≈ 35 КБ / 5,0 |
| CPU сервера (Status ≈ 167/с) | 7,7 % ядра | 8,2 % ядра | 8,5 % ядра |
| Записей в Postgres за окно | 0 | 0 | 0 |
| Подключение 5 000 ПК (полное рукопожатие, 200 параллельно) | 20 с | 14 с | 22 с |
| 1 000 команд с «другого инстанса», последняя доставлена через | 2,1 с | 1,8 с | 1,8 с |

Вывод: mTLS добавляет ≈ 30–35 КБ на соединение (буферы записей `crypto/tls`, состояние шифра, разобранная цепочка
сертификатов клиента); горутин и стеков не добавляет, CPU на установившихся стримах — +0,5–1 % ядра на 5 000 ПК.
Оценка на 50 000 ПК: ≈ 4,5 ГБ RAM под стримы на все инстансы (TLS 1.2) против ≈ 3 ГБ без TLS; при 3 инстансах —
≈ 1,5 ГБ на инстанс плюс база процесса. Время подключения в прогоне ограничено клиентами на той же машине (генерация
рукопожатий 200 параллельно), а не сервером; разброс 14–22 с между прогонами — шум общей машины.

## 14. Клиент .NET (агент, `agent/`)

Этап 5: A1 — слой связи нового агента (служба), A2 — интерфейс (процесс UI: WPF + WebView2, страница — перенос
`WindowsAgent/Frontend`), установщик и выпуск. Вопросы и принятые решения — `docs/open-questions/stage5-agent.md`.

### 14.1. Решение и проекты

`agent/ZafiroAgent.slnx`, .NET 10 (`agent/global.json`), версии пакетов — точно, централизованно
(`agent/Directory.Packages.props`), lock-файлы проверяются (`dotnet restore --locked-mode`), обновления — Dependabot
(`nuget`, `/agent`, cooldown 14 дней). Предупреждения — ошибки; анализаторы AOT/trim и платформ (CA1416) включены.

| Проект | Что внутри |
|---|---|
| `Zafiro.Agent.Protocol` | C# из `proto/agent/v1/*.proto` (Grpc.Tools 2.85.0 при сборке, в репозиторий не кладётся; Google.Protobuf 3.36.2, Grpc.Net.Client 2.85.0 — как `tools/proto-csharp-check.sh`) + локальные контракты: IPC служба ↔ UI (`Proto/zafiro/agent/ipc/v1/ipc.proto`) и файлы состояния службы (`Proto/zafiro/agent/local/v1/state.proto`) |
| `Zafiro.Agent.Core` | библиотека связи без UI и хостинга: регистрация и продление (`Enrollment/Enroller.cs`), хранилища ключа (`Credentials/`), канал mTLS (`Transport/AgentChannelFactory.cs`, `TrustAnchors.cs`, `Backoff.cs`), стрим и команды (`Runtime/AgentRuntime.cs`, `AgentState.cs`), журналы команд и событий (`Journal/`), клиенты гостя (`Guest/GuestApi.cs`), ошибки (`Errors/AgentRpcException.cs`), обновления (`Updates/`) |
| `Zafiro.Agent.Service` | хост службы (`Microsoft.Extensions.Hosting`; Windows — служба `ZafiroAgent`, Linux — консоль для тестов), платформа (`Platform/WindowsPlatform.cs`, `LinuxPlatform.cs`), IPC (`Ipc/`), запуск UI и уведомления для него (`Ui/UiCoordinator.cs`), ключ образа (`ImageKeyStore.cs`, AG-10) |
| `Zafiro.Agent.UI.Core` | логика процесса UI без WPF (проверяется на Linux): клиент IPC (`Ipc/IpcClient.cs`), мост страницы ↔ служба (`Bridge/BridgeRouter.cs`), QR-код (`Bridge/QrCodes.cs`), выбор экрана (`Screens/UiController.cs`), тексты уведомлений (`Screens/UiTranslations.cs`), правила киоска (`Kiosk/KioskRules.cs`) |
| `Zafiro.Agent.UI` | процесс UI, `net10.0-windows`, WPF + WebView2 (`Microsoft.Web.WebView2` 1.0.4258.31): окно (`Views/MainWindow.cs`), уведомления в углу (`Views/CornerNotice.cs`), закрытие других мониторов (`Views/ScreenCover.cs`), перехват клавиш (`Kiosk/KioskKeyboard.cs`), действия страницы (`Platform/UiHost.cs`); страница — `Frontend/` |
| `Zafiro.Agent.Tests` | xUnit: unit (без сервера) и интеграционные `[Trait("Category","Integration")]` против Go-сервера |
| `agent/tests/frontend` | тесты страницы UI на Node (`node --test`): мост, деньги в тиынах, формулы минут, переводы, CSP, методы моста |
| `agent/installer` | установщик Inno Setup 6 (`ZafiroAgent.iss`), защита учётной записи клиента (`Protect-ClubPc.ps1`) |

Native AOT: служба публикуется `dotnet publish -r win-x64|linux-x64 -p:PublishAot=true` без предупреждений trim/AOT
(без рефлексии: protobuf вместо JSON для файлов и IPC, source-generated P/Invoke, генератор привязки конфигурации;
WMI не используется). Замер (Linux, AOT): исполняемый файл 12,5 МБ, RSS службы с открытым стримом ≈ 31 МБ.

### 14.2. Архитектура: служба, UI, IPC

```
 Сеанс 0 (SYSTEM)                                   Сеанс пользователя
┌──────────────────────────────────────┐          ┌──────────────────────────┐
│ Zafiro.Agent.Service                 │  IPC     │ UI (WPF + WebView2, A2)  │
│  AgentRuntime ── gRPC/mTLS ──▶ сервер│◀────────▶│ экран входа/блокировки,  │
│  ключ ПК (CNG), токен гостя (память) │ protobuf │ лаунчер, магазин         │
│  журналы команд/событий (ProgramData)│  кадры   │ живёт, пока нужен экран  │
│  UiCoordinator: запуск UI ───────────┼─────────▶│ (screen ≠ NONE)          │
└──────────────────────────────────────┘          └──────────────────────────┘
```

- **Служба** держит единственное соединение HTTP/2 с сервером (стрим `Connect` + unary-вызовы гостя на том же
  канале), выполняет команды, хранит ключ ПК и токен гостя. UI ничего секретного не видит: в ответах
  `Login`/`Register`/`WatchQrLogin` поле `GuestSignIn.token` пустое.
- **IPC** (`ipc.proto`): кадр — 4 байта длины (LE; от UI ≤ 1 МиБ, от службы ≤ 4 МиБ — картинка клуба до 3 МиБ) +
  `Envelope{request|response|event}`. Методы — `agent.GetSnapshot`, `agent.Enroll` (код из панели), `agent.VerifyStaffPin`,
  `agent.GetTranslations`, `agent.ReportActivity`, `agent.SignOut`, `agent.SetLocale` (язык гостя, §15.3),
  `agent.GetImage` (картинки клуба из кэша службы, §15.2; со страницы мостом не вызывается) и все RPC гостя по имени `<Сервис>/<Метод>` (`GuestService/PurchaseTariff`…)
  с теми же сообщениями, что в `proto/agent/v1` (таблица имён и типов — `Zafiro.Agent.Protocol/Ipc/IpcMethods.cs`, общая
  для службы и моста UI; тест сверяет её с `Ipc/IpcDispatcher.cs`). События: `Snapshot` (связь, состояние ПК, нужный
  экран, клуб, сессия с названием тарифа, гость, баланс, часы сервера, автобиллинг, версия агента, язык текстов сервера),
  `ShowMessage`, чат, счётчики, QR, переводы, `SessionEnding`
  (за 10, 5, 3 и 1 минуту до конца сессии — `UiCoordinator`, как `WarnSessionTime` старого агента). Уведомления
  (`ShowMessage`, ответ клуба в чате, `SessionEnding`), пришедшие, пока UI не подключён, служба держит (≤ 16, ≤ 5 минут),
  запускает ради них UI и отдаёт их сразу после первого снимка.
  Ошибки — `Error{error_code, grpc_status, message (LocalizedMessage сервера), retry_after_ms, violations}`.
- **Проверка клиента IPC.** Windows — именованный канал `ZafiroAgent.Ipc`: экземпляры создают только SYSTEM и
  администраторы, подключаются вошедшие пользователи; клиент принимается, если он в активном сеансе консоли, его exe —
  UI из папки установки (`Service:UiExecutable`) и его SID (UI подключается с `TokenImpersonationLevel.Identification`)
  — пользователь этого сеанса или SYSTEM. Linux — unix socket (0600), uid клиента из `SO_PEERCRED` — uid службы или
  `Service:AllowedUids`. Не больше 4 клиентов и 8 запросов на клиента одновременно.
- **UI только когда нужен экран** (решение 5): `AgentState.Screen` — `SETUP` (ПК не зарегистрирован/отозван),
  `LOCK` (никто не играет, гость без сессии, блокировка сотрудником), `NONE` (идёт сессия, техрежим, выключение).
  `UiCoordinator` раз в 3 с: экран нужен (или пришло сообщение сотрудника), а UI не подключён — запуск UI в сеансе
  консоли (`CreateProcessAsUser`, как сторож старого агента; администраторы — только если разрешено установщиком;
  повторы 3 с … 1 мин). При `screen = NONE` UI закрывается сам и освобождает WebView2 (§14.7).

### 14.3. Ключ, сертификат, доверие

- **Корень CA** — только встроенный (pinning): `Agent:RootCaPem`/`RootCaFile`; сервер проверяется
  `X509ChainTrustMode.CustomRootTrust` без системного хранилища и без CRL/OCSP; TLS 1.2+.
- **Windows** (`WindowsCredentialStore`): именованный постоянный машинный ключ CNG ECDSA P-256, `ExportPolicy = None`,
  сначала Microsoft Platform Crypto Provider (TPM), иначе Software KSP. Сертификат и метаданные (`StoredCredentials`) —
  `%ProgramData%\Zafiro\Agent\credentials.bin`, зашифрован DPAPI учётной записи службы (SYSTEM); папка — только SYSTEM и
  администраторы, без наследования, чужая папка/ссылка откладывается (как `DeviceVaultStore` старого агента).
  Хранилище сертификатов `LocalMachine\My` не нужно: сертификат связывается с ключом CNG (`CopyWithPrivateKey`).
- **Linux** (`FileCredentialStore`, тесты): ключ PKCS#8 и сертификат в файле 0600, каталог 0700.
- Данные привязаны к `hardware_uuid`: образ, снятый с другого ПК, считается «не зарегистрирован».
- **Выданный сертификат проверяется** до сохранения: цепочка до встроенного корня (корень из ответа не берётся), срок,
  EKU `clientAuth`, открытый ключ — тот, что в CSR. В TLS уходит сертификат + промежуточный CA
  (`SslStreamCertificateContext`).

### 14.4. Жизненный цикл службы

1. **Старт**: загрузка учётных данных; нет — `CONNECTION_NOT_ENROLLED`, экран регистрации. Бездисковый ПК с
   `Agent:ImageKey` регистрируется сам (Enroll по ключу образа) после каждой загрузки.
2. **Enroll** (`Enroller`): новый ключ → CSR (`CertificateRequest`, SHA-256) → `EnrollmentService.Enroll` по каналу
   без клиентского сертификата. `PENDING_APPROVAL` → `CONNECTION_PENDING_APPROVAL`, повтор тем же ключом через
   `RetryInfo` (по умолчанию 30 с). Ошибка — ключ удаляется.
3. **Connect**: канал mTLS (`SocketsHttpHandler`: PING каждые 30 с, таймаут 10 с, одно соединение без закрытия по
   простою; отправка ≤ 1 МиБ, приём ≤ 8 МиБ; метаданные `zafiro-protocol-version`, `zafiro-agent-version`,
   `zafiro-locale` на каждом вызове — язык гостя, иначе язык клуба из Welcome, иначе `Agent:Locale`, §15.3) → `Hello` (версия, `last_command_seq` из журнала, `boot_id`, железо, возможности,
   статус, версия переводов, язык) → `Welcome` за 30 с (часы сервера, конфигурация, сессия, интервал статуса) →
   `Status` сразу, при каждом изменении состояния (не чаще 1 в 2 с) и не реже `status_interval` → досылка
   неподтверждённых событий и итогов долгих команд.
4. **Команды** (`AgentRuntime.HandleCommandAsync`): повтор `command_id` или `seq ≤ last_command_seq` — не выполняется
   (сохранённый итог или `DUPLICATE`); истёкшая — `COMMAND_EXPIRED`; итог пишется в журнал (`commands.pb`, атомарно,
   последние 256) **до** отправки `CommandResult` — повтор `Reboot` после перезагрузки ПК не перезагружает его снова.
   Действие (перезагрузка, Drain) — после отправки итога. `UpdateAvailable` — `ACCEPTED`, итог позже
   (недоставленные итоги хранятся и досылаются после переподключения).
5. **События** (`events.pb`): журнал на диске до `EventAck`, повтор после переподключения.
6. **Переподключение**: full jitter `random(0, min(60 с, 2^attempt))`, не раньше `RetryInfo`; счётчик сбрасывается
   после стрима ≥ 60 с; `Drain` — случайно в окне; `PROTOCOL_VERSION_UNSUPPORTED` — раз в 10 минут.
7. **Продление**: после `renew_after` (или `Welcome.certificate_renew_after`) + jitter до 24 ч (≤ 90 % оставшегося
   срока): новый ключ → `RenewCertificate` по текущему каналу → новый канал → после первого `Welcome` прежний ключ
   удаляется.
8. **Отзыв / истечение**: `CERTIFICATE_REVOKED` — ключ и сертификат удаляются, гость выходит, `CONNECTION_REVOKED`
   («ПК не подключён к клубу», нужен новый код); `CERTIFICATE_EXPIRED`/`CERTIFICATE_REQUIRED` или истёкший `not_after` —
   данные удаляются, новая регистрация.
9. **Гость** (`GuestApi`): токен — только в памяти службы, `authorization: Bearer` в метаданных; `GUEST_TOKEN_INVALID`
   — выход гостя. Денежные RPC: пустой `idempotency_key` заполняется UUID v7, при `UNAVAILABLE`/`DEADLINE_EXCEEDED`/
   `IDEMPOTENCY_IN_PROGRESS` — до 3 попыток **тем же** ключом. Таймауты: 15 с запись, 5 с чтение.
10. **Локально по часам сервера**: конец сессии (событие `SessionEnded{EXPIRED}`), конец техрежима, выход гостя без
    сессии по бездействию (`idle_logout_minutes`, бездействие сообщает UI; не выводит, пока сервер сообщает, что ждёт
    автобиллинг — §15.1), действие после сессии
    (`after_session_action` + задержка, если гость не купил время).
11. **Обновление**: подпись Ed25519 (`UpdateVerifier`: строка как `commands.UpdateSigningMessage`, ключи — встроенные
    `Service/ServiceOptions.cs` `UpdateKeys`, RFC 8032 — BouncyCastle) → хост из списка → скачивание (≤ size_bytes)
    → размер и SHA-256 по открытому файлу → тихая установка (без `install_now` — только когда ПК свободен). Любое
    несовпадение — файл удаляется, `UPDATE_VERIFICATION_FAILED` + `AgentEvent.update_result`.

Ошибки сервера разбираются в типизированные исключения (`RpcErrors.Parse`): `ErrorCode` из `ErrorInfo.reason`
(неизвестный — `INTERNAL`), `LocalizedMessage` (показывается как есть), `RetryInfo`, `BadRequest`;
`CertificateRevokedException`, `CertificateExpiredException`, `PendingApprovalException`, `RateLimitedException`,
`GuestTokenInvalidException`, `ValidationFailedException`, `ProtocolVersionUnsupportedException`.

### 14.5. Нагрузка на ПК

Без опросов: всё по стриму (push). Фоновые задачи — один таймер 1 с (часы сессии, техрежим, продление) и таймер
статуса (60 с). Одно TCP-соединение, PING раз в 30 с. Native AOT, GC рабочей станции без фонового режима. UI с WebView2
не работает во время сессии: процесс UI запускается ради экрана или уведомления и завершается, когда смотреть нечего
(уведомление в углу — окно WPF без WebView2).

### 14.6. Тесты и сборка

```bash
agent/test.sh unit          # Docker mcr.microsoft.com/dotnet/sdk:10.0: restore --locked-mode, build (и UI через
                            # EnableWindowsTargeting), unit-тесты
agent/test.sh frontend      # тесты страницы UI: node --test agent/tests/frontend/*.test.mjs (Node 22 или контейнер)
agent/test.sh integration   # agent/tests/integration/server.sh: Postgres/Valkey/NATS (compose, tmpfs, свои порты),
                            # goose up, демо-клуб (tools/visual/fixtures/demo.json, подписка продлена), go build,
                            # сервер с dev-PKI; затем тесты Category=Integration (сеть хоста)
agent/test.sh aot           # Native AOT publish службы (linux-x64) без предупреждений → agent/.work/aot
agent/tests/Zafiro.Agent.Tests/TestData/gen-update-vector.sh   # новый вектор подписи (server/cmd/sign-update)
```

Unit: backoff, журнал команд (дедупликация, перезапуск, предел 256), события, подпись обновления против вектора
Go-утилиты `server/cmd/sign-update` (и порча каждого поля), разбор `google.rpc.Status`, состояние ПК и экран,
выполнение команд (повтор перезагрузки, истёкшие, сессия, блокировка, техрежим, WoL), CSR и проверка выданного
сертификата, файловое хранилище (0600, чужое железо), IPC по unix socket (снимок, методы, uid), SMBIOS, WoL.
UI (`UiTests.cs`, без WPF): мост — вызов службы по схеме proto (тиыны строкой int64), ошибка сервера с текстом, отказ
лишнему полю/неверному типу/неизвестному enum/чужому методу/испорченному конверту/слишком большому сообщению без вызова
службы, локальные методы `ui.*`; клиент IPC UI ↔ настоящий `IpcServer` службы по unix socket (снимок, уведомление,
отложенное до подключения, отказ гостю без входа); выбор экрана (`UiController`), предупреждения 10/5/3/1, правила
киоска, аргументы UI, тексты уведомлений, перенос ключа образа (AG-10). Страница (`agent/tests/frontend`): деньги
(int64, показ без float, сумма перевода → тиыны), `minutesPrice`/`affordableMinutes` по GS-053 и PQ-1, переводы и
множественное число, мост (id, ошибки, таймауты, события), телефон; статически — CSP без внешних адресов и inline-скриптов,
нет `eval`/`innerHTML`, все `bridge.call` — из `IpcMethods.cs` или `ui.*`, ключи `i18n/ru|kk|en.json` совпадают.

Интеграционный сценарий (`Integration/ServerScenarioTests.cs`): код регистрации (`POST /api/agent-enrollment/codes`
под владельцем демо-клуба) → Enroll (повтор кода — `ENROLLMENT_CODE_EXPIRED` с текстом) → Connect → Welcome →
уведомление из панели (`/api/club-computers/notify`) доходит `ShowMessage`, итог подтверждён, seq в журнале →
перезапуск службы: команда не приходит второй раз → продление сертификата и переподключение с новым → вход гостя
(неверный пароль — `INVALID_CREDENTIALS`) → тарифы → покупка, повтор с тем же ключом (одно списание),
`IDEMPOTENCY_KEY_REUSED` для другого запроса → `BalanceChanged` от сервера → завершение сессии гостем → выход →
«Отключить ПК» (`/revoke`) закрывает стрим `CERTIFICATE_REVOKED`, ключ удалён.

CI — `.github/workflows/agent.yml`: сборка, unit-тесты и Native AOT publish на `windows-latest` и `ubuntu-latest`,
интеграционные — на ubuntu с тем же `server.sh`.

**Не проверено (нет Windows-машины):** служба Windows, SChannel с ключом CNG/TPM, именованный канал и проверка SID,
запуск UI в сеансе пользователя, SMBIOS/реестр на реальном железе, весь процесс UI (WPF, WebView2, перехват клавиш,
мониторы) и установщик — пункт `stage5-agent.md` AG-1, чек-лист — §14.9.

### 14.7. UI: процесс, мост, безопасность

```
 служба (SYSTEM) ──IPC──▶ Zafiro.Agent.UI.exe (сеанс пользователя)
                          ├─ IpcClient: канал ZafiroAgent.Ipc (Identification), переподключение 0,5…5 с
                          ├─ UiController: Snapshot.screen → вид окна (скрыто | регистрация | блокировка | лаунчер)
                          ├─ MainWindow: WebView2 ── https://app.zafiro.invalid/index.html (папка Frontend)
                          │     postMessage {id, method, params} ──▶ BridgeRouter ──▶ IPC ──▶ служба ──▶ gRPC
                          │     ◀── {id, result | error}, {event: Snapshot…}, {local: staffPin…}
                          ├─ CornerNotice: уведомления в углу (WPF, без WebView2)
                          └─ KioskKeyboard (WH_KEYBOARD_LL), ScreenCover (другие мониторы), сворачивание чужих окон
```

- **Когда UI живёт.** Служба запускает UI, когда нужен экран (`SETUP`, `LOCK`) или есть уведомление; ярлык «Zafiro»
  (`Zafiro.Agent.UI.exe --launcher`) открывает лаунчер во время сессии (если UI уже запущен — сигналом
  `Local\ZafiroAgent.UI.Launcher`; запуск службой этот сигнал не проверяет, чтобы чужой процесс, занявший имя, не мог
  помешать экрану блокировки). WebView2 создаётся вместе с окном и освобождается при его закрытии; окна нет и уведомлений
  нет — процесс завершается. После покупки времени на экране блокировки лаунчер остаётся открытым, пока гость не нажмёт
  «На рабочий стол». Обрыв канала к службе (обновление) — экран блокировки остаётся, поверх — «Нет связи со службой
  агента»; служба так и не ответила за 15 с — UI выходит (службы нет — блокировать нечем).
- **Мост** (`BridgeRouter`): сообщение страницы ≤ 64 КиБ, ровно `{id, method, params}`, id — целое 1…2⁵³−1;
  метод — только из `IpcMethods` (запрос разбирается строго по схеме proto: неизвестное поле, неверный тип, неизвестное
  значение enum — `VALIDATION_FAILED` без вызова службы) или локальный: `ui.ReturnToDesktop`, `ui.Power`
  (`restart|shutdown`, кнопки экрана входа), `ui.InputLanguage` (язык ввода у поля пароля), `ui.QrImage` (только https,
  ≤ 512 символов → PNG data:). JSON — каноническое отображение proto3: lowerCamelCase, int64 (тиыны, id) — строкой с
  целым числом (страница переводит в `Number` только безопасные целые, показ — `Zafiro.formatAmount` без float), enum —
  именем. Ошибка — `ipc.Error` как есть (`errorCode`, `grpcStatus`, `message` — LocalizedMessage сервера, `retryAfterMs`,
  `violations`); страница показывает `message`, иначе — свой запасной текст. Ключ идемпотентности денежных RPC страница
  не передаёт: его создаёт служба (UUID v7) и повторяет тем же ключом.
- **Страница** (`Frontend/`): `index.html`, `styles.css`, `main.js` — лаунчер старого агента без изменений вида
  (вызовы `webview.postMessage({type})` и обработчики ответов старого C# заменены вызовами моста, данные — в новом формате,
  опросов нет: сессия, гость, баланс, настройки — из снимка, чат и счётчики — событиями); `lock.js` + `lock.css` — экран
  входа и регистрации ПК (перенос WPF-окна `ClubSetupWindow`: вход, регистрация, восстановление пароля, QR-вход, кнопки
  питания со вторым нажатием, языки, номер ПК) и PIN сотрудника (`StaffPinWindow`, Ctrl+Alt+P); `app.js` — снимок,
  выбор экрана, переводы; `core.js` — мост, деньги, переводы, телефон (тесты Node). Ключа установки клуба больше нет:
  ПК подключается одноразовым кодом из панели (`agent.Enroll`).
- **Переводы**: ключ — русский текст; строки сервера (`agent.GetTranslations` через службу, при новой версии из
  снимка или `TranslationsUpdated`) поверх встроенных `Frontend/i18n/ru|kk|en.json` (перенос файлов старого агента +
  новые ключи A2). Язык — выбранный гостем, после выхода гостя — язык клуба (`config.default_locale`).
- **Безопасность страницы**: только виртуальный хост `app.zafiro.invalid` → папка `Frontend` (`DenyCors`); навигация
  на другой адрес, фреймы, новые окна, загрузки, запросы разрешений (камера, микрофон…) — отклоняются;
  `WebResourceRequested` отвечает 403 на любой запрос не к виртуальному хосту; CSP в `index.html`:
  `default-src 'none'; script-src 'self'; img-src 'self' data: https://img.zafiro.invalid; connect-src 'self'` (только
  встроенные переводы), без других внешних адресов и inline-скриптов; сообщения моста принимаются только от
  `https://app.zafiro.invalid/`.
  Release: без DevTools, контекстного меню и горячих клавиш браузера; автозаполнение и сохранение паролей выключены.
  Картинки клуба (новости, картинка экрана входа, логотип на закрытых мониторах) — с виртуального хоста
  `img.zafiro.invalid`: процесс UI отвечает на них байтами из службы (IPC `agent.GetImage` → кэш службы →
  `AgentService.GetImage`), сам в сеть не ходит (§15.2). У товаров картинок нет и в старом (заглушка, как было).
- **Киоск** (экран блокировки, нет оплаченной сессии): окно поверх всех, чужие окна на переднем плане сворачиваются
  (раз в 1 с), другие мониторы закрыты чёрными окнами с названием клуба, перехват Win, Alt+Tab, Alt+F4, Ctrl+Esc и др.
  (`KioskRules` — без изменений из старого агента), Alt+F4 окну не закрывает его. Ctrl+Alt+Del перехватить нельзя.
- **Деньги**: суммы на странице — целые тиыны; «На весь баланс» и автобиллинг считает и покупает сервер (§15.1):
  страница показывает `GetMinuteOffer.whole_balance_minutes/price_tiyn`, покупает `GuestService/PlayWholeBalance` и
  показывает отсчёт паузы автобиллинга из снимка (`Snapshot.auto_billing`); своей формулы и своих покупок нет.

### 14.8. Установщик и выпуск

- **Inno Setup 6.5.4** (`agent/installer/ZafiroAgent.iss`; в CI — установщик с GitHub jrsoftware/issrc, SHA-256
  закреплён в `.github/workflows/agent-release.yml`). Выбран вместо WiX: собирается на `windows-latest` без лишних
  инструментов, старый установщик был на нём же (задачи, страницы обновления, `/MERGETASKS="!protect"` тихого обновления
  службой) — AG-14.
- Содержимое (`{app}` = `Program Files\Zafiro Agent`): `Zafiro.Agent.Service.exe` (Native AOT, служба `ZafiroAgent`,
  LocalSystem, автозапуск, перезапуск при сбое), `appsettings.json` (только при первой установке),
  `appsettings.Production.json` (галочка «экран и под администратором», признак бездискового ПК), `root-ca.pem`
  (переменная репозитория `AGENT_ROOT_CA_PEM`, AG-3; служба берёт его по умолчанию), `Zafiro.Agent.UI.exe` + `Frontend\`
  (self-contained), `Protect-ClubPc.ps1` (галочка «Защитить учётную запись клиента»). Ярлык «Zafiro» —
  `Zafiro.Agent.UI.exe --launcher`. Удаление — служба, UI и `ProgramData\Zafiro\Agent` (ключ и сертификат ПК).
- **Ключ образа** (бездисковые ПК, AG-10): `ZafiroAgentSetup.exe /IMAGEKEYFILE=<файл>` → `Zafiro.Agent.Service.exe
  --set-image-key` (ключ — через stdin) → DPAPI LocalMachine в `ProgramData\Zafiro\Agent\image-key.bin` (папка только
  SYSTEM и администраторы). Ключ, оставшийся в `appsettings.json` (`Agent:ImageKey`), служба при старте переносит туда же
  и стирает из файла. Делать в режиме правки образа (супер-рабочая станция), иначе запись пропадёт при перезагрузке.
- **Выпуск** (`agent-release.yml`): job `build` (windows-latest) — unit-тесты .NET и страницы, `dotnet publish`
  службы (`win-x64`, Native AOT, без предупреждений trim/AOT) и UI (`win-x64`, self-contained), `root-ca.pem`,
  Authenticode exe (секреты `AGENT_CODESIGN_PFX`/`AGENT_CODESIGN_PASSWORD`; нет — предупреждение, AG-15), установщик,
  Authenticode установщика; job `sign` (ubuntu) — `server/cmd/sign-update` (sha256, размер, Ed25519, проверка ключом
  сервера) и выпуск `agent-v<версия>`.

### 14.9. Ручная проверка на Windows 10 и 11 (до первого перевода клуба)

Стенд — тот же Go-сервер (`agent/tests/integration/server.sh start`, сервер доступен ПК по сети) или staging;
установщик из `agent-release` (или локальная сборка: `dotnet publish` службы и UI в одну папку, `ISCC.exe`).
Проверить на Windows 10 22H2 и Windows 11 23H2+, с монитором 100 % и 125–150 % масштаба, и на ПК с двумя мониторами.

- [ ] **Установка**: установщик ставит службу `ZafiroAgent` (LocalSystem, автозапуск), ярлык «Zafiro», `root-ca.pem`;
  `Protect-ClubPc.ps1` с галочкой защищает учётную запись Client; повторный запуск — страница «Переустановка»; удаление
  убирает службу и `ProgramData\Zafiro\Agent`.
- [ ] **Регистрация ПК**: после установки в сеансе пользователя открывается экран «Подключение к клубу» (вид — как
  старое окно ClubSetupWindow); неверный код — текст сервера; код из панели — экран входа, номер ПК и зона слева сверху.
- [ ] **Блокировка**: экран входа поверх всех окон на всех мониторах (второй монитор — чёрный с названием клуба);
  Win, Alt+Tab, Alt+F4, Ctrl+Esc, Ctrl+Shift+Esc, Win+Tab не выводят из экрана; запущенная заранее игра сворачивается;
  гость без прав администратора не может закрыть `Zafiro.Agent.UI.exe` (диспетчер задач запрещён); убитый процесс
  (если удалось) служба запускает снова ≤ 3 с; убитый `msedgewebview2.exe` — страница загружается снова.
- [ ] **Вход**: телефон форматируется «700 123-45-67», неверный пароль — текст сервера, вход — лаунчер; регистрация
  (имя, повтор пароля), «Забыли пароль?» (код в Telegram, новый пароль); QR-вход (если включён в клубе): QR виден,
  подтверждение с телефона — вход; кнопка языка ввода меняет раскладку; языки клуба меняют тексты.
- [ ] **Покупка**: тарифы с ценами (тенге, без лишних нулей), выбор баланса (основной/бонусы/смешанно), покупка —
  ПК разблокирован, лаунчер открыт до «На рабочий стол»; поминутно и «На весь баланс» (число минут считает сервер);
  абонемент; завершение сессии — возврат на баланс уведомлением; автобиллинг после паузы (отсчёт «Поминутная оплата
  через m:ss», покупает сервер; гость не выходит по бездействию, пока идёт пауза).
- [ ] **Сессия**: во время игры UI не работает (`Zafiro.Agent.UI.exe` и `msedgewebview2.exe` нет в процессах), ярлык
  «Zafiro» открывает лаунчер; за 10, 5, 3, 1 минуту — уведомления в углу поверх игры; сообщение сотрудника с панели —
  уведомление; ответ клуба в чате — уведомление; конец времени — экран блокировки, отсчёт перезагрузки/выключения, если
  он включён в клубе.
- [ ] **Магазин и прочее**: товары (картинки — заглушки, как в старом), новости с картинками клуба, картинка экрана входа и логотип на
  втором мониторе (AG-13), профиль — тариф и версия агента (AG-17), смена языка — тексты ошибок сервера на этом языке
  (AG-16), корзина, оплата — заказ в панели; друзья, перевод с
  паролем; пересадка; история с фильтрами; профиль (имя, пароль); чат.
- [ ] **Техрежим**: Ctrl+Alt+P — окно PIN; неверный PIN — текст сервера; верный — экран снимается на время из настроек
  клуба, затем блокировка возвращается.
- [ ] **Обновление**: выпуск с подписью Ed25519 из `agent-release` и включённое автообновление — служба скачивает,
  проверяет подпись и SHA-256, ставит тихо (`/VERYSILENT … /MERGETASKS="!protect"`) только когда ПК свободен; UI во время
  обновления показывает «Нет связи со службой агента» и возвращается; регистрация ПК сохраняется; испорченный файл —
  `UPDATE_VERIFICATION_FAILED`, установка не запускается.
- [ ] **Бездисковый ПК**: в режиме правки образа `ZafiroAgentSetup.exe /IMAGEKEYFILE=…` — в `appsettings.json` ключа нет,
  `ProgramData\Zafiro\Agent\image-key.bin` есть (читают только SYSTEM и администраторы); ключ, вписанный в
  `appsettings.json` вручную, после запуска службы стирается из файла; после загрузки клиентского ПК из образа — новый
  MAC попадает в «ожидающие», подтверждённый ПК регистрируется сам после каждой загрузки.
- [ ] **Отключить ПК** в панели: экран «ПК не подключён к клубу»; гость этого ПК сразу может войти на соседнем ПК (AG-5).

## 15. Закрытие пробелов протокола агента (Этап 5, A3)

Вопросы — `docs/open-questions/stage5-agent.md` AG-12, AG-13, AG-16, AG-17. Контракт изменён совместимо (новые
RPC, поля и вид команды; `buf breaking` против прошлого коммита — без поломок).

### 15.1. «На весь баланс» и автобиллинг считает сервер (AG-12, решение Q-008)

- **`GuestService.PlayWholeBalance{idempotency_key}`** → `TimePurchase` (купленные минуты, сумма, сессия с концом).
  `billing/service.PurchaseWholeBalance`: в одной транзакции с блокировкой баланса — минуты
  `pricing.WholeBalanceMinutes` по основному балансу и обычная покупка минут с основного (тот же ledger
  `minutes_purchase`, журнал `time.purchased`, `StartSession`/`BalanceChanged` агенту). Своя операция идемпотентности
  (`minutes_whole_balance`). Ошибки: `FEATURE_DISABLED` (клуб не разрешил), `TARIFF_UNAVAILABLE` (у зоны нет цены часа),
  `INSUFFICIENT_FUNDS` (не хватает на минуту). `GetMinuteOffer` теперь отдаёт `whole_balance_minutes` и
  `whole_balance_price_tiyn` — кнопку и цену страница берёт отсюда. Числа — GS-053 (2000 ₸ при 600 ₸/час — 200 минут).
- **Автобиллинг** — правила старого агента (`WindowsAgent/Frontend/main.js` autoBillingTick, BR-023, GS-055):
  гость вошёл, сессии нет, клуб включил автобиллинг, у зоны есть цена часа, основного баланса хватает на минуту →
  пауза `auto_billing_pause_minutes`, затем покупка минут на весь основной баланс; после конца тарифа по времени —
  сразу; гость сам завершил сессию, её завершили в клубе или перенесли, покупка не удалась — стоп до нового входа.
  Включения/выключения гостем в старом агенте нет (настройка клуба `finances.auto_billing`), «выключение» — только
  этот стоп.
  - Состояние ПК — в Valkey рядом с токеном гостя (`<prefix>ab:<pc>` + ZSET `<prefix>ab:due` «когда пересмотреть»):
    вход гостя (`signIn`/`Register`) — с чистого листа; обёртка уведомлений billing/кассы
    (`guestapi.Server.WrapNotifier`) — начало сессии переносит пересмотр к её концу, `EndSession` с причиной EXPIRED —
    «после тарифа», с другой — стоп, `BalanceChanged` — пересмотр сейчас; воркер истечения сессий и событие агента
    `SessionEnded{EXPIRED}` — «после тарифа».
  - Решение и покупку делает воркер `agent:auto-billing` (`worker.AutoBilling`, тик 1 с, один исполнитель на кластер —
    advisory lock на каждый тик — `leader.RunOnce`, Q-086; постоянно соединение не держит): берёт ПК, срок которых пришёл (не больше 200 за тик), проверяет, что гость всё ещё вошёл
    (токен ПК), и применяет `pricing.AutoBilling`. Покупка — `PurchaseWholeBalance{AutoBilling: true}` (операция
    `minutes_auto_billing`) с ключом `autobill:<ПК>:<гость>:<вход>:<шаг>`: повтор после сбоя (и короткое раздвоение
    лидерства) отвечает из `billing_requests`, второго списания нет. Без денег/выключено — пересмотр через 30 с (и сразу
    после `BalanceChanged`); сбой БД — через 10 с.
  - Агенту — мгновенная команда `Command.auto_billing_changed{user_id, AutoBillingStatus}` при смене состояния
    (`enabled`, `waiting`, `pause_ends_at`, `stopped`); после входа и переподключения служба читает
    `GuestService.GetAutoBilling`. Служба кладёт состояние в `Snapshot.auto_billing` и не выводит гостя по бездействию,
    пока `waiting`; страница показывает «Поминутная оплата через m:ss» по часам сервера. Локальной формулы у агента нет.
  - Тесты: `pricing` (формулы, GS-053/055), `guestapi` интеграционные `TestAG12*` (пауза → покупка 140 минут на 1400 ₸,
    повторный тик без второй покупки, после конца тарифа — сразу, без денег — не ждёт, стоп после завершения гостем и
    новый вход, повтор ключа, `AutoBillingChanged` в стриме), .NET `StageA3Tests`, страница `page.test.mjs`.

### 15.2. Картинки клуба (AG-13)

- **`AgentService.GetImage{kind, id, if_none_match}`** → `{data, content_type, etag, not_modified}` под mTLS: логотип и
  картинка экрана входа — клуба из сертификата ПК (`club_images`), новость — клуба ПК или платформы (`news_images`);
  чужая или удалённая — `NOT_FOUND` + `VALIDATION_FAILED`. Только PNG/JPEG ≤ 3 МиБ, etag — SHA-256. Лимит — как у
  переводов (`agent-translations`).
- **Служба** (`Core/Guest/ImageCache.cs`): кэш на диске `<DataDirectory>/images` (≤ 32 МиБ, старые удаляются),
  свежая (10 минут) — без сервера, иначе запрос с etag; сервер недоступен — из кэша; `NOT_FOUND` — удаляется.
- **UI**: страница ссылается на `https://img.zafiro.invalid/logo|signin|news/<id>`; процесс UI отвечает на эти запросы
  WebView2 (`WebResourceRequested`) байтами из службы (`BridgeRouter.GetImageAsync`, IPC `agent.GetImage`), остальное —
  403 как раньше. Как в старом агенте: картинка клуба на экране входа вместо стандартной (`ClubSetupWindow`
  ShowSignInImage), логотип над названием клуба на закрытых мониторах (`Kiosk/ScreenCover.cs`), картинки новостей;
  нет картинки — встроенная.

### 15.3. Язык текстов сервера (AG-16)

- Гость выбирает язык на экране → страница вызывает `agent.SetLocale` (только языки клуба; сброс — при выходе гостя) →
  служба шлёт его в `zafiro-locale` на каждом вызове; без выбора — язык клуба (`ClubConfig.default_locale`), до Welcome —
  `Agent:Locale`.
- Сервер: перехватчик `guestapi.LocalizeErrors` (подключён в `internal/app`) переводит `LocalizedMessage` (и текст
  статуса, описание поля `BadRequest`) каталогом `i18n` на язык `zafiro-locale`, если он включён, иначе на язык клуба
  ПК. Ключ — русский текст; подстановки (`:computer`, `:time`…) переводятся вместе с ключом — `rpcError` кладёт ключ и
  параметры во внутреннюю деталь `DebugInfo`, которую перехватчик убирает из ответа.

### 15.4. Снимок для профиля (AG-17)

- `ActiveSession.tariff_name` — `session_purchases.name` последней части сессии (тариф, пакет; для поминутной
  оплаты — зона): в `Welcome.session`, `StartSession` (обёртка уведомлений), ответах входа и покупок.
- `Snapshot.agent_version` — версия службы (`Agent:AgentVersion`); профиль лаунчера показывает «Тариф» и «Версия
  агента: …», как `SessionPayload` старого агента (там `TariffName` фактически всегда был пуст — сервер его не
  присылал).

