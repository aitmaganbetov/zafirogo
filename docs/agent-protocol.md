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
| `AgentService` | mTLS | — (VerifyStaffPin — опционально) | стрим `Connect`, переводы, PIN сотрудника |
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
- Сертификат: `CN=pc-<computer_id>`, `O=club-<club_id>`, SAN URI `zafiro://club/<club_id>/computer/<computer_id>`,
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
Исключение — бездисковые ПК: см. `docs/open-questions/api.md`, вопрос «Бездисковые ПК».

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

`idempotency_key` обязателен в: `PurchaseTariff`, `PurchaseMinutes`, `StartPackage`, `EndSession`,
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

## 7. Ошибки

- unary: gRPC-код + `google.rpc.ErrorInfo{reason: "ERROR_CODE_…", domain: "agent.zafiro.kz", metadata}`;
  при валидации — `google.rpc.BadRequest`, при лимитах — `google.rpc.RetryInfo`, текст — `google.rpc.LocalizedMessage`
  (язык из `zafiro-locale`). Go: `google.golang.org/genproto/googleapis/rpc/errdetails` + `status.WithDetails`;
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
