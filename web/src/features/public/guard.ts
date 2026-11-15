import { redirect } from '@tanstack/react-router';
import { isCabinetHost } from '../superadmin/host';

/**
 * Страницы сайта (/pricing, /product/*) на хосте кабинета суперадмина не показываются: RestrictSuperadminHost
 * (bootstrap/app.php, группа web) отправляет всё, кроме кабинета и входа, в кабинет. Главная «/» на этом хосте —
 * сам кабинет (features/superadmin/host.ts).
 */
export function publicSiteGuard(): void {
  if (isCabinetHost()) throw redirect({ href: '/superadmin' });
}
