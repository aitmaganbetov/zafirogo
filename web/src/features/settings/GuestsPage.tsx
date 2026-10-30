import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { SettingsCard } from '../../components/SettingsCard';
import { SettingsLayout } from '../../components/SettingsLayout';
import { clubSettingsQuery } from './api';
import { SettingsForm } from './SettingsForm';
import { useSettingsResult } from './status';

/* Настройки → Гости — перенос resources/views/settings/guests.blade.php (SettingsController::guests()). */
export function GuestsPage() {
  const { t } = useTranslation();
  const { me } = useSession();
  const { data } = useQuery(clubSettingsQuery());
  const { status, done, fail } = useSettingsResult();
  if (!data) return null;
  const g = data.settings.guests;
  const toggle = (name: keyof typeof g, label: string) => (
    <div className="inline-flex items-center gap-3 rounded-xl border border-gray-200 p-4 dark:border-gray-700">
      <label htmlFor={name} className="text-sm font-medium text-gray-700 dark:text-gray-300">
        {label}
      </label>
      <input type="hidden" name={name} value="0" />
      <input id={name} type="checkbox" role="switch" name={name} value="1" className="settings-switch" defaultChecked={g[name]} />
    </div>
  );

  return (
    <SettingsLayout status={status}>
      <SettingsForm key={JSON.stringify(g)} section="guests" readOnly={me?.read_only === true} onDone={done} onError={fail('guests')}>
        <h4 className="text-xl font-semibold text-gray-900 dark:text-white">{t('Гости')}</h4>
        <SettingsCard className="flex flex-col items-start gap-3">
          {toggle('guest_mode_enabled', t('Включить гостевой режим'))}
          <p className="-mt-1 max-w-xl text-xs text-gray-500">{t('На карте клуба у выбранных ПК появится кнопка «Подключить тариф»: время для гостя без аккаунта, оплата на кассе. После перезагрузки ПК гостевое время сбрасывается.')}</p>
          {toggle('show_qr_code_on_login', t('Отображать QR-код на странице авторизации'))}
          {toggle('allow_guest_self_transfer', t('Разрешить самостоятельную пересадку гостя'))}
          {toggle('allow_adding_friends', t('Возможность добавления в друзья'))}
          {toggle('allow_balance_transfer_to_friends', t('Разрешить перевод баланса друзьям'))}
        </SettingsCard>
      </SettingsForm>
    </SettingsLayout>
  );
}
