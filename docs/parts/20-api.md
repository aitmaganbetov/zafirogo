# API Zafiro (Laravel): инвентаризация маршрутов, агент ПК, интеграции

Часть документа `docs/MIGRATION.md` (субагент «API и агент», Этап 0).
Источник — `/home/user/Zafiro` (только чтение). Все пути файлов ниже — относительно корня старого репозитория.

Итого маршрутов: **90** — `routes/api.php` **49**, `routes/api_staff.php` **24**, `routes/api_v1.php` **17**.

Новый контракт агента и гостя: `proto/agent/v1/*.proto`, описание — `docs/agent-protocol.md`.
Открытые вопросы — `docs/open-questions/api.md`.

## 1. Общая обвязка (`bootstrap/app.php`, `app/Providers/AppServiceProvider.php`)

- [ ] Префиксы: `routes/api.php` подключён стандартно с префиксом `/api` (`bootstrap/app.php`, `withRouting(api: …)`, строка 31); внутри него `routes/api_staff.php` — префикс `/api/staff`, имя `api.staff.` (`routes/api.php`, строки 174–177); `routes/api_v1.php` — префикс `/api/v1`, имя `api.v1.` (`routes/api.php`, строки 180–183). Health-check Laravel — `/up` (`bootstrap/app.php`, строка 33).
- [ ] Группа `api` дополнена `SetLocale` (`bootstrap/app.php`, строка 61); ко всем запросам добавлен `RecordRequestMetrics` (строка 59).
- [ ] Алиасы middleware (`bootstrap/app.php`, строки 37–51): `current-club` → `SetCurrentClub`, `club-role` → `RequireClubRole`, `club-can` → `RequireClubPermission`, `subscription` → `RequireActiveSubscription` (HTTP 402 при неактивной подписке), `agent.device` → `AuthenticateAgentDevice`, `api.log`/`api.size`/`api.key`/`api.scope`/`api.idempotent`/`api.limit` → `app/Http/Middleware/Api/*`.
- [ ] Порядок приоритета: `SetCurrentClub` после аутентификации; для `/api/v1` — `LogApiRequest`, `LimitApiRequestSize`, `AuthenticateApiKey`, `ApiRateLimit` раньше `ThrottleRequests` и route model binding (`bootstrap/app.php`, строки 52–58), чтобы привязки моделей шли уже в клубе ключа.
- [ ] Ошибки: для `api/*` всегда JSON (`shouldRenderJsonWhen`, строка 66); для `api/v1/*` — единый формат `App\Support\ApiError::fromThrowable` (строка 69).
- [ ] `auth:sanctum` — токены Sanctum (`personal_access_tokens`), ability `club:<id>`, срок 30 дней (`AuthController::issueUserLoginResponse`, ~строки 97, 114–118; `AgentRegisterController`; `QrLogins::poll`). Клуб запроса — `SetCurrentClub` (заголовок `X-Club-Id` только среди клубов токена; см. `docs/integration-api.md`, раздел 1).
- [ ] `agent.device` (`app/Http/Middleware/AuthenticateAgentDevice.php`): заголовок `X-Agent-Token` (`zag_` + 48 символов, в БД `club_computers.agent_token_hash` = sha256, `ClubComputerSessions::findByAgentToken`, ~строка 253); опционально `X-Agent-Device` (UUID или `MAC-AABBCCDDEEFF`) — несовпадение с ПК → 401 `device_mismatch`; неактивная подписка → 402; ставит клуб, UTC, язык ПК (`LocaleResolver::forAgentRequest`), атрибут `agent_computer`. Неверный токен → 401 `invalid_agent_token`.

### 1.1. Именованные лимиты `throttle:<имя>` (`AppServiceProvider::boot`, строки 47–97)

Ключ «ПК» (`agentRateLimitKey`, строки 126–138): sha256 от `X-Agent-Token`, иначе id Sanctum-токена, иначе IP.

| Имя | Лимит | Ключ |
|---|---|---|
| `club-computers` | 60/мин | ПК |
| `agent-enroll` | 5/мин и 300/мин | (IP + sha256(device_id)) и IP |
| `agent-exit-pin` | 5 за 5 мин и 100/час | ПК и клуб ПК |
| `customer-login` | 5/мин и 300/мин | (IP + цифры телефона) и IP |
| `password-recovery` | 10/мин и 30/мин | (IP + телефон) и IP |
| `staff-app` | 240/мин | пользователь (или IP) |
| `club-resolve` | 10/мин и 300/мин | (IP + sha256(ключ клуба)) и IP |
| `agent-register` | 5 за 10 мин и 60 за 10 мин | ПК и IP |
| `agent-qr-login` | 20/мин | ПК |
| `agent-qr-poll` | 60/мин | ПК |
| `agent-token-rotate` | 10/мин | ПК |
| `agent-translations` | 600/мин | IP |
| `club-computer-registration` | 10/мин | ПК |

`throttle:N,M,<префикс>` — N запросов за M минут с отдельным счётчиком на маршрут (ключ — пользователь, без входа — IP; стандарт Laravel `ThrottleRequests`).

### 1.2. Middleware `/api/v1` (`app/Http/Middleware/Api/*`, `config/integrations.php`)

- [ ] `api.log` — журнал запросов `LogApiRequest` (хранение 90 дней, `request_log_retention_days`).
- [ ] `api.size` — тело ≤ 256 КБ (`LimitApiRequestSize`, `max_request_kb`).
- [ ] `api.key` — ключ `Authorization: Bearer zfk_…` или `X-Api-Key` (`AuthenticateApiKey::presentedToken`, ~строка 82); 20 неудачных попыток/мин на IP → 429 `too_many_failed_attempts`; `allowed_ips` → 403 `ip_not_allowed`; маршрут без `api.scope:*` недоступен (default deny, `declaresScope`, ~строка 96); `X-Club-Id` чужого клуба → `tenant_override_forbidden`; кэш метаданных ключа 300 с.
- [ ] `api.scope:<scope>` — права ключа (`config/integrations.php` → `scopes`): `products:read`, `products:write`, `stock:write`, `sales:read`, `computers:read`, `bookings:write`, `computers:control` (опасное), `tariffs:read`, `members:read` (опасное, персональные данные); `api.scope:any` — любой ключ.
- [ ] `api.idempotent[:required]` — заголовок `Idempotency-Key` (`^[A-Za-z0-9_:\-]{1,100}$`), хранение ответа 24 ч в `api_idempotency_keys`, повтор с другим телом → 422 `idempotency_key_reused`, параллельный → 409 `idempotency_in_progress`, без заголовка при `required` → 428; ответы ≥ 500 не сохраняются (`ApiIdempotency.php`).
- [ ] `api.limit` — фиксированное окно 1 мин: на ключ `rate_limit_per_minute` (по умолчанию 120, максимум 1000, env `API_KEY_RATE_LIMIT`, `API_KEY_RATE_LIMIT_MAX`) и на клуб 600 (`API_CLUB_RATE_LIMIT`); заголовки `X-RateLimit-*`, `Retry-After` (`ApiRateLimit.php`).

