# Перенос темы Д: реселлеры (ноябрь 2026)

Исполнитель П5 (`docs/upstream-2026-11.md`, тема Д, §3.4, §8, §9; правила BR-185…BR-189; сценарии GS-560…GS-565;
GAP-052, GAP-053, GAP-054; решения UQ-3, UQ-10, UQ-21). Источник — старый Laravel `a680bab` (коммиты `ca5c8f0`,
`2318bc7`, `9c2df5f`; итоговое состояние — реселлеров создаёт суперадмин, хост реселлера только впускает реселлеров):
`app/Models/{Reseller,ResellerPayout}.php`, `Http/Controllers/{ResellerController,Superadmin/ResellerController}.php`,
`SettingsController::reseller()`, `AuthController::login()`, `Middleware/RestrictResellerHost.php`,
`Support/ResellerHost.php`, `reseller/dashboard.blade.php`, `layouts/reseller.blade.php`,
`superadmin/resellers/index.blade.php`, `settings/club.blade.php` (`#reseller`), `tests/Feature/ResellerTest.php`.

## 1. Что перенесено

| Старое | Новое |
|---|---|
| `Reseller::normalizeCode()`, `CODE_PATTERN`; разбор телефона формы; `earned − paid_out` | `server/internal/reseller/reseller.go` (`NormalizeCode`, `ValidCode`, `NormalizePhone`, `Balance`, `FormatPercent`) |
| `Superadmin\ResellerController::store()`, `update()`, `payout()`, `detach()`, `destroy()`; `SettingsController::reseller()`; `ResellerController::dashboard()` | `server/internal/reseller/service.go` (`Create`, `Update`, `Payout`, `Detach`, `Delete`, `Attach`, `Dashboard`); запросы `db/queries/resellers.sql` (`Reseller*`, пул `zafiro_platform`) |
| `GET superadmin/resellers?q=` | `GET /api/superadmin/resellers?q=&page=` (`saListResellers`) |
| `POST superadmin/resellers` | `POST /api/superadmin/resellers` (`saCreateReseller`) |
| `PATCH superadmin/resellers/{reseller}` | `PATCH /api/superadmin/resellers/{resellerId}` (`saUpdateReseller`) |
| `POST superadmin/resellers/{reseller}/payouts` | `POST /api/superadmin/resellers/{resellerId}/payouts` (`saPayoutReseller`, `Idempotency-Key`, код TOTP) |
| `DELETE superadmin/resellers/{reseller}` | `DELETE /api/superadmin/resellers/{resellerId}` (`saDeleteReseller`, код TOTP) |
| `DELETE superadmin/resellers/{reseller}/clubs/{club}` | `DELETE /api/superadmin/resellers/{resellerId}/clubs/{clubId}` (`saDetachResellerClub`) |
| `POST /settings/reseller` (`settings.update`, `throttle:10,1`) | `POST /api/settings/reseller` (`attachClubReseller`; правило лимита `settings.reseller`); данные карточки — `GET /api/settings/reseller` (`getClubReseller`, право `settings.view`) |
| `GET /` на хосте реселлера (или `/reseller`) | `GET /api/reseller/dashboard?clubs_page=&earnings_page=` (`api/reseller.yaml`, раздел `sections/reseller`), страница `/reseller` (на хосте — с корня) |
| `RESELLER_DOMAIN` (`config/app.php`), `ResellerHost`, `RestrictResellerHost` | `reseller.domain` (`ZAFIRO_RESELLER_DOMAIN`), `http/reseller_host.go`, `webui.WithResellerDomain` (`<html data-reseller-host>`), `web/src/features/reseller/host.ts`, корневой гард `routes/__root.tsx` |
| `AuthController::login()` на хосте реселлера | `auth.LoginInput.IsReseller` (`internal/auth/service.go`), `sections/auth/auth.go` |
| `superadmin/resellers/index.blade.php`, пункт меню | `web/src/features/superadmin/ResellersPage.tsx`, `SuperadminLayout.tsx`, `host.ts` (`/resellers`) |
| `reseller/dashboard.blade.php`, `layouts/reseller.blade.php`, `partials/reseller-sidebar.blade.php` | `web/src/features/reseller/{DashboardPage,ResellerLayout}.tsx`; общий с кабинетом суперадмина каркас — `features/superadmin/CabinetLayout.tsx` |
| `settings/club.blade.php` `#reseller` | `web/src/features/settings/ResellerCard.tsx` (+ якорь «Реселлер» в `ClubSettingsPage.tsx`) |
| `auth/login.blade.php` на хосте реселлера | `features/auth/LoginPage.tsx` («Кабинет реселлера Zafiro. Аккаунт реселлера создаёт Zafiro.») |

