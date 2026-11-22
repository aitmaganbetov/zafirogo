# Реестр аудита: UI/UX и скорость фронтенда (область UI), 22.11.2026

Ветка `audit/full-system-2026-11` (HEAD `e4e1156`), только чтение. Рабочая папка: `audit/ui/` (скрипты, сырые JSONL — `audit/ui/results/`,
снимки — `audit/ui/shots/`, `audit/ui/agent/shots/`, Lighthouse — `audit/ui/lh/*.json`). Пути ниже — от
`/tmp/claude-0/-home-user-zafirogo/541f6760-cdd0-504b-ac95-cb07a87b9341/scratchpad/`.

Прошлые аудиты (UI-01…UI-31, FE-01…FE-23, AU-1…AU-16 в `docs/open-questions/audit-ui.md`) не повторяются. Регрессий исправленного
не найдено (см. «Что проверено»). «Наследие» = та же разметка/классы в Blade/WPF старого проекта (дефект виден и там).

| ID | Приоритет | Категория | Файл:строка | Проблема | Сценарий/доказательство | Предлагаемое исправление | Меняет бизнес-логику? | Статус |
|---|---|---|---|---|---|---|---|---|
| UI-1 | Medium | Неработающее состояние | `web/src/pages/ErrorPage.tsx:81-87` (`RouteErrorPage`), загрузчики `web/src/routes/_admin/activity-logs/index.tsx:12`, `web/src/routes/_admin/reservations/index.tsx:11` | Ответ API 422/400 в `loader` маршрута показывается как «500 Server Error» внутри панели; выхода, кроме меню, нет. В Laravel тот же GET с невалидным фильтром (`FilterActivityLogsRequest`: `event` Rule::in, `user_id` Rule::in(членства), `from/to` date, `search` max:120) — redirect back с ошибкой валидации, не 500. | `audit/ui/badparams.mjs` (владелец, 1440): `/activity-logs?user_id=999999` (сотрудник, удалённый из клуба, — сохранённая ссылка/история браузера), `?event=no.such.event`, `?from=abc`, `?cursor=garbage` (400), `/reservations?date=bad` → в `main` «Server Error … 500 Server Error», в сети 422/400. Остальные экраны с параметрами (`/users`, `/tariffs?zone=`, `/shop-orders?status=`, `/club-map?computer=`) неверные значения игнорируют — работают. | В `RouteErrorPage` для `status 422/400` не «500», а та же страница без параметров поиска (`navigate({search: {}})`) + флеш с `firstError(error)`; либо в загрузчиках журнала и броней ловить 422 и повторять запрос без фильтров. Вид экранов не меняется. | нет | CONFIRMED |
| UI-2 | Medium | Наложение текста (телефон, все языки) | `web/src/features/settings/ShellThemeField.tsx:39-46` (Blade: `settings/club.blade.php:91-101` — наследие) | «Тема оформления на ПК»: подпись темы не переносится внутри карточки `grid-cols-2` — текст выходит за рамку и ложится на соседнюю карточку («Стандартная (фиолетовая)» на «Синяя», «Оранжевая», «Бирюзовая» за границей). kk: «Стандартты (күлгін)», «Көгілдір-жасыл». | Обход 375/360/320 (`results/ru.jsonl`, `narrow.jsonl`, `kk.jsonl`): `text-spill-x` sw 147 > cw 116 (375), 147 > 108 (320); снимок `audit/ui/shots/shell-theme_ru_m375.png`. На 768/1440 (4 колонки) — нет. | Текст подписи в `<span className="min-w-0 [overflow-wrap:anywhere]">` (или `break-words`), либо `grid-cols-1 min-[420px]:grid-cols-2`. Меняет пиксели только на телефоне — отличие от Blade записать как AU-* и `-mobile` в `tools/visual/pages.json`. | нет | CONFIRMED |
| UI-3 | Medium | Адаптивность / длинные kk-тексты | `web/src/features/superadmin/InvoicesPage.tsx:96-97` (Blade `superadmin/invoices/index.blade.php:14-15` — наследие) | Карточка «Ожидают оплаты» кабинета суперадмина: `flex flex-wrap` + колонка `min-w-0 flex-1` (основа 0) — на kk при 375 px цена и кнопки «Төлем алынды»/«Бас тарту» занимают строку, колонка счёта сжимается до 7 px: номер, клуб, тариф печатаются по одной букве в строке, карточка 1 536 px высотой. ru/en на 375 — 179 px (норма). | `node probe.mjs m375 superadmin /invoices … --locale kk --host admin.localhost` → `{"w":7,"h":1536}`; ru/en → 179×84. Снимок `audit/ui/shots/sa-invoices_kk_m375.png`. Остальные `min-w-0 flex-1` в проекте имеют `basis-*` — проблема только здесь. | `min-w-0 flex-1 basis-64` (как `ComputersPage.tsx:42`, `TelegramPage.tsx:85`): при нехватке места блоки переносятся. На 1440 пиксели те же. | нет | CONFIRMED |
| UI-4 | Medium | Доступность: клавиатура (WCAG 2.1.1, 2.4.3) | `web/src/features/clubmap/ClubMapPage.tsx:82-87` (`PcTile` — `<span>`), `:481` и `web/src/styles/app.css:2269-2295` (`.cm-control-panel`) — наследие `club-map/index.blade.php:60,161` | 1) Плитку ПК нельзя выбрать с клавиатуры (`span` без `tabIndex`/роли, выбор — только `onClick`): без мыши недоступны все действия панели (вкл./выкл., бронь, техрежим, завершить сессию, подключить тариф). 2) Скрытая панель управления (`opacity:0; pointer-events:none`) остаётся в порядке Tab: 9 невидимых остановок фокуса — фокус «пропадает». | `audit/ui/focus.mjs` (`results/focus.jsonl`, `/club-map`, 70 × Tab): плитки `.cm-computer` в последовательности отсутствуют; `invisible` = `button.cm-control-button` «Включить», «Перезагрузить», «Выключить», `#reserve-selected`, `#notify-open`, `#end-sessions`, `#guest-session-open`, … | Плитке — `role="button" tabIndex={0} aria-pressed={selected}` и Enter/Space → тот же обработчик; панели — `inert` (или `visibility:hidden` с задержкой перехода), пока `selected.size === 0`. Пиксели прежние (кроме кольца фокуса). | нет | CONFIRMED |
| UI-5 | Medium | Скорость: публичный сайт (LCP) | `web/src/features/public/LandingPage.tsx:70`, `web/src/features/public/parts.tsx:106`, `server/internal/webui/site.go:166-180` (`siteHead`), раздача `/images/*` | Лендинг на мобильном профиле: Performance 83, **LCP 3,8 с** (> 2,5 с). LCP — `/images/club-map.webp`; 55 % LCP (2,07 с) — «load delay»: SPA находит картинку только после `main.js` (85 КБ gzip) и пакета переводов. `/images/landing/club.jpg` 112 КБ отдаётся в блок, которому хватает ~19 КБ (Lighthouse: −93 КБ размер, −45 КБ формат). `/images/*` — `max-age=3600` (у `/assets/*` — immutable). То же на `/pricing` (LCP 3,8 с) и `/product/*`. | Lighthouse 12.8.2 mobile: `audit/ui/lh/landing-mobile.json`, `pricing-mobile.json` (`largest-contentful-paint-element`: TTFB 450, Load Delay 2 069, Render Delay 1 128 мс). | В `siteHead` для «/» — `<link rel="preload" as="image" href="/images/club-map.webp" fetchpriority="high">` (сервер уже подставляет `<head>` шести страниц сайта); `fetchpriority="high"` на `<img>`; `club.jpg` → webp + `srcset`/`sizes`; долгий кэш `/images/*` с версией в имени. Вид не меняется. | нет | CONFIRMED |
| UI-6 | Low | Доступность: имена полей (WCAG 1.3.1, 4.1.2) | `web/src/features/settings/ClubSettingsPage.tsx:214,241`; `web/src/features/settings/ProfilePage.tsx:141`; `web/src/features/tariffs/TariffsPage.tsx:440,491,492`; `web/src/components/Pagination.tsx:88,120` — наследие Blade | axe (critical): у полей файла «Логотип», «Картинка входа», «Фото клуба» (`photos[]`), у `select[name=duration_unit]`, `usage_limit_value`, `select[name=usage_limit_unit]` нет доступного имени (подпись — соседний текст, не `<label>`); `aria-label` на `<span>` без роли в пагинации (`aria-prohibited-attr`, `/languages/2/translations`). Прошлый FE-16 исправил подписи минут (6 → 2 узла на `/settings/club`), файлы остались. | axe-core 4.10.2 в обходе (`results/ru.jsonl`, `dark.jsonl`): `label` — settings-club ×2, settings-profile ×1, tariffs-edit ×2; `select-name` — tariffs-edit ×2. | `aria-labelledby` на id заголовка карточки/подписи или `aria-label={t(…)}`; в пагинации — `role="link" aria-disabled` или `title`. Пиксели прежние. | нет | CONFIRMED |
| UI-7 | Low | Наложение окон (телефон) | `web/src/layouts/PwaBanner.tsx:86`, `web/src/styles/app.css` `.pwa-banner` (z `--z-toast`) / `.cm-control-panel` (z `--z-dropdown`) — наследие `app.css` старого | На iPhone/Samsung (подсказка установки) баннер `.pwa-banner` (z 500, низ экрана, 167 px) закрывает панель управления выбранными ПК на карте клуба (z 200): центр панели под баннером, кнопки «Выключить», «Бронь» и ниже недоступны, пока не нажать «Не сейчас». | `audit/ui/pwa.mjs` (UA iPhone, 375×812): `banner 629–796, panel 502–802, topAtPanel: pwa-banner`; снимок `audit/ui/shots/pwa-ios_club-map_m375.png`. | Не показывать баннер, пока открыта `.cm-control-panel.show` (или поднимать панель над баннером: `bottom: calc(баннер + 16px)`). | нет | CONFIRMED |
| UI-8 | Low | Локализация kk: даты и числа | `web/src/layouts/ClubClock.tsx:57-59`; `web/src/features/clubmap/ComputerHealth.tsx:230-235`, `ComputerInfoPanel.tsx:27`; агент `agent/src/Zafiro.Agent.UI/Frontend/core.js:42-58` (`formatAmount`), `main.js:216` — наследие (`resources/js/app.js:138` старого, старый агент — `toLocaleString`) | В Chromium нет данных CLDR для `kk`: `Intl` отдаёт корневой формат. Шапка панели на kk: «M10 15, Thu»; агент на kk: «2026 M11 4 12:00 дейін», баланс «123,456.78 KZT» (запятая — разделитель тысяч, точка — дробной части; ru/kk-норма «123 456,78»). | `audit/ui/icu.mjs`: Chromium 153 (headless и полный) `kk-KZ` → `2026 M11 4 12:00`, `supportedLocalesOf` при этом «поддерживает» kk; Node (full ICU) — «2026 ж. 04 қар.». Снимки `audit/ui/shots/kk-clock_d1440.png`, `audit/ui/agent/shots/launcher-historyNavigation-kk-1366x768.png`. На реальных Chrome/Edge/WebView2 (Windows) не проверено — у них тот же отфильтрованный ICU Chromium. | Для `kk` не полагаться на `Intl`: числа — форматом `ru` (те же разделители), месяцы/дни недели — из каталога переводов (как делает сервер), или `Intl` с проверкой `formatToParts` и откатом на `ru`. | нет | CONFIRMED (Chromium 153); PLAUSIBLE (WebView2/Chrome на Windows) |
| UI-9 | Low | Длинные kk-тексты в шапке кабинетов | `web/src/features/superadmin/CabinetLayout.tsx:169` — наследие `layouts/reseller.blade.php:21`, `layouts/superadmin.blade.php:21` | Плашка роли («Реселлер»/«Суперадмин») без `whitespace-nowrap shrink-0`: на kk при 375 px длинный заголовок («Менің клубтарым және табысым») сжимает её до 91 px — значок и текст в две строки, плашка 48 px вместо 28, шапка выше. ru/en — одна строка. | `node probe.mjs m375 reseller / … --locale kk --host resseler.localhost` → `span.rounded-full … h:48 w:91` (ru: 28×113, en: 28×100). Снимок `audit/ui/shots/reseller-dashboard_kk_m375.png`. | `shrink-0 whitespace-nowrap` на плашке (заголовок и так обрезается многоточием с `title`). | нет | CONFIRMED |
| UI-10 | Low | Агент: экран входа на 768-строчных мониторах | `agent/src/Zafiro.Agent.UI/Frontend/lock.css:156-168` (`.lock-card`, `max-height: calc(100% - 48px); overflow-y:auto`) — наследие `ClubSetupWindow.xaml:152-155` (ScrollViewer) | На 1366×768 / 1024×768 карточка входа ниже окна: блок «Вход с телефона» (QR) обрезан на 88 px посреди текста, в режиме регистрации скрыто 250 px (ссылки «Забыли пароль?», «Есть код брони?», QR); 1280×720 — 136/298 px. Прокрутка есть, но признака прокрутки нет (полоса — overlay). 1280×1024 и 1920×1080 — всё помещается. | `audit/ui/agent/lockprobe.mjs` (мост-заглушка): `1366x768 … "hidden":88 … register "hidden":250`; снимки `audit/ui/agent/shots/login-login-ru-1366x768.png`, `login-loginRegisterToggle-kk-1366x768.png`. | При `max-height: 820px` — компактнее отступы/QR 96 px, либо тень-градиент внизу карточки как признак прокрутки (`mask-image`). Нужен вопрос владельцу (вид = старый WPF). | нет | CONFIRMED |

