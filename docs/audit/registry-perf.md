# Реестр: производительность сервера и БД (PERF / DB), этап 1 — только чтение и измерения

Ветка `audit/full-system-2026-11` (e4e1156), файлы репозитория не менялись (`git status` чист). Стенды — свои контейнеры
`audit-perf-*` (удалены), скрипты и сырые данные — `/tmp/claude-0/-home-user-zafirogo/541f6760-cdd0-504b-ac95-cb07a87b9341/scratchpad/audit/perf2/`.
Найденное и исправленное прошлым аудитом (PB-01…PB-27, AP-1…AP-7) не повторяется; открытые пункты владельца (AP-O1…AP-O9)
перечислены в «Измерениях» со свежими числами, но новыми находками не считаются. Регрессов исправленного не найдено.

| ID | Приоритет | Категория | Файл:строка | Проблема | Сценарий/доказательство | Предлагаемое исправление | Меняет бизнес-логику? | Статус |
|---|---|---|---|---|---|---|---|---|
| PERF-1 | Medium | Пул соединений / воркеры | `server/internal/worker/leader/leader.go:39-52` (`TryAcquire` держит соединение пула платформы на всё время тика); вложенный второй захват того же пула: `worker/start_app_bookings.go:90,101`, `worker/expire_subscriptions.go:48,50`, `worker/burn_packages.go:57-59` (→ `billing/service/packages.go:107`), `monitoring/workers.go:143-145,173,226`; рекомендация `docs/deploy.md:342` (`platform_url …&pool_max_conns=8`) | Самоблокировка пула `zafiro_platform`: задача под `leader.RunOnce` держит соединение с advisory lock и ждёт **второе** соединение того же пула (`db.InPlatformTx`). Если все слоты заняты такими держателями — никто не продвинется; ожидание без срока (контекст воркера без таймаута). Запас при умолчании 16 есть, при рекомендованных 8 — 1–2 слота (на старте инстанса — 14 одновременных захватов) | Локальный сервер на базе из 200 клубов, `pool_max_conns=4` у `database.platform_url`: через 68 с и дальше `/readyz` = 503 (`postgres_platform: context deadline exceeded`), в Postgres 3 из 4 соединений «idle» на `pg_try_advisory_lock` с момента старта; дамп горутин — держатели в `leader.RunOnce` ждут `InPlatformTx` (`perf2/gor-pool4.txt`). При 5, 6 и 8 старт прошёл (18 ожиданий пула на старте при 8); staging с 16 — 14 соединений платформы занято сразу после старта | Работу тика выполнять на соединении с локом (`conn` уже передаётся в `fn`) или брать лок `pg_try_advisory_xact_lock` внутри рабочей транзакции; либо отдельный маленький пул/одно выделенное соединение под локи; `Acquire` второго соединения — с таймаутом; в `docs/deploy.md` — минимум `pool_max_conns` платформы ≥ (число задач с вложенным захватом + 4) и тест «пул = N» | нет | CONFIRMED (при 4), PLAUSIBLE при 8 под нагрузкой |
| DB-1 | Medium | Миграции / блокировки | `deploy/staging/compose.yaml:131-140` (job `goose up` без `lock_timeout`), `docs/deploy.md:222` (миграции идут при работающей старой версии), `docs/schema-changes.md` §19.1 | DDL миграции ждёт `ACCESS EXCLUSIVE` за любой долгой транзакцией и выстраивает за собой очередь: все чтения/записи таблицы встают до конца миграции. У job нет `lock_timeout`/`statement_timeout` (в `db/migrations` их тоже нет), транзакции платформы не ограничены (AP-5) | Заполненная база (250 тыс. `session_purchases`): транзакция-читатель держит таблицу 6 с → `goose up-to 44` ждёт; обычный индексный `SELECT … WHERE club_id=1 AND session_id='x'` выполнился за **5,2 с** вместо < 1 мс; сама миграция 0044 без конкуренции — 55 мс (`perf2/lockq.log`) | В DSN job миграций `options=-c lock_timeout=5s -c statement_timeout=…` (или `SET lock_timeout` первой строкой каждой миграции после go-live) + повтор job при `55P03`; пункт в §19.1 и в runbook выкладки | нет | CONFIRMED |
| PERF-2 | Low | Агент: перебалансировка | `server/internal/agent/stream/rebalance.go:69-87` (`rebalanceRelease`), `:94-135` (`publishStreams`: доля соседа — его запись до 3 интервалов назад) | Во время массового подключения (старт клуба/платформы, оба инстанса растут одновременно) инстанс сравнивает свой текущий счётчик с устаревшим счётчиком соседа и отправляет `Drain` простаивающим ПК, хотя потоки уже поровну — лишние переподключения TLS | Нагрузочный прогон 1 000 ПК: `ramp` — 1 200 подключений на 1 000 ПК, `zafiro_agent_stream_rebalance_requested_total` app1 = 200, при этом снимки раз в 10 с: 83/84, 167/167, 250/250 … 500/500 (`perf2/load-run1-report.md`, timeline) | Не начинать перебалансировку, пока собственный счётчик растёт быстрее порога (или пока все записи соседей моложе 1 интервала); учитывать возраст записи соседа: `counts[i] + рост × возраст`; минимальный «возраст инстанса»/потоков для старта | нет | CONFIRMED |
| DB-2 | Low | Миграции: откат | `db/migrations/0028_least_privilege.sql` (Down), `0013_agent_image_keys.sql:135-137` (Down), `0032_perf_indexes.sql:96` | `up → down → up` проходит, но `down-to N` не возвращает схему версии N: остаются колоночные `GRANT INSERT/UPDATE(name, phone, …) ON users TO zafiro_app` (Down 0028 даёт табличные права, колоночные не снимает); `agent_certificates_issued_via_check` после Down 0013 — `NOT VALID`; `pg_trgm` остаётся (осознанно, комментарий 0032); функции 0026/0030/0031 теряют строку комментария | Для каждой из 38 версий: схема `up-to N` vs `up` → `down-to N` (`pg_dump -s`, сортировка): расхождения на 25 версиях, все из перечисленных источников (`perf2/downcheck.sh`, `downcheck.log`) | В Down 0028 — `REVOKE INSERT(…), UPDATE(…) ON users`; Down 0013 — `ADD CONSTRAINT … ` без `NOT VALID` (или `VALIDATE`); в CI — тот же скрипт сравнения схем | нет | CONFIRMED |
| DB-3 | Low | Миграции: правила | `docs/schema-changes.md` §19.1 п. 2; `server/internal/db/dbtest` (`Migrate` выполняет файл одной командой) | Правила §19.1 (`NOT VALID`+`VALIDATE`, `CREATE INDEX CONCURRENTLY` в файле `NO TRANSACTION`) нигде не проверяются автоматически, а тестовая база по документу ещё не умеет выполнять файл `NO TRANSACTION` — первая миграция по правилу п. 2 сломает интеграционные тесты | Чтение кода/документа; ни в `.github`, ни в `tools` нет проверки миграций (поиск `NOT VALID`, `CONCURRENTLY`, `squawk`) | Линтер миграций в CI (например, правило: `ADD CONSTRAINT … CHECK` без `NOT VALID` и `CREATE INDEX` без `CONCURRENTLY` на тенантных таблицах — ошибка для номеров > последней до go-live); научить `dbtest.Migrate` выполнять команды по одной | нет | CONFIRMED |
| PERF-3 | Low | Транзакции: CPU внутри транзакции | `agent/guestapi/social.go:252`, `http/legacy/guest.go:897` (проверка кода перевода Argon2id после `LockLatestTransferCode … FOR UPDATE`, `billing/balance_transfer.go:174-184`), `verifycode/recovery.go:106,180,185`, `verifycode/transfer.go:83` | Хэш/проверка Argon2id (19 МиБ, t=2; при bcrypt старого пароля — cost 12) внутри транзакции Postgres: соединение и блокировка строки держатся на время хэша и ожидания семафора хэшера (`2 × GOMAXPROCS`), который при волне входов гостей занят | Статический поиск вызовов внутри замыканий `Tx/InTenantTx/InPlatformTx` (`perf2/txext.txt`); путь входа гостя хэширует вне транзакции (`guestapi/auth.go:145`) — проблема только в редких сценариях. Время не мерилось | Считать/проверять хэш до транзакции (код — из строки, прочитанной без блокировки, затем повторная проверка версии/`attempts` под `FOR UPDATE`), или брать слот хэшера до `BEGIN` | нет | CONFIRMED (код), влияние PLAUSIBLE |
| PERF-4 | Low | Память агентских потоков (оценка ресурсов) | `docs/deploy.md` §12 (≈ 92–97 КБ на поток), `docs/load-test.md` §3.3 | Замер даёт ≈ 190–260 КБ RSS на поток (в 2–2,5 раза больше документа). Для 50 000 ПК на 4 инстансах (12,5 тыс. потоков, после потери одного — 16,7 тыс.) это 2,4–4,3 ГБ против заложенных 4 ГБ на инстанс | Прогон 1 000 ПК: инстанс с 1 потоком — 71–105 МБ, с 500 — 197–205 МБ; после `kill`: 147 потоков — 107 МБ, 853 — 239 МБ (наклон 187 КБ/поток); горутин ≈ 5,3 на поток, утечки нет (2 638 ± 1 за 8 мин steady) | Перемерить на 5 000 ПК (`GODEBUG=madvdontneed=1`/`GOMEMLIMIT`, heap-профиль на поток), обновить §12; при подтверждении — `GOMEMLIMIT` в конфигурации и 6 ГБ на инстанс для 50 000 ПК | нет | CONFIRMED (замер 1 000 ПК), экстраполяция PLAUSIBLE |
| DB-4 | Low | Конфигурация Postgres | `deploy/staging/compose.yaml:79` (только `max_connections`, `shared_buffers`), `docs/deploy.md` §11–12 | Нет базовых настроек Postgres для прода, кроме WAL: `work_mem` 4 МБ — сверка ledger сортирует на диск, `random_page_cost` 4 (для NVMe — 1,1), `effective_cache_size`, `autovacuum_*` для журналов, `idle_in_transaction_session_timeout` на логинах (AP-O7) | `ReconcileUnbalancedTransactions` (полная сверка, 1 млн проводок): 882 мс, `external merge Disk: 33 МБ`; `ListMemberBalanceDrift`: 326 мс, 3 × external merge по ~5,7 МБ | Таблица настроек в `docs/deploy.md` §12 (8 vCPU/32 ГБ: `shared_buffers` 8 ГБ, `effective_cache_size` 24 ГБ, `work_mem` 32–64 МБ, `random_page_cost` 1.1, `max_wal_size` 8 ГБ, `wal_compression` lz4) и те же в staging | нет | CONFIRMED |
| PERF-5 | Low | Тесты производительности | `server/**` — один бенчмарк на весь сервер (`agent/presence/guests_integration_test.go:200`, только с тегом integration) | Нет микробенчмарков горячих путей (обработка `Status`, присутствие, расчёт цены, `money`, сжатие/JSON карты клуба) — регресс производительности CI не увидит | `grep -rn 'func Benchmark' server` | Бенчмарки в пакетах `agent/stream`, `agent/presence`, `pricing`, `money`, `http` (compress), запуск `go test -bench` в ночном CI с порогом (`benchstat`) | нет | CONFIRMED |
| PERF-6 | Low | Платформенные журналы | `db/queries/superadmin.sql` `SuperadminCountActivityLogs`, `SuperadminCountPlatformLogs`; пул платформы без `statement_timeout` (AP-5) | Счётчик журнала суперадминки без фильтра — `count(*)` по всем клубам на каждой странице; растёт линейно с платформой (дополнение к AP-O8, где отмечен только OFFSET) | 1,2 млн строк `activity_logs` (200 клубов): 99 мс, 2 параллельных воркера; при ~500 млн строк (2 000 клубов × годы) — порядка десятков секунд, обрыв по `http.request_timeout` 30 с | Потолок счётчика (`count` по `LIMIT 10001`) или оценка `reltuples` без фильтров; период по умолчанию | да — нужно решение (текст «Записей: N») | CONFIRMED (замер), рост PLAUSIBLE |

