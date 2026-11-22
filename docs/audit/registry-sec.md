# Реестр аудита — безопасность (SEC), 22.11.2026

Ветка `audit/full-system-2026-11` (`e4e1156`), репозиторий не менялся (`git status` чист). Прежние находки
(`docs/security-review.md` SEC-001…021, `docs/open-questions/audit-security.md` A-01…A-14, `audit-ci.md` AC-*,
решения «ревью 22.11.2026») не повторяются. Регрессий прежних исправлений не найдено (см. «Что проверено»).

| ID | Приоритет | Категория | Файл:строка | Проблема | Сценарий/доказательство | Предлагаемое исправление | Меняет бизнес-логику? | Статус |
|---|---|---|---|---|---|---|---|---|
| SEC-1 | Medium | AuthN / rate limit (OWASP A07, ASVS V2.2.1), переходный период | `server/internal/http/legacy/auth.go:632` (login), `:964` (passwordRoute); `legacy.go:111-126` (ReverseProxy) | Вход и восстановление пароля мобильного приложения, которые уходят в старый Laravel (телефон без клубов здесь / клуб каталога не здесь), пересылаются **до** любого лимита zafirogo (`a.forward` раньше `a.throttle(CustomerLogin/PasswordRecovery)`), и до пересылки на каждый запрос идёт запрос в Postgres (`localUser`). Laravel лимитирует по `$request->ip()`, а `TrustProxies` в старом проекте не настроен (`bootstrap/app.php` без `trustProxies`) — для Laravel все такие клиенты имеют один IP — адрес zafirogo. | Стенд: `ZAFIRO_MOBILE_UPSTREAM` → фейковый Laravel (журнал запросов). 25 подряд `POST /api/auth/login` (`device_name`, неизвестный телефон, разные пароли) — 25×401, все 25 дошли до апстрима, ни одного 429 от zafirogo; 12 `POST /api/auth/password/code` — все 12 пересланы. В апстрим уходит `X-Forwarded-For: <реальный клиент>` (заголовок клиента отброшен — ок), но старый Laravel его не читает (PLAUSIBLE для прода: зависит от конфигурации боевого Laravel). Следствие: `customer-login-ip` 300/мин и `password-recovery-ip` 30/мин Laravel становятся **общими на всех** гостей непереехавших клубов → один клиент с 300 запросами/мин блокирует мобильный вход всем; 5 неверных попыток на телефон жертвы блокируют её вход глобально. | Применять те же правила `customer-login` / `password-recovery` (ключи как в Laravel) **до** `forward`; в старом Laravel на время перехода — `trustProxies(at: <адреса zafirogo>)`, иначе — записать в `docs/mobile-compat.md` §3.4 как ограничение. Тест: 6-й запрос на один телефон с одного IP при апстриме — 429 без обращения к апстриму. | нет (лимиты те же, что в Laravel) | CONFIRMED (zafirogo), PLAUSIBLE (Laravel) |
| SEC-2 | Medium | Защита каналов (ASVS V9.2.2, V14.1) / конфигурация | `server/internal/app/core.go:114-121`; `server/internal/config/validate.go:118-145`; `docs/deploy.md:125-126` | У клиента Valkey нет `TLSConfig` и нет настройки TLS вообще — требование `docs/deploy.md` «Valkey/NATS — TLS, если сеть общая» выполнить нельзя. В `prod` не проверяется: пароль Valkey (может быть пустым), `sslmode` Postgres (pgx по умолчанию `prefer` — без проверки сертификата, понижается до открытого канала), учётные данные NATS. В Valkey лежат сессии, refresh-семейства, билеты WS, хеши гостевых токенов и счётчики лимитов. | Чтение кода: `valkey.ClientOption{InitAddress, Password, DisableCache}`; `validateConnections` проверяет только формат адресов. Если Valkey/Postgres на другом хосте/в общей сети — пароль Valkey (`AUTH`) и данные сессий идут открытым текстом. | Конфиг `valkey.tls` (CA-файл, `ServerName`) → `ClientOption.TLSConfig`; в `prod` — ошибка запуска без `valkey.password`, без `sslmode=verify-full`/`verify-ca` в обоих DSN (или явный флаг `database.allow_insecure` для изолированной сети), без пользователя в `nats.url`. | нет | CONFIRMED (код), риск зависит от топологии прода |
| SEC-3 | Medium | SCA / образы (A06) | `deploy/backup/Dockerfile:15` | WAL-G собирается **Go 1.25.14** — ветка Go 1.25 больше не поддерживается (актуальные 1.26/1.27). По OSV на 21.11.2026 в стандартной библиотеке 1.25.14 не исправлены 15 уязвимостей, в т.ч. GO-2026-6809 (crypto/x509: недоверие скомпрометированным промежуточным CA), GO-2026-6610/6605 (HTTP-клиент), GO-2026-6608 (лимит памяти MIME). WAL-G — HTTP/TLS-клиент к S3 в контейнере сервера БД. | Локальная база OSV (`osv-vulnerabilities` Go/all.zip), сопоставление диапазонов stdlib с 1.25.14 — список в «Измерениях». | `GO_IMAGE=golang:1.27.x-trixie@sha256:…` (или 1.26.x) — WAL-G v3.0.9 должен собираться новее; добавить `deploy/backup/Dockerfile` в проверку версий Go (`tools/check-versions.sh`). | нет | CONFIRMED |
| SEC-4 | Low | SCA / Go stdlib (A06) | `server/go.mod:3`, `Dockerfile:22` (`golang:1.27.3`) | govulncheck (база OSV): GO-2026-6859 / CVE-2026-97054 — гонка заголовка `Connection: close` в `http.MaxBytesReader` (паника HTTP/1 сервера), исправлено в 1.27.4 (11.11.2026). Достижимость низкая: `MaxBytesReader` получает обёрнутый `ResponseWriter` (`RequestLogger`/`compress`), а хук `requestTooLarge` есть только у `*http.response` — заголовок не трогается. | `govulncheck -db file://<OSV> ./...` → «Your code is affected by 1 vulnerability from the Go standard library» (трассы через `bodylimit.go:40`, `v1/upstream.go:212`). Ещё 4 уязвимости модулей (x/net html ×2, klauspost/compress, x/crypto/openpgp) — код их не вызывает. | `go 1.27.4` в `go.mod` и образ `golang:1.27.4-…@sha256` после 14-дневного cooldown (≈ 25.11); x/net ≥ 0.62.0, klauspost/compress ≥ 1.20.2 — через Dependabot. | нет | CONFIRMED (уязвимость), PLAUSIBLE (эксплуатация) |
| SEC-5 | Low | Агент: обновления (ASVS V10.3.2, V14.2) | `agent/src/Zafiro.Agent.Core/Updates/UpdateVerifier.cs:70-84`; `agent/src/Zafiro.Agent.Core/Runtime/AgentRuntime.cs:867-877` | Нет защиты от отката: агент ставит любую версию с верной подписью, не сравнивая её со своей (`AgentVersion`) и не проверяя канал. Подписанное сообщение не содержит срока — однажды подписанный старый уязвимый установщик годен навсегда. От отката сейчас защищает только сервер (`release.go` шлёт `UpdateAvailable` лишь ПК со старой версией, `isOutdated`) — то есть доверие к серверу, а подпись Ed25519 задумана как защита именно от скомпрометированного сервера. | Чтение кода: `Verify()` — поля, хост, ключ, подпись; `StartUpdate()` — сразу `DownloadAndInstallAsync`. Скомпрометированный инстанс/NATS может прислать `UpdateAvailable` версии 1.0.0 с её настоящей подписью → агент 2.x откатится. | В агенте: отклонять `u.Version <= AgentVersion` (сравнение как `isOutdated`) и канал ≠ своему; хранить «наибольшую установленную версию» в каталоге состояния (ACL SYSTEM). Тест в `Zafiro.Agent.Tests`. | нет | CONFIRMED |
| SEC-6 | Low | Агент: гостевой токен (ASVS V3.3.1) | `server/internal/agent/stream/guest.go:38-39`; `server/internal/agent/guestapi/tokens.go:43,405-411` | Новое (956862d, ревью 22.11): срок гостевого токена продлевается, пока **ПК сообщает** `Status.state = IN_SESSION` с этим гостем; наличие идущей сессии на сервере не проверяется. ПК с сертификатом (ключ образа — SEC-003, админ ПК) может держать токен гостя, ушедшего без «Выйти», до 30 дней вместо 12 ч (Q-038) и тратить его баланс. | Чтение кода: `ComputerGuest(..., inSession = s.GetState()==IN_SESSION)` → `ComputerUserRenew`; в `token_renew_integration_test.go` продление идёт на произвольных `SessionId` «s-1», «s-2». | Продлевать, только если в присутствии/БД есть активная сессия этого гостя на этом ПК (`club_computers`/`computer_sessions`, сессия не гостевая и `user_id` совпадает); иначе — без продления. | нет | CONFIRMED (код) |
| SEC-7 | Low | AuthN / rate limit (ASVS V2.2.1) | `server/internal/auth/ratelimit/rules.go:199` (`digits(s.Phone)`); `server/internal/auth/service.go:172` (`NormalizePhone`) | Ключ лимита «IP + телефон» — цифры как ввёл клиент, а вход нормализует номер: `7010000005`, `77010000005`, `87010000005` — один аккаунт, три ключа. Предел 5/мин на (IP, телефон) фактически 15/мин; так же у `customer-login`, `password-recovery`, `web-register`. (В Laravel ключ такой же — `preg_replace('/\D+/', …)`.) | Стенд: после 5 неверных входов на `+77010000005` — 429 для `+7…`, `8…`, но `7010000005` → 422 «Неверные учётные данные» (лимит не сработал), с верным паролем тот же вариант → 200. | Ключ — `NormalizePhone()` (как вход), а не просто цифры. | нет | CONFIRMED |
| SEC-8 | Low | Политика паролей (ASVS V2.1.1) | `server/internal/http/sections/auth/register.go:212-219`; `legacy/auth.go` (`msgPasswordMin`), `guestapi/auth.go:46` | Минимум 6 символов, без проверки по спискам утёкших паролей — для владельцев/администраторов клубов (доступ к деньгам клуба) ниже ASVS L2 (≥ 12). Перенесено из Laravel буквально. | Чтение кода; Laravel: `min:6`. | Решение владельца: для ролей персонала и суперадмина — ≥ 12 (или ≥ 8 + проверка по списку частых паролей) при регистрации и смене; гостям оставить 6. | да — нужно решение | CONFIRMED |
| SEC-9 | Low | Кэш / заголовок Host (A05) | `server/internal/http/legacy/catalog.go:306,500-505`; `legacy.go:115-121` | Если `mobile.upstream_host` не задан (в `prod` не обязателен), запрос каталога в Laravel идёт с `Host` первого клиента, а ответ кладётся в общий кэш по ключу `index|город|язык` без хоста; прокси `forward` тоже передаёт `Host` клиента. Клиент с поддельным `Host` может на 30 с подменить/обнулить клубы Laravel в каталоге для всех (если боевой Laravel отвечает по-разному на разные `Host`). | Чтение кода; в staging `ZAFIRO_MOBILE_UPSTREAM_HOST` задан — там не воспроизводится. | В `prod` при заданном `mobile.upstream` требовать `mobile.upstream_host` (validate), либо не брать `Host` из запроса. | нет | PLAUSIBLE |