## Что проверено и как

- **Стенд**: копия `web/ server/ db/ api/ proto/` в `audit/ui/repo` (репозиторий не менялся), `npm run build` (vite 8.3.5) → бинарник со
  вшитым фронтом, compose-проект `audit-ui` (Postgres 18.6/Valkey 9.1.3/NATS 2.15.2, tmpfs, порт 8291), демо-данные
  `tools/visual/fixtures/demo.json` + переводы старого `database/translations` и `db/seed/translations-new.*` (`audit/ui/stand.sh`).
  Остановлен и удалён после работы. Моки e2e не использовались: реальный сервер даёт все 81 маршрут с данными и kk/en.
- **Обход всех маршрутов** (`audit/ui/crawl.mjs`, детекторы — `lib.mjs`, перенесены из прошлого аудита): 81 страница (панель клуба —
  все роли, кабинет суперадмина `admin.localhost`, кабинет реселлера `resseler.localhost`, сайт и `/product/*`, гостевые `/pc-login/*`,
  ошибки) × 375×812, 768×1024, 1440×900 × ru/kk/en = 729 снимков состояния; плюс 320/360 для новых экранов, тёмная тема (15 страниц × 2
  ширины). Детекторы: прокрутка страницы, элементы за окном/контейнером, обрезка/выход текста, поля короче значения, перекрытие и
  достижимость интерактивных элементов, размер целей < 40/24 px, шрифт полей < 16 px (зум iOS), контраст, CLS.
