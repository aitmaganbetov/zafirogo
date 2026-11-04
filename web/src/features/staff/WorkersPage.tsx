import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Fragment, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { can, roleOf, useSession } from '../../auth/session';
import { ConfirmForm } from '../../components/ConfirmForm';
import { SimplePagination } from '../../components/SimplePagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { useFlash } from '../../lib/flash';
import { serverErrors } from '../auth/serverErrors';
import {
  addWorker,
  changeWorkerRole,
  createInvitation,
  invitationUrl,
  removeWorker,
  workersHref,
  workersQuery,
  type Worker,
  type WorkersSearch,
} from './api';

/*
 * Работники — перенос resources/views/workers/index.blade.php (WorkerController, ClubInvitationController::store()):
 * добавить зарегистрированного по телефону, пригласить ссылкой, сменить роль (onchange → отправка), убрать (confirm),
 * кто на смене и лимиты, ссылки на права сотрудника. Пагинация — keyset (simple-tailwind вместо номеров страниц).
 */

const FIELD = 'min-h-10 rounded-lg border border-gray-300 bg-white px-3 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-900 dark:text-white';

export function WorkersPage({ search }: { search: WorkersSearch }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const queryClient = useQueryClient();
  useAdminPageTitle(t('Работники'));
  const flash = useFlash('status');
  const [status, setStatus] = useState<string | null>(flash);
  const [error, setError] = useState<string | null>(null);
  const [inviteUrl, setInviteUrl] = useState<string | null>(null);
  const [phone, setPhone] = useState('');
  const [addRole, setAddRole] = useState('cashier');
  const [email, setEmail] = useState('');
  const [inviteRole, setInviteRole] = useState('operator');
  const [busy, setBusy] = useState(false);
  const { data } = useQuery(workersQuery(search));

  const readOnly = me?.read_only === true;
  const myRole = roleOf(me);
  const assignable = data?.assignable_roles ?? [];

  /** Действие формы: сообщение — status, ошибка — первая ошибка ($errors->first()). */
  const run = async (action: () => Promise<{ message: string }>, after?: () => void) => {
    setBusy(true);
    try {
      const res = await action();
      setError(null);
      setStatus(res.message);
      after?.();
      await queryClient.invalidateQueries({ queryKey: ['staff'] });
    } catch (e) {
      setError(serverErrors(e).first);
    } finally {
      setBusy(false);
    }
  };

  const onAdd = (e: FormEvent) => {
    e.preventDefault();
    void run(() => addWorker(phone, addRole), () => setPhone(''));
  };

  const onInvite = (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    createInvitation(email.trim() === '' ? null : email.trim(), inviteRole)
      .then((res) => {
        setError(null);
        setStatus(res.message);
        setInviteUrl(invitationUrl(res.path));
        setEmail('');
      })
      .catch((err: unknown) => setError(serverErrors(err).first))
      .finally(() => setBusy(false));
  };

  const shiftLimits = data?.shift_limits ?? [];
  const pager = {
    prevHref: data?.prev_cursor != null ? workersHref({ before: data.prev_cursor }) : null,
    nextHref: data?.next_cursor != null ? workersHref({ cursor: data.next_cursor }) : null,
  };

  return (
    <div className="p-6 lg:p-8">
      <header className="mb-6 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h3 className="text-2xl font-bold text-gray-900 dark:text-white">{t('Работники')}</h3>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-400">
            {t('Сотрудники клуба. Добавить можно пользователя, который уже зарегистрирован в Zafiro, или отправить ссылку-приглашение.')}
          </p>
        </div>
        <a href="/users" className="text-sm font-medium text-green-700 hover:underline dark:text-green-400">
          {t('Клиенты →')}
        </a>
      </header>

      {status && (
        <div className="mb-4 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700 dark:border-green-800 dark:bg-green-900/20 dark:text-green-300">
          {t(status)}
        </div>
      )}
      {error && (
        <div className="mb-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300" role="alert">
          {t(error)}
        </div>
      )}
      {inviteUrl && (
        <div className="mb-4 flex gap-2">
          <input id="club-invite-url" readOnly value={inviteUrl} className="min-w-0 flex-1 rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm dark:border-gray-700 dark:bg-gray-900 dark:text-white" />
          <button type="button" data-copy-invite className="min-h-10 rounded-lg border border-gray-300 px-4 text-sm font-medium dark:border-gray-700" onClick={() => void navigator.clipboard.writeText(inviteUrl)}>
            {t('Копировать ссылку')}
          </button>
        </div>
      )}

      {can(me, 'workers.create') && !readOnly && (
        <div className="mb-6 grid gap-4 lg:grid-cols-2">
          <form method="POST" action="/workers" className="rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-800 dark:bg-gray-900" onSubmit={onAdd}>
            <h4 className="mb-3 font-semibold text-gray-900 dark:text-white">{t('Добавить зарегистрированного пользователя')}</h4>
            <div className="flex flex-wrap items-center gap-2">
              <div className="flex">
                <span className="inline-flex items-center rounded-l-lg border border-r-0 border-gray-300 bg-gray-100 px-3 text-sm text-gray-600 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-300">+7</span>
                <input name="phone" type="tel" inputMode="numeric" maxLength={16} required value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="700 123 45 67" aria-label={t('Телефон пользователя')} className={`${FIELD} w-40 rounded-l-none`} />
              </div>
              <select name="role" required aria-label={t('Роль')} className={FIELD} value={addRole} onChange={(e) => setAddRole(e.target.value)}>
                {assignable.map((r) => (
                  <option key={r.role} value={r.role}>
                    {t(r.label)}
                  </option>
                ))}
              </select>
              <button type="submit" className="brand-button min-h-10 px-4" disabled={busy}>
                <i className="fas fa-user-plus"></i>
                {t('Добавить')}
              </button>
            </div>
          </form>
          <form action="/club-invitations" method="POST" className="rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-800 dark:bg-gray-900" onSubmit={onInvite}>
            <h4 className="mb-3 font-semibold text-gray-900 dark:text-white">{t('Пригласить по ссылке')}</h4>
            <div className="flex flex-wrap items-center gap-2">
              <input name="email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder={t('Email (необязательно)')} aria-label={t('Email приглашённого')} className={FIELD} />
              <select name="role" required aria-label={t('Роль приглашённого')} className={FIELD} value={inviteRole} onChange={(e) => setInviteRole(e.target.value)}>
                {assignable.map((r) => (
                  <option key={r.role} value={r.role}>
                    {t(r.label)}
                  </option>
                ))}
              </select>
              <button type="submit" className="min-h-10 rounded-lg border border-gray-300 px-4 text-sm font-medium dark:border-gray-700" disabled={busy}>
                <i className="fas fa-link"></i> {t('Создать ссылку')}
              </button>
            </div>
          </form>
        </div>
      )}

      <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">
        {shiftLimits.map((s) => (
          <Fragment key={s.role}>
            <span className="mr-4">
              {s.role === 'cashier' ? t('Кассиров на смене') : t('Операторов на смене')}:{' '}
              <strong>
                {s.count}
                {s.limit ? ` / ${s.limit}` : ''}
              </strong>
            </span>{' '}
          </Fragment>
        ))}
        <a href="/settings/club" className="text-green-700 hover:underline dark:text-green-400">
          {t('Лимит в настройках клуба')}
        </a>
      </p>

      <div className="overflow-x-auto rounded-xl border border-gray-200 bg-white dark:border-gray-800 dark:bg-gray-900">
        <table className="w-full min-w-[720px] text-left text-sm">
          <thead className="bg-gray-50 text-xs uppercase text-gray-500 dark:bg-gray-800 dark:text-gray-400">
            <tr>
              <th className="px-5 py-3 font-semibold">{t('Имя')}</th>
              <th className="px-5 py-3 font-semibold">{t('Номер')}</th>
              <th className="px-5 py-3 font-semibold">{t('Роль')}</th>
              <th className="px-5 py-3 font-semibold">{t('Смена')}</th>
              <th className="px-5 py-3">
                <span className="sr-only">{t('Действия')}</span>
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {(data?.items ?? []).map((w) => (
              <WorkerRow key={w.membership_id} worker={w} canUpdate={can(me, 'workers.update') && !readOnly}
                canDelete={can(me, 'workers.delete') && !readOnly} rightsLink={myRole === 'owner' || myRole === 'admin'}
                assignable={assignable} busy={busy}
                onRole={(role) => void run(() => changeWorkerRole(w.membership_id, role))}
                onRemove={() => void run(() => removeWorker(w.membership_id))} />
            ))}
          </tbody>
        </table>
      </div>
      <div className="mt-4">
        <SimplePagination {...pager} />
      </div>
    </div>
  );
}

