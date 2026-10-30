import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { SettingsCard } from '../../components/SettingsCard';
import { SettingsLayout } from '../../components/SettingsLayout';
import { serverErrors } from '../auth/serverErrors';
import { resetStaffPermissions, staffPermissionsQuery, updateStaffPermissions, type StaffPermissions } from './api';

/*
 * Настройки → Права сотрудника — перенос resources/views/settings/permissions-edit.blade.php (PermissionController::
 * edit/update/reset): матрица «раздел × действие» (rights[section][action]); отличие от прав роли — рамка; «—» —
 * действия нет. Сохранение — PUT, сброс к правам роли — POST …/reset; права сотрудника действуют сразу
 * (staff.access_changed переподключает его WebSocket и перечитывает права).
 */

type Rights = Record<string, Record<string, boolean>>;

function rightsOf(p: StaffPermissions): Rights {
  const out: Rights = {};
  for (const s of p.sections) {
    const row: Record<string, boolean> = {};
    for (const c of s.cells) row[c.action] = c.allowed;
    out[s.section] = row;
  }
  return out;
}

export function PermissionsEditPage({ membershipId }: { membershipId: number }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const queryClient = useQueryClient();
  const { data } = useQuery(staffPermissionsQuery(membershipId));
  // Изменения формы поверх прав с сервера (снимаются после сохранения).
  const [edits, setEdits] = useState<Rights>({});
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const readOnly = me?.read_only === true;

  const done = async (p: Promise<{ message: string }>) => {
    setBusy(true);
    try {
      const res = await p;
      setStatus(res.message);
      setError(null);
      setEdits({});
      await queryClient.invalidateQueries({ queryKey: ['staff'] });
    } catch (e) {
      setError(serverErrors(e).first);
    } finally {
      setBusy(false);
    }
  };

  const onSave = (e: FormEvent) => {
    e.preventDefault();
    if (!data) return;
    const rights = rightsOf(data);
    for (const [section, row] of Object.entries(edits)) rights[section] = { ...rights[section], ...row };
    void done(updateStaffPermissions(membershipId, rights));
  };
  const onReset = (e: FormEvent) => {
    e.preventDefault();
    void done(resetStaffPermissions(membershipId));
  };

  return (
    <SettingsLayout status={status ? t(status) : null}>
      {error && (
        <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300" role="alert">
          {t(error)}
        </div>
      )}
      <SettingsCard id="permissions" aria-labelledby="permissions-title">
        <a href="/settings/permissions" className="mb-3 inline-block text-sm text-gray-600 hover:underline dark:text-gray-400">
          {t('← Все сотрудники')}
        </a>
        {data && (
          <>
            <h5 id="permissions-title" className="text-lg font-semibold text-gray-900 dark:text-white">
              {t('Права: {{user}}', { user: data.member.name })}
            </h5>
            <p className="mb-4 max-w-3xl text-sm text-gray-500 dark:text-gray-400">
              {t('Роль: {{role}}. Отметьте, что сотрудник может в каждом разделе. Отличия от прав роли отмечены рамкой; «—» — такого действия в разделе нет.', {
                role: t(data.member.role_label),
              })}
            </p>

            <form method="POST" onSubmit={onSave}>
              <div className="overflow-x-auto">
                <table className="w-full min-w-[640px] text-left text-sm">
                  <thead className="border-b border-gray-200 text-xs uppercase text-gray-500 dark:border-gray-800 dark:text-gray-400">
                    <tr>
                      <th className="px-3 py-3 font-semibold">{t('Раздел')}</th>
                      {data.actions.map((a) => (
                        <th key={a.action} className="px-3 py-3 text-center font-semibold">
                          {t(a.label)}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                    {data.sections.map((s) => (
                      <tr key={s.section}>
                        <td className="px-3 py-3 font-medium text-gray-900 dark:text-white">{t(s.label)}</td>
                        {data.actions.map((a) => {
                          const cell = s.cells.find((c) => c.action === a.action);
                          return (
                            <td key={a.action} className="px-3 py-3 text-center">
                              {cell ? (
                                <>
                                  <input
                                    type="checkbox"
                                    name={`rights[${s.section}][${a.action}]`}
                                    value="1"
                                    title={t(cell.description)}
                                    aria-label={`${t(s.label)}: ${t(a.label)} — ${t(cell.description)}`}
                                    checked={edits[s.section]?.[a.action] ?? cell.allowed}
                                    disabled={readOnly}
                                    onChange={(e) =>
                                      setEdits((r) => ({ ...r, [s.section]: { ...r[s.section], [a.action]: e.target.checked } }))
                                    }
                                    className={cell.given ? 'brand-check h-5 w-5 rounded border-gray-300 ring-2 ring-amber-400 ring-offset-2 dark:ring-offset-gray-900' : 'brand-check h-5 w-5 rounded border-gray-300'}
                                  />
                                  <span className="mt-1 block text-xs text-gray-500 dark:text-gray-400">{t(cell.description)}</span>
                                </>
                              ) : (
                                <span className="text-gray-400" aria-hidden="true">
                                  —
                                </span>
                              )}
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <div className="mt-4 flex flex-wrap items-center justify-end gap-3">
                <button type="submit" className="brand-button rounded-xl px-5 py-2 text-sm transition" disabled={busy || readOnly}>
                  {t('Сохранить права')}
                </button>
              </div>
            </form>
            {data.member.customized && (
              <form method="POST" className="mt-3 text-right" onSubmit={onReset}>
                <button type="submit" className="text-sm text-gray-600 hover:underline dark:text-gray-400" disabled={busy || readOnly}>
                  {t('Вернуть права по роли')}
                </button>
              </form>
            )}
          </>
        )}
      </SettingsCard>
    </SettingsLayout>
  );
}
