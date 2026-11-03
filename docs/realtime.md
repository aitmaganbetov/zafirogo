# Real-time: события клуба (outbox → NATS JetStream → WebSocket, вебхуки)

Этап 2, блок A4. Код: `server/internal/outbox`, `server/internal/realtime`, `server/internal/webhook`,
`server/internal/activity`, `server/internal/worker/leader`; миграция `db/migrations/0011_events.sql`.
Вопросы — [`open-questions/stage2-events.md`](open-questions/stage2-events.md).

## 1. Путь события

```
действие (касса, карта, чат…) ─┐ одна транзакция Postgres (db.InTenantTx)
outbox.Add(ctx, q, Event)     ─┘ → outbox_events (club_id, type, payload jsonb, event_id uuid)
                                  │ NOTIFY zafiro_outbox (при COMMIT)
публикатор outbox (один на кластер, advisory lock job:outbox-publisher)
  SELECT … WHERE published_at IS NULL ORDER BY id LIMIT 100 FOR UPDATE SKIP LOCKED
  → JetStream: stream zafiro-events, subject club.<club_id>.<type>, Nats-Msg-Id = event_id
  → published_at = now()
                                  │
          ┌───────────────────────┴────────────────────────┐
realtime.Handler (на каждом инстансе)          webhook.Consumer (durable "webhooks", общий)
ordered consumer club.>, только новые            фильтр club.*.<тип-вебхука>
→ сотрудники клуба на этом инстансе              → webhook_deliveries (pending), id = uuid v5(событие, endpoint)
  с правом раздела (WebSocket)                   → webhook.Deliverer: SKIP LOCKED + аренда, POST + HMAC, ретраи
```

- **Источник правды — Postgres.** Падение NATS или публикатора не теряет событий: строки остаются с
  `published_at IS NULL` и уходят позже. Падение между отправкой и пометкой — повторная отправка с тем же
  `Nats-Msg-Id`, JetStream отбрасывает дубль в окне дедупликации (по умолчанию 10 мин). Тест:
  `TestOutboxCrashMidwayNoLossNoDuplicates`.
- **Порядок.** Публикатор один (лидер), события берутся по `id` — события клуба уходят в порядке записи
  (с точностью до транзакций, закоммиченных «не по порядку» id).
- **Пробуждение.** `LISTEN zafiro_outbox` на соединении, держащем лок лидерства (триггер шлёт NOTIFY после
  вставки; Postgres доставляет его при COMMIT), плюс страховочный опрос раз в `Interval` (2 с).
- **Очистка.** Опубликованные строки старше `Retention` (24 ч) удаляет сам публикатор батчами по 1 000.

## 2. Соединение WebSocket

`GET /ws` (маршрут подключает оркестратор: `realtime.NewHandler(opts)` + `Handler.Run(ctx)` в группе сервера).

- **Аутентификация** — middleware админки кладёт `principal.Principal` в контекст до хендлера. Нет Principal —
  `401 {"message":"Unauthenticated."}`; нет выбранного клуба или роли — `403 {"message":"Доступ запрещён."}`.
  Браузер не умеет ставить `Authorization` на WebSocket: токен — в cookie сессии (тот же механизм, что у
  REST) или в query/`Sec-WebSocket-Protocol` — решает A1 (см. вопрос EV-3).
- **Клуб** — только `Principal.ClubID`. Параметров у `/ws` нет; подписаться на чужой клуб нельзя.
- **Origin** — по умолчанию только тот же хост (`coder/websocket`), дополнительные — `OriginPatterns`.
- **Лимит** — не больше `MaxConnsPerUser` (5) соединений пользователя на инстанс; сверх —
  `429 {"message":"Слишком много подключений."}`.
- **Ping/pong** — сервер шлёт ping каждые 25 с; нет pong за 10 с — закрытие `1008 "ping timeout"`.
- **Backpressure** — у соединения буфер `SendBuffer` (64) сообщений; переполнен — закрытие `1008 "slow consumer"`.
- **Клиент ничего не отправляет** (кроме управляющих кадров). Сообщение с данными — закрытие `1008`.
- **Коды закрытия:** `1001` — сервер останавливается; `1008` — медленный клиент / нет pong / данные от клиента;
  `4001` — переподключиться с новым токеном (права изменились — событие `staff.access_changed`, или истёк
  `MaxLifetime`).
- **После переподключения** клиент перечитывает состояние экрана по REST: доставка в браузер — «не более
  одного раза», пока соединение открыто; пропущенные события не досылаются.

## 3. Формат сообщений

Каждое сообщение — один JSON-объект в текстовом кадре:

```json
{"id": "0f8c…-uuid", "type": "computer.state", "at": "2026-10-30T12:00:00.123456Z", "data": { … }}
```

