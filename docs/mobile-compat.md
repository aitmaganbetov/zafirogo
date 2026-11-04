# Мобильные приложения на новом сервере (слой совместимости, Q-058)

Решение владельца (DECISIONS Q-058): приложения Flutter `mobileclient` (клиент клуба) и `mobileemployee`
(сотрудник) **не меняются**; клуб, переехавший на новый сервер, должен продолжать с ними работать. Новый сервер
отвечает на те же пути старого REST тем же JSON. Код — `server/internal/http/legacy`, токены — таблица
`mobile_api_tokens` (миграция `0024`), настройки — блок `mobile.*` конфигурации. Решения и допущения —
[`open-questions/stage4-mobile.md`](open-questions/stage4-mobile.md).

## 0. Как устроены приложения (что парсит Flutter)

| | mobileclient | mobileemployee |
|---|---|---|
| Базовый URL | `--dart-define=API_BASE_URL` (по умолчанию `http://10.0.2.2/api`), `lib/config.dart` | то же, по умолчанию `https://zafiro.kz/api` |
| Заголовки | `Accept: application/json`, `Accept-Language: <ru/kk/en>`, `Authorization: Bearer <token>`, `Content-Type: application/json` | то же, `Accept-Language: ru` |
| Токен | `access_token` из ответа входа, `flutter_secure_storage` (`auth_token`) | то же |
| `X-Club-Id` | не шлёт (клуб — в токене) | не шлёт |
| `Idempotency-Key` | не шлёт | не шлёт |
| Ошибки | `errors` → первое сообщение первого поля, иначе `message`; `code`; 401 — выход | то же + `errors` по полям; 402/403/404/429 — свои тексты, если `message` пуст |
| `device_name` входа | `mobileclient` | `Zafiro Staff` |

Источники: `mobileclient/lib/api/api_client.dart`, `models.dart`, `session.dart`; `mobileemployee/lib/api/api_client.dart`,
`models.dart`.

## 1. Вызов приложения → маршрут → новый сервис

Формат ответа — как в старом контроллере (поля, порядок, `null`), суммы — как отдавал старый API: в `/api/auth/*`
— `float` тенге (`json_encode((float) …)`: `1500.0`), в `/api/staff/*` — строки `"1500.00"`
(`StaffApiController::money()`), в журнале клиента и переводе — строки со знаком (`GuestHistory::negative()`,
`Friends::formatCents()`). Внутри — только тиыны.

### 1.1. Вход и аккаунт (оба приложения)

| Вызов (Dart) | Маршрут | Laravel | Новый код | Лимит |
|---|---|---|---|---|
| `login` | `POST /api/auth/login` | `AuthController::login()` | `legacy.login` → `users`, `MobileUserClubs`, `password.Hasher`, токен `mobile_api_tokens`, журнал `auth.api_login`/`auth.api_login_failed` | `customer-login` (5/мин IP+телефон, 300/мин IP) |
| `logout` | `POST /api/auth/logout` | `AuthController::logout()` | `legacy.logout` (отзыв токена, `auth.api_logout`) | — |
| `me` (client) | `GET /api/auth/me` | `AuthController::me()` | `legacy.me` → `MobileMemberProfile` | — |
| `clubs` (client) | `GET /api/auth/clubs` | `AuthController::clubs()` | `MobileUserClubs` | `api.auth.clubs` 30/мин |
| `switchClub` | `POST /api/auth/switch-club` | `AuthController::switchClub()` | новый токен клуба, старый отзывается, `auth.api_club_switched` | `api.auth.switch-club` 10/мин |
| `passwordCode` (employee) | `POST /api/auth/password/code` | `PasswordRecoveryController::code()` | `verifycode.PasswordRecovery.SendCode` (H2) | `password-recovery` |
| `passwordReset` (employee) | `POST /api/auth/password/reset` | `PasswordRecoveryController::reset()` | `verifycode.PasswordRecovery.Reset` + отзыв всех токенов аккаунта | `password-recovery` |

### 1.2. mobileclient (гостевые маршруты `routes/api.php`)

Группа: `auth:sanctum`, `current-club`, `subscription` (402 «Подписка клуба не активна.»).

