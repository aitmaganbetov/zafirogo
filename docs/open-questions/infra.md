# Открытые вопросы: каркас, CI, инфраструктура

Автор: субагент (д) «Каркас, CI, инфраструктура», Этап 0.

## 1. Где терминируется TLS и кто ставит HSTS

- [x] **Вопрос.** Сервер zafiro сам слушает HTTPS или стоит за обратным прокси / балансировщиком?
- **Контекст.** `server/internal/http/middleware.go` (`SecurityHeaders`) ставит HSTS только при `r.TLS != nil`.
  За прокси HSTS не выставляется. `RealIP` доверяет `X-Forwarded-For` только от `ZAFIRO_HTTP_TRUSTED_PROXIES`
  (по умолчанию пусто). Для gRPC агентов нужен mTLS — его прокси должен либо пропускать (TCP passthrough),
  либо терминировать с проверкой клиентского сертификата.
- **Варианты.** (а) TLS в процессе (autocert/файлы), (б) TLS на балансировщике + HSTS там,
  (в) gRPC — passthrough на отдельный порт с mTLS в процессе, HTTP — через прокси.
- **Рекомендация.** (в): HTTP за прокси (HSTS на прокси), gRPC-порт отдельно с mTLS в самом zafiro —
  иначе сервер не видит клиентский сертификат ПК.