## 2. `routes/api.php` (49 маршрутов)

Сокращения: **SCS** = `auth:sanctum, current-club, subscription`; **AD** = `agent.device`.
«Новый RPC» — куда переходит в `proto/agent/v1` (подробная таблица — `docs/agent-protocol.md`).

### 2.1. Установка, вход, профиль аккаунта

- [ ] `POST /api/clubs/resolve` — `api.clubs.resolve` — `Api\ClubInstallationController` (`__invoke`) — `throttle:club-resolve` — по ключу установки (64 символа) возвращает `club_id`, `club_name`, `api_base_url`, `login_endpoint` (`App\Services\ClubInstallationKey::resolve`). Вызывает: агент `ZafiroApiClient.ResolveClubAsync` (`WindowsAgent/ZafiroAgent/Services/ZafiroApiClient.cs`, строки 95–114) из `ClubSetupService.cs` (строки 80, 136). Новый: `EnrollmentService.Enroll` (одноразовый код). (`routes/api.php`, строка 29)
- [ ] `GET /api/me` — `api.me` — `Api\AuthController@summary` — SCS — профиль и балансы гостя (`user_id, name, phone, club_id, balance, bonus_balance, currency`; суммы float). Вызывает: агент `HttpApiService.GetSessionInfoAsync` (`Services/HttpApiService.cs`, строки 34–45) из `MainWindow.xaml.cs` (строки 197, 810) — обновление баланса не чаще раза в 20 с (`BalanceRefreshInterval`, строка 63). Новый: `GuestService.GetProfile` + команда `BalanceChanged`. (строка 33)
- [ ] `POST /api/agent/user/login` — `api.agent.user.login` — `Api\AuthController@agentUserLogin` — `throttle:customer-login` — вход гостя на ПК по телефону/паролю + `club_key`/`club_id`; один ПК на клиента (409 `signed_in_elsewhere`, `App\Services\AgentSignIns`), повторный вход на ПК удаляет прежний токен с тем же `device_name`; ответ `access_token` (30 дней), `club_id`, `club_name`, `user`. Вызывает: агент `ZafiroApiClient.LoginAsync` (строки 116–155); если ключ клуба хранит служба — запрос идёт через службу (`VaultServer.RelayAsync`, ключ подставляется в тело). Новый: `GuestAuthService.Login`. (строка 37)
- [ ] `POST /api/auth/login` — `api.auth.login` — `Api\AuthController@login` — `throttle:customer-login` — то же для мобильных приложений: дополнительно `token` и список клубов `clubs`. Вызывают: `mobileclient/lib/api/api_client.dart`, `mobileemployee/lib/api/api_client.dart`; агент — только как запасной путь, если `/agent/user/login` вернул 404 (`ZafiroApiClient.cs`, строки 132–133). Новый: не переносится в контракт агента (мобилки отложены). (строка 42)
- [ ] `POST /api/auth/password/code` — `api.auth.password.code` — `Api\PasswordRecoveryController@code` — `throttle:password-recovery` — код в Telegram (Telegram Gateway, `App\Services\PasswordRecovery::sendCode`; 5 кодов/час, повтор через 60 с, код 5 мин). Вызывают: агент `ClubSetupService.cs` (строка 374, `PostPublicAsync`), `mobileemployee`. Новый: `GuestAuthService.RequestPasswordResetCode`. (строка 46)
- [ ] `POST /api/auth/password/reset` — `api.auth.password.reset` — `Api\PasswordRecoveryController@reset` — `throttle:password-recovery` — новый пароль по 6-значному коду (`PasswordRecovery::reset`, 5 попыток). Вызывают: агент `ClubSetupService.cs` (строка 380), `mobileemployee`. Новый: `GuestAuthService.ResetPassword`. (строка 49)
- [ ] `GET /api/auth/me` — `api.auth.me` — `Api\AuthController@me` — SCS — пользователь, роль, скидка, балансы. Вызывает: `mobileclient`. Новый: (мобилки отложены). (строка 55)
- [ ] `POST /api/auth/logout` — `api.auth.logout` — `Api\AuthController@logout` — `auth:sanctum, current-club` — удаляет текущий токен, пишет `auth.api_logout` в журнал. Вызывают: агент `ClubSetupService.cs` (строка 233, `SendAuthorizedAsync`), `mobileclient`, `mobileemployee`. Новый: `GuestAuthService.Logout`. (строка 57)
- [ ] `GET /api/auth/clubs` — `api.auth.clubs` — `Api\AuthController@clubs` — `auth:sanctum, current-club, throttle:30,1,api.auth.clubs` (без проверки подписки) — клубы аккаунта. Вызывает: `mobileclient`. Новый: — (мобилки). (строка 60)
- [ ] `POST /api/auth/switch-club` — `api.auth.switch-club` — `Api\AuthController@switchClub` — `auth:sanctum, current-club, throttle:10,1,api.auth.switch-club` — новый токен другого клуба. Вызывают: `mobileclient`, `mobileemployee`. Новый: — (мобилки). (строка 61)

### 2.2. ПК: heartbeat, регистрация, токен устройства