| Поле | Тип | Смысл |
|---|---|---|
| `id` | string (uuid) | id события (`outbox_events.event_id`); для дедупликации на клиенте |
| `type` | string | тип события (ниже) |
| `at` | string (RFC 3339, UTC) | время действия (запись в outbox) |
| `data` | object | данные события; **суммы — целые тиыны** (`int64`), как во всём API |

Первое сообщение после подключения — `{"type": "hello", "at": …, "data": {"club_id": 1, "user_id": 5, "ping_interval": 25}}`.

Тело сообщения JetStream — то же плюс `club_id` (`outbox.Envelope`).

## 4. Типы событий и права

Право определяется первым словом типа (`realtime.SectionRights`); раздела нет в таблице — событие не уходит
никому. Права — `app/Support/ClubPermissions.php`, те же, что открывают экран, который раньше опрашивал сервер
(`app/Http/Middleware/RequireClubArea.php`).

| Раздел | Право | Заменяет опрос |
|---|---|---|
| `notifications.*` | любой сотрудник клуба (есть роль) | №1 `GET /notifications` (60 с), `layouts/admin.blade.php` |
| `computer.*` | `club_map.view` | №2 `GET /club-map/state` (15 с), `club-map/index.blade.php`; список ПК, дашборд |
| `chat.*` | `chat.view` | №3 `GET /chat/poll` (5 с), `chat/index.blade.php`; бейдж сайдбара |
| `todo.*` | `todos.view` | №4 `GET /todos/state` (15 с), `todos/index.blade.php` |
| `shop_order.*` | `orders.view` | №5 `GET /shop-orders/state` (15 с), `shop-orders/index.blade.php`; бейдж |
| `cash.*`, `sale.*` | `cash.view` | (нового нет: касса обновлялась перезагрузкой страницы) |
| `balance.*` | `customers.view` | — |
| `reservation.*` | `reservations.view` | — |
| `product.*` | `warehouse.view` | — (тип-вебхук) |
| `stock.*` | `stock.view` | — (тип-вебхук) |
| `staff.access_changed` | служебное, клиенту не пересылается | закрывает соединения сотрудника кодом 4001 |

Схемы `data` (пишут владельцы сервисов в волне B/Этапе 3; здесь — договорённость):

| Тип | `data` | Когда |
|---|---|---|
| `notifications.changed` | `{}` | изменилось что-то, из чего строится колокольчик (`ClubNotifications::for()`): журнал из списка событий колокольчика, чат, заказ, остаток, ПК офлайн. Клиент перечитывает `GET /notifications` (список зависит от роли и `notifications_seen_at`) |
| `computer.state` | `{"computer_id": 7, "status": "online\|offline\|busy\|maintenance", "session": {"user_id": 5, "ends_at": "…"} \| null, "reserved": bool, "maintenance": bool, "agent_version": "1.2.3"}` | дифф одного ПК (поля как у элемента `computerStates` старого `/club-map/state`); пишется только при изменении состояния, не на heartbeat |
| `computer.load` | `{"computer_id": 7, "load": {"cpu_bp": 4250, "ram_bp": 6100, "idle_seconds": 30} \| отсутствует, "problems": ["cpu_hot"], "rtt_ms": 18}` | Этап 7: загрузка и проблемы здоровья ПК (поля как `load`/`problems`/`rtt_ms` у `ComputerState`). **Не через outbox** — §7; не чаще раза в 15 с на ПК и только при заметном изменении |
| `computer.session.expired` | `{"computer_id": 7, "session_id": 123}` | воркер истечения сессий |
| `computer.reservation_changed` | `{"computer_ids": [7], "reserved": true}` | флаг брони ПК: карта клуба (reserve) и API v1 `PUT /computers/{id}/reservation` (тип-вебхук, как в старом; брони экрана «Брони» шлют `reservation.changed` и `computer.state`) |
| `computer.controlled` | `{"computer_ids": [7], "action": "power_on\|restart\|power_off\|service", "status": "restarting\|offline" \| null}` | команда управления (тип-вебхук) |
| `chat.message` | `{"message_id": 10, "customer_id": 5, "from_staff": bool, "created_at": "…"}` | новое сообщение; текст — по REST (персональные данные не в шине) |
| `chat.unread` | `{"unread": 3}` | счётчик непрочитанных клуба (бейдж) |
| `todo.changed` | `{"todo_id": 4, "action": "created\|done\|reopened\|deleted", "due_on": "2026-10-30"}` | задача |
| `shop_order.created` / `shop_order.updated` | `{"order_id": 8, "status": "new\|done\|cancelled", "new_count": 2}` | заказ из агента; `new_count` — для бейджа |
| `cash.shift_changed` | `{"shift_id": 3, "action": "opened\|closed"}` | смена кассы |
| `cash.transaction` | `{"transaction_id": 77, "kind": "sale\|top_up\|refund\|income\|expense", "amount": 150000}` | операция кассы (тиыны) |
| `sale.completed` | `{"sale_id": 77, "total": 150000, "items": [{"product_id": 1, "quantity": 2}]}` | продажа (тип-вебхук) |
| `balance.changed` | `{"user_id": 5, "balance": 100000, "bonus_balance": 2000}` | баланс клиента после операции ledger |
| `product.created` / `product.updated` | `{"product_id": 1}` | товар (тип-вебхук) |
| `stock.changed` | `{"product_id": 1, "quantity": 12}` | остаток (тип-вебхук) |
| `staff.access_changed` | `{"user_id": 5}` | роль/права/членство сотрудника изменились, сотрудник удалён, сессия отозвана |

