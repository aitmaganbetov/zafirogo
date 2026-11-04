/** Подписи ролей ($roles в superadmin/users/index и clubs/show) — ключи перевода. */
export const ROLES: Record<string, string> = { owner: 'Владелец', admin: 'Администратор', manager: 'Менеджер', cashier: 'Кассир', operator: 'Оператор' };

/** $roles[$role] ?? $role. */
export function roleLabel(t: (key: string) => string, role: string): string {
  const label = ROLES[role];
  return label === undefined ? role : t(label);
}