| Вызов | Маршрут | Laravel | Новый код | Лимит |
|---|---|---|---|---|
| `news` | `GET /api/guest/news` | `GuestNewsController` | `GuestListPlatformNews`, `GuestListClubNews` | 30/мин |
| `history` | `GET /api/guest/history?before=` | `GuestHistoryController`, `GuestHistory::page()` | `GuestHistoryOwn/Received`, формат `entry()` | 60/мин |
| `qrLoginInfo` | `GET /api/guest/qr-login/{code}` | `GuestQrLoginController::show()` | `PcLoginFindOpen` | 30/мин |
| `qrLoginApprove` | `POST /api/guest/qr-login/{code}` | `GuestQrLoginController::approve()` | `guestapi.Server.ApproveQrLogin` (ПК получает `QrLoginApproved`) | 10/мин |
| `friends` | `GET /api/guest/friends` | `GuestFriendController::index()` | `friends.List` + `GuestFeatures::all()` | 60/мин |
| `addFriend` | `POST /api/guest/friends` | `store()` | `friends.Request` | 10/мин |
| `acceptFriend` | `POST /api/guest/friends/{id}/accept` | `accept()` | `friends.Accept` | 30/мин |
| `removeFriend` | `POST /api/guest/friends/{id}/remove` | `remove()` | `friends.Remove` | 30/мин |
| `transferCode` | `POST /api/guest/friends/{id}/transfer/code` | `transferCode()` | `verifycode.TransferCodes.Send` (H2) | 5/мин |
| `transferConfirm` | `POST /api/guest/friends/{id}/transfer/confirm` | `transferConfirm()` | `billing.ConfirmFriendTransferCode` (ledger), `RecordBalanceTransferred` | 10/мин |

Пакет `internal/friends` вынесен из `agent/guestapi`: агент (gRPC `SocialService`) и приложение (REST) пользуются
одной логикой `Friends::request/accept/remove`.

Известная особенность приложения (не меняем): `HistoryEntry.fromJson` читает `amount` только если это число, а старый
API отдаёт строку («-150.00») — суммы в журнале приложения не показываются ни на старом, ни на новом сервере.

### 1.3. mobileemployee (`routes/api_staff.php`, 24 маршрута)

Группа: `auth:sanctum`, `current-club`, `subscription`, `club-role:owner,admin,manager,cashier,operator` (403),
`throttle:staff-app` (240/мин на сотрудника). Права маршрутов — как в старом (`club-can:*` → 403 «Нет права на
этот раздел…», `club-role` → 403 без текста).

| Вызов | Маршрут | Доп. middleware | Новый код |
|---|---|---|---|
| `me` | `GET /api/staff/me` | — | `MobileMemberProfile`, `permissions.StaffAppFor`, `cash/service.CurrentShift`, счётчики (`MobileStaffNewOrders`, `ChatUnreadTotal`, уведомления — раздел `notifications`) |
| `computers` | `GET /api/staff/computers` | — | `ClubMapComputers`, `ClubMapHeldComputerIDs`, присутствие агента, `clubmap.Computer`, `MobileStaffZones`, `MobileStaffTariffs` |
| `control` | `POST /api/staff/computers/control` | `club-can:pc_control.update` | `clubmap.ControlTx`, `SendControl`, `ControlMessage` |
| `guestSession` | `POST /api/staff/computers/guest-session` | `club-can:cash.create`, 30/мин | `billing/service.SellGuestSession` (ledger, касса) |
| `endSession` | `POST /api/staff/computers/{id}/end-session` | `club-role:owner,admin,manager,operator` | `billing/service.EndSession` |
| `cash` | `GET /api/staff/cash` | `club-can:cash.view` | `CurrentShift`, `Report` (X), `MobileStaffShiftTransactions` |
| `openShift` | `POST /api/staff/cash/shifts` | `+cash.create` | `cash/service.OpenShift` |
| `closeShift` | `POST /api/staff/cash/shifts/{id}/close` | `+cash.create` | `CloseShift` (без отметки печати Z, как API), `Report` (Z) |
| `topUp` | `POST /api/staff/cash/top-ups` | `+cash.create` | `cash/service.TopUp` (ledger, бонус `TopUpBonus`) |
| `sale` | `POST /api/staff/cash/sales` | `+cash.create` | `cash/service.Sale` |
| `cashTransaction` | `POST /api/staff/cash/transactions` | `+cash.create` | `cash/service.Transact` |
| `customers` | `GET /api/staff/customers` | — | `MobileStaffCustomers`, `bonus.LoyaltyPercentFor` |
| `products` | `GET /api/staff/products` | — | `MobileStaffProducts` |
| `stockReceive` | `POST /api/staff/stock/receive` | `club-can:stock.create` | `stock.AdjustStock` (≤ 100 000) |
| `stockMovement` | `POST /api/staff/stock/movements` | `club-can:warehouse.update` | `stock.AdjustStock` |
| `orders` | `GET /api/staff/orders` | — | `MobileStaffOrders` |
| `orderDone` | `POST /api/staff/orders/{id}/done` | `club-can:orders.update` | `shop.HandOver` |
| `orderCancel` | `POST /api/staff/orders/{id}/cancel` | `club-can:orders.delete` | `shop.Cancel` (возврат через ledger) |
| `chats` | `GET /api/staff/chats` | — | `ListChatConversations`, `ChatUnreadTotal` |
| `chatPoll` | `GET /api/staff/chat/poll` | — | `chat.MarkRead`, `ListChatThread` |
| `chatSend` | `POST /api/staff/chat/{customer}` | `club-can:chat.create`, 60/мин | `chat.Reply` + доставка на ПК (`ChatToGuest`) |
| `notifications` | `GET /api/staff/notifications` | — | раздел `notifications` нового REST (вызов изнутри, формат совпадает) |
| `notificationsSeen` | `POST /api/staff/notifications/seen` | — | то же |
| `reportToday` | `GET /api/staff/reports/today` | `club-role:owner,admin,manager` | `MobileStaffCashDays` (агрегат в SQL), ПК, `MobileStaffLowStock`, `MobileStaffRecentTransactions` |