## Что проверено и как

- **Стенд БД.** Postgres 18.6 (`postgres:18.6`, как в тестах) в tmpfs, `pg_stat_statements`, `shm_size 1g`; миграции goose
  v3.29.0. Данные: `loadgen fixture -clubs 200 -pcs 25` + `visual-seed` + `perf2/seed.sql` (сидер прошлого аудита, даты
  сдвинуты): крупный клуб — 50 035 членов, 500 ПК, 1 млн проводок ledger, 1 млн строк журнала, 306 тыс. кассовых операций,
  250 тыс. `session_purchases`; всего 155 тыс. пользователей, база 2,38 ГБ.
- **Миграции.** `goose up` (38 файлов, 11,7 с вместе с ожиданием старта) → `reset` (0,3 с) → `up` (0,4 с) — без ошибок.
  Пофайловая эквивалентность Down — `perf2/downcheck.sh` (DB-2). Замер 0044/0045 на заполненной базе: 55 мс / 10 мс;
  очередь блокировок — DB-1. Партиции: воркер `db:ensure-partitions` при старте и раз в сутки на 3 месяца вперёд (на
  стенде — по февраль 2027), оповещение при простое > 3 суток (`zafiro-alerts.rules.yml:135`), партиции DEFAULT нет —
  нормально.
