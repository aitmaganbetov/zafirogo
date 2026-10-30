import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { SettingsCard } from '../../components/SettingsCard';
import { SettingsLayout } from '../../components/SettingsLayout';
import { useFlash } from '../../lib/flash';
import { permissionStaffQuery } from './api';

/*
 * Настройки → Права (список) — перенос resources/views/settings/permissions.blade.php (PermissionController::index()):
 * персонал кроме владельца, «права по роли / изменены», ссылка «Настроить права» (mayEdit) или «Права задаёт владелец».
 */
export function PermissionsPage() {
  const { t } = useTranslation();
  const status = useFlash('status');
  const { data } = useQuery(permissionStaffQuery());
  const staff = data?.items ?? [];

  return (
    <SettingsLayout status={status ? t(status) : null}>
      <SettingsCard id="permissions" aria-labelledby="permissions-title">
        <h5 id="permissions-title" className="mb-1 text-lg font-semibold text-gray-900 dark:text-white">
          {t('Права сотрудников')}
        </h5>
        <p className="mb-4 max-w-3xl text-sm text-gray-500 dark:text-gray-400">
          {t('Выберите сотрудника, чтобы отметить, что он может делать в каждом разделе. Пока права не меняли, у сотрудника права его роли. Владелец может всё.')}
        </p>

        {data && staff.length === 0 ? (
          <p className="text-sm text-gray-500 dark:text-gray-400">{t('В клубе пока нет сотрудников. Добавьте их на странице «Работники».')}</p>
        ) : (
          <ul className="divide-y divide-gray-100 rounded-xl border border-gray-200 dark:divide-gray-800 dark:border-gray-800">
            {staff.map((m) => (
              <li key={m.membership_id} className="flex flex-wrap items-center justify-between gap-3 px-4 py-3" data-permission-member>
                <div>
                  <span className="block font-medium text-gray-900 dark:text-white">{m.name}</span>
                  <span className="text-sm text-gray-500 dark:text-gray-400">
                    {t(m.role_label)} · {m.phone} · {m.customized ? t('права изменены') : t('права по роли')}
                  </span>
                </div>
                {m.editable ? (
                  <a href={`/settings/permissions/${m.membership_id}`} className="brand-button rounded-xl px-4 py-2 text-sm">
                    {t('Настроить права')}
                  </a>
                ) : (
                  <span className="text-sm text-gray-500 dark:text-gray-400">{t('Права задаёт владелец')}</span>
                )}
              </li>
            ))}
          </ul>
        )}
      </SettingsCard>
    </SettingsLayout>
  );
}
