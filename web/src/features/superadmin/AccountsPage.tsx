import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Pagination } from '../../components/Pagination';
import { useSession } from '../../auth/session';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { isApiError } from '../../api/http';
import { accountAction, accountsQuery, CODE_ACTIONS, firstError, qs, type AccountAction, type AccountsSearch } from './api';
import { saHref } from './host';
import { roleLabel } from './roles';
import { useSuperadminFlash } from './flash';

/*
 * Пользователи — перенос resources/views/superadmin/users/index.blade.php (Superadmin\UserController).
 * Этап 7 (новое): метка «2FA не настроена» у суперадмина и сброс второго фактора другого суперадмина (не в режиме
 * просмотра клуба — сервер ответит 403 read_only).
 * Аудит A-10 (новое, осознанное отличие — docs/open-questions/audit-ui.md AU-14): выдача и снятие прав суперадмина
 * и сброс 2FA подтверждаются не window.confirm, а окном с тем же вопросом и полем свежего кода TOTP; сервер без
 * кода действие не выполнит. Окно — <dialog> на тех же классах, что карточки и кнопки кабинета (своего CSS нет).
 */

interface PendingAction {
  readonly id: number;
  readonly action: AccountAction;
  readonly question: string;
}

export function AccountsPage({ search }: { readonly search: AccountsSearch }) {
  const { t } = useTranslation();
  useAdminPageTitle(t('Пользователи'));
  const flash = useSuperadminFlash();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data } = useQuery(accountsQuery(search));
  const readOnly = useSession().me?.read_only === true;
  const [pending, setPending] = useState<PendingAction | null>(null);
  const [codeError, setCodeError] = useState<string | null>(null);
  const dialogRef = useRef<HTMLDialogElement>(null);
  const act = useMutation({
    mutationFn: ({ id, action, code }: { id: number; action: AccountAction; code?: string }) => accountAction(id, action, code),
    onSuccess: (r) => {
      setPending(null);
      flash.status(r.message);
      return queryClient.invalidateQueries({ queryKey: ['superadmin'] });
    },
    onError: (e, vars) => {
      // Ошибка кода (неверный, уже использованный, не введён) — под полем, окно остаётся открытым.
      const code = vars.code !== undefined && isApiError(e) ? e.field('code') : undefined;
      if (code) {
        setCodeError(code);
        return;
      }
      setPending(null);
      flash.error(firstError(e));
    },
  });
  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    if (pending && !dialog.open) dialog.showModal();
    if (!pending && dialog.open) dialog.close();
  }, [pending]);
  if (!data) return null;

  const submitFilter = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    void navigate({ href: saHref('/accounts' + qs({ search: String(f.get('search') ?? ''), filter: String(f.get('filter') ?? '') })) });
  };
  const confirmed = (id: number, action: AccountAction, question?: string) => (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (CODE_ACTIONS.includes(action)) {
      setCodeError(null);
      setPending({ id, action, question: question ?? '' });
      return;
    }
    if (question === undefined || window.confirm(question)) act.mutate({ id, action });
  };
  const submitCode = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!pending) return;
    const code = String(new FormData(e.currentTarget).get('code') ?? '').trim();
    setCodeError(null);
    act.mutate({ id: pending.id, action: pending.action, code });
  };

  return (
    <>
      <form method="GET" className="mb-4 flex flex-wrap items-end gap-3 rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-900" onSubmit={submitFilter}>
        <label className="min-w-56 flex-1">
          <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Поиск')}</span>
          <input type="search" name="search" defaultValue={search.search ?? ''} placeholder={t('Имя или телефон')} className="w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2" />
        </label>
        <label>
          <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Показать')}</span>
          <select name="filter" defaultValue={search.filter ?? ''} className="rounded-lg border border-gray-300 bg-gray-50 px-3 py-2">
            <option value="">{t('Все')}</option>
            <option value="blocked">{t('Заблокированные')}</option>
            <option value="superadmin">{t('Суперадмины')}</option>
          </select>
        </label>
        <button type="submit" className="rounded-lg border border-gray-300 px-4 py-2 text-sm">
          {t('Найти')}
        </button>
      </form>

      <div className="overflow-x-auto rounded-xl border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-900">
        <table className="w-full text-sm">
          <thead className="border-b border-gray-200 dark:border-gray-700">
            <tr className="text-left text-gray-600">
              <th className="px-4 py-3">{t('Пользователь')}</th>
              <th className="px-4 py-3">{t('Клубы и роли')}</th>
              <th className="px-4 py-3">{t('Статус')}</th>
              <th className="px-4 py-3"></th>
            </tr>
          </thead>
          <tbody>
            {data.items.map((user) => {
              const blocked = user.blocked_since != null;
              return (
                <tr key={user.id} className="border-t border-gray-100 align-top dark:border-gray-800">
                  <td className="px-4 py-3">
                    <div className="font-medium text-gray-900 dark:text-white">
                      {user.name}
                      {user.self && (
                        <>
                          {' '}
                          <span className="text-xs text-gray-500">({t('вы')})</span>
                        </>
                      )}
                    </div>
                    <div className="font-mono text-xs text-gray-500">{user.phone}</div>
                  </td>
                  <td className="px-4 py-3">
                    {user.memberships.length === 0 ? (
                      <span className="text-gray-400">—</span>
                    ) : (
                      user.memberships.map((m) => (
                        <div key={m.club_id}>
                          <a href={saHref(`/clubs/${m.club_id}`)} className="text-violet-700 hover:underline">
                            {m.club_name}
                          </a>{' '}
                          <span className="text-gray-500">· {roleLabel(t, m.role)}</span>
                        </div>
                      ))
                    )}
                  </td>
                  <td className="px-4 py-3 space-y-1">
                    {user.is_superadmin && <span className="inline-block rounded-full bg-violet-100 px-2 py-1 text-xs text-violet-800">{t('Суперадмин')}</span>}
                    {user.is_superadmin && !user.second_factor && (
                      <span className="inline-block rounded-full bg-red-100 px-2 py-1 text-xs text-red-800">{t('2FA не настроена')}</span>
                    )}
                    {blocked && (
                      <span className="inline-block rounded-full bg-red-100 px-2 py-1 text-xs text-red-800">{t('Заблокирован с {{date}}', { date: user.blocked_since })}</span>
                    )}
                  </td>
                  <td className="px-4 py-3">
                    {!user.self && (
                      <div className="flex flex-wrap justify-end gap-2">
                        {blocked ? (
                          <form method="POST" onSubmit={confirmed(user.id, 'unblock')}>
                            <button type="submit" className="rounded-lg border border-gray-300 px-3 py-1 text-sm">
                              {t('Разблокировать')}
                            </button>
                          </form>
                        ) : (
                          !user.is_superadmin && (
                            <form method="POST" onSubmit={confirmed(user.id, 'block', t('Заблокировать {{user}}? Он сразу выйдет из всех сеансов и приложений.', { user: user.name }))}>
                              <button type="submit" className="rounded-lg border border-red-200 px-3 py-1 text-sm text-red-700">
                                {t('Заблокировать')}
                              </button>
                            </form>
                          )
                        )}
                        {user.second_factor && !readOnly && (
                          <form
                            method="POST"
                            onSubmit={confirmed(user.id, 'reset-second-factor', t('Сбросить двухфакторную аутентификацию {{user}}? Он выйдет из всех сеансов и при следующем входе настроит её заново.', { user: user.name }))}
                          >
                            <button type="submit" className="rounded-lg border border-red-200 px-3 py-1 text-sm text-red-700">
                              {t('Сбросить 2FA')}
                            </button>
                          </form>
                        )}
                        {user.is_superadmin ? (
                          <form method="POST" onSubmit={confirmed(user.id, 'revoke', t('Снять с {{user}} права суперадмина?', { user: user.name }))}>
                            <button type="submit" className="rounded-lg border border-gray-300 px-3 py-1 text-sm">
                              {t('Снять права суперадмина')}
                            </button>
                          </form>
                        ) : (
                          !blocked && (
                            <form method="POST" onSubmit={confirmed(user.id, 'grant', t('Выдать {{user}} права суперадмина? Он получит доступ ко всем клубам платформы.', { user: user.name }))}>
                              <button type="submit" className="rounded-lg border border-violet-200 px-3 py-1 text-sm text-violet-700">
                                {t('Сделать суперадмином')}
                              </button>
                            </form>
                          )
                        )}
                      </div>
                    )}
                  </td>
                </tr>
              );
            })}
            {data.items.length === 0 && (
              <tr>
                <td colSpan={4} className="px-4 py-8 text-center text-gray-500">
                  {t('Пользователи не найдены.')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <dialog
        ref={dialogRef}
        aria-labelledby="second-factor-confirm-title"
        className="m-auto w-[calc(100%-2rem)] max-w-md rounded-xl border border-gray-200 bg-white p-5 text-gray-900 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-100"
        onClose={() => setPending(null)}
        onClick={(e) => {
          if (e.target === dialogRef.current) setPending(null);
        }}
      >
        {pending && (
          <form method="POST" key={`${pending.action}-${pending.id}`} onSubmit={submitCode}>
            <h3 id="second-factor-confirm-title" className="font-semibold text-gray-900 dark:text-white">
              {t('Подтверждение кодом')}
            </h3>
            <p className="mt-2 text-sm text-gray-600 dark:text-gray-400">{pending.question}</p>
            <label className="mt-4 block">
              <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Код из приложения')}</span>
              <input
                type="text"
                name="code"
                required
                autoFocus
                maxLength={7}
                inputMode="numeric"
                autoComplete="one-time-code"
                placeholder="123456"
                aria-invalid={codeError != null}
                aria-describedby="second-factor-confirm-hint"
                className="w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100"
              />
            </label>
            {codeError && (
              <p role="alert" className="mt-1 text-sm text-red-600">
                {codeError}
              </p>
            )}
            <p id="second-factor-confirm-hint" className="mt-2 text-sm text-gray-500">
              {t('Введите новый код из приложения-аутентификатора. Код, с которым вы вошли или уже подтвердили действие, не подойдёт.')}
            </p>
            <div className="mt-4 flex flex-wrap justify-end gap-2">
              <button type="button" className="rounded-lg border border-gray-300 px-4 py-2 text-sm" onClick={() => setPending(null)}>
                {t('Отмена')}
              </button>
              <button type="submit" disabled={act.isPending} className="rounded-lg border border-violet-200 px-4 py-2 text-sm text-violet-700">
                {t('Подтвердить')}
              </button>
            </div>
          </form>
        )}
      </dialog>
      <div className="mt-4">
        <Pagination
          page={data.page}
          perPage={data.per_page}
          total={data.total}
          count={data.items.length}
          hrefFor={(p) => saHref('/accounts' + qs({ ...search, page: p > 1 ? p : undefined }))}
        />
      </div>
    </>
  );
}
