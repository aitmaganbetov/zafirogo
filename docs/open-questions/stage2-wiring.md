# Вопросы Этапа 2 — B0 «сборка приложения» (config, app, main)

Код: `server/internal/config`, `server/internal/app`, `server/cmd/zafiro`, `db/init/10-zafiro-logins.sh`,
`docker-compose.dev.yml`, `.env.example`, `Taskfile.yml`, `docs/dev-setup.md`.

## Подключено через временные адаптеры / не подключено

- [x] **W-1. Хуки потока агента — частично.** Подключено: `stream.Options.Events` и `Welcome` —
  `guestapi.NewHooks` (B2), уведомления billing/кассы → `stream.Notifier()` (`Billing.SetNotifier`,
  `Cash.Notifier`), gRPC — `guestapi.Register` (AgentService + сервисы гостя) вместо `stream.Register`, REST
  админки — `httpserver.Options{Pool, Billing, Cash, Presence}` (B1). Не подключено (реализаций нет):
  `stream.Options.Results` (ResultHandler — итог команды для панели), `Presence` (PresenceListener → событие карты
  клуба в outbox/WebSocket) и `presence.Sweeper.OnOffline` — метки `TODO(B2)` в `server/internal/app/app.go`.
  - Решение (30.10.2026): принято: реализовать `ResultHandler`, `PresenceListener`, `Sweeper.OnOffline`.
- [x] **W-2. Истёкшая сессия без id.** `worker.ExpireSessions.OnExpired` → `Notifier.SessionEnded(EXPIRED)`. В
  `billingsvc.ExpiredSession` нет id сессии, поэтому `EndSession.session_id` берётся из присутствия (что сообщил
  агент в Status). Если агент не сообщил — пусто. Предложение: добавить `SessionID` в `ExpiredSession` (B2/владелец
  `billing/service`).
  - Решение (30.10.2026): принято: добавить `SessionID` в `ExpiredSession`.
- [x] **W-3. Язык текстов журнала.** `activity_logs.description` для событий входа (`auth.*`, `club.switched`) и
  регистрации ПК (`computer.registered`) пишется русским текстом-ключом с подстановками (`Вход в систему: Иван`).
  Старый код писал `__()` на языке запроса. Серверного каталога переводов ещё нет. Связано с EV-7 (хранить ключ и
  параметры). Рекомендация: EV-7 (б) — ключ + параметры, перевод при показе.
  - Решение (30.10.2026): по DECISIONS W-3 — ключ (русский текст) + параметры, язык — при показе.
- [x] **W-4. `platform_logs` не пишется.** Вход суперадмина без клуба (`superadmin.login`) и просмотр клуба
  (`superadmin.club_viewed`) пока идут только в системный лог: запросов к `platform_logs` нет. Нужен запрос
  (`db/queries/platform_logs.sql`) — кому делать (A4/B1)?
  - Решение (30.10.2026): по принципам (PD-15) — `platform_logs` пишется, как в старом; запросы — в пакете `activity` (A4).
- [x] **W-5. IP в журнале для HTTP.** `activity.Record` требует IP (`ErrNoIP`, как `LogicException` старого кода).
  IP и User-Agent кладёт в контекст middleware `activity.WithRequestMeta`, а он в роутере `server/internal/http`
  пока не подключён (после `RealIP`, до обработчиков). Без него события входа `auth.*` не попадут в журнал (только
  предупреждение в логе). Владельцу `server/internal/http` (B1): добавить middleware. Для gRPC регистрации IP —
  адрес пира (`grpcserver.PeerIP`), это сделано в адаптере.
  - Решение (30.10.2026): сделано: middleware `RequestMeta` подключён (RQ-10).
- [x] **W-6. Морф-класс в `activity_logs.subject_type`.** `enroll` передаёт `club_computer`; адаптер переводит в
  `App\Models\ClubComputer` (в Laravel morphMap нет — полные имена классов). Подтвердить, что в новом проекте
  сохраняем имена классов Laravel (для совместимости перенесённых данных) — или вводим короткие имена с миграцией.
  - Решение (30.10.2026): по принципам (PD-14) — сохраняем имена классов Laravel.