Скидка реселлера в счёте и начисление при оплате сделаны в теме Б (`internal/subscription`, `upstream-billing.md` §4) —
здесь не дублируются; сквозной путь проверяет `TestResellers/GS-563…`.

События: журнал клуба — `club.reseller_attached`; журнал платформы — `superadmin.reseller_created`, `…_updated` (детали —
изменённые поля строками), `…_payout` (`amount` — число тенге), `…_detached`, `…_deleted` (`code`, `user_id`, `clubs`);
`audit_log` — `permission / reseller.created|deleted`, `tariff / reseller.updated|detached`, `money / reseller.payout`,
`settings / reseller.attached` (клуб). Журнал денег платформы — `reseller_payout:<id выплаты>` в транзакции выплаты.

Визуальное сравнение (`tools/visual`, старый `a680bab`): `superadmin-resellers`, `reseller-dashboard`, `reseller-login`,
`settings-club` (карточка `#reseller` больше не скрыта), `superadmin-invoices` (временный порог 2 % снят) и остальные
страницы кабинета — совпадают (0,00–0,17 %). Демо-данные: реселлер `arman-games` (аккаунт 15, клуб «Zafiro Lite»,
начисление 2 656,25 ₸, выплата 1 500 ₸) и остановленный `alia-partner`.

## 2. Отличия от старого (осознанные)