## Что проверено и как

- **gosec v2.30.0** (как в CI: `gosec -quiet -exclude-generated -exclude-dir=gen -exclude-dir=store ./...`, Go 1.27.3) — 0 находок, 24 с.
  Подавлений `#nosec` — 135 (G115 ×63, G101 ×36, G304 ×14, G204 ×13, G404 ×9 …); выборочно просмотрены G101/G107/G304/G204 —
  обоснованы (тексты, а не секреты; `release.go:150` — адрес из константы `installerURL`, не из запроса).
- **govulncheck v1.9.0**: `vuln.go.dev` закрыт прокси (403) → собрана локальная база в формате vulndb из
  `osv-vulnerabilities.storage.googleapis.com/Go/all.zip` (4 820 записей GO-*, изменена 21.11.2026), `govulncheck -db file://…` → SEC-4.
- **semgrep 1.95.0** (venv; реестр `semgrep.dev` закрыт → правила из `github.com/semgrep/semgrep-rules`: `go/`, `generic/secrets/`),
  303 правила на 442 файлах `server/` (без тестов и генерации) — 43 срабатывания, все ложные/осознанные: bcrypt-хеши в фикстурах
  loadgen и тестовых данных, CSRF-cookie без HttpOnly (double-submit, так задумано), `w.Write` готовых тел, `math/rand` в
  балансировке/нагрузке, копия `url.URL` (не общая). Правила `p/typescript`, `p/react` не запускались (см. ниже).
