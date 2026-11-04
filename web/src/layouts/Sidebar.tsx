import type { SyntheticEvent, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useAdminSession } from './adminSession';
import { ROLE_LABELS, SIDEBAR_SECTIONS, isActive, resolveTarget } from './navigation';
import type { ClubRole, MembershipRole } from './types';

/*
 * Боковое меню панели клуба — перенос Zafiro resources/views/partials/sidebar.blade.php
 * (разметка и классы один-в-один). Свёртывание и мобильная шторка — в AdminLayout
 * (бывший partials/sidebar-script.blade.php).
 */

export interface SidebarProps {
  /** Мобильная шторка открыта (класс .active). */
  readonly open: boolean;
  /** Меню свёрнуто в узкую полосу (desktop): у ссылок появляется title с текстом. */
  readonly rail: boolean;
  /** Кнопка «Закрыть меню» в шторке. */
  readonly onClose: () => void;
}

function isClubRole(role: MembershipRole): role is ClubRole {
  return role !== 'client';
}

export function Sidebar({ open, rail, onClose }: SidebarProps) {
  const { t } = useTranslation();
  const session = useAdminSession();
  const { membership, user, badges } = session;
  const path = session.currentPath ?? window.location.pathname;

  const sections = SIDEBAR_SECTIONS.map((section) => ({
    section,
    links: section.items.flatMap((item) => {
      const to = resolveTarget(item, membership);
      return to === null ? [] : [{ item, to }];
    }),
  }));

  const roleLabel = isClubRole(membership.role) ? t(ROLE_LABELS[membership.role]) : membership.role;
  const initial = Array.from(user.name)[0] ?? '';

  const logout = (event: SyntheticEvent<HTMLFormElement>) => {
    if (session.onLogout === undefined) return;
    event.preventDefault();
    session.onLogout();
  };

  return (
    <aside id="adminSidebar" className={open ? 'sidebar active' : 'sidebar'}>
      <div className="sidebar-brand">
        <img className="sidebar-brand-mark" src="/images/logo.svg" alt="" />
        <div>
          <strong>Zafiro</strong>
          <small>{t('Управление клубом')}</small>
        </div>
        <button type="button" className="sidebar-close" data-sidebar-close aria-label={t('Закрыть меню')} onClick={onClose}>
          <i className="fas fa-xmark" aria-hidden="true"></i>
        </button>
      </div>
      {/* Кассир и оператор не видят Админпанель; остальные страницы — по правам сотрудника (Настройки → Права). */}
      <nav className="sidebar-nav">
        {sections.map(({ section, links }) => (
          <SidebarSectionBlock key={section.key} label={section.alwaysShowLabel || links.length > 0 ? t(section.label) : null}>
            {links.map(({ item, to }) => {
              const label = t(item.label);
              const count = item.badge === undefined ? 0 : badges[item.badge];
              return (
                <a key={item.key} href={to} className={isActive(item, path) ? 'sidebar-link active' : 'sidebar-link'} title={rail ? label : undefined}>
                  <i className={item.icon}></i>
                  <span>{label}</span>
                  {count > 0 && <span className="ml-auto rounded-full bg-red-600 px-2 text-xs font-bold text-white">{count}</span>}
                </a>
              );
            })}
          </SidebarSectionBlock>
        ))}
      </nav>
      <div className="sidebar-footer">
        <div className="sidebar-user-mini">
          <span>{initial}</span>
          <div>
            <strong>{user.name}</strong>
            <small>{roleLabel}</small>
          </div>
        </div>
        <form action="/logout" method="POST" onSubmit={logout}>
          {session.csrfToken !== undefined && <input type="hidden" name="_token" value={session.csrfToken} />}
          <button title={t('Выйти')}>
            <i className="fas fa-arrow-right-from-bracket"></i>
          </button>
        </form>
      </div>
    </aside>
  );
}

function SidebarSectionBlock({ label, children }: { readonly label: string | null; readonly children: ReactNode }) {
  return (
    <>
      {label !== null && <span className="sidebar-label">{label}</span>}
      {children}
    </>
  );
}