- **axe-core 4.10.2** (WCAG 2.0/2.1/2.2 A/AA + best-practice, `target-size` включён) на каждой странице обхода ru (3 ширины) и в тёмной теме.
- **Клавиатура**: `audit/ui/focus.mjs` — 70 × Tab на 15 экранах: индикатор фокуса (outline/box-shadow) есть везде, кроме внутренних
  сегментов `type=time`/`date` (норма браузера); невидимые остановки — только UI-4. Ловушки фокуса и Esc (`audit/ui/trap.mjs`, `trap2.mjs`):
  «Добавить клиента», «Добавить тариф», «Новый товар» — фокус внутри, Esc закрывает, фокус возвращается на кнопку.
- **Наложения**: колокольчик + мобильное меню (`overlay2.mjs`) — меню закрывает панель уведомлений, наложения нет; баннер PWA — UI-7.
- **Неверные параметры адреса** — `audit/ui/badparams.mjs` (19 адресов) → UI-1.
- **Рендеринг**: карта клуба, событие `computer.load` через NATS (`audit/ui/perf/maprender.mjs`, прошлый скрипт FE-03); INP-прокси —
  Event Timing API при CPU ×4 на кассе, складе, карте, клиентах (`perf/inp.mjs`); холодная загрузка с сессией, slow 4G + CPU ×4,
  медиана 3 (`perf/vitals.mjs`); утечки — просмотр всех `setInterval`/`addEventListener` в `web/src` (у всех есть очистка) и в агенте.