- **npm audit** (`web/`): `--omit=dev` — 0; всё — 9 moderate, один корень `minimatch` GHSA-g3hv-75wg-m249 в eslint /
  typescript-eslint (= принятая SEC-012, по-прежнему не обновлена).
- **NuGet агента**: 58 пакетов из `agent/**/packages.lock.json` сверены с OSV NuGet/all.zip (свой сопоставитель диапазонов,
  проверен на `System.Text.Json 8.0.0` → 2 известные CVE) — 0 уязвимых.
- **gitleaks v8.28.0** (docker, `git` по всей истории, 133 коммита, 22 МБ, `--redact`) — 10 срабатываний `generic-api-key`, все
  ложные: `deploy/staging/nginx-test.sh:103` (тестовый токен журнала), `agent/tests/Zafiro.Agent.Tests/UiTests.cs:434`,
  `server/cmd/migrate-from-mysql/e2e_integration_test.go:174`, `server/internal/migrate/migratetest/m2.go:153` (фиктивный `zfk_`),
  `agent/src/Zafiro.Agent.Core/Credentials/{ICredentialStore,FileCredentialStore,WindowsCredentialStore}.cs` (имена параметров),
  `server/internal/config/config_test.go:275`, `server/internal/app/app_integration_test.go:129`. `git grep` по шаблонам
  приватных ключей/AWS/GitHub/Slack/Telegram-бота — 0; в git нет `.pem/.key/.env` (кроме `.env.example`, только dev-значения).
