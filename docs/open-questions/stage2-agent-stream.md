# Этап 2, A3 — стрим агента и присутствие ПК: открытые вопросы

Реализация: `server/internal/agent/{presence,commands,stream}`, `db/migrations/0010_agent_stream.sql`,
`db/queries/agent_presence.sql`, `docs/agent-protocol.md` §13. Ниже — решения, принятые без владельца, и что нужно
от соседних модулей.

## AS-1. Маршрутизация команд: subject на инстанс, а не на ПК

- [ ] Подтвердить и поправить `docs/agent-protocol.md` §2 «Маршрутизация команд» (там `agent.cmd.<club_id>.<computer_id>`).

Контекст: при подписке на ПК каждый reconnect — SUB/UNSUB в кластере NATS; при `Drain`/падении инстанса это лавина
из десятков тысяч изменений интереса. Сделано: отправитель читает владельца стрима из Valkey (`zafiro:agent:pc:<id>`
= `<instance>|<gen>`) и публикует в `zafiro.agent.inst.<instance>`; одна подписка на инстанс. Потеря в гонке исключена
порядком (новое соединение берёт владение до чтения очереди; отправитель смотрит владельца после сохранения).
Варианты: (а) как сделано; (б) subject на ПК (как в документе). Рекомендация — (а).

## AS-2. Кто читает «онлайн» по `last_seen_at`

- [ ] Решить, чем заменить в новом коде формулу `last_seen_at >= now() - 90 с` (`ClubComputer::HEARTBEAT_TIMEOUT_SECONDS`).

Контекст: `last_seen_at` больше не обновляется каждые 20 с (в Postgres — только переходы). В Laravel по нему считают:
`ClubComputer::currentStatus()/connectionStatus()`, `ComputerPower::queue()/wakeTargets()`, `SeatTransfers` (~стр. 34),
`ClubNotifications` (~стр. 165), `Monitoring/ClubAttention`, `Superadmin/DashboardController` (~стр. 95),
`Superadmin/ClubController` (~стр. 74, 144), `Console/Commands/SnapshotClubLoad`. При переносе этих мест: живое
состояние — `presence.Store.Get/List` (Valkey), агрегаты по всем клубам — `club_computers.status = 'online'`
(частичный индекс `club_computers_online_index`). Рекомендация — так и сделать; формулу с 90 с не переносить.

## AS-3. Новое значение `agent_state = 'service'`

- [ ] Подтвердить.

Протокол ввёл `ComputerState.SERVICE` (техрежим), в Laravel `ClubComputer::AGENT_STATES` его нет (техрежим виден по
`staff_kind`). 0010 расширяет CHECK. `'service'` не снимает `staff_*` (снимают только idle/locked/in_session, как
`changes()`).

## AS-4. `agent_commands` без месячных партиций

- [ ] Подтвердить исключение из правила «журналы — партиции по месяцам».

Это очередь, а не история: строки живут до `expires_at` (минуты–сутки), удаляются фоновой задачей через 7 дней
(`presence.Sweeper.CleanupCommands`). Объём: единицы команд в час на ПК. История действий сотрудников с ПК
(перезагрузил, выключил) — в журнале действий (A4), не здесь.

## AS-5. Сроки новых команд

- [ ] Подтвердить сроки, которых не было в Laravel.

Из Laravel: питание/WoL/техрежим 120 с (`ComputerPower::COMMAND_TTL_SECONDS`), сообщение 300 с (`MESSAGE_TTL_SECONDS`).
Новые: `StartSession` — до `session.ends_at` (не позже 12 ч); `EndSession`, `Lock`, `Unlock`, `SignOutGuest` — 10 мин
(актуальная сессия всё равно приходит в `Welcome`); `UpdateAvailable` — 24 ч; `BalanceChanged`/`GuestCountersChanged`
ждут ПК в Valkey 5 мин. Остальные мгновенные до ПК не в сети не доходят.

## AS-6. Valkey без кластера

- [ ] Подтвердить топологию Valkey (primary + реплика, без cluster mode).