- **Индексы.** Дублей/префиксных дублей btree нет (запрос к `pg_index`). Внешние ключи без индекса — только малые таблицы и
  `ledger_accounts_membership_fk`, `computer_health_*_computer_id_fkey` (удаление ПК с 720 тыс. строк истории — 34 мс:
  выручает skip scan PG 18). Неиспользуемые под нагрузкой (staging, 1 000 ПК, 13 мин): `activity_logs_*_event_idx`,
  `…_subject_type_subject_id_idx`, `…_club_id_event_created_at_idx`, три индекса `audit_log`, четыре `ledger_transactions`
  (`session_id`, `cash_shift_id`, `cash_transaction_id`, `reservation_id`), `cash_transactions_type_index`, trgm-индексы
  `users` — материал для AP-O4 (решение владельца, не дублирую).
- **RLS.** Политики — `club_id = zafiro_current_club_id()` (SQL STABLE, встраивается): накладных расходов нет
  (count с RLS и с явным `club_id = 1` — 3 564 и 3 571 буфер).
- **N+1 и агрегаты в Go.** Свой AST-сканер (`perf2/nplus1/main.go`): 56 вызовов запросов внутри циклов — все пагинация,
  пачки (`slices.Chunk`) или ≤ 2 итераций; агрегат денег в Go — только X-отчёт (AP-O5, известно).
