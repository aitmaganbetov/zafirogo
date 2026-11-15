import { useTranslation } from 'react-i18next';
import { SettingsCard } from '../../components/SettingsCard';

/*
 * Карточка «Бронь из приложения» страницы «Настройка клуба» — перенос блока #app-booking из
 * resources/views/settings/club.blade.php (тема Г, docs/upstream-2026-11.md). Поля — часть общей формы секции club
 * (SettingsController::save()): app_booking_enabled (по умолчанию выключено) и app_booking_cancel (по умолчанию да).
 */
export function AppBookingCard({ enabled, cancel }: { enabled: boolean; cancel: boolean }) {
  const { t } = useTranslation();
  return (
    <SettingsCard id="app-booking" aria-labelledby="app-booking-title">
      <h5 id="app-booking-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Бронь из приложения')}</h5>
      <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">
        {t('Гость выбирает в мобильном приложении зону, ПК и время и оплачивает бронь с баланса по цене зоны. Оплаченное время начнётся, когда гость войдёт на этом ПК (или вы отметите «Клиент пришёл»). Не пришёл — деньги остаются клубу.')}
      </p>
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        <div className="flex items-center gap-3 self-start rounded-xl border border-gray-200 p-4 dark:border-gray-700">
          <label htmlFor="app_booking_enabled" className="text-sm font-medium text-gray-700 dark:text-gray-300">{t('Бронь из приложения')}</label>
          <input type="hidden" name="app_booking_enabled" value="0" />
          <input id="app_booking_enabled" type="checkbox" role="switch" name="app_booking_enabled" value="1" className="settings-switch" defaultChecked={enabled} />
        </div>
        <div className="rounded-xl border border-gray-200 p-4 dark:border-gray-700">
          <div className="flex items-center gap-3">
            <label htmlFor="app_booking_cancel" className="text-sm font-medium text-gray-700 dark:text-gray-300">{t('Гость может отменять бронь')}</label>
            <input type="hidden" name="app_booking_cancel" value="0" />
            <input id="app_booking_cancel" type="checkbox" role="switch" name="app_booking_cancel" value="1" className="settings-switch" defaultChecked={cancel} />
          </div>
          <p className="mt-1 text-xs text-gray-500">
            {t('До начала брони гость может отменить её в приложении, деньги вернутся на баланс. Если выключено — отменить бронь может только администратор (тоже с возвратом).')}
          </p>
        </div>
      </div>
    </SettingsCard>
  );
}