## Решения, принятые при сборке (подтвердить)

- [x] **W-7. Логины Postgres в dev.** Миграции создают только групповые роли NOLOGIN (`0004_rls.sql`). Логины
  сервера (`zafiro_app_login`, `zafiro_platform_login`, пароли из `.env`) создаёт `db/init/10-zafiro-logins.sh`
  при первой инициализации тома compose; `task db:logins` — повторно (идемпотентно). Сервер при старте проверяет
  членство в ролях и отказывается работать, если логин — суперпользователь или с BYPASSRLS. В prod логины —
  инфраструктура. CI (`go.yml`) не менялся: интеграционные тесты создают логины сами (dbtest), Valkey и NATS —
  testcontainers.
  - Решение (30.10.2026): принято как сделано.
- [x] **W-8. Режим без зависимостей сохранён.** Пустой `database.url` — только `/healthz`, `/readyz` и фронт (смоук
  бинарника в CI `go.yml` → build). В `env=prod` запрещён валидацией.
  - Решение (30.10.2026): принято как сделано.
- [x] **W-9. Случайные секреты в dev/test.** Незаданные `auth.signing_keys` и `agent.enroll.code_pepper` в dev/test
  генерируются при запуске (предупреждение в логе): сессии и выданные коды не переживают перезапуск. В prod —
  ошибка запуска. Альтернатива — dev-значения в `.env.example`. Рекомендация: оставить (секретов в репозитории нет).
  - Решение (30.10.2026): принята рекомендация: оставить.
- [x] **W-10. Отзыв сертификатов между инстансами.** `enroll.OnRevoked` публикует в NATS
  `<agent.stream.subject_prefix>.cert.revoked` (обычный NATS, не JetStream: потеря сообщения покрывается
  `CertCache.Refresh` раз в 5 с); подписка каждого инстанса → `CertCache.MarkRevoked`; `CertCache.OnRevoked` →
  `stream.Kick(UNAUTHENTICATED, CERTIFICATE_REVOKED)` (Kick сам находит инстанс с потоком). При N инстансах ПК
  получает до N одинаковых Kick — безвредно. Закрывает вопрос A2 §8 «Распространение отзыва».
  - Решение (30.10.2026): принято как сделано.
- [x] **W-11. Лимитер регистрации в Valkey.** `enroll.Limiter` — фиксированное окно `INCR` + `PEXPIRE NX` в Valkey
  (общий для инстансов, ключи — sha256), как `MemoryLimiter`/`RateLimiter::hit`. Скользящее окно A1
  (`ratelimit.Limiter`) не подошло по интерфейсу (`Hit/Exceeded` с произвольным ключом). Закрывает A2 §8 «Лимиты».
  - Решение (30.10.2026): принято как сделано.
- [x] **W-12. Порядок остановки и бюджет.** `shutdown_timeout` 30s (было 15s): readyz → 503, HTTP Shutdown
  параллельно с `stream.Drain(min(agent.stream.drain_within=10s, ½ бюджета))` и ожиданием закрытия потоков →
  `stream.Close` → gRPC GracefulStop (по дедлайну Stop) → отмена воркеров → закрытие NATS/Valkey/пулов. Если воркеры
  не уложились — выход без закрытия пулов (Close пула ждал бы зависшее соединение). Ошибка любого воркера (кроме
  отмены) останавливает весь процесс — перезапуск оркестратором.
  - Решение (30.10.2026): принято как сделано.
- [x] **W-13. Партиции — при старте и раз в сутки** под `leader.RunOnce` (`db:ensure-partitions`) пулом
  zafiro_platform, на 3 месяца вперёд (`workers.partitions.months`); после ошибки — повтор через минуту.
  - Решение (30.10.2026): принято как сделано.