- **Внешние вызовы в транзакциях.** AST-сканер замыканий `Tx/InTenantTx/InPlatformTx` (`perf2/txext/main.go`): HTTP/NATS
  внутри транзакций нет; Valkey — только `legacy/staff.go:678` (`a.presence` в `Tx`, остаток PB-20); Argon2id — PERF-3.
  `idle in transaction` и ожиданий блокировок под нагрузкой — 0 (выборка `pg_stat_activity` раз в 2 с, `pg.lock_waiting`,
  `pg.longest_xact_s` = 0 во всех фазах). Advisory locks — сессионные на выделенном соединении (PERF-1).
- **Горячие пути.** pprof (сборка с overlay `net/http/pprof`, без правки репозитория): смешанная нагрузка панели —
  9,8 % ядра, из них X-отчёт 23 %; дашборд — время в ожидании БД. Valkey под нагрузкой: 667 команд/с на 1 000 ПК
  (`evalsha` лимитов, `hset/set/pexpire` присутствия). Мьютексы горячего пути — по соединению, не общие.
- **Нагрузка.** Staging из `deploy/staging` + `tools/loadgen/compose.load.yaml`, образ — свой (alpine + локально собранные
  `zafiro` с `-tags timetzdata` и `goose` v3.29.0, вместо `task staging:image` — экономия диска), проект compose
  `audit-perf-staging`; без `net.sh` (нет `ip`) — все клубы с одного адреса.