- **Lighthouse 12.8.2** (`CHROME_PATH=/opt/pw-browsers/chromium-1243/chrome-linux64/chrome npx lighthouse@12.8.2 <url> --chrome-flags="--headless=new --no-sandbox"`):
  вход (mobile, desktop), лендинг, `/pricing` (mobile) — `audit/ui/lh/*.json`, `lh/parse.py`.
- **Фронтенд агента**: страница `Frontend/index.html` через `python3 -m http.server` (только чтение), мост `window.chrome.webview`
  подменён (`audit/ui/agent/harness.mjs`: снимок службы и ответы GuestService/ShopService/SocialService по proto `agent/v1`): экраны
  SETUP, вход (вход/регистрация/восстановление/код брони), лаунчер (7 разделов) × 1366×768, 1920×1080, 1280×1024 × ru/kk/en = 108
  состояний; те же детекторы; `lockprobe.mjs` (+1280×720, 1024×768); простой CPU — `idle.mjs`.
- **Регрессии прошлых находок не найдены**: UI-01 (ширина страницы с `sr-only`) — на 375 ни одной `page-hscroll`; UI-02 (сетки) — нет;
  UI-09/UI-10 (модалки) — работают; FE-03 (перерисовка карты) — исправлено (см. «Измерения»); FE-01 (сжатие) — `gzip` на ассетах и HTML;
  FE-16 — подписи минут исправлены, остались поля файлов (UI-6).