- Решение (30.10.2026): см. [Q-071](../OPEN_QUESTIONS.md#q-071) — принята рекомендация: HTTP за прокси (HSTS на прокси), gRPC — отдельный порт с mTLS в процессе (TCP passthrough).

## 2. Целевая платформа развёртывания и Dockerfile

- [x] **Вопрос.** Куда деплоим (VPS + systemd, Docker/Compose, Kubernetes)? Нужен ли Dockerfile уже на Этапе 0–1?
- **Контекст.** Сейчас есть только `task build` → `bin/zafiro` (статический, `CGO_ENABLED=0`).
  Dependabot для `docker` не настроен, т.к. Dockerfile нет (есть `docker-compose` для dev).
- **Рекомендация.** Distroless/static образ с одним бинарником; добавить на Этапе 1 вместе с CD.
- Решение (30.10.2026): см. [Q-072](../OPEN_QUESTIONS.md#q-072) — принята рекомендация: distroless/static образ с одним бинарником, вместе с CD.

## 3. Версия Node.js

- [x] **Вопрос.** Фиксируем Node 22 LTS (как в задаче) или переходим на 24 LTS?
- **Контекст.** jsdom 30.1.3 (через Vitest) требует Node ≥ 22.22.2; в среде разработки стоит 22.22.0 —
  работает с предупреждением `EBADENGINE`. CI использует `setup-node` с `node-version: 22` (последний 22.x).
- **Рекомендация.** Оставить 22 в CI, локально обновиться до ≥ 22.22.2; переход на 24 — отдельным PR.
- Решение (30.10.2026): см. [Q-073](../OPEN_QUESTIONS.md#q-073) — принята рекомендация: Node 22 в CI, локально ≥ 22.22.2; 24 — отдельным PR.

## 4. TypeScript 6 vs 7

- [x] **Вопрос.** Переходить ли на TypeScript 7 (нативный компилятор)?
- **Контекст.** Последний `typescript` в npm — 7.0.2, но `typescript-eslint` 8.72.1 поддерживает `<6.1.0`,
  а `openapi-typescript` 7.13.0 объявляет peer `typescript ^5.x`. Закреплён `typescript` 6.0.3;
  для openapi-typescript в `web/package.json` добавлен `overrides` (`"typescript": "$typescript"`),
  генерация работает.
- **Рекомендация.** Остаться на 6.0.x, пока typescript-eslint не поддержит 7.
- Решение (30.10.2026): см. [Q-074](../OPEN_QUESTIONS.md#q-074) — принята рекомендация: TypeScript 6.0.3 до поддержки 7 в `typescript-eslint` и `openapi-typescript`.

## 5. Версия @playwright/test

- [x] **Вопрос.** Закреплять последнюю (1.64.1) или версию, совпадающую с браузерами в среде агентов?
- **Контекст.** В среде стоит Playwright 1.63.0 с `chromium-1243` (`/opt/pw-browsers`); `playwright install`
  запускать нельзя. Закреплён `@playwright/test` 1.63.0 — e2e проходит без `executablePath`.
  В CI (`web.yml`) браузер ставится `npx playwright install --with-deps chromium` той же версии.
- **Рекомендация.** Оставить 1.63.0 до обновления образа среды; затем поднять через Dependabot.
- Решение (30.10.2026): см. [Q-075](../OPEN_QUESTIONS.md#q-075) — принята рекомендация: `@playwright/test` 1.63.0 до обновления образа.

## 6. Docker Hub rate limit

- [x] **Вопрос.** Нужен ли собственный registry-mirror / логин в Docker Hub для CI и dev?
- **Контекст.** Локально `docker pull postgres:18.6-alpine3.24` получил 429 Too Many Requests;
  образы взяты из `mirror.gcr.io`. В GitHub Actions лимиты мягче, но при росте числа джобов возможны сбои.
- **Рекомендация.** Пока ничего; при сбоях — `docker/login-action` с read-only токеном Docker Hub.
- Решение (30.10.2026): см. [Q-076](../OPEN_QUESTIONS.md#q-076) — принята рекомендация: зеркало не заводим; при сбоях — `docker/login-action` с read-only токеном.

## 7. Перенос данных: где живёт инструмент

- [x] **Вопрос.** Инструмент переноса — подкоманда в `server/cmd/migrate-from-mysql` (сейчас) или отдельный модуль
  в `tools/migrate-from-mysql`?
- **Контекст.** Заготовка лежит в `server/cmd/migrate-from-mysql/main.go`, чтобы переиспользовать `internal/store`
  и доменные пакеты (internal недоступен из другого модуля). `tools/migrate-from-mysql/README.md` — описание.
  Драйвер MySQL (`github.com/go-sql-driver/mysql`) попадёт в зависимости `server`, но не в бинарник `zafiro`.
- **Рекомендация.** Оставить в `server/cmd`, бинарник собирать отдельно.
- Решение (30.10.2026): см. [Q-077](../OPEN_QUESTIONS.md#q-077) — принята рекомендация: инструмент — `server/cmd/migrate-from-mysql` (отдельный бинарник); ссылки на `tools/migrate-from-mysql` в `CLAUDE.md` поправить (правка `CLAUDE.md` — за владельцем).

## 8. Ограничения размера тела запроса и rate limiting

- [x] **Вопрос.** Какие лимиты тела запроса и частоты ставить по умолчанию?
- **Контекст.** Сейчас ограничены только заголовки (`MaxHeaderBytes` 1 MiB) и время (`ReadTimeout` 30s,
  `RequestTimeout` 30s). В Laravel лимиты частоты заданы через `throttle:` на маршрутах — их переносит агент API.
- **Рекомендация.** Глобально `http.MaxBytesReader` 1 MiB для JSON (больше — точечно для загрузок),
  rate limit на Valkey по маршрутам из Laravel — на Этапе 1.
- Решение (30.10.2026): см. [Q-055](../OPEN_QUESTIONS.md#q-055) — принята рекомендация: `http.MaxBytesReader` 1 MiB для JSON (больше — точечно для загрузок), rate limit на Valkey по маршрутам Laravel.

## 9. Ветки CI

- [x] **Вопрос.** Запускать CI на push в `phase-*` ветки или только на PR и `main`?
- **Контекст.** `go.yml`/`web.yml`: `push` в `main` + все `pull_request`.
- **Рекомендация.** Так и оставить: работа идёт через PR.
- Решение (30.10.2026): см. [Q-078](../OPEN_QUESTIONS.md#q-078) — решено фактом: CI запускается на PR и push в `main` (`.github/workflows/go.yml`, `web.yml`).

## 10. Service worker из Zafiro (`web/public/sw.js`)

- [x] **Вопрос.** Сохраняем PWA/офлайн-режим и как адаптируем service worker?
- **Контекст.** `web/public/sw.js` скопирован из `Zafiro/public/sw.js` без изменений, но **не регистрируется**
  (`web/src/main.tsx`): он ссылается на Laravel-пути — `OFFLINE_URL = '/offline'` (маршрут Laravel, в SPA
  ответит index.html), кэш собранных ассетов по `/build/*` (у Vite — `/assets/*`). Манифест в старом проекте
  отдаёт маршрут `pwa.manifest` (`routes/web.php`, `PwaController::manifest`) — не перенесён, в `index.html`
  нет `<link rel="manifest">`. Цвет темы `#101713` взят из README агента экранов (`PwaController::THEME_COLOR`).
- **Рекомендация.** Этап 1: статический `manifest.webmanifest` в `web/public`, в `sw.js` заменить `/build/` на
  `/assets/`, офлайн-страницу сделать статическим `offline.html`, затем регистрировать SW в `main.tsx`.
- Решение (30.10.2026): см. [Q-068](../OPEN_QUESTIONS.md#q-068) — по DECISIONS Q-068 — PWA сохраняется (манифест, service worker с путями SPA, офлайн-страница).

## 11. Внешние CDN (Google Fonts, cdnjs) и CSP

- [x] **Вопрос.** Оставляем Inter с Google Fonts и Font Awesome 6.4.0 с cdnjs (как в старом) или хостим сами?
- **Контекст.** Подключены в `web/index.html` как в `layouts/admin.blade.php` и `partials/fonts.blade.php`.
  В старом проекте `integrity` у Font Awesome не было, поэтому SRI не добавлен (cdnjs из этой среды
  недоступен — посчитать хеш не удалось). CSP (`server/internal/http/middleware.go`) разрешает
  `fonts.googleapis.com`, `fonts.gstatic.com`, `cdnjs.cloudflare.com`; inline-скрипт темы разрешён по sha256
  (тест `TestCSPCoversInlineScripts` сверяет хеш с `web/index.html`).
- **Рекомендация.** Самостоятельный хостинг через npm-пакеты с точной версией (`@fontsource/inter`,
  `@fortawesome/fontawesome-free@6.4.0`) — без внешних запросов и с `font-src 'self'`; либо хотя бы SRI.
- Решение (30.10.2026): см. [Q-064](../OPEN_QUESTIONS.md#q-064) — по DECISIONS Q-064 — шрифты и библиотеки из npm в сборке, версии закреплены, без внешних CDN.

## 12. govulncheck локально

- [x] **Вопрос.** Нужен ли доступ к `vuln.go.dev` из сред агентов?
- **Контекст.** Прокси среды отвечает 403 на `https://vuln.go.dev`, поэтому `govulncheck ./...` локально
  не выполнен; в CI (`go.yml`) он запускается. Также в образе среды есть `/usr/local/bin/golangci-lint` 2.5.0
  (собран Go 1.25), который в `PATH` стоит раньше `$(go env GOPATH)/bin` и не может проверять код Go 1.27 —
  нужно использовать закреплённый v2.15.0 из `task tools`.
- **Рекомендация.** Разрешить `vuln.go.dev` в политике прокси; обновить golangci-lint в образе среды.
- Решение (30.10.2026): см. [Q-079](../OPEN_QUESTIONS.md#q-079) — принята рекомендация: открыть `vuln.go.dev` в прокси и обновить `golangci-lint` в образе; до тех пор — закреплённый v2.15.0 из `task tools`, `govulncheck` — в CI.