| № | Что | Старое | Здесь | Почему |
|---|---|---|---|---|
| UR-1 | Вход на хосте реселлера | пароль | пароль; **суперадмин-реселлер — ещё и второй фактор** | второго фактора в старом нет вообще (Этап 7 — наш). Обычному реселлеру он не нужен — как в старом. Но сессия суперадмина несёт права суперадмина на любом хосте: вход без кода на хосте реселлера был бы обходом Этапа 7 |
| UR-2 | Выплата и удаление реселлера | сразу (удаление — `confirm()`) | окно со **свежим кодом TOTP** действующего суперадмина | по образцу выдачи прав суперадмина (аудит A-10): оба действия необратимы — выплата не исправляется и не удаляется, удаление стирает документы выплат. Создание, условия и отвязка клуба — без кода (обратимы, как тариф и скидка клуба) |
| UR-3 | Режим просмотра клуба | `superadmin.*` разрешён | раздел «Реселлеры» только читается (403 `read_only` на запись); код реселлера в настройках — тоже 403 | поручение темы; режим просмотра — «только смотреть» (как сброс 2FA и выдача прав) |
| UR-4 | Двойной клик «Записать выплату» | две выплаты | `Idempotency-Key` формы: повтор — тот же ответ, другая сумма с тем же ключом — 409 | §3.6 |
| UR-5 | Арифметика и ввод | float; MySQL округляет сумму выплаты с лишними знаками | целые тиыны и базисные пункты; в API — `amount_tiyn`, `commission_bp`; больше двух знаков форма не отправляет | UQ-7, Q-002 |
| UR-6 | Журнал денег, аудит | нет | `platform_money_log`, `audit_log` | UQ-6, CLAUDE.md «Безопасность» |
| UR-7 | Привязка клуба при гонке двух вводов | проверка без блокировки | блокировка строки клуба + `UPDATE … WHERE reseller_id IS NULL`; второй получает «Клуб уже привязан…» | §3.6 |
| UR-8 | Сессия реселлера на его хосте | сессия аккаунта (клуб выбирает middleware панели, которой на хосте нет) | сессия без клуба, даже если реселлер ещё и сотрудник клуба | на этом хосте панели клуба нет; REST панели здесь отвечает 403 любому |
| UR-9 | Запись входа реселлера в журнал | нет (неудача `not_reseller` — есть) | так же | принцип 2 |
| UR-10 | Поиск реселлеров | `LIKE` (регистр — по сравнению MySQL) | `ILIKE` с экранированием `%`, `_`, `\` | то же поведение на Postgres |
| UR-11 | Карточка «Реселлер» | часть HTML страницы | свой запрос `GET /api/settings/reseller`; процент реселлера клубу не отдаётся | карточка — отдельный компонент; клуб не должен знать условия реселлера |
| UR-12 | Каркасы кабинетов суперадмина и реселлера | два почти одинаковых Blade | один `CabinetLayout` с меню и подписями в параметрах | без третьей копии поведения меню; разметка совпадает попиксельно |

Не изменено намеренно: превышение остатка при выплате не проверяется («к выплате» может быть отрицательным, GAP-053);
удаление реселлера стирает выплаты (UQ-10 — суммы остаются в `platform_money_log`); массовая отвязка клубов при
удалении не меняет `clubs.updated_at`; пароль в форме «Новый реселлер» — обычное текстовое поле; даты кабинетов — по UTC
(кабинеты вне клуба); без `reseller.domain` реселлер без клубов войти не может (как старый проект без `RESELLER_DOMAIN`).

## 3. Вопросы владельцу

### UR-Q1. Когда открывать кабинет реселлера на новом сервере (UQ-3)

Реселлеры, счета и выплаты — данные платформы; при поклубном переходе клубы реселлера живут на двух серверах, и кабинет
нового сервера показал бы только переехавшие клубы и их начисления.

- **Сделано:** код готов; `reseller.domain` по умолчанию пуст (в `deploy/staging` — `resseler.staging.zafiro.test`).
  Перенос данных реселлеров уже есть (Ш0) и проверен: кабинет на перенесённых данных показывает те же суммы
  (`migrate` `TestMigrateUpstream2026_11`), проверка 8 учений восстановления проходит.
- **Рекомендация (принята в UQ-3):** в проде не задавать `reseller.domain` и не направлять `resseler.zafiro.kz` на
  zafirogo до переноса последнего клуба; выплаты до тех пор записывать на старом сервере. Нужна дата/условие включения.
- Раздел «Реселлеры» кабинета суперадмина нового сервера до этого момента лучше не использовать для выплат: выплата,
  записанная здесь, не попадёт в старую базу (и наоборот — повторный перенос платформенного шага её не потеряет, но
  суммы «к выплате» на двух серверах разойдутся).

### UR-Q2. Код TOTP на выплату — оставить?

Запись выплаты подтверждается кодом (UR-2). Если выплат много и код мешает — снять его для выплаты можно одной правкой
(`sections/superadmin/resellers.go` `SaPayoutReseller`, окно в `ResellersPage.tsx`); ключ идемпотентности останется.
Для удаления реселлера код рекомендую оставить.

### UR-Q3. Хост реселлера в проверке сертификатов (А8)

В старом хост реселлера входит в список проверки TLS **по умолчанию** (`Monitoring/Certificates.php:15`: адрес сайта,
домены суперадмина, wiki и реселлера). В zafirogo умолчания нет вовсе — список берётся только из
`superadmin.ssl_hosts` (и домен суперадмина в него сам не попадает). Сейчас: в `docs/deploy.md` записано добавить
`resseler.zafiro.kz` в `superadmin.ssl_hosts`. Сделать умолчание как в старом (домены из конфигурации) — отдельная
небольшая правка экрана «Сервер» (пункт А8 чек-листа остаётся открытым).

### UR-Q4. Подписи событий реселлеров в журналах (GAP-052)

`club.reseller_attached` и `superadmin.reseller_*` не имеют подписей в `ActivityLog::EVENTS` / `PlatformLog::EVENT_LABELS`
старого кода: в фильтре событий их нет, в строке журнала — описание. Перенесено так же (как UB-4 темы Б). Подписи —
новые переводы на три языка; добавить по решению.

### UR-Q5. Реселлер, который ещё и сотрудник клуба

На хосте реселлера он получает сессию без клуба (UR-8), на хосте панели — обычную сессию клуба (кабинет реселлера там
не отвечает, если задан `reseller.domain`). Это две разные сессии на двух хостах, как в старом (cookie сессии — на хост).
Вопросов нет, если так и задумано.

## 4. Правки общих файлов (минимальные, добавлением)

- `sqlc.yaml` — исключение `Reseller*` из правила tenant-filter (с комментарием).
- `server/internal/http/sections.go` — строка `reseller.Section()`; `server.go`, `httpx/deps.go` — `ResellerDomain`,
  middleware `resellerHost`; `readonly.go` (+ `readonly_walk_test.go`) — запись в «Реселлеры» закрыта в режиме просмотра;
  `subscription.go` — `/api/reseller` вне проверки подписки.
- `internal/auth/service.go` — `LoginInput.IsReseller`, `startResellerSession`, причина `not_reseller`;
  `sections/auth/auth.go` — вход на хосте реселлера.
- `auth/permissions/routes.go` (+ тест) — `settings.reseller` → `settings.update`; `auth/ratelimit/rules.go` (+ тест) —
  `settings.reseller` 10/мин; `sections/settings/section.go` — две операции и лимит.
- `sections/superadmin/section.go` — шесть операций в списке; `internal/config/config.go` — `reseller.domain`;
  `internal/app/app.go` — проброс домена; `internal/webui/webui.go` — `WithResellerDomain`.
- `api/settings.yaml`, `api/superadmin.yaml` — свои операции и схемы; `api/README.md` — раздел `reseller`.
- `web`: `router.tsx` (rewrite хоста), `routes/__root.tsx` (гард хоста), `features/auth/LoginPage.tsx`,
  `features/public/guard.ts` (комментарий), `features/superadmin/{SuperadminLayout,host,api}.ts(x)`,
  `features/settings/ClubSettingsPage.tsx` (импорт, якорь, строка карточки), `features/settings/ProfilePage.test.tsx`
  (порядок якорей: добавился `#reseller`).