- **Бэкапы.** Прочитаны `docs/runbooks/restore.md`, `deploy/backup/*`: WAL-G, `archive_timeout 60s`, delta-цепочки,
  шифрование OpenPGP, проверки `verify.sql`; `shm_size: 1g` (AP-O6) теперь в staging и dev. `verify.sql` прогнан на
  базе 2,38 ГБ: **2,3 с** (проверка балансов упала ожидаемо — сидер пишет балансы мимо ledger).

## Что не удалось проверить и почему

- **Учения восстановления (`task backup:drill-e2e`)** — не запускались: нужен образ Postgres+WAL-G, собираемый из
  исходников WAL-G (`deploy/backup/Dockerfile`), и SeaweedFS (`chrislusf/seaweedfs:4.48`) — ни того, ни другого нет
  локально, свободно 2,2–2,6 ГБ диска при параллельных аудиторах. Оценка — по конфигурации (выше); риск — не измерены
  время восстановления на больших данных и PITR.
- **Полный сценарий 5 000 ПК / 30 мин** — не запускался (ресурсы общие); прогон — 1 000 ПК, steady 8 мин. Пик `-peak-mult 10`
  на 1 000 ПК дал лишь ×1,6 денежных операций (множитель считается от гостей в сессии), поэтому пик дополнен своим
  стресс-тестом пополнений (ниже).
- **Метрики пиковой фазы after-peak** (REST p50 ≈ 35 мс вместо 4–5) искажены моим параллельным профилированием на другом
  стенде в ту же минуту — в выводах не используются.
- **Длительная проверка утечек** — только 13 мин под нагрузкой (горутины и heap стабильны); суточного прогона не было.
- **Суперадминка по HTTP** — вход требует TOTP (у `visual-seed` нет ключа): запросы проверены `EXPLAIN` под логином
  `zafiro_platform_login`.

## Измерения (это «до»; команды для повтора)

Подготовка стенда БД (P = `/tmp/claude-0/-home-user-zafirogo/541f6760-cdd0-504b-ac95-cb07a87b9341/scratchpad/audit/perf2`):

```bash
docker run -d --name audit-perf-pg --shm-size=1g --tmpfs /var/lib/postgresql:rw,size=4500m \
  -e POSTGRES_DB=zafiro -e POSTGRES_USER=zafiro -e POSTGRES_PASSWORD=perf_owner_pw \
  -e ZAFIRO_DB_APP_USER=zafiro_app_login -e ZAFIRO_DB_APP_PASSWORD=perf_app_pw \
  -e ZAFIRO_DB_PLATFORM_USER=zafiro_platform_login -e ZAFIRO_DB_PLATFORM_PASSWORD=perf_platform_pw -e TZ=UTC \
  -p 127.0.0.1:57432:5432 -v /home/user/zafirogo/db/init:/docker-entrypoint-initdb.d:ro postgres:18.6 \
  postgres -c shared_preload_libraries=pg_stat_statements -c track_io_timing=on -c shared_buffers=512MB -c max_wal_size=1GB
GOOSE_DRIVER=postgres GOOSE_DBSTRING="postgres://zafiro:perf_owner_pw@127.0.0.1:57432/zafiro?sslmode=disable" goose -dir db/migrations up
cd server && go build -tags loadgen -o $P/loadgen ./cmd/loadgen && go build -o $P/visual-seed ./cmd/visual-seed
$P/loadgen fixture -clubs 200 -pcs 25 -out $P/load.json
$P/visual-seed -fixture $P/load.json -db "postgres://zafiro:perf_owner_pw@127.0.0.1:57432/zafiro?sslmode=disable"
$P/q.sh -f - < $P/seed.sql                 # 1 мин 37 с, 2 377 МБ
go build -overlay $P/overlay.json -o $P/zafiro ./cmd/zafiro   # + pprof на AUDIT_PPROF_ADDR
# valkey/nats: audit-perf-valkey (57379, пароль perf_valkey_pw), audit-perf-nats (57222)
$P/srv.sh start; TOK=$($P/login.sh)
python3 -I $P/lat.py http://127.0.0.1:58191 "$TOK" $P/routes.txt 100 4      # таблица маршрутов
$P/exp.sh "$(python3 -I $P/getq.py ИмяЗапроса)" "(типы)" "(значения)" [custom|generic]   # EXPLAIN как pgx
$P/downcheck.sh                              # эквивалентность Down
```