- **Известное, не новое** (решение владельца ожидается, в реестр не включено): контраст бренда `#ff0169` (3,84) и `#f14635` (3,7),
  пары ночной темы `text-gray-500` (3,66–3,93), секунды часов `#d90058` на тёмном (3,24), `page-has-heading-one` (66 страниц),
  `scrollable-region-focusable` (22 страницы) — UI-26/FE-16; сдвиг при подмене шрифта (UI-27/FE-22) — новое проявление: `/clubs/1` кабинета,
  CLS 0,187 на первом заходе, 0 при шрифте в кэше (`audit/ui/cls2.mjs`); «Настройки» за низом меню при 1440×900 (меню прокручивается) — вид старого.
- **Кросс-браузер (риск по коду)**: `:has()` (`app.css:2155,2306,2311,2747,2892`, `has-[:checked]` в `ShellThemeField`) — Firefox ≥ 121,
  Safari ≥ 15.4; `100dvh` (`app.css:3768`) — Safari ≥ 15.4; `inert` — Safari ≥ 15.5, Firefox ≥ 112; Tailwind 4 (oklch, `@property`,
  `color-mix`) — Chrome ≥ 111, Safari ≥ 16.4, Firefox ≥ 128 — всё как в старом (тоже Tailwind 4). Без `:has` выбранная тема ПК не
  выделяется (радио `sr-only`). Агент — WebView2 (evergreen), `color-mix` безопасен.

## Что не удалось проверить и почему

- **Firefox и WebKit** — не установлены, диск 2,3 ГБ свободно (ставить большие браузеры нельзя по правилам); остаточный риск — список
  CSS-функций выше (браузеры клубов старше 2023 г.).
- **Lighthouse для дашборда** — сессия: cookie обновления `__Secure-zafiro_rt` одноразовая и требует CSRF из `document.cookie`, а
  `--extra-headers` кладёт cookie только в заголовок (страница уходит на `/login`); запуск через порт отладки Playwright зависал
  (240 с). Вместо него — холодная загрузка с сессией в Playwright (slow 4G + CPU ×4), см. «Измерения».
- **Реальный WebView2 / Chrome на Windows** для UI-8 (данные `kk` в ICU) и полосы прокрутки карточки входа (UI-10) — нет Windows.
- **Карта на 300 ПК** — данные ответа размножены подменой `/api/club-map` (как в FE-03), не реальные 300 строк в базе.
- **React Profiler** не подключался (production-сборка без profiling); стоимость рендера — CDP `Performance.getMetrics`, мутации DOM, Event Timing.

## Измерения

**Lighthouse 12.8.2, мобильный профиль (slow 4G, CPU ×4)** — «до» для этого этапа (в скобках — ноябрьский аудит FE):

