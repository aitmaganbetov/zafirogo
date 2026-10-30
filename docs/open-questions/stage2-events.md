# Вопросы Этапа 2 — блок A4 «События» (журнал действий, outbox, вебхуки, WebSocket, один исполнитель)

Код: `server/internal/{activity,outbox,webhook,realtime,worker/leader}`, `db/migrations/0011_events.sql`,
`db/queries/{activity,outbox,webhooks}.sql`. Схема сообщений — [`../realtime.md`](../realtime.md).

## Ответы на вопросы Этапа 1 (предложение)

- [ ] **SV-8 / CSQ-5. Кто пишет `activity_logs` и вебхуки кассы/склада/сессий.**
  - Сделано: пакет `activity` — `activity.Record(ctx, q, Entry)` в той же транзакции, что действие (как старый
    `ActivityLogger::record()` внутри `DB::transaction()`); IP, User-Agent и префикс API-ключа берутся из контекста
    (`activity.WithRequestMeta` ставит HTTP/gRPC-middleware один раз на запрос) — сервисам не нужно тащить IP в
    `Actor`. Вебхуки и события админки — `outbox.Add(ctx, q, Event)` в той же транзакции.
  - Рекомендация: журнал и outbox пишет **сервис** (там транзакция), а не подписчик NATS — иначе запись журнала
    может разойтись с действием. Владельцам `billing/service` и `cash/service` — добавить вызовы
    (`tariff.purchased`, `time.*`, `cash.*`, `user.balance_*`, `stock.*`, `product.*`, вебхуки `sale.completed`,
    `stock.changed`, `product.*`) в волне B.

## Новые вопросы

- [ ] **EV-1. Доставки вебхуков создаются при обработке события, а не в транзакции действия.**
  - Старое: `WebhookDispatcher::dispatch()` вставлял строки `webhook_deliveries` в транзакции действия по списку
    слушателей на этот момент (`WebhookDispatcher.php` ~24–51).
  - Сейчас: в транзакции пишется событие в `outbox_events`; строки доставок создаёт durable consumer JetStream через
    миллисекунды после COMMIT, по слушателям на момент обработки. Откат действия по-прежнему не шлёт вебхук.
    Расхождение заметно, только если endpoint создают/выключают в ту же секунду.
  - Варианты: (а) оставить; (б) вставлять доставки прямо в транзакции действия (как старое) и использовать
    JetStream только для пробуждения. Рекомендация: (а) — одна точка записи (outbox) для админки и вебхуков.

- [ ] **EV-2. Секрет вебхука (Q-050).** Читается из `webhook_endpoints.secret` как есть. Перенесённые из Laravel
  значения зашифрованы `Crypt` (`APP_KEY`, `WebhookEndpoint::casts()` — `encrypted`) — пока Q-050 не решён,
  подпись перенесённых endpoint'ов будет неверной. Не решал.

- [ ] **EV-3. Как браузер передаёт токен при подключении к `/ws`.** Браузерный WebSocket не ставит `Authorization`.
  Варианты: cookie сессии (если A1 хранит access-токен в httpOnly-cookie) / короткоживущий одноразовый тикет
  `POST /ws-ticket` → `?ticket=` / подпротокол `Sec-WebSocket-Protocol`. Хаб читает только `principal.FromContext`,
  выбор — за A1. Рекомендация: cookie (уже есть CSRF-защита Origin в `coder/websocket`), иначе тикет на 30 с.

- [ ] **EV-4. Лимит соединений на пользователя — на инстанс.** `MaxConnsPerUser` (5) считается в памяти инстанса.
  Общий на кластер — счётчик в Valkey с TTL. Рекомендация: пока на инстанс (балансировщик с липкими сессиями
  по пользователю делает его почти общим).

- [ ] **EV-5. SSRF-фильтр вебхуков строже старого.** Кроме диапазонов PHP `FILTER_FLAG_NO_PRIV_RANGE |
  FILTER_FLAG_NO_RES_RANGE` (`WebhookUrlGuard::check()`) отвергаются CGNAT `100.64.0.0/10`, документационные
  сети, `198.18.0.0/15`, `64:ff9b::/96`, `2002::/16`, multicast. Endpoint с таким адресом в старом работал бы,
  здесь — `blocked: private_address`. Рекомендация: оставить.