interface WorkerRowProps {
  readonly worker: Worker;
  readonly canUpdate: boolean;
  readonly canDelete: boolean;
  readonly rightsLink: boolean;
  readonly assignable: readonly { role: string; label: string }[];
  readonly busy: boolean;
  readonly onRole: (role: string) => void;
  readonly onRemove: () => void;
}

function WorkerRow({ worker: w, canUpdate, canDelete, rightsLink, assignable, busy, onRole, onRemove }: WorkerRowProps) {
  const { t } = useTranslation();
  return (
    <tr className="text-gray-700 dark:text-gray-300" data-worker-row>
      <td className="px-5 py-3 font-medium text-gray-900 dark:text-white">{w.name}</td>
      <td className="whitespace-nowrap px-5 py-3">{w.phone}</td>
      <td className="px-5 py-3">
        {w.manageable && canUpdate ? (
          <form method="POST" className="flex items-center gap-2" onSubmit={(e) => e.preventDefault()}>
            <select name="role" aria-label={t('Роль')} className={FIELD} value={w.role} disabled={busy} onChange={(e) => onRole(e.target.value)}>
              {assignable.map((r) => (
                <option key={r.role} value={r.role}>
                  {t(r.label)}
                </option>
              ))}
            </select>
          </form>
        ) : (
          t(w.role_label)
        )}
      </td>
      <td className="px-5 py-3">
        {w.on_shift ? (
          <span className="rounded-full bg-green-100 px-2 py-1 text-xs font-medium text-green-800 dark:bg-green-900/30 dark:text-green-300">{t('На смене')}</span>
        ) : (
          <span className="text-gray-400">—</span>
        )}
      </td>
      <td className="px-5 py-3 text-right">
        {/* Rights of the employee: the owner, or an admin for non-admins (PermissionController). */}
        {w.manageable && rightsLink && (
          <a href={`/settings/permissions/${w.membership_id}`} className="mr-3 text-sm font-medium text-green-700 hover:underline dark:text-green-400">
            {t('Права')}
          </a>
        )}{' '}
        {w.manageable && canDelete && (
          <ConfirmForm className="inline" message={t('Убрать из сотрудников? Аккаунт останется клиентом клуба.')} onConfirm={onRemove}>
            <button type="submit" className="text-sm text-red-600 hover:underline">
              {t('Убрать')}
            </button>
          </ConfirmForm>
        )}
      </td>
    </tr>
  );
}
