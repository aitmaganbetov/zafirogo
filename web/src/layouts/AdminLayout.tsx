import { useCallback, useEffect, useRef, useState } from 'react';
import type { ChangeEvent, SyntheticEvent, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { APP_NAME, PageTitleContext, useAdminSession } from './adminSession';
import { ClubClock } from './ClubClock';
import { NotificationsMenu } from './NotificationsMenu';
import { PwaBanner } from './PwaBanner';
import { Sidebar } from './Sidebar';
import { MOBILE_QUERY, STORAGE_KEYS, readStorage, writeStorage } from './storage';
import { ThemeToggle } from './ThemeToggle';

/*
 * Каркас панели клуба — перенос Zafiro resources/views/layouts/admin.blade.php
 * (разметка и классы один-в-один) и поведения из partials/sidebar-script.blade.php,
 * resources/js/app.js (часы, PWA, язык для offline-страницы).
 *
 * Данные (пользователь, клуб, права, бейджи, уведомления) приходят через
 * AdminSessionContext (см. adminSession.ts), заголовок страницы — через useAdminPageTitle().
 */

export function AdminLayout({ children }: { children: ReactNode }) {
  const { t, i18n } = useTranslation();
  const session = useAdminSession();
  const [mobile, setMobile] = useState(() => window.matchMedia(MOBILE_QUERY).matches);
  const toggleRef = useRef<HTMLButtonElement>(null);

  // Свёрнутое меню (desktop) запоминается в браузере; на мобильном меню — шторка.
  const [collapsed, setCollapsed] = useState(() => readStorage(STORAGE_KEYS.sidebarCollapsed) === '1');
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [pageTitle, setPageTitle] = useState<string | null>(null);

  const rail = !mobile && collapsed;
  const title = pageTitle ?? t('Панель управления');

  // Смена брейкпоинта: шторка закрывается, свёрнутость берётся из сохранённой настройки.
  useEffect(() => {
    const media = window.matchMedia(MOBILE_QUERY);
    const onChange = () => {
      setMobile(media.matches);
      setDrawerOpen(false);
      setCollapsed(readStorage(STORAGE_KEYS.sidebarCollapsed) === '1');
    };
    media.addEventListener('change', onChange);
    return () => { media.removeEventListener('change', onChange); };
  }, []);

  const closeDrawer = useCallback(() => { setDrawerOpen(false); }, []);
  const closeDrawerAndFocus = useCallback(() => {
    setDrawerOpen(false);
    toggleRef.current?.focus();
  }, []);

  useEffect(() => {
    if (!drawerOpen) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') closeDrawerAndFocus();
    };
    document.addEventListener('keydown', onKey);
    return () => { document.removeEventListener('keydown', onKey); };
  }, [drawerOpen, closeDrawerAndFocus]);

  const toggleMenu = () => {
    if (mobile) {
      setDrawerOpen((open) => !open);
      return;
    }
    setCollapsed((current) => {
      const next = !current;
      writeStorage(STORAGE_KEYS.sidebarCollapsed, next ? '1' : '0');
      return next;
    });
  };

  // <body class="admin-body dark:bg-gray-950"> — body вне React, классы ставятся на время жизни layout.
  useEffect(() => {
    const classes = ['admin-body', 'dark:bg-gray-950'];
    document.body.classList.add(...classes);
    return () => { document.body.classList.remove(...classes); };
  }, []);

  // Язык клуба запоминается для offline-страницы (resources/js/app.js).
  useEffect(() => {
    document.documentElement.lang = i18n.language;
    writeStorage(STORAGE_KEYS.locale, i18n.language);
  }, [i18n.language]);

  useEffect(() => {
    document.title = `${title} · ${APP_NAME}`;
  }, [title]);

  const menuOpen = mobile ? drawerOpen : !rail;
  const toggleLabel = mobile ? (drawerOpen ? t('Скрыть меню') : t('Показать меню')) : rail ? t('Развернуть меню') : t('Свернуть меню');

  const switchClub = (event: ChangeEvent<HTMLSelectElement>) => {
    if (session.onSwitchClub !== undefined) {
      session.onSwitchClub(Number(event.currentTarget.value));
      return;
    }
    event.currentTarget.form?.submit();
  };

  const leaveViewedClub = (event: SyntheticEvent<HTMLFormElement>) => {
    if (session.onLeaveViewedClub === undefined) return;
    event.preventDefault();
    session.onLeaveViewedClub();
  };

  const csrf = session.csrfToken !== undefined ? <input type="hidden" name="_token" value={session.csrfToken} /> : null;

  return (
    <PageTitleContext.Provider value={setPageTitle}>
      <div className={drawerOpen ? 'overlay active' : 'overlay'} id="overlay" onClick={closeDrawer}></div>
      <div className={rail ? 'admin-app sidebar-collapsed' : 'admin-app'}>
        <Sidebar open={drawerOpen} rail={rail} onClose={closeDrawerAndFocus} />
        <main className="admin-main">
          <header className="admin-topbar">
            <div className="admin-topbar-inner">
              <div className="admin-title-wrap">
                <button
                  type="button"
                  id="sidebarToggle"
                  ref={toggleRef}
                  className="admin-mobile-toggle"
                  aria-label={toggleLabel}
                  aria-controls="adminSidebar"
                  aria-expanded={menuOpen}
                  onClick={toggleMenu}
                >
                  <i className="fas fa-bars"></i>
                </button>
                <h2 className="admin-title">{title}</h2>
              </div>
              <div className="admin-topbar-actions">
                {session.availableClubs.length > 1 && (
                  <form method="POST" action="/clubs/switch">
                    {csrf}
                    <select
                      name="club_id"
                      aria-label={t('Активный клуб')}
                      value={session.membership.clubId}
                      onChange={switchClub}
                      className="rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-700 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-200"
                    >
                      {session.availableClubs.map((club) => (
                        <option key={club.clubId} value={club.clubId}>
                          {club.clubName}
                        </option>
                      ))}
                    </select>
                  </form>
                )}
                {/* В старом layout поле поиска без обработчика (заглушка) — см. docs/open-questions/screens.md. */}
                <label className="admin-search">
                  <i className="fas fa-search"></i>
                  <input type="search" placeholder={t('Поиск...')} />
                </label>
                <ClubClock clubTime={session.clubTime} />
                <ThemeToggle />
                <NotificationsMenu source={session.notifications} />
              </div>
            </div>
          </header>
          <div className="admin-content">
            {session.superadminViewing !== null && (
              // Суперадмин смотрит чужой клуб: изменения сервер отклоняет.
              <div
                className="flex flex-wrap items-center justify-between gap-3 border-b border-violet-200 bg-violet-50 px-6 py-3 text-sm text-violet-900 dark:border-violet-800 dark:bg-violet-950 dark:text-violet-100"
                role="status"
              >
                <span>
                  <i className="fas fa-eye mr-2" aria-hidden="true"></i>
                  {t('Вы смотрите клуб «{{club}}» как суперадмин. Только просмотр: изменения недоступны.', { club: session.superadminViewing.name })}
                </span>
                <form method="POST" action="/superadmin/leave-club" onSubmit={leaveViewedClub}>
                  {csrf}
                  <button type="submit" className="rounded-lg bg-violet-700 px-3 py-1.5 font-medium text-white hover:bg-violet-800">
                    {t('Вернуться в кабинет')}
                  </button>
                </form>
              </div>
            )}
            {children}
          </div>
        </main>
      </div>
      <PwaBanner />
    </PageTitleContext.Provider>
  );
}
