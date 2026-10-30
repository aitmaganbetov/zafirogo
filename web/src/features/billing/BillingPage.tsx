import { useSuspenseQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatDate } from '../../lib/datetime';
import { billingQuery, type BillingSubscription } from './api';

/*
 * Подписка — перенос resources/views/billing/index.blade.php (BillingController::show). Даты — как ->format() в старом:
 * в часовом поясе приложения (config app.timezone = UTC), не клуба.
 */

const STATE_LABEL: Record<BillingSubscription['state'], string> = {
  expired: 'Истекла',
  inactive: 'Не активна',
  trialing: 'Пробный период',
  active: 'Активна',
};

export function BillingPage() {
  const { t } = useTranslation();
  const { me } = useSession();
  const { data } = useSuspenseQuery(billingQuery());
  useAdminPageTitle(t('Подписка'));
  const subscription = data.subscription;

  return (
    <div className="max-w-3xl p-6 lg:p-8">
      <h3 className="mb-6 text-xl font-semibold text-gray-900 dark:text-white">{t('Подписка клуба «{{club}}»', { club: me?.club?.name ?? '' })}</h3>
      {subscription ? (
        <dl className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          {/* __($subscription->plan->name): названия планов платформы переводятся в старом шаблоне. */}
          <div><dt className="text-sm text-gray-500">{t('План')}</dt><dd className="mt-1 font-medium text-gray-900 dark:text-white">{t(subscription.plan_name)}</dd></div>
          <div><dt className="text-sm text-gray-500">{t('Статус')}</dt><dd className="mt-1 font-medium text-gray-900 dark:text-white">{t(STATE_LABEL[subscription.state])}</dd></div>
          {subscription.status === 'trialing' && subscription.trial_ends_at && (
            <div><dt className="text-sm text-gray-500">{t('Пробный период до')}</dt><dd className="mt-1 font-medium text-gray-900 dark:text-white">{formatDate(subscription.trial_ends_at, 'UTC', 'd.m.Y H:i')}</dd></div>
          )}
          {subscription.current_period_ends_at && (
            <div><dt className="text-sm text-gray-500">{t('Оплачено до')}</dt><dd className="mt-1 font-medium text-gray-900 dark:text-white">{formatDate(subscription.current_period_ends_at, 'UTC', 'd.m.Y')}</dd></div>
          )}
        </dl>
      ) : (
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('Для клуба ещё не оформлена подписка.')}</p>
      )}
      <p className="mt-8 text-sm text-gray-500">{t('Онлайн-оплата появится после настройки платёжного провайдера.')}</p>
    </div>
  );
}
