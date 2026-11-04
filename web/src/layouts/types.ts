/**
 * Типы данных, которые админ-layout получает от приложения (раньше — переменные Blade,
 * которые раздаёт middleware SetCurrentClub через View::share: currentClub, currentMembership,
 * availableClubs, superadminViewing; см. Zafiro app/Http/Middleware/SetCurrentClub.php ~стр. 96–132).
 */

/** Роли сотрудника клуба: Zafiro app/Support/ClubRoles.php. */
export type ClubRole = 'owner' | 'admin' | 'manager' | 'cashier' | 'operator';

/** Роль клиента (не сотрудник) — у клиентов нет панели клуба. */
export type MembershipRole = ClubRole | 'client';

/** Права, выданные/снятые владельцем конкретному сотруднику: club_memberships.permissions {"section.action": bool}. */
export type PermissionOverrides = Readonly<Record<string, boolean>>;

export interface Membership {
  readonly clubId: number;
  readonly clubName: string;
  readonly role: MembershipRole;
  readonly permissions: PermissionOverrides;
}

export interface CurrentUser {
  readonly name: string;
}

/** Время клуба для часов в верхней панели (layouts/admin.blade.php, <time data-clock>). */
export interface ClubTime {
  /** IANA-зона клуба, например Asia/Almaty. */
  readonly timezone: string;
  /** Серверное время в мс на момент получения данных — поправка на неверные часы устройства. */
  readonly nowMs: number;
}

/** Уведомление верхней панели: ответ GET /notifications (NotificationController@index). */
export interface NotificationItem {
  readonly id: string;
  readonly url: string;
  readonly icon: string;
  readonly title: string;
  readonly body: string;
  readonly unread: boolean;
  readonly at: string;
  readonly at_human: string;
}

export interface NotificationsPayload {
  readonly unread_count: number;
  readonly items: readonly NotificationItem[];
}

/**
 * Источник уведомлений. В старом проекте — опрос GET /notifications раз в 60 с
 * (layouts/admin.blade.php, inline-скрипт). В новом — подписка по WebSocket (`subscribe`);
 * `load` — первичная загрузка и временный запасной вариант.
 */
export interface NotificationsSource {
  readonly load: () => Promise<NotificationsPayload>;
  readonly markSeen: () => Promise<void>;
  readonly subscribe?: (onChange: (payload: NotificationsPayload) => void) => () => void;
}

/** Счётчики-бейджи в меню: ChatController::unreadTotal() и ShopOrder::newCount(). */
export interface SidebarBadges {
  readonly chat: number;
  readonly orders: number;
}

export interface AdminSession {
  readonly user: CurrentUser;
  readonly membership: Membership;
  /** Все клубы пользователя (селектор «Активный клуб» показывается, если их больше одного). */
  readonly availableClubs: readonly Membership[];
  /** Суперадмин смотрит чужой клуб (только просмотр) — баннер над контентом. */
  readonly superadminViewing: { readonly name: string } | null;
  readonly clubTime: ClubTime;
  readonly badges: SidebarBadges;
  readonly notifications: NotificationsSource | null;
  /** Текущий путь для подсветки пункта меню; по умолчанию window.location.pathname. */
  readonly currentPath?: string;
  /** CSRF-токен для POST-форм (выход, смена клуба), если бэкенд его требует. */
  readonly csrfToken?: string;
  readonly onSwitchClub?: (clubId: number) => void;
  readonly onLogout?: () => void;
  readonly onLeaveViewedClub?: () => void;
}
