# web/ — SPA (React 19 + TanStack Router/Query) — конвенции для агентов экранов

Перенос экранов Zafiro из Blade: **та же разметка и Tailwind-классы, те же тексты (ключ = русский текст)**.
Сервер и контракт API — `api/README.md`. Команды: `npm run dev | typecheck | lint | test | build | test:e2e`,
`npm run gen:api` (типы API), `npm run gen:routes` (дерево маршрутов; запускается само в typecheck/test/build).

## Раздел = маршруты + фича

| Что | Где |
|---|---|
| Маршруты (тонкие: путь, гард, loader, компонент) | `src/routes/_admin/<URI старого маршрута>/…` (панель клуба), `src/routes/_auth/…` (гость), `src/routes/<путь>.tsx` (без layout) |
| Страницы, компоненты, запросы, мутации раздела | `src/features/<раздел>/` |
| Типы API раздела (генерируются) | `src/api/gen/<раздел>.d.ts` |
| Общие компоненты (перенос `resources/views/components` и повторяющихся партиалов) | `src/components/` |
| Хелперы: деньги, даты, телефон, flash, title | `src/lib/` |

Файлы маршрутов в git не конфликтуют: `src/routeTree.gen.ts` генерируется (`.gitignore`) — vite-плагином в
dev/build и `npm run gen:routes` перед typecheck/test.

### Пути — как в `routes/web.php`

`/cash-register` → `src/routes/_admin/cash-register/index.tsx`, `/cash-register/receipts/{id}` →
`src/routes/_admin/cash-register/receipts.$id.tsx`, `/settings/club` → `src/routes/_admin/settings/club.tsx`.
`_admin` — pathless-layout: в URL его нет. Суперадминка (Q-069) — свой layout `src/routes/superadmin.tsx` +
`src/routes/superadmin/…` (группа 6). `/` — лендинг (группа 7; сейчас `routes/index.tsx` — редирект).

### Страница панели клуба

```tsx
// src/routes/_admin/todos/index.tsx
import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { todosQuery } from '../../../features/todos/queries';
import { TodosPage } from '../../../features/todos/TodosPage';

export const Route = createFileRoute('/_admin/todos/')({
  beforeLoad: requirePermission('todos.view'),          // club-area: право из /api/auth/me
  validateSearch: (s: Record<string, unknown>) => ({ date: typeof s.date === 'string' ? s.date : undefined }),
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(todosQuery(deps.date)),
  component: TodosPage,
});
```

- Гарды (`src/auth/guards.ts`): `requireClub` (уже на `_admin`), `requirePermission('раздел.действие')`,
  `requireRole(...)`, `requireStaff`. Отказ — страница 403 с текстом старого кода (`ForbiddenError`), нет данных —
  `throw notFound()` (404). Это только UI: права проверяет сервер.
- Заголовок верхней панели и `<title>` — `useAdminPageTitle(t('Касса'))` (`layouts/adminSession.ts`).
- Кнопки изменения — по праву: `can(me, 'cash.create')` (`auth/session.ts`). Режим просмотра суперадмина:
  `me.read_only` — кнопки изменения скрыть/отключить (сервер всё равно ответит 403 `read_only`).
- Ссылки — обычные `<a href="/путь">`, как в Blade: корневой маршрут перехватывает клики по ссылкам своего
  сайта и делает переход без перезагрузки. `<Link>` TanStack тоже можно, если разметка не меняется.
- Flash (`->with('status', …)`): перед переходом `setFlash('status', 'Текст')`, на странице
  `const status = useFlash('status')` и **своя** разметка блока из старого шаблона (`src/lib/flash.ts`).

## Данные — API-клиент

```ts
// src/features/cash/api.ts
import { createApiClient, unwrap, idempotencyKey } from '../../api/http';
import type { paths, components } from '../../api/gen/cash';
export const cashApi = createApiClient<paths>();
export type CashShiftState = components['schemas']['CashShiftState'];

export const shiftQuery = () => queryOptions({ queryKey: ['cash', 'shift'], queryFn: () => unwrap(cashApi.GET('/api/cash/shift')) });
await unwrap(cashApi.POST('/api/cash/sales', { params: { header: { 'Idempotency-Key': idempotencyKey() } }, body }));
```

- `src/api/http.ts`: access-токен только в памяти; на 401 — один refresh по cookie (CSRF-заголовок) и один повтор;
  ошибки — `ApiError` (`kind`: `validation` 422, `forbidden` 403, `conflict` 409, `rate_limited` 429 (`retryAfter`),
  `not_found`, `gone`, …; `e.field('amount')` — первая ошибка поля).
- Ключи Query — `['<раздел>', …]`; мутация после успеха — `invalidateQueries({ queryKey: ['<раздел>'] })` или
  `setQueryData`.