Денежные операции приложения сотрудника не присылают `Idempotency-Key` (как и старый API); сервер берёт ключ из
заголовка, если он есть, иначе выдаёт новый на запрос (повтор = новая операция, как в старом). Все движения денег —
через сервисы с ledger.

### 1.4. Маршруты, которые не переносятся

Приложения их не вызывают (`grep` по `mobileclient/lib`, `mobileemployee/lib`):

- агентские (`/api/me`, `/api/agent/*`, `/api/club-computers/*`, `/api/clubs/resolve`, `/api/products`, `/api/tariffs*`)
  — агент переходит на gRPC (`proto/agent/v1`, docs/agent-protocol.md);
- гостевые маршруты агента: `/api/guest/minutes*`, `/api/guest/packages*`, `/api/guest/chat`, `/api/guest/telegram`,
  `/api/guest/shop/purchase`, `/api/guest/profile/*`, `/api/guest/seats`, `/api/guest/seat-transfer`,
  `POST /api/guest/friends/{id}/transfer` (перевод с паролем — только агент);
- `/api/settings/{section}/{key}` (Q-059), `/api/v1/*` — блок H1.

Запрос к такому пути с токеном клуба этого сервера — 404 `{"message": "The route … could not be found."}`; с токеном
старого сервера — уходит туда (§3).

## 2. Аутентификация (Sanctum)

### 2.1. Формат

- Приложение получает `access_token` (и `token`) вида `<id>|<48 символов>` — как Sanctum 4 (`Str::random(40)` +
  `crc32b`), в БД — `sha256(часть после «|»)` hex (`mobile_api_tokens.token_hash`), как `personal_access_tokens.token`.
- Срок — 30 дней (`mobile.token_ttl`), токен одного клуба (ability `club:<id>` → колонка `club_id`).
- `X-Club-Id` разрешён только равный клубу токена (иначе 403 «API-токен привязан к другому клубу.»).
- Выход, смена клуба, сброс пароля **не удаляют** строку, а ставят `revoked_at`: так сервер знает, что токен его, и не
  отправляет его на старый сервер (§3).
- `last_used_at` — не чаще раза в минуту на токен (Sanctum писал на каждый запрос).
- Поиск токена до выбора клуба — транзакция с `app.mobile_token_hash` (политика `mobile_token_lookup`, как коды
  регистрации ПК); отзыв всех токенов аккаунта — по `app.user_id` (политики `mobile_token_owner_*`).

### 2.2. Новые токены

`id` из последовательности с `10^12`: не пересекаются с `personal_access_tokens.id` клубов, которые переедут позже.

### 2.3. Перенос токенов при переезде клуба (для `migrate-from-mysql`)

Чтобы вошедшие пользователи не выкидывались:

- [ ] Читать `personal_access_tokens` с `tokenable_type = 'App\Models\User'` и `abilities` содержащим `"club:<id>"`
      переносимого клуба (агентские токены ПК `zag_…` и токены без клуба не переносить).
- [ ] Писать `mobile_api_tokens (id, club_id, user_id, name, token_hash, created_at, last_used_at, expires_at)`:
      `id` — **тот же** `personal_access_tokens.id` (иначе «<id>|…» в телефоне не найдётся), `token_hash` — колонка
      `token` как есть (sha256 hex), `user_id` — по таблице соответствия пользователей, `expires_at` — как есть
      (`NULL` — бессрочный, так и остаётся).
- [ ] Истёкшие (`expires_at < now()`) можно не переносить.
- [ ] Вставка — с явным `id` (`GENERATED BY DEFAULT`); последовательность не трогать (она с `10^12`).
- [ ] Проверка: `SELECT count(*)` по клубу совпадает; выборочный токен из MySQL проходит `GET /api/auth/me`.

## 3. Один адрес в приложении, два сервера (поклубный переход)