- [ ] **EV-6. Хаб читает все события кластера на каждом инстансе.** Один ordered consumer `club.>` на инстанс,
  раздача по карте клуб → соединения. Просто и без потерь при росте числа клубов; при очень высоком потоке —
  подписка только на клубы с соединениями. Рекомендация: так оставить до нагрузочного теста.

- [ ] **EV-7. `activity_logs.description` хранит готовый текст на языке клуба.** Старый код сохранял результат
  `__()` (язык запроса), и журнал другого языка показывает текст на языке записи. Для ru/kk/en в одном клубе
  правильнее хранить ключ и параметры и переводить при показе. Сделано как в старом (поле `Description` — готовая
  строка). Рекомендация: в Этапе 3 добавить `details._t = {key, params}` и показывать перевод, если он есть.

- [ ] **EV-8. `activity_logs.created_at` стал NOT NULL** (ключ партиционирования). Строки без даты (в Laravel их нет:
  Eloquent ставит timestamps) получают `updated_at` или время миграции. Индекс `activity_logs_club_id_index` не
  перенесён — он префикс `(club_id, created_at)`. PK — `(id, created_at)`.

- [ ] **EV-9. Хранение `activity_logs`.** Старый код журнал не чистит (в `routes/console.php` нет prune). Партиции
  позволяют удалять месяцы `DROP`; срок хранения не задан. Рекомендация: хранить всё (как старое), решение — владельцу.
  Новые партиции: `db.EnsurePartitions` — нужно добавить `"activity_logs"` в `db.PartitionedTables`
  (`server/internal/db/pool.go`, не мой файл — оркестратору); функция `zafiro_create_monthly_partitions` уже
  принимает `activity_logs` (0011).

- [ ] **EV-10. Партиционирование остальных журналов (L-7, schema-changes §7) не сделано:** `api_request_logs`,
  `webhook_deliveries` (UNIQUE `event_id` → `(event_id, created_at)` ломает `ON CONFLICT (event_id)` дедупликацию —
  нужна отдельная таблица ключей), `platform_logs`, метрики, `club_load_snapshots`, `chat_messages`. Сделан
  `activity_logs` по тому же образцу. Рекомендация: отдельная миграция, когда будут готовы их запросы.

- [ ] **EV-11. Новый текст ошибки `Слишком много подключений.`** (429 при превышении лимита соединений) — нового
  ключа перевода нет в старом каталоге; нужно добавить kk/en в суперадминке. Тексты 401/403 — `Unauthenticated.`
  (как JSON Laravel) и `Доступ запрещён.` (ключ есть).

- [ ] **EV-12. Фильтр секретов в `details` журнала.** Удаляются поля, имя которых содержит `password`, `secret`,
  `token`, `otp`, `cvv`, `card_number`, `authorization`, `cookie`, `private_key`, `recovery_code`,
  `verification_code`, `installation_key`, или слово `pin` (`exit_pin`, `agentPin`). Поле `code` не фильтруется (код
  языка). Старый код такие поля не писал — фильтр страховочный, удаление логируется (только имена полей).

- [ ] **EV-13. IP обязателен** (как `LogicException` в `ActivityLogger::record()`). Для gRPC агента IP — адрес пира
  (за балансировщиком — из PROXY protocol/метаданных); для фоновых задач журнал не пишется (старое — тоже). Если
  понадобится запись от системы — договориться о значении (например, `127.0.0.1`).

## Рефакторинг (предложение, не сделан — файл не мой)

- `server/internal/worker/expire_sessions.go` `Tick()` повторяет `leader.RunOnce` (тот же ключ
  `hashtext('job:'+имя)`, то же снятие лока с закрытием соединения при ошибке). Предлагаю заменить тело на
  `leader.RunOnce(ctx, w.Pool, ExpireSessionsJob, func(ctx, conn) error { … service.ExpireDueSessions(ctx, conn, now()) … })`;
  совместимость ключей проверена тестом `leader.TestCompatibleWithExpireSessions`.
