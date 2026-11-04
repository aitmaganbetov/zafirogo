# Вопросы Этапа 6 — O1 «образ и выкладка»

Код и файлы: `Dockerfile`, `.dockerignore`, `deploy/staging/**`, `deploy/README.md`, `deploy/trivy-ignore.yaml`,
`docs/deploy.md`, `.github/workflows/release.yml`, `.github/dependabot.yml` (ecosystem `docker`), `Taskfile.yml`
(`image`, `staging:*`), `server/internal/grpc/proxyproto.go`, `server/internal/config` (`agent.grpc.proxy_trusted`,
`agent.grpc.proxy_header_timeout`), `server/internal/app/app.go` (обёртка listener), `server/cmd/zafiro/main.go`
(`main.buildVersion`), `server/internal/app/staging_test.go` (тег `staging`).

Решения приняты по принципам `docs/DECISIONS.md` (владелец поручил); отменить может только владелец.

## Открытые (нужна работа до прода)

- [ ] **S6-O1-6. TLS внутренних соединений в проде.** Staging — без TLS внутри изолированной сети compose.
  - **Рекомендация:** Postgres — `sslmode=verify-full` в DSN (управляемый Postgres его требует); Valkey и NATS — TLS,
    если сеть делится с другими системами; в выделенной сети/VPC — допустимо без TLS. Решает инфраструктура прода.

## Принятые решения (подтвердить)

- [x] **S6-O1-1. Ротация выпускающего CA агентов — сделано (вариант а).** Ключ `agent.pki.extra_trust_files`
  (`ZAFIRO_AGENT_PKI_EXTRA_TRUST_FILES`, список PEM; из каждого файла — первый сертификат, тот же формат, что
  `ca_cert_file`; не CA → ошибка запуска; совпадение с `ca_cert_file` → ошибка конфигурации) → `pki.LoadCA` →
  `ClientCAs` и `CA.Verify`. Выпускает и продлевает только текущий CA (`RenewCertificate` → `CA.Issue`, в ответе —
  цепочка нового CA), поэтому продление переводит ПК на новый CA. Корень в доверие сервера не попадает (сертификат
  «соседнего» выпускающего CA под тем же корнем — отказ). Агент закрепляет корень, а не выпускающий CA, и уже
  принимает несколько корней (`RootCaPem`/`RootCaFile` — PEM-список; `TrustAnchors.FromPem`). Исправлено одно: склейка
  файлов корней без перевода строки (`cat old.pem new.pem`, в конце первого нет `\n`) давала 0 корней и агент не
  запускался — теперь читается (тест на два корня, склеенных и через перевод строки). Доставка корней «из Welcome» не делалась: добавление корня по каналу сервера
  расширяло бы доверие без подписи CI; новый корень — через подписанное обновление/установщик. Регламент —
  `docs/deploy.md` §5 «Ротация CA агентов». Тесты: `pki.TestRotateIssuingCA` (рукопожатие mTLS и Verify),
  `config.TestAgentPKIExtraTrustFiles`, интеграционный `app.TestAgentCARotation` (подключение и продление со старым
  сертификатом, отказ без `extra_trust_files`), агент `CredentialTests.Несколько_закреплённых_корней…`.