| Страница | Perf | A11y | BP | SEO | FCP | LCP | TBT | CLS | Вес |
|---|---|---|---|---|---|---|---|---|---|
| Вход | 78 (59) | 100 | 100 | 90 (нет `meta description`) | 3,6 с (6,8) | 4,3 с (8,3) | 50 мс | 0 | 402 КБ |
| Вход, desktop | 97 (90) | — | — | — | 0,9 с | 1,1 с | 0 | 0 | 309 КБ |
| Лендинг `/` | 83 | 95 | 100 | 100 | 2,9 с | 3,8 с | 110 мс | 0 | 308 КБ |
| `/pricing` | 83 | 95 | 100 | 100 | 2,9 с | 3,8 с | 90 мс | 0 | 307 КБ |

Подсказки: неиспользуемый JS 40–41 КБ (`main.js`), неиспользуемый CSS 21 КБ, блокирующий CSS `main.css` 33 КБ gzip (~750 мс на входе),
LCP входа — 89 % «render delay» (ждёт `main.js` + пакет переводов 87 КБ). Шрифты: Inter — `font-display: swap` (28 `@font-face`),
Font Awesome — `block`; `preload` шрифтов нет (FE-22).

**Холодная загрузка с вошедшей сессией** (`perf/vitals.mjs`, 375×812, slow 4G 150 мс/1,6 Мбит/с, CPU ×4, медиана из 3):

| Экран | FCP | LCP | Передано | Запросов |
|---|---|---|---|---|
| Дашборд | 3 164 мс | 3 256 мс | 458 КБ | 61 |
| Карта клуба | 2 748 мс | 2 748 мс | 413 КБ | 60 |
| Касса | 3 068 мс | 3 844 мс | 494 КБ | 69 |
| Вход | 2 416 мс | 2 416 мс | 340 КБ | 48 |
| Лендинг | 2 400 мс | 2 400 мс | 410 КБ | 50 |

**Сборка** (vite 8.3.5): `main` JS 279 КБ (86 КБ gzip), `chart` 171 КБ (60 КБ gzip, только карта), `apidocs` 1 307 КБ (только `/docs/api`),
`main` CSS 152 КБ (33 КБ gzip); `dist/assets` 4,75 МБ (61 woff2 + 62 woff — FE-12). Сервер: `gzip` для `/assets/*` (`immutable`, год) и HTML
(`no-cache`); `/images/*` — `max-age=3600`.

**Рендеринг карты клуба** (событие `computer.load` через NATS, 60 событий; было в FE-03 → стало):
12 ПК — Script 21,4 → **1,57 мс**/событие, Task 26,4 → 2,37 мс, мутаций DOM 76,9 → 3,2; 300 ПК — Script 37–44 → **1,60 мс**,
Task 44–55 → 3,01 мс. Событие без изменений — 0,47 мс.

**INP-прокси** (Event Timing, CPU ×4, 1440): касса — 12 добавлений в заказ, p75 40 мс, max 80; склад — «Новый товар»/Esc, p75 96, max 120;
карта — выбор 10 ПК, p75 88, max 96; клиенты — ввод в поиск, max 24 мс. Длинных задач — 0–39 мс суммарно. Все < 200 мс.

**Агент (страница WebView2)**: простой 30 с — экран входа: Task 31 мс, Script 0,9 мс, 0 перерасчётов стиля/раскладки; лаунчер: Task 12,4 мс,
Script 1,6 мс; JS heap 0,9 МБ, 1 249 узлов DOM; вызовы моста за 30 с — 1 (`agent.ReportActivity`). Бесконечная анимация — только
спиннер обновления баланса (по `aria-busy`).

**Обход**: 729 состояний (81 маршрут × 3 ширины × 3 языка) + 30 (320/360) + 30 (тёмная) + 108 (агент). Серьёзных срабатываний
детекторов на ru: 0 прокруток страницы, 0 элементов за окном, 0 недостижимых; на kk — UI-3, UI-9; на всех языках — UI-2.
axe (ru, 3 ширины): critical — `label` 21 узел/4 страницы, `select-name` 9/2 (вкл. Swagger UI); serious — `color-contrast` 152/12
(лендинг и сайт — бренд), `scrollable-region-focusable` 55/22, `nested-interactive` и `target-size` — только Swagger UI `/docs/api`.
