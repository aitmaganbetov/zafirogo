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

**Как CI подписывает выпуск** (реализация CI — отдельная задача):

1. Ключ: `openssl genpkey -algorithm ed25519 -out agent-update-<key_id>.pem` — один раз, офлайн. Приватный ключ
   кладётся только в секрет CI (например `AGENT_UPDATE_SIGNING_KEY`, лучше — KMS с операцией Sign), копия — в
   офлайн-хранилище. Открытый: `openssl pkey -in … -pubout -outform DER | tail -c 32 | base64` — в
   `agent.update.public_keys` сервера и в список ключей агента (`key_id` → ключ) в исходниках агента.
2. Сборка выпуска: установщик → `sha256` (hex, нижний регистр) и `size_bytes` (байты) файла.
3. Строка подписи (UTF-8, `\n` — LF, без завершающего перевода строки):
   `zafiro-agent-update/v1\n<version>\n<sha256>\n<size_bytes>\n<channel>`.
4. Подпись: `openssl pkeyutl -sign -inkey key.pem -rawin -in message.txt -out sig.bin` (64 байта, Ed25519 «чистый»,
   без предхеширования). Проверка в том же job: `openssl pkeyutl -verify -pubin -inkey pub.pem -rawin -in message.txt
   -sigfile sig.bin`.
5. Метаданные выпуска (`version`, `url`, `sha256`, `size_bytes`, `channel`, `key_id`, `signature_ed25519` в base64)
   публикуются вместе с установщиком; сервер берёт их в команду `UpdateAvailable` как есть.
6. Ротация: новый ключ `key_id` добавляется в агента выпуском, подписанным старым ключом, и в
   `agent.update.public_keys` сервера; старый убирается после перехода всех агентов.

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
  подписка для закрытия стрима `Connect` отозванного ПК (агент A3).
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
Отзыв сертификата — `stream.Service.Kick(..., codes.Unauthenticated, ERROR_CODE_CERTIFICATE_REVOKED, …)`.

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
под стримы (без TLS; TLS 1.3 добавляет память на соединение — замерить на стенде с mTLS), ≈ 0,5 ядра суммарно на статусы.
Postgres — только переходы (подключения, смены состояния, команды), не статусы. Узкое место массового переподключения —
чтение карточки ПК при `Hello` (1 запрос) и запись `online` после офлайна; `Drain` с окном 30 с растягивает 50 000
подключений до ≈ 1 700/с на кластер.
