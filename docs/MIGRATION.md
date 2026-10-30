# Zafiro → zafirogo: чек-лист переноса

<!-- Файл собран tools/build-migration-doc.sh из docs/parts/*.md. Не редактировать вручную. -->

Источник правды — старый проект https://github.com/aitmaganbetov/Zafiro (пути ниже — относительно его корня).
Связанные документы: [бизнес-правила](business-rules.md), [эталонные сценарии](golden-scenarios.md),
[изменения схемы](schema-changes.md), [протокол агента](agent-protocol.md), [открытые вопросы](OPEN_QUESTIONS.md).

| Раздел | Пунктов |
|---|---:|
| 10. Экраны (resources/views) — инвентаризация | 88 |
| API Zafiro (Laravel): инвентаризация маршрутов, агент ПК, интеграции | 133 |
| 30. База данных: инвентаризация схемы Zafiro (MySQL → Postgres) | 69 |
| 40. Настройки: клуба, платформы, конфигурация | 64 |
| 50. Фоновые задачи, очередь, события | 37 |
| 60. Интеграции | 52 |


## 10. Экраны (resources/views) — инвентаризация

Источник: `Zafiro/resources/views` (67 Blade-шаблонов + `resources/wiki/index.html`), `routes/web.php`,
контроллеры `app/Http/Controllers`, middleware `app/Http/Middleware`, права `app/Support/ClubPermissions.php`.
Ссылки на маршруты — `routes/web.php:строка`.

### Сводка

| Группа | Экранов (страниц) | Шаблоны |
|---|---:|---|
| Вход и регистрация | 5 | `auth/*` (4), `invitations/accept` |
| Админка клуба | 29 | 13 «Рабочее пространство», 5 «Учёт», 11 «Система» (8 страниц настроек) |
| Суперадминка | 10 | `superadmin/*` (9) + общий `news/index` в режиме платформы |
| ПК (QR-вход) | 2 | `pc-login/show`, `pc-login/done` |
| Публичные | 4 | `welcome`, `offline`, `api-docs`, `resources/wiki/index.html` (статичный HTML) |
| **Итого экранов** | **50** | |
| Общие компоненты и layout (не экраны) | 19 | 3 layout, 5 partials, 1 компонент, 10 частичных шаблонов |

`news/index.blade.php` — один шаблон на два экрана (клуб и платформа), считается в обеих группах.
`cash-register/report.blade.php` — один шаблон для X- и Z-отчёта (один экран).

Мест опроса (polling) — **5** (см. раздел «Polling → WebSocket»), плюс 2 места «перезагрузка страницы при изменении».

### Общие правила доступа (middleware)

Алиасы — `bootstrap/app.php` (~стр. 37–51). В группу `web` добавлены `SetLocale`, `RestrictSuperadminHost`,
`RestrictWikiHost` (`bootstrap/app.php` ~стр. 59).

- [ ] `auth` — вход обязателен; гостя редиректит на `/login` (`bootstrap/app.php` ~стр. 36, `redirectGuestsTo`).
- [ ] `guest` — только для неавторизованных (страницы входа/регистрации).
- [ ] `current-club` = `SetCurrentClub`: блокированный аккаунт разлогинивается; выбирает текущий клуб из сессии
      `current_club_id` (или первый); суперадмин в режиме просмотра клуба (`superadmin_viewing_club_id`) — только
      чтение; без клубов суперадмин уходит в кабинет, остальным 403. Раздаёт во view `currentClub`,
      `currentMembership`, `availableClubs`, `superadminViewing` (`app/Http/Middleware/SetCurrentClub.php` ~стр. 96–132).
- [ ] `subscription` = `RequireActiveSubscription`: без активной подписки — редирект на `/billing`
      (JSON — 402); исключения: `billing.show`, `auth.logout`, `clubs.switch`.
- [ ] `club-area` = `RequireClubArea`: право по имени маршрута из таблицы `ROUTES` → `ClubPermissions::allows()`;
      маршрут, которого нет в таблице, — 403 (`app/Http/Middleware/RequireClubArea.php`).
- [ ] `club-role:a,b` = `RequireClubRole`: роль из списка или owner (`RequireClubRole.php`).
- [ ] `can:manage-integrations` — owner или `club_memberships.can_manage_integrations` (`AppServiceProvider.php` ~стр. 43,
      `ClubMembership::canManageIntegrations()`).
- [ ] `superadmin` = `RequireSuperadmin`: флаг `is_superadmin` и не заблокирован.
- [ ] `RestrictSuperadminHost`: на домене суперадмина (`SUPERADMIN_DOMAIN`) пускает только `superadmin.*`, `languages.*`,
      `auth.*`, либо суперадмина в режиме просмотра клуба; остальное → кабинет.
- [ ] Права «раздел.действие» и роли по умолчанию — `ClubPermissions::SECTIONS` (owner — всё; переопределения на
      сотрудника — `club_memberships.permissions`).
- [ ] Rate limit (`throttle:`) на веб-маршрутах: `superadmin.telegram.alerts.test` 5/мин, `languages.import` 20/мин,
      `club-invitations.accept` 10/мин, `pc-login.approve` 10/мин, `auth.password.send`/`auth.password.update` —
      именованный `password-recovery` (10/мин на IP+телефон, 30/мин на IP; `AppServiceProvider.php` ~стр. 72),
      `chat.reply` 60/мин, `users.password` 20/мин, `club-computers.guest-session` 30/мин, `reservations.customers` 60/мин.
      **На `POST /login` и `POST /register` throttle нет** — см. открытые вопросы.

---

### 1. Вход и регистрация (layout `layouts/auth.blade.php`)

- [ ] **Вход** — `auth/login.blade.php`
  - Маршрут: `GET /login` `auth.show-login` → `AuthController@showLogin`; `POST /login` `auth.login` → `AuthController@login` (`routes/web.php:157–158`).
  - Middleware: `guest` (+ `web`). Throttle нет.
  - Данные: нет (форма).
  - Форма: `phone` (локальная часть, маска), `password`, `remember` → `POST /login`. Ссылки на `/forgot-password`, `/register`.
  - JS: `resources/js/app.js` — маска телефона `.auth-phone-local` (формат `XXX XXX-XX-XX`, курсор сохраняется), перед отправкой оставляет только цифры.
- [ ] **Регистрация клуба** — `auth/register.blade.php`
  - Маршрут: `GET /register` `auth.show-register` → `AuthController@showRegister`; `POST /register` `auth.register` → `AuthController@register` (`routes/web.php:164–165`).
  - Middleware: `guest`.
  - Форма: `club_name`, `name`, `phone`, `password`, `password_confirmation`.
  - JS: маска телефона (`app.js`).
- [ ] **Забыли пароль** — `auth/forgot-password.blade.php`
  - Маршрут: `GET /forgot-password` `auth.password.forgot` → `PasswordRecoveryController@show`; `POST` `auth.password.send` → `@send` (`routes/web.php:160–161`).
  - Middleware: `guest`; POST — `throttle:password-recovery`.
  - Данные: `available` — настроен ли Telegram Gateway (`PasswordRecovery::available()`).
  - Форма: `phone` → код в Telegram на номер; телефон кладётся в сессию, редирект на `/reset-password`.
  - JS: маска телефона.
- [ ] **Новый пароль по коду** — `auth/reset-password.blade.php`
  - Маршрут: `GET /reset-password` `auth.password.reset` → `@edit` (без телефона в сессии — назад на forgot); `POST` `auth.password.update` → `@update` (`routes/web.php:162–163`).
  - Middleware: `guest`; POST — `throttle:password-recovery`.
  - Форма: `code`, `password`, `password_confirmation`.
- [ ] **Приглашение в клуб** — `invitations/accept.blade.php`
  - Маршрут: `GET /invitations/{token}` `club-invitations.show` → `ClubInvitationController@show`; `POST` `club-invitations.accept` → `@accept` (`routes/web.php:145–146`).
  - Middleware: нет `auth` (работает и для гостя), POST — `throttle:10,1,club-invitations.accept`.
  - Данные: приглашение (клуб, роль, email), `alreadyMember`.
  - Форма: авторизованному — кнопка «Принять»; гостю — регистрация `name`, `phone`, `password`, `password_confirmation`.
  - JS: маска телефона (`app.js`).

### 2. Админка клуба (layout `layouts/admin.blade.php`)

Все маршруты — в группе `auth` + `current-club` (`routes/web.php:168`). Страницы по разделам сайдбара
(`partials/sidebar.blade.php`). Общий JS каркаса (тема, уведомления, меню, часы, PWA) — раздел 6.

#### 2.1 Рабочее пространство

- [ ] **Админпанель (дашборд)** — `dashboard.blade.php`
  - Маршрут: `GET /dashboard` `dashboard` → `DashboardController` (`routes/web.php:177`).
  - Middleware: `subscription`, `club-role:owner,admin,manager,cashier,operator`. Кассира редиректит на `/cash-register`, оператора — на `/club-computers` (`DashboardController.php` ~стр. 20).
  - Данные: доход/расход/кол-во операций за сегодня и график 7 дней — один SQL с `SUM(CASE…)` по локальным дням клуба (`CashTransaction`, `GROUP BY type`); товары (всего, остаток < 5); зоны; ПК всего/онлайн/забронировано (загружает **все** ПК и считает статус в PHP — `currentStatus()`); клиентов; последние 6 операций кассы; валюта из `finances.currency`.
  - Формы: нет. Ссылки на кассу, карту, склад.
  - JS: Chart.js **4.5.1 с CDN jsdelivr** (`@push('scripts')`), столбчатый график доход/расход.
- [ ] **Касса (POS)** — `cash-register/index.blade.php`
  - Маршрут: `GET /cash-register` `cash-register.index` → `CashRegisterController@index` (`routes/web.php:270`).
  - Middleware: `subscription`, `club-area` → `cash.view` (по умолчанию admin, manager, cashier).
  - Данные: открытая смена и её итоги (`CashRegisterService::totals`); **все** товары с остатком > 0 (с фото); **все** клиенты клуба с балансами (join `club_memberships` + `member_balances`, без пагинации); активные услуги.
  - Формы: `POST /cash-register/shifts` (`opening_balance`) — открыть смену; `POST /cash-register/sales` (`items[i][product_id|service_id]`, `items[i][quantity]`) → редирект на чек; `POST /cash-register/balance-top-ups` (`user_id`, `amount`, `balance_type` balance/bonus_balance, `payment_method`, `cash_view`) — пополнение с бонусом за пополнение (`TopUpBonus`). Ссылки: операции смены, X-отчёт, Z-отчёт.
  - JS (inline): корзина (Map), категории, режимы и способы оплаты (визуально), клиентский выбор пользователя с фильтром по `data-search` (все пользователи уже в HTML), вкладки «Продажа/Пополнение баланса»; **расчёт в браузере**: «НДС» `Math.round(total * 5 / 105)` и итог (только отображение).
  - Не используется в UI: `POST /cash-register/transactions` `cash-register.transactions.store` (приход/расход) — есть маршрут и тест `tests/Feature/CashRegisterTest.php:26`, кнопки нет.
- [ ] **Операции смены** — `cash-register/operations.blade.php`
  - Маршрут: `GET /cash-register/operations` `cash-register.operations` → `@operations` (`routes/web.php:278`); 404 без открытой смены.
  - Middleware: `cash.view`.
  - Данные: приходы открытой смены с возвратом, пагинация 25.
  - Формы: партиал `cash-register/refund-button.blade.php` → `POST /cash-register/receipts/{id}/refund` (`cash.delete`), `confirm()`.
- [ ] **Чек** — `cash-register/receipt.blade.php`
  - Маршрут: `GET /cash-register/receipts/{cashTransaction}` `cash-register.receipt` → `@receipt` (только тип income).
  - Middleware: `cash.view`.
  - Данные: операция, смена, возврат; `canRefund`.
  - Формы: кнопка возврата (партиал). JS: `onclick="window.print()"`; печатная вёрстка через `@media print` / `@page` в CSS.
- [ ] **X/Z-отчёт** — `cash-register/report.blade.php`
  - Маршруты: `GET /cash-register/reports/x` `cash-register.x-report` → `@xReport`; `GET /cash-register/shifts/{cashShift}/z-report` `cash-register.z-report` → `@zReport`; `POST …/z-report/printed` `cash-register.z-report.printed` → `@markZReportPrinted`; `POST /cash-register/shifts/{cashShift}/close` `cash-register.shifts.close` → `@close` (`routes/web.php:280–283`).
  - Middleware: просмотр `cash.view`, печать/закрытие `cash.create`.
  - Данные: итоги смены, `ShiftReport::for()` (приход по видам, проданные товары, сотрудники), имена кассира/закрывшего/оператора.
  - Формы: закрыть смену (кнопка активна только после печати Z-отчёта).
  - JS: печать `window.print()`; на `afterprint` — `fetch POST …/z-report/printed` с одноразовым `nonce` из сессии; без отметки печати смена не закрывается (`CashRegisterController@close` проверяет флаг в сессии).
- [ ] **Зоны (карта клуба)** — `club-map/index.blade.php` (самый сложный экран, ~56 КБ)
  - Маршруты: `GET /club-map` `club-map.index` → `ClubZoneController@index`; `GET /club-map/state` `club-map.state` → `@state` (JSON); ресурс `club-map` store/update/destroy; `PATCH /club-map/{clubMap}/layout`; `POST /club-map/align`; `POST /club-map/{clubMap}/computers` `club-computers.store`; `PATCH|DELETE /club-computers/{id}`; `PATCH /club-computers/{id}/layout`; `POST /club-computers/control`; `POST /club-computers/end-sessions`; `POST /club-computers/notify`; `POST /club-computers/guest-session` (throttle 30/мин); `POST /reservations/many`; `GET /reservations/customers` (throttle 60/мин) (`routes/web.php:197–203, 272, 295–296, 303–308`).
  - Middleware: просмотр `club_map.view`; редактор `club_map.create|update|delete` (manager+); управление ПК `pc_control.update`, завершение сессий `pc_control.delete`; гостевая сессия `cash.create`; бронь `reservations.create`.
  - Данные: все зоны с ПК, сотрудником техрежима, пользователем сессии, тарифами зоны; состояние каждого ПК `ClubZoneController::computerStates()` (статус, подсветка pc-free/pc-account/pc-guest/pc-staff, сессия, окончание, что куплено — `SessionPurchase`, бронь, last_seen, версия агента и устаревание); все тарифы; гостевые функции (`GuestFeatures`).
  - Формы/модалки: зона (создать/изменить/удалить, `confirm`), ПК (номер, имя, MAC, зона, удалить), бронь выбранных ПК (поиск клиента), уведомление на ПК, «Подключить тариф» гостю (тариф или поминутно, способ оплаты), панель управления выбранными ПК (включить/перезагрузить/выключить/бронь/техрежим/уведомление/завершить сессию), снятие флага брони, редактор-переключатель (`edit_mode`).
  - JS (8 inline-блоков): **опрос `GET /club-map/state` каждые 15 с** и при возврате на вкладку; панель «Информация о ПК»; drag-and-drop зон и ПК pointer-событиями с сеткой (47×42), проверкой пересечений и сохранением `fetch PATCH …/layout`; поворот дивана; изменение размера; множественный выбор ПК; `fetch` на control/end-sessions/notify/guest-session/reservations.many; поиск клиента с debounce 250 мс и `AbortController`; уведомления-тосты `setTimeout` 2,5–6 с; после брони `location.reload()` через 1,2 с.
  - **Расчёт денег в браузере**: предпросмотр гостевой сессии поминутно — `Math.floor((Math.round(hour*100)*minutes + 30) / 60) / 100` за каждый ПК (`club-map/index.blade.php` ~стр. 138). Должен совпадать с серверным расчётом (`GuestSessionController` → `GuestSessions`).
- [ ] **Брони** — `reservations/index.blade.php`
  - Маршруты: `GET /reservations` `reservations.index` → `ComputerReservationController@index`; `POST /reservations` `reservations.store`; `POST /reservations/{id}/cancel`; `POST /reservations/{id}/arrive`; `GET /reservations/customers` (JSON, throttle 60/мин) (`routes/web.php:288–298`).
  - Middleware: `reservations.view|create|update|delete` (по умолчанию весь персонал).
  - Данные: брони за локальный день клуба (диапазон по `starts_at`), пагинация 50; **все** ПК клуба для выбора; `gapMinutes`; время начала по умолчанию (следующий час).
  - Формы: фильтр дня `GET ?date=` (пред./след. день); новая бронь (`club_computer_id`, `user_id`, `customer_label`, `starts_at`, `duration_minutes`, `note`); отменить (`confirm`); «клиент пришёл».
  - JS: поиск клиента по имени/телефону — `fetch /reservations/customers?q=` с debounce 250 мс и `AbortController`, выпадающий список.
- [ ] **To Do List** — `todos/index.blade.php`
  - Маршруты: `GET /todos` `todos.index`; `GET /todos/state` (JSON); `POST /todos/{task}/toggle`; `POST /todos`; `DELETE /todos/{task}` (`routes/web.php:180–182, 221–222`).
  - Middleware: `subscription`, `club-area` → `todos.view|create|update|delete`.
  - Данные: задачи дня (локальный день клуба), на сегодня — плюс просроченные невыполненные; `limit(300)`; кто и когда выполнил.
  - Формы: выбор дня `GET ?date=`; добавить (`title`, `due_on` ≥ сегодня); отметить (`done`); удалить (`confirm`).
  - JS: **опрос `GET /todos/state` каждые 15 с** (если вкладка видима); при расхождении — `location.reload()`.
- [ ] **Чат с клиентами** — `chat/index.blade.php`
  - Маршруты: `GET /chat` `chat.index`; `GET /chat/poll` (JSON); `POST /chat/{customer}` `chat.reply` (throttle 60/мин) (`routes/web.php:183–185`).
  - Middleware: `chat.view` (весь персонал); ответ — `chat.create` (кассир, оператор). Открытие диалога отвечающим помечает сообщения прочитанными.
  - Данные: до 100 диалогов (группировка по `customer_id`, `MAX(id)`, непрочитанные), последние 100 сообщений открытого диалога (автор, ПК).
  - Формы: ответ `body` (Enter — отправить, Shift+Enter — перенос) через `fetch` (JSON 201).
  - JS: **опрос `GET /chat/poll?customer=&after=` каждые 5 с** (если вкладка видима) — дописывает новые сообщения; если вырос общий счётчик непрочитанных и поле ввода пустое — `location.reload()`.
- [ ] **Заказы из агента** — `shop-orders/index.blade.php`
  - Маршруты: `GET /shop-orders` `shop-orders.index`; `GET /shop-orders/state` (JSON); `POST /shop-orders/{order}/done`; `POST /shop-orders/{order}/cancel` (`routes/web.php:186–189`).
  - Middleware: `orders.view|update|delete` (весь персонал).
  - Данные: заказы по статусу (`new` по умолчанию / done / cancelled / all), клиент, кто выдал; пагинация 30; число новых и `max(id)`.
  - Формы: «Выдан», «Отменить» (деньги возвращаются на счёт клиента; `confirm` через `data-confirm`).
  - JS: **опрос `GET /shop-orders/state` каждые 15 с**; если изменилось число новых или появился новый id — `location.reload()`.
- [ ] **Пользователи (клиенты)** — `users.blade.php`
  - Маршруты: `GET /users` `users.index` → `UserController@index`; `POST /users`; `PATCH /users/{membership}`; `POST /users/{membership}/password` (throttle 20/мин) (`routes/web.php:290–293`).
  - Middleware: `customers.view|create|update`; скидка/начальный баланс — `customer_money.update`; пополнение/списание без кассы — только owner (`UserController::canTopUpWithoutCash()`).
  - Данные: участники клуба кроме персонала, с балансами; пагинация 25; признак «сотрудник другого клуба/суперадмин» (только скидка и баланс); лояльность (`LoyaltyProgram`: накопительная скидка, «Потрачено»).
  - Формы: диалоги `<dialog>` «Новый клиент» (`name`, `phone`, `email`, `password`, `password_confirmation`, `discount`, `balance`, `bonus_balance`) и «Изменить клиента» (+ `top_up_amount`, `top_up_balance`, `balance_action` top_up/write_off); «Сбросить пароль» (`confirm`).
  - JS: `showModal()`/`close()` диалогов, закрытие по клику на фон, заполнение формы редактирования из data-атрибутов.
- [ ] **Пароль сброшен** — `users-password.blade.php`
  - Маршрут: ответ на `POST /users/{membership}/password` (`UserController@resetPassword`), заголовок `Cache-Control: no-store, private`.
  - Данные: клиент и новый пароль (показывается один раз). Формы/JS нет.
- [ ] **Работники** — `workers/index.blade.php`
  - Маршруты: `GET /workers` `workers.index` → `WorkerController@index`; `POST /workers`; `PATCH /workers/{membership}`; `DELETE /workers/{membership}`; `POST /club-invitations` `club-invitations.store` (`routes/web.php:196, 263–266`).
  - Middleware: `workers.view|create|update|delete` (по умолчанию только admin, owner — всё).
  - Данные: персонал (сортировка по роли), пагинация 50; кто на смене; лимиты кассиров/операторов на смене (`StaffShifts`); назначаемые роли.
  - Формы: добавить зарегистрированного по `phone` + `role`; пригласить по ссылке (`email`, `role`); сменить роль; убрать (`confirm`); ссылки на права сотрудника и настройки.
  - JS: копирование ссылки-приглашения `navigator.clipboard`.

#### 2.2 Учёт

- [ ] **Склад (каталог)** — `warehouse.blade.php`
  - Маршруты: `GET /warehouse` `warehouse` → `WarehouseController`; `POST /warehouse/products`; `POST /warehouse/categories`; `POST /warehouse/stock-movements`; `DELETE /warehouse/products/{product}` (`routes/web.php:194, 204–206, 231`).
  - Middleware: `warehouse.view|create|update` (manager+), `warehouse.delete` — по умолчанию никому, кроме owner.
  - Данные: **все** товары (для выпадающих списков), товары с пагинацией 50, категории с числом товаров, последние 10 движений.
  - Формы (модалки `.warehouse-modal`): категория (`name`); товар (`name`, `sku`, `product_category_id`, `price`, `quantity`, изображение); движение (`product_id`, `type` приход/списание, `quantity`, `note`); удаление товара (`confirm`). Скрытое `form_type` — какую модалку открыть после ошибки валидации.
  - JS: открытие/закрытие модалок, Escape, клик по фону, `body.style.overflow`, фокус первого поля через `setTimeout(50)`.
  - Замечание: в `RequireClubArea::ROUTES` есть `warehouse.products.update`, но такого маршрута нет.
- [ ] **Остатки / приход товара (для кассы)** — `stock/index.blade.php`
  - Маршруты: `GET /stock` `stock.index` → `StockController@index`; `POST /stock/receive` (`routes/web.php:310–311`).
  - Middleware: `stock.view|create` (весь персонал). Пункт меню «Склад» ведёт сюда, если нет `warehouse.view`.
  - Данные: товары с поиском по имени/SKU, пагинация 50, URL картинок.
  - Формы: поиск `GET ?search=`; принять товар (`product_id`, `quantity`, `note`, `confirm`).
- [ ] **Услуги** — `services/index.blade.php`
  - Маршруты: `GET /services` → `ClubServiceController@index`; `POST /services`; `PATCH /services/{service}`; `DELETE /services/{service}` (`routes/web.php:232–235`).
  - Middleware: `services.view|create|update|delete` (manager+).
  - Данные: услуги (активные сначала), пагинация 50; флаги `canCreate/canUpdate/canDelete`.
  - Формы: добавить/изменить (`name`, `price`, `unit`, `is_active`), удалить (`confirm`). JS нет.
- [ ] **Список ПК** — `club-computers/index.blade.php`
  - Маршрут: `GET /club-computers` `club-computers.index` → `ClubComputerListController` (`routes/web.php:305`).
  - Middleware: `club_map.view`. Стартовая страница оператора.
  - Данные: ПК с зоной, пагинация 50 (соединение, статус/бронь, позиция, состояние агента, ОС, сессия, версия агента, последний heartbeat, железо); блок «Новая версия агента» с инструкцией для бездисковых клубов и ссылкой на установщик (`AgentUpdate::status()`).
  - Формы/JS: нет (только чтение). Статусы не обновляются без перезагрузки.
- [ ] **Логи действий** — `activity-logs/index.blade.php`
  - Маршрут: `GET /activity-logs` `activity-logs.index` → `ActivityLogController@index` (`routes/web.php:195`).
  - Middleware: `activity_logs.view` (manager+).
  - Данные: журнал клуба, фильтры `from`/`to` (локальные дни клуба → UTC-диапазон), `event`, `user_id`, `search` (описание, имя/телефон пользователя — `LIKE '%…%'`); пагинация 50; список пользователей, встречающихся в журнале.
  - Формы: фильтр `GET`. JS нет.

#### 2.3 Система

Страницы настроек используют `settings/layout.blade.php` (левое меню настроек + общая форма `POST /settings/save`
со скрытым `section`) и `<x-settings.card>`; якоря подразделов — `settings/anchors.blade.php`, текущий якорь
подсвечивает скрипт `settings/layout.blade.php` (scroll-listener).

- [ ] **Настройки → Настройка клуба** — `settings/club.blade.php`
  - Маршруты: `GET /settings` (редирект) и `GET /settings/club` `settings.club` → `SettingsController@club`; `POST /settings/save`; `POST /settings/timezone`; `POST|DELETE /settings/logo` (`ClubLogoController`); `POST|DELETE /settings/signin-image` (`ClubSignInImageController`); `POST /settings/agent-pin`, `POST /settings/agent-staff-time` (`routes/web.php:207–219, 261–262`).
  - Middleware: `settings.view` / `settings.update`; PIN агента и время сотрудника — `workers.update`.
  - Данные: настройки клуба (`loadSettings()`), часовой пояс и список зон, дата смены PIN, минуты техрежима, ключ установки клуба.
  - Формы (8 карточек): «Сессии и ПК» (`pc_action_after_session_end`, …), «Агент на ПК» (`agent_auto_update`, `agent_idle_logout_minutes`, `agent_shop_enabled`), «Сотрудники на смене» (лимиты), «Ключ установки клуба», «Логотип клуба» (multipart `logo`), «Картинка экрана входа» (multipart `image`), «Часовой пояс» (`timezone`), «PIN-код для выхода из агента» (`pin`, `pin_confirmation`; `minutes`).
  - JS: копирование ключа установки (`navigator.clipboard`); подсветка якорей.
- [ ] **Настройки → Языки** — `settings/languages.blade.php`
  - Маршруты: `GET /settings/languages` → `SettingsController@languages`; `POST /settings/language` `settings.language` → `LanguageController@select`.
  - Middleware: `settings.view` / `settings.update`.
  - Данные: языки с числом переведённых строк (агрегат в SQL), язык клуба, всего строк в каталоге.
  - Формы: выбор языка интерфейса клуба (`locale`).
- [ ] **Настройки → Финансы** — `settings/finances.blade.php`
  - Маршрут: `GET /settings/finances` → `SettingsController@finances`; сохранение — `POST /settings/save` (section=finances).
  - Middleware: `settings.view` / `settings.update`.
  - Формы: `currency`; «Оплата времени» (`discount_scope`, `bonus_max_percent`, `allow_mixed_payment`, `allow_whole_balance`, `auto_billing`, `auto_billing_pause_minutes`); «Накопительная скидка» (`loyalty_enabled`, `loyalty_levels[i][spent|percent]`); «Бонусы за пополнение» (`topup_bonus_enabled`, `topup_bonus_levels[i][amount|percent]`) — таблицы уровней из партиала `settings/percent-levels.blade.php`.
  - JS: добавление/удаление строк уровней из `<template>`, пустое состояние.
- [ ] **Настройки → Гости** — `settings/guests.blade.php`
  - Маршрут: `GET /settings/guests`; сохранение — `POST /settings/save`.
  - Формы: `guest_mode_enabled`, `show_qr_code_on_login`, `allow_adding_friends`, `allow_balance_transfer_to_friends`, `allow_guest_self_transfer`. JS нет.
- [ ] **Настройки → API** (справка для мобильного приложения) — `settings/api.blade.php`
  - Маршрут: `GET /settings/api` → `SettingsController@api`.
  - Данные: список настроек клуба с URL `api.settings.show`, пример ответа `api.products.index`, ссылки `api.auth.login`, `api.clubs.resolve`, хост.
  - Формы нет; `@push('scripts')` пустой.
- [ ] **Настройки → Права (список)** — `settings/permissions.blade.php`
  - Маршрут: `GET /settings/permissions` → `PermissionController@index` (`routes/web.php:240`).
  - Middleware: `subscription`, `club-role:owner,admin` (админ не правит других админов — `mayEdit`).
  - Данные: персонал кроме owner.
- [ ] **Настройки → Права сотрудника** — `settings/permissions-edit.blade.php`
  - Маршруты: `GET /settings/permissions/{membership}`; `PUT …` `settings.permissions.update`; `POST …/reset` (`routes/web.php:241–243`).
  - Middleware: `club-role:owner,admin`.
  - Данные: сотрудник, `ClubPermissions::sections()` и подписи действий.
  - Формы: матрица `rights[section][action]` (по роли / дать / запретить), сброс к правам роли.
- [ ] **Настройки → API и интеграции** — `settings/integrations.blade.php`
  - Маршруты: префикс `settings/integrations`, `GET /` → `IntegrationController@index`; ключи (`POST /keys`, `POST /keys/{id}/rotate`, `DELETE /keys/{id}`), вебхуки (`POST /webhooks`, `PATCH`, `POST …/secret`, `DELETE`), `POST /deliveries/{id}/retry`, `PATCH /members/{membership}` (`routes/web.php:247–258`).
  - Middleware: `subscription`, `can:manage-integrations`; права сотрудникам выдаёт только owner (`isOwner`).
  - Данные: API-ключи, вебхуки, последние 20 доставок, последние 30 запросов API, персонал, scopes и события из `config/integrations.php`; новый секрет показывается один раз, ответ `Cache-Control: no-store, private`.
  - Формы: ключ (`name`, `scopes[]`, `allowed_ips`, `rate_limit_per_minute`, `expires_at`), вебхук (`url`, `events[]`, `is_active`), право `can_manage_integrations`; `confirm()` на отзыв/удаление/ротацию.
  - JS: копирование секретов (`navigator.clipboard`, запасной `execCommand('copy')`).
- [ ] **Новости клуба** — `news/index.blade.php` (режим клуба)
  - Маршруты: `GET /news` `news.index` → `NewsController@index`; `POST /news`; `PATCH /news/{news}`; `DELETE /news/{news}` (`routes/web.php:223–226`). Картинки — публичный `GET /news-images/{news}/{file}`.
  - Middleware: `news.view|create|update|delete` (manager+).
  - Данные: новости клуба, пагинация `News::PAGE`; текст-пояснение.
  - Формы: multipart `title`, `body`, `image`, `remove_image`, `is_published`; удалить (`confirm`). Шаблон выбирает layout через `@extends($layout)` и маршруты через `$routes`.
- [ ] **Подписка** — `billing/index.blade.php`
  - Маршрут: `GET /billing` `billing.show` → `BillingController@show` (`routes/web.php:173`).
  - Middleware: `club-area` → `billing.view` (без `subscription` — сюда редиректят при неактивной подписке).
  - Данные: подписка клуба и план (статус, пробный период, оплачено до). Формы нет («Онлайн-оплата появится…»).
- [ ] **Тарифы** — `tariffs/index.blade.php` (+ партиал `tariffs/periods.blade.php`)
  - Маршруты: `GET /tariffs` `tariffs.index`; `GET /tariffs/{tariff}/edit` `tariffs.edit` (тот же экран с открытой формой); `POST /tariffs`; `PATCH /tariffs/{tariff}`; `DELETE /tariffs/{tariff}` (`routes/web.php:220, 227–230`).
  - Middleware: `tariffs.view|create|update` (manager+); удаление — `tariffs.delete` (по умолчанию никому) и в UI только owner (`canDelete`).
  - Данные: зоны с ПК, фильтр по зоне `?zone=`, тарифы с зонами; проданное количество для тарифов с лимитом (`COUNT` по `activity_logs` `tariff.purchased`); валюта.
  - Формы (`<dialog>`): `payment_method`, `name`, `duration_value`+`duration_unit`, `price`, `discount_enabled`, `zone_ids[]`, `usage_limit_*`, `quantity_limit_*`, `sale_periods[i][start_day|start_time|end_day|end_time]`, `validity_periods[…]`, `is_active`; удалить (`confirm`); ссылка «Изменить цену часа в зонах» → карта клуба.
  - JS: открытие диалога (и автозаполнение при `edit`), добавление строк периодов из `<template>` (новая строка — следующий день недели).

### 3. Суперадминка (layout `layouts/superadmin.blade.php`, меню `partials/superadmin-sidebar.blade.php`)

Middleware всех маршрутов: `auth`, `superadmin`; на отдельном домене `SUPERADMIN_DOMAIN` с корня или с префиксом
`/superadmin` (`routes/web.php:52–106`). Flash-сообщения и первая ошибка — в layout.

- [ ] **Сервер (дашборд)** — `superadmin/dashboard.blade.php`
  - Маршрут: `GET /` `superadmin.dashboard` → `Superadmin\DashboardController`; `PUT /agent-release`, `DELETE /agent-release` → `AgentReleaseController` (`routes/web.php:60–62`).
  - Данные: период `?period=` (агрегаты по минутным бакетам в SQL): CPU, память, запросы/мин по каналам, время ответа, доля 5xx, нагрузка по выбранным клубам `clubs[]`; живой снимок сервера (`ServerProbe`), задержка планировщика, запросы за 15 мин, крупнейшие таблицы, последние ошибки лога, сертификаты, бэкап, ПК всего/онлайн, версии агентов, релиз агента, последние 10 записей журнала платформы.
  - Формы: фильтр `GET` (period, clubs[]); объявить версию агента (`latest_version`, `download_url`, `released_at`, `auto_update`) / снять (`confirm`).
  - JS: нет; графики — серверный SVG (`superadmin/partials/chart.blade.php`, `App\Support\LineChart`). Автообновления нет.
- [ ] **Клубы** — `superadmin/clubs/index.blade.php`
  - Маршрут: `GET /clubs` `superadmin.clubs.index` → `Superadmin\ClubController@index`.
  - Данные: клубы с подзапросами (владелец, ПК всего/онлайн/занято, запросы агентов/API за `LOAD_MINUTES`, последний вход сотрудника, сессий за сутки, ошибки вебхуков, окончание подписки), поиск, фильтр подписки, сортировка (`sort-link`), пагинация 25; устаревшие агенты; «требует внимания» (`ClubAttention`); итоги.
  - Формы: фильтр `GET`. JS нет.
- [ ] **Клуб** — `superadmin/clubs/show.blade.php`
  - Маршруты: `GET /clubs/{club}`; `POST …/subscription/extend` (`days`), `…/suspend` (`confirm`), `…/plan` (`saas_plan_id`); `POST /clubs/{club}/view` — войти в панель клуба только для просмотра; `POST /leave-club` (`routes/web.php:65–70`).
  - Данные: клуб и подписка, участники (пагинация 30), ПК всего/онлайн, тепловая карта загрузки (день недели × час за 28 дней из `club_load_snapshots`), планы, 15 записей журнала клуба и 15 — платформы (`superadmin/logs/rows`).
- [ ] **Пользователи (аккаунты)** — `superadmin/users/index.blade.php`
  - Маршруты: `GET /accounts`; `POST /accounts/{user}/block|unblock`; `POST|DELETE /accounts/{user}/superadmin` (`routes/web.php:72–76`).
  - Данные: аккаунты с членствами во всех клубах, поиск по имени/телефону, фильтр blocked/superadmin, пагинация 30.
  - Формы: фильтр; блок/разблок, выдать/снять суперадмина (`confirm`).
- [ ] **Тарифы платформы** — `superadmin/plans/index.blade.php` (+ `plans/fields.blade.php`)
  - Маршруты: `GET /plans`; `POST /plans`; `PATCH /plans/{plan}` (`routes/web.php:78–80`).
  - Данные: планы с числом клубов. Формы: `name`, `slug`, `price_monthly`, `trial_days`, `features`, `is_active`.
- [ ] **Новости платформы** — `news/index.blade.php` (режим платформы)
  - Маршруты: `GET|POST /platform-news`, `PATCH|DELETE /platform-news/{news}` → `Superadmin\NewsController` (`routes/web.php:92–95`). Данные/формы — как у новостей клуба.
- [ ] **Журнал** — `superadmin/logs/index.blade.php` (+ `logs/rows.blade.php`)
  - Маршрут: `GET /logs` → `Superadmin\LogController` (`routes/web.php:82`).
  - Данные: вкладки «платформа» (`PlatformLog`) / «клубы» (`ActivityLog` без скоупа клуба), фильтры `club`, `event`, пагинация 50.
- [ ] **Языки** — `superadmin/languages.blade.php`
  - Маршруты: `GET /languages` `superadmin.languages`; `POST /languages`; `PATCH|DELETE /languages/{language}`; `GET /languages/export`; `POST /languages/import` (throttle 20/мин, multipart `file`) (`routes/web.php:83, 99–103`).
  - Данные: языки с числом переводов и клубов. Формы: `code`, `name`, `is_enabled`; удалить (`confirm`); экспорт/импорт.
- [ ] **Переводы языка** — `superadmin/translations.blade.php`
  - Маршруты: `GET /languages/{language}/translations`; `PUT …` `languages.translations.save` (`routes/web.php:104–105`).
  - Данные: весь каталог исходных строк (`TranslationCatalog::sourceKeys()`) в памяти, фильтр `search`/`missing`, пагинация 50 **в PHP** (`LengthAwarePaginator`).
  - Формы: `translations[hash]` — сохранение страницы переводов.
- [ ] **Telegram** — `superadmin/telegram.blade.php`
  - Маршруты: `GET|POST|DELETE /telegram`; `POST|DELETE /telegram/alerts`; `POST /telegram/alerts/test` (throttle 5/мин) (`routes/web.php:85–90`).
  - Данные: настроен ли Telegram Gateway (без токена), чат оповещений, отправлено кодов за 30 дней.
  - Формы: `token` (Gateway), `bot_token` + `chat_id` (оповещения о сбоях), тест, удаление (`confirm`). Токены не возвращаются в форму и не флешатся (`bootstrap/app.php` `dontFlash`).

### 4. ПК (QR-вход с телефона)

- [ ] **Подтвердить вход на ПК** — `pc-login/show.blade.php` (layout `auth`)
  - Маршруты: `GET /pc-login/{code}` `pc-login.show` → `QrLoginController@show`; `POST /pc-login/{code}` `pc-login.approve` → `@approve` (`routes/web.php:151–152`), `code` — 40 символов `[A-Za-z0-9]`.
  - Middleware: `auth` (без `current-club` — любой клуб кода); POST — `throttle:10,1,pc-login.approve`.
  - Данные: открытый QR-вход (`QrLogins::findOpen`): клуб, ПК; иначе «QR-код устарел» (5 минут).
  - Формы: «Войти на ПК» → POST approve; «Выйти из аккаунта» → `POST /logout`.
  - JS: нет (маска телефона `app.js` — на странице входа, куда редиректит `auth`). ПК узнаёт об одобрении опросом со стороны агента (`agent-qr-poll`, `routes/api.php`) — вне экранов админки.
- [ ] **Готово** — `pc-login/done.blade.php`
  - Маршрут: `GET /pc-login/done` `pc-login.done` (`routes/web.php:150`), `auth`. Статичный текст.

### 5. Публичные

- [ ] **Лендинг** — `welcome.blade.php`
  - Маршрут: `GET /` `home` → `LandingController` (`routes/web.php:120`).
  - Данные: максимальный пробный период активных планов (по умолчанию 14), ссылка на установщик агента (`AgentUpdate::downloadUrl()` или GitHub releases).
  - Ссылки: вход, регистрация, дашборд, wiki. JS нет (только `app.js`).
- [ ] **Нет подключения (PWA offline)** — `offline.blade.php`
  - Маршрут: `GET /offline` `pwa.offline` → `PwaController@offline` (`Cache-Control: no-cache`); показывает service worker `public/sw.js` при навигации без сети.
  - Данные: тексты на всех включённых языках.
  - JS: выбор языка по `localStorage['zafiro.locale']`, затем `navigator.languages`; «Повторить» и `online` → `location.reload()`.
- [ ] **Справка Integration API** — `api-docs.blade.php`
  - Маршруты: `GET /docs/api` `api-docs` (`Route::view`); `GET /docs/api/openapi.yaml` `api-docs.spec` (`resources/api/openapi-v1.yaml`) (`routes/web.php:127–129`).
  - JS: Swagger UI **5.17.14 с CDN jsdelivr** с SRI, `tryItOutEnabled: false`.
- [ ] **Wiki** — `resources/wiki/index.html` (статичный HTML ~91 КБ, не Blade)
  - Маршрут: `GET /wiki` `wiki` или корень `WIKI_DOMAIN` (старый `/wiki` → 301) (`routes/web.php:110–117`), `Cache-Control: public, max-age=300`.
- [ ] Публичные ресурсы без шаблона (не экраны, но нужны новому серверу): `GET /manifest.webmanifest` (`PwaController@manifest`, ярлыки Касса/Склад/Список ПК), `GET /club-logos/{club}/{file}`, `GET /club-signin-images/{club}/{file}`, `GET /news-images/{news}/{file}` (случайное имя из 40 символов, `immutable`), `public/sw.js`, `public/icons/*`, `public/images/*`.

### 6. Общие компоненты и layout

- [ ] `layouts/admin.blade.php` — каркас панели клуба: `<head>` (шрифты, PWA, тема до стилей, Vite, Font Awesome 6.4.0 с cdnjs), оверлей, `.admin-app` (свёрнутость из `localStorage` до отрисовки), сайдбар, верхняя панель: кнопка меню, заголовок `@yield('page-title')`, выбор клуба (если клубов > 1, `POST /clubs/switch` по `onchange`), поле поиска **без обработчика**, часы клуба (`data-clock`), переключатель темы, уведомления; баннер «суперадмин смотрит клуб» с `POST /leave-club`; PWA-баннер.
  - JS: тема (`zafiro.theme`, класс `.dark`); уведомления — **опрос `GET /notifications` раз в 60 с** и при `visibilitychange`, `POST /notifications/seen` при открытии, звук Web Audio, `zafiro.notificationsHeard`, `zafiro.notificationSound`.
  - Маршруты: `GET /notifications` `notifications.index` → `NotificationController@index`, `POST /notifications/seen` (`club-role:owner,admin,manager,cashier,operator`, `routes/web.php:175–176`). Источники уведомлений: журнал действий, новые заказы, непрочитанный чат, низкий остаток, ПК не на связи, новая версия агента, подписка, приглашения (`app/Services/ClubNotifications.php`).
  - **Перенесено**: `web/src/layouts/AdminLayout.tsx` (+ `ClubClock`, `ThemeToggle`, `NotificationsMenu`, `PwaBanner`).
- [ ] `partials/sidebar.blade.php` — меню клуба по правам, бейджи «Чат» (`ChatController::unreadTotal()`) и «Заказы» (`ShopOrder::newCount()`) — **два SQL COUNT на каждую страницу**, роль и выход. **Перенесено**: `web/src/layouts/Sidebar.tsx`, `navigation.ts`, `permissions.ts`.
- [ ] `partials/sidebar-script.blade.php` — свёртывание (desktop, `zafiro.sidebarCollapsed`), шторка (≤768px), Escape, оверлей, `title` у ссылок в свёрнутом режиме, aria-атрибуты. **Перенесено** в `AdminLayout.tsx`.
- [ ] `partials/fonts.blade.php` — Inter 400/500/600/700 с Google Fonts (локальных шрифтов в проекте нет).
- [ ] `partials/pwa.blade.php` — manifest, `theme-color` `#101713`, иконки, apple-meta.
- [ ] `layouts/superadmin.blade.php` + `partials/superadmin-sidebar.blade.php` — тот же каркас, меню: Сервер, Клубы, Пользователи, Тарифы платформы, Новости, Журнал, Языки и переводы, Telegram, «Панель своего клуба» (если есть членство). Не перенесено.
- [ ] `layouts/auth.blade.php` — двухколоночный экран входа (`.auth-shell`, визуальная часть с логотипом). Не перенесено.
- [ ] `components/settings/card.blade.php` — карточка подраздела настроек (`<x-settings.card>`).
- [ ] `settings/layout.blade.php`, `settings/anchors.blade.php` — меню настроек и якоря (scroll-подсветка).
- [ ] `settings/percent-levels.blade.php` — таблица уровней (лояльность, бонус за пополнение).
- [ ] `tariffs/periods.blade.php` — периоды продажи/действия тарифа.
- [ ] `cash-register/refund-button.blade.php` — кнопка возврата с `confirm`.
- [ ] `superadmin/clubs/sort-link.blade.php`, `superadmin/clubs/subscription-badge.blade.php`, `superadmin/logs/rows.blade.php`, `superadmin/partials/chart.blade.php` (SVG-график), `superadmin/plans/fields.blade.php`.
- [ ] `resources/js/app.js` — маска телефона на `.auth-form`, язык для offline, регистрация `sw.js`, PWA-баннер, часы клуба. `resources/css/app.css` — перенесён в `web/src/styles/app.css`.
- [ ] Пагинация — стандартный Tailwind-шаблон Laravel (`->links()`; в старом CSS `@source` на `vendor/laravel/.../Pagination/resources/views`) — нужен React-компонент с теми же классами.
- [ ] Flash-сообщения (`session('status')`, `$errors->first()`) — в каждой странице свой блок; в SPA нужен общий механизм.

### Polling → WebSocket

| # | Где | Что опрашивает | Интервал | Что делает с ответом | Предлагаемое событие WS |
|---|---|---|---|---|---|
| 1 | `layouts/admin.blade.php` (уведомления) | `GET /notifications` | 60 с + при возврате на вкладку | перерисовывает список, точка, звук для новых | `notifications.changed` (список/счётчик на сотрудника) |
| 2 | `club-map/index.blade.php` ~стр. 46–52 | `GET /club-map/state` (все ПК клуба, `computerStates`) | 15 с + при возврате на вкладку | обновляет классы/статусы/data-атрибуты ПК | `computer.state` (дифф по одному ПК: статус, сессия, техрежим, бронь, версия) |
| 3 | `chat/index.blade.php` | `GET /chat/poll?customer=&after=` | 5 с (вкладка видима) | дописывает сообщения; при росте непрочитанных — `location.reload()` | `chat.message` (новое сообщение), `chat.unread` (счётчик) |
| 4 | `todos/index.blade.php` | `GET /todos/state?date=` | 15 с | при расхождении — `location.reload()` | `todo.changed` (создана/выполнена/удалена) |
| 5 | `shop-orders/index.blade.php` | `GET /shop-orders/state` | 15 с | при новом заказе/изменении числа новых — `location.reload()` | `shop_order.created`, `shop_order.updated` |

Связанные места без опроса, которым в SPA тоже нужен push:
- [ ] Бейджи сайдбара «Чат» и «Заказы» (`partials/sidebar.blade.php`) — сейчас обновляются только при переходе по страницам → `chat.unread`, `shop_order.*`.
- [ ] Список ПК (`club-computers/index.blade.php`) и дашборд (онлайн/забронировано) — статусы только при загрузке → те же `computer.state`.
- [ ] Вне админки (не входит в эту часть, для сведения): агент опрашивает QR-вход (`agent-qr-poll`), приложение сотрудника опрашивает карту/чат/счётчики (лимитер `staff-app` 240/мин, `AppServiceProvider.php` ~стр. 77), heartbeat агентов — заменяются gRPC-стримом (часть «API и агент»).

### Замечания по производительности экранов (для переноса)

- [ ] Касса: все клиенты клуба и все товары в HTML без пагинации (`CashRegisterController@index`) → поиск через API, как `reservations.customers`.
- [ ] Склад: `Product::get()` всех товаров для выпадающих списков (`WarehouseController`).
- [ ] Брони: все ПК клуба для выбора (`ComputerReservationController@index`).
- [ ] Дашборд: статусы ПК считаются в PHP по всем ПК (`DashboardController` ~стр. 50).
- [ ] Переводы суперадмина: весь каталог строк в памяти и пагинация в PHP (`Superadmin\LanguageController@translations`).
- [ ] Логи: `LIKE '%…%'` по описанию и пользователю без индекса (`ActivityLogController`).
- [ ] Сайдбар: два COUNT на каждую страницу.

## API Zafiro (Laravel): инвентаризация маршрутов, агент ПК, интеграции

Часть документа `docs/MIGRATION.md` (субагент «API и агент», Этап 0).
Источник — `/home/user/Zafiro` (только чтение). Все пути файлов ниже — относительно корня старого репозитория.

Итого маршрутов: **90** — `routes/api.php` **49**, `routes/api_staff.php` **24**, `routes/api_v1.php` **17**.

Новый контракт агента и гостя: `proto/agent/v1/*.proto`, описание — `docs/agent-protocol.md`.
Открытые вопросы — `docs/open-questions/api.md`.

### 1. Общая обвязка (`bootstrap/app.php`, `app/Providers/AppServiceProvider.php`)

- [ ] Префиксы: `routes/api.php` подключён стандартно с префиксом `/api` (`bootstrap/app.php`, `withRouting(api: …)`, строка 31); внутри него `routes/api_staff.php` — префикс `/api/staff`, имя `api.staff.` (`routes/api.php`, строки 174–177); `routes/api_v1.php` — префикс `/api/v1`, имя `api.v1.` (`routes/api.php`, строки 180–183). Health-check Laravel — `/up` (`bootstrap/app.php`, строка 33).
- [ ] Группа `api` дополнена `SetLocale` (`bootstrap/app.php`, строка 61); ко всем запросам добавлен `RecordRequestMetrics` (строка 59).
- [ ] Алиасы middleware (`bootstrap/app.php`, строки 37–51): `current-club` → `SetCurrentClub`, `club-role` → `RequireClubRole`, `club-can` → `RequireClubPermission`, `subscription` → `RequireActiveSubscription` (HTTP 402 при неактивной подписке), `agent.device` → `AuthenticateAgentDevice`, `api.log`/`api.size`/`api.key`/`api.scope`/`api.idempotent`/`api.limit` → `app/Http/Middleware/Api/*`.
- [ ] Порядок приоритета: `SetCurrentClub` после аутентификации; для `/api/v1` — `LogApiRequest`, `LimitApiRequestSize`, `AuthenticateApiKey`, `ApiRateLimit` раньше `ThrottleRequests` и route model binding (`bootstrap/app.php`, строки 52–58), чтобы привязки моделей шли уже в клубе ключа.
- [ ] Ошибки: для `api/*` всегда JSON (`shouldRenderJsonWhen`, строка 66); для `api/v1/*` — единый формат `App\Support\ApiError::fromThrowable` (строка 69).
- [ ] `auth:sanctum` — токены Sanctum (`personal_access_tokens`), ability `club:<id>`, срок 30 дней (`AuthController::issueUserLoginResponse`, ~строки 97, 114–118; `AgentRegisterController`; `QrLogins::poll`). Клуб запроса — `SetCurrentClub` (заголовок `X-Club-Id` только среди клубов токена; см. `docs/integration-api.md`, раздел 1).
- [ ] `agent.device` (`app/Http/Middleware/AuthenticateAgentDevice.php`): заголовок `X-Agent-Token` (`zag_` + 48 символов, в БД `club_computers.agent_token_hash` = sha256, `ClubComputerSessions::findByAgentToken`, ~строка 253); опционально `X-Agent-Device` (UUID или `MAC-AABBCCDDEEFF`) — несовпадение с ПК → 401 `device_mismatch`; неактивная подписка → 402; ставит клуб, UTC, язык ПК (`LocaleResolver::forAgentRequest`), атрибут `agent_computer`. Неверный токен → 401 `invalid_agent_token`.

#### 1.1. Именованные лимиты `throttle:<имя>` (`AppServiceProvider::boot`, строки 47–97)

Ключ «ПК» (`agentRateLimitKey`, строки 126–138): sha256 от `X-Agent-Token`, иначе id Sanctum-токена, иначе IP.

| Имя | Лимит | Ключ |
|---|---|---|
| `club-computers` | 60/мин | ПК |
| `agent-enroll` | 5/мин и 300/мин | (IP + sha256(device_id)) и IP |
| `agent-exit-pin` | 5 за 5 мин и 100/час | ПК и клуб ПК |
| `customer-login` | 5/мин и 300/мин | (IP + цифры телефона) и IP |
| `password-recovery` | 10/мин и 30/мин | (IP + телефон) и IP |
| `staff-app` | 240/мин | пользователь (или IP) |
| `club-resolve` | 10/мин и 300/мин | (IP + sha256(ключ клуба)) и IP |
| `agent-register` | 5 за 10 мин и 60 за 10 мин | ПК и IP |
| `agent-qr-login` | 20/мин | ПК |
| `agent-qr-poll` | 60/мин | ПК |
| `agent-token-rotate` | 10/мин | ПК |
| `agent-translations` | 600/мин | IP |
| `club-computer-registration` | 10/мин | ПК |

`throttle:N,M,<префикс>` — N запросов за M минут с отдельным счётчиком на маршрут (ключ — пользователь, без входа — IP; стандарт Laravel `ThrottleRequests`).

#### 1.2. Middleware `/api/v1` (`app/Http/Middleware/Api/*`, `config/integrations.php`)

- [ ] `api.log` — журнал запросов `LogApiRequest` (хранение 90 дней, `request_log_retention_days`).
- [ ] `api.size` — тело ≤ 256 КБ (`LimitApiRequestSize`, `max_request_kb`).
- [ ] `api.key` — ключ `Authorization: Bearer zfk_…` или `X-Api-Key` (`AuthenticateApiKey::presentedToken`, ~строка 82); 20 неудачных попыток/мин на IP → 429 `too_many_failed_attempts`; `allowed_ips` → 403 `ip_not_allowed`; маршрут без `api.scope:*` недоступен (default deny, `declaresScope`, ~строка 96); `X-Club-Id` чужого клуба → `tenant_override_forbidden`; кэш метаданных ключа 300 с.
- [ ] `api.scope:<scope>` — права ключа (`config/integrations.php` → `scopes`): `products:read`, `products:write`, `stock:write`, `sales:read`, `computers:read`, `bookings:write`, `computers:control` (опасное), `tariffs:read`, `members:read` (опасное, персональные данные); `api.scope:any` — любой ключ.
- [ ] `api.idempotent[:required]` — заголовок `Idempotency-Key` (`^[A-Za-z0-9_:\-]{1,100}$`), хранение ответа 24 ч в `api_idempotency_keys`, повтор с другим телом → 422 `idempotency_key_reused`, параллельный → 409 `idempotency_in_progress`, без заголовка при `required` → 428; ответы ≥ 500 не сохраняются (`ApiIdempotency.php`).
- [ ] `api.limit` — фиксированное окно 1 мин: на ключ `rate_limit_per_minute` (по умолчанию 120, максимум 1000, env `API_KEY_RATE_LIMIT`, `API_KEY_RATE_LIMIT_MAX`) и на клуб 600 (`API_CLUB_RATE_LIMIT`); заголовки `X-RateLimit-*`, `Retry-After` (`ApiRateLimit.php`).

### 2. `routes/api.php` (49 маршрутов)

Сокращения: **SCS** = `auth:sanctum, current-club, subscription`; **AD** = `agent.device`.
«Новый RPC» — куда переходит в `proto/agent/v1` (подробная таблица — `docs/agent-protocol.md`).

#### 2.1. Установка, вход, профиль аккаунта

- [ ] `POST /api/clubs/resolve` — `api.clubs.resolve` — `Api\ClubInstallationController` (`__invoke`) — `throttle:club-resolve` — по ключу установки (64 символа) возвращает `club_id`, `club_name`, `api_base_url`, `login_endpoint` (`App\Services\ClubInstallationKey::resolve`). Вызывает: агент `ZafiroApiClient.ResolveClubAsync` (`WindowsAgent/ZafiroAgent/Services/ZafiroApiClient.cs`, строки 95–114) из `ClubSetupService.cs` (строки 80, 136). Новый: `EnrollmentService.Enroll` (одноразовый код). (`routes/api.php`, строка 29)
- [ ] `GET /api/me` — `api.me` — `Api\AuthController@summary` — SCS — профиль и балансы гостя (`user_id, name, phone, club_id, balance, bonus_balance, currency`; суммы float). Вызывает: агент `HttpApiService.GetSessionInfoAsync` (`Services/HttpApiService.cs`, строки 34–45) из `MainWindow.xaml.cs` (строки 197, 810) — обновление баланса не чаще раза в 20 с (`BalanceRefreshInterval`, строка 63). Новый: `GuestService.GetProfile` + команда `BalanceChanged`. (строка 33)
- [ ] `POST /api/agent/user/login` — `api.agent.user.login` — `Api\AuthController@agentUserLogin` — `throttle:customer-login` — вход гостя на ПК по телефону/паролю + `club_key`/`club_id`; один ПК на клиента (409 `signed_in_elsewhere`, `App\Services\AgentSignIns`), повторный вход на ПК удаляет прежний токен с тем же `device_name`; ответ `access_token` (30 дней), `club_id`, `club_name`, `user`. Вызывает: агент `ZafiroApiClient.LoginAsync` (строки 116–155); если ключ клуба хранит служба — запрос идёт через службу (`VaultServer.RelayAsync`, ключ подставляется в тело). Новый: `GuestAuthService.Login`. (строка 37)
- [ ] `POST /api/auth/login` — `api.auth.login` — `Api\AuthController@login` — `throttle:customer-login` — то же для мобильных приложений: дополнительно `token` и список клубов `clubs`. Вызывают: `mobileclient/lib/api/api_client.dart`, `mobileemployee/lib/api/api_client.dart`; агент — только как запасной путь, если `/agent/user/login` вернул 404 (`ZafiroApiClient.cs`, строки 132–133). Новый: не переносится в контракт агента (мобилки отложены). (строка 42)
- [ ] `POST /api/auth/password/code` — `api.auth.password.code` — `Api\PasswordRecoveryController@code` — `throttle:password-recovery` — код в Telegram (Telegram Gateway, `App\Services\PasswordRecovery::sendCode`; 5 кодов/час, повтор через 60 с, код 5 мин). Вызывают: агент `ClubSetupService.cs` (строка 374, `PostPublicAsync`), `mobileemployee`. Новый: `GuestAuthService.RequestPasswordResetCode`. (строка 46)
- [ ] `POST /api/auth/password/reset` — `api.auth.password.reset` — `Api\PasswordRecoveryController@reset` — `throttle:password-recovery` — новый пароль по 6-значному коду (`PasswordRecovery::reset`, 5 попыток). Вызывают: агент `ClubSetupService.cs` (строка 380), `mobileemployee`. Новый: `GuestAuthService.ResetPassword`. (строка 49)
- [ ] `GET /api/auth/me` — `api.auth.me` — `Api\AuthController@me` — SCS — пользователь, роль, скидка, балансы. Вызывает: `mobileclient`. Новый: (мобилки отложены). (строка 55)
- [ ] `POST /api/auth/logout` — `api.auth.logout` — `Api\AuthController@logout` — `auth:sanctum, current-club` — удаляет текущий токен, пишет `auth.api_logout` в журнал. Вызывают: агент `ClubSetupService.cs` (строка 233, `SendAuthorizedAsync`), `mobileclient`, `mobileemployee`. Новый: `GuestAuthService.Logout`. (строка 57)
- [ ] `GET /api/auth/clubs` — `api.auth.clubs` — `Api\AuthController@clubs` — `auth:sanctum, current-club, throttle:30,1,api.auth.clubs` (без проверки подписки) — клубы аккаунта. Вызывает: `mobileclient`. Новый: — (мобилки). (строка 60)
- [ ] `POST /api/auth/switch-club` — `api.auth.switch-club` — `Api\AuthController@switchClub` — `auth:sanctum, current-club, throttle:10,1,api.auth.switch-club` — новый токен другого клуба. Вызывают: `mobileclient`, `mobileemployee`. Новый: — (мобилки). (строка 61)

#### 2.2. ПК: heartbeat, регистрация, токен устройства

- [ ] `POST /api/club-computers/{computer}/heartbeat` — `api.club-computers.heartbeat` — `ClubComputerController@heartbeat` — SCS, `throttle:club-computers` — heartbeat токеном пользователя (агенты до 1.1); если у ПК есть токен устройства → 403 `use_device_token`; клиент может сообщать только `state, session_id, session_ends_at, agent_version` (`app/Http/Controllers/ClubComputerController.php`, строки 274–292). Вызывает: агент `ZafiroApiClient.SendHeartbeatAsync` без токена устройства (строки 512–560). Новый: `AgentService.Connect` (Status). (строка 65)
- [ ] `POST /api/agent/heartbeat` — `api.agent.heartbeat` — `ClubComputerController@agentHeartbeat` — AD, `throttle:club-computers` — главный heartbeat: `ClubComputerSessions::heartbeat`; при `commands=true` выдаёт `power_command` (`restart|shutdown|service`, `ComputerPower::take`), `wake_macs`, `staff_message`, `staff_minutes`; при `can_update=true` — `agent_update {version,url,sha256}` (`AgentUpdate::offerFor`); при Bearer-токене гостя — `customer {balance, bonus_balance, friend_requests, chat_last_staff_id}` (`customerState`, строки 335–355); блок `club` (название, лого, языки, `after_session`, `guest_features`, `idle_logout_minutes`), `session`, `session_expired`, `server_time`, `recommended_interval_seconds` (строки 301–333, 434–491). Вызывают: агент `HeartbeatService` (через `SendHeartbeatAsync` с токеном), служба `VaultServer.KeepOnlineAsync`/`CheckAsync`/`ProbeAsync` (`Service/VaultServer.cs`, строки 294–443), экран входа `ClubSetupService.CheckGuestSessionAsync` (строки 318–335) и `ReportFreshBootAsync` (строки 390–410). Новый: `AgentService.Connect` (Hello/Status/Welcome/команды). (строка 70)
- [ ] `POST /api/agent/token/rotate` — `api.agent.token.rotate` — `ClubComputerController@rotateAgentToken` — AD, `throttle:agent-token-rotate` — выдаёт новый `agent_token`, старый перестаёт работать (`ClubComputerSessions::issueAgentToken`). Вызывает: служба `VaultServer.TryRotateAsync` (строки 268–290) сразу после сохранения токена. Новый: `EnrollmentService.RenewCertificate`. (строка 75)
- [ ] `GET /api/agent/translations/{locale}` — `api.agent.translations` — `Api\AgentTranslationController` — `throttle:agent-translations`, `locale` = `[a-z]{2,8}`, без аутентификации — строки группы `agent` с правками суперадмина + `version`, `Cache-Control: public, max-age=300` (`App\Services\TranslationCatalog`). Вызывает: `ZafiroApiClient.GetAgentTranslationsAsync` (строки 63–79) из `MainWindow.xaml.cs` (строка 643), когда `translations_version` в heartbeat новее. Новый: `AgentService.GetTranslations` + команда `TranslationsChanged`. (строка 80)
- [ ] `POST /api/agent/enroll` — `api.agent.enroll` — `Api\AgentEnrollController` — `throttle:agent-enroll` — ключ клуба + `device_id` + MAC → токен ПК; неизвестный ПК добавляется в клуб в зону «Не назначена» от имени владельца (`ClubComputerRegistrar::find/register/adoptMac`), лимит тарифа → 422 `computer_not_added`. Вызывает: служба `VaultServer.EnrollAsync` (строки 358–399), повтор через 10 мин (1 мин после 429/5xx). Новый: `EnrollmentService.Enroll`. (строка 86)
- [ ] `POST /api/club-computers/resolve` — `api.club-computers.resolve` — `ClubComputerController@resolve` — SCS, `throttle:club-computers` — найти ПК по MAC/имени/UUID (`ClubComputerResolver::resolve`); сотруднику с `claim=true` — `agent_token` + «визит сотрудника» (`startStaffVisit`, журнал `agent.staff_signin`). 404 `computer_not_found`. Вызывают: `ClubSetupService.cs` (строка 191), `HeartbeatService.cs` (строка 339). Новый: `EnrollmentService.Enroll` (код вместо входа сотрудника). (строка 112)
- [ ] `POST /api/club-computers/register` — `api.club-computers.register` — `ClubComputerController@register` — SCS, `throttle:club-computer-registration` — сотрудник (роли owner/admin/manager/operator, `ClubComputerPolicy::register`) регистрирует ПК (`ClubComputerRegistrar::register`), 201/200 + `agent_token`. Вызывают: `ClubSetupService.cs` (строка 193), `HeartbeatService.cs` (строка 344). Новый: `EnrollmentService.Enroll`. (строка 116)

#### 2.3. Агент: QR-вход, регистрация гостя, PIN, сессия

- [ ] `POST /api/agent/qr-login` — `api.agent.qr-login.store` — `Api\AgentQrLoginController@store` — AD, `throttle:agent-qr-login` — QR-код входа (функция `qr_login` клуба, иначе 403 `qr_disabled`): `id`, `url` (`/pc-login/{code}`), `expires_at` (5 мин), `poll_interval_seconds: 3` (`App\Services\QrLogins::create`). Вызывает: `ClubSetupService.cs` (строка 275) из `Views/ClubSetupWindow.xaml.cs` (строка 885). Новый: `GuestAuthService.CreateQrLogin`. (строка 91)
- [ ] `POST /api/agent/qr-login/poll` — `api.agent.qr-login.poll` — `Api\AgentQrLoginController@poll` — AD, `throttle:agent-qr-poll` — статус `pending|expired|approved` (+ токен гостя, `QrLogins::poll`). Вызывает: `ClubSetupService.cs` (строка 296) из `ClubSetupWindow.xaml.cs` (строка 904), каждые 3 с. Новый: `GuestAuthService.WatchQrLogin` (server-streaming). (строка 94)
- [ ] `POST /api/agent/register` — `api.agent.register` — `Api\AgentRegisterController` — AD, `throttle:agent-register` — регистрация клиента на ПК (имя, телефон +7…, пароль ≥ 6); существующий аккаунт Zafiro вступает в клуб со своим паролем; создаёт `club_memberships` (client), `member_balances` (0/0), токен 30 дней. Вызывает: `ClubSetupService.cs` (строка 357). Новый: `GuestAuthService.Register`. (строка 99)
- [ ] `POST /api/agent/exit-pin` — `api.agent.exit-pin` — `Api\AgentExitPinController` — AD, `throttle:agent-exit-pin` — проверка PIN выхода сотрудника (`clubs.agent_exit_pin_hash`), 409 `pin_not_configured`, 422 `invalid_pin`; при успехе «визит сотрудника» `startStaffVisit(..., 'pin')` → `staff_minutes`; журнал `agent.staff_exit`/`agent.exit_pin_failed`. Вызывает: `ZafiroApiClient.VerifyExitPinAsync` (строки 169–202) из `App.xaml.cs` (строка 236). Новый: `AgentService.VerifyStaffPin`. (строка 103)
- [ ] `POST /api/club-computers/{computer}/session/end` — `api.club-computers.session.end` — `ClubComputerController@endSession` — SCS, `throttle:club-computers`, `computer` числовой — досрочное завершение сессии владельцем или сотрудником (`ClubComputerSessions::end` → `SessionSettlement`), ответ `refunded`, `refunded_bonus` (строки), `kept_minutes`. Вызывает: `HttpApiService.EndSessionWithResultAsync` (строки 161–195) из `MainWindow.xaml.cs` (строка 853). Новый: `GuestService.EndSession` (+ команда `EndSession`). (строка 107)

#### 2.4. Товары, тарифы, функции гостя

- [ ] `GET /api/products` — `api.products.index` — `Api\ProductController` — SCS, `throttle:60,1,api.products.index` — товары клуба по имени, пагинация 50 (`ProductResource`, цена float). Вызывает: `HttpApiService.GetProductsAsync` (строки 91–112) из `MainWindow.xaml.cs` (строка 1194). Новый: `ShopService.ListProducts`. Расхождение: `docs/integration-api.md` (раздел 1) пишет «без пагинации», а код использует `paginate(50)`. (строка 120)
- [ ] `GET /api/tariffs` — `api.tariffs.index` — `Api\AgentTariffController@index` — SCS, `throttle:60,1,api.tariffs.index` — тарифы зоны ПК (`?computer_id`), иначе зоны ПК текущей сессии (`AgentTariffPurchaseService::availableFor`). Вызывает: `HttpApiService.GetTariffsAsync` (строки 65–89) из `MainWindow.xaml.cs` (строка 833). Новый: `GuestService.ListTariffs`. (строка 125)
- [ ] `POST /api/tariffs/{tariff}/purchase` — `api.tariffs.purchase` — `Api\AgentTariffController@purchase` — SCS, `throttle:10,1,api.tariffs.purchase` — покупка тарифа на ПК (`computer_id`, `balance` main|bonus); если у ПК есть токен устройства, нужен совпадающий `X-Agent-Token`, иначе 403 `wrong_computer` (`AgentTariffPurchaseService::purchase`). Вызывает: `HttpApiService.PurchaseTariffAsync` (строки 119–151) из `MainWindow.xaml.cs` (строка 1211). Новый: `GuestService.PurchaseTariff` (с обязательным `idempotency_key`). (строка 128)
- [ ] `GET /api/guest/friends` — `api.guest.friends.index` — `Api\GuestFriendController@index` — SCS, `throttle:60,1,…` — друзья/входящие/исходящие + `features` (`App\Services\Friends::overview`). Вызывают: агент `MainWindow.xaml.cs` (строка 993, `CallGuestApiAsync`), `mobileclient`. Новый: `SocialService.ListFriends`. (строка 134)
- [ ] `POST /api/guest/friends` — `api.guest.friends.store` — `GuestFriendController@store` — SCS, `throttle:10,1,…` — заявка по телефону (`Friends::request`), функция `friends`. Вызывают: агент (строка 998), `mobileclient`. Новый: `SocialService.AddFriend`. (строка 135)
- [ ] `POST /api/guest/friends/{friendship}/accept` — `api.guest.friends.accept` — `GuestFriendController@accept` — SCS, `throttle:30,1,…`. Вызывают: агент (строка 1003), `mobileclient`. Новый: `SocialService.AcceptFriend`. (строка 136)
- [ ] `POST /api/guest/friends/{friendship}/remove` — `api.guest.friends.remove` — `GuestFriendController@remove` — SCS, `throttle:30,1,…`. Вызывают: агент (строка 1003), `mobileclient`. Новый: `SocialService.RemoveFriend`. (строка 137)
- [ ] `POST /api/guest/friends/{friendship}/transfer` — `api.guest.friends.transfer` — `GuestFriendController@transfer` — SCS, `throttle:10,1,…` — перевод с паролем аккаунта (`Friends::transfer`, лимит 1 000 000 за перевод, `MAX_TRANSFER_CENTS`), функция `balance_transfer`. Вызывает: агент (строка 1012). Новый: `SocialService.TransferBalance` (password). (строка 138)
- [ ] `POST /api/guest/friends/{friendship}/transfer/code` — `api.guest.friends.transfer.code` — `GuestFriendController@transferCode` — SCS, `throttle:5,1,…` — код подтверждения в Telegram (`FriendTransferCodes::send`). Вызывает: `mobileclient`. Новый: `SocialService.RequestTransferCode`. (строка 140)
- [ ] `POST /api/guest/friends/{friendship}/transfer/confirm` — `api.guest.friends.transfer.confirm` — `GuestFriendController@transferConfirm` — SCS, `throttle:10,1,…` — перевод по коду (`FriendTransferCodes::confirm`). Вызывает: `mobileclient`. Новый: `SocialService.TransferBalance` (telegram_code). (строка 141)
- [ ] `GET /api/guest/minutes` — `api.guest.minutes` — `Api\GuestMinutesController@show` — SCS, `throttle:60,1,…` — цена часа зоны ПК, бонусы, смешанная оплата, весь баланс, автобиллинг (`PaymentRules`, `AgentTariffPurchaseService::hourlyPrice`). Вызывает: агент (строка 1047). Новый: `GuestService.GetMinuteOffer`. (строка 143)
- [ ] `POST /api/guest/minutes/purchase` — `api.guest.minutes.purchase` — `GuestMinutesController@purchase` — SCS, `throttle:10,1,…` — покупка 1..720 минут (`purchaseMinutes`), проверка `X-Agent-Token` как у тарифа. Вызывает: агент (строка 1059; и автобиллинг из `Frontend/main.js`, `purchaseMinutes`). Новый: `GuestService.PurchaseMinutes`. (строка 144)
- [ ] `GET /api/guest/packages` — `api.guest.packages` — `Api\GuestPackageController@index` — SCS, `throttle:60,1,…` — сохранённые пакеты (`packagesFor`). Вызывает: агент (строка 1085). Новый: `GuestService.ListPackages`. (строка 146)
- [ ] `POST /api/guest/packages/{package}/start` — `api.guest.packages.start` — `GuestPackageController@start` — SCS, `throttle:10,1,…` — запуск пакета на ПК (`startPackage`), проверка `X-Agent-Token`. Вызывает: агент (строка 1096). Новый: `GuestService.StartPackage`. (строка 147)
- [ ] `GET /api/guest/chat` — `api.guest.chat` — `Api\GuestChatController@index` — SCS, `throttle:60,1,…` — последние 100 сообщений (или `after`), помечает ответы сотрудников прочитанными. Вызывает: агент (строка 1032) — опрос каждые 5 с при открытом чате, иначе раз в 30 с без счётчиков сервера (`Frontend/main.js`, строки 1203–1206). Новый: `SocialService.ListChatMessages` + команда `ChatMessageReceived`. (строка 148)
- [ ] `POST /api/guest/chat` — `api.guest.chat.store` — `GuestChatController@store` — SCS, `throttle:20,1,…` — сообщение клубу (≤ 1000, `ChatMessage::MAX_LENGTH`). Вызывает: агент (строка 1039). Новый: `SocialService.SendChatMessage`. (строка 149)
- [ ] `GET /api/guest/telegram` — `api.guest.telegram` — `Api\GuestTelegramController@show` — SCS, `throttle:30,1,…` — заглушка `{available:false, linked:false}` для агентов до 0.0.57. Вызывающих в текущем коде нет. Новый: не переносится. (строка 150)
- [ ] `GET /api/guest/news` — `api.guest.news` — `Api\GuestNewsController` — SCS, `throttle:30,1,…` — до 10 новостей платформы и 10 новостей клуба. Вызывают: агент (строка 1043; `Frontend/main.js` раз в 10 мин, строка 1162), `mobileclient`. Новый: `GuestService.ListNews`. (строка 151)
- [ ] `POST /api/guest/shop/purchase` — `api.guest.shop.purchase` — `Api\GuestShopController@purchase` — SCS, `throttle:10,1,…` — заказ товаров к ПК с основного баланса (`App\Services\AgentShop::purchase`; ≤ 30 строк, ≤ 50 шт.), проверка `X-Agent-Token`, 403 `shop_disabled`. Вызывает: агент (строка 1149). Новый: `ShopService.PlaceOrder`. (строка 152)
- [ ] `POST /api/guest/profile/name` — `api.guest.profile.name` — `Api\GuestProfileController@name` — SCS, `throttle:10,1,…`. Вызывает: агент (строка 1018). Новый: `GuestService.UpdateName`. (строка 154)
- [ ] `POST /api/guest/profile/password` — `api.guest.profile.password` — `GuestProfileController@password` — SCS, `throttle:5,1,…` — смена пароля с текущим, остальные токены удаляются. Вызывает: агент (строка 1027). Новый: `GuestService.ChangePassword`. (строка 155)
- [ ] `GET /api/guest/history` — `api.guest.history` — `Api\GuestHistoryController` — SCS, `throttle:60,1,…` — журнал гостя (`App\Services\GuestHistory::page`, 30 на страницу, фильтры purchases/top_ups/transfers/visits). Вызывают: агент (строка 1129), `mobileclient`. Новый: `GuestService.ListHistory`. (строка 156)
- [ ] `GET /api/guest/seats` — `api.guest.seats.index` — `Api\GuestSeatController@index` — SCS, `throttle:30,1,…` — карта для пересадки (`SeatTransfers::map`), функция `self_transfer`. Вызывает: агент (строка 1158). Новый: `GuestService.GetSeatMap`. (строка 157)
- [ ] `POST /api/guest/seat-transfer` — `api.guest.seat-transfer` — `GuestSeatController@store` — SCS, `throttle:10,1,…` — пересадка (`SeatTransfers::transfer`), проверка `X-Agent-Token`. Вызывает: агент (строка 1163). Новый: `GuestService.TransferSeat`. (строка 158)
- [ ] `GET /api/guest/qr-login/{code}` — `api.guest.qr-login.show` — `Api\GuestQrLoginController@show` — SCS, `throttle:30,1,…`, `code` = 40 символов — что подтверждается (ПК, клуб); 410 `qr_expired`, 403 `other_club`. Вызывает: `mobileclient`. Новый: — (мобилки; в вебе — `/pc-login/{code}`). (строка 160)
- [ ] `POST /api/guest/qr-login/{code}` — `api.guest.qr-login.approve` — `GuestQrLoginController@approve` — SCS, `throttle:10,1,…` — подтверждение входа на ПК с телефона (`QrLogins::approve`). Вызывает: `mobileclient`. Новый: — (мобилки); сервер шлёт агенту `QrLoginApproved`. (строка 161)
- [ ] `GET /api/settings/{section}/{key}` — `api.settings.show` — `SettingsController@showSetting` — SCS, `throttle:60,1,api.settings.show` — значение одной настройки клуба любой роли (и `ip` = host). Вызывающих в агенте и мобилках не найдено; покрыт `tests/Feature/SettingsTest.php`. Новый: не переносится (см. open-questions). (строка 164)

### 3. `routes/api_staff.php` (24 маршрута, мобильное приложение сотрудника)

Общие middleware группы (`routes/api.php`, строки 174–177): SCS + `club-role:owner,admin,manager,cashier,operator` (`App\Support\ClubRoles::STAFF`) + `throttle:staff-app` (240/мин на сотрудника). Описание контракта — `docs/staff-api.md`. Все маршруты вызывает только `mobileemployee/lib/api/api_client.dart`. Мобилки отложены — в контракт агента не входят; при переносе — REST по OpenAPI (решение владельца, см. open-questions).

- [ ] `GET /api/staff/me` — `api.staff.me` — `Api\Staff\MeController@show` — профиль, права, функции, уведомления, смена (`ClubNotifications`, `CashRegisterService`). (строка 22)
- [ ] `GET /api/staff/computers` — `api.staff.computers.index` — `Api\Staff\ComputerController@index` — карта ПК с бронями и версиями агента (`ComputerReservations`, `AgentUpdate`). (строка 25)
- [ ] `POST /api/staff/computers/control` — `api.staff.computers.control` — `ClubComputerController@control` — `club-can:pc_control.update` — бронь/включение/перезагрузка/выключение (`ComputerPower`, вебхук `computer.controlled`). (строка 26)
- [ ] `POST /api/staff/computers/{computer}/end-session` — `api.staff.computers.end-session` — `ClubComputerController@endSession` — `club-role:owner,admin,manager,operator` (`ClubComputerSessions::STAFF_ROLES`). (строки 27–28)
- [ ] `POST /api/staff/computers/guest-session` — `api.staff.computers.guest-session` — `GuestSessionController@store` — `club-can:cash.create`, `throttle:30,1,api.staff.guest-session` — сессия без аккаунта с кассы (`App\Services\GuestSessions`). (строки 29–30)
- [ ] `GET /api/staff/cash` — `api.staff.cash.show` — `Api\Staff\CashController@show` — `club-can:cash.view`. (строка 34)
- [ ] `POST /api/staff/cash/shifts` — `api.staff.cash.shifts.open` — `CashController@open` — `club-can:cash.view` + `club-can:cash.create`. (строка 36)
- [ ] `POST /api/staff/cash/shifts/{shift}/close` — `api.staff.cash.shifts.close` — `CashController@close` — то же; Z-отчёт (`ShiftReport`). (строка 37)
- [ ] `POST /api/staff/cash/top-ups` — `api.staff.cash.top-ups.store` — `CashController@topUp` — то же; пополнение с бонусом (`TopUpBonus`). (строка 38)
- [ ] `POST /api/staff/cash/sales` — `api.staff.cash.sales.store` — `CashController@sale` — то же; продажа товаров. (строка 39)
- [ ] `POST /api/staff/cash/transactions` — `api.staff.cash.transactions.store` — `CashController@transact` — то же; приход/расход кассы. (строка 40)
- [ ] `GET /api/staff/customers` — `api.staff.customers.index` — `Api\Staff\CustomerController@index` — поиск клиентов (`LoyaltyProgram`). (строка 45)
- [ ] `GET /api/staff/products` — `api.staff.products.index` — `Api\Staff\ProductController@index`. (строка 46)
- [ ] `POST /api/staff/stock/receive` — `api.staff.stock.receive` — `Api\Staff\ProductController@receive` — `club-can:stock.create` (`AdjustStockAction`). (строка 47)
- [ ] `POST /api/staff/stock/movements` — `api.staff.stock.movements.store` — `Api\Staff\ProductController@move` — `club-can:warehouse.update`. (строка 48)
- [ ] `GET /api/staff/orders` — `api.staff.orders.index` — `Api\Staff\OrderController@index` — заказы из агента. (строка 51)
- [ ] `POST /api/staff/orders/{order}/done` — `api.staff.orders.done` — `OrderController@done` — `club-can:orders.update` (`AgentShop`). (строка 52)
- [ ] `POST /api/staff/orders/{order}/cancel` — `api.staff.orders.cancel` — `OrderController@cancel` — `club-can:orders.delete` (возврат денег и остатка, `AgentShop`). (строка 53)
- [ ] `GET /api/staff/chats` — `api.staff.chats.index` — `ChatController@conversationsJson`. (строка 56)
- [ ] `GET /api/staff/chat/poll` — `api.staff.chat.poll` — `ChatController@poll` — опрос чата. (строка 57)
- [ ] `POST /api/staff/chat/{customer}` — `api.staff.chat.reply` — `ChatController@reply` — `club-can:chat.create`, `throttle:60,1,api.staff.chat.reply`; отвечает сотрудник с правом `chat.create` (по умолчанию cashier/operator, владелец — всегда; проверка `ChatController::answers()` → `ClubPermissions::allows`, константа `ChatMessage::ANSWERING_ROLES` в проверке не используется). (строка 58)
- [ ] `GET /api/staff/notifications` — `api.staff.notifications.index` — `NotificationController@index`. (строка 61)
- [ ] `POST /api/staff/notifications/seen` — `api.staff.notifications.seen` — `NotificationController@markSeen`. (строка 62)
- [ ] `GET /api/staff/reports/today` — `api.staff.reports.today` — `Api\Staff\MeController@today` — `club-role:owner,admin,manager` (`StaffPermissions::REPORTS`). (строка 63)

### 4. `routes/api_v1.php` (17 маршрутов, интеграционный API)

Общие middleware: `api.log, api.size, api.key, api.limit` (`routes/api.php`, строки 180–183). Вызывают внешние системы клуба по ключам API (создаются в «Настройки → API и интеграции», `docs/integration-api.md`). В репозитории вызывающих нет. Форма ошибок — `ApiError` (`{error: {code, message, required_scopes?}}`, коды перечислены в `resources/api/openapi-v1.yaml`, строка 476).

- [ ] `GET /api/v1/me` — `api.v1.me` — `Api\V1\MeController` — `api.scope:any` — ключ, клуб, права. (строка 19)
- [ ] `GET /api/v1/products` — `api.v1.products.index` — `Api\V1\ProductController@index` — `api.scope:products:read` (курсорная пагинация). (строка 23)
- [ ] `GET /api/v1/products/{product}` — `api.v1.products.show` — `ProductController@show` — `products:read`. (строка 24)
- [ ] `GET /api/v1/product-categories` — `api.v1.product-categories.index` — `ProductController@categories` — `products:read`. (строка 25)
- [ ] `GET /api/v1/stock-movements` — `api.v1.stock-movements.index` — `Api\V1\StockMovementController@index` — `products:read`. (строка 26)
- [ ] `POST /api/v1/products` — `api.v1.products.store` — `ProductController@store` — `products:write`, `api.idempotent` (необязательный ключ); вебхук `product.created`. (строка 29)
- [ ] `PATCH /api/v1/products/{product}` — `api.v1.products.update` — `ProductController@update` — `products:write`, `api.idempotent`; вебхук `product.updated`. (строка 30)
- [ ] `POST /api/v1/stock-movements` — `api.v1.stock-movements.store` — `StockMovementController@store` — `stock:write`, `api.idempotent:required`; вебхук `stock.changed`. (строки 32–34)
- [ ] `GET /api/v1/sales` — `api.v1.sales.index` — `Api\V1\SaleController@index` — `sales:read`. (строка 38)
- [ ] `GET /api/v1/sales/{sale}` — `api.v1.sales.show` — `SaleController@show` — `sales:read`. (строка 39)
- [ ] `GET /api/v1/zones` — `api.v1.zones.index` — `Api\V1\ComputerController@zones` — `computers:read`. (строка 44)
- [ ] `GET /api/v1/computers` — `api.v1.computers.index` — `ComputerController@index` — `computers:read`. (строка 45)
- [ ] `GET /api/v1/computers/{computer}` — `api.v1.computers.show` — `ComputerController@show` — `computers:read`. (строка 46)
- [ ] `PUT /api/v1/computers/{computer}/reservation` — `api.v1.computers.reservation` — `ComputerController@reservation` — `bookings:write`, `api.idempotent`; вебхук `computer.reservation_changed`. (строки 48–51)
- [ ] `POST /api/v1/computers/control` — `api.v1.computers.control` — `ComputerController@control` — `computers:control`, `api.idempotent:required`; до 50 ПК (`max_bulk_items`), `ComputerPower`, вебхук `computer.controlled`. (строки 52–55)
- [ ] `GET /api/v1/tariffs` — `api.v1.tariffs.index` — `Api\V1\TariffController@index` — `tariffs:read`. (строка 57)
- [ ] `GET /api/v1/members` — `api.v1.members.index` — `Api\V1\MemberController@index` — `members:read` (персональные данные). (строка 58)

#### 4.1. Сверка с `resources/api/openapi-v1.yaml` и `docs/integration-api.md`

- [ ] OpenAPI описывает те же 17 операций (пути строки 86–428 файла): `/me`, `/products` (GET, POST), `/products/{product}` (GET, PATCH), `/product-categories`, `/stock-movements` (GET, POST), `/sales`, `/sales/{sale}`, `/zones`, `/computers`, `/computers/{computer}`, `/computers/{computer}/reservation` (PUT), `/computers/control` (POST), `/tariffs`, `/members`. Расхождений по набору маршрутов нет.
- [ ] Права в OpenAPI указаны текстом в `description` («Scope: `products:write`»), а не машиночитаемо (`security`/`x-scopes`) — при переносе на oapi-codegen стоит добавить `x-required-scopes`, чтобы middleware генерировался из спецификации.
- [ ] Идемпотентность в OpenAPI: `idempotencyKey` / `idempotencyKeyRequired` (строки 464–465) совпадают с `api.idempotent` / `api.idempotent:required` в маршрутах; ответ 428 — для `stock-movements` POST и `computers/control`.
- [ ] Вебхуки (`config/integrations.php` → `webhook_events`): `product.created`, `product.updated`, `stock.changed`, `sale.completed`, `computer.reservation_changed`, `computer.controlled`; доставка — `WebhookDispatcher` в транзакции + задача `DeliverWebhook`, повторы 10 с/60 с/5 мин/30 мин/2 ч, отключение после 20 ошибок подряд, HTTPS и запрет приватных сетей (`docs/integration-api.md`, раздел 2 «Вебхуки»). В OpenAPI вебхуки не описаны (нет `webhooks:`).
- [ ] `docs/integration-api.md`, раздел 1: таблица аудита старых маршрутов неполная (нет `/api/agent/*`, `/api/guest/*`), а про `/api/products` сказано «без пагинации», хотя `Api\ProductController` использует `paginate(50)`.

### 5. Таблица «метод агента → маршрут»

| Метод (файл) | HTTP | Маршрут | Токены |
|---|---|---|---|
| `ZafiroApiClient.GetAgentTranslationsAsync` (`Services/ZafiroApiClient.cs`) | GET | `/api/agent/translations/{locale}` | — |
| `ZafiroApiClient.ResolveClubAsync` | POST | `/api/clubs/resolve` | — (ключ клуба в теле) |
| `ZafiroApiClient.LoginAsync` | POST | `/api/agent/user/login` (запасной — `login_endpoint` = `/api/auth/login`) | — / через службу (ключ клуба подставляет служба) |
| `ZafiroApiClient.ReadQrLogin` | — | разбор ответа `/api/agent/qr-login/poll` | — |
| `ZafiroApiClient.VerifyExitPinAsync` | POST | `/api/agent/exit-pin` | X-Agent-Token (+ Bearer гостя для журнала) |
| `ZafiroApiClient.PostWithDeviceTokenAsync` | POST | `/api/agent/qr-login`, `/api/agent/qr-login/poll`, `/api/agent/register`, `/api/agent/heartbeat` (экран входа) | X-Agent-Token, X-Agent-Device |
| `ZafiroApiClient.PostPublicAsync` | POST | `/api/auth/password/code`, `/api/auth/password/reset` | — |
| `ZafiroApiClient.SendAuthorizedAsync` | любой | `auth/logout` (из `ClubSetupService`), `club-computers/*` | Bearer (+ X-Agent-Token) |
| `ZafiroApiClient.ResolveComputerAsync` | POST | `/api/club-computers/resolve` | Bearer сотрудника/гостя |
| `ZafiroApiClient.RegisterComputerAsync` | POST | `/api/club-computers/register` | Bearer сотрудника |
| `ZafiroApiClient.SendHeartbeatAsync` | POST | `/api/agent/heartbeat` (с токеном ПК) или `/api/club-computers/{id}/heartbeat` | X-Agent-Token + Bearer гостя / Bearer |
| `HttpApiService.GetSessionInfoAsync` (`Services/HttpApiService.cs`) | GET | `/api/me` | Bearer гостя |
| `HttpApiService.GetTariffsAsync` | GET | `/api/tariffs?computer_id=` | Bearer гостя |
| `HttpApiService.GetProductsAsync` | GET | `/api/products` | Bearer гостя (если есть) |
| `HttpApiService.PurchaseTariffAsync` | POST | `/api/tariffs/{id}/purchase` | Bearer + X-Agent-Token |
| `HttpApiService.EndSessionWithResultAsync` | POST | `/api/club-computers/{id}/session/end` | Bearer гостя |
| `HttpApiService.CallGuestApiAsync` | GET/POST | `/api/guest/{friends, friends/{id}/accept|remove|transfer, profile/name, profile/password, chat, news, minutes, minutes/purchase, packages, packages/{id}/start, history, shop/purchase, seats, seat-transfer}` | Bearer гостя (+ X-Agent-Token для записи «с этого ПК») |
| `VaultServer.TryRotateAsync` (`Service/VaultServer.cs`) | POST | `/api/agent/token/rotate` | X-Agent-Token, X-Agent-Device |
| `VaultServer.EnrollAsync` | POST | `/api/agent/enroll` | — (ключ клуба в теле) |
| `VaultServer.CheckAsync` / `ProbeAsync` / `KeepOnlineAsync` | POST | `/api/agent/heartbeat` (`{"can_update":true}` / `{}`) | X-Agent-Token, X-Agent-Device |
| `VaultServer.RelayAsync` | POST | белый список `VaultProtocol.RelayPath` (`Service/VaultProtocol.cs`, строка 52): `agent/heartbeat`, `agent/exit-pin`, `agent/user/login`, `agent/register`, `agent/qr-login`, `agent/qr-login/poll`, `guest/seat-transfer`, `guest/minutes/purchase`, `guest/shop/purchase`, `guest/chat`, `guest/packages/{id}/start`, `tariffs/{id}/purchase` | служба подставляет токен ПК / ключ клуба |

Фронтенд агента (`WindowsAgent/Frontend/main.js`) сам HTTP не вызывает: он шлёт `webview.postMessage({type: …})` (`requestTariffs`, `purchaseTariff`, `requestProducts`, `purchaseProducts`, `requestFriends`, `addFriend`, `acceptFriend`/`removeFriend`, `transferBalance`, `requestChat`, `sendChat`, `requestNews`, `requestMinutes`, `purchaseMinutes`, `requestPackages`, `startPackage`, `requestHistory`, `requestSeats`, `transferSeat`, `updateProfileName`, `changePassword`, `refreshBalance`, `requestSession`, `logout`, `setLanguage`, `returnToDesktop`, `requestGuestFeatures`), а `MainWindow.xaml.cs` (строки 880–1250) превращает их в вызовы `HttpApiService`.

### 6. Аутентификация агента сейчас

- [ ] **Ключ установки клуба** (64 символа `[A-Za-z0-9]`, `VaultProtocol.ClubKeyFormat`): вводит сотрудник при первой настройке; агент проверяет его через `/api/clubs/resolve`, служба хранит его (`VaultServer` op `club-key`, `DeviceVaultStore.SaveClubKey`) и подставляет в `/api/agent/user/login` и `/api/agent/enroll`. Ключ один на клуб и многоразовый — его утечка позволяет регистрировать ПК в клубе (ограничено лимитом тарифа и `agent-enroll`).
- [ ] **Подключение ПК сотрудником**: сотрудник входит в агенте (`/api/agent/user/login`), агент вызывает `/api/club-computers/resolve` (`claim=true`, пока токена нет), при 404 — `/api/club-computers/register`; сотрудник получает `agent_token` (`ClubComputerController::agentToken`, строки 493–505), при этом ПК помечается «в руках сотрудника» (`startStaffVisit`).
- [ ] **Токен устройства** `zag_<48>`: в БД только sha256 (`club_computers.agent_token_hash`, `agent_token_issued_at`); выдача нового отзывает прежний (`ClubComputerSessions::issueAgentToken`, ~строка 245). Агент передаёт его в службу (`op=store`), служба проверяет токен пустым heartbeat, убеждается, что прежний отозван, сохраняет и **сразу ротирует** (`/api/agent/token/rotate`), чтобы токен, побывавший в профиле клиента, перестал работать (`VaultServer.StoreAsync`, строки 214–247).
- [ ] **Хранение**: служба Windows «Zafiro Agent» (SYSTEM) — `C:\ProgramData\ZafiroAgent\device.json`, токен и ключ клуба под DPAPI учётной записи службы, ACL только SYSTEM/администраторы (`Service/DeviceVaultStore.cs`, строки 14–43, 141–154, 201). Агент знает только метку `DeviceTokens.VaultMarker`; запросы с токеном идут через именованный канал `ZafiroAgent.Vault` (`VaultProtocol.PipeName`), служба пересылает только на `https://zafiro.kz` и только пути из `RelayPath` (`VaultProtocol.IsAllowedRelayTarget`). Без службы токен хранится у агента в `%APPDATA%\ZafiroAgent\agent.config.json` под DPAPI пользователя (`Services/ConfigurationService.cs`, строка 16; `Security/SecureStorageService.cs`). `DeviceVaultClient` (`Services/DeviceVaultClient.cs`) — клиент канала; есть ли служба — по ключу реестра `HKLM\SYSTEM\CurrentControlSet\Services\ZafiroAgent`.
- [ ] **Токен гостя** (Sanctum, 30 дней, ability `club:<id>`, имя токена = имя ПК): хранится в `agent.config.json` (`ProtectedApiToken`, DPAPI пользователя); `HeartbeatService` прекращает работу при истечении (`HeartbeatService.cs`, строка 237).
- [ ] **Бездисковые/клонированные ПК**: токен из образа принадлежит ПК-эталону; `X-Agent-Device` + MAC не дают использовать его на клоне (401 `device_mismatch`); служба клона получает собственный токен через `/api/agent/enroll` по ключу клуба + UUID + MAC (неизвестный ПК добавляется автоматически) — `AgentEnrollController`, `VaultServer.EnrollAsync`, `WindowsAgent/README.md` (строки 82–98).
- [ ] Привязка покупок к ПК: для ПК с токеном устройства тариф/минуты/пакет/магазин/пересадка требуют совпадающий `X-Agent-Token` (403 `wrong_computer`) — `AgentTariffController::purchase`, `GuestMinutesController::purchase`, `GuestPackageController::start`, `GuestShopController::purchase`, `GuestSeatController::store`.

### 7. Heartbeat сейчас

- [ ] **Частота**: сервер рекомендует `recommended_interval_seconds` = `max(5, config('club.computer_heartbeat_interval_seconds', 30))` (`ClubComputerController::heartbeatResponse`, строка 438; `config/club.php`, строка 4, env `COMPUTER_HEARTBEAT_INTERVAL_SECONDS`). Агент ограничивает 5 с … 1 ч, по умолчанию 30 с; ошибки — backoff 10 с ×2 до 2 мин; 4xx — пауза 2 мин (`Services/HeartbeatService.cs`, строки 13–25, 84–98); при смене состояния — внеочередной heartbeat (семафор `_wake`, строки 102–115, 379–383).
- [ ] Дополнительно: служба шлёт пустой heartbeat каждые 60 с всегда (`Service/AgentService.cs`, строка 39; `VaultServer.KeepOnlineAsync`), экран входа — каждые 15 с (`Views/ClubSetupWindow.xaml.cs`, строки 47, 732; `ClubSetupService.CheckGuestSessionAsync`), после загрузки Windows — `fresh_boot` с повтором каждые 15 с до 10 мин (`ClubSetupService.ReportFreshBootAsync`). Итого ПК с вошедшим гостем шлёт ~3 heartbeat в минуту, на экране входа ~5 в минуту.
- [ ] **Что пишет в БД** (`app/Services/ClubComputerSessions.php`, `heartbeat()`, строки 45–65; `needsWrite()`, 72–85; `changes()`, 93–134): запись идёт, если `fresh_boot`, сессия должна закончиться, `last_seen_at` старше 20 с (`SEEN_WRITE_SECONDS`) или что-то изменилось; тогда транзакция с `SELECT … FOR UPDATE` на `club_computers`, `expireIfDue` (истёкшая сессия только очищается — поля сессии в NULL, без расчёта возврата; `ClubComputerSessions.php` ~177–186), `forceFill(last_seen_at, status='online', remote_ip, agent_state, сессия, agent_version, local_ip, cpu_model, cpu_cores, ram_total_mb, gpu_model, os_version, mac_address (только заполнение), device_name, hardware_reported_at)`. Так как интервал 30 с > 20 с, **практически каждый heartbeat основного цикла пишет в MySQL**.
- [ ] Каждый `agentHeartbeat` с `commands=true` ещё делает `ComputerPower::take` (UPDATE при наличии команды), `wakeTargets`, `takeMessage`; с токеном гостя — 3 запроса `customerState` (баланс, заявки, чат); ответ собирает настройки клуба (`GuestFeatures`, языки, `afterSessionAction`). Онлайн определяется по `last_seen_at` моложе 90 с (`ClubComputer::HEARTBEAT_TIMEOUT_SECONDS`).
- [ ] Новое: статус в памяти/Valkey, онлайн = открытый стрим, в Postgres — только изменения (`docs/agent-protocol.md`).

### 8. Polling в агенте (всё заменяется push по стриму)

- [ ] Heartbeat основного цикла — 30 с (команды питания/сообщения сотрудника доходят с задержкой до 30–60 с: «Уведомление появится на экране в течение минуты», `ClubComputerController::notify`). → `Command` в стриме.
- [ ] Heartbeat службы — 60 с; экран входа — 15 с. → один стрим службы.
- [ ] QR-вход — опрос каждые 3 с до 5 мин (`ClubSetupWindow.xaml.cs`, строки 885–905). → `WatchQrLogin` (server-streaming) / `QrLoginApproved`.
- [ ] Баланс — `/api/me` не чаще 20 с по событию (`MainWindow.xaml.cs`, строки 63, 193). → `BalanceChanged`.
- [ ] Чат — 5 с при открытом чате, иначе 30 с, если сервер не прислал счётчики (`Frontend/main.js`, строки 1197–1206). → `ChatMessageReceived` / `GuestCountersChanged`.
- [ ] Новости — 10 мин (`Frontend/main.js`, строка 1162). → оставить запрос по открытию экрана (без таймера).
- [ ] Локальные таймеры (сеть не трогают): автобиллинг 1 с, бездействие 5 с, время сессии 15 с (`main.js`, строки 558, 1046, 1289); `MainWindow` — таймер сессии 5 с, защита блокировки 1 с (строки 38, 62).
- [ ] Связь агент ↔ служба: «жив» каждые 5 с по каналу (`App.xaml.cs`, строка 145), служба проверяет раз в 3 с, таймаут 15 с (`Service/Watchdog.cs`, строки 29–35).

### 9. Агент: нагрузка (для будущего этапа)

- [ ] Сборка: .NET `net10.0-windows`, WPF + WebView2 `Microsoft.Web.WebView2 1.0.4258.31`, `System.Management` (WMI для железа) (`WindowsAgent/ZafiroAgent/ZafiroAgent.csproj`, строки 5–8, 31–34). Native AOT / trimming не включены (в csproj нет `PublishAot`/`PublishTrimmed`); WPF с Native AOT не поддерживается — служба (без WPF) может быть отдельным AOT-бинарником.
- [ ] Процессы: служба `ZafiroAgent` (SYSTEM: хранилище токена, relay, watchdog, обновления) + процесс агента в сеансе пользователя (WPF + WebView2 = несколько процессов `msedgewebview2.exe`).
- [ ] WebView2 создаётся в `MainWindow` (`EnsureCoreWebView2Async`, `MainWindow.xaml.cs`, строка 728) и при сворачивании окна (`Hide()`, строка 135) не освобождается — в коде не найдено `Dispose`/`TrySuspendAsync`/`MemoryUsageTargetLevel` (поиск по `MainWindow.xaml.cs`); браузер живёт всю сессию игры. Экран входа (`ClubSetupWindow`) — нативный WPF.
- [ ] Постоянная фоновая работа: таймеры UI 1 с (защита блокировки, автобиллинг в JS), 5 с (таймер сессии, бездействие, alive-пинг службе), heartbeat 30 с + 60 с службы, сбор железа через WMI (`Services/HardwareInfo.cs`, `WindowsHardwareProbe.cs`) с кэшем в `HardwareCollector`: железо — 6 ч, сеть (MAC/IP) — 30 с (`HardwareInfo.cs`, строки 159–164); железо отправляется в каждом heartbeat.
- [ ] Цель нового этапа (ТЗ, «Принятые решения» п. 5): служба отдельно от UI, gRPC-стрим вместо HTTP-опросов, WebView2 только когда показан экран блокировки/лаунчер (создавать по требованию и уничтожать/`TrySuspendAsync` при игре), Native AOT для службы. Измерений CPU/RAM в репозитории нет — метрика `ComputerLoad.agent_memory_mb` в новом `Status` нужна, чтобы их собрать.

## 30. База данных: инвентаризация схемы Zafiro (MySQL → Postgres)

Источник: все 86 миграций `../Zafiro/database/migrations` (с `0001_01_01_000000_create_users_table.php`
по `2026_11_03_100000_add_staff_message_to_club_computers_table.php`) и 38 моделей `../Zafiro/app/Models`.
Ниже — **итоговая** схема после всех миграций. Postgres-зеркало: `db/migrations/0001_init.sql`;
отличия и новое — `docs/schema-changes.md`; вопросы — `docs/open-questions/database.md`.

Ссылки на миграции даются сокращённо по префиксу имени файла (например `040140` =
`2026_10_13_040140_finalize_club_tenancy.php`).

### Сводка

| Показатель | Значение |
|---|---|
| Миграций | 86 (из них с изменением данных — 12, см. «Миграции данных») |
| Таблиц в MySQL всего | 51 (42 предметные + 8 Laravel-инфраструктуры + служебная `migrations`) |
| Перенесено в `0001_init.sql` | **42** |
| Не переносится | **9** (`sessions`, `password_reset_tokens`, `cache`, `cache_locks`, `jobs`, `job_batches`, `failed_jobs`, `personal_access_tokens`, `migrations`) |
| Моделей Eloquent | 38 (`Concerns/` не считаем) |
| Модели с `BelongsToClub` | 24 (+ тенантные без трейта: `club_subscriptions`, `club_zone_tariff`, `news` с NULL, `club_request_metrics`, `club_load_snapshots`) |
| Таблицы без модели | `club_zone_tariff` (pivot), `api_idempotency_keys`, `server_metrics`, `request_metrics`, `club_request_metrics`, `club_load_snapshots` |
| Модели без своей таблицы | нет |

#### Как сейчас хранятся деньги (важно)

- **Основная часть сумм — `decimal(12,2)` / `decimal(14,2)` в ТЕНГЕ** (не в тиынах), cast `decimal:2`:
  `products.price`, `tariffs.price`, `club_zones.price_per_hour`, `club_services.price`, `shop_orders.amount`,
  `member_balances.balance`, `member_balances.bonus_balance`, `club_memberships.total_spent`,
  `saas_plans.price_monthly` — `decimal(12,2)`; `cash_shifts.opening_balance`, `cash_shifts.closing_balance`,
  `cash_transactions.amount` — `decimal(14,2)`.
- **В тиынах (целое) уже хранятся**: `session_purchases.main_cents`, `session_purchases.bonus_cents`,
  `transfer_codes.amount_cents` (`unsignedBigInteger`, cast `integer`).
- Код переводит между ними строковыми функциями `AgentTariffPurchaseService::amountToCents()` /
  `formatCents()` (`app/Services/AgentTariffPurchaseService.php`, ~стр. 385–395) — точно, без float;
  но в `CashRegisterService` встречаются `(float)` и `/ 100` (`app/Services/CashRegisterService.php`,
  ~стр. 126, 133, 169–187) — см. open-questions.
- **Суммы внутри JSON** (`cash_transactions.receipt_data`, `shop_orders.items`, `activity_logs.details`,
  `webhook_deliveries.payload`, `clubs.settings` → уровни лояльности/бонусов) хранятся в тенге
  (строка `"123.45"` или число). В 0001 содержимое JSON не меняется.
- Не деньги, хотя `decimal`: `club_memberships.discount` `decimal(5,2)` — проценты;
  `server_metrics.load_*` `decimal(6,2)` — load average.
- Валюта: `tariffs.currency` (`'KZT'` по умолчанию), настройка клуба `finances.currency` ∈ `KZT, RUB, $`
  (`app/Http/Requests/SaveSettingsRequest.php`, стр. 31). «Тиын» = 1/100 основной единицы валюты клуба.

**Решение в 0001:** денежные `decimal` → `bigint` в тиынах (значение × 100 при переносе, точно),
имена полей сохранены, у каждого поля `COMMENT`. Это смена единицы измерения — зафиксировано
в `docs/schema-changes.md` и вынесено вопросом владельцу (`docs/open-questions/database.md`, В-1).

#### Тенантность

Трейт `app/Models/Concerns/BelongsToClub.php`: global scope `club` (`where club_id = CurrentClub::id()`,
без клуба — `1 = 0`), при `creating` подставляет `club_id` или бросает `LogicException`, связь `club()`.
Трейт `Concerns/UsesClubTimezone.php`: время хранится в UTC (`config/app.php` `'timezone' => 'UTC'`),
читается в часовом поясе текущего клуба.

Межтенантная целостность в MySQL держится **составными FK** `(club_id, X_id) → parent(club_id, id)`
(`042959_add_composite_tenant_foreign_keys`, `computer_reservations`), для этого у родителей есть
`UNIQUE (club_id, id)`. В Postgres сохранено 1:1.

#### Особенности MySQL, которые нужно учесть

- [ ] Кодировка `utf8mb4`, сопоставление `utf8mb4_unicode_ci` (`config/database.php`, ~стр. 56–57) —
  **регистронезависимые** UNIQUE и сравнения (`users.phone`, `clubs.slug`, `(club_id, sku)`,
  `(club_id, name)` у тарифов/категорий, `languages.code`). Postgres по умолчанию регистрозависим → open-questions В-7.
- [ ] MySQL создаёт индекс под каждый FK автоматически; в Postgres эти индексы созданы явно
  (имена `<table>_<col>_foreign` / имя составного FK).
- [ ] `unsigned` целые: в Postgres нет — тип на ступень шире + `CHECK (col >= 0)`.
- [ ] `timestamp` и `datetime` в MySQL хранят UTC (приложение пишет UTC). Перенос: читать при
  `SET time_zone = '+00:00'`, писать в `timestamptz` как UTC.
- [ ] Laravel `timestamps()` — оба поля `NULL`-абельные и без DEFAULT; так же в 0001.
- [ ] Миграция `042959` пропускается на SQLite (тесты Pest) — значит тесты не проверяли составные FK.

---

### Предметные таблицы (42) — переносятся

#### Пользователи, клубы, подписки

- [ ] **`users`** — модель `User` (`HasApiTokens`, `Notifiable`, `UsesClubTimezone`), BelongsToClub: **нет** (глобальная).
  - Поля: `id` bigint uns PK; `name` varchar(255) NN; `phone` varchar(255) NN **UNIQUE**; `email` varchar(255) NULL;
    `email_verified_at` timestamp NULL; `password` varchar(255) NN (bcrypt, cast `hashed`); `remember_token` varchar(100) NULL;
    `created_at`/`updated_at` timestamp NULL; `is_superadmin` bool NN DEFAULT false (`2026_10_15_000000`);
    `blocked_at` timestamp NULL (`2026_10_15_100000`).
  - История: `role`, `discount`, `balance`, `bonus_balance` добавлены в `2026_10_09_000000` и удалены в `040140`
    (перенесены в `club_memberships`/`member_balances`); `telegram_*` добавлены в `2026_10_16_120000`, удалены в `2026_10_18_120000`.
  - Индексы: `users_phone_unique`.
  - Casts: `email_verified_at` datetime, `password` hashed, `is_superadmin` boolean, `blocked_at` datetime.
  - Связи: `memberships()` hasMany ClubMembership; `currentMembership()` hasOne (по `CurrentClub`); `tokens()` (Sanctum).
  - Hidden: `password`, `remember_token`.

- [ ] **`clubs`** — модель `Club` (`UsesClubTimezone`), BelongsToClub: нет (это и есть тенант).
  - Поля: `id`; `name` varchar NN; `slug` varchar NN **UNIQUE**; `settings` json NULL (cast array — все настройки клуба,
    см. MIGRATION.md раздел настроек); timestamps; `installation_key_hash` char(64) NULL UNIQUE; `installation_key_prefix`
    char(12) NULL INDEX (`043934`); `installation_key` text NULL — **зашифрован** `Crypt` (cast `encrypted`, `044406`);
    `locale` varchar(16) NULL (`181908`); `timezone` varchar(64) NN DEFAULT `'Asia/Almaty'` (`060000`);
    `agent_exit_pin_hash` varchar NULL (bcrypt, `SettingsController` ~стр. 70); `agent_exit_pin_changed_at` timestamp NULL (`180000`);
    `agent_staff_minutes` smallint uns NN DEFAULT 15 (`220000`); `logo_path` varchar NULL (`210000`);
    `signin_image_path` varchar NULL (`2026_10_19_120000`).
  - Индексы: `clubs_slug_unique`, `clubs_installation_key_hash_unique`, `clubs_installation_key_prefix_index`.
  - Логика модели: при `creating` генерирует ключ установки (Str::random(64), sha256, префикс 12); при `updating`
    запрещает менять ключ (`LogicException`) — `app/Models/Club.php`, `booted()`.
  - Связи: `memberships()` hasMany; `subscription()` hasOne ClubSubscription.
  - Hidden: `installation_key*`, `agent_exit_pin_hash`, `logo_path`, `signin_image_path`.

- [ ] **`saas_plans`** — модель `SaasPlan`, BelongsToClub: нет.
  - Поля: `id`; `slug` varchar(80) UNIQUE; `name` varchar(120); `price_monthly` decimal(12,2) NULL (**тенге**);
    `trial_days` smallint uns DEFAULT 0; `features` json NULL; `is_active` bool DEFAULT true INDEX; timestamps.
  - Данные: `042815` создаёт план `trial` (14 дней) при отсутствии.
  - Casts: `price_monthly` decimal:2, `trial_days` integer, `features` array, `is_active` boolean. Связь: `subscriptions()` hasMany.

- [ ] **`club_subscriptions`** — модель `ClubSubscription`, BelongsToClub: **нет** (есть `club_id`, но scope не применяется).
  - Поля: `id`; `club_id` FK clubs **CASCADE**, **UNIQUE** (одна подписка на клуб); `saas_plan_id` FK saas_plans **RESTRICT**;
    `status` varchar(30) INDEX; `started_at` timestamp NN; `trial_ends_at`, `current_period_ends_at`, `canceled_at` timestamp NULL;
    `provider` varchar(40) NULL; `provider_reference` varchar NULL; timestamps.
  - Enum `status`: `trialing` (`AuthController.php` ~182), `active`, `expired` (`Console/Commands/ExpireSubscriptions.php` ~29),
    `suspended` (`Superadmin/ClubController.php` ~198). → CHECK.
  - Логика: `isActive()`, `isPastPaidPeriod()`, scope `active` (`app/Models/ClubSubscription.php`).
  - Связи: `plan()`, `club()` belongsTo.

- [ ] **`club_memberships`** — модель `ClubMembership`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK clubs CASCADE; `user_id` FK users CASCADE; `role` varchar(255) NN DEFAULT `'Пользователь'`;
    `permissions` json NULL (`2026_10_30`, формат `{"cash.view": true, …}` после `2026_10_31`); `discount` decimal(5,2) DEFAULT 0 (%);
    `total_spent` decimal(12,2) DEFAULT 0 (**тенге**, `2026_10_15_000020`); timestamps; `notifications_seen_at` timestamp NULL (`173104`);
    `can_manage_integrations` bool DEFAULT false (`190912`).
  - Индексы: `UNIQUE (club_id, user_id)` (на него ссылаются составные FK 6 таблиц); FK-индекс `user_id`.
  - Enum `role`: staff — `owner, admin, manager, cashier, operator`; клиент — `client` (`app/Support/ClubRoles.php`);
    **плюс старые не-staff роли** (DEFAULT `'Пользователь'`, бэкфилл `040139` из `users.role`) → CHECK **не ставим** (open-questions В-9).
  - Casts: `discount`, `total_spent` decimal:2; `notifications_seen_at` datetime; `can_manage_integrations` boolean; `permissions` array.
  - Связи: `user()` belongsTo; `balance()` hasOne MemberBalance по `user_id` (+ scope клуба).

- [ ] **`member_balances`** — модель `MemberBalance`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` ubigint NN; `user_id` ubigint NN; `balance` decimal(12,2) DEFAULT 0 (**тенге**);
    `bonus_balance` decimal(12,2) DEFAULT 0 (**тенге**); timestamps.
  - Индексы: `UNIQUE (club_id, user_id)`. FK `member_balances_membership_fk (club_id, user_id) → club_memberships(club_id, user_id)` **CASCADE**.
  - Нет FK `club_id → clubs` напрямую (каскад идёт через membership). Нет CHECK на неотрицательность.
  - Casts: `balance`, `bonus_balance` decimal:2. Связь: `user()`.
  - Сейчас баланс меняется `update`/`decrement` поля (`CashRegisterService`, `AgentShop` ~стр. 70) — в новом стеке только через ledger.

- [ ] **`club_invitations`** — модель `ClubInvitation`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `email` varchar NULL; `role` varchar(40) NN; `token_hash` varchar(64) UNIQUE;
    `invited_by` FK users NULL **SET NULL**; `expires_at` timestamp NN; `accepted_at` timestamp NULL; timestamps.
  - Индексы: `(club_id, accepted_at, expires_at)`, unique `token_hash`.
  - Enum `role`: `admin, manager, cashier, operator` (`ClubInvitationController.php` стр. 27) → CHECK.
  - Casts: `expires_at`, `accepted_at` datetime.

#### Склад и касса

- [ ] **`product_categories`** — модель `ProductCategory`, BelongsToClub: **да**.
  - Поля: `id`; `name` varchar NN; timestamps; `club_id` FK clubs CASCADE.
  - Индексы: `UNIQUE (club_id, name)` (было `UNIQUE(name)` до `040140`); `UNIQUE (club_id, id)`; `club_id`.
  - Связь: `products()` hasMany.

- [ ] **`products`** — модель `Product` (`HasFactory`, `SoftDeletes`), BelongsToClub: **да**.
  - Поля: `id`; `sku` varchar NN; `name` varchar NN; `category` varchar NN INDEX (старое текстовое поле категории);
    `product_category_id` ubigint NULL; `price` decimal(12,2) (**тенге**); `quantity` int uns DEFAULT 0; timestamps;
    `club_id` FK CASCADE; `deleted_at` timestamp NULL (`2026_11_01`).
  - Индексы: `UNIQUE (club_id, sku)`, `UNIQUE (club_id, id)`, `club_id`, `(club_id, name)`, `category`.
  - FK: `products_club_category_fk (club_id, product_category_id) → product_categories(club_id, id)` **RESTRICT**
    (раньше nullOnDelete — заменено в `042959`).
  - Casts: `price` decimal:2, `quantity` integer. Связи: `stockMovements()` hasMany, `productCategory()` belongsTo.
  - `quantity` unsigned — уход в минус в MySQL strict = ошибка БД; в Postgres сохранено `CHECK (quantity >= 0)`.

- [ ] **`stock_movements`** — модель `StockMovement`, BelongsToClub: **да**.
  - Поля: `id`; `product_id` NN; `user_id` NULL; `type` varchar NN INDEX; `quantity` int uns; `balance_after` int uns;
    `note` varchar NULL; timestamps; `club_id` FK CASCADE.
  - Индексы: `type`, `(product_id, created_at)`, `club_id`, `(club_id, created_at)`.
  - FK: `stock_movements_club_product_fk (club_id, product_id) → products(club_id, id)` CASCADE;
    `stock_movements_club_user_fk (club_id, user_id) → club_memberships(club_id, user_id)` (NO ACTION — удаление
    участника с движениями запрещено).
  - Enum `type`: `receipt`, `write_off` (`StockMovement::TYPE_*`) → CHECK.
  - Casts: `quantity`, `balance_after` integer. Связь: `product()` belongsTo.

- [ ] **`cash_shifts`** — модель `CashShift`, BelongsToClub: **да**.
  - Поля: `id`; `opened_by`, `closed_by` NULL; `status` varchar INDEX; `open_marker` bool NULL;
    `opening_balance` decimal(14,2) (**тенге**); `closing_balance` decimal(14,2) NULL; `opened_at` timestamp NN;
    `closed_at` NULL; timestamps; `club_id` FK CASCADE.
  - Индексы: `UNIQUE (club_id, open_marker)` — **одна открытая смена на клуб** (`true` у открытой, `NULL` у закрытой);
    `UNIQUE (club_id, id)`; `club_id`; `status`.
  - FK: `cash_shifts_opened_by_membership_fk`, `cash_shifts_closed_by_membership_fk` → `club_memberships(club_id, user_id)`.
  - Enum `status`: `open`, `closed` (`CashShift::STATUS_*`) → CHECK.
  - Casts: балансы decimal:2, `opened_at`/`closed_at` datetime. Связь: `transactions()` hasMany.

- [ ] **`cash_transactions`** — модель `CashTransaction`, BelongsToClub: **да**.
  - Поля: `id`; `cash_shift_id` NN; `refund_of_id` NULL **UNIQUE** FK → `cash_transactions.id` **SET NULL** (`2026_10_24`,
    один возврат на приход); `user_id` NULL; `type` varchar INDEX; `amount` decimal(14,2) (**тенге**); `description` varchar NN;
    `receipt_data` json NULL; timestamps; `club_id` FK CASCADE.
  - Индексы: `type`, `club_id`, `(club_id, created_at)`, unique `refund_of_id`.
  - FK: `cash_transactions_club_shift_fk (club_id, cash_shift_id) → cash_shifts(club_id, id)` CASCADE;
    `cash_transactions_club_user_fk (club_id, user_id) → club_memberships`.
  - Enum `type`: `income`, `expense` → CHECK. Маркер `description = 'Продажа POS'` (`CashTransaction::POS_SALE`).
  - JSON `receipt_data`: `{items: [{product_id?, name, quantity, price, …}]}` для продажи POS или
    `{top_up: {user_id, balance_type: balance|bonus_balance, bonus: <тенге float>}}` (`CashRegisterService.php` ~стр. 69, 126).
  - Casts: `amount` decimal:2, `receipt_data` array. Связи: `shift()` belongsTo, `refund()` hasOne self; `refundKind()`.

- [ ] **`club_services`** — модель `ClubService`, BelongsToClub: **да** (`2026_11_02`).
  - Поля: `id`; `club_id` FK CASCADE; `name` varchar(150); `price` decimal(12,2) (**тенге**); `unit` varchar(30) NULL;
    `is_active` bool DEFAULT true; timestamps. Индекс `(club_id, is_active, name)`.
  - Casts: `price` decimal:2, `is_active` boolean.

#### Зоны, ПК, тарифы

- [ ] **`club_zones`** — модель `ClubZone`, BelongsToClub: **да**.
  - Поля: `id`; `name` varchar NN; `description` varchar NULL; `capacity` int uns NULL; `price_per_hour` decimal(12,2) NULL
    (**тенге**, `2026_10_15_170000`; NULL — нет поминутной продажи); `icon` varchar(40) DEFAULT `desktop`; `theme` varchar(20)
    DEFAULT `neutral`; `status` varchar(20) DEFAULT `available` INDEX; `is_featured` bool DEFAULT false; `sort_order` int uns
    DEFAULT 0 INDEX; `position_x`/`position_y` int uns DEFAULT 20; `width` DEFAULT 320; `height` DEFAULT 180 (`104240`);
    `rotation` tinyint uns DEFAULT 0 (`123811`); timestamps; `club_id` FK CASCADE.
  - Индексы: `status`, `sort_order`, `club_id`, `UNIQUE (club_id, id)`.
  - Enum (`app/Http/Requests/StoreClubZoneRequest.php`, стр. 27–29; `UpdateClubZoneLayoutRequest.php` стр. 28):
    `icon` ∈ `crown, desktop, gamepad, mug-hot, couch, dice, door-open, bong, smoking`;
    `theme` ∈ `green, neutral, yellow, purple, blue`; `status` ∈ `available, busy, service`; `rotation` ∈ `0, 90` → CHECK (кроме rotation: только ≥ 0).
  - Casts: целые, `price_per_hour` decimal:2, `is_featured` boolean. Связи: `computers()` hasMany (order by number);
    `tariffs()` belongsToMany через `club_zone_tariff` c `wherePivot('club_id', current)`.

- [ ] **`club_computers`** — модель `ClubComputer`, BelongsToClub: **да**. Самая «горячая» таблица (heartbeat).
  - Поля: `id`; `club_zone_id` NN; `number` int uns NN; `status` varchar(20) DEFAULT `available` INDEX;
    `power_command` varchar(16) NULL, `power_command_at`, `wake_requested_at` timestamp NULL (`2026_10_21`);
    `is_reserved` bool DEFAULT false (`125823`); `position_x`/`position_y` int uns DEFAULT 5; timestamps;
    `last_seen_at` timestamp NULL INDEX (`2026_10_09_000002`); `club_id` FK CASCADE;
    `device_name`, `display_name`, `device_id` varchar(255) NULL; `agent_state` varchar(20) NULL; `agent_version` varchar(50) NULL;
    `agent_update_version` varchar(20) NULL; `agent_update_offers` tinyint uns DEFAULT 0; `agent_update_offered_at` NULL (`2026_10_20`);
    `current_session_id` varchar(255) NULL (UUID-строка); `current_session_user_id` FK users NULL **SET NULL**;
    `session_guest` bool DEFAULT false; `session_ends_at` NULL; `staff_user_id` FK users NULL SET NULL; `staff_kind` varchar(16) NULL;
    `staff_until` NULL (`220000`); `mac_address` varchar(17) NULL; `local_ip`, `remote_ip` varchar(45); `cpu_model`, `gpu_model`,
    `os_version` varchar; `cpu_cores` smallint uns; `ram_total_mb` int uns; `hardware_reported_at` (`072421`);
    `agent_token_hash` char(64) NULL UNIQUE; `agent_token_issued_at` NULL (`150000`); `staff_message` varchar(300) NULL;
    `staff_message_at` NULL (`2026_11_03`).
  - Индексы: `UNIQUE (club_zone_id, number)`; `UNIQUE (club_id, id)`; `UNIQUE (club_id, device_id)` (глобальный unique
    `device_id` снят в `075615`); `UNIQUE (club_id, mac_address)` (`2026_10_17_100000`, MAC нормализуется `Support/MacAddress`);
    `UNIQUE agent_token_hash`; `(club_id, device_name)`; `(club_id, wake_requested_at)`; `status`; `last_seen_at`; `club_id`.
  - FK: `club_computers_club_zone_fk (club_id, club_zone_id) → club_zones(club_id, id)` CASCADE; users SET NULL ×2.
  - Enum: `status` ∈ `available, online, offline, restarting` (`ClubComputerSessions.php` ~95, `ClubComputerController.php` ~178,
    `Api/V1/ComputerController.php` ~80); `agent_state` ∈ `idle, in_session, locked, shutting_down` (`ClubComputer::AGENT_STATES`);
    `power_command` ∈ `restart, shutdown, service` (`ComputerPower::COMMANDS`); `staff_kind` ∈ `pin, panel, signin`
    (`AgentExitPinController` ~48, `ClubComputerController` ~191, ~500) → CHECK. Вычисляемый статус `reserved`/`online`/`offline`
    — в `currentStatus()`, в БД не пишется.
  - Константы: `HEARTBEAT_TIMEOUT_SECONDS = 90`.
  - Casts: целые, booleans, 10 полей datetime. Hidden: `agent_token_hash`.
  - Связи: `zone()`, `staffUser()`, `sessionUser()` belongsTo; `reservations()` hasMany.
  - Производительность: `last_seen_at`, `remote_ip`, `status='online'` пишутся на **каждый heartbeat**
    (`ClubComputerSessions.php` ~стр. 95) — в новом стеке в Valkey.

- [ ] **`tariffs`** — модель `Tariff` (`SoftDeletes`), BelongsToClub: **да**.
  - Поля: `id`; `name` varchar(100) NN; `description` text NULL; `duration_minutes` int uns INDEX; `price` decimal(12,2)
    (**тенге**); `is_active` bool DEFAULT true INDEX; timestamps; `payment_method` varchar(32) DEFAULT `main_balance_only`;
    `currency` varchar(10) DEFAULT `KZT`; `discount_enabled` bool DEFAULT false; `user_roles` json NULL;
    `usage_limit_enabled` bool DEFAULT false; `usage_limit_minutes` int uns NULL; `quantity_limit_enabled` bool DEFAULT false;
    `quantity_limit` int uns NULL; `validity_periods` json NULL; `sale_periods` json NULL (`2026_10_26`); `club_id` FK CASCADE;
    `deleted_at` NULL (`2026_10_29`).
  - Удалены: `color`, `club_name` (`2026_10_28`); `sale_starts_at`/`sale_ends_at` (добавлены `2026_10_25`, удалены `2026_10_26`).
  - Индексы: `UNIQUE (club_id, name)` (учитывает и удалённые тарифы!), `UNIQUE (club_id, id)`, `club_id`, `duration_minutes`, `is_active`.
  - Enum `payment_method` ∈ `main_balance_only, main_or_bonus` (`StoreTariffRequest.php` стр. 28) → CHECK.
  - JSON `validity_periods`/`sale_periods`: `[{start_day: mon..sun, start_time: "HH:MM", end_day, end_time}]`
    (`StoreTariffRequest.php` стр. 37–45, `Support/WeeklyPeriods`; исправление данных `2026_10_27`). `user_roles`: список ролей.
  - Casts: целые, `price` decimal:2, booleans, 3 array. Связи: `zones()` belongsToMany с pivot `club_id`. Методы `onSale()`, `inValidityPeriod()`.

- [ ] **`club_zone_tariff`** — pivot, модели нет; тенантный (есть `club_id`).
  - Поля: `club_zone_id`, `tariff_id`, `club_id` NN. PK `(club_zone_id, tariff_id)`; индекс `club_id`.
  - FK: `(club_id, club_zone_id) → club_zones` CASCADE; `(club_id, tariff_id) → tariffs` CASCADE; `club_id → clubs` CASCADE.

#### Сессии гостей, пакеты, брони, QR-вход

- [ ] **`session_purchases`** — модель `SessionPurchase`, BelongsToClub: **да** (без UsesClubTimezone).
  - Поля: `id`; `club_id` FK CASCADE; `session_id` varchar(36) (UUID сессии ПК); `user_id` FK users CASCADE; `kind` varchar(10);
    `tariff_id` FK tariffs NULL SET NULL; `name` varchar; `minutes` int uns; `main_cents`, `bonus_cents` ubigint DEFAULT 0
    (**тиыны**); `expires_at` datetime NULL; timestamps.
  - Индексы: `(club_id, session_id)`, `created_at`.
  - Enum `kind` ∈ `tariff, minutes` (`SessionPurchase::KIND_*`; запуск пакета пишется как `tariff`,
    `AgentTariffPurchaseService.php` ~191) → CHECK.
  - Это «состав» платной сессии для возврата (`SessionSettlement.php`).
  - **Не история:** строки старше 8 дней (кроме идущих сессий) удаляются ежедневно, истёкшие `time_packages` — тоже
    (`routes/console.php`, `session-purchases:prune`). Долговременной истории денег за время в БД нет — только `activity_logs`.

- [ ] **`time_packages`** — модель `TimePackage`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `user_id` FK CASCADE; `tariff_id` FK NULL SET NULL; `name`; `minutes_left` int uns;
    `expires_at` datetime NN; timestamps. Индекс `(club_id, user_id, expires_at)`.
  - Связь: `tariff()` belongsTo **withTrashed**.

- [ ] **`computer_reservations`** — модель `ComputerReservation`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `club_computer_id` ubigint NN; `user_id` FK NULL SET NULL; `customer_name` varchar(120) NULL;
    `customer_phone` varchar(20) NULL; `note` varchar(500) NULL; `starts_at`, `ends_at` datetime NN; `arrived_at`, `cancelled_at`
    datetime NULL; `created_by` FK users NULL SET NULL; timestamps.
  - Индексы: `(club_id, starts_at)`, `(club_computer_id, starts_at)`.
  - FK: `computer_reservations_club_computer_fk (club_id, club_computer_id) → club_computers(club_id, id)` CASCADE.
  - Состояние вычисляется: `pending/active/finished/arrived/cancelled` (`state()`); scopes `pending`, `overlapping`.

- [ ] **`agent_qr_logins`** — модель `AgentQrLogin` (`HasUuids`), BelongsToClub: **да**.
  - Поля: `id` **uuid** PK; `club_id` FK CASCADE; `club_computer_id` FK club_computers CASCADE (простой FK);
    `code_hash` varchar(64) UNIQUE; `approved_user_id` FK users NULL **CASCADE**; `approved_at`, `consumed_at` datetime NULL;
    `expires_at` datetime NN INDEX; timestamps. `LIFETIME_MINUTES = 5`.

#### Гости: друзья, переводы, чат, заказы, восстановление пароля

- [ ] **`friendships`** — модель `Friendship`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `requester_id`, `addressee_id` FK users CASCADE; `accepted_at` datetime NULL; timestamps.
  - Индексы: `UNIQUE (club_id, requester_id, addressee_id)`, `(club_id, addressee_id)`. Scope `involving`.

- [ ] **`transfer_codes`** — модель `TransferCode`, BelongsToClub: **нет** (нет `club_id`; клуб — через `friendship_id`).
  - Поля: `id`; `user_id` FK CASCADE; `friendship_id` FK CASCADE; `amount_cents` ubigint (**тиыны**); `code_hash` varchar (bcrypt);
    `attempts` tinyint uns DEFAULT 0; `expires_at` datetime NN; timestamps. Индекс `(user_id, created_at)`.

- [ ] **`chat_messages`** — модель `ChatMessage`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `customer_id` FK users CASCADE; `author_id` FK users NULL SET NULL; `from_staff` bool NN;
    `body` text; `club_computer_id` FK NULL SET NULL; `read_at` datetime NULL; timestamps.
  - Индексы: `(club_id, customer_id, id)`, `(club_id, from_staff, read_at)`. `MAX_LENGTH = 1000`, `ANSWERING_ROLES = cashier, operator`.

- [ ] **`shop_orders`** — модель `ShopOrder`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `user_id` FK CASCADE; `club_computer_id` FK NULL SET NULL; `computer_name` varchar NULL;
    `items` json NN (`[{product_id, name, quantity, price: "0.00", total: "0.00"}]`, `AgentShop.php` ~стр. 55–62);
    `amount` decimal(12,2) (**тенге**); `status` varchar(20) DEFAULT `new`; `handled_by` FK users NULL SET NULL; `handled_at` datetime NULL; timestamps.
  - Индексы: `(club_id, status, id)`, `(club_id, id)`. Enum `status` ∈ `new, done, cancelled` → CHECK.

- [ ] **`password_recovery_codes`** — модель `PasswordRecoveryCode`, BelongsToClub: нет.
  - Поля: `id`; `user_id` FK CASCADE; `code_hash` varchar (bcrypt); `attempts` tinyint uns DEFAULT 0; `expires_at` datetime NN; timestamps.
    Индекс `(user_id, created_at)`.

#### Новости, задачи

- [ ] **`news`** — модель `News`, BelongsToClub: **нет намеренно** (`club_id NULL` = новость платформы; scopes `platform()`, `ofClub()`, `published()`).
  - Поля: `id`; `club_id` FK NULL CASCADE; `title` varchar(150); `body` text; `image_path` varchar NULL; `is_published` bool DEFAULT true;
    `created_by` FK NULL SET NULL; timestamps. Индекс `(club_id, is_published, created_at)`. `PAGE = 20`.

- [ ] **`todo_tasks`** — модель `TodoTask`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `title` varchar(200); `due_on` **date** (день клуба, cast не задан — строка Y-m-d);
    `created_by`, `done_by` FK NULL SET NULL; `done_at` datetime NULL; timestamps. Индекс `(club_id, due_on)`.

#### Журналы

- [ ] **`activity_logs`** — модель `ActivityLog`, BelongsToClub: **да**. Журнал действий клуба (растёт).
  - Поля: `id`; `event` varchar(60) INDEX; `description` text; `user_id` **NN** (`171650`); `subject_type` varchar(100) NULL
    (класс модели); `subject_id` ubigint NULL; `details` json NULL; `ip_address` varchar(45) **NN** (`171650`); `user_agent` text NULL;
    timestamps; `club_id` FK CASCADE.
  - Индексы: `event`, `(created_at, event)`, `(subject_type, subject_id)`, `club_id`, `(club_id, created_at)`, `(club_id, event, created_at)`.
  - FK: `activity_logs_club_user_fk (club_id, user_id) → club_memberships` (NO ACTION).
  - `event`: ~100 значений в `ActivityLog::EVENT_LABELS` — CHECK **не ставим** (открытый список).
  - Используется как источник данных: бэкфилл лояльности `2026_10_18_100000` читает `tariff.purchased`, `time.purchased`,
    `shop.purchased`, `shop.order_cancelled`; `GuestHistory` строит историю гостя по журналу.

- [ ] **`platform_logs`** — модель `PlatformLog`, BelongsToClub: нет (действия суперадмина).
  - Поля: `id`; `user_id` FK NULL SET NULL; `club_id` FK NULL SET NULL; `event` varchar(64); `description` varchar(500);
    `subject_type`/`subject_id` (nullableMorphs, индекс); `details` json; `ip_address` varchar(45) NN; `user_agent` varchar(500) NULL; timestamps.
  - Индексы: `created_at`, `(club_id, created_at)`, `(subject_type, subject_id)`. `EVENT_LABELS` — 28 событий.

- [ ] **`platform_settings`** — модель `PlatformSetting` (PK строка `key`), BelongsToClub: нет.
  - Поля: `key` varchar(64) PK; `value` json NULL; `updated_by` FK users NULL SET NULL; timestamps.
  - Известные ключи: `agent_release` (`PlatformSetting::AGENT_RELEASE`), `telegram_gateway` (`{token: <Crypt>}`,
    `Services/TelegramGateway.php` ~55), `telegram_alerts` (`{token: <Crypt>, chat_id, locale}`, `Services/Monitoring/TelegramAlerts.php` ~75);
    `telegram_bot` удалён (`2026_10_18_120000`). Значения кэшируются навсегда (`Cache::rememberForever`).

#### Локализация

- [ ] **`languages`** — модель `Language`, BelongsToClub: нет. Поля: `id`; `code` varchar(16) UNIQUE; `name` varchar(64);
  `is_enabled` bool DEFAULT true; `is_system` bool DEFAULT false; `sort_order` smallint uns DEFAULT 0; timestamps.
  Сид в миграции `181908`: `ru`, `kk`, `en` (system) + `translations:sync`. Связь `translations()` hasMany.
- [ ] **`translations`** — модель `Translation`, BelongsToClub: нет. Поля: `id`; `language_id` FK CASCADE; `group` varchar(64)
  DEFAULT `'*'`; `key` text; `key_hash` char(40) (`sha1(group|key)`); `value` text; timestamps.
  Индекс `UNIQUE (language_id, group, key_hash)`. В Postgres `group` — зарезервированное слово, в SQL экранируется `"group"`.

#### Интеграционный API и вебхуки (`2026_10_13_190912_create_integration_api_tables`)

- [ ] **`api_keys`** — модель `ApiKey`, BelongsToClub: **да**. Поля: `id`; `club_id` FK CASCADE; `created_by` FK users (NO ACTION);
  `name` varchar(100); `prefix` varchar(16) UNIQUE; `key_hash` char(64) (Hidden); `scopes` json NN; `allowed_ips` json NULL;
  `rate_limit_per_minute` smallint uns NULL; `expires_at`, `last_used_at` NULL; `last_used_ip` varchar(45); `revoked_at` NULL;
  `revoked_by` FK users NULL (NO ACTION); `rotated_from_id` FK api_keys NULL SET NULL; timestamps. Индекс `(club_id, revoked_at)`.
- [ ] **`api_request_logs`** — модель `ApiRequestLog` (`UPDATED_AT = null`), BelongsToClub: нет. Поля: `id`; `club_id`, `api_key_id`
  ubigint NULL **без FK**; `key_prefix` varchar(16); `method` varchar(8); `endpoint` varchar(191); `status` smallint uns; `duration_ms` int uns;
  `ip` varchar(45); `created_at` timestamp NN DEFAULT CURRENT_TIMESTAMP. Индексы `(club_id, created_at)`, `(api_key_id, created_at)`, `created_at`.
- [ ] **`api_idempotency_keys`** — модели нет (middleware `app/Http/Middleware/Api/ApiIdempotency.php`). Поля: `id`; `api_key_id` FK CASCADE;
  `idempotency_key` varchar(100); `request_hash` char(64); `status` smallint uns NULL; `response` longtext NULL; `created_at` DEFAULT CURRENT_TIMESTAMP.
  Индексы `UNIQUE (api_key_id, idempotency_key)`, `created_at`. Тенант — через ключ API.
- [ ] **`webhook_endpoints`** — модель `WebhookEndpoint`, BelongsToClub: **да**. Поля: `id`; `club_id` FK CASCADE; `created_by` FK users;
  `url` varchar(500); `secret` text (**зашифрован** cast `encrypted`, Hidden); `events` json; `is_active` bool DEFAULT true;
  `failure_count` int uns DEFAULT 0; `last_success_at`, `last_failure_at` NULL; timestamps. Индекс `(club_id, is_active)`.
- [ ] **`webhook_deliveries`** — модель `WebhookDelivery`, BelongsToClub: **да**. Поля: `id`; `club_id` FK CASCADE;
  `webhook_endpoint_id` FK CASCADE; `event_id` uuid UNIQUE; `event` varchar(64); `payload` json; `status` varchar(16) DEFAULT `pending`;
  `attempts` smallint uns DEFAULT 0; `last_status_code` smallint uns NULL; `last_error` varchar(255) NULL; `next_attempt_at`, `delivered_at` NULL; timestamps.
  Индексы `(webhook_endpoint_id, created_at)`, `(status, next_attempt_at)`, `(club_id, status, created_at)`.
  Enum `status` ∈ `pending, delivered, failed, skipped` (`app/Jobs/DeliverWebhook.php` ~41–69) → CHECK. Таблица — очередь доставки
  (outbox): в новом стеке остаётся как outbox, транспорт — NATS JetStream.

#### Мониторинг (`2026_10_19_150000_create_monitoring_tables`, `2026_10_19_170000`) — моделей нет

- [ ] **`server_metrics`** — раз в минуту: `minute` int uns UNIQUE (Unix/60); `load_1/5/15` decimal(6,2); `cpu_count`; `memory_*_mb`;
  `disk_*_mb`; `uptime_seconds`; `database_mb`; `queue_pending`, `queue_failed` DEFAULT 0; `queue_oldest_seconds`. Без timestamps.
  Поля `queue_*` описывают очередь Laravel (`jobs`/`failed_jobs`) — в новом стеке смысл меняется (JetStream).
- [ ] **`request_metrics`** — `minute`, `channel` varchar(16), `requests`, `errors`, `duration_ms`; `UNIQUE (minute, channel)`.
  Enum `channel` ∈ `panel, agent, api, guest` (`Services/Monitoring/RequestRecorder::CHANNELS`) → CHECK.
- [ ] **`club_request_metrics`** — `club_id` FK CASCADE, `minute`, `agent_requests`, `api_requests`, `panel_requests`, `guest_requests`,
  `errors`, `duration_ms` (все DEFAULT 0); `UNIQUE (club_id, minute)`, индекс `minute`.
- [ ] **`club_load_snapshots`** — каждые 5 минут: `club_id` FK CASCADE, `captured_at` timestamp NN, `weekday` (1 = пн, пояс клуба),
  `hour`, `computers`, `online`, `busy`; индексы `(club_id, captured_at)`, `captured_at`.

---

### Laravel-инфраструктура — НЕ переносится (9)

- [ ] `sessions` (`0001_01_01_000000`) — веб-сессии админки → **Valkey** (сессии/refresh-токены с TTL) либо stateless access-JWT + refresh.
- [ ] `password_reset_tokens` (`0001_01_01_000000`) — стандартный сброс по e-mail; в коде восстановление идёт через
  `password_recovery_codes` + Telegram Gateway (`Services/PasswordRecovery.php`) → не нужна.
- [ ] `cache`, `cache_locks` (`0001_01_01_000001`) — кэш и блокировки (`Cache::rememberForever` для `PlatformSetting` и др.) → **Valkey**
  (кэш, rate limit, распределённые блокировки `SET NX PX`).
- [ ] `jobs`, `job_batches`, `failed_jobs` (`0001_01_01_000002`) — очередь (сейчас `DeliverWebhook` через `schedule:run`) →
  **NATS JetStream** (стримы + consumer'ы, DLQ вместо `failed_jobs`) и Go-воркеры вместо cron.
- [ ] `personal_access_tokens` (`2026_10_09_000001`, Sanctum; токены гостей/мобилок с abilities `club:<id>`,
  `QrLogins.php` ~66, `Api/AuthController.php` ~114, `AgentRegisterController.php` ~64) → **свои токены**:
  короткий access + ротируемый refresh (таблица refresh-токенов — новая миграция Этапа 1); агент ПК — mTLS-сертификат.
  Старые токены не переносим: гости перелогинятся, агенты переустанавливаются (решение №3 ТЗ).
- [ ] `migrations` (служебная Laravel) → `goose_db_version` (создаёт goose).

---

### Очистка (retention) в старом коде — `routes/console.php`

- [ ] `monitoring:prune` (daily) — метрики мониторинга; `integrations:prune` (hourly) — `api_request_logs`, `api_idempotency_keys`,
  `webhook_deliveries` (сроки — в командах `app/Console/Commands`); `sanctum:prune-expired --hours=24` (daily);
  `session-purchases:prune` (daily, см. выше); `agent-qr-logins:prune` (daily, старше суток).
- [ ] Очередь — драйвер `database` (`config/queue.php` стр. 16), воркер `queue:work --stop-when-empty --max-time=50` раз в минуту.

### Миграции данных (не DDL) — что повторить или учесть при переносе

- [ ] `040139_backfill_club_data` — клуб `legacy`, перенос ролей/балансов из `users` (уже исторично; в ETL не нужен).
- [ ] `042815_backfill_legacy_club_subscription` — план `trial` 14 дней и подписка `legacy` (данные придут переносом).
- [ ] `044406_add_persistent_installation_key` — генерация ключей установки (зашифрованы APP_KEY → open-questions В-5).
- [ ] `065745` — `device_name = PC-%02d`.
- [ ] `181908` — сид языков `ru/kk/en` + `translations:sync` (для чистой установки нового стека нужен сид → schema-changes).
- [ ] `2026_10_14_200000_revoke_duplicate_agent_tokens` — чистка Sanctum (не нужен).
- [ ] `2026_10_17_100000` — нормализация MAC (`Support/MacAddress::normalize`), дубли → NULL.
- [ ] `2026_10_18_100000_backfill_loyalty_spending` — пересчёт `total_spent` по журналу (в тиынах через `amountToCents`)
  и миграция `settings.finances` (`loyalty_rules` → `loyalty_levels`, удаление `loyalty_type`, `tax_rate`).
- [ ] `2026_10_18_120000` — удаление `platform_settings.telegram_bot`.
- [ ] `2026_10_27_100000` — исправление недельных периодов тарифов, заканчивающихся в понедельник.
- [ ] `2026_10_30_100000` / `2026_10_31_100000` — права: `settings.club.allow_cashier_refunds` → `permissions`, затем
  раздел → `раздел.действие`.

### Проверка

- [ ] `goose up → down → up` на `postgres:18.6` — проходит (отчёт субагента в, Этап 0).
- [ ] `sqlc generate` и `sqlc vet` (правило `tenant-filter`) — проходят; `go build ./internal/store/...` — ок.

## 40. Настройки: клуба, платформы, конфигурация

> Раздел инвентаризации Этапа 0. Источник правды — `../Zafiro` (Laravel). Все ссылки на файлы даны от корня старого репозитория.
> Открытые вопросы раздела: `docs/open-questions/settings-jobs-integrations.md`.

### 40.0. Где и как хранятся настройки

- [ ] **Настройки клуба — JSON-колонка `clubs.settings`** (`database/migrations/2026_10_13_040135_create_clubs_table.php`, `$table->json('settings')->nullable()`), cast `array` в `app/Models/Club.php` (`casts()`, ~строка 22). Отдельной таблицы `settings`/`club_settings` **нет**.
  - Структура: три секции `club`, `finances`, `guests` → `{ "club": {...}, "finances": {...}, "guests": {...} }`.
  - В БД хранится только то, что клуб хоть раз сохранил; значения по умолчанию в БД **не пишутся**: они подставляются при чтении.
- [ ] **Чтение** — `app/Services/CurrentClub.php` (`settings()`, ~строка 71): возвращает `club->settings` как есть, без дефолтов.
  Особый случай: если `settings === null` и `slug === 'legacy'`, то настройки читаются из файла `storage/app/settings.json` (наследие первой версии; см. открытый вопрос Q-S1).
  Каждый сервис сам задаёт дефолт через `data_get(..., default)` / `?? default` — дефолты продублированы в нескольких местах (контроллер + сервис + blade). В Go нужен **один** типизированный `ClubSettings` с дефолтами и нормализацией.
- [ ] **Чтение для экрана/API** — `app/Http/Controllers/SettingsController.php` (`loadSettings()`, ~315–329): `array_replace_recursive(defaultSettings(), CurrentClub::settings())`, затем все «минутные» поля секции `club` (кроме `pc_action_after_session_end`, `agent_shop_enabled`, `agent_auto_update`) нормализуются через `minutes()` (~416–426): число → int или float; **строка** вида `"30 сек"` / `"5,5 мин"` → первое число из строки (запятая → точка), если в строке есть «сек» — делится на 60. Это поддержка старых строковых значений; при переносе данных MySQL → Postgres надо прогнать ту же нормализацию (Q-S2).
- [ ] **Дефолты** — `SettingsController::defaultSettings()` (~331–376). Это единственное полное описание схемы.
- [ ] **Запись** — `SettingsController::save()` (~208–313), маршрут `POST /settings/save` (`settings.save`, `routes/web.php` ~213), валидация `app/Http/Requests/SaveSettingsRequest.php`.
  - Сохраняется **одна секция** за запрос (`section` ∈ `club|finances|guests`); меняются только поля, присутствующие в запросе (`$request->has($field)`).
  - Приведение типов: булевы — `$request->boolean()` (форма шлёт hidden `0` + checkbox `1`); «минутные» — `number((float))` (int, если целое); `loyalty_levels`/`topup_bonus_levels` — `PercentLevels::normalize`; `bonus_max_percent`, `auto_billing_pause_minutes` — `(int)`; остальное (`currency`, `pc_action_after_session_end`) — как пришло.
  - Пустые строки таблицы уровней отбрасываются до валидации (`SaveSettingsRequest::prepareForValidation()`, ~79–87).
  - Весь JSON `settings` перезаписывается целиком (`$club->settings = $settings; $club->save()`), без блокировки строки: параллельное сохранение двух секций может потерять изменения (в Go — `UPDATE ... SET settings = jsonb_set(...)` или `SELECT ... FOR UPDATE`).
  - Аудит: при изменениях — `ActivityLogger::record('settings.updated', ...)` с диффом `было → стало` по каждому полю (уровни форматируются как `порог: процент%` через `;`). Событие показывается в уведомлениях (`app/Services/ClubNotifications.php`, иконка `fa-sliders`).
  - Побочный эффект: `Club::saved` в `app/Providers/AppServiceProvider.php` (`invalidateApiKeyCache()`, ~119–123) сбрасывает кэш метаданных API-ключей клуба при изменении `settings` (метаданные ключа содержат копию `settings`).
- [ ] **Права**: просмотр — `settings.view`, сохранение — `settings.update` (обе — `ClubPermissions::MANAGERS`, `app/Support/ClubPermissions.php` ~42; маппинг маршрутов — `app/Http/Middleware/RequireClubArea.php` ~63–64). PIN агента и время обслуживания — право `workers.update` (`RequireClubArea.php` ~67).
- [ ] **Экраны** (`resources/views/settings/`): `club.blade.php` (`/settings/club`), `finances.blade.php` (`/settings/finances`, подшаблон `percent-levels.blade.php` для таблиц уровней), `guests.blade.php` (`/settings/guests`), `languages.blade.php` (`/settings/languages`), `api.blade.php` (`/settings/api` — справочник URL настроек), `integrations.blade.php` (`/settings/integrations`, см. 60-integrations), `permissions*.blade.php` (права сотрудников — не настройки клуба в смысле JSON, описываются в разделе прав). Общий layout — `settings/layout.blade.php`, якоря — `anchors.blade.php`.
- [ ] **API чтения отдельного ключа**: `GET /api/settings/{section}/{key}` (`routes/api.php` ~164–170; `auth:sanctum`, `current-club`, `subscription`, `throttle:60,1`) → `SettingsController::showSetting()` (~193–206): `{ip, setting, name, value}`. Доступно **любой** роли клуба (замечание в `docs/integration-api.md`, раздел 1). Решение о судьбе — Q-S3.

### 40.1. Секция `club` (`clubs.settings.club`)

Экран: `/settings/club` (`resources/views/settings/club.blade.php`), блоки «Сессии и ПК», «Агент на ПК», «Сотрудники на смене».

- [ ] **`club.session_idle_timeout`** — число минут (int|float, шаг 0.01 в форме).
  - Дефолт: `15` (`SettingsController::defaultSettings()`; дубль в `club.blade.php` ~31).
  - Валидация: `required_if:section,club|numeric|min:0|max:10080` (`SaveSettingsRequest` ~24).
  - Где используется: **нигде в логике** (grep по `app/`, `WindowsAgent/` — только контроллер, request, view и тесты `tests/Feature/SettingsTest.php`, `AgentShopTest.php`). Хранится и показывается, но не влияет на поведение. → Q-S4.
- [ ] **`club.pc_action_after_session_end`** — enum-строка `none|restart|shutdown`.
  - Дефолт: `'none'`.
  - Валидация: `sometimes|in:none,restart,shutdown` (~25).
  - Используется: `app/Http/Controllers/ClubComputerController.php` (`afterSessionAction()`, ~511–521) → поле `club.after_session.action` в ответе heartbeat агенту (неизвестное значение → `none`).
- [ ] **`club.pc_restart_after_session_end`** — число минут (int|float), задержка перед действием из предыдущего пункта (название историческое: относится к любому действию).
  - Дефолт: `0.5` (30 секунд).
  - Валидация: `required_if:section,club|numeric|min:0|max:10080` (~26).
  - Используется: `ClubComputerController::afterSessionAction()` → `delay_seconds = (int) round(max(0, minutes) * 60)`; нечисловое → `30`.
- [ ] **`club.session_auto_terminate_when_pc_unavailable`** — число минут.
  - Дефолт: `5`.
  - Валидация: `required_if:section,club|numeric|min:0|max:10080` (~27).
  - Используется: **нигде**. Сессии на офлайн-ПК завершаются только по `session_ends_at` командой `club-computers:expire-sessions` (см. 50-jobs). → Q-S4.
- [ ] **`club.booking_interval`** — число минут, «зазор» до и после каждой брони.
  - Дефолт: `30` (дубль: `ComputerReservations::DEFAULT_GAP_MINUTES = 30`, `app/Services/ComputerReservations.php` ~19).
  - Валидация: `required_if:section,club|numeric|min:0|max:10080` (~28).
  - Используется: `ComputerReservations::gapMinutes()` (~23–28): `max(0, (int) round((float) value))`, нечисловое → 30. Применяется в `create()` (поиск конфликтов `[starts-gap, ends+gap]`, проверка оплаченной сессии) и в выборках «ПК зарезервирован» (~97, ~109, ~121); показывается на экране броней (`ComputerReservationController.php` ~44).
- [ ] **`club.max_cashiers_on_shift`** — int, лимит одновременно вошедших в панель кассиров; `0` — без лимита.
  - Дефолт: `0`.
  - Валидация: `sometimes|integer|min:0|max:100` (~29).
  - Используется: `app/Services/StaffShifts.php` (`limit()`, `hasPlace()`; ключи в `StaffShifts::SETTINGS`) — при входе в панель `app/Http/Controllers/AuthController.php` (~110); на экране сотрудников `WorkerController.php` (~36–42). «На смене» = есть живая web-сессия в таблице `sessions` за `SESSION_LIFETIME` минут (работает только при `SESSION_DRIVER=database`, иначе лимит не действует). В Go сессии будут в Valkey — механизм подсчёта «на смене» нужно переопределить (Q-S5).
- [ ] **`club.max_operators_on_shift`** — то же для роли `operator`. Дефолт `0`, валидация `sometimes|integer|min:0|max:100` (~30).
- [ ] **`club.agent_idle_logout_minutes`** — число минут: клиент, вошедший на ПК без оплаченной сессии, разлогинивается через N минут; `0` — никогда.
  - Дефолт: `GuestFeatures::DEFAULT_IDLE_LOGOUT_MINUTES = 5` (`app/Services/GuestFeatures.php`).
  - Валидация: `sometimes|numeric|min:0|max:1440` (~52); в форме `step=1`.
  - Используется: `GuestFeatures::idleLogoutMinutes()` — `max(0, (int) ceil((float) value))`; отдаётся агенту в heartbeat `club.idle_logout_minutes` (`ClubComputerController.php` ~469). Таймер — на стороне агента.
- [ ] **`club.agent_shop_enabled`** — bool, магазин в агенте.
  - Дефолт: `true`.
  - Валидация: `nullable|boolean` (~53).
  - Используется: `GuestFeatures::shopEnabled()`; `Api/GuestShopController.php` (~17, при выключенном — отказ); heartbeat `club.guest_features.shop` (`ClubComputerController.php` ~467).
- [ ] **`club.agent_auto_update`** — bool, служба агента сама ставит новую версию.
  - Дефолт: `true`.
  - Валидация: `nullable|boolean` (~54).
  - Используется: `app/Services/AgentUpdate.php` (`clubAllowsAutoUpdate()`, ~112–115) → `offerFor()` (см. 60-integrations, «Обновления агента»).

#### Колонки `clubs`, которые по смыслу — настройки клуба (редактируются на `/settings/club` и `/settings/languages`)

- [ ] **`clubs.timezone`** — string(64), IANA-зона. Дефолт колонки `'Asia/Almaty'` (`2026_10_14_060000_add_timezone_to_clubs_table.php`). Валидация `required|string|timezone:all` (`SettingsController::timezone()`, ~38–55). Аудит `club.timezone_changed`. Список зон: сначала Казахстан, затем все по смещению (`timezoneOptions()`, ~109–124). Используется везде для отображения времени (`CurrentClub::timezone()`), в снимках загрузки (`SnapshotClubLoad`).
- [ ] **`clubs.locale`** — string(16) nullable, язык интерфейса клуба. Дефолт при `null` — `LocaleResolver::DEFAULT_CLUB_LOCALE = 'ru'` (`app/Services/LocaleResolver.php` ~17, `forClub()` ~27–32); если язык выключен — fallback. Валидация `required|in:<включённые коды>` (`app/Http/Controllers/LanguageController.php`, `select()`), маршрут `POST /settings/language`. Аудит `club.language_changed`.
- [ ] **`clubs.agent_exit_pin_hash` / `agent_exit_pin_changed_at`** — PIN сотрудника для выхода из агента, хранится хэшем (`Hash::make`, bcrypt). Валидация `nullable|string|regex:/^\d{4,8}$/|confirmed`; пустое значение удаляет PIN (`SettingsController::agentPin()`, ~58–82, `POST /settings/agent-pin`, право `workers.update`). Аудит `club.agent_pin_changed`. В Go — Argon2id; старые bcrypt-хэши переносить и перехэшировать при первой успешной проверке (Q-S6).
- [ ] **`clubs.agent_staff_minutes`** — unsigned smallint, сколько минут сотрудник может работать на ПК до возврата агента. Дефолт колонки `15` (`2026_10_14_220000_add_agent_staff_visits.php`), константа `ClubComputerSessions::DEFAULT_STAFF_MINUTES = 15`. Валидация `required|integer|min:1|max:240` (`SettingsController::agentStaffTime()`, ~85–102, `POST /settings/agent-staff-time`). Используется `ClubComputerSessions::startStaffVisit()` (~236–242).
- [ ] **`clubs.logo_path`** — логотип: `required|file|mimes:png,jpg,jpeg|max:1024 КБ|dimensions:min 32×32, max 4096×4096` (`app/Http/Controllers/ClubLogoController.php` ~38–39), диск `local`, публичный URL с именем файла (`Club::logoUrl()`). Отдаётся агенту в heartbeat.
- [ ] **`clubs.signin_image_path`** — картинка экрана входа агента: `mimes:png,jpg,jpeg|max:3072 КБ|dimensions:min 640×360, max 4096×4096` (`ClubSignInImageController.php` ~38–41). URL — `Club::signInImageUrl()`.
- [ ] **`clubs.installation_key` (+ `_hash`, `_prefix`)** — ключ установки агента; показывается на `/settings/club` (блок «Ключ установки клуба»), только чтение; создаётся в `Club::booted()` (`Str::random(64)`, хранится `encrypted` + sha256 + префикс 12 символов), изменение запрещено (`LogicException`). Описывается подробнее в разделе агента; в настройках — только отображение.

### 40.2. Секция `finances` (`clubs.settings.finances`)

Экран: `/settings/finances` (`resources/views/settings/finances.blade.php`), блоки «Валюта», «Оплата времени», «Накопительная скидка», «Бонусы за пополнение».
Формулы расчётов — в `docs/business-rules.md` (другой раздел); здесь — только схема ключей.

- [ ] **`finances.currency`** — enum-строка `KZT|RUB|$`.
  - Дефолт: `'KZT'` (продублирован в ~10 местах через `data_get(..., 'finances.currency', 'KZT')`).
  - Валидация: `required_if:section,finances|in:KZT,RUB,$` (~31).
  - Используется только как **подпись** (на расчёты не влияет): `TariffController.php` (~37 — копируется в `tariffs.currency` при создании тарифа, ~108), `DashboardController.php` (~64), `AgentTariffPurchaseService.php` (~121), `Api/AuthController.php` (~232, ~258), `Api/GuestMinutesController.php` (~34), `Api/Staff/CashController.php` (~29), `Api/Staff/MeController.php` (~36, ~76), `Http/Resources/UserResource.php` (~25), `finances.blade.php` (подписи «Потратил от, :currency»).
- [ ] **`finances.loyalty_enabled`** — bool, накопительная скидка включена.
  - Дефолт: `false`. Валидация: `nullable|boolean` (~32).
  - Используется: `app/Services/LoyaltyProgram.php` (`enabled()`, `levels()` — при выключенной уровни = `[]`).
- [ ] **`finances.loyalty_levels`** — JSON-массив `[{ "spent": number, "percent": number }]`.
  - `spent` — порог накопленных трат **в тенге** (не тиынах!), число с ≤ 2 знаками после запятой, хранится int, если целое, иначе float; `percent` — 0.01…100, ≤ 2 знака.
  - Нормализация `PercentLevels::normalize($levels, 'spent')` (`app/Support/PercentLevels.php` ~12–26): отбросить строки без числовых полей, округлить до 0.01, отсортировать по порогу.
  - Дефолт: `[]`.
  - Валидация (~33–36): `nullable|array|max:20` (`LoyaltyProgram::MAX_LEVELS = 20`); элемент `array:spent,percent`; `spent`: `required|numeric|min:0.01|max:999999999|distinct`; `percent`: `required|numeric|min:0.01|max:100`. Сообщения — `SaveSettingsRequest::messages()`.
  - Используется: `LoyaltyProgram::percentFor()` → `PercentLevels::percentAt()` (сравнение в центах: `cents >= round(spent*100)`, берётся max процент достигнутых уровней); `discountFor()` = `max(membership.discount, percentFor)`; применяется в `AgentTariffPurchaseService.php` (~149, ~366) через `discounted()` (~371–376: базисные пункты `round(percent*100)`, `intdiv(price*bp + 5000, 10000)`); показ в приложении сотрудника `Api/Staff/CustomerController.php` (~35).
  - История: миграция `2026_10_18_100000_backfill_loyalty_spending.php` переводила черновой формат `loyalty_rules [{threshold, discount}]`, `loyalty_type`, `tax_rate` в текущий и удаляла эти ключи. В данных MySQL их уже быть не должно, но инструмент переноса должен это проверить (Q-S2).
- [ ] **`finances.topup_bonus_enabled`** — bool. Дефолт `false`. Валидация `nullable|boolean` (~43). Используется `app/Services/TopUpBonus.php` (`enabled()`).
- [ ] **`finances.topup_bonus_levels`** — JSON-массив `[{ "amount": number, "percent": number }]`, `amount` — порог суммы пополнения **в тенге** (≤ 2 знака), `percent` 0.01…100.
  - Нормализация: `PercentLevels::normalize($levels, 'amount')`.
  - Дефолт: `[]`.
  - Валидация (~44–47): `nullable|array|max:20` (`TopUpBonus::MAX_LEVELS = 20`); `array:amount,percent`; `amount`: `required|numeric|min:0.01|max:999999999|distinct`; `percent`: `required|numeric|min:0.01|max:100`.
  - Используется: `TopUpBonus::for(float $amount)` (~29–35): `cents = round(amount*100)`, бонус = `intdiv(cents * round(percent*100) + 5000, 10000)` (округление половины вверх до тиына). Вызовы: `CashRegisterController::topUpBalance()` (~73–76), `Api/Staff/CashController::topUp()` (~57–60), `CashRegisterService` (конструктор).
- [ ] **`finances.discount_scope`** — enum-строка `tariffs|hourly|all` (`PaymentRules::DISCOUNT_SCOPES`, `app/Services/PaymentRules.php` ~13).
  - Дефолт: `'tariffs'` (и при неизвестном значении — `discountScope()`, ~17–22).
  - Валидация: `sometimes|in:tariffs,hourly,all` (~37).
  - Используется: `discountOnTariffs()` (`!== 'hourly'`) → `AgentTariffPurchaseService.php` ~365 (только тарифы с `discount_enabled`); `discountOnHourly()` (`!== 'tariffs'`) → `AgentTariffPurchaseService.php` ~145 (поминутная оплата).
- [ ] **`finances.bonus_max_percent`** — int 0…100, какой % цены можно оплатить бонусами; `0` — бонусы не платят за время.
  - Дефолт: `100`.
  - Валидация: `sometimes|integer|min:0|max:100` (~38); при чтении зажимается `max(0, min(100, (int)))`, нечисловое → 100 (`PaymentRules::bonusMaxPercent()`, ~35–40).
  - Используется: `bonusLimitCents(price) = intdiv(price * percent, 100)` (~84–87) → `AgentTariffPurchaseService.php` ~241 (кроме тарифов `main_balance_only`); `bonusPercentOffered()` (~76–81: если `< 100` и смешанная оплата выключена — агенту отдаётся 0) → `AgentTariffPurchaseService.php` ~353–354, `Api/GuestMinutesController.php` ~32; поле `bonus_max_percent` есть в модели агента `WindowsAgent/ZafiroAgent/Models/Tariff.cs` ~30.
- [ ] **`finances.allow_mixed_payment`** — bool, бонусы за часть цены + остаток с основного баланса. Дефолт `false`. Валидация `nullable|boolean` (~39). Используется `PaymentRules::mixedPaymentAllowed()` → `AgentTariffPurchaseService.php` ~242, ~355; `GuestMinutesController.php` ~33.
- [ ] **`finances.allow_whole_balance`** — bool, агент предлагает купить минуты на весь основной баланс. Дефолт `false`. Валидация `nullable|boolean` (~40). Используется `PaymentRules::wholeBalanceAllowed()` → `GuestMinutesController.php` ~38.
- [ ] **`finances.auto_billing`** — bool, поминутная оплата начинается сама после входа (после паузы) и после окончания тарифа. Дефолт `false`. Валидация `nullable|boolean` (~41). Используется `PaymentRules::autoBillingEnabled()` → `GuestMinutesController.php` ~40 (флаг для агента; покупку минут инициирует агент).
- [ ] **`finances.auto_billing_pause_minutes`** — int 0…60, пауза после входа до начала поминутной оплаты.
  - Дефолт: `5`.
  - Валидация: `sometimes|integer|min:0|max:60` (~42); при чтении зажимается 0…60, нечисловое → 5 (`PaymentRules::autoBillingPauseMinutes()`, ~53–58).
  - Используется: `GuestMinutesController.php` ~41.

### 40.3. Секция `guests` (`clubs.settings.guests`)

Экран: `/settings/guests` (`resources/views/settings/guests.blade.php`). Чтение — `app/Services/GuestFeatures.php` (`enabled()` через `FILTER_VALIDATE_BOOLEAN`, отсутствующее значение → `false`, кроме `guest_mode_enabled`). Все пять ключей — bool, валидация `nullable|boolean` (`SaveSettingsRequest` ~48–51, ~55). Четыре ключа отдаются агенту в heartbeat `club.guest_features` (`GuestFeatures::all()`, `ClubComputerController.php` ~467).

- [ ] **`guests.show_qr_code_on_login`** (фича `qr_login`) — показывать QR-код на экране входа агента. Дефолт `false`. Используется в heartbeat (агент решает показывать QR).
- [ ] **`guests.allow_guest_self_transfer`** (фича `self_transfer`) — самостоятельная пересадка клиента. Дефолт `false`. `GuestFeatures::ensure('self_transfer')` → `Api/GuestSeatController.php` ~20, ~28 (403 «Эта возможность отключена в клубе.»).
- [ ] **`guests.allow_adding_friends`** (фича `friends`) — друзья. Дефолт `false`. `ensure('friends')` → `Api/GuestFriendController.php` ~30, ~48.
- [ ] **`guests.allow_balance_transfer_to_friends`** (фича `balance_transfer`) — перевод баланса друзьям. Дефолт `false`. `ensure('balance_transfer')` → `GuestFriendController.php` ~63, ~84, ~98.
- [ ] **`guests.guest_mode_enabled`** — продажа времени на кассе гостю без аккаунта. Дефолт **`true`**. Используется `GuestFeatures::guestModeEnabled()` → `GuestSessionController.php` ~18, `Api/Staff/ComputerController.php` ~59, `Api/Staff/MeController.php` ~28 (влияет на набор прав в `StaffPermissions::for()`).

### 40.4. Сводка ключей `clubs.settings`

| Ключ | Тип | Дефолт | Диапазон |
|---|---|---|---|
| club.session_idle_timeout | минуты (num) | 15 | 0…10080 (не используется) |
| club.pc_action_after_session_end | enum | none | none/restart/shutdown |
| club.pc_restart_after_session_end | минуты (num) | 0.5 | 0…10080 |
| club.session_auto_terminate_when_pc_unavailable | минуты (num) | 5 | 0…10080 (не используется) |
| club.booking_interval | минуты (num) | 30 | 0…10080 |
| club.max_cashiers_on_shift | int | 0 | 0…100 |
| club.max_operators_on_shift | int | 0 | 0…100 |
| club.agent_idle_logout_minutes | минуты (num) | 5 | 0…1440 |
| club.agent_shop_enabled | bool | true | |
| club.agent_auto_update | bool | true | |
| finances.currency | enum | KZT | KZT/RUB/$ |
| finances.loyalty_enabled | bool | false | |
| finances.loyalty_levels | JSON `[{spent,percent}]` | [] | ≤20, spent 0.01…999999999 ₸, percent 0.01…100 |
| finances.topup_bonus_enabled | bool | false | |
| finances.topup_bonus_levels | JSON `[{amount,percent}]` | [] | ≤20, amount 0.01…999999999 ₸, percent 0.01…100 |
| finances.discount_scope | enum | tariffs | tariffs/hourly/all |
| finances.bonus_max_percent | int % | 100 | 0…100 |
| finances.allow_mixed_payment | bool | false | |
| finances.allow_whole_balance | bool | false | |
| finances.auto_billing | bool | false | |
| finances.auto_billing_pause_minutes | int мин | 5 | 0…60 |
| guests.show_qr_code_on_login | bool | false | |
| guests.allow_guest_self_transfer | bool | false | |
| guests.allow_adding_friends | bool | false | |
| guests.allow_balance_transfer_to_friends | bool | false | |
| guests.guest_mode_enabled | bool | true | |

Итого: **26 ключей** в `clubs.settings` (10 `club` + 11 `finances` + 5 `guests`) и **7 колонок `clubs`** с ролью настроек.

Предложение для Go:
- [ ] Типизированная структура `ClubSettings` (Go) + zod-схема (web) из одного источника (OpenAPI-схема), хранение в `clubs.settings jsonb`. Дефолты применяются в одном месте при чтении. Нормализацию «старых» строковых минут — один раз в `tools/migrate-from-mysql`.
- [ ] Пороги уровней (`spent`, `amount`) при переносе можно конвертировать в тиыны (int64), чтобы не было float в денежных порогах; сравнение `cents >= round(x*100)` эквивалентно. Решение — Q-S7.
- [ ] Кэш настроек клуба — в памяти процесса/Valkey с инвалидацией через событие NATS `club.settings.changed` (сейчас кэша нет: настройки читаются из уже загруженной модели клуба в каждом запросе).
- [ ] Изменения настроек — в аудит-журнал (как `settings.updated` с диффом) и в push агентам (сейчас агенты узнают изменения только со следующим heartbeat).

### 40.5. Платформенные настройки (суперадминка): таблица `platform_settings`

Таблица `platform_settings(key varchar(64) PK, value json null, updated_by FK users, timestamps)` (`database/migrations/2026_10_15_120000_create_platform_settings_table.php`), модель `app/Models/PlatformSetting.php`: `read()` кэширует навсегда (`platform-setting:<key>`), `write()` сбрасывает кэш; `null` удаляет строку.

- [ ] **`agent_release`** (`PlatformSetting::AGENT_RELEASE`) — объявленная версия агента: `{latest_version: "X.Y.Z", released_at: date, download_url: https|null, auto_update: bool, installer_url: string|null, installer_sha256: hex64|null}`.
  - Пишется: `app/Http/Controllers/Superadmin/AgentReleaseController.php` (`update()`: `latest_version` `required|regex:/^\d{1,4}\.\d{1,4}\.\d{1,6}$/`, `released_at` `required|date`, `download_url` `nullable|url:https|max:500`, `auto_update` `nullable|boolean`; при `auto_update` URL установщика строится по шаблону `AgentUpdate::INSTALLER_URL`, sha256 скачивается из `<url>.sha256`, без него — ошибка). `destroy()` — снять объявление. Журнал `PlatformLogger`: `superadmin.agent_release_set` / `_cleared`.
  - Фолбэк, если строки нет: `config('club.agent')` (env `AGENT_LATEST_VERSION`, `AGENT_RELEASED_AT`, `AGENT_DOWNLOAD_URL`), `AgentUpdate::release()` (~79–82). В фолбэке нет `auto_update`, т. е. автообновление из env невозможно.
  - Читается: `AgentUpdate` (`status()`, `autoInstaller()`, `offerFor()`), `Superadmin/DashboardController.php` (~103). Экран: дашборд суперадминки (`resources/views/superadmin/dashboard.blade.php`).
- [ ] **`telegram_gateway`** (`TelegramGateway::SETTING`) — `{token: <Crypt::encryptString>}`. Пишется `Superadmin/TelegramController::update()` (`token` `required|string|max:255|regex:/^[A-Za-z0-9_\-:.]{20,255}$/`, проверка токена запросом `checkVerificationStatus`), удаляется `destroy()`. Читается **мимо кэша** (`TelegramGateway::token()`). Экран `/superadmin/telegram` (`resources/views/superadmin/telegram.blade.php`).
- [ ] **`telegram_alerts`** (`TelegramAlerts::SETTING`) — `{token: <encrypted>, chat_id: string, locale: string}`. Пишется `TelegramController::updateAlerts()` (`bot_token` `regex:/^\d{5,15}:[A-Za-z0-9_\-]{30,100}$/`, `chat_id` `nullable|regex:/^(-?\d{1,20}|@[A-Za-z0-9_]{5,32})$/`; без chat_id берётся последний чат из `getUpdates`; отправляется тестовое сообщение), удаляется `destroyAlerts()`. Читается мимо кэша.
- [ ] **`telegram_bot`** — устаревший ключ (бот Zafiro), удаляется миграцией `2026_10_18_120000_telegram_gateway_password_recovery.php` (~29). Не переносить.
- [ ] **Секреты**: значения `token` зашифрованы `APP_KEY` (Laravel Crypt, AES-256-CBC + HMAC). При переносе в Go нужно расшифровать старым ключом и перешифровать новым механизмом (Q-S8); проще — попросить суперадмина ввести токены заново.
- [ ] **Языки** (платформенно): таблица `languages(code, name, is_enabled, is_system, sort_order)` — включённые языки влияют на выбор языка клубами и агентом (`Superadmin/LanguageController.php`: `code` `regex:/^[a-z]{2,3}(-[a-z0-9]{2,8})?$/`; нельзя выключить язык, выбранный каким-либо клубом, и язык по умолчанию `ru`). Подробно — в 60-integrations («Переводы»).
- [ ] **Тарифные планы SaaS** (`saas_plans`, `/superadmin/plans`) — данные, а не настройки; описываются в разделе подписок.

### 40.6. Конфигурация `config/*.php` (поведение, не секреты)

- [ ] **`config/club.php`**: `computer_heartbeat_interval_seconds` (env `COMPUTER_HEARTBEAT_INTERVAL_SECONDS`, 30; отдаётся агенту как `recommended_interval_seconds = max(5, value)`, `ClubComputerController.php` ~438); `agent.latest_version|released_at|download_url` (env `AGENT_*`, фолбэк для `agent_release`).
- [ ] **`config/integrations.php`** — всё про API v1 и вебхуки (подробно в 60-integrations):
  `scopes` (9 прав: `products:read`, `products:write`, `stock:write`, `sales:read`, `computers:read`, `bookings:write`, `computers:control`*, `tariffs:read`, `members:read`* — * «опасные»), `webhook_events` (6 событий), `key_prefix = 'zfk_'`, `key_cache_ttl_seconds = 300`, `default_rate_limit_per_minute` (env `API_KEY_RATE_LIMIT`, 120), `max_rate_limit_per_minute` (env `API_KEY_RATE_LIMIT_MAX`, 1000), `club_rate_limit_per_minute` (env `API_CLUB_RATE_LIMIT`, 600), `failed_auth_per_minute_per_ip = 20`, `max_request_kb = 256`, `max_bulk_items = 50`, `request_log_retention_days = 90`, `idempotency_retention_hours = 24`, `webhook_retry_backoff_seconds = [10, 60, 300, 1800, 7200]`, `webhook_timeout_seconds = 5`, `webhook_signature_tolerance_seconds = 300`, `webhook_disable_after_failures = 20`, `webhook_delivery_retention_days = 30`, `webhook_require_https` (env `WEBHOOK_REQUIRE_HTTPS`, true), `webhook_allow_private_hosts` (env `WEBHOOK_ALLOW_PRIVATE_HOSTS`, false).
- [ ] **`config/monitoring.php`**: `record_requests` (env `MONITORING_RECORD_REQUESTS`, true), `requests_path` (env `MONITORING_REQUESTS_PATH`, `storage/framework/metrics`), `retention_days = 30`, `backup_path` (env `BACKUP_PATH`), `certificate_hosts` (env `MONITORING_SSL_HOSTS`, CSV; пусто → хосты `APP_URL`, `SUPERADMIN_DOMAIN`, `WIKI_DOMAIN`), пороги алертов: `scheduler_silent_minutes = 5`, `disk_percent = 85`, `error_rate_percent = 5`, `error_rate_min_errors = 10`, `queue_pending = 100`, `queue_oldest_minutes = 10`.
- [ ] **`config/app.php`**: `superadmin_domain` (env `SUPERADMIN_DOMAIN`; пусто — суперадминка на `/superadmin`, `app/Support/SuperadminHost.php`, `RestrictSuperadminHost`), `wiki_domain` (env `WIKI_DOMAIN`; `RestrictWikiHost`), `timezone = 'UTC'` (хранение в UTC), `locale` (env `APP_LOCALE`).
- [ ] **`config/session.php`**: `SESSION_DRIVER=database` и `SESSION_LIFETIME=120` влияют на бизнес-логику (лимит сотрудников на смене, `StaffShifts::onShift()`; сброс сессий при восстановлении пароля, `PasswordRecovery::reset()`).
- [ ] **`config/queue.php`**: `QUEUE_CONNECTION=database`, `DB_QUEUE_RETRY_AFTER=90` — очередь вебхуков (заменяется, см. 50-jobs).
- [ ] **`config/cache.php`**: `CACHE_STORE=database` — общий кэш (ключи API, лимиты, состояния алертов, пакеты переводов); `integrations:prune` чистит протухшие строки `cache` только при этом драйвере. В Go → Valkey.
- [ ] **`config/services.php`, `config/mail.php`**: postmark/resend/ses/slack и SMTP — **шаблонные, в коде не используются** (`Mail::`, `Notification::` не встречаются в `app/`). `MAIL_MAILER=log`. Не переносить (Q-I6).
- [ ] **Жёстко заданные константы с влиянием на поведение** (кандидаты в конфиг Go): `AgentUpdate::MAX_OFFERS = 3`, `OFFER_INTERVAL_MINUTES = 120`, `INSTALLER_URL`; `PasswordRecovery`/`FriendTransferCodes`: `CODE_MINUTES = 5`, `RESEND_SECONDS = 60`, `CODES_PER_HOUR = 5`, `MAX_ATTEMPTS = 5`; `IntegrationController::MAX_ACTIVE_KEYS = 50`, `MAX_WEBHOOKS = 20`; `ClubComputer::HEARTBEAT_TIMEOUT_SECONDS = 90`; `SnapshotClubLoad::OFFLINE_ALERT_MIN_ONLINE = 3`; `ClubAttention`: `OFFLINE_MIN_ONLINE = 3`, `WEBHOOK_FAILURES = 3`, `SUBSCRIPTION_DAYS = 3`; лимиты частоты `RateLimiter::for(...)` в `AppServiceProvider::boot()` (~47–97).

### 40.7. Переменные `.env.example` / `env()` (только имена)

- [ ] Влияют на поведение приложения: `APP_URL`, `APP_ENV`, `APP_DEBUG`, `APP_LOCALE`, `APP_FALLBACK_LOCALE`, `SUPERADMIN_DOMAIN`, `WIKI_DOMAIN`, `COMPUTER_HEARTBEAT_INTERVAL_SECONDS`, `AGENT_LATEST_VERSION`, `AGENT_RELEASED_AT`, `AGENT_DOWNLOAD_URL`, `API_KEY_RATE_LIMIT`, `API_KEY_RATE_LIMIT_MAX`, `API_CLUB_RATE_LIMIT`, `WEBHOOK_REQUIRE_HTTPS`, `WEBHOOK_ALLOW_PRIVATE_HOSTS`, `MONITORING_RECORD_REQUESTS`, `MONITORING_REQUESTS_PATH`, `MONITORING_SSL_HOSTS`, `BACKUP_PATH`, `SESSION_DRIVER`, `SESSION_LIFETIME`, `QUEUE_CONNECTION`, `CACHE_STORE`, `BCRYPT_ROUNDS`, `LOG_*`.
- [ ] Секреты/инфраструктура (в Go заменяются своими): `APP_KEY`, `APP_PREVIOUS_KEYS` (шифрование `clubs.installation_key`, `webhook_endpoints.secret`, Telegram-токенов), `DB_*`, `REDIS_*`, `MAIL_*`, `AWS_*`, `POSTMARK_*`, `RESEND_API_KEY`, `SLACK_*`, `MEMCACHED_*`, `SQS_*`, `DYNAMODB_*`, `BEANSTALKD_*`.
- [ ] Предлагаемые переменные Go (koanf, префикс `ZAFIRO_`): эквиваленты первой группы + `ZAFIRO_ENCRYPTION_KEY` (AES-GCM для токенов/секретов вебхуков), `ZAFIRO_LEGACY_APP_KEY` (только для `tools/migrate-from-mysql`, расшифровка старых значений).

## 50. Фоновые задачи, очередь, события

> Раздел инвентаризации Этапа 0. Ссылки — на старый репозиторий `../Zafiro`.
> Открытые вопросы — `docs/open-questions/settings-jobs-integrations.md`.

### 50.0. Как это устроено сейчас

- [ ] **Планировщик** — `routes/console.php` (Laravel Scheduler). Запускается cron'ом раз в минуту: `* * * * * cd /home/admzafiro/Zafiro && php artisan schedule:run` (`CLAUDE.md`, раздел «Выкладка»). Демонов нет.
- [ ] **Очередь** — драйвер `database` (`QUEUE_CONNECTION=database`, таблицы `jobs`, `failed_jobs`; `config/queue.php`, `retry_after = 90`). Воркер — короткоживущий `queue:work --stop-when-empty --max-time=50 --sleep=1`, запускается планировщиком каждую минуту. Единственный job-класс — `App\Jobs\DeliverWebhook`.
- [ ] **Блокировки от двойного запуска**: `withoutOverlapping(N)` — мьютекс в кэше (`CACHE_STORE=database`, таблица `cache_locks`) с истечением через N минут. **`onOneServer()` нигде не используется** — архитектура рассчитана на один сервер. `runInBackground()` нет: задачи одной минуты идут **последовательно** в одном процессе `schedule:run`, поэтому порядок в файле важен (комментарий в `routes/console.php`: `monitoring:collect` первым, чтобы его не задерживал воркер очереди до 50 с).
- [ ] **Events/Listeners/Observers**: каталогов `app/Events`, `app/Listeners`, `app/Observers` нет. Есть только Eloquent-хуки моделей (п. 50.4) и `terminate()`-middleware (п. 50.3).
- [ ] **Сторож планировщика**: `app/Services/Monitoring/SchedulerWatchdog.php` — на HTTP-запросах (не чаще раза в минуту, маркер-файл) проверяет, что последняя строка `server_metrics` не старше `monitoring.scheduler_silent_minutes = 5`; иначе Telegram-алерт «Планировщик молчит…».

#### Общая схема в Go (предложение)

- [ ] Пакет `internal/jobs`: реестр задач `{name, interval/cron, timeout, singleton bool, fn}`; запуск в том же бинарнике (`zafiro serve` поднимает воркеры) и опционально отдельной ролью (`zafiro worker`).
- [ ] **Один исполнитель на кластер** для периодических задач — Postgres advisory lock: в начале тика `SELECT pg_try_advisory_lock(hashtext('job:<name>'))` на выделенном соединении (или `pg_try_advisory_xact_lock` внутри транзакции задачи). Не взял — пропускает тик. Это заменяет `withoutOverlapping` + `onOneServer` и не требует отдельного leader election. Альтернатива — лиза в NATS JetStream KV (`kv.Create` с TTL) — если захотим не держать соединение БД. Рекомендация: advisory lock (Postgres есть всегда, при падении процесса лок освобождается сам).
- [ ] **Тикер** — `time.Ticker` с выравниванием на границу минуты + джиттер; метрики `job_runs_total`, `job_duration_seconds`, `job_last_success_timestamp` (замена `SchedulerWatchdog`: алерт, если `last_success` старше порога).
- [ ] **Очередь** — без отдельной таблицы `jobs`: доменные таблицы-«outbox» (`webhook_deliveries`) + выборка `FOR UPDATE SKIP LOCKED` по `next_attempt_at`; NATS JetStream используется для «разбудить воркер сейчас» и для событий между инстансами (`club.settings.changed`, `computer.state.changed`). Так ни одно сообщение не теряется при падении NATS (источник правды — Postgres), а несколько инстансов не берут одну доставку.
- [ ] Graceful shutdown: задачи получают `ctx`, при SIGTERM дорабатывают текущую порцию (батч) и выходят.

### 50.1. Расписание `routes/console.php`

- [ ] **`monitoring:collect`** — `everyMinute()->withoutOverlapping(5)`. Класс `app/Console/Commands/CollectServerMetrics.php`.
  - Делает: (1) `RequestRecorder::drain(<текущая минута>)` — читает файлы `storage/framework/metrics/requests-<minute>.log` завершившихся минут, суммирует по каналам (`panel|agent|api|guest`) и клубам, удаляет файлы; (2) пишет `request_metrics` (upsert «прибавить», по `minute+channel`) и `club_request_metrics` (по `club_id+minute`, вставка пачками по 500, только существующие клубы); (3) `ServerProbe::snapshot()` → `insertOrIgnore` в `server_metrics` (load avg, CPU, RAM из `/proc`, диск, uptime, размер БД из `information_schema`, длина очереди из `jobs`/`failed_jobs`); (4) алерты через `Alerts::update()`: `scheduler` (восстановлен), `disk` (> 85 %), `errors` (5xx ≥ 5 % и ≥ 10 ошибок за 5 мин), `queue` (≥ 100 ждут или старейшая ≥ 10 мин).
  - Таблицы: `server_metrics` (1 строка/мин), `request_metrics` (≤ 4 строки/мин), `club_request_metrics` (≤ 1 строка на активный клуб в минуту: при 1 000 клубов ≈ 1,44 млн строк/сутки, хранится 2 суток).
  - Go: тикер 1 мин, singleton. Счётчики запросов — **в памяти каждого инстанса** (atomic/шардированная map), сброс раз в минуту `INSERT ... ON CONFLICT (minute, channel) DO UPDATE SET requests = requests + EXCLUDED.requests` — каждый инстанс пишет сам (не singleton), файлов нет. Снимок сервера — singleton по хосту (а не по кластеру): метрики хоста лучше отдавать в Prometheus, а `server_metrics` оставить для страницы суперадминки (Q-J3). Размер БД — `pg_database_size()`, очередь — число `webhook_deliveries` в `pending` и возраст старейшей.
- [ ] **`monitoring:clubs`** — `everyFiveMinutes()->withoutOverlapping(5)`. `app/Console/Commands/SnapshotClubLoad.php`.
  - Делает: агрегат по `club_computers` всех клубов (`COUNT`, online = `last_seen_at >= now-90с`, busy = есть сессия и `session_ends_at > now`) → `club_load_snapshots` (одна строка на клуб: `weekday`, `hour` в часовом поясе клуба — для тепловой карты); алерт `club-offline.<id>`: все ПК клуба офлайн, если 2–12 мин назад было ≥ 3 онлайн и подписка активна; «снова в сети» — при восстановлении.
  - Объём: 288 строк/клуб/сутки; при 1 000 клубов ≈ 8,6 млн строк за 30 дней хранения.
  - Go: тикер 5 мин, singleton. Онлайн-статус ПК в новой архитектуре живёт в памяти/Valkey (heartbeat не пишется в Postgres) → снимок берётся из реестра подключений агентов (gRPC-стримы) через Valkey, а не из `club_computers.last_seen_at`. Таблицу партиционировать по месяцам.
- [ ] **`monitoring:certificates`** — `hourly()`, без защиты от наложения. `app/Console/Commands/CheckCertificates.php` → `app/Services/Monitoring/Certificates.php::refresh()`: TLS-соединение к `host:443` каждого хоста (`MONITORING_SSL_HOSTS` или хосты `APP_URL`/`SUPERADMIN_DOMAIN`/`WIKI_DOMAIN`), дата окончания сертификата → кэш `monitoring.certificates` навсегда (страница суперадминки сама наружу не ходит). Алерта по сроку нет — только отображение.
  - Go: тикер 1 ч, singleton, `crypto/tls.Dial` с `InsecureSkipVerify` только для чтения `NotAfter`; результат — в Valkey. Добавить алерт «< 14 дней» (Q-J4).
- [ ] **`monitoring:prune`** — `daily()`. `app/Console/Commands/PruneMonitoringData.php`: удаляет `server_metrics`, `request_metrics`, `club_load_snapshots` старше 30 дней (`monitoring.retention_days`), `club_request_metrics` старше 2 дней. Одним `DELETE` без батчей.
  - Go: при партиционировании по месяцам/дням — `DROP`/`DETACH PARTITION` (singleton, ежедневно); для `club_request_metrics` — дневные партиции или Valkey вместо таблицы (данные нужны только «за последние минуты»).
- [ ] **`queue:work --stop-when-empty --max-time=50 --sleep=1`** — `everyMinute()->withoutOverlapping(5)`. Обрабатывает очередь `default` (только `DeliverWebhook`). Задержка доставки вебхука до ~1 мин + время в очереди.
  - Go: постоянный воркер доставки вебхуков (п. 50.2), не singleton — масштабируется по инстансам через `SKIP LOCKED`.
- [ ] **`sanctum:prune-expired --hours=24`** — `daily()`. Удаляет из `personal_access_tokens` токены, истёкшие > 24 ч назад (токены агента/мобилок/QR-входа на 30 дней: `Api/AuthController.php` ~114, ~179, `AgentRegisterController.php` ~64, `QrLogins.php` ~66).
  - Go: Sanctum не переносится. Refresh-токены — в Postgres (`auth_refresh_tokens`, с `expires_at`) или Valkey с TTL. Если Postgres — ежедневная чистка singleton; если Valkey — не нужна.
- [ ] **`club-computers:expire-sessions`** — `everyMinute()`, **без** `withoutOverlapping`. `app/Console/Commands/ExpireComputerSessions.php` → `ClubComputerSessions::expireAll()` (`app/Services/ClubComputerSessions.php` ~189–195): один `UPDATE club_computers SET current_session_id=NULL, current_session_user_id=NULL, session_guest=0, session_ends_at=NULL, updated_at=now WHERE session_ends_at <= now()` по всем клубам. Без расчёта возврата (время израсходовано полностью) и без записи в журнал действий. Нужна, чтобы сессия на **офлайн-ПК** закончилась вовремя.
  - Точность: до 1 мин (cron) + длительность предыдущих задач минуты.
  - Go: singleton-тикер **каждые 5–10 с** (дешёвый `UPDATE ... WHERE session_ends_at <= now() RETURNING club_id, id` по индексу `session_ends_at`) + публикация `computer.session.expired` в NATS → push в админку (WebSocket) и агенту (стрим). Альтернатива — таймер в памяти на каждую сессию у инстанса-владельца клуба; отклонена: теряется при рестарте. Q-J1: нужна ли запись в журнал/ledger при естественном окончании.
- [ ] **`integrations:prune`** — `hourly()->withoutOverlapping()`. `app/Console/Commands/PruneIntegrationData.php`: батчами по 1 000 id удаляет `api_request_logs` старше 90 дн., `api_idempotency_keys` старше 24 ч, `webhook_deliveries` в статусах `delivered|failed|skipped` старше 30 дн.; при `CACHE_STORE=database` — до 50 000 протухших строк `cache`.
  - Go: singleton, ежечасно, те же сроки и батчи (`DELETE ... WHERE id IN (SELECT id ... LIMIT 1000)`); `api_request_logs` — партиции по месяцам и `DROP PARTITION`; чистка `cache` не нужна (Valkey с TTL).
- [ ] **`session-purchases:prune`** (замыкание) — `daily()`. Удаляет `session_purchases` старше 8 дней, кроме принадлежащих текущим сессиям (`current_session_id` любого ПК), и `time_packages` с `expires_at < now()` (сохранённые минуты тарифа с ограничением использования сгорают).
  - Важно для денег: удаление истёкших пакетов = клиент теряет минуты. В Go — singleton, ежедневно, **с записью в ledger/аудит** списания истёкших пакетов (Q-J2), иначе сверка балансов минут невозможна.
- [ ] **`agent-qr-logins:prune`** (замыкание) — `daily()`. Удаляет `agent_qr_logins`, истёкшие более суток назад (QR живёт 5 мин).
  - Go: QR-коды входа — в Valkey с TTL 5 мин; задача не нужна. Если останутся в Postgres — ежедневная чистка singleton.
- [ ] **`subscriptions:expire`** — `everyFiveMinutes()->withoutOverlapping()`. `app/Console/Commands/ExpireSubscriptions.php`: для `club_subscriptions` со `status='active'` и `current_period_ends_at <= now()` → `status='expired'` в транзакции + `PlatformLogger::record('subscription.expired', ...)`. Доступ закрывается и без неё (`ClubSubscription::isActive()` сравнивает дату), задача только выравнивает статус. Триалы (`trialing`) не трогает.
  - Побочный эффект: `ClubSubscription::saved` сбрасывает кэш API-ключей клуба (`AppServiceProvider` ~113).
  - Go: singleton, 5 мин (или 1 мин), тот же запрос + событие `club.subscription.changed` в NATS (сброс кэшей, отключение агентов/API). Q-J5: выравнивать ли так же `trialing → expired`.

### 50.2. Очередь: `App\Jobs\DeliverWebhook`

- [ ] **Постановка**: `WebhookDispatcher::dispatch($event, $data)` (`app/Services/Integrations/WebhookDispatcher.php` ~24–51) создаёт строку `webhook_deliveries` (`status=pending`, `next_attempt_at=now`) **внутри транзакции бизнес-операции** и ставит `DeliverWebhook::dispatch($id)->afterCommit()`. Повтор вручную — `IntegrationController::retryDelivery()` (~188–200).
- [ ] **Выполнение** (`app/Jobs/DeliverWebhook.php`): пропуск, если доставка уже финальна; `skipped`, если endpoint удалён/выключен; SSRF-проверка `WebhookUrlGuard::check()` с «пришпиливанием» IP (`CURLOPT_RESOLVE`); POST с таймаутом 5 с (connect 3 с), без редиректов; подпись `X-Zafiro-Signature: v1=hex(HMAC-SHA256(secret, "{timestamp}.{body}"))`, заголовки `X-Zafiro-Event`, `X-Zafiro-Delivery` (uuid события), `X-Zafiro-Timestamp`, `User-Agent: Zafiro-Webhooks/1`.
- [ ] **Ретраи**: `$tries = 10` (страховка), фактически — `release($delay)` по списку `webhook_retry_backoff_seconds = [10, 60, 300, 1800, 7200]` → до 6 попыток за ~2,6 ч; затем `failed`, `endpoint.failure_count++`; при 20 подряд неудачных доставках endpoint выключается (`is_active=false`) и сбрасывается кэш слушателей. Успех обнуляет `failure_count`.
- [ ] Из-за модели «воркер раз в минуту на 50 с» задержка `release(10)` фактически 10–60 с.
- [ ] Go: воркер `webhooks.Deliverer` (N горутин на инстанс): `SELECT ... FROM webhook_deliveries WHERE status='pending' AND next_attempt_at <= now() ORDER BY next_attempt_at LIMIT 50 FOR UPDATE SKIP LOCKED` → отправка вне транзакции с «арендой» (`locked_until`) → обновление статуса. Пробуждение по NATS-сообщению `webhooks.enqueued` (публикуется после commit) + опрос раз в 5 с как страховка. Семантика (подпись, бэкофф, отключение после 20 ошибок, SSRF-guard с пиннингом IP через `net.Dialer.Control`) — один в один. Индекс `(status, next_attempt_at)` уже есть.

### 50.3. Работа «на хвосте» HTTP-запроса

- [ ] **`RecordRequestMetrics::terminate()`** (`app/Http/Middleware/RecordRequestMetrics.php`, глобальный middleware, `bootstrap/app.php`): после ответа дописывает строку `канал\tclub\tms\tstatus` в файл минуты (`RequestRecorder::record()`) и вызывает `SchedulerWatchdog::check()`. Отключается `MONITORING_RECORD_REQUESTS=false`.
  - Go: middleware chi с in-memory счётчиками (см. `monitoring:collect`), без файлов. Сторож — метрика последнего успешного запуска задач + алерт из singleton-воркера на **другом** инстансе (или внешний Prometheus Alertmanager).
- [ ] **`ApiKey last_used_at`** — обновляется не чаще раза в минуту на ключ (`AuthenticateApiKey::touch()`, флаг в кэше). Go: батч-флаш раз в минуту из памяти.
- [ ] **`LogApiRequest`** — синхронная вставка в `api_request_logs` на каждый запрос `/api/v1`. Go: буферизованная пачечная запись (канал + `COPY`/multi-insert раз в 1 с), ошибки записи не ломают запрос (как сейчас).

### 50.4. Хуки моделей и «ленивые» истечения (без задач)

- [ ] **`AppServiceProvider::invalidateApiKeyCache()`** (~101–124): `ClubMembership::saved/deleted` (смена роли/права интеграций) → сброс кэша ключей пользователя; `ClubSubscription::saved/deleted` и `Club::saved` (смена `name|slug|locale|timezone|settings`) → сброс кэша всех ключей клуба; `WebhookEndpoint::saved/deleted` → сброс кэша слушателей вебхуков клуба. Go: явные вызовы инвалидации в сервисном слое + событие NATS `cache.invalidate.*`, чтобы сбросились кэши всех инстансов.
- [ ] **`Club::booted()`** (`app/Models/Club.php` ~25–45): генерация ключа установки при создании, запрет его изменения. Go: в сервисе создания клуба + CHECK/триггер.
- [ ] **`BelongsToClub`** (`app/Models/Concerns/BelongsToClub.php`): глобальный scope `club` и проставление `club_id` при создании. Go: sqlc-запросы с `club_id` + RLS.
- [ ] **Ленивые истечения** (проверяются при чтении, отдельной задачи нет): визит сотрудника на ПК (`club_computers.staff_until`), брони (`computer_reservations` по времени), коды восстановления пароля и переводов (`password_recovery_codes`, `transfer_codes` — старше суток удаляются только при следующем запросе того же пользователя, `PasswordRecovery::sendCode()` ~47, `FriendTransferCodes::send()`), алерты (`Alerts` в кэше навсегда). В Go коды — Valkey с TTL; для `staff_until` и броней — по-прежнему проверка при чтении + событие по таймеру для push-уведомления (Q-J6).

### 50.5. Ручные команды artisan

- [ ] **`translations:sync`** — `SyncTranslations` → `TranslationCatalog::sync()`: добавляет в `translations` недостающие строки из `database/translations/{code}.php` для всех языков, не трогая отредактированные; при вставке — `flush()` (новая версия пакетов). Запускается при каждой выкладке. Go: команда `zafiro translations sync` + автоматически при старте (под advisory lock), источник — embed-файлы.
- [ ] **`agent:translations`** — `ExportAgentTranslations`: пишет `WindowsAgent/Frontend/i18n/{ru,kk,en}.json` из группы `agent` (встроенные в агента тексты). Go: генератор в `task gen` (или `go generate`), выход — в репозиторий агента.
- [ ] **`superadmin:grant {phone} {--revoke}`** — `GrantSuperadmin`: выдать/снять `users.is_superadmin` по телефону (`+7` + 10 цифр). Go: `zafiro admin grant-superadmin --phone ... [--revoke]` + запись в платформенный журнал (сейчас только `Log::warning`).
- [ ] **`inspire`** — демонстрационная команда Laravel. Не переносить.

### 50.6. Сводка

| # | Задача | Частота | Защита | Go: тип | Singleton |
|---|---|---|---|---|---|
| 1 | monitoring:collect | 1 мин | withoutOverlapping(5) | тикер (+ in-memory счётчики на каждом инстансе) | снимок — да |
| 2 | monitoring:clubs | 5 мин | withoutOverlapping(5) | тикер | да |
| 3 | monitoring:certificates | 1 ч | — | тикер | да |
| 4 | monitoring:prune | 1 д | — | тикер / DROP PARTITION | да |
| 5 | queue:work (вебхуки) | 1 мин, ≤50 с | withoutOverlapping(5) | постоянный воркер, SKIP LOCKED + NATS wakeup | нет |
| 6 | sanctum:prune-expired | 1 д | — | тикер или TTL Valkey | да |
| 7 | club-computers:expire-sessions | 1 мин | — | тикер 5–10 с + NATS-событие | да |
| 8 | integrations:prune | 1 ч | withoutOverlapping | тикер | да |
| 9 | session-purchases:prune | 1 д | — | тикер + ledger | да |
| 10 | agent-qr-logins:prune | 1 д | — | TTL Valkey | — |
| 11 | subscriptions:expire | 5 мин | withoutOverlapping | тикер + NATS-событие | да |
| 12 | DeliverWebhook (job) | по событию | tries=10, бэкофф 5 шагов | см. №5 | нет |
| 13 | RecordRequestMetrics/SchedulerWatchdog | каждый запрос | — | middleware + метрики | нет |
| 14 | translations:sync | вручную/выкладка | — | CLI + при старте | lock |
| 15 | agent:translations | вручную | — | генератор | — |
| 16 | superadmin:grant | вручную | — | CLI | — |

Итого: **11 записей расписания**, **1 job-класс очереди**, **3 рабочие ручные команды** (+ `inspire`), **2 механизма на хвосте запроса**, **3 группы хуков моделей**, ленивые истечения — 5 видов.

## 60. Интеграции

> Раздел инвентаризации Этапа 0. Ссылки — на старый репозиторий `../Zafiro`.
> Открытые вопросы — `docs/open-questions/settings-jobs-integrations.md`.
> Секреты указаны **только именами** (где хранятся), значения не приводятся.

### 60.1. Telegram

В проекте **три** разных механизма с Telegram; бота для клиентов/клубов нет, входящих вебхуков от Telegram нет (сервер только ходит наружу).

- [ ] **Telegram Gateway — коды подтверждения** (`app/Services/TelegramGateway.php`).
  - API: `POST https://gatewayapi.telegram.org/{method}` с `Authorization: Bearer <token>`, таймаут 10 с. Методы: `checkVerificationStatus` (проверка токена при сохранении: `REQUEST_ID_INVALID` = токен валиден, `ACCESS_TOKEN*` = нет), `sendVerificationMessage {phone_number (E.164), code, ttl}` — Gateway возвращает деньги, если не доставлено за `ttl`.
  - Секрет: токен Gateway — `platform_settings['telegram_gateway'].token`, зашифрован `Crypt` (ключ `APP_KEY`), читается мимо кэша, в логи не пишется (логируется только имя метода и класс исключения; телефон тоже не логируется). `bootstrap/app.php` — `dontFlash(['token','bot_token'])`.
  - Настройка: `/superadmin/telegram` (`Superadmin/TelegramController::update()/destroy()`), журнал `superadmin.telegram_gateway_set|_cleared`. Счётчик отправленных за 30 дней — по `platform_logs.event = 'user.password_code_sent'`.
  - Потребители:
    - [ ] **Восстановление пароля** (`app/Services/PasswordRecovery.php`): 6-значный код (`random_int(100000, 999999)`), живёт `CODE_MINUTES = 5`, повтор не раньше `RESEND_SECONDS = 60`, не более `CODES_PER_HOUR = 5` в час на аккаунт, `MAX_ATTEMPTS = 5` неверных вводов на код; хранится bcrypt-хэш в `password_recovery_codes`; неизвестный/заблокированный номер получает тот же ответ (нет перебора аккаунтов). Успех: новый пароль, удаление всех Sanctum-токенов и web-сессий пользователя, удаление кодов, журнал `user.password_recovered`. Маршруты: web `GET/POST /forgot-password`, `GET/POST /reset-password` (`routes/web.php` ~159–163); агент `POST /api/password/code`, `POST /api/password/reset` (`routes/api.php` ~45–51); лимитер `password-recovery`: 10/мин на IP+телефон, 30/мин на IP (`AppServiceProvider` ~72–75).
    - [ ] **Подтверждение перевода другу из мобильного приложения** (`app/Services/FriendTransferCodes.php`): код привязан к другу и сумме, одноразовый; те же константы 5 мин / 60 с / 5 в час / 5 попыток; таблица `transfer_codes`. Маршруты `POST /api/guest/friends/{friendship}/transfer/code|confirm` (`routes/api.php` ~139–141, throttle 5/мин и 10/мин). Мобилки на этом этапе не переносим — Q-I3.
  - Go: клиент `internal/integrations/telegram/gateway` (net/http, таймаут 10 с, без логирования тела/токена), токен в `platform_settings` с шифрованием AES-GCM ключом из env. Коды — в Postgres (как сейчас) или Valkey с TTL; хэш кода — достаточно HMAC-SHA256 с секретом сервера (коды короткоживущие) или Argon2id. **Переносится как есть** (логика лимитов — буквально).
- [ ] **Бот оповещений о сбоях** (`app/Services/Monitoring/TelegramAlerts.php`).
  - API: `https://api.telegram.org/bot<token>/{getMe|getUpdates|sendMessage}`, таймаут 10 с. Токен — часть URL, поэтому исключения логируются только классом.
  - Секрет: `platform_settings['telegram_alerts'] = {token (Crypt), chat_id, locale}`.
  - Настройка: `/superadmin/telegram` (`updateAlerts`, `testAlerts` с `throttle:5,1`, `destroyAlerts`); без `chat_id` берётся последний чат из `getUpdates` (нужно написать боту `/start`); тестовое сообщение при подключении.
  - Отправка: только через `app/Services/Monitoring/Alerts.php::update()` — сообщение один раз при начале проблемы (`⚠️`) и один раз при окончании (`✅`); состояние «проблема открыта» — ключ кэша `monitoring.alert.<key>` навсегда. Язык — того суперадмина, что подключил бота. Ключи алертов: `scheduler`, `disk`, `errors`, `queue`, `club-offline.<club_id>`.
  - Go: тот же клиент Bot API; состояние алертов — в Valkey (`SET NX`), чтобы при нескольких инстансах сообщение ушло один раз. **Переносится как есть**; при наличии Prometheus/Alertmanager можно позже заменить (Q-I4).
- [ ] **Старый бот Zafiro для клиентов** — удалён. Остался заглушечный `GET /api/guest/telegram` → `{available:false, linked:false}` (`app/Http/Controllers/Api/GuestTelegramController.php`) для агентов < 0.0.57; ключ `platform_settings.telegram_bot` удаляется миграцией `2026_10_18_120000_telegram_gateway_password_recovery.php`. **Не переносить** (API агента проектируется заново).

### 60.2. Вебхуки (исходящие)

- [ ] **Модель данных**: `webhook_endpoints(club_id, created_by, url ≤500, secret text (encrypted cast), events json, is_active, failure_count, last_success_at, last_failure_at)`, `webhook_deliveries(club_id, webhook_endpoint_id, event_id uuid unique, event, payload json, status pending|delivered|failed|skipped, attempts, last_status_code, last_error ≤255, next_attempt_at, delivered_at)` — `database/migrations/2026_10_13_190912_create_integration_api_tables.php` (~66–96). Индексы: `(club_id, is_active)`, `(webhook_endpoint_id, created_at)`, `(status, next_attempt_at)`.
- [ ] **Подписки/управление**: `/settings/integrations` (`app/Http/Controllers/IntegrationController.php`, middleware `subscription` + `can:manage-integrations` = владелец или член с `club_memberships.can_manage_integrations`). Создание: `url` `required|string|max:500|url:https,http` + `WebhookUrlGuard`, `events` `required|array|min:1|in:<config>`; лимит `MAX_WEBHOOKS = 20` на клуб; секрет `whsec_` + 40 символов показывается **один раз**. Также: изменение (URL/события/вкл-выкл; включение обнуляет `failure_count`), перевыпуск секрета, удаление, повтор доставки `failed|skipped`. Последние 20 доставок на экране. Аудит: `webhook.created|updated|secret_rotated|deleted|retried`.
- [ ] **События** (`config/integrations.php` → `webhook_events`) и места генерации (`WebhookDispatcher::dispatch()` внутри транзакции):
  - `product.created` — `app/Actions/CreateProductAction.php` ~52;
  - `product.updated` — `app/Actions/UpdateProductAction.php` ~37;
  - `stock.changed` — `CreateProductAction.php` ~49, `AdjustStockAction.php` ~56, `CashRegisterService.php` ~62, ~226, `AgentShop.php` ~80, ~137;
  - `sale.completed` — `CashRegisterService.php` ~81;
  - `computer.reservation_changed` — `ClubComputerController.php` ~166, `Api/V1/ComputerController.php` ~60;
  - `computer.controlled` — `ClubComputerController.php` ~196, `Api/V1/ComputerController.php` ~100.
  - Конверт: `{id: uuid, event, created_at ISO8601, club_id, data}`; правило: в `data` нет секретов и персональных данных (`CLAUDE.md`, раздел API).
- [ ] **Отправка, подпись, ретраи** — `app/Jobs/DeliverWebhook.php`, подробно в `50-jobs.md` п. 50.2. Подпись `v1=hex(HMAC-SHA256(secret, "{timestamp}.{body}"))`; допуск по времени для получателя — `webhook_signature_tolerance_seconds = 300` (только документирован, проверяет получатель). Бэкофф `[10, 60, 300, 1800, 7200]` с, отключение после 20 неудач подряд.
- [ ] **SSRF-защита** — `app/Services/Integrations/WebhookUrlGuard.php`: только http(s), без user:pass, HTTPS обязателен (`WEBHOOK_REQUIRE_HTTPS`), порты 80/443/8080/8443, все A/AAAA — публичные (`WEBHOOK_ALLOW_PRIVATE_HOSTS=false`), запрос пришпиливается к проверенному IP; редиректы запрещены.
- [ ] **Кэш слушателей**: `webhooks.listeners.<club_id>` на 600 с, сброс при изменении endpoint'ов (хук в `AppServiceProvider`).
- [ ] **Секреты**: `webhook_endpoints.secret` — зашифрован `APP_KEY` (Laravel `encrypted` cast); нужен в открытом виде для HMAC, поэтому в Go — симметричное шифрование (AES-GCM, ключ из env, версионирование ключа), а не хэш. При переносе — расшифровать старым `APP_KEY` в `tools/migrate-from-mysql` (Q-S8).
- [ ] Go: **переносится как есть** (формат подписи и заголовков — тот же, чтобы интеграторам ничего не менять), отправка — воркер с `SKIP LOCKED` + NATS-пробуждение. Решение о сохранении функции — Q-I1.

### 60.3. Интеграционный API v1 и API-ключи

- [ ] **Маршруты** (`routes/api_v1.php`, префикс `/api/v1`, подключение `routes/api.php` ~179–183; цепочка middleware `api.log → api.size → api.key → api.limit`, приоритет до model binding — `bootstrap/app.php` ~53–57; ошибки в своём формате — `app/Support/ApiError.php`, `bootstrap/app.php` ~69). Контракт — `resources/api/openapi-v1.yaml`, Swagger UI — `GET /docs/api` (`routes/web.php` ~127–128). Подробное описание — `docs/integration-api.md` старого репо.
  - `GET /me` (any); `GET /products`, `/products/{id}`, `/product-categories`, `/stock-movements` (`products:read`); `POST /products`, `PATCH /products/{id}` (`products:write` + idempotent optional); `POST /stock-movements` (`stock:write` + idempotent **required**); `GET /sales`, `/sales/{id}` (`sales:read`); `GET /zones`, `/computers`, `/computers/{id}` (`computers:read`); `PUT /computers/{id}/reservation` (`bookings:write` + idempotent); `POST /computers/control` (`computers:control` + idempotent required); `GET /tariffs` (`tariffs:read`); `GET /members` (`members:read`). Итого 17 маршрутов. Контроллеры — `app/Http/Controllers/Api/V1/*`, сериализация — `Transformers.php`.
- [ ] **Ключи** (`app/Services/Integrations/ApiKeyManager.php`, таблица `api_keys`):
  - Формат `zfk_<8 [a-z0-9]>_<40 [A-Za-z0-9]>`; хранится только `sha256(полный ключ)` (`key_hash`) и уникальный `prefix` (`zfk_xxxxxxxx`); секрет показывается один раз.
  - Поля: `name`, `scopes json`, `allowed_ips json` (IP/CIDR, `IpUtils::checkIp`), `rate_limit_per_minute` (1…`max_rate_limit_per_minute`), `expires_at` (> сегодня), `last_used_at/ip`, `revoked_at/by`, `rotated_from_id`.
  - Проверка `verify()`: regexp → кэш метаданных по префиксу (300 с; «нет такого» — 60 с) → `hash_equals` → отозван / истёк / клуб удалён / **владелец ключа потерял право интеграций** / подписка неактивна (402).
  - Ротация: новый ключ с теми же параметрами в том же клубе + отзыв старого в одной транзакции. Лимит `MAX_ACTIVE_KEYS = 50` на клуб. Аудит `api_key.created|rotated|revoked`, `integrations.permission_changed`.
  - Клуб берётся **только** из ключа; `X-Club-Id`/`club_id` в запросе, отличный от клуба ключа, → 403 `tenant_override_forbidden` (`AuthenticateApiKey::overridesTenant()`). Маршрут без `api.scope:*` недоступен (`declaresScope()`, default deny).
  - Токен принимается из `Authorization: Bearer` или `X-Api-Key`, длина ≤ 128.
- [ ] **Scopes** — 9 прав из `config/integrations.php` (`computers:control` и `members:read` помечены опасными); проверка `RequireApiScope` (403 `insufficient_scope` + `required_scopes`).
- [ ] **Лимиты**: неудачная аутентификация — 20/мин на IP (только `invalid_api_key`), ключ — `rate_limit_per_minute` или 120 (≤ 1000), клуб — 600/мин; фиксированное окно в минуту (`Cache::increment`), заголовки `X-RateLimit-Limit/Remaining`, `Retry-After` (`app/Http/Middleware/Api/ApiRateLimit.php`). Размер тела ≤ 256 КБ (`LimitApiRequestSize`), пачка ≤ 50 элементов (`max_bulk_items`).
- [ ] **Идемпотентность** (`app/Http/Middleware/Api/ApiIdempotency.php`, таблица `api_idempotency_keys`, уникальность `(api_key_id, idempotency_key)`): заголовок `Idempotency-Key` `[A-Za-z0-9_:-]{1,100}`; хэш `sha256(METHOD path\nbody)`; другой запрос с тем же ключом → 422 `idempotency_key_reused`; параллельный → 409 `idempotency_in_progress`; повтор → сохранённый ответ + `Idempotent-Replayed: true`; ответ 5xx или исключение — запись удаляется (можно повторить); `required` без заголовка → 428. Хранение 24 ч.
- [ ] **Журнал запросов** — `api_request_logs` (метод, endpoint ≤191, статус, длительность, IP, префикс ключа; 90 дней), показ последних 30 на `/settings/integrations`.
- [ ] **Связь с настройками клуба**: в кэш метаданных кладётся копия `clubs` (`id, name, slug, locale, timezone, settings`), запрос выполняется от «виртуального» клуба без запроса в БД, часовой пояс — UTC.
- [ ] Go: схема ключей переносится как есть (sha256 от высокоэнтропийного ключа достаточно; Argon2 не нужен), кэш метаданных — Valkey + локальный LRU с инвалидацией по NATS; rate limit — Valkey (`INCR` + `EXPIRE`, то же окно); идемпотентность — Postgres (как сейчас) или Valkey. OpenAPI v1 — в `oapi-codegen` отдельным спецом. Нужно ли API v1 в прежнем виде — Q-I1.

### 60.4. Обновления агента

- [ ] **Источник версии**: `platform_settings['agent_release']` (суперадмин, `Superadmin/AgentReleaseController.php`) или фолбэк env `AGENT_LATEST_VERSION|AGENT_RELEASED_AT|AGENT_DOWNLOAD_URL` (`config/club.php`). Подробно — `40-settings.md` п. 40.5.
- [ ] **Откуда файлы**: GitHub Releases старого репо, шаблон `AgentUpdate::INSTALLER_URL = https://github.com/aitmaganbetov/Zafiro/releases/download/agent-v{version}/ZafiroAgentSetup-{version}.exe`; рядом `.sha256` (`"<hex>  <имя>"`), публикуется CI `.github/workflows/windows-agent.yml` (тег `agent-vX.Y.Z`, ~115–183). Сервер при объявлении скачивает `.sha256` (таймаут 15 с) и сохраняет хэш; без хэша автообновление не включается.
- [ ] **Подписи**: только SHA-256 файла, переданный сервером в heartbeat. **Цифровой подписи (Authenticode/Ed25519) нет** — доверие держится на TLS до сервера и GitHub. Требование ТЗ: обновления подписаны Ed25519 → новое (Q-I2).
- [ ] **Каналы**: один «канал» — последняя объявленная версия на всю платформу; поклубного/поэтапного (canary) выката нет; клуб может только выключить автообновление (`settings.club.agent_auto_update`).
- [ ] **Доставка предложения**: `AgentUpdate::offerFor()` (`app/Services/AgentUpdate.php` ~124–149) в ответе heartbeat (`agent_update: {version, url, sha256}`, `ClubComputerController.php` ~479) — только если: релиз с `auto_update=true`, URL `https://`, sha256 валиден, клуб разрешил, версия агента ниже (`version_compare`, нечитаемая версия = старая), на ПК нет оплаченной сессии и `agent_state` не `idle|in_session`; не чаще `MAX_OFFERS = 3` раз на версию с интервалом `OFFER_INTERVAL_MINUTES = 120` (учёт в `club_computers.agent_update_version|offers|offered_at` — для бездисковых ПК).
- [ ] **Агент** (`WindowsAgent/ZafiroAgent/Service/AgentUpdater.cs`): принимает URL только с `github.com`, `objects.githubusercontent.com`, `release-assets.githubusercontent.com`; ≤ 300 МБ; сверяет SHA-256 открытого на чтение файла; тихая установка Inno Setup `/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP- /MERGETASKS="!protect"`; одна попытка на версию в час; старые установщики удаляет.
- [ ] **Уведомление в панели**: `AgentUpdate::status()` — сколько ПК клуба со старой версией (выборка всех `agent_version` клуба в PHP — в Go агрегат в SQL), ссылка на скачивание только `https://`.
- [ ] Go: новый механизм — манифест релиза `{version, channel, url, sha256, size, ed25519_signature}`, подпись ключом из CI (публичный ключ вшит в агент), доставка предложения по gRPC-стриму, хранение файлов — GitHub Releases нового репо или собственное хранилище (Q-I2). Логику «только свободный ПК», «3 предложения/120 мин», флаг клуба — **перенести как есть**.

### 60.5. Переводы (локализация)

- [ ] **Источник**: `database/translations/{ru,kk,en}.php` (по 2 274 строки; ≈ 1 924 ключа группы `*` и ≈ 324 ключа группы `agent`; ключ — русский текст). Русский — исходный язык (`TranslationCatalog::SOURCE_LOCALE = 'ru'`), фолбэк — `en`.
- [ ] **Хранение**: `languages(code, name, is_enabled, is_system, sort_order)`, `translations(language_id, group, key text, key_hash char(40), value)`, уникальность `(language_id, group, key_hash)` (`2026_10_13_181908_create_languages_and_translations_tables.php`); `Translation::hashKey(group, key)`.
- [ ] **`app/Services/TranslationCatalog.php`**: кэш `translations.state {version, enabled, groups}` + пакеты `translations.v{N}.pack.{code}` (все группы языка, слитые поверх английского фолбэка, TTL 7 дней, построение под `Cache::lock`); любое изменение → `flush()` увеличивает версию (агент перечитывает тексты по `translations_version` в heartbeat). `sync()` — см. `50-jobs.md` (`translations:sync`). `sourceKeys()` — для подсчёта прогресса перевода.
- [ ] **`app/Translation/DatabaseTranslationLoader.php`**: строки из БД поверх файловых переводов Laravel (сообщения валидации фреймворка).
- [ ] **`app/Services/TranslationTransfer.php`**: экспорт/импорт CSV (`;`, колонки `id(=key_hash); group; ru; en; <code>`), до 10 000 строк, пустые ячейки не удаляют перевод при импорте, строки с несовпадающими плейсхолдерами `:name` отклоняются с отчётом, защита от CSV-формул (`= + - @` → апостроф). Маршруты суперадминки: `GET /languages/export`, `POST /languages/import` (`file` `mimes:csv,txt|max:2048`, `throttle:20,1`), `GET/PUT /languages/{language}/translations` (до 200 значений за запрос, ≤ 5000 символов) — `app/Http/Controllers/Superadmin/LanguageController.php`, экраны `resources/views/superadmin/languages.blade.php`, `translations.blade.php`.
- [ ] **Агент**: `GET /api/agent/translations/{locale}` (`Api/AgentTranslationController.php`; `throttle:agent-translations` 600/мин на IP; `Cache-Control: public, max-age=300`) → `{locale, version, lines}` группы `agent`; встроенные тексты агента — `agent:translations` → `WindowsAgent/Frontend/i18n/*.json`.
- [ ] Go: те же таблицы; embed-файлы источника (конвертировать PHP-массивы в JSON/YAML один раз, генератор в `tools/`); кэш пакетов в памяти процесса с версией в Valkey + NATS-событие `translations.changed`; фронт React — i18next с загрузкой пакета по версии; агент — через gRPC `GetTranslations` + push версии. Плюрализация: ru — 3 формы, kk — 1, en — 2 (`trans_choice` с `|`) — сохранить формат строк, i18next настроить на тот же синтаксис или конвертировать (Q-I5). **Переносится как есть.**

### 60.6. Мониторинг (`app/Services/Monitoring/*`)

- [ ] `RequestRecorder` — счётчики запросов в файлы по минутам (см. 50-jobs).
- [ ] `ServerProbe` — `/proc/loadavg|meminfo|cpuinfo|uptime`, диск, размер БД и 8 крупнейших таблиц (MySQL `information_schema`), очередь (`jobs`, `failed_jobs`). В Go: `pg_database_size`, `pg_total_relation_size`; очередь — `webhook_deliveries`.
- [ ] `Certificates` — сроки TLS-сертификатов (ежечасно).
- [ ] `ClubAttention` — «клубы, требующие внимания» на дашборде суперадминки: онлайн ПК упал вдвое за 15 мин (было ≥ 3), ≥ 3 неудачных вебхуков за сутки, подписка/триал заканчивается в ближайшие 3 дня.
- [ ] `ErrorLog` — последние 10 ошибок из хвоста `storage/logs/laravel.log` (256 КБ). В Go: slog JSON → кольцевой буфер последних ошибок в памяти/Valkey или ссылка на внешний сбор логов (Q-I4).
- [ ] `Alerts`, `TelegramAlerts`, `SchedulerWatchdog` — см. 60.1 и 50-jobs.
- [ ] Экран — дашборд суперадминки «Сервер» (`Superadmin/DashboardController.php`, `resources/views/superadmin/dashboard.blade.php`). Секретов нет (кроме токена бота).

### 60.7. Новости

- [ ] `app/Services/NewsManager.php`: новости платформы (`club_id = null`, суперадминка `/platform-news`, `Superadmin/NewsController.php`) и клуба (`/news`, `NewsController.php`). Валидация: `title ≤150`, `body ≤5000`, `is_published`, картинка `png|jpg|jpeg ≤2 МБ, 64×64…4096×4096`, файл `news/<club|platform>/<random40>.<ext>` на диске `local`; публичная выдача `GET /news-images/{news}/{file}` (`routes/web.php` ~141–143).
- [ ] Агент: `GET /api/guest/news` (`Api/GuestNewsController.php`, throttle 30/мин) → `{platform: [...], club: [...]}` (по `GuestNewsController::LIMIT = 10` последних опубликованных).
- [ ] Go: перенос как есть; файлы — локальный диск/объектное хранилище (S3-совместимое) — Q-I7. Внешних сервисов нет.

### 60.8. Почта, SMS

- [ ] **Почта не используется**: `MAIL_MAILER=log`, в `app/` нет `Mail::`, `Notification::`, `Mailable`. Конфиги `config/mail.php`, `config/services.php` (postmark, resend, ses, slack) — шаблон Laravel. Приглашения сотрудников (`/invitations/{token}`) передаются ссылкой вручную.
- [ ] **SMS нет**: все коды — через Telegram Gateway.
- [ ] Go: не переносить; заложить интерфейс `Notifier` на будущее (Q-I6).

### 60.9. Прочее, касающееся интеграций

- [ ] **`GET /api/settings/{section}/{key}`** и экран `/settings/api` — справочник URL для чтения настроек и товаров мобильным приложением (`SettingsController::api()`, `showSetting()`). Q-S3.
- [ ] **`GET /api/products`** — товары для мобильного приложения (любая роль клуба, без пагинации; `docs/integration-api.md` раздел 1). Относится к API мобилок — отложено.
- [ ] **Swagger UI** `/docs/api` (`resources/views/api-docs.blade.php`) — `swagger-ui-dist@5.17.14` с jsDelivr с SRI-хэшами; в Go — отдать ту же версию (лучше вшить через `embed.FS`, без CDN).

### 60.10. Сводка

| # | Интеграция | Внешняя сторона | Секреты (имя/место) | Перенос |
|---|---|---|---|---|
| 1 | Telegram Gateway: восстановление пароля | gatewayapi.telegram.org | `platform_settings.telegram_gateway.token` (Crypt/APP_KEY) | как есть |
| 2 | Telegram Gateway: код перевода другу | то же | то же | вместе с мобилками (Q-I3) |
| 3 | Telegram-бот алертов | api.telegram.org | `platform_settings.telegram_alerts.token` | как есть |
| 4 | Заглушка `/api/guest/telegram` | — | — | не переносить |
| 5 | Исходящие вебхуки | URL клиента | `webhook_endpoints.secret` (encrypted/APP_KEY) | как есть (Q-I1) |
| 6 | API v1 + ключи + scopes + идемпотентность + лимиты + журнал | внешние системы клуба | `api_keys.key_hash` (sha256) | решение Q-I1 |
| 7 | Обновления агента | GitHub Releases | — (sha256), подписи нет | переделать с Ed25519 (Q-I2) |
| 8 | Переводы (каталог, CSV, sync, агент) | — | — | как есть |
| 9 | Мониторинг сервера/клубов | TLS-хосты | — | частично заменить метриками |
| 10 | Новости | — | — | как есть |
| 11 | Почта/SMS | — | `MAIL_*` не используются | не переносить |
| 12 | `/api/settings/*` для мобилок | — | — | Q-S3 |

Итого: **12 интеграционных узлов** (из них внешних сервисов — 3: Telegram Gateway, Telegram Bot API, GitHub Releases; плюс исходящие вебхуки к произвольным URL).
