import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { CabinetLayout } from '../superadmin/CabinetLayout';
import { resellerHref } from './host';

/*
 * Каркас кабинета реселлера — перенос resources/views/layouts/reseller.blade.php и
 * partials/reseller-sidebar.blade.php (разметка и классы один-в-один; общая с кабинетом суперадмина часть —
 * CabinetLayout). В меню один пункт — сам кабинет.
 */
export function ResellerLayout({ userName, path, onLogout, children }: { readonly userName: string; readonly path: string; readonly onLogout: () => void; readonly children: ReactNode }) {
  const { t } = useTranslation();
  const nav = (rail: boolean) => (
    <a href={resellerHref()} className="sidebar-link active" title={rail ? t('Мои клубы и доход') : undefined}>
      <i className="fas fa-chart-line"></i>
      <span>{t('Мои клубы и доход')}</span>
    </a>
  );
  return (
    <CabinetLayout userName={userName} path={path} onLogout={onLogout} subtitle={t('Кабинет реселлера')} role={t('Реселлер')} roleIcon="fas fa-handshake" nav={nav}>
      {children}
    </CabinetLayout>
  );
}