Правила для `data`: без секретов; без персональных данных сверх id (имя, телефон, текст чата клиент берёт по
REST с проверкой прав); суммы — тиыны. Для типов-вебхуков `data` уходит во внешние системы как есть.

## 5. Вебхуки (интеграции)

Как `DeliverWebhook` старого кода (`app/Jobs/DeliverWebhook.php`), см. `server/internal/webhook`:

- События: `product.created`, `product.updated`, `stock.changed`, `sale.completed`,
  `computer.reservation_changed`, `computer.controlled` (`config/integrations.php webhook_events`).
- Тело: `{"id", "event", "created_at" (ISO 8601, "+00:00"), "club_id", "data"}`; порядок ключей jsonb
  (по длине, затем по байтам) совпадает с порядком JSON-колонки MySQL; компактно, без экранирования юникода
  и слэшей.
- Заголовки: `X-Zafiro-Event`, `X-Zafiro-Delivery` (= `id`), `X-Zafiro-Timestamp`,
  `X-Zafiro-Signature: v1=hex(HMAC-SHA256(secret, "{timestamp}.{body}"))`, `User-Agent: Zafiro-Webhooks/1`,
  `Content-Type: application/json`.
- Таймаут 5 с, соединение 3 с, без редиректов, без прокси; SSRF-проверка (`WebhookUrlGuard`) с пиннингом IP.
- Повторы: `[10, 60, 300, 1800, 7200]` с → до 6 попыток; затем `failed`, `failure_count++`; 20 неудач подряд —
  endpoint выключается. Успех — `failure_count = 0`, `last_success_at`. Endpoint удалён/выключен — `skipped`.
- Секрет — из `webhook_endpoints.secret` как есть (в Laravel зашифрован `APP_KEY`; Q-050).

## 6. Масштаб

- Публикатор: один на кластер; батч 100, асинхронная публикация с ожиданием подтверждений. Если упрётся —
  шардировать по `club_id % N` (N лидеров с ключами `outbox-publisher-<k>`).
- Хаб: каждый инстанс читает все события (`club.>`) одним ordered consumer и раздаёт локально по карте
  клуб → соединения. При 50 000 ПК и событиях только на изменение состояния это сотни сообщений в секунду —
  приемлемо. Если нет — подписка по клубам с соединениями (`club.<id>.>`), см. EV-6.
- Лимит соединений — на инстанс; общий на кластер — через Valkey (EV-4).

## 7. Живые обновления без outbox (Этап 7: `computer.load`)

Загрузка и здоровье ПК меняются постоянно; писать их в `outbox_events` (Postgres) и JetStream на каждый Status нельзя
(CLAUDE.md «Производительность»). Поэтому:

```
Status агента (стрим, инстанс ПК) ─ ограничение частоты (stream.HealthOptions.liveDue)
  → clubmap.LiveEvents: core NATS publish live.club.<club_id>.computer.load (тело — outbox.Envelope, id — uuid)
  → realtime.Handler каждого инстанса (подписка live.club.>) → сотрудники клуба с правом club_map.view
```

- Subject `live.club.>` не пересекается с потоком `club.>` — JetStream эти сообщения не сохраняет (тест
  `TestLiveToWebSocket`), вебхуки их не видят.
- Частота: на ПК не чаще `agent.health.live_min_gap` (15 с) и только при заметном изменении (±5 п.п. CPU/ОЗУ,
  ±2 °C CPU/GPU, появление или пропажа данных); смена набора проблем, периферии или SMART — сразу.
- Доставка «не более одного раза» (core NATS, буфер подписки ограничен — лишнее отбрасывается). После переподключения
  клиент перечитывает экран по REST, где те же поля есть в `/api/club-map/state`.
- По живому каналу принимаются только события данных: служебное `staff.access_changed` игнорируется.
- Клиент (`web/src/features/clubmap/useMapLive.ts`) подставляет данные без запроса; открытая панель ПК перечитывает
  `GET /api/club-computers/{id}/health`.
- Нагрузка — `docs/load-test.md` §6.3.