- [ ] `POST /api/club-computers/{computer}/heartbeat` — `api.club-computers.heartbeat` — `ClubComputerController@heartbeat` — SCS, `throttle:club-computers` — heartbeat токеном пользователя (агенты до 1.1); если у ПК есть токен устройства → 403 `use_device_token`; клиент может сообщать только `state, session_id, session_ends_at, agent_version` (`app/Http/Controllers/ClubComputerController.php`, строки 274–292). Вызывает: агент `ZafiroApiClient.SendHeartbeatAsync` без токена устройства (строки 512–560). Новый: `AgentService.Connect` (Status). (строка 65)
- [ ] `POST /api/agent/heartbeat` — `api.agent.heartbeat` — `ClubComputerController@agentHeartbeat` — AD, `throttle:club-computers` — главный heartbeat: `ClubComputerSessions::heartbeat`; при `commands=true` выдаёт `power_command` (`restart|shutdown|service`, `ComputerPower::take`), `wake_macs`, `staff_message`, `staff_minutes`; при `can_update=true` — `agent_update {version,url,sha256}` (`AgentUpdate::offerFor`); при Bearer-токене гостя — `customer {balance, bonus_balance, friend_requests, chat_last_staff_id}` (`customerState`, строки 335–355); блок `club` (название, лого, языки, `after_session`, `guest_features`, `idle_logout_minutes`), `session`, `session_expired`, `server_time`, `recommended_interval_seconds` (строки 301–333, 434–491). Вызывают: агент `HeartbeatService` (через `SendHeartbeatAsync` с токеном), служба `VaultServer.KeepOnlineAsync`/`CheckAsync`/`ProbeAsync` (`Service/VaultServer.cs`, строки 294–443), экран входа `ClubSetupService.CheckGuestSessionAsync` (строки 318–335) и `ReportFreshBootAsync` (строки 390–410). Новый: `AgentService.Connect` (Hello/Status/Welcome/команды). (строка 70)
- [ ] `POST /api/agent/token/rotate` — `api.agent.token.rotate` — `ClubComputerController@rotateAgentToken` — AD, `throttle:agent-token-rotate` — выдаёт новый `agent_token`, старый перестаёт работать (`ClubComputerSessions::issueAgentToken`). Вызывает: служба `VaultServer.TryRotateAsync` (строки 268–290) сразу после сохранения токена. Новый: `EnrollmentService.RenewCertificate`. (строка 75)
- [ ] `GET /api/agent/translations/{locale}` — `api.agent.translations` — `Api\AgentTranslationController` — `throttle:agent-translations`, `locale` = `[a-z]{2,8}`, без аутентификации — строки группы `agent` с правками суперадмина + `version`, `Cache-Control: public, max-age=300` (`App\Services\TranslationCatalog`). Вызывает: `ZafiroApiClient.GetAgentTranslationsAsync` (строки 63–79) из `MainWindow.xaml.cs` (строка 643), когда `translations_version` в heartbeat новее. Новый: `AgentService.GetTranslations` + команда `TranslationsChanged`. (строка 80)
- [ ] `POST /api/agent/enroll` — `api.agent.enroll` — `Api\AgentEnrollController` — `throttle:agent-enroll` — ключ клуба + `device_id` + MAC → токен ПК; неизвестный ПК добавляется в клуб в зону «Не назначена» от имени владельца (`ClubComputerRegistrar::find/register/adoptMac`), лимит тарифа → 422 `computer_not_added`. Вызывает: служба `VaultServer.EnrollAsync` (строки 358–399), повтор через 10 мин (1 мин после 429/5xx). Новый: `EnrollmentService.Enroll`. (строка 86)
- [ ] `POST /api/club-computers/resolve` — `api.club-computers.resolve` — `ClubComputerController@resolve` — SCS, `throttle:club-computers` — найти ПК по MAC/имени/UUID (`ClubComputerResolver::resolve`); сотруднику с `claim=true` — `agent_token` + «визит сотрудника» (`startStaffVisit`, журнал `agent.staff_signin`). 404 `computer_not_found`. Вызывают: `ClubSetupService.cs` (строка 191), `HeartbeatService.cs` (строка 339). Новый: `EnrollmentService.Enroll` (код вместо входа сотрудника). (строка 112)
- [ ] `POST /api/club-computers/register` — `api.club-computers.register` — `ClubComputerController@register` — SCS, `throttle:club-computer-registration` — сотрудник (роли owner/admin/manager/operator, `ClubComputerPolicy::register`) регистрирует ПК (`ClubComputerRegistrar::register`), 201/200 + `agent_token`. Вызывают: `ClubSetupService.cs` (строка 193), `HeartbeatService.cs` (строка 344). Новый: `EnrollmentService.Enroll`. (строка 116)

### 2.3. Агент: QR-вход, регистрация гостя, PIN, сессия

- [ ] `POST /api/agent/qr-login` — `api.agent.qr-login.store` — `Api\AgentQrLoginController@store` — AD, `throttle:agent-qr-login` — QR-код входа (функция `qr_login` клуба, иначе 403 `qr_disabled`): `id`, `url` (`/pc-login/{code}`), `expires_at` (5 мин), `poll_interval_seconds: 3` (`App\Services\QrLogins::create`). Вызывает: `ClubSetupService.cs` (строка 275) из `Views/ClubSetupWindow.xaml.cs` (строка 885). Новый: `GuestAuthService.CreateQrLogin`. (строка 91)
- [ ] `POST /api/agent/qr-login/poll` — `api.agent.qr-login.poll` — `Api\AgentQrLoginController@poll` — AD, `throttle:agent-qr-poll` — статус `pending|expired|approved` (+ токен гостя, `QrLogins::poll`). Вызывает: `ClubSetupService.cs` (строка 296) из `ClubSetupWindow.xaml.cs` (строка 904), каждые 3 с. Новый: `GuestAuthService.WatchQrLogin` (server-streaming). (строка 94)
- [ ] `POST /api/agent/register` — `api.agent.register` — `Api\AgentRegisterController` — AD, `throttle:agent-register` — регистрация клиента на ПК (имя, телефон +7…, пароль ≥ 6); существующий аккаунт Zafiro вступает в клуб со своим паролем; создаёт `club_memberships` (client), `members_balances` (0/0), токен 30 дней. Вызывает: `ClubSetupService.cs` (строка 357). Новый: `GuestAuthService.Register`. (строка 99)
- [ ] `POST /api/agent/exit-pin` — `api.agent.exit-pin` — `Api\AgentExitPinController` — AD, `throttle:agent-exit-pin` — проверка PIN выхода сотрудника (`clubs.agent_exit_pin_hash`), 409 `pin_not_configured`, 422 `invalid_pin`; при успехе «визит сотрудника» `startStaffVisit(..., 'pin')` → `staff_minutes`; журнал `agent.staff_exit`/`agent.exit_pin_failed`. Вызывает: `ZafiroApiClient.VerifyExitPinAsync` (строки 169–202) из `App.xaml.cs` (строка 236). Новый: `AgentService.VerifyStaffPin`. (строка 103)
- [ ] `POST /api/club-computers/{computer}/session/end` — `api.club-computers.session.end` — `ClubComputerController@endSession` — SCS, `throttle:club-computers`, `computer` числовой — досрочное завершение сессии владельцем или сотрудником (`ClubComputerSessions::end` → `SessionSettlement`), ответ `refunded`, `refunded_bonus` (строки), `kept_minutes`. Вызывает: `HttpApiService.EndSessionWithResultAsync` (строки 161–195) из `MainWindow.xaml.cs` (строка 853). Новый: `GuestService.EndSession` (+ команда `EndSession`). (строка 107)

### 2.4. Товары, тарифы, функции гостя