- **Образы**: все `FROM`/`image:` в `Dockerfile`, `deploy/**` закреплены тегом + digest; без digest — только dev/тесты
  (`docker-compose.dev.yml`, `tools/visual`, `agent/tests/integration`; уже AC-O4). Итоговый образ — distroless `nonroot`,
  `USER 65532`, без оболочки; staging: `read_only`, `cap_drop: ALL`, `no-new-privileges`, секреты файлами, Postgres/Valkey —
  только 127.0.0.1, метрики `:9090` без публикации, `/metrics` на балансировщике — 404, Grafana — пароль из файла, без анонимов.
- **Стенд** (отдельный compose `audit-sec2`, свои порты, демо-данные `tools/visual`, бинарник `cmd/zafiro` из HEAD, лимиты
  включены; удалён после проверки): заголовки (CSP с `connect-src` своего хоста, HSTS, XFO DENY, COOP/CORP, nosniff,
  Permissions-Policy) на SPA и API; CORS — заголовков нет (preflight чужого Origin — 404); `/debug/pprof`, `/debug/vars` — SPA,
  на порту метрик — 404; cookie `__Secure-zafiro_rt` (HttpOnly, Secure, SameSite=Strict, Path=/api/auth) и `__Host-zafiro_csrf`;
  refresh без CSRF — отказ, `Sec-Fetch-Site: cross-site` — 403, повтор refresh — 409; лимит входа панели и мобильного —
  429 с 6-й попытки; `X-Forwarded-For` от недоверенного клиента не влияет на ключ лимита.
- **IDOR/RBAC на стенде**: владелец клуба 2 прошёл все 65 операций с параметрами в пути из `api/*.yaml` (кроме суперадмина,
  v1, auth) с id 1, 2, 3, 5 клуба 1 — ни одного 2xx на чужих данных (200 только на свой счёт №3 и пустой чат); все 47 операций
  `superadmin.yaml` — владелец 403, аноним/реселлер 401; кабинет реселлера — владелец 403.
- **Матрица прав**: `RequireClubArea::ROUTES` Laravel (103 маршрута) и `permissions.RouteRights` — совпадают 1:1;
  операции разделов: ни одна изменяющая операция не привязана к праву `.view` (кроме `IssueZReportNonce` — как `zReport()`
  в Laravel). Public-операции (staff, integrations, computers, dashboard, notifications, pclogin, reseller) — проверки в
  обработчиках прочитаны. Приглашение: менеджер не приглашает админа — как в Laravel.
