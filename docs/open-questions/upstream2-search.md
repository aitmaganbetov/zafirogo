# Перенос старого Zafiro 35c36a0: поиск клиентов на странице «Пользователи»

Источник: `app/Http/Controllers/UserController.php index()`, `resources/views/users.blade.php`,
`tests/Feature/UsersTest.php` («the user list finds customers by name, phone in any format or email»).
Решения приняты исполнителем по принципам `docs/DECISIONS.md` (владелец поручил).

## U2S-1. Параметр поиска — `q`, а не `search`

В старом — `?search=`. В REST (`api/customers.yaml`) и в URL страницы (`/users?q=`) поиск уже был
параметром `q` (общий `common.yaml#/components/parameters/Search`, как во всех списках нового API).
Оставлен `q`; поле формы называется `search`, как в Blade, но форма переходит на `/users?q=…`.
Старые ссылки `?search=` не поддерживаются (новый SPA, закладок на старый URL нет — домены разные).

## U2S-2. `%` и `_` в запросе ищутся буквально

Laravel подставлял строку в `LIKE '%'.$search.'%'` без экранирования: `%` находил всех, `_` — любой символ.
Это не задуманное поведение, а недосмотр; в новом шаблон экранируется (`httpx.LikePattern`, как во всех
поисках zafirogo). Разница видна только на запросах с `%`/`_` — в именах и телефонах их почти не бывает,
в email `_` встречается: `ivan_p` теперь находит только `ivan_p…`, а не `ivanXp…`.

## U2S-3. Длина запроса — 422, как `max:100`

`'search' => ['nullable', 'string', 'max:100']` после TrimStrings: длиннее 100 символов (после trim) —
422 с ошибкой поля `q` (в старом — редирект назад с ошибкой валидации). Поле формы — `maxlength="100"`.

## U2S-4. Регистр: ILIKE

MySQL сравнивал `LIKE` по collation `utf8mb4_unicode_ci` — без учёта регистра (тест «DANA@» находит
dana@example.com). В Postgres — `ILIKE` для имени и email. Телефон — `LIKE` по цифрам (регистра нет).
Отличие collation: `unicode_ci` считал равными ё/е и буквы с диакритикой, ILIKE — нет. Принято (как в
остальных поисках zafirogo, кассе и бронях).

## U2S-5. Индекс pg_trgm на users(email) — миграция 0043

name и phone уже под GIN pg_trgm (0032). Без индекса на email условие `OR email ILIKE` заставляет читать
всю таблицу users (общая для платформы). Замер (290 тыс. пользователей платформы, клуб на 100 тыс. клиентов,
Postgres 18, план под значения — `db.customPlan`):

| запрос | без индекса email | с индексом |
|---|---|---|
| нет совпадений (`zzzz`) | Parallel Seq Scan users, 74 мс | BitmapOr по трём GIN, 4,8 мс |
| email `user12345@` + цифры 12345 | — | BitmapOr, 15 мс |
| телефон `8 700 791-90-00` | — | BitmapOr, 8,6 мс |
| частое имя `петров` (10 тыс. в клубе) | — | обход по user_id, 5,7 мс (страница 26 строк) |

Счётчик «Всего» для частого имени — 30–146 мс (хеш-соединение ~29 тыс. совпавших пользователей с членствами
клуба); без поиска счётчик не трогает users (`AdminCountCustomersAll`, PB-07). Запросы уже в
`customPlanQueries` (`server/internal/db/plan.go`), новых нет.
