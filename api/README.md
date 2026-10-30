# api/ — контракт REST по разделам

Каждый раздел админки — **свой файл спецификации, свой сгенерированный пакет, свой пакет обработчиков**.
Агент раздела правит только свои файлы; общие точки — одна строка в реестре и (редко) поле в `httpx.Deps`.

| Что | Где | Кто правит |
|---|---|---|
| Общие схемы, параметры, ответы (`Error`, `ValidationError`, `Unauthenticated`, `Forbidden`, `NotFound`, `ValidationFailed`, `TooManyRequests`, `BadRequest`, `Conflict`, `IdempotencyKey`, `Cursor`, `Limit`, `Search`) | `api/common.yaml` | только по согласованию (затрагивает всех) |
| Спецификация раздела | `api/<раздел>.yaml` | владелец раздела |
| Go: модели + chi + strict-сервер (генерируется, коммитится) | `server/internal/http/api/<раздел>/<раздел>.gen.go`, пакет `<раздел>api` | `task gen:api` |
| Go: общие модели (генерируется) | `server/internal/http/api/common/common.gen.go`, пакет `apicommon` | `task gen:api` |
| Go: обработчики раздела | `server/internal/http/sections/<раздел>/` (пакет `<раздел>`) | владелец раздела |
| Go: общая основа (Deps, права, Tx, ошибки, пагинация) | `server/internal/http/httpx/` | по согласованию |
| Реестр разделов | `server/internal/http/sections.go` — одна строка на раздел | каждый добавляет свою строку |
| TS-типы (генерируется, коммитится) | `web/src/api/gen/<раздел>.d.ts` | `npm run gen:api` |

Разделы сейчас: `system` (healthz/readyz), `auth` (вход, сессия, выбор клуба, суперадмин-просмотр, регистрация,
приглашения, билет WebSocket), `i18n` (переводы), `cash`, `warehouse`, `customers`, `tariffs`, `sessions`
(гостевая сессия, завершение), `clubmap` (состояние карты), `pclogin` (QR-вход на ПК с телефона), `billing` (экран «Подписка»), `public` (лендинг; `/offline`, манифест PWA, спецификация `/docs/api` — не JSON, в том же пакете). `superadmin` (кабинет суперадмина: только `RequireSuperadmin`, только на хосте `superadmin.domain`, если он задан).

## Имя раздела

`[a-z][a-z0-9]*` — это имя Go-пакета: без дефисов и подчёркиваний, не ключевое слово Go (`map` → `clubmap`).
Тег операций в файле — то же имя (`tags: [cash]`).

## Как добавить раздел (пример: `todos`)

1. `api/todos.yaml` — скопируйте шапку любого раздела (`openapi`, `info`, `servers`, `security: [bearerAuth]`,
   `components.securitySchemes`). Общие вещи — только ссылками:
   ```yaml
   "422":
     $ref: "common.yaml#/components/responses/ValidationFailed"
   parameters:
     - $ref: "common.yaml#/components/parameters/Cursor"
   ```
   Свои схемы — в `components.schemas` своего файла. Имена `operationId` уникальны **глобально**
   (`listTodos`, не `list`): по ним строятся имена методов и типов на фронте и в логах.
   Пути разделов не пересекаются (chi паникует при дубле метода и пути).
2. `task gen:api` (или `sh api/gen.sh`) → `server/internal/http/api/todos/todos.gen.go` (пакет `todosapi`).
3. `server/internal/http/sections/todos/section.go` — по образцу `sections/tariffs/section.go`:
   ```go
   var access = httpx.Access{Routes: map[string]string{
       "ListTodos": "todos.index",          // операция → имя маршрута Laravel (RequireClubArea::ROUTES)
   }}
   type handlers struct{ d *httpx.Deps }
   var _ todosapi.StrictServerInterface = (*handlers)(nil)
   func Section() httpx.Section { return httpx.Section{Name: "todos", Mount: mount} }
   func mount(r chi.Router, d *httpx.Deps) {
       si := todosapi.NewStrictHandlerWithOptions(&handlers{d: d},
           httpx.Middlewares[todosapi.StrictMiddlewareFunc](access),
           todosapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
       todosapi.HandlerWithOptions(si, todosapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
   }
   ```
   и `access_test.go` (как в любом разделе: `access.Uncovered(...)` пусто).
4. Одна строка в `server/internal/http/sections.go`: `todos.Section(),`.
5. `npm run gen:api` в `web/` → `web/src/api/gen/todos.d.ts`.

## Права, клуб, режим просмотра