**Панель, крупный клуб (100 запросов, 4 потока; журнал — 10; машина общая, load average 11–14 — мс шумят):**

| маршрут | p50 | p95 | p99 | gzip, байт |
|---|---|---|---|---|
| `/api/dashboard` (один поток, тихо) | 15–30 | — | — | 426 |
| `/api/dashboard` (2 потока) | 20,0 | 50,8 | 69,8 | 426 |
| `/api/club-map` / `state` / `state?ids=` | 45,0 / 32,5 / 8,7 | 110 / 60 / 21 | 140 / 83 / 62 | 28 405 / 16 223 / 524 |
| `/api/cash/reports/x` | 113,6 | 204 | 218 | 2 220 |
| `/api/cash/customers` (пусто, AP-O2) | 173,0 | 550 | 686 | 471 |
| `/api/cash/customers?q=Ахмет` / `?q=7705001` | 39,3 / 63,8 | 92,8 / 151 | 116 / 183 | 452 / 479 |
| `/api/customers` / `?q=Ахмет` / `?q=а` | 49,7 / 66,6 / 230,7 | 117 / 156 / 456 | 135 / 182 / 548 | 471 / 927 / 1 080 |
| `/api/activity-logs` (AP-O1) | 1 027–1 107 | 1 471–1 868 | — | 388 818–388 901 |
| `/api/activity-logs?search=ПК 417` | 5 805–6 631 | 7 580–8 671 | — | 387 960 |
| `/api/activity-logs?from=2026-11-01&to=…` / `?event=` / `?user_id=` | 584 / 543 / 884 | 919 / 862 / 1 198 | — | ≈ 388 600 |
| `/api/chat` | 27,8 | 46,0 | 89,1 | 1 875 |
| остальные 25 маршрутов | 2–17 | 4–75 | — | `perf2/lat-before.md` |

Топ `pg_stat_statements` (30 проходов по всем маршрутам): `CountActivityLogPage` 440 мс / 26 914 буферов,
`ListActivityLogUsers` 406 мс / 341 488 буферов / 50 010 строк, страница журнала 365 мс; `CustomersCount` с поиском 67,6 мс /
3 162 буфера (`perf2/pss-top.txt`).

**Фоновые задачи (крупный клуб):** полная сверка ledger — `ListMemberBalanceDrift` 326 мс, `ReconcileUnbalancedTransactions`
882 мс, `ReconcileCashDrawerSnapshot` 0,06 мс; `ExpireDueSessions`, `ListExpiredTimePackages` — индексные, < 0,1 мс;
`SuperadminCountActivityLogs` 99 мс (1,2 млн строк), `SuperadminActivityLogs` OFFSET 5000 — 8 мс; `verify.sql` — 2,3 с.

**Нагрузочный прогон (staging, 2 инстанса, 1 000 ПК в 40 клубах, 80 WS сотрудников):**

