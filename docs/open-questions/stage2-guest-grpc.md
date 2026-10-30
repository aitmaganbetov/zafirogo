# Этап 2, B2 — gRPC агента и гостя: открытые вопросы

Реализация: `server/internal/agent/guestapi` (GuestAuthService, GuestService, ShopService, SocialService,
AgentService.GetTranslations/VerifyStaffPin встраиванием `*stream.Service`, хуки стрима `Hooks`),
`server/internal/shop` (заказ к ПК, BR-075), запросы `db/queries/guest.sql`, `db/queries/shop.sql`.
Новых таблиц нет (миграция 0012 не понадобилась): токены гостя и идемпотентность RPC — в Valkey, QR-входы,
заказы, друзья, чат — в таблицах `0001_init.sql`, ключи заказа — в `billing_requests` (0006).

## GG-1. Токен гостя: Valkey, привязка к ПК, 12 часов

- [ ] Подтвердить.

`zgt_` + 32 случайных байта (base64url), в Valkey только SHA-256: `tok:<sha>` {club, pc, user, exp, used}, `pc:<pc>` (один
токен на ПК — повторный вход на ПК отзывает прежний, как `$user->tokens()->where('name', $deviceName)->delete()`),
`user:<user>` (ПК → токен: «один ПК на клиента» и отзыв при смене пароля). Привязка — к `club_id` + `computer_id`
сертификата, **не** к серийному номеру: иначе продление сертификата (каждые ~20 дней) выкидывало бы гостя. Срок 12 ч
(в Laravel — 30 дней, `AuthController::issueUserLoginResponse()`; см. `open-questions/api.md`). Варианты: (а) как сделано;
(б) серийный номер в привязке + перенос токенов при `RenewCertificate`. Рекомендация — (а).

## GG-2. «Вошёл на другом ПК» без `last_used_at` Sanctum

- [ ] Подтвердить.

`AgentSignIns::elsewhere()`: живой токен другого ПК, использованный за 2 минуты. Агент старого протокола дёргал API
каждые ~20 с (heartbeat с Bearer), новый — держит стрим и редко вызывает unary-RPC. Поэтому «активен» = токен
использован за 2 минуты (отметка обновляется не чаще раза в 20 с) **или** ПК в сети и сообщает этого гостя в
`Status.signed_in_user_id` (присутствие A3). Сотрудники (owner/admin/manager/operator/cashier) не ограничены — как в старом.

## GG-3. Идемпотентность — два уровня

- [ ] Подтвердить.

1) Valkey `idem:<pc>:<key>` → отпечаток запроса + ответ (proto) или бизнес-ошибка (`google.rpc.Status`), 24 ч;
INTERNAL/UNAVAILABLE/отмена не сохраняются. 2) Ключ БД сервиса с префиксом `agent:<pc>:<key>` (`billing_requests`,
`ledger_idempotency`) — потеря Valkey не приводит к двойному списанию. Ключ — только UUID (иначе
`IDEMPOTENCY_KEY_REQUIRED`). В `/api/v1` (ApiIdempotency) повтор с другим телом — 422, параллельный — 409; здесь —
`IDEMPOTENCY_KEY_REUSED` (FAILED_PRECONDITION) и `IDEMPOTENCY_IN_PROGRESS` (ABORTED), как в `common.proto`.

## GG-4. Версия переводов агента

- [ ] Решить вместе с переносом суперадминки переводов.

В Laravel версия — счётчик в кэше (`TranslationCatalog::flush()` +1 при правке). Здесь — момент последнего изменения
строк/языков (`max(updated_at)` в мс): без кэша и без отдельного счётчика, растёт при любой правке, но **не** при удалении
строки. Встроенные файлы `lang/*/agent.php` (`shippedGroup()`) в Go не переносились: если строк группы `agent` в базе нет,
ответ — пустой словарь (агент показывает свои тексты). Лимит — `agent-translations` 600/мин на IP, как в старом (в
`agent.proto` написано «30/мин на ПК» — такого правила в `ratelimit` нет; вопрос к A1/владельцу). Команду
`TranslationsChanged` должна слать суперадминка после правки (вне B2).

## GG-5. Telegram: восстановление пароля и код перевода — Unimplemented

- [ ] Перенести вместе с сервисом Telegram Gateway.

