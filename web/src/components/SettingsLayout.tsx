import { Fragment, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useLocation } from '@tanstack/react-router';
import { can, roleOf, useSession } from '../auth/session';
import { useAdminPageTitle } from '../layouts/adminSession';

/*
 * Каркас страниц настроек — resources/views/settings/layout.blade.php (разметка и классы один-в-один): заголовок,
 * левое меню разделов (по праву settings.view, «Права» — owner/admin, «API и интеграции» — @can('manage-integrations')),
 * flash `status` и содержимое (`@yield('settings-after')` — карточки страницы). Форму `settings-content` и якоря
 * (`settings.anchors`) страница настроек передаёт сама: `anchors` — под пунктом текущей страницы.
 */

interface MenuItem {
  readonly to: string;
  readonly icon: string;
  readonly label: string;
  /** request()->routeIs(…): точный путь или префикс («settings.permissions*»). */
  readonly prefix?: boolean;
  readonly anchors?: boolean;
}

const SETTINGS_ITEMS: readonly MenuItem[] = [
  { to: '/settings/club', icon: 'fas fa-building w-4', label: 'Настройка клуба', anchors: true },
  { to: '/settings/languages', icon: 'fas fa-language w-4', label: 'Языки', anchors: true },
  { to: '/settings/finances', icon: 'fas fa-coins w-4', label: 'Финансы', anchors: true },
  { to: '/settings/guests', icon: 'fas fa-users w-4', label: 'Гости' },
];

const ACTIVE = 'brand-tab';
const IDLE = 'text-gray-700 dark:text-gray-300 hover:bg-gray-100 dark:hover:bg-gray-800';

function itemClass(active: boolean): string {
  return `flex items-center gap-3 w-full text-left px-4 py-3 rounded-xl ${active ? ACTIVE : IDLE} font-medium transition`;
}

export interface SettingsLayoutProps {
  /** Сообщение session('status'). */
  readonly status?: string | null;
  /** settings.anchors открытой страницы (только club/languages/finances). */
  readonly anchors?: ReactNode;
  readonly children: ReactNode;
}

export function SettingsLayout({ status, anchors, children }: SettingsLayoutProps) {
  const { t } = useTranslation();
  const { me } = useSession();
  const { pathname } = useLocation();
  useAdminPageTitle(t('Настройки'));
  const role = roleOf(me);
  const is = (to: string, prefix = false) => pathname === to || (prefix && pathname.startsWith(`${to}/`));

  return (
    <div className="p-6 lg:p-8">
      <div className="mb-6 animate-slide">
        <h3 className="text-3xl font-bold text-gray-900 dark:text-white mb-2">{t('Настройки клуба')}</h3>
        <p className="text-gray-600 dark:text-gray-400">{t('Управляйте настройками клуба в одном месте.')}</p>
      </div>

      <div className="grid grid-cols-1 xl:grid-cols-[240px_minmax(0,1fr)] gap-6">
        <div className="bg-white dark:bg-gray-900 rounded-2xl border border-gray-200 dark:border-gray-800 p-3 h-fit animate-slide shadow-sm xl:sticky xl:top-6 xl:max-h-[calc(100vh-3rem)] xl:overflow-y-auto">
          <div className="space-y-2">
            {can(me, 'settings.view') && (
              <>
                {SETTINGS_ITEMS.map((item) => (
                  <Fragment key={item.to}>
                    <a href={item.to} className={itemClass(is(item.to))}>
                      <i className={item.icon}></i>
                      {t(item.label)}
                    </a>
                    {item.anchors && is(item.to) ? anchors : null}
                  </Fragment>
                ))}
                <a href="/settings/api" className={itemClass(is('/settings/api'))}>
                  <i className="fas fa-code w-4"></i>API
                </a>
              </>
            )}
            {(role === 'owner' || role === 'admin') && (
              <a href="/settings/permissions" className={itemClass(is('/settings/permissions', true))}>
                <i className="fas fa-user-shield w-4"></i>
                {t('Права')}
              </a>
            )}
            {/* @can('manage-integrations'): владелец или can_manage_integrations (me.club.can_manage_integrations). */}
            {(role === 'owner' || me?.club?.can_manage_integrations === true) && (
              <a href="/settings/integrations" className={itemClass(is('/settings/integrations', true))}>
                <i className="fas fa-plug w-4"></i>
                {t('API и интеграции')}
              </a>
            )}
          </div>
        </div>

        {/* Each section of a page is its own card (<x-settings.card>). */}
        <div className="min-w-0 space-y-6">
          {status && (
            <div className="rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700 dark:border-green-800 dark:bg-green-900/20 dark:text-green-300">
              {status}
            </div>
          )}
          {children}
        </div>
      </div>
    </div>
  );
}