- [ ] `GET /api/products` — `api.products.index` — `Api\ProductController` — SCS, `throttle:60,1,api.products.index` — товары клуба по имени, пагинация 50 (`ProductResource`, цена float). Вызывает: `HttpApiService.GetProductsAsync` (строки 91–112) из `MainWindow.xaml.cs` (строка 1194). Новый: `ShopService.ListProducts`. Расхождение: `docs/integration-api.md` (раздел 1) пишет «без пагинации», а код использует `paginate(50)`. (строка 120)
- [ ] `GET /api/tariffs` — `api.tariffs.index` — `Api\AgentTariffController@index` — SCS, `throttle:60,1,api.tariffs.index` — тарифы зоны ПК (`?computer_id`), иначе зоны ПК текущей сессии (`AgentTariffPurchaseService::availableFor`). Вызывает: `HttpApiService.GetTariffsAsync` (строки 65–89) из `MainWindow.xaml.cs` (строка 833). Новый: `GuestService.ListTariffs`. (строка 125)
- [ ] `POST /api/tariffs/{tariff}/purchase` — `api.tariffs.purchase` — `Api\AgentTariffController@purchase` — SCS, `throttle:10,1,api.tariffs.purchase` — покупка тарифа на ПК (`computer_id`, `balance` main|bonus); если у ПК есть токен устройства, нужен совпадающий `X-Agent-Token`, иначе 403 `wrong_computer` (`AgentTariffPurchaseService::purchase`). Вызывает: `HttpApiService.PurchaseTariffAsync` (строки 119–151) из `MainWindow.xaml.cs` (строка 1211). Новый: `GuestService.PurchaseTariff` (с обязательным `idempotency_key`). (строка 128)
- [ ] `GET /api/guest/friends` — `api.guest.friends.index` — `Api\GuestFriendController@index` — SCS, `throttle:60,1,…` — друзья/входящие/исходящие + `features` (`App\Services\Friends::overview`). Вызывают: агент `MainWindow.xaml.cs` (строка 993, `CallGuestApiAsync`), `mobileclient`. Новый: `SocialService.ListFriends`. (строка 134)
- [ ] `POST /api/guest/friends` — `api.guest.friends.store` — `GuestFriendController@store` — SCS, `throttle:10,1,…` — заявка по телефону (`Friends::request`), функция `friends`. Вызывают: агент (строка 998), `mobileclient`. Новый: `SocialService.AddFriend`. (строка 135)
- [ ] `POST /api/guest/friends/{friendship}/accept` — `api.guest.friends.accept` — `GuestFriendController@accept` — SCS, `throttle:30,1,…`. Вызывают: агент (строка 1003), `mobileclient`. Новый: `SocialService.AcceptFriend`. (строка 136)
- [ ] `POST /api/guest/friends/{friendship}/remove` — `api.guest.friends.remove` — `GuestFriendController@remove` — SCS, `throttle:30,1,…`. Вызывают: агент (строка 1003), `mobileclient`. Новый: `SocialService.RemoveFriend`. (строка 137)
- [ ] `POST /api/guest/friends/{friendship}/transfer` — `api.guest.friends.transfer` — `GuestFriendController@transfer` — SCS, `throttle:10,1,…` — перевод с паролем аккаунта (`Friends::transfer`, лимит 1 000 000 за перевод, `MAX_TRANSFER_CENTS`), функция `balance_transfer`. Вызывает: агент (строка 1012). Новый: `SocialService.TransferBalance` (password). (строка 138)
- [ ] `POST /api/guest/friends/{friendship}/transfer/code` — `api.guest.friends.transfer.code` — `GuestFriendController@transferCode` — SCS, `throttle:5,1,…` — код подтверждения в Telegram (`FriendTransferCodes::send`). Вызывает: `mobileclient`. Новый: `SocialService.RequestTransferCode`. (строка 140)
- [ ] `POST /api/guest/friends/{friendship}/transfer/confirm` — `api.guest.friends.transfer.confirm` — `GuestFriendController@transferConfirm` — SCS, `throttle:10,1,…` — перевод по коду (`FriendTransferCodes::confirm`). Вызывает: `mobileclient`. Новый: `SocialService.TransferBalance` (telegram_code). (строка 141)
- [ ] `GET /api/guest/minutes` — `api.guest.minutes` — `Api\GuestMinutesController@show` — SCS, `throttle:60,1,…` — цена часа зоны ПК, бонусы, смешанная оплата, весь баланс, автобиллинг (`PaymentRules`, `AgentTariffPurchaseService::hourlyPrice`). Вызывает: агент (строка 1047). Новый: `GuestService.GetMinuteOffer`. (строка 143)
- [ ] `POST /api/guest/minutes/purchase` — `api.guest.minutes.purchase` — `GuestMinutesController@purchase` — SCS, `throttle:10,1,…` — покупка 1..720 минут (`purchaseMinutes`), проверка `X-Agent-Token` как у тарифа. Вызывает: агент (строка 1059; и автобиллинг из `Frontend/main.js`, `purchaseMinutes`). Новый: `GuestService.PurchaseMinutes`. (строка 144)
- [ ] `GET /api/guest/packages` — `api.guest.packages` — `Api\GuestPackageController@index` — SCS, `throttle:60,1,…` — сохранённые пакеты (`packagesFor`). Вызывает: агент (строка 1085). Новый: `GuestService.ListPackages`. (строка 146)
- [ ] `POST /api/guest/packages/{package}/start` — `api.guest.packages.start` — `GuestPackageController@start` — SCS, `throttle:10,1,…` — запуск пакета на ПК (`startPackage`), проверка `X-Agent-Token`. Вызывает: агент (строка 1096). Новый: `GuestService.StartPackage`. (строка 147)
- [ ] `GET /api/guest/chat` — `api.guest.chat` — `Api\GuestChatController@index` — SCS, `throttle:60,1,…` — последние 100 сообщений (или `after`), помечает ответы сотрудников прочитанными. Вызывает: агент (строка 1032) — опрос каждые 5 с при открытом чате, иначе раз в 30 с без счётчиков сервера (`Frontend/main.js`, строки 1203–1206). Новый: `SocialService.ListChatMessages` + команда `ChatMessageReceived`. (строка 148)
- [ ] `POST /api/guest/chat` — `api.guest.chat.store` — `GuestChatController@store` — SCS, `throttle:20,1,…` — сообщение клубу (≤ 1000, `ChatMessage::MAX_LENGTH`). Вызывает: агент (строка 1039). Новый: `SocialService.SendChatMessage`. (строка 149)
- [ ] `GET /api/guest/telegram` — `api.guest.telegram` — `Api\GuestTelegramController@show` — SCS, `throttle:30,1,…` — заглушка `{available:false, linked:false}` для агентов до 0.0.57. Вызывающих в текущем коде нет. Новый: не переносится. (строка 150)
- [ ] `GET /api/guest/news` — `api.guest.news` — `Api\GuestNewsController` — SCS, `throttle:30,1,…` — до 10 новостей платформы и 10 новостей клуба. Вызывают: агент (строка 1043; `Frontend/main.js` раз в 10 мин, строка 1162), `mobileclient`. Новый: `GuestService.ListNews`. (строка 151)
- [ ] `POST /api/guest/shop/purchase` — `api.guest.shop.purchase` — `Api\GuestShopController@purchase` — SCS, `throttle:10,1,…` — заказ товаров к ПК с основного баланса (`App\Services\AgentShop::purchase`; ≤ 30 строк, ≤ 50 шт.), проверка `X-Agent-Token`, 403 `shop_disabled`. Вызывает: агент (строка 1149). Новый: `ShopService.PlaceOrder`. (строка 152)
- [ ] `POST /api/guest/profile/name` — `api.guest.profile.name` — `Api\GuestProfileController@name` — SCS, `throttle:10,1,…`. Вызывает: агент (строка 1018). Новый: `GuestService.UpdateName`. (строка 154)
- [ ] `POST /api/guest/profile/password` — `api.guest.profile.password` — `GuestProfileController@password` — SCS, `throttle:5,1,…` — смена пароля с текущим, остальные токены удаляются. Вызывает: агент (строка 1027). Новый: `GuestService.ChangePassword`. (строка 155)
- [ ] `GET /api/guest/history` — `api.guest.history` — `Api\GuestHistoryController` — SCS, `throttle:60,1,…` — журнал гостя (`App\Services\GuestHistory::page`, 30 на страницу, фильтры purchases/top_ups/transfers/visits). Вызывают: агент (строка 1129), `mobileclient`. Новый: `GuestService.ListHistory`. (строка 156)
- [ ] `GET /api/guest/seats` — `api.guest.seats.index` — `Api\GuestSeatController@index` — SCS, `throttle:30,1,…` — карта для пересадки (`SeatTransfers::map`), функция `self_transfer`. Вызывает: агент (строка 1158). Новый: `GuestService.GetSeatMap`. (строка 157)
- [ ] `POST /api/guest/seat-transfer` — `api.guest.seat-transfer` — `GuestSeatController@store` — SCS, `throttle:10,1,…` — пересадка (`SeatTransfers::transfer`), проверка `X-Agent-Token`. Вызывает: агент (строка 1163). Новый: `GuestService.TransferSeat`. (строка 158)
- [ ] `GET /api/guest/qr-login/{code}` — `api.guest.qr-login.show` — `Api\GuestQrLoginController@show` — SCS, `throttle:30,1,…`, `code` = 40 символов — что подтверждается (ПК, клуб); 410 `qr_expired`, 403 `other_club`. Вызывает: `mobileclient`. Новый: — (мобилки; в вебе — `/pc-login/{code}`). (строка 160)
- [ ] `POST /api/guest/qr-login/{code}` — `api.guest.qr-login.approve` — `GuestQrLoginController@approve` — SCS, `throttle:10,1,…` — подтверждение входа на ПК с телефона (`QrLogins::approve`). Вызывает: `mobileclient`. Новый: — (мобилки); сервер шлёт агенту `QrLoginApproved`. (строка 161)
- [ ] `GET /api/settings/{section}/{key}` — `api.settings.show` — `SettingsController@showSetting` — SCS, `throttle:60,1,api.settings.show` — значение одной настройки клуба любой роли (и `ip` = host). Вызывающих в агенте и мобилках не найдено; покрыт `tests/Feature/SettingsTest.php`. Новый: не переносится (см. open-questions). (строка 164)