- `httpx.Access.Routes`: операция → **имя маршрута Laravel** (`routes/web.php`), право — по таблице
  `auth/permissions/routes.go` (`RequireClubArea::ROUTES`). Нет маршрута в таблице — паника при старте
  (добавьте маршрут в таблицу прав, сверив со старым `RequireClubArea.php`). Для операции из `Routes` middleware
  проверяет: вход (401 `Unauthenticated.`), выбранный клуб и запрет записи в режиме просмотра суперадмина
  (403 `read_only`), право (403 с текстом старого кода; свой текст — `Access.Messages`).
- `Access.Public` — операции, которые проверяют доступ сами (вход, регистрация, публичные страницы).
- Тест `access_test.go` падает, если операция не попала ни туда, ни туда.
- Клуб — **только** `httpx.ClubID(ctx)` (из сессии), никогда из пути/тела.

## Обработчик

- Данные — `h.d.Tx(ctx, func(ctx, q *store.Queries) error {...})` (тенантная транзакция с RLS, хуки после
  COMMIT). Нетенантные таблицы — `h.d.PlatformPool`.
- Денежные POST — заголовок `Idempotency-Key` (`common.yaml#/components/parameters/IdempotencyKey`) и
  `httpx.Idempotent(...)` внутри транзакции; нет заголовка — 400 `idempotency_key_required` (до прав и тела).
- Ошибки: верните `*httpx.APIError` (`httpx.Validation(field, msg)`, `httpx.RuleError(field, rule)`,
  `httpx.NotFound()`, `httpx.NewAPIError(status, code, msg)`, `httpx.FormErrors` — все ошибки формы сразу) или
  доменную ошибку, которую знает `httpx.MapError`. Свои доменные ошибки — маппер раздела:
  `d.ResponseError(myMapper)`. Тело 422 — `{error: "validation_failed", message, errors: {поле: [текст]}}`,
  тексты — русские строки старого кода (ключи перевода; фронт переводит `t(message)`).
- **Язык текстов ответа (F1-5).** `message` и тексты `errors` ошибок переводятся на язык клуба (`me.club.locale`)
  в `ResponseError` по каталогу переводов (`httpx.Deps.I18n`, таблица `translations`, ключ — русский текст в
  формате Laravel). Текст **без** подстановок — просто ключ (`httpx.Validation(field, "Смена уже открыта.")`).
  Текст **с** подстановками создавайте из ключа и параметров, иначе он не найдётся в каталоге:
  `httpx.ValidationMsg(field, "Недостаточно товара: :product.", map[string]string{"product": name})`,
  `httpx.ErrorMsg(status, code, ключ, params)`, `form.AddMsg(field, ключ, params)` (FormErrors). Доменные ошибки с
  `Message`+`Params` (cash, billing, pricing) `MapError` уже переводит так. Тексты правил валидатора
  (`RuleError`, `RuleMessage`) переводятся как валидатор Laravel: `validation.<правило>` + `validation.attributes.<поле>`.
  Текст успешного ответа — `h.d.T(ctx, ключ, params)` (`__()`; при `params["count"]` и формах через «|» — как
  `trans_choice`). Язык клуба — `h.d.Locale(ctx)` (кэш 30 с). Ошибку возвращайте как `error` (не готовым
  `…422JSONResponse`) — иначе перевода не будет. Фронт по-прежнему делает `t(e.message)`: переведённый сервером
  текст в каталоге не найдётся и покажется как есть.
- Пагинация — keyset: `limit`/`cursor` из common, `httpx.PageSize`, `httpx.EncodeCursor/DecodeCursor`,
  ответ `{items, next_cursor}`.
- Деньги — `int64` тиыны, поля `*_tiyn`. Ввод сумм с > 2 знаков — 422 (Q-002).
- `w`/`r` (cookie, заголовки) — `httpx.HTTPFrom(ctx)`.
- События для браузера — `outbox.Add` в той же транзакции (docs/realtime.md); тип события — `<раздел>.<что>`.

## Лимиты (throttle)

`httpx.Section.Limits`: `"POST /api/путь" → правило ratelimit` (как `throttle:` в `routes/web.php`; правило —
в `auth/ratelimit/rules.go`). Совпадение — точный путь (без параметров).

## Зависимости

Нужен новый сервис (чат, задачи…) — добавьте поле в `httpx.Deps` (одна строка) и заполнение в
`httpserver.NewRouter` из `Options`, а `Options` заполняет `server/internal/app/app.go`. Раздел не импортирует
другие разделы и `httpserver`.

## Проверка

`task gen` → `git diff --exit-code` (CI: шаг «REST code matches api/*.yaml» в `.github/workflows/go.yml` и
«API types are up to date» в `web.yml`). Интеграционные тесты HTTP — `server/internal/http/*_integration_test.go`
(через `NewRouter`, общий стенд `newAdminEnv`) или свои в пакете раздела.