Lua-скрипты присутствия обращаются к ключам, вычисленным внутри (`pc:`, `club:`), и к одному zset `online`.
В cluster mode это не работает. Нагрузка 50 000 ПК — ≈ 1 000 операций/с, кластер не нужен. Если кластер понадобится —
ключи в hash tag клуба `{c<club>}` и zset по клубам.

## AS-7. Нет `ErrorCode` для вытеснения стрима

- [ ] Добавить в `common.proto` `ERROR_CODE_STREAM_REPLACED` (в конец enum)?

Сейчас: `ABORTED`, сообщение `"replaced"`, без `ErrorInfo` (протокол требует `ErrorInfo.reason` = имя `ErrorCode`).
Агенту достаточно кода `ABORTED` (повторяемый), но журнал агента был бы точнее.

## AS-8. Испорченное событие агента подтверждается

- [ ] Подтвердить.

`AgentEvent` с неверным `event_id`/без `occurred_at`/без `kind` логируется и получает `EventAck` (если есть `event_id`),
иначе агент повторял бы его после каждого reconnect, блокируя журнал. Ошибка обработчика (`EventHandler.HandleEvent`)
— без ack (повтор после reconnect).

## AS-9. Что heartbeat делал, а стрим не делает

- [ ] Подтвердить.

- `mac_address`: heartbeat только дополнял пустой MAC (`changes()`); теперь MAC задаёт регистрация ПК (A2).
- Сессия от агента: старые агенты без оплаченной сессии могли сообщить свой `session_id` (`sessionUpdates()`);
  в новом протоколе сессия — только данные сервера, `Status.session_id` хранится лишь в Valkey.
- `expireIfDue` в heartbeat — теперь только воркер `club-computers:expire-sessions`.
- `fresh_boot` → `resetGuestSession`: хук `EventHandler.FreshBoot` (реализацию связывает оркестратор поверх billing).
- `Status.signed_in_user_id` в Postgres не пишется (в Laravel такого поля в `club_computers` нет; вход гостя —
  событие `GuestSignedIn` в обработчик).

## AS-10. Офлайн с задержкой 15 с

- [ ] Подтвердить `OfflineGrace = 15 с`.

Закрытый стрим помечается офлайн в Postgres через 15 с, если ПК не переподключился (короткие обрывы и `Drain` не
пишут в базу). В Valkey (карта клуба) ПК сразу показывается `online = false`. В Laravel ПК становился офлайн через
90 с без heartbeat.

## AS-11. `Welcome` заполняется частично

- [ ] Оркестратору: реализовать `stream.WelcomeProvider`.

Сервис сам заполняет ПК, зону, `server_time`, `status_interval`, сессию (как `hasPaidSession()/sessionPayload()`),
`latest_command_seq`, версии протокола и из `clubs`: `club_name`, `default_locale`, `staff_minutes`. Остальное
`ClubConfig` (логотип, языки, `after_session`, `guest_features`, `idle_logout_minutes`, валюта, автобиллинг),
`translations_version`, `certificate_renew_after` — через `WelcomeProvider` (модули настроек, переводов, A2).
`ActiveSession.started_at` не заполняется: в `club_computers` нет колонки начала сессии.

## AS-12. Подключение оркестратором (ключи конфигурации)

| Ключ | Тип | Умолчание | Секрет |
|---|---|---|---|
| `agent.instance_id` | string `[A-Za-z0-9_-]{1,64}`, уникален в кластере | имя хоста | нет |
| `agent.stream.presence_ttl` | duration | `90s` | нет |
| `agent.stream.offline_grace` | duration | `15s` | нет |
| `agent.stream.hello_timeout` | duration | `10s` | нет |
| `agent.stream.status_interval` | duration | `60s` | нет |
| `agent.stream.state_debounce` | duration | `5s` | нет |
| `agent.stream.ack_timeout` | duration | `30s` | нет |
| `agent.stream.key_prefix` | string | `zafiro:agent:` | нет |
| `agent.stream.subject_prefix` | string | `zafiro.agent` | нет |
| `agent.sweeper.interval` / `reconcile_every` / `command_retention` | duration | `5s` / `5m` / `168h` | нет |

Нужны: пул `zafiro_app` (стрим, команды), пул `zafiro_platform` (Sweeper), клиент Valkey, соединение NATS.