```bash
STG_IMAGE=audit-perf-zafiro:staging docker compose -p audit-perf-staging -f deploy/staging/compose.yaml \
  -f tools/loadgen/compose.load.yaml --env-file deploy/staging/.env.staging up -d
$P/loadgen fixture -clubs 40 -pcs 25 -out $P/load/fixture.json && $P/visual-seed -fixture $P/load/fixture.json -db <owner DSN :15432>
$P/loadgen prepare -staging ../deploy/staging -certs $P/load/certs.json -enroll-sample 20
$P/loadgen run -staging ../deploy/staging -project audit-perf-staging -certs $P/load/certs.json -out $P/load/run1 \
  -ramp 1m -steady 8m -peak 1m -peak-mult 10 -settle 1m -restart graceful,kill
python3 -I $P/stress.py 40 1 30 ; python3 -I $P/stress.py 40 2 25 ; python3 -I $P/stress.py 40 4 30   # стресс пополнений
```

| Фаза | Операция | n | p50 | p95 | p99 | max | ошибок |
|---|---|---|---|---|---|---|---|
| prepare | код регистрации (REST) / `Enroll` через nginx | 20 | 2,2 / 5,3 | 4,2 / 7,8 | — | — | 0 |
| ramp 60 с | подключение ПК (TLS + Hello) | 1 200 (из них 200 — PERF-2) | 7,5 | 11,5 | 18,7 | 68,6 | 0 |
| steady 8 мин | покупка минут / тарифа (gRPC) | 111 / 110 | 14,2 / 14,0 | 21,1 / 21,0 | 23,0 / 22,9 | 56,7 / 33,6 | 0 |
| steady | пополнение / продажа на кассе (REST) | 93 / 41 | 9,9 / 9,0 | 18,2 / 17,0 | 26,2 / 36,3 | 41 / 110 | 0 |
| steady | вход гостя (Argon2id) / завершение сессии / заказ | 237 / 38 / 66 | 34,2 / 11,4 / 11,1 | 59,6 / 23,4 / 19,2 | 70,7 / 88,0 / 20,7 | — | 0 |
| steady | REST: карта `state` / журнал / дашборд / X-отчёт | 173 / 62 / 77 / 181 | 4,3 / 7,7 / 4,3 / 3,9 | 9,6 / 11,9 / 8,7 / 8,9 | 15,2 / 17,9 / 10,4 / 16,6 | — | 0 |
| restart-graceful | `docker stop app1`: остановка 10,4 с, переподключение 500 ПК | 500 | 6,5 | 13,5 | 25,2 | 47 | 0 обрывов |
| restart-kill | `docker kill app2`: все снова на связи через 2,1 с | 600 | 5,8 | 49,0 | 91,7 | 115 | 600 EOF (ожидаемо), денег — 0 |

Ресурсы steady: CPU инстанса 3,3 % ядра (макс. 13 %), RSS 197–209 МБ на 500 потоков, горутин 2 638/2 664 (постоянно),
пул app занято ≤ 1 из 16, платформы — 14 соединений открыто; Postgres 3,2 % (макс. 9,5 %), 30 транз./с, WAL 54 КБ/с;
Valkey 905 команд/с (пик 2 132), NATS 40/81 сообщ./с; `pg.lock_waiting` = 0, `pg.deadlocks` = 0, `replay_mismatch` = 0.

Стресс денежного пути (пополнения на кассе напрямую в app1/app2, закрытый цикл, 40 клубов):
40 потоков — **376–387 оп./с**, p50 100 / p95 163–185 / p99 195–240 / max 358 мс; 80 потоков — 404 оп./с, p50 195 / p95 271 /
p99 316 мс; 160 потоков — 398 оп./с, p50 397 / p95 506 / p99 564 мс; 0 ошибок. Узкое место — Postgres (178 % CPU, инстансы по
55 %); на одно пополнение — 10,7 вставок, 4 обновления, ≈ 590 обращений к буферам, 3 транзакции (вместе с outbox).
Это ≈ ×400 от steady этого прогона и ≈ ×6 от пика 5 000 ПК из `docs/load-test.md` (65/с).
