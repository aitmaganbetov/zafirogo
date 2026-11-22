import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode, type SyntheticEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { APP_NAME, PageTitleContext } from '../../layouts/adminSession';
import { MOBILE_QUERY, STORAGE_KEYS, readStorage, writeStorage } from '../../layouts/storage';
import { FlashContext, type SuperadminFlash } from './flash';

/*
 * Общий каркас кабинетов вне клуба — суперадмина (layouts/superadmin.blade.php) и реселлера
 * (layouts/reseller.blade.php): в Blade это два почти одинаковых файла (разметка и классы один-в-один), отличаются
 * подписью под логотипом, меню, ролью и значком в шапке. Поведение меню — partials/sidebar-script (как AdminLayout).
 * Flash «status» и первая ошибка ($errors->first()) — в layout, как в Blade: страницы сообщают их через
 * useSuperadminFlash().
 */

export interface CabinetLayoutProps {
  readonly userName: string;
  readonly path: string;
  readonly onLogout: () => void;
  /** Подпись под логотипом и заголовок страницы по умолчанию («Кабинет суперадмина»). */
  readonly subtitle: string;
  /** Роль под именем и в значке шапки («Суперадмин»). */
  readonly role: string;
  /** Класс иконки значка шапки («fas fa-shield-halved»). */
  readonly roleIcon: string;
  /** Пункты меню; rail — меню свёрнуто в полоску (подписи — в title). */
  readonly nav: (rail: boolean) => ReactNode;
  readonly children: ReactNode;
}

export function CabinetLayout({ userName, path, onLogout, subtitle, role, roleIcon, nav, children }: CabinetLayoutProps) {
  const { t } = useTranslation();
  const [mobile, setMobile] = useState(() => window.matchMedia(MOBILE_QUERY).matches);
  const [collapsed, setCollapsed] = useState(() => readStorage(STORAGE_KEYS.sidebarCollapsed) === '1');
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [pageTitle, setPageTitle] = useState<string | null>(null);
  // Flash живёт до следующего перехода (как session('status') — одна страница): помнит путь, где показан.
  const [message, setMessage] = useState<{ path: string; status: string | null; error: string | null } | null>(null);
  const status = message?.path === path ? message.status : null;
  const error = message?.path === path ? message.error : null;
  const toggleRef = useRef<HTMLButtonElement>(null);
  const rail = !mobile && collapsed;
  const title = pageTitle ?? subtitle;

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

  // layouts/superadmin.blade.php не подключает скрипт темы (zafiro.theme) и переключателя нет: кабинет всегда в
  // светлой теме. Класс .dark снимается на время кабинета (docs/open-questions/stage3-superadmin.md SA-7).
  useEffect(() => {
    const root = document.documentElement;
    const dark = root.classList.contains('dark');
    root.classList.remove('dark');
    return () => {
      if (dark) root.classList.add('dark');
    };
  }, []);

  useEffect(() => {
    const classes = ['admin-body', 'dark:bg-gray-950'];
    document.body.classList.add(...classes);
    return () => { document.body.classList.remove(...classes); };
  }, []);

  useEffect(() => {
    document.title = `${title} · ${APP_NAME}`;
  }, [title]);

  const flash = useMemo<SuperadminFlash>(
    () => ({
      status: (m) => setMessage({ path, status: m, error: null }),
      error: (m) => setMessage({ path, status: null, error: m }),
    }),
    [path],
  );

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

  const logout = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    onLogout();
  };

  const menuOpen = mobile ? drawerOpen : !rail;
  const toggleLabel = mobile ? (drawerOpen ? t('Скрыть меню') : t('Показать меню')) : rail ? t('Развернуть меню') : t('Свернуть меню');

  return (
    <PageTitleContext.Provider value={setPageTitle}>
      <FlashContext.Provider value={flash}>
        <div className={drawerOpen ? 'overlay active' : 'overlay'} id="overlay" onClick={() => setDrawerOpen(false)}></div>
        <div className={rail ? 'admin-app sidebar-collapsed' : 'admin-app'}>
          <aside id="adminSidebar" className={drawerOpen ? 'sidebar active' : 'sidebar'} inert={mobile && !drawerOpen}>
            <div className="sidebar-brand">
              <img className="sidebar-brand-mark w-auto" src="/images/logo.svg" alt="" width={179} height={135} />
              <div>
                <strong>Zafiro</strong>
                <small>{subtitle}</small>
              </div>
              <button type="button" className="sidebar-close" data-sidebar-close aria-label={t('Закрыть меню')} onClick={closeDrawerAndFocus}>
                <i className="fas fa-xmark" aria-hidden="true"></i>
              </button>
            </div>
            <nav className="sidebar-nav">{nav(rail)}</nav>
            <div className="sidebar-footer">
              <div className="sidebar-user-mini">
                <span>{Array.from(userName)[0] ?? ''}</span>
                <div>
                  <strong>{userName}</strong>
                  <small>{role}</small>
                </div>
              </div>
              <form action="/logout" method="POST" onSubmit={logout}>
                <button title={t('Выйти')}>
                  <i className="fas fa-arrow-right-from-bracket"></i>
                </button>
              </form>
            </div>
          </aside>
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
                  <h2 className="admin-title" title={title}>{title}</h2>
                </div>
                <div className="admin-topbar-actions">
                  {/* shrink-0 whitespace-nowrap (аудит 22.11.2026, UI-9): длинный kk-заголовок не ломает плашку в две строки. */}
                  <span className="shrink-0 whitespace-nowrap rounded-full bg-violet-100 px-3 py-1 text-xs font-semibold text-violet-800 dark:bg-violet-900/40 dark:text-violet-200">
                    <i className={`${roleIcon} mr-1`} aria-hidden="true"></i>
                    {role}
                  </span>
                </div>
              </div>
            </header>
            <div className="admin-content">
              <div className="p-6 lg:p-8">
                {status && (
                  <div className="mb-4 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700" role="status">
                    {status}
                  </div>
                )}
                {error && (
                  <div className="mb-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700" role="alert">
                    {error}
                  </div>
                )}
                {children}
              </div>
            </div>
          </main>
        </div>
      </FlashContext.Provider>
    </PageTitleContext.Provider>
  );
}