### 3.1. Выбранный вариант

Приложение знает один базовый URL (`https://zafiro.kz/api`), старый Laravel не меняем. Поэтому маршрутизацию по клубу
делает **новый сервер** перед собой, без изменений в приложениях и в старом коде:

```
приложение ── https://zafiro.kz/api/... ──► балансировщик (nginx)
     /api/auth/*, /api/guest/*, /api/staff/*  ──►  zafirogo (mobile.enabled)
                                                    ├─ клуб на новом сервере → ответ здесь
                                                    └─ иначе → обратный прокси на Laravel (mobile.upstream)
     всё остальное zafiro.kz                   ──►  Laravel (как сейчас)
```

Правило «клуб здесь или нет» (`legacy.authenticate`, `legacy.login`, `legacy.passwordRoute`):

| Запрос | Здесь, если | Иначе |
|---|---|---|
| С токеном `<id>\|<токен>` | строка `mobile_api_tokens` с этим id и хешем есть (даже отозванная/истёкшая → 401 здесь) | прокси на Laravel без изменений |
| `POST /api/auth/login` | у телефона есть членство хотя бы в одном клубе этого сервера | прокси |
| `POST /api/auth/password/*` | то же по нормализованному телефону | прокси |
| Без токена на защищённый маршрут | — | 401 здесь (Laravel ответил бы так же) |

Пока `mobile.upstream` пуст (все клубы здесь), прокси нет — всё обслуживается здесь.

### 3.2. Почему не другие варианты

- **Ответ 307/409 с адресом сервера клуба** — приложения не умеют менять базовый URL (он задаётся при сборке), а
  Dart `http` не повторяет POST по 307 с телом — не работает без изменения приложений.
- **Discovery-эндпоинт** — тоже требует изменения приложений; оставлен на будущее обновление приложений.
- **Маршрутизация по клубу в nginx** — nginx не знает клуба токена (`<id>|…` — id строки, не клуба) без обращения к
  БД; вход вообще без токена. Решение на уровне приложения-прокси (zafirogo) знает обе вещи.

### 3.3. Конфигурация

```yaml
mobile:
  enabled: true                       # ZAFIRO_MOBILE_ENABLED
  upstream: http://laravel.internal   # ZAFIRO_MOBILE_UPSTREAM — пусто, когда все клубы переехали
  upstream_host: zafiro.kz            # ZAFIRO_MOBILE_UPSTREAM_HOST — Host для Laravel
  token_ttl: 720h                     # ZAFIRO_MOBILE_TOKEN_TTL
```

Пример nginx на `zafiro.kz` (на время перехода):

```nginx
location ~ ^/api/(auth|guest|staff)/ {
    proxy_pass http://zafirogo;              # zafirogo сам вернёт чужие запросы в Laravel (mobile.upstream)
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;   # доверенный прокси: http.trusted_proxies
}
```

`/api/auth/login` панели нового сервера и мобильный вход живут на одном пути: мобильный вход отличается полем
`device_name` (у формы панели его нет, `additionalProperties: false`), `/api/auth/me` и `/logout` — токеном формата
Sanctum. Старые агенты ПК (до перехода на gRPC) тоже ходят в `/api/guest/*` и `/api/auth/logout` — их токены здесь не
найдены, запросы уходят в Laravel.

### 3.4. Ограничения переходного периода

- [ ] Аккаунт, у которого клубы на обоих серверах: вход попадает на новый сервер (есть клуб здесь) и видит в списке
      только клубы этого сервера; смена клуба на клуб старого сервера — 403 «нет доступа». Клубы с общими клиентами
      переносить вместе; по DECISIONS Q-058 клубы с активными мобилками переносятся последними.
- [ ] Сброс пароля такого аккаунта меняет пароль только на новом сервере.
- [ ] После переноса клуб в Laravel заморожен (README инструмента переноса): его токены там больше не нужны.

## 4. Тесты

`server/internal/http/legacy/legacy_integration_test.go` (`go test -tags=integration`): вход/профиль/выход, неверный
пароль, 401 без токена, 402 подписки, смена клуба, перенесённый токен Sanctum (тот же id), чужой `X-Club-Id`,
истёкший токен, панель не перехватывается, восстановление пароля без Telegram, лимит `customer-login` (429 +
`Retry-After`), маршрутизация на старый сервер (чужой токен, чужой телефон, отозванный свой — не уходит), друзья
(GS-099…GS-101), новости и журнал (GS-519), QR (GS-522: устарел, другой клуб), приложение сотрудника GS-274…GS-281
(роли и права, смена кассира с бонусом и Z-отчётом, клиенты, склад, заказы, чат, уведомления, отчёт дня, карта и
управление ПК), заказ чужого клуба — 404.