- **Регрессии прежних исправлений** (стенд): A-05 — секреты пути в журнале `***` (`/api/pc-login/***`,
  `/api/guest/qr-login/***`, `/invitations/***`); A-07 — клиент с QR-сессией: `ws-ticket` и `agent-enrollment` — 403;
  A-12 — `text/plain` на REST панели — 415. Рефакторинги 22.11 (legacy, guestapi/enroll, register, config) прочитаны по
  диффам — проверки прав, лимиты и транзакции сохранены.
- **Ручной обзор**: SQL (только sqlc; `fmt.Sprintf` в SQL — только `internal/migrate` и `visual-seed` с константами/
  `pgx.Identifier`), `os/exec` — только `cmd/loadgen`; загрузки фото клуба (сигнатура PNG/JPEG/WebP, размеры, имя случайное,
  отдача с `nosniff` + `CSP default-src 'none'`); SSRF (Kaspi — только https, манифест агента — константа, каталог/прокси —
  фиксированный апстрим); WebSocket — проверка Origin (`OriginPatterns`); Argon2id m=19 МиБ, t=2, p=1; access 10 мин,
  idle 120 мин; WebView2 UI агента (навигация только на виртуальный хост, devtools/контекстное меню/загрузки/разрешения
  выключены, внешние запросы — 403); подпись обновлений Ed25519 и sha256 по открытому файлу; CI — `pull_request` (не `_target`),
  `persist-credentials: false`, действия по SHA, ключ подписи — в окружении `agent-release`; бэкапы — OpenPGP, на сервере БД
  только открытый ключ.

## Что не удалось проверить и почему

- **trivy** (образы): `ghcr.io/aquasecurity/trivy:0.76.0` не скачался — блобы ghcr на `pkg-containers.githubusercontent.com` не
  резолвятся через прокси; база trivy тоже на ghcr. Свободного диска ≈ 2,4–2,8 ГБ (параллельный прогон тестов) — образ
  сервера целиком не собирался. Остаточный риск: CVE пакетов в `postgres:18.6`, `nginx:1.30.5-alpine`, `valkey`, `nats`,
  `seaweedfs`, `grafana` не проверены (в CI `release.yml` trivy сканирует только образ сервера).
- **semgrep p/typescript, p/react**: реестр `semgrep.dev` закрыт прокси; фронт проверен вручную (нет `dangerouslySetInnerHTML`/
  `innerHTML`/`eval`, access-токен только в памяти, динамические `href` — с сервера, https-ссылки Kaspi/загрузок).
- **osv.dev API** (`api.osv.dev`) закрыт прокси (403) — вместо него выгрузки OSV из GCS (Go, NuGet); npm — `npm audit`.
- **Laravel в проде** (SEC-1): настройка `TrustProxies` боевого сервера неизвестна — в репозитории старого проекта её нет.
- **Агент на Windows**: права на named pipe, ACL каталога, DPAPI, поведение установщика — только чтение кода (нет Windows/dotnet).
- **gRPC агента** динамически не проверялся (прошлый аудит делал это своим клиентом; изменения 22.11 прочитаны по диффу).
- **Настройки GitHub** (защита окружений `agent-release`/`release`, правила веток) — вне репозитория.

## Измерения

- gosec: 0 находок / 813 Go-файлов, 24 с; `#nosec` — 135.
- govulncheck (OSV, 21.11.2026): stdlib — 1 вызываемая (GO-2026-6859), модули — 4 невызываемые.
- semgrep: 303 правила × 442 файла, 43 срабатывания, 0 подтверждённых, 42 с.
- npm audit: prod 0; dev 9 moderate (1 корень).
- NuGet: 58 пакетов, 0 уязвимых.
- gitleaks: 133 коммита, 10 срабатываний, 0 настоящих секретов.
- Go 1.25.14 (WAL-G): 15 неисправленных уязвимостей stdlib — GO-2026-6809, 6859, 6599, 6617, 6600, 6610, 6603, 6609, 6612, 6613,
  6611, 6607, 6604, 6608, 6605.
- Стенд: обход IDOR — 65 операций × 4 id = 260 запросов, 0 утечек; суперадмин — 47 операций × 3 роли, 0 допусков;
  пересылка в апстрим без лимита — 25/25 входов и 12/12 запросов кода.