## 3. `routes/api_staff.php` (24 маршрута, мобильное приложение сотрудника)

Общие middleware группы (`routes/api.php`, строки 174–177): SCS + `club-role:owner,admin,manager,cashier,operator` (`App\Support\ClubRoles::STAFF`) + `throttle:staff-app` (240/мин на сотрудника). Описание контракта — `docs/staff-api.md`. Все маршруты вызывает только `mobileemployee/lib/api/api_client.dart`. Мобилки отложены — в контракт агента не входят; при переносе — REST по OpenAPI (решение владельца, см. open-questions).

- [ ] `GET /api/staff/me` — `api.staff.me` — `Api\Staff\MeController@show` — профиль, права, функции, уведомления, смена (`ClubNotifications`, `CashRegisterService`). (строка 22)
- [ ] `GET /api/staff/computers` — `api.staff.computers.index` — `Api\Staff\ComputerController@index` — карта ПК с бронями и версиями агента (`ComputerReservations`, `AgentUpdate`). (строка 25)
- [ ] `POST /api/staff/computers/control` — `api.staff.computers.control` — `ClubComputerController@control` — `club-can:pc_control.update` — бронь/включение/перезагрузка/выключение (`ComputerPower`, вебхук `computer.controlled`). (строка 26)
- [ ] `POST /api/staff/computers/{computer}/end-session` — `api.staff.computers.end-session` — `ClubComputerController@endSession` — `club-role:owner,admin,manager,operator` (`ClubComputerSessions::STAFF_ROLES`). (строки 27–28)
- [ ] `POST /api/staff/computers/guest-session` — `api.staff.computers.guest-session` — `GuestSessionController@store` — `club-can:cash.create`, `throttle:30,1,api.staff.guest-session` — сессия без аккаунта с кассы (`App\Services\GuestSessions`). (строки 29–30)
- [ ] `GET /api/staff/cash` — `api.staff.cash.show` — `Api\Staff\CashController@show` — `club-can:cash.view`. (строка 34)
- [ ] `POST /api/staff/cash/shifts` — `api.staff.cash.shifts.open` — `CashController@open` — `club-can:cash.view` + `club-can:cash.create`. (строка 36)
- [ ] `POST /api/staff/cash/shifts/{shift}/close` — `api.staff.cash.shifts.close` — `CashController@close` — то же; Z-отчёт (`ShiftReport`). (строка 37)
- [ ] `POST /api/staff/cash/top-ups` — `api.staff.cash.top-ups.store` — `CashController@topUp` — то же; пополнение с бонусом (`TopUpBonus`). (строка 38)
- [ ] `POST /api/staff/cash/sales` — `api.staff.cash.sales.store` — `CashController@sale` — то же; продажа товаров. (строка 39)
- [ ] `POST /api/staff/cash/transactions` — `api.staff.cash.transactions.store` — `CashController@transact` — то же; приход/расход кассы. (строка 40)
- [ ] `GET /api/staff/customers` — `api.staff.customers.index` — `Api\Staff\CustomerController@index` — поиск клиентов (`LoyaltyProgram`). (строка 45)
- [ ] `GET /api/staff/products` — `api.staff.products.index` — `Api\Staff\ProductController@index`. (строка 46)
- [ ] `POST /api/staff/stock/receive` — `api.staff.stock.receive` — `Api\Staff\ProductController@receive` — `club-can:stock.create` (`AdjustStockAction`). (строка 47)
- [ ] `POST /api/staff/stock/movements` — `api.staff.stock.movements.store` — `Api\Staff\ProductController@move` — `club-can:warehouse.update`. (строка 48)
- [ ] `GET /api/staff/orders` — `api.staff.orders.index` — `Api\Staff\OrderController@index` — заказы из агента. (строка 51)
- [ ] `POST /api/staff/orders/{order}/done` — `api.staff.orders.done` — `OrderController@done` — `club-can:orders.update` (`AgentShop`). (строка 52)
- [ ] `POST /api/staff/orders/{order}/cancel` — `api.staff.orders.cancel` — `OrderController@cancel` — `club-can:orders.delete` (возврат денег и остатка, `AgentShop`). (строка 53)
- [ ] `GET /api/staff/chats` — `api.staff.chats.index` — `ChatController@conversationsJson`. (строка 56)
- [ ] `GET /api/staff/chat/poll` — `api.staff.chat.poll` — `ChatController@poll` — опрос чата. (строка 57)
- [ ] `POST /api/staff/chat/{customer}` — `api.staff.chat.reply` — `ChatController@reply` — `club-can:chat.create`, `throttle:60,1,api.staff.chat.reply`; отвечать могут cashier/operator (`ChatMessage::ANSWERING_ROLES`, проверка в контроллере). (строка 58)
- [ ] `GET /api/staff/notifications` — `api.staff.notifications.index` — `NotificationController@index`. (строка 61)
- [ ] `POST /api/staff/notifications/seen` — `api.staff.notifications.seen` — `NotificationController@markSeen`. (строка 62)
- [ ] `GET /api/staff/reports/today` — `api.staff.reports.today` — `Api\Staff\MeController@today` — `club-role:owner,admin,manager` (`StaffPermissions::REPORTS`). (строка 63)