- Денежный POST — новый `Idempotency-Key` на каждую отправку формы (повтор той же отправки — тот же ключ).
- Ошибки 422 — под полем, как `@error` в Blade (`<p className="…">{t(e.field('x'))}</p>` с классами шаблона);
  общий текст — `t(e.message)`.

## Деньги и даты (`src/lib/money.ts`, `src/lib/datetime.ts`)

- Суммы — целые тиыны (`*_tiyn`). Показ — `formatMoney(tiyn, 2)` = `number_format($x, 2, ',', ' ')`;
  `formatMoney(tiyn, 0)` = `number_format($x, 0, ',', ' ')`; другие разделители — аргументами, как в шаблоне.
- Ввод — строка; `parseMoney(s)` → тиыны; > 2 знаков после точки — ошибка формы (Q-002), никакого округления.
  Тексты ошибок — `PARSE_MONEY_MESSAGES` (ключи валидатора Laravel). Никаких `parseFloat`/`Number(x) * 100`.
- Даты — в поясе клуба `me.club.timezone`: `formatDate(iso, tz, 'd.m.Y H:i')` (токены PHP `date()`),
  `clubDate(now, tz)` — «сегодня» клуба для `?date=`.

## Переводы (`src/i18n/`)

- `const { t } = useTranslation(); t('Русский текст')` — ключ = текст `__()` старого шаблона **дословно**.
- Подстановки: `t('Создан товар: {{product}}', { product })` (в старом `:product`). Множественное число:
  `t('{{count}} минута|{{count}} минуты|{{count}} минут', { count })` — ключ — строка `trans_choice` старого кода
  с `{{count}}`; формы язык берёт с сервера.
- Переводы грузятся с сервера (`GET /api/i18n/{locale}`), правятся в суперадминке; в коде их нет. Язык — язык
  клуба (`me.club.locale`), у гостя — язык браузера (как `LocaleResolver`).
- Данные из БД и машинные коды через `t()` не пропускать. Текст ошибки API — ключ: `t(e.message)`.

## Real-time (WebSocket, `src/realtime/`)

- Соединение одно на вкладку, держит `AdminShell` (билет `POST /api/ws-ticket` → `/ws?ticket=…`, переподключение
  с backoff). Типы событий и `data` — `docs/realtime.md`.
- Экран: `useRealtimeEvent('computer.state', (m) => queryClient.setQueryData(key, (old) => …))` — готовые
  изменения (Q-062); или `useInvalidateOn(['todo.*'], ['todos'])` — перечитать. Опрос (`setInterval` + fetch)
  **не использовать**. После переподключения активные запросы перечитываются сами.
- Бейджи меню «Чат»/«Заказы» — `realtime/badges.ts` (Q-063).

## Компоненты (`src/components/`)

`Pagination` (вид `$paginator->links()` Laravel: `page`, `perPage`, `total`, `count`, `hrefFor`), `ConfirmForm`
(`<form onsubmit="return confirm(…)">`), `FieldError` (ошибка под полем с классами шаблона), `SettingsCard`
(`<x-settings.card>`), `PhoneLocalInput` (маска телефона входа). Страницы ошибок 403/404/500 — `pages/ErrorPage.tsx`
(стандартные шаблоны Laravel). Модалки не обобщаем — каждая переносится своей разметкой. Новый общий компонент — только если разметка повторяется в
нескольких шаблонах дословно; классы — из Blade без изменений.

## Тесты

- Vitest рядом с кодом (`*.test.ts(x)`); моки API — `vi.spyOn(globalThis, 'fetch')` или `msw`-подобные функции
  в тесте (без хардкода хоста).
- Playwright: `e2e/<раздел>.spec.ts` — против `vite preview` с моком `page.route('**/api/**')` (так в CI);
  `e2e/<раздел>.server.spec.ts` — против настоящего сервера (`test.skip(!process.env.E2E_REAL_SERVER)`).
  Визуальное сравнение — `e2e/visual/` (инструмент F2).
- Стенд для `*.server.spec.ts`: отдельная база в Postgres из `docker-compose.dev.yml` (`CREATE DATABASE …`,
  `goose -env=none -dir db/migrations postgres <dsn владельца> up`, логины-члены `zafiro_app`/`zafiro_platform`),
  переводы — `go run ./cmd/import-translations -dir ../../Zafiro/database/translations -dsn <platform dsn>`,
  бинарник — `task build` (или `webui:copy` + `go build`), запуск с `ZAFIRO_HTTP_ADDR`, `ZAFIRO_DATABASE_URL`,
  `ZAFIRO_DATABASE_PLATFORM_URL`, `ZAFIRO_AGENT_GRPC_LISTEN` на свободных портах; затем
  `E2E_REAL_SERVER=1 E2E_BASE_URL=http://127.0.0.1:<порт> npx playwright test <файл>`.