- [x] **S6-O1-4. Ключи `/api/v1` при поклубном переходе — сделано (вариант б).** Весь `/api/v1/` идёт в zafirogo;
  `v1.LegacyUpstream` (`server/internal/http/sections/v1/upstream.go`, подключается в `app` вместе со слоем мобильных
  через `httpserver.Options.Legacy`): ключ правильного формата, чьего префикса нет в `api_keys`, — обратный прокси в
  Laravel без изменений (`Host` — `legacy_upstream_host`, `X-Forwarded-For` — адрес клиента); свой ключ (в т. ч.
  отозванный/истёкший/с неверным секретом), запрос без ключа или с ключом не того формата — здесь. Адрес —
  `integrations.legacy_upstream` (+ `_host`), пусто — `mobile.upstream` (тот же Laravel; одна настройка на переход).
  Карта префиксов в nginx больше не нужна: `deploy/staging/nginx/maps` удалён, `location ~ ^/api/(auth|guest|staff|v1)/`
  → zafirogo; регламент go-live (README переноса, п. 8–10) упрощён. Откат клуба после go-live — `DELETE FROM api_keys
  WHERE club_id = …` в zafirogo (иначе его ключи получали бы здесь 402).
  - Принято: проксируемые запросы не пишутся в `api_request_logs` zafirogo и не считаются в его блокировку подбора по
    IP (журнал и лимит — у Laravel, как раньше); цена — один индексный запрос по префиксу на запрос `/api/v1`, пока
    задан upstream. Ошибка базы при поиске префикса — 500 здесь (не прокси: иначе сбой базы отправил бы свои ключи в
    Laravel, где клуб заморожен). Laravel недоступен — 502 `server_error` в конверте v1.
  - Тесты: `v1.TestLegacyUpstreamRouting` (unit), интеграционный `v1.TestV1LegacyUpstream` (свой, отозванный, чужой
    ключ, без ключа; Host, XFF, путь с query), `config.TestIntegrationsUpstream`.

- [x] **S6-O1-2. Миграции в образе — `goose` CLI, а не подкоманда `zafiro migrate`.** `goose` v3.29.0 (та же версия, что
  `task tools` и CI), собран только с драйвером Postgres (`no_mysql no_sqlite3 …`, 12,6 МБ), миграции — `/migrations`.
  Job: `goose up` с `GOOSE_DBSTRING` владельца схемы. Почему не библиотека в `zafiro`: (1) миграции лежат вне модуля
  `server` (`go:embed` их не видит — пришлось бы копировать при сборке); (2) новая зависимость в `go.mod`, который
  параллельно меняли другие блоки; (3) бинарник приложения не получает кода DDL и не нужен DSN владельца — разделение
  ролей (PD-9) видно уже по команде. Поведение то же, что `task db:migrate` (таблица `goose_db_version`).
- [x] **S6-O1-3. PROXY protocol на gRPC-листенере** (`grpcserver.NewProxyListener`): заголовок принимается только от
  `agent.grpc.proxy_trusted` и от них **обязателен** (иначе клиент в доверенной сети мог бы выдать себя за
  балансировщик и наоборот); от остальных адресов не читается (прямые подключения работают). Поддержаны v1 (nginx
  stream умеет только его) и v2 (HAProxy, AWS NLB). Заголовок читается лениво в горутине соединения (не в `Accept`),
  с таймаутом `proxy_header_timeout` 5 с и с сохранением дедлайна grpc-go. Своя реализация (~250 строк + тесты), а не
  `pires/go-proxyproto`: нужна политика «обязателен от доверенных, игнор от прочих» и без новой зависимости. HTTP
  PROXY protocol не нужен: там `X-Forwarded-For` от `http.trusted_proxies`.
- [x] **S6-O1-5. Балансировщик — nginx 1.30.5, не Caddy.** Нужны одновременно HTTPS, L4-проброс с PROXY protocol и
  `map` по заголовку для `/api/v1`; у Caddy L4 — только сторонний плагин (своя сборка). nginx уже назван в
  `docs/mobile-compat.md` и README переноса. Версия 1.30.5 (26.09.2026), а не 1.30.6 (29.10.2026, моложе 14 дней,
  PD-4; список изменений с nginx.org из среды недоступен — если 1.30.6 исправляет уязвимость, обновить сразу).
- [x] **S6-O1-7. Образы staging — переменные `STG_*_IMAGE` с тегом и digest** (по умолчанию — Docker Hub; локально
  при 429 — `mirror.gcr.io`, тот же digest). Dependabot `docker-compose` не обновляет `${VAR:-…}`: образы Postgres,
  Valkey, NATS, nginx обновляются вручную вместе с `docker-compose.dev.yml` (Dependabot подскажет по dev-compose).
  Переменные — с префиксом `STG_`, потому что `Taskfile.yml` подгружает `.env` разработки в окружение, а окружение
  важнее `--env-file` при подстановке compose.