## 4. `routes/api_v1.php` (17 маршрутов, интеграционный API)

Общие middleware: `api.log, api.size, api.key, api.limit` (`routes/api.php`, строки 180–183). Вызывают внешние системы клуба по ключам API (создаются в «Настройки → API и интеграции», `docs/integration-api.md`). В репозитории вызывающих нет. Форма ошибок — `ApiError` (`{error: {code, message, required_scopes?}}`, коды перечислены в `resources/api/openapi-v1.yaml`, строка 476).

- [ ] `GET /api/v1/me` — `api.v1.me` — `Api\V1\MeController` — `api.scope:any` — ключ, клуб, права. (строка 19)
- [ ] `GET /api/v1/products` — `api.v1.products.index` — `Api\V1\ProductController@index` — `api.scope:products:read` (курсорная пагинация). (строка 23)
- [ ] `GET /api/v1/products/{product}` — `api.v1.products.show` — `ProductController@show` — `products:read`. (строка 24)
- [ ] `GET /api/v1/product-categories` — `api.v1.product-categories.index` — `ProductController@categories` — `products:read`. (строка 25)
- [ ] `GET /api/v1/stock-movements` — `api.v1.stock-movements.index` — `Api\V1\StockMovementController@index` — `products:read`. (строка 26)
- [ ] `POST /api/v1/products` — `api.v1.products.store` — `ProductController@store` — `products:write`, `api.idempotent` (необязательный ключ); вебхук `product.created`. (строка 29)
- [ ] `PATCH /api/v1/products/{product}` — `api.v1.products.update` — `ProductController@update` — `products:write`, `api.idempotent`; вебхук `product.updated`. (строка 30)
- [ ] `POST /api/v1/stock-movements` — `api.v1.stock-movements.store` — `StockMovementController@store` — `stock:write`, `api.idempotent:required`; вебхук `stock.changed`. (строки 32–34)
- [ ] `GET /api/v1/sales` — `api.v1.sales.index` — `Api\V1\SaleController@index` — `sales:read`. (строка 38)
- [ ] `GET /api/v1/sales/{sale}` — `api.v1.sales.show` — `SaleController@show` — `sales:read`. (строка 39)
- [ ] `GET /api/v1/zones` — `api.v1.zones.index` — `Api\V1\ComputerController@zones` — `computers:read`. (строка 44)
- [ ] `GET /api/v1/computers` — `api.v1.computers.index` — `ComputerController@index` — `computers:read`. (строка 45)
- [ ] `GET /api/v1/computers/{computer}` — `api.v1.computers.show` — `ComputerController@show` — `computers:read`. (строка 46)
- [ ] `PUT /api/v1/computers/{computer}/reservation` — `api.v1.computers.reservation` — `ComputerController@reservation` — `bookings:write`, `api.idempotent`; вебхук `computer.reservation_changed`. (строки 48–51)
- [ ] `POST /api/v1/computers/control` — `api.v1.computers.control` — `ComputerController@control` — `computers:control`, `api.idempotent:required`; до 50 ПК (`max_bulk_items`), `ComputerPower`, вебхук `computer.controlled`. (строки 52–55)
- [ ] `GET /api/v1/tariffs` — `api.v1.tariffs.index` — `Api\V1\TariffController@index` — `tariffs:read`. (строка 57)
- [ ] `GET /api/v1/members` — `api.v1.members.index` — `Api\V1\MemberController@index` — `members:read` (персональные данные). (строка 58)

### 4.1. Сверка с `resources/api/openapi-v1.yaml` и `docs/integration-api.md`

- [ ] OpenAPI описывает те же 17 операций (пути строки 86–428 файла): `/me`, `/products` (GET, POST), `/products/{product}` (GET, PATCH), `/product-categories`, `/stock-movements` (GET, POST), `/sales`, `/sales/{sale}`, `/zones`, `/computers`, `/computers/{computer}`, `/computers/{computer}/reservation` (PUT), `/computers/control` (POST), `/tariffs`, `/members`. Расхождений по набору маршрутов нет.
- [ ] Права в OpenAPI указаны текстом в `description` («Scope: `products:write`»), а не машиночитаемо (`security`/`x-scopes`) — при переносе на oapi-codegen стоит добавить `x-required-scopes`, чтобы middleware генерировался из спецификации.
- [ ] Идемпотентность в OpenAPI: `idempotencyKey` / `idempotencyKeyRequired` (строки 464–465) совпадают с `api.idempotent` / `api.idempotent:required` в маршрутах; ответ 428 — для `stock-movements` POST и `computers/control`.
- [ ] Вебхуки (`config/integrations.php` → `webhook_events`): `product.created`, `product.updated`, `stock.changed`, `sale.completed`, `computer.reservation_changed`, `computer.controlled`; доставка — `WebhookDispatcher` в транзакции + задача `DeliverWebhook`, повторы 10 с/60 с/5 мин/30 мин/2 ч, отключение после 20 ошибок подряд, HTTPS и запрет приватных сетей (`docs/integration-api.md`, раздел 2 «Вебхуки»). В OpenAPI вебхуки не описаны (нет `webhooks:`).
- [ ] `docs/integration-api.md`, раздел 1: таблица аудита старых маршрутов неполная (нет `/api/agent/*`, `/api/guest/*`), а про `/api/products` сказано «без пагинации», хотя `Api\ProductController` использует `paginate(50)`.

## 5. Таблица «метод агента → маршрут»

