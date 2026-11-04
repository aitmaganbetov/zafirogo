# 10. Экраны (resources/views) — инвентаризация

Источник: `Zafiro/resources/views` (67 Blade-шаблонов + `resources/wiki/index.html`), `routes/web.php`,
контроллеры `app/Http/Controllers`, middleware `app/Http/Middleware`, права `app/Support/ClubPermissions.php`.
Ссылки на маршруты — `routes/web.php:строка`.

## Сводка

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

## Общие правила доступа (middleware)

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

## 1. Вход и регистрация (layout `layouts/auth.blade.php`)

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

## 2. Админка клуба (layout `layouts/admin.blade.php`)

Все маршруты — в группе `auth` + `current-club` (`routes/web.php:168`). Страницы по разделам сайдбара
(`partials/sidebar.blade.php`). Общий JS каркаса (тема, уведомления, меню, часы, PWA) — раздел 6.

### 2.1 Рабочее пространство

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

### 2.2 Учёт

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

### 2.3 Система

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

## 3. Суперадминка (layout `layouts/superadmin.blade.php`, меню `partials/superadmin-sidebar.blade.php`)

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

## 4. ПК (QR-вход с телефона)

- [ ] **Подтвердить вход на ПК** — `pc-login/show.blade.php` (layout `auth`)
  - Маршруты: `GET /pc-login/{code}` `pc-login.show` → `QrLoginController@show`; `POST /pc-login/{code}` `pc-login.approve` → `@approve` (`routes/web.php:151–152`), `code` — 40 символов `[A-Za-z0-9]`.
  - Middleware: `auth` (без `current-club` — любой клуб кода); POST — `throttle:10,1,pc-login.approve`.
  - Данные: открытый QR-вход (`QrLogins::findOpen`): клуб, ПК; иначе «QR-код устарел» (5 минут).
  - Формы: «Войти на ПК» → POST approve; «Выйти из аккаунта» → `POST /logout`.
  - JS: нет (маска телефона `app.js` — на странице входа, куда редиректит `auth`). ПК узнаёт об одобрении опросом со стороны агента (`agent-qr-poll`, `routes/api.php`) — вне экранов админки.
- [ ] **Готово** — `pc-login/done.blade.php`
  - Маршрут: `GET /pc-login/done` `pc-login.done` (`routes/web.php:150`), `auth`. Статичный текст.

## 5. Публичные

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

## 6. Общие компоненты и layout

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

## Polling → WebSocket

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

## Замечания по производительности экранов (для переноса)

- [ ] Касса: все клиенты клуба и все товары в HTML без пагинации (`CashRegisterController@index`) → поиск через API, как `reservations.customers`.
- [ ] Склад: `Product::get()` всех товаров для выпадающих списков (`WarehouseController`).
- [ ] Брони: все ПК клуба для выбора (`ComputerReservationController@index`).
- [ ] Дашборд: статусы ПК считаются в PHP по всем ПК (`DashboardController` ~стр. 50).
- [ ] Переводы суперадмина: весь каталог строк в памяти и пагинация в PHP (`Superadmin\LanguageController@translations`).
- [ ] Логи: `LIKE '%…%'` по описанию и пользователю без индекса (`ActivityLogController`).
- [ ] Сайдбар: два COUNT на каждую страницу.
