# web/src/layouts — каркас панели клуба

Первый React-layout zafirogo: перенос админ-каркаса Zafiro (Laravel/Blade) один-в-один
по разметке и Tailwind-классам. Стили — `web/src/styles/app.css` (перенос `resources/css/app.css`).

## Что перенесено и откуда

| Файл | Откуда в Zafiro | Что делает |
|---|---|---|
| `AdminLayout.tsx` | `resources/views/layouts/admin.blade.php`, `partials/sidebar-script.blade.php`, `resources/js/app.js` | Оболочка `.admin-app`: оверлей, меню, верхняя панель (заголовок, кнопка меню, выбор клуба, поиск, часы, тема, уведомления), баннер «суперадмин смотрит клуб», PWA-баннер. Свёртывание меню (`zafiro.sidebarCollapsed`), мобильная шторка (≤768px), Escape, смена брейкпоинта — на React state. Классы `admin-body dark:bg-gray-950` ставит на `<body>`. Запоминает язык для offline-страницы (`zafiro.locale`). |
| `Sidebar.tsx` | `resources/views/partials/sidebar.blade.php` | Меню по правам, бейджи «Чат»/«Заказы», подвал с пользователем, ролью и выходом. |
| `navigation.ts` | `partials/sidebar.blade.php`, `routes/web.php` | Конфигурация пунктов: `permission` (строка права как в старом), `to` (URI как в старом), `fallback`, `exceptRoles`, `activePaths` (аналог `request()->routeIs()`), `badge`. |
| `permissions.ts` | `app/Support/ClubPermissions.php` | `allows()` и `SECTIONS` — только для показа меню; доступ проверяет сервер. |
| `ClubClock.tsx` | `<time data-clock>` в `layouts/admin.blade.php` + часы в `resources/js/app.js` | Время в зоне клуба с поправкой на серверные часы, обновление на границе секунды. |
| `ThemeToggle.tsx` | кнопка `[data-theme-toggle]` и её скрипт в `layouts/admin.blade.php` | Класс `.dark` на `<html>`, ключ `zafiro.theme`. |
| `NotificationsMenu.tsx` | `.admin-notifications` и inline-скрипт в `layouts/admin.blade.php` | Список, точка непрочитанного, «просмотрено», звук Web Audio, `zafiro.notificationsHeard`, `zafiro.notificationSound`. |
| `PwaBanner.tsx` | `.pwa-banner` в `layouts/admin.blade.php` + `resources/js/app.js` | Предложение установить приложение (beforeinstallprompt / подсказки iOS и Samsung), «Не сейчас» на 30 дней. |
| `adminSession.ts`, `types.ts` | `View::share` в `app/Http/Middleware/SetCurrentClub.php` | Контекст сессии (`AdminSessionContext`) и заголовок страницы (`useAdminPageTitle`). |
| `storage.ts` | try/catch вокруг `localStorage` в старых скриптах | Безопасный доступ к хранилищу, ключи прежние. |

## Как использовать

```tsx
import { AdminLayout } from './layouts/AdminLayout';
import { AdminSessionContext, useAdminPageTitle } from './layouts/adminSession';

<AdminSessionContext.Provider value={session}>
  <AdminLayout>
    <CashRegisterPage />   {/* внутри: useAdminPageTitle(t('Касса')) */}
  </AdminLayout>
</AdminSessionContext.Provider>
```

`AdminLayout` не зависит от роутера: ссылки меню — обычные `<a href>`, дочерняя страница — `children`.
Подсветка пункта меню — по `session.currentPath` (если не задан — `window.location.pathname`).

## Что должно быть в файлах каркаса (их ведёт другой агент)

1. **`web/index.html`** — как `<head>` старого `layouts/admin.blade.php`:
   ```html
   <link rel="preconnect" href="https://fonts.googleapis.com">
   <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
   <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
   <link href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.4.0/css/all.min.css" rel="stylesheet">
   <meta name="theme-color" content="#101713">
   <!-- до стилей, чтобы ночная тема не мигала: -->
   <script>try{if(localStorage.getItem('zafiro.theme')==='dark')document.documentElement.classList.add('dark')}catch(e){}</script>
   ```
   Шрифты в старом проекте **не локальные**: Inter с Google Fonts (`partials/fonts.blade.php`),
   иконки Font Awesome 6.4.0 с cdnjs. Поэтому `web/public/fonts/` не создавался. Самостоятельный хостинг
   (npm-пакеты с точной версией) — открытый вопрос, см. `docs/open-questions/screens.md`.
2. **`web/src/main.tsx`** — `import './styles/app.css'`; регистрация service worker `/sw.js`
   (если PWA сохраняем); инициализация i18next **с `keySeparator: false` и `nsSeparator: false`**:
   ключи — русские фразы с точками и двоеточиями (например «… Только просмотр: изменения недоступны.»).
3. **`web/public/`** — статика из `Zafiro/public`: `images/logo.svg` (логотип в меню),
   `icons/icon-192.png` (PWA-баннер), `icons/*` (манифест), `images/product-placeholder.svg`, `sw.js`.
   Пока не скопированы — пути в разметке указаны как в старом проекте (`/images/logo.svg`, `/icons/icon-192.png`).

## Плейсхолдеры i18next

Ключи — русский текст, как `__()` в Laravel. Подстановки Laravel `:club` записаны в формате i18next
`{{club}}` (например `t('Вы смотрите клуб «{{club}}» как суперадмин. …', { club })`). При выгрузке
переводов из старой БД плейсхолдеры нужно конвертировать — вопрос в `docs/open-questions/screens.md`.

## Что пока заглушка

- **Данные сессии**: `STUB_ADMIN_SESSION` в `adminSession.ts` (пустой пользователь, роль owner, без клубов).
  Настоящие данные — API сессии (Этап 1).
- **Уведомления**: источник `session.notifications` (`load`, `markSeen`, `subscribe`). Без `subscribe`
  работает запасной опрос раз в 60 с, как в старом layout; цель — WebSocket.
- **Бейджи меню** (`badges.chat`, `badges.orders`): в старом проекте считались при каждой отрисовке
  страницы (`ChatController::unreadTotal()`, `ShopOrder::newCount()`); в SPA их должен обновлять WebSocket.
- **Поиск в верхней панели** — в старом проекте без обработчика; перенесён как есть.
- **Выход / смена клуба / «Вернуться в кабинет»**: формы POST на старые URI (`/logout`, `/clubs/switch`,
  `/superadmin/leave-club`) с `_token`, либо колбэки `onLogout` / `onSwitchClub` / `onLeaveViewedClub`,
  если приложение их передало.
- **Кабинет суперадмина** (`layouts/superadmin.blade.php`, `partials/superadmin-sidebar.blade.php`) и
  **страницы входа** (`layouts/auth.blade.php`) — ещё не перенесены; оболочка та же (`.admin-app`, `.sidebar`
  / `.auth-shell`), меню суперадмина — 8 пунктов без прав.
- Пагинация Laravel (`vendor/laravel/.../Pagination/resources/views/tailwind.blade.php`) — нужен React-компонент
  с теми же классами (в старом CSS на неё указывал `@source`).