| Метод (файл) | HTTP | Маршрут | Токены |
|---|---|---|---|
| `ZafiroApiClient.GetAgentTranslationsAsync` (`Services/ZafiroApiClient.cs`) | GET | `/api/agent/translations/{locale}` | — |
| `ZafiroApiClient.ResolveClubAsync` | POST | `/api/clubs/resolve` | — (ключ клуба в теле) |
| `ZafiroApiClient.LoginAsync` | POST | `/api/agent/user/login` (запасной — `login_endpoint` = `/api/auth/login`) | — / через службу (ключ клуба подставляет служба) |
| `ZafiroApiClient.ReadQrLogin` | — | разбор ответа `/api/agent/qr-login/poll` | — |
| `ZafiroApiClient.VerifyExitPinAsync` | POST | `/api/agent/exit-pin` | X-Agent-Token (+ Bearer гостя для журнала) |
| `ZafiroApiClient.PostWithDeviceTokenAsync` | POST | `/api/agent/qr-login`, `/api/agent/qr-login/poll`, `/api/agent/register`, `/api/agent/heartbeat` (экран входа) | X-Agent-Token, X-Agent-Device |
| `ZafiroApiClient.PostPublicAsync` | POST | `/api/auth/password/code`, `/api/auth/password/reset` | — |
| `ZafiroApiClient.SendAuthorizedAsync` | любой | `auth/logout` (из `ClubSetupService`), `club-computers/*` | Bearer (+ X-Agent-Token) |
| `ZafiroApiClient.ResolveComputerAsync` | POST | `/api/club-computers/resolve` | Bearer сотрудника/гостя |
| `ZafiroApiClient.RegisterComputerAsync` | POST | `/api/club-computers/register` | Bearer сотрудника |
| `ZafiroApiClient.SendHeartbeatAsync` | POST | `/api/agent/heartbeat` (с токеном ПК) или `/api/club-computers/{id}/heartbeat` | X-Agent-Token + Bearer гостя / Bearer |
| `HttpApiService.GetSessionInfoAsync` (`Services/HttpApiService.cs`) | GET | `/api/me` | Bearer гостя |
| `HttpApiService.GetTariffsAsync` | GET | `/api/tariffs?computer_id=` | Bearer гостя |
| `HttpApiService.GetProductsAsync` | GET | `/api/products` | Bearer гостя (если есть) |
| `HttpApiService.PurchaseTariffAsync` | POST | `/api/tariffs/{id}/purchase` | Bearer + X-Agent-Token |
| `HttpApiService.EndSessionWithResultAsync` | POST | `/api/club-computers/{id}/session/end` | Bearer гостя |
| `HttpApiService.CallGuestApiAsync` | GET/POST | `/api/guest/{friends, friends/{id}/accept|remove|transfer, profile/name, profile/password, chat, news, minutes, minutes/purchase, packages, packages/{id}/start, history, shop/purchase, seats, seat-transfer}` | Bearer гостя (+ X-Agent-Token для записи «с этого ПК») |
| `VaultServer.TryRotateAsync` (`Service/VaultServer.cs`) | POST | `/api/agent/token/rotate` | X-Agent-Token, X-Agent-Device |
| `VaultServer.EnrollAsync` | POST | `/api/agent/enroll` | — (ключ клуба в теле) |
| `VaultServer.CheckAsync` / `ProbeAsync` / `KeepOnlineAsync` | POST | `/api/agent/heartbeat` (`{"can_update":true}` / `{}`) | X-Agent-Token, X-Agent-Device |
| `VaultServer.RelayAsync` | POST | белый список `VaultProtocol.RelayPath` (`Service/VaultProtocol.cs`, строка 52): `agent/heartbeat`, `agent/exit-pin`, `agent/user/login`, `agent/register`, `agent/qr-login`, `agent/qr-login/poll`, `guest/seat-transfer`, `guest/minutes/purchase`, `guest/shop/purchase`, `guest/chat`, `guest/packages/{id}/start`, `tariffs/{id}/purchase` | служба подставляет токен ПК / ключ клуба |

Фронтенд агента (`WindowsAgent/Frontend/main.js`) сам HTTP не вызывает: он шлёт `webview.postMessage({type: …})` (`requestTariffs`, `purchaseTariff`, `requestProducts`, `purchaseProducts`, `requestFriends`, `addFriend`, `acceptFriend`/`removeFriend`, `transferBalance`, `requestChat`, `sendChat`, `requestNews`, `requestMinutes`, `purchaseMinutes`, `requestPackages`, `startPackage`, `requestHistory`, `requestSeats`, `transferSeat`, `updateProfileName`, `changePassword`, `refreshBalance`, `requestSession`, `logout`, `setLanguage`, `returnToDesktop`, `requestGuestFeatures`), а `MainWindow.xaml.cs` (строки 880–1250) превращает их в вызовы `HttpApiService`.

## 6. Аутентификация агента сейчас

- [ ] **Ключ установки клуба** (64 символа `[A-Za-z0-9]`, `VaultProtocol.ClubKeyFormat`): вводит сотрудник при первой настройке; агент проверяет его через `/api/clubs/resolve`, служба хранит его (`VaultServer` op `club-key`, `DeviceVaultStore.SaveClubKey`) и подставляет в `/api/agent/user/login` и `/api/agent/enroll`. Ключ один на клуб и многоразовый — его утечка позволяет регистрировать ПК в клубе (ограничено лимитом тарифа и `agent-enroll`).
- [ ] **Подключение ПК сотрудником**: сотрудник входит в агенте (`/api/agent/user/login`), агент вызывает `/api/club-computers/resolve` (`claim=true`, пока токена нет), при 404 — `/api/club-computers/register`; сотрудник получает `agent_token` (`ClubComputerController::agentToken`, строки 493–505), при этом ПК помечается «в руках сотрудника» (`startStaffVisit`).
- [ ] **Токен устройства** `zag_<48>`: в БД только sha256 (`club_computers.agent_token_hash`, `agent_token_issued_at`); выдача нового отзывает прежний (`ClubComputerSessions::issueAgentToken`, ~строка 245). Агент передаёт его в службу (`op=store`), служба проверяет токен пустым heartbeat, убеждается, что прежний отозван, сохраняет и **сразу ротирует** (`/api/agent/token/rotate`), чтобы токен, побывавший в профиле клиента, перестал работать (`VaultServer.StoreAsync`, строки 214–247).
- [ ] **Хранение**: служба Windows «Zafiro Agent» (SYSTEM) — `C:\ProgramData\ZafiroAgent\device.json`, токен и ключ клуба под DPAPI учётной записи службы, ACL только SYSTEM/администраторы (`Service/DeviceVaultStore.cs`, строки 14–43, 141–154, 201). Агент знает только метку `DeviceTokens.VaultMarker`; запросы с токеном идут через именованный канал `ZafiroAgent.Vault` (`VaultProtocol.PipeName`), служба пересылает только на `https://zafiro.kz` и только пути из `RelayPath` (`VaultProtocol.IsAllowedRelayTarget`). Без службы токен хранится у агента в `%APPDATA%\ZafiroAgent\agent.config.json` под DPAPI пользователя (`Services/ConfigurationService.cs`, строка 16; `Security/SecureStorageService.cs`). `DeviceVaultClient` (`Services/DeviceVaultClient.cs`) — клиент канала; есть ли служба — по ключу реестра `HKLM\SYSTEM\CurrentControlSet\Services\ZafiroAgent`.
- [ ] **Токен гостя** (Sanctum, 30 дней, ability `club:<id>`, имя токена = имя ПК): хранится в `agent.config.json` (`ProtectedApiToken`, DPAPI пользователя); `HeartbeatService` прекращает работу при истечении (`HeartbeatService.cs`, строка 237).
- [ ] **Бездисковые/клонированные ПК**: токен из образа принадлежит ПК-эталону; `X-Agent-Device` + MAC не дают использовать его на клоне (401 `device_mismatch`); служба клона получает собственный токен через `/api/agent/enroll` по ключу клуба + UUID + MAC (неизвестный ПК добавляется автоматически) — `AgentEnrollController`, `VaultServer.EnrollAsync`, `WindowsAgent/README.md` (строки 82–98).
- [ ] Привязка покупок к ПК: для ПК с токеном устройства тариф/минуты/пакет/магазин/пересадка требуют совпадающий `X-Agent-Token` (403 `wrong_computer`) — `AgentTariffController::purchase`, `GuestMinutesController::purchase`, `GuestPackageController::start`, `GuestShopController::purchase`, `GuestSeatController::store`.

