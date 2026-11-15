import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { CabinetLayout } from './CabinetLayout';
import { saHref } from './host';

/*
 * Каркас кабинета суперадмина — перенос resources/views/layouts/superadmin.blade.php и
 * partials/superadmin-sidebar.blade.php (разметка и классы один-в-один). Общая с кабинетом реселлера часть —
 * CabinetLayout.
 */

export interface SuperadminLayoutProps {
  readonly userName: string;
  /** Есть членство в клубе — пункт «Панель своего клуба». */
  readonly hasClub: boolean;
  readonly path: string;
  readonly onLogout: () => void;
  readonly children: ReactNode;
}

interface Link {
  readonly key: string;
  readonly href: string;
  readonly icon: string;
  readonly label: string;
  readonly active: (p: string) => boolean;
}

export function SuperadminLayout({ userName, hasClub, path, onLogout, children }: SuperadminLayoutProps) {
  const { t } = useTranslation();
  // request()->routeIs(...) по внутреннему пути /superadmin/….
  const under = (p: string) => (x: string) => x === '/superadmin' + p || x.startsWith('/superadmin' + p + '/');
  const platform: Link[] = [
    { key: 'dashboard', href: saHref('/'), icon: 'fas fa-server', label: t('Сервер'), active: (x) => x === '/superadmin' || x === '/superadmin/' },
    { key: 'clubs', href: saHref('/clubs'), icon: 'fas fa-building', label: t('Клубы'), active: under('/clubs') },
    { key: 'users', href: saHref('/accounts'), icon: 'fas fa-users', label: t('Пользователи'), active: under('/accounts') },
    { key: 'plans', href: saHref('/plans'), icon: 'fas fa-tags', label: t('Тарифы платформы'), active: under('/plans') },
    { key: 'resellers', href: saHref('/resellers'), icon: 'fas fa-handshake', label: t('Реселлеры'), active: under('/resellers') },
    { key: 'invoices', href: saHref('/invoices'), icon: 'fas fa-receipt', label: t('Оплаты'), active: under('/invoices') },
    { key: 'news', href: saHref('/platform-news'), icon: 'fas fa-newspaper', label: t('Новости'), active: under('/platform-news') },
  ];
  const system: Link[] = [
    { key: 'logs', href: saHref('/logs'), icon: 'fas fa-clock-rotate-left', label: t('Журнал'), active: under('/logs') },
    { key: 'languages', href: saHref('/languages'), icon: 'fas fa-language', label: t('Языки и переводы'), active: under('/languages') },
    { key: 'telegram', href: saHref('/telegram'), icon: 'fab fa-telegram', label: t('Telegram'), active: under('/telegram') },
  ];
  const linkEl = (rail: boolean) => (l: Link) => (
    <a key={l.key} href={l.href} className={l.active(path) ? 'sidebar-link active' : 'sidebar-link'} title={rail ? l.label : undefined}>
      <i className={l.icon}></i>
      <span>{l.label}</span>
    </a>
  );
  const nav = (rail: boolean) => (
    <>
      <span className="sidebar-label">{t('Платформа')}</span>
      {platform.map(linkEl(rail))}
      <span className="sidebar-label">{t('Система')}</span>
      {system.map(linkEl(rail))}
      {hasClub && (
        <a href="/dashboard" className="sidebar-link" title={rail ? t('Панель своего клуба') : undefined}>
          <i className="fas fa-arrow-left"></i>
          <span>{t('Панель своего клуба')}</span>
        </a>
      )}
    </>
  );

  return (
    <CabinetLayout userName={userName} path={path} onLogout={onLogout} subtitle={t('Кабинет суперадмина')} role={t('Суперадмин')} roleIcon="fas fa-shield-halved" nav={nav}>
      {children}
    </CabinetLayout>
  );
}
