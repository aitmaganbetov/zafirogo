import { allows } from './permissions';
import type { ClubRole, Membership, SidebarBadges } from './types';

/*
 * Меню панели клуба — перенос Zafiro resources/views/partials/sidebar.blade.php.
 * Порядок, иконки (Font Awesome 6.4.0), тексты (ключи i18next = русский текст, как __() в Laravel),
 * права и пути — как в старом шаблоне. Пути — URI старых маршрутов из routes/web.php.
 */

/** Запасная цель ссылки, если основного права нет. */
export interface SidebarFallback {
  readonly permission?: string;
  readonly roles?: readonly ClubRole[];
  readonly to: string;
}

export interface SidebarItem {
  readonly key: string;
  /** Ключ перевода (русский текст). */
  readonly label: string;
  /** Классы иконки Font Awesome, как в старом <i class="...">. */
  readonly icon: string;
  readonly to: string;
  /** Право «раздел.действие» (ClubPermissions); null — пункт не зависит от права. */
  readonly permission: string | null;
  /** Скрыть для ролей (Админпанель не видят кассир и оператор: @unless($frontDesk)). */
  readonly exceptRoles?: readonly ClubRole[];
  /** Если основного права нет — ссылка по запасному праву/роли (Склад → Остатки, Настройки → Права). */
  readonly fallback?: SidebarFallback;
  /**
   * Пути, на которых пункт подсвечен (аналог request()->routeIs()).
   * 'exact:/path' — точное совпадение; иначе — путь и всё, что под ним.
   */
  readonly activePaths: readonly string[];
  readonly badge?: keyof SidebarBadges;
}

export interface SidebarSection {
  readonly key: string;
  readonly label: string;
  /** false — заголовок раздела показывается, только если виден хотя бы один пункт («Система»). */
  readonly alwaysShowLabel: boolean;
  readonly items: readonly SidebarItem[];
}

const FRONT_DESK: readonly ClubRole[] = ['cashier', 'operator'];

export const SIDEBAR_SECTIONS: readonly SidebarSection[] = [
  {
    key: 'workspace',
    label: 'Рабочее пространство',
    alwaysShowLabel: true,
    items: [
      { key: 'dashboard', label: 'Админпанель', icon: 'fas fa-chart-pie', to: '/dashboard', permission: null, exceptRoles: FRONT_DESK, activePaths: ['exact:/dashboard'] },
      { key: 'cash-register', label: 'Касса', icon: 'fas fa-cash-register', to: '/cash-register', permission: 'cash.view', activePaths: ['/cash-register'] },
      { key: 'club-map', label: 'Зоны', icon: 'fas fa-map-location-dot', to: '/club-map', permission: 'club_map.view', activePaths: ['/club-map'] },
      { key: 'reservations', label: 'Брони', icon: 'fas fa-calendar-check', to: '/reservations', permission: 'reservations.view', activePaths: ['/reservations'] },
      { key: 'todos', label: 'To Do List', icon: 'fas fa-list-check', to: '/todos', permission: 'todos.view', activePaths: ['/todos'] },
      { key: 'chat', label: 'Чат', icon: 'fas fa-comments', to: '/chat', permission: 'chat.view', activePaths: ['/chat'], badge: 'chat' },
      { key: 'shop-orders', label: 'Заказы', icon: 'fas fa-bag-shopping', to: '/shop-orders', permission: 'orders.view', activePaths: ['/shop-orders'], badge: 'orders' },
      { key: 'users', label: 'Пользователи', icon: 'fas fa-users', to: '/users', permission: 'customers.view', activePaths: ['/users'] },
      { key: 'workers', label: 'Работники', icon: 'fas fa-user-tie', to: '/workers', permission: 'workers.view', activePaths: ['/workers'] },
    ],
  },
  {
    key: 'accounting',
    label: 'Учёт',
    alwaysShowLabel: true,
    items: [
      { key: 'warehouse', label: 'Склад', icon: 'fas fa-boxes-stacked', to: '/warehouse', permission: 'warehouse.view', fallback: { permission: 'stock.view', to: '/stock' }, activePaths: ['/warehouse', '/stock'] },
      { key: 'services', label: 'Услуги', icon: 'fas fa-bell-concierge', to: '/services', permission: 'services.view', activePaths: ['/services'] },
      // routeIs('club-computers.index'): только сама страница списка, не POST-маршруты /club-computers/*.
      { key: 'club-computers', label: 'Список ПК', icon: 'fas fa-computer', to: '/club-computers', permission: 'club_map.view', activePaths: ['exact:/club-computers'] },
      { key: 'activity-logs', label: 'Логи', icon: 'fas fa-clock-rotate-left', to: '/activity-logs', permission: 'activity_logs.view', activePaths: ['/activity-logs'] },
    ],
  },
  {
    key: 'system',
    label: 'Система',
    alwaysShowLabel: false,
    items: [
      // /settings в старом проекте — редирект на /settings/club. Без права settings.view владелец/админ
      // попадает в «Настройки → Права».
      { key: 'settings', label: 'Настройки', icon: 'fas fa-sliders', to: '/settings', permission: 'settings.view', fallback: { roles: ['owner', 'admin'], to: '/settings/permissions' }, activePaths: ['/settings'] },
      { key: 'news', label: 'Новости', icon: 'fas fa-newspaper', to: '/news', permission: 'news.view', activePaths: ['/news'] },
      { key: 'billing', label: 'Подписка', icon: 'fas fa-credit-card', to: '/billing', permission: 'billing.view', activePaths: ['/billing'] },
      { key: 'tariffs', label: 'Тарифы', icon: 'fas fa-tags', to: '/tariffs', permission: 'tariffs.view', activePaths: ['/tariffs'] },
    ],
  },
];

/** Подписи ролей в подвале меню (sidebar.blade.php, sidebar-footer). */
export const ROLE_LABELS: Readonly<Record<ClubRole, string>> = {
  owner: 'Владелец',
  admin: 'Администратор',
  manager: 'Менеджер',
  cashier: 'Кассир',
  operator: 'Оператор',
};

function hasRole(membership: Membership, roles: readonly ClubRole[]): boolean {
  return roles.some((role) => role === membership.role);
}

/** Куда ведёт пункт для этого сотрудника; null — пункт скрыт. */
export function resolveTarget(item: SidebarItem, membership: Membership): string | null {
  if (item.exceptRoles !== undefined && hasRole(membership, item.exceptRoles)) return null;
  if (item.permission === null || allows(membership, item.permission)) return item.to;
  const fallback = item.fallback;
  if (fallback === undefined) return null;
  const byPermission = fallback.permission !== undefined && allows(membership, fallback.permission);
  const byRole = fallback.roles !== undefined && hasRole(membership, fallback.roles);
  return byPermission || byRole ? fallback.to : null;
}

export function isActive(item: SidebarItem, path: string): boolean {
  return item.activePaths.some((pattern) => {
    if (pattern.startsWith('exact:')) return path === pattern.slice('exact:'.length);
    return path === pattern || path.startsWith(`${pattern}/`);
  });
}