## 7. Heartbeat сейчас

- [ ] **Частота**: сервер рекомендует `recommended_interval_seconds` = `max(5, config('club.computer_heartbeat_interval_seconds', 30))` (`ClubComputerController::heartbeatResponse`, строка 438; `config/club.php`, строка 4, env `COMPUTER_HEARTBEAT_INTERVAL_SECONDS`). Агент ограничивает 5 с … 1 ч, по умолчанию 30 с; ошибки — backoff 10 с ×2 до 2 мин; 4xx — пауза 2 мин (`Services/HeartbeatService.cs`, строки 13–25, 84–98); при смене состояния — внеочередной heartbeat (семафор `_wake`, строки 102–115, 379–383).
- [ ] Дополнительно: служба шлёт пустой heartbeat каждые 60 с всегда (`Service/AgentService.cs`, строка 39; `VaultServer.KeepOnlineAsync`), экран входа — каждые 15 с (`Views/ClubSetupWindow.xaml.cs`, строки 47, 732; `ClubSetupService.CheckGuestSessionAsync`), после загрузки Windows — `fresh_boot` с повтором каждые 15 с до 10 мин (`ClubSetupService.ReportFreshBootAsync`). Итого ПК с вошедшим гостем шлёт ~3 heartbeat в минуту, на экране входа ~5 в минуту.
- [ ] **Что пишет в БД** (`app/Services/ClubComputerSessions.php`, `heartbeat()`, строки 45–65; `needsWrite()`, 72–85; `changes()`, 93–134): запись идёт, если `fresh_boot`, сессия должна закончиться, `last_seen_at` старше 20 с (`SEEN_WRITE_SECONDS`) или что-то изменилось; тогда транзакция с `SELECT … FOR UPDATE` на `club_computers`, `expireIfDue` (завершение сессии с расчётом), `forceFill(last_seen_at, status='online', remote_ip, agent_state, сессия, agent_version, local_ip, cpu_model, cpu_cores, ram_total_mb, gpu_model, os_version, mac_address (только заполнение), device_name, hardware_reported_at)`. Так как интервал 30 с > 20 с, **практически каждый heartbeat основного цикла пишет в MySQL**.
- [ ] Каждый `agentHeartbeat` с `commands=true` ещё делает `ComputerPower::take` (UPDATE при наличии команды), `wakeTargets`, `takeMessage`; с токеном гостя — 3 запроса `customerState` (баланс, заявки, чат); ответ собирает настройки клуба (`GuestFeatures`, языки, `afterSessionAction`). Онлайн определяется по `last_seen_at` моложе 90 с (`ClubComputer::HEARTBEAT_TIMEOUT_SECONDS`).
- [ ] Новое: статус в памяти/Valkey, онлайн = открытый стрим, в Postgres — только изменения (`docs/agent-protocol.md`).

## 8. Polling в агенте (всё заменяется push по стриму)

- [ ] Heartbeat основного цикла — 30 с (команды питания/сообщения сотрудника доходят с задержкой до 30–60 с: «Уведомление появится на экране в течение минуты», `ClubComputerController::notify`). → `Command` в стриме.
- [ ] Heartbeat службы — 60 с; экран входа — 15 с. → один стрим службы.
- [ ] QR-вход — опрос каждые 3 с до 5 мин (`ClubSetupWindow.xaml.cs`, строки 885–905). → `WatchQrLogin` (server-streaming) / `QrLoginApproved`.
- [ ] Баланс — `/api/me` не чаще 20 с по событию (`MainWindow.xaml.cs`, строки 63, 193). → `BalanceChanged`.
- [ ] Чат — 5 с при открытом чате, иначе 30 с, если сервер не прислал счётчики (`Frontend/main.js`, строки 1197–1206). → `ChatMessageReceived` / `GuestCountersChanged`.
- [ ] Новости — 10 мин (`Frontend/main.js`, строка 1162). → оставить запрос по открытию экрана (без таймера).
- [ ] Локальные таймеры (сеть не трогают): автобиллинг 1 с, бездействие 5 с, время сессии 15 с (`main.js`, строки 558, 1046, 1289); `MainWindow` — таймер сессии 5 с, защита блокировки 1 с (строки 38, 62).
- [ ] Связь агент ↔ служба: «жив» каждые 5 с по каналу (`App.xaml.cs`, строка 145), служба проверяет раз в 3 с, таймаут 15 с (`Service/Watchdog.cs`, строки 29–35).

## 9. Агент: нагрузка (для будущего этапа)

- [ ] Сборка: .NET `net10.0-windows`, WPF + WebView2 `Microsoft.Web.WebView2 1.0.4258.31`, `System.Management` (WMI для железа) (`WindowsAgent/ZafiroAgent/ZafiroAgent.csproj`, строки 5–8, 31–34). Native AOT / trimming не включены (в csproj нет `PublishAot`/`PublishTrimmed`); WPF с Native AOT не поддерживается — служба (без WPF) может быть отдельным AOT-бинарником.
- [ ] Процессы: служба `ZafiroAgent` (SYSTEM: хранилище токена, relay, watchdog, обновления) + процесс агента в сеансе пользователя (WPF + WebView2 = несколько процессов `msedgewebview2.exe`).
- [ ] WebView2 создаётся в `MainWindow` (`EnsureCoreWebView2Async`, `MainWindow.xaml.cs`, строка 728) и при сворачивании окна (`Hide()`, строка 135) не освобождается — в коде не найдено `Dispose`/`TrySuspendAsync`/`MemoryUsageTargetLevel` (поиск по `MainWindow.xaml.cs`); браузер живёт всю сессию игры. Экран входа (`ClubSetupWindow`) — нативный WPF.
- [ ] Постоянная фоновая работа: таймеры UI 1 с (защита блокировки, автобиллинг в JS), 5 с (таймер сессии, бездействие, alive-пинг службе), heartbeat 30 с + 60 с службы, сбор железа через WMI (`Services/HardwareInfo.cs`, `WindowsHardwareProbe.cs`) с кэшем в `HardwareCollector`: железо — 6 ч, сеть (MAC/IP) — 30 с (`HardwareInfo.cs`, строки 159–164); железо отправляется в каждом heartbeat.
- [ ] Цель нового этапа (ТЗ, «Принятые решения» п. 5): служба отдельно от UI, gRPC-стрим вместо HTTP-опросов, WebView2 только когда показан экран блокировки/лаунчер (создавать по требованию и уничтожать/`TrySuspendAsync` при игре), Native AOT для службы. Измерений CPU/RAM в репозитории нет — метрика `ComputerLoad.agent_memory_mb` в новом `Status` нужна, чтобы их собрать.