- `tools/visual`: `pages.json` (3 новые страницы; `settings-club` — убран пустой `hide`; `superadmin-invoices` — снят
  порог), `fixtures/demo.json` (пользователь 15, счёт 4, реселлеры), `old/seed.php`, `old.sh`, `new.sh`;
  `server/cmd/visual-seed` (реселлеры и строки журнала денег).
- `deploy/staging` (`server_name`, `ZAFIRO_RESELLER_DOMAIN`, проверка в `nginx-test.sh`), `.env.example`,
  `docs/deploy.md`, `docs/dev-setup.md`, `deploy/README.md`.
- `server/internal/migrate/upstream_integration_test.go` — вызов проверок кабинета реселлера и проверки 8 `verify.sql`
  (сами проверки — в новом `reseller_cabinet_integration_test.go`).

## 5. Остаток

- Окно кода второго фактора: `SecondFactorDialog.tsx` — та же разметка, что встроенное окно экрана «Пользователи»
  (`AccountsPage.tsx`); экран «Пользователи» на общий компонент не переведён (чужой экран) — небольшая чистка.
- А8 (умолчание списка проверки сертификатов) — UR-Q3.
- Flutter и старый REST мобильных: маршрутов реселлеров в `/api` старого проекта нет — переносить нечего.
- Пункт чек-листа «Карточки „Бронь из приложения“ и „Реселлер“ на „Настройка клуба“» (`docs/parts/70-upstream-2026-11.md`,
  общий для тем Г и Д): карточка «Реселлер» сделана; отметку ставит оркестратор после темы Г.