Сервиса Telegram в Go нет, поэтому `RequestPasswordResetCode`, `ResetPassword`, `RequestTransferCode` отвечают
`UNIMPLEMENTED` (TODO в коде). `TransferBalance` с `telegram_code` уже работает поверх `billing.ConfirmFriendTransferCode`
(коды, выданные мобильным приложением/старым сервером; проверка кода — `password.Hasher.Verify`, bcrypt/Argon2id).

## GG-6. Пересадка: «ПК в сети» по `last_seen_at`

- [ ] Решить вместе с AS-2 (open-questions/stage2-agent-stream.md).

`TransferSeat` вызывает `billing/service.TransferSeat`, который считает целевой ПК свободным, только если
`last_seen_at` моложе 90 с (`session.Computer.Online`). После A3 `last_seen_at` пишется только при переходах, поэтому
давно подключённый ПК выглядит «не в сети» и пересадка на него отклоняется. Нужно передать в billing признак «в сети» из
присутствия (Valkey). По той же причине `GetSeatMap` (`SeatTransfers::map()`) пока `UNIMPLEMENTED`.

## GG-7. Нормализация телефона при входе

- [ ] Подтвердить.

`LoginRequest` (Laravel) не нормализовал телефон (regex `^\+?[1-9]\d{1,14}$`, поиск по точному совпадению); агент
нормализовал сам. В контракте (`guest.proto`) сервер нормализует `PhoneNumber::normalize()` — так и сделано.

## GG-8. Журнал неудачных входов

- [ ] Решить вместе с A1.

`recordFailedLogin('auth.api_login_failed')` писал запись во все клубы аккаунта — нужен пул `zafiro_platform`. Здесь
неудачный вход не журналируется (лимит `customer-login` работает). Успешные — `auth.api_login`, `auth.qr_login`,
`user.registered`, `user.joined_club`, `auth.api_logout` — пишутся в клуб ПК.

## GG-9. События агента, меняющие сессию

- [ ] Подтвердить.

`SessionEnded` с причиной `GUEST` (гость завершил на ПК, если RPC не дошёл) завершает сессию владельца с расчётом
(`billing.EndSession`, не сотрудник). `EXPIRED` — воркер сервера, `SEAT_TRANSFER`/`STAFF` — свои RPC/панель, `POWER` —
выключение ПК сессию не обрывает (как в старом: «an agent state change … does not erase it»). `GuestSignedOut` отзывает
токен гостя этого ПК.

## GG-10. Подтверждение QR с телефона — только серверная часть

- [ ] Подключить к веб-странице `/pc-login/{code}` и мобильному приложению.

`Server.ApproveQrLogin(ctx, clubID, code, userID)` повторяет `QrLogins::approve()`, будит ожидание `WatchQrLogin` на этом
инстансе и шлёт ПК подсказку `QrLoginApproved`; на других инстансах ожидание перечитывает статус раз в 3 с. Клуб передаёт
вызывающий (клуб входа клиента). Поиск открытого QR только по коду во всех клубах потребует политики RLS вида
`enroll_code_lookup` (A2) — решить при переносе страницы.

## GG-11. Картинки

- [ ] Подтвердить адреса.

`ProductResource.image_url` — колонки `products.image_path` в схеме нет, поле пустое. Логотип, картинка входа, новости —
по маршрутам Laravel (`/club-logos/{club}/{file}`, `/club-signin-images/{club}/{file}`, `/news-images/{news}/{file}`) от
`Options.PublicBaseURL`; сами маршруты в Go ещё не перенесены.

## GG-12. Прочие решения без владельца

- [ ] Подтвердить.

- `ListProducts` — курсор keyset (name, id) вместо номера страницы; размер по умолчанию 50, максимум 100.
- `GetProfile` — без лимита (в `guest.proto` «60/мин на токен (новый)», правила в `ratelimit` нет; в Laravel у `/api/me`
  лимита не было).
- Заказ на 0 ₸ (все товары бесплатные) — без проводки ledger (проводка с нулевыми суммами невозможна).
- `Register` и `CreateQrLogin` проверяют подписку клуба (в старом — middleware `agent.device`, 402).
- `ListProducts.currency` — всегда `KZT`, как `ProductResource`, а не валюта клуба.
- Журнал покупок времени (`tariff.purchased`, `time.purchased`, `time.refunded`) и уведомления `StartSession`/`BalanceChanged`
  после покупок пишет `billing/service` (параллельная доработка), `guestapi` их не дублирует; `ListHistory` читает журнал
  как `GuestHistory::page()`.