- [x] **S6-O1-8. Секреты в compose — файлы** (`secrets:`): конфигурация сервера с ключами — YAML
  (`ZAFIRO_CONFIG_FILE=/run/secrets/zafiro_config`), PKI — четыре файла. Compose без swarm монтирует файлы с правами
  хоста, поэтому в staging они `0644` внутри каталога `0700` (процесс — uid 65532). Прод/k8s — `defaultMode: 0400` +
  `runAsUser`/`fsGroup` 65532. Поддержка `ZAFIRO_*_FILE` в конфигурации не понадобилась (YAML-файл уже секрет).
- [x] **S6-O1-9. Образ — только linux/amd64.** Серверы прода — x86-64; arm64 добавить в `release.yml` (`platforms`),
  если появится такая площадка.
- [x] **S6-O1-10. Версия в образе.** В образе нет `.git`, сведения VCS пусты — добавлен `-X main.buildVersion=<тег>`
  (`server/cmd/zafiro/main.go`, 5 строк): версия в логе `starting zafiro` и `zafiro -version`.
- [x] **S6-O1-11. `npm ci --ignore-scripts` при сборке образа**: postinstall-скрипты зависимостей не выполняются
  (защита цепочки поставок); сборка Vite/Tailwind/esbuild работает на optionalDependencies. Node 22.23.3 (23.09.2026),
  а не 22.23.4 (20.10.2026 — моложе 14 дней). `tsc -b` проверяет и тесты, поэтому в стадию `web` копируется
  `server/internal/cash/testdata/vat.json` (общая таблица НДС).
- [x] **S6-O1-12. Публикуется ровно просканированный образ** (`docker save` → artifact → `docker load` → push), а не
  пересобранный в job публикации; `packages: write` и `id-token: write` — только в job `publish` (окружение
  `release`). Подпись cosign keyless — по переменной `COSIGN_SIGN=true` (нужен публичный Rekor; если запрещено
  политикой — ключевая подпись с ключом в секретах окружения `release`).
- [x] **S6-O1-13. Проверка staging — Go-тест с тегом `staging`** в `server/internal/app/staging_test.go` (вне списка
  файлов O1: клиенту агента нужны `server/gen` и `internal/pki`, а это пакеты модуля `server`; тег не входит ни в
  `go test ./...`, ни в `-tags=integration`). Тест сам останавливает и запускает инстанс (`docker compose stop/start`),
  убирает за собой (выход гостя, отзыв тестового ПК). `task staging:seed` берёт демо-данные визуального сравнения и
  делает подписки активными от «сейчас» (демо датировано октябрём 2026).
- [x] **S6-O1-14. Покупка «в момент остановки».** На staging `docker compose stop` завершается за 0,5–1 с (поток
  агента уходит по `Drain` сразу), поэтому покупка, отправленная одновременно с остановкой, успевала завершиться;
  обрыв ответа на середине не воспроизведён. Проверено главное: повтор с тем же ключом на другом инстансе возвращает
  прежний результат, повтор ранней покупки — тоже, списание ровно одно. Сценарий «ответ потерян» на уровне сервиса
  покрыт интеграционными тестами идемпотентности (`guestapi`).
- [x] **S6-O1-15. nginx: пассивные проверки upstream** (OSS nginx не умеет активные): инстанс при остановке сам
  отвечает 503 на `/readyz` и закрывает listener; nginx повторяет на другом инстансе только запросы, которые не ушли
  (ошибка соединения) или идемпотентные 502/503/504 — POST с деньгами не повторяется (повтор — клиент с ключом
  идемпотентности). Адреса инстансов — `server … resolve` (nginx ≥ 1.27.3) через DNS docker.
- [x] **S6-O1-16. Лимит дескрипторов nginx.** `ulimits` в compose в непривилегированной среде Docker не применяется
  (контейнер не стартует), поэтому в staging не задан (`worker_rlimit_nofile` пишет предупреждение). Прод —
  `LimitNOFILE`/`ulimits` ≥ 2 × число ПК на узел (`docs/deploy.md` §11).
