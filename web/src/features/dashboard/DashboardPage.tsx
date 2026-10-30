import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatDate } from '../../lib/datetime';
import { formatMoney } from '../../lib/money';
import { useInvalidateOn } from '../../realtime/hooks';
import { dashboardQuery, formatInt } from './api';
import { DashboardChart } from './DashboardChart';

/*
 * Админпанель — перенос resources/views/dashboard.blade.php (DashboardController). Разметка и классы — из шаблона.
 * ПК онлайн/забронировано и касса обновляются по WebSocket (computer.*, cash.*) — в старом только при загрузке.
 */
export function DashboardPage() {
  const { t } = useTranslation();
  const { me } = useSession();
  useAdminPageTitle(t('Админпанель'));
  useInvalidateOn(['computer.*', 'cash.*'], ['dashboard']);
  const { data } = useQuery(dashboardQuery());
  const tz = me?.club?.timezone ?? 'UTC';
  if (!data) return null;
  const currency = data.currency;
  const money = (tiyn: number) => formatMoney(tiyn, 0, ',', ' ');
  const link = 'flex items-center justify-between gap-3 text-sm text-gray-700 dark:text-gray-300';
  return (
    <div className="p-6 lg:p-8" data-testid="dashboard">
      {/* Welcome Section */}
      <div className="mb-8 animate-slide">
        <h3 className="text-3xl font-bold text-gray-900 dark:text-white mb-2">{t('Добро пожаловать, {{name}}!', { name: me?.user.name ?? '' })}</h3>
        <p className="text-gray-600 dark:text-gray-400">{t('Сводка по кассе, залу и складу за сегодня')}</p>
      </div>

      {/* Stats Cards */}
      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-6 mb-8">
        <div className="stat-card bg-white dark:bg-gray-900 rounded-xl p-6 border border-gray-200 dark:border-gray-800 animate-slide animation-delay-100">
          <div className="flex items-center justify-between mb-4">
            <div>
              <p className="text-sm font-medium text-gray-600 dark:text-gray-400">{t('Доход сегодня')}</p>
              <p className="text-2xl font-bold text-gray-900 dark:text-white mt-1">{money(data.today_income_tiyn)} {currency}</p>
            </div>
            <div className="w-12 h-12 bg-gradient-to-br from-purple-100 to-purple-200 dark:from-purple-900 dark:to-purple-800 rounded-lg flex items-center justify-center">
              <i className="fas fa-shopping-cart text-purple-600 dark:text-purple-400 text-xl"></i>
            </div>
          </div>
          <div className="flex items-center gap-2 text-green-700 text-sm">
            <i className="fas fa-arrow-up"></i>
            <span>{t('{{count}} операция сегодня|{{count}} операции сегодня|{{count}} операций сегодня', { count: data.today_transaction_count })}</span>
          </div>
        </div>

        <div className="stat-card bg-white dark:bg-gray-900 rounded-xl p-6 border border-gray-200 dark:border-gray-800 animate-slide animation-delay-200">
          <div className="flex items-center justify-between mb-4">
            <div>
              <p className="text-sm font-medium text-gray-600 dark:text-gray-400">{t('Расходы сегодня')}</p>
              <p className="text-2xl font-bold text-gray-900 dark:text-white mt-1">{money(data.today_expenses_tiyn)} {currency}</p>
            </div>
            <div className="w-12 h-12 bg-gradient-to-br from-blue-100 to-blue-200 dark:from-blue-900 dark:to-blue-800 rounded-lg flex items-center justify-center">
              <i className="fas fa-credit-card text-blue-600 dark:text-blue-400 text-xl"></i>
            </div>
          </div>
          <div className="flex items-center gap-2 text-red-600 text-sm">
            <i className="fas fa-arrow-down"></i>
            <span>{t('{{count}} расходная операция|{{count}} расходные операции|{{count}} расходных операций', { count: data.today_expense_count })}</span>
          </div>
        </div>

        <div className="stat-card bg-white dark:bg-gray-900 rounded-xl p-6 border border-gray-200 dark:border-gray-800 animate-slide animation-delay-300">
          <div className="flex items-center justify-between mb-4">
            <div>
              <p className="text-sm font-medium text-gray-600 dark:text-gray-400">{t('Чистый итог')}</p>
              <p className="text-2xl font-bold text-gray-900 dark:text-white mt-1">{money(data.today_income_tiyn - data.today_expenses_tiyn)} {currency}</p>
            </div>
            <div className="w-12 h-12 bg-gradient-to-br from-green-100 to-green-200 dark:from-green-900 dark:to-green-800 rounded-lg flex items-center justify-center">
              <i className="fas fa-boxes text-green-700 dark:text-green-400 text-xl"></i>
            </div>
          </div>
          <div className="flex items-center gap-2 text-green-700 text-sm">
            <i className="fas fa-arrow-up"></i>
            <span>{t('Доходы минус расходы за сегодня')}</span>
          </div>
        </div>

        <div className="stat-card bg-white dark:bg-gray-900 rounded-xl p-6 border border-gray-200 dark:border-gray-800 animate-slide animation-delay-400">
          <div className="flex items-center justify-between mb-4">
            <div>
              <p className="text-sm font-medium text-gray-600 dark:text-gray-400">{t('Пользователи')}</p>
              <p className="text-2xl font-bold text-gray-900 dark:text-white mt-1">{formatInt(data.user_count)}</p>
            </div>
            <div className="w-12 h-12 bg-gradient-to-br from-orange-100 to-orange-200 dark:from-orange-900 dark:to-orange-800 rounded-lg flex items-center justify-center">
              <i className="fas fa-chart-line text-orange-600 dark:text-orange-400 text-xl"></i>
            </div>
          </div>
          <div className="flex items-center gap-2 text-green-700 text-sm">
            <i className="fas fa-arrow-up"></i>
            <span>{t('Зарегистрировано в системе')}</span>
          </div>
        </div>
      </div>

      {/* Charts Row */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6 mb-8">
        <div className="lg:col-span-2 bg-white dark:bg-gray-900 rounded-xl p-6 border border-gray-200 dark:border-gray-800 animate-slide animation-delay-500">
          <h3 className="text-lg font-bold text-gray-900 dark:text-white mb-6">{t('Доходы и расходы за 7 дней')}</h3>
          <DashboardChart days={data.days} />
        </div>

        <div className="bg-white dark:bg-gray-900 rounded-xl p-6 border border-gray-200 dark:border-gray-800 animate-slide animation-delay-600">
          <h3 className="text-lg font-bold text-gray-900 dark:text-white mb-6">{t('Состояние клуба')}</h3>
          <div className="space-y-5">
            <a href="/club-map" className={link}><span><i className="fas fa-desktop mr-2 text-green-700"></i>{t('ПК онлайн')}</span><strong className="text-gray-900 dark:text-white">{data.online_computer_count} / {data.computer_count}</strong></a>
            <a href="/club-map" className={link}><span><i className="fas fa-bookmark mr-2 text-amber-500"></i>{t('ПК забронировано')}</span><strong className="text-gray-900 dark:text-white">{data.reserved_computer_count}</strong></a>
            <a href="/club-map" className={link}><span><i className="fas fa-map-location-dot mr-2 text-blue-600"></i>{t('Зоны')}</span><strong className="text-gray-900 dark:text-white">{data.zone_count}</strong></a>
            <a href="/warehouse" className={link}><span><i className="fas fa-boxes-stacked mr-2 text-rose-500"></i>{t('Товары с низким остатком')}</span><strong className="text-gray-900 dark:text-white">{data.low_stock_count}</strong></a>
            <a href="/warehouse" className={link}><span><i className="fas fa-box mr-2 text-gray-500"></i>{t('Всего товаров')}</span><strong className="text-gray-900 dark:text-white">{data.product_count}</strong></a>
          </div>
        </div>
      </div>

      {/* Recent Invoice */}
      <div className="bg-white dark:bg-gray-900 rounded-xl p-6 border border-gray-200 dark:border-gray-800 animate-slide animation-delay-700">
        <div className="flex justify-between items-center mb-6">
          <h3 className="text-lg font-bold text-gray-900 dark:text-white">{t('Последние операции')}</h3>
          <a href="/cash-register" className="brand-link text-sm font-medium">{t('Открыть кассу →')}</a>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b border-gray-200 dark:border-gray-800">
              <tr>
                <th className="text-left py-3 font-semibold text-gray-700 dark:text-gray-300">{t('Операция')}</th>
                <th className="text-left py-3 font-semibold text-gray-700 dark:text-gray-300">{t('Описание')}</th>
                <th className="text-left py-3 font-semibold text-gray-700 dark:text-gray-300">{t('Дата')}</th>
                <th className="text-right py-3 font-semibold text-gray-700 dark:text-gray-300">{t('Сумма')}</th>
              </tr>
            </thead>
            <tbody>
              {data.recent_transactions.length === 0 ? (
                <tr><td colSpan={4} className="py-8 text-center text-sm text-gray-500 dark:text-gray-400">{t('Кассовых операций пока нет.')}</td></tr>
              ) : (
                data.recent_transactions.map((tx) => (
                  <tr key={tx.id} className="border-b border-gray-100 dark:border-gray-800 hover:bg-gray-50 dark:hover:bg-gray-800/50 transition">
                    <td className="py-4"><span className={`rounded-full px-2 py-1 text-xs font-semibold ${tx.type === 'income' ? 'bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-400' : 'bg-rose-100 text-rose-700 dark:bg-rose-900/30 dark:text-rose-400'}`}>{tx.type === 'income' ? t('Доход') : t('Расход')}</span></td>
                    <td className="py-4 text-gray-700 dark:text-gray-300">{tx.description}</td>
                    <td className="py-4 text-gray-600 dark:text-gray-400">{tx.created_at ? formatDate(tx.created_at, tz, 'd.m.Y H:i') : ''}</td>
                    <td className={`py-4 text-right font-semibold ${tx.type === 'income' ? 'text-green-700 dark:text-green-400' : 'text-rose-700 dark:text-rose-400'}`}>{tx.type === 'expense' ? '−' : '+'}{money(tx.amount_tiyn)} {currency}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
