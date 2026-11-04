import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useEffect, useRef, useState, type FormEvent, type MouseEvent } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { idempotencyKey } from '../../api/http';
import { can, roleOf, useSession } from '../../auth/session';
import { ConfirmForm } from '../../components/ConfirmForm';
import { SimplePagination } from '../../components/SimplePagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { useFlash } from '../../lib/flash';
import { formatMoney } from '../../lib/money';
import { useInvalidateOn } from '../../realtime/hooks';
import { NO_ERRORS, serverErrors, type FormServerErrors } from '../auth/serverErrors';
import {
  createCustomer,
  customersHref,
  customersQuery,
  resetCustomerPassword,
  revealPassword,
  updateCustomer,
  type Customer,
  type CustomersSearch,
} from './api';
import { buildCreateBody, buildUpdateBody, type CreateForm, type EditForm } from './forms';

/*
 * Пользователи (клиенты) — перенос resources/views/users.blade.php (UserController::index/store/update/resetPassword):
 * разметка и классы один-в-один, диалоги <dialog> «Новый клиент» и «Изменить клиента», сброс пароля с confirm.
 * Пагинация — keyset (simple-tailwind вместо номеров страниц), поиск — параметр ?q= (поля поиска в старом экране нет).
 */

const INPUT =
  'w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-white';
const LABEL = 'mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300';
const ERROR_BOX =
  'mb-5 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300';
const STATUS_BOX =
  'mb-4 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700 dark:border-green-800 dark:bg-green-900/20 dark:text-green-300';
/** Ошибка действия в строке (лимит сброса пароля и т. п.): в старом — страница ошибки Laravel. */
const PAGE_ERROR_BOX =
  'mb-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300';

const CREATE_DEFAULTS: CreateForm = {
  name: '', phone: '', email: '', password: '', password_confirmation: '', discount: '0', balance: '0', bonus_balance: '0',
};

/** Закрытие <dialog> по клику на фон (event.target === dialog). */
function closeOnBackdrop(e: MouseEvent<HTMLDialogElement>) {
  if (e.target === e.currentTarget) e.currentTarget.close();
}

export function UsersPage({ search }: { search: CustomersSearch }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  useAdminPageTitle(t('Пользователи'));
  useInvalidateOn(['balance.changed'], ['customers']);
  const flash = useFlash('status');
  const [status, setStatus] = useState<string | null>(flash);
  const [pageError, setPageError] = useState<string | null>(null);
  const { data } = useQuery(customersQuery(search));

  const readOnly = me?.read_only === true;
  const canCreate = can(me, 'customers.create') && !readOnly;
  const canUpdate = can(me, 'customers.update') && !readOnly;
  const canSetMoney = can(me, 'customer_money.update');
  const canTopUp = roleOf(me) === 'owner';

  // --- «Новый клиент» ---
  const createRef = useRef<HTMLDialogElement>(null);
  const [createErrors, setCreateErrors] = useState<FormServerErrors>(NO_ERRORS);
  const createKey = useRef<string | null>(null);
  const createForm = useForm<CreateForm>({ defaultValues: CREATE_DEFAULTS });
  const submitCreate = async (v: CreateForm) => {
    const built = buildCreateBody(v, canSetMoney);
    if (!built.ok) {
      setCreateErrors({ first: built.error, fields: {} });
      return;
    }
    createKey.current ??= idempotencyKey();
    try {
      const res = await createCustomer(built.body, createKey.current);
      createKey.current = null;
      setCreateErrors(NO_ERRORS);
      createForm.reset(CREATE_DEFAULTS);
      createRef.current?.close();
      setStatus(res.message);
      await queryClient.invalidateQueries({ queryKey: ['customers'] });
    } catch (e) {
      createKey.current = null;
      setCreateErrors(serverErrors(e));
    }
  };
  const onCreate = (e: FormEvent<HTMLFormElement>) => void createForm.handleSubmit(submitCreate)(e);

  // --- «Изменить клиента» ---
  const editRef = useRef<HTMLDialogElement>(null);
  const [editing, setEditing] = useState<Customer | null>(null);
  const [editErrors, setEditErrors] = useState<FormServerErrors>(NO_ERRORS);
  const editKey = useRef<string | null>(null);
  const editForm = useForm<EditForm>({ defaultValues: { name: '', email: '', discount: '', top_up_amount: '', top_up_balance: 'balance', balance_action: 'top_up' } });
  const openEdit = (c: Customer) => {
    setEditing(c);
    setEditErrors(NO_ERRORS);
    editKey.current = null;
    editForm.reset({ name: c.name, email: c.email ?? '', discount: c.discount, top_up_amount: '', top_up_balance: 'balance', balance_action: 'top_up' });
    editRef.current?.showModal();
  };
  const submitEdit = async (v: EditForm) => {
    if (!editing) return;
    const built = buildUpdateBody(v, { canSetMoney, canTopUp });
    if (!built.ok) {
      setEditErrors({ first: built.error, fields: {} });
      return;
    }
    const key = built.body.top_up_amount_tiyn != null ? (editKey.current ??= idempotencyKey()) : null;
    try {
      const res = await updateCustomer(editing.user_id, built.body, key);
      editKey.current = null;
      editRef.current?.close();
      setStatus(res.message);
      await queryClient.invalidateQueries({ queryKey: ['customers'] });
    } catch (e) {
      editKey.current = null;
      setEditErrors(serverErrors(e));
    }
  };
  const onEdit = (e: FormEvent<HTMLFormElement>) => void editForm.handleSubmit(submitEdit)(e);

  const onResetPassword = async (c: Customer) => {
    try {
      const res = await resetCustomerPassword(c.user_id);
      revealPassword(res);
      await navigate({ to: '/users/$id/password', params: { id: String(c.user_id) } });
    } catch (e) {
      setPageError(serverErrors(e).first);
    }
  };

  useEffect(() => {
    const d = createRef.current;
    if (d && createErrors.first && !d.open) d.showModal(); // data-open-on-load
  }, [createErrors]);

  const items = data?.items ?? [];
  const pager = {
    prevHref: data?.prev_before_id != null ? customersHref({ q: search.q, before: data.prev_before_id }) : null,
    nextHref: data?.next_after_id != null ? customersHref({ q: search.q, after: data.next_after_id }) : null,
  };
  const locked = editing?.account_locked === true;

  return (
    <div className="p-6 lg:p-8">
      <div className="mb-6 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h3 className="text-2xl font-bold text-gray-900 dark:text-white">{t('Пользователи')}</h3>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-400">{t('Всего: {{count}}', { count: data?.total ?? 0 })}</p>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          {can(me, 'workers.view') && (
            <a href="/workers" className="min-h-10 inline-flex items-center gap-2 rounded-lg border border-gray-300 px-4 text-sm font-medium text-gray-700 dark:border-gray-700 dark:text-gray-200">
              <i className="fas fa-user-tie"></i>
              {t('Работники')}
            </a>
          )}
          {canCreate && (
            <button id="open-user-dialog" type="button" className="brand-button min-h-10 px-4" onClick={() => createRef.current?.showModal()}>
              <i className="fas fa-user-plus"></i>
              {t('Добавить клиента')}
            </button>
          )}
        </div>
      </div>

      {status && <div className={STATUS_BOX}>{t(status)}</div>}
      {pageError && <div className={PAGE_ERROR_BOX}>{t(pageError)}</div>}

      <dialog id="user-create-dialog" className="user-create-dialog" ref={createRef} onClick={closeOnBackdrop}>
        <div className="user-create-dialog-shell">
          <header className="user-create-dialog-head">
            <h4 className="text-lg font-semibold text-gray-900 dark:text-white">{t('Новый клиент')}</h4>
            <button type="button" data-close-user-dialog className="grid h-9 w-9 place-items-center rounded-lg text-gray-500 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-gray-800" aria-label={t('Закрыть')} onClick={() => createRef.current?.close()}>
              <i className="fas fa-times"></i>
            </button>
          </header>
          <div className="user-create-dialog-body">
            {createErrors.first && <div className={ERROR_BOX}>{t(createErrors.first)}</div>}
            <form action="/users" method="POST" className="grid grid-cols-1 gap-4 md:grid-cols-2" onSubmit={onCreate}>
              <div>
                <label htmlFor="new-user-name" className={LABEL}>{t('Имя')}</label>
                <input id="new-user-name" {...createForm.register('name')} required maxLength={255} autoComplete="name" className={INPUT} />
              </div>
              <div>
                <label htmlFor="new-user-phone" className={LABEL}>{t('Телефон')}</label>
                <div className="flex">
                  <span className="inline-flex items-center rounded-l-lg border border-r-0 border-gray-300 bg-gray-100 px-3 text-sm font-medium text-gray-600 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-300">+7</span>
                  <input id="new-user-phone" {...createForm.register('phone')} required type="tel" inputMode="numeric" pattern="[0-9]{10}" maxLength={10} placeholder="7001234567" autoComplete="tel-national" className="w-full min-w-0 rounded-r-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-white" />
                </div>
              </div>
              <div>
                <label htmlFor="new-user-email" className={LABEL}>
                  Email <span className="font-normal text-gray-500">{t('необязательно')}</span>
                </label>
                <input id="new-user-email" {...createForm.register('email')} type="email" maxLength={255} autoComplete="email" className={INPUT} />
              </div>
              <div>
                <label htmlFor="new-user-password" className={LABEL}>{t('Пароль')}</label>
                <input id="new-user-password" {...createForm.register('password')} type="password" required minLength={6} autoComplete="new-password" className={INPUT} />
              </div>
              <div>
                <label htmlFor="new-user-password-confirmation" className={LABEL}>{t('Повторите пароль')}</label>
                <input id="new-user-password-confirmation" {...createForm.register('password_confirmation')} type="password" required minLength={6} autoComplete="new-password" className={INPUT} />
              </div>
              {/* Discount and opening balance: managers only; the front desk tops up at the cash register. */}
              {canSetMoney && (
                <>
                  <div>
                    <label htmlFor="new-user-discount" className={LABEL}>{t('Скидка, %')}</label>
                    <input id="new-user-discount" {...createForm.register('discount')} type="number" min="0" max="100" step="0.01" className={INPUT} />
                  </div>
                  <div>
                    <label htmlFor="new-user-balance" className={LABEL}>{t('Баланс')}</label>
                    <input id="new-user-balance" {...createForm.register('balance')} type="number" min="0" step="0.01" className={INPUT} />
                  </div>
                  <div>
                    <label htmlFor="new-user-bonus-balance" className={LABEL}>{t('Бонусный баланс')}</label>
                    <input id="new-user-bonus-balance" {...createForm.register('bonus_balance')} type="number" min="0" step="0.01" className={INPUT} />
                  </div>
                </>
              )}
              <div className="flex items-end justify-end gap-3 md:col-span-2">
                <button type="button" data-close-user-dialog className="min-h-10 rounded-lg border border-gray-300 px-4 text-sm font-medium text-gray-700 hover:bg-gray-50 dark:border-gray-700 dark:text-gray-300 dark:hover:bg-gray-800" onClick={() => createRef.current?.close()}>
                  {t('Отмена')}
                </button>
                <button type="submit" className="brand-button min-h-10 px-5" disabled={createForm.formState.isSubmitting}>
                  <i className="fas fa-user-plus"></i>
                  {t('Создать')}
                </button>
              </div>
            </form>
          </div>
        </div>
      </dialog>

      <dialog id="user-edit-dialog" className="user-create-dialog" ref={editRef} onClick={closeOnBackdrop}>
        <div className="user-create-dialog-shell">
          <header className="user-create-dialog-head">
            <h4 className="text-lg font-semibold text-gray-900 dark:text-white">{t('Изменить клиента')}</h4>
            <button type="button" data-close-edit-dialog className="grid h-9 w-9 place-items-center rounded-lg text-gray-500 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-gray-800" aria-label={t('Закрыть')} onClick={() => editRef.current?.close()}>
              <i className="fas fa-times"></i>
            </button>
          </header>
          <div className="user-create-dialog-body">
            {/* В старом ошибки формы изменения показывались в диалоге «Новый клиент»; здесь — в этом диалоге. */}
            {editErrors.first && <div className={ERROR_BOX}>{t(editErrors.first)}</div>}
            <form method="POST" className="grid grid-cols-1 gap-4 md:grid-cols-2" data-edit-form onSubmit={onEdit}>
              <div>
                <label htmlFor="edit-user-name" className={LABEL}>{t('Имя')}</label>
                <input id="edit-user-name" {...editForm.register('name')} required maxLength={255} readOnly={locked} className={INPUT} />
              </div>
              <div>
                <label htmlFor="edit-user-email" className={LABEL}>
                  Email <span className="font-normal text-gray-500">{t('необязательно')}</span>
                </label>
                <input id="edit-user-email" {...editForm.register('email')} type="email" maxLength={255} readOnly={locked} className={INPUT} />
              </div>
              <p className="text-sm text-gray-500 dark:text-gray-400 md:col-span-2" data-account-locked-note hidden={!locked}>
                {t('Это сотрудник другого клуба: имя, email и пароль он меняет сам. Здесь меняются только скидка и баланс.')}
              </p>
              {canSetMoney && (
                <div>
                  <label htmlFor="edit-user-discount" className={LABEL}>{t('Скидка, %')}</label>
                  <input id="edit-user-discount" {...editForm.register('discount')} type="number" min="0" max="100" step="0.01" className={INPUT} />
                </div>
              )}
              {canTopUp && (
                <>
                  {/* The owner only: a top-up or a write-off without the cash register (no cash transaction; logged). */}
                  <div className="md:col-span-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-200">
                    {t('Пополнение и списание без кассы: деньги не проходят через смену и отчёт кассы, операция записывается в журнал от вашего имени.')}
                  </div>
                  <div className="md:col-span-2">
                    <label htmlFor="edit-user-balance-action" className={LABEL}>{t('Баланс без кассы')}</label>
                    <select id="edit-user-balance-action" {...editForm.register('balance_action')} className={INPUT}>
                      <option value="top_up">{t('Пополнить')}</option>
                      <option value="write_off">{t('Списать')}</option>
                    </select>
                  </div>
                  <div>
                    <label htmlFor="edit-user-top-up" className={LABEL}>
                      {t('Сумма')} <span className="font-normal text-gray-500">{t('необязательно')}</span>
                    </label>
                    <input id="edit-user-top-up" {...editForm.register('top_up_amount')} type="number" min="0.01" max="10000000" step="0.01" className={INPUT} />
                  </div>
                  <div>
                    <label htmlFor="edit-user-top-up-balance" className={LABEL}>{t('Какой баланс')}</label>
                    <select id="edit-user-top-up-balance" {...editForm.register('top_up_balance')} className={INPUT}>
                      <option value="balance">{t('Основной баланс')}</option>
                      <option value="bonus_balance">{t('Бонусный баланс')}</option>
                    </select>
                  </div>
                </>
              )}
              <div className="flex items-end justify-end gap-3 md:col-span-2">
                <button type="button" data-close-edit-dialog className="min-h-10 rounded-lg border border-gray-300 px-4 text-sm font-medium text-gray-700 dark:border-gray-700 dark:text-gray-300" onClick={() => editRef.current?.close()}>
                  {t('Отмена')}
                </button>
                <button type="submit" className="brand-button min-h-10 px-5" disabled={editForm.formState.isSubmitting}>
                  {t('Сохранить')}
                </button>
              </div>
            </form>
          </div>
        </div>
      </dialog>

      <div className="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-gray-800 dark:bg-gray-900">
        <div className="overflow-x-auto">
          <table className="w-full min-w-[760px] text-left text-sm">
            <thead className="bg-gray-50 text-xs uppercase text-gray-500 dark:bg-gray-800 dark:text-gray-400">
              <tr>
                <th scope="col" className="px-5 py-3 font-semibold">{t('Логин')}</th>
                <th scope="col" className="px-5 py-3 font-semibold">{t('Номер')}</th>
                <th scope="col" className="px-5 py-3 text-right font-semibold">{t('Скидка')}</th>
                <th scope="col" className="px-5 py-3 text-right font-semibold">{t('Потрачено')}</th>
                <th scope="col" className="px-5 py-3 text-right font-semibold">{t('Баланс')}</th>
                <th scope="col" className="px-5 py-3 text-right font-semibold">{t('Бонусный баланс')}</th>
                <th scope="col" className="px-5 py-3">
                  <span className="sr-only">{t('Действия')}</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {items.map((c) => (
                <tr key={c.user_id} className="text-gray-700 dark:text-gray-300" data-customer-row>
                  <td className="px-5 py-4 font-medium text-gray-900 dark:text-white">{c.name}</td>
                  <td className="px-5 py-4 whitespace-nowrap">{c.phone}</td>
                  <td className="px-5 py-4 text-right tabular-nums">
                    {c.discount_effective}%
                    {c.discount_cumulative && <span className="block text-xs text-gray-500 dark:text-gray-400">{t('накопительная')}</span>}
                  </td>
                  <td className="px-5 py-4 text-right tabular-nums">{formatMoney(c.total_spent_tiyn, 2, '.', ' ')}</td>
                  <td className="px-5 py-4 text-right tabular-nums">{formatMoney(c.balance_tiyn, 2, '.', ' ')}</td>
                  <td className="px-5 py-4 text-right tabular-nums">{formatMoney(c.bonus_balance_tiyn, 2, '.', ' ')}</td>
                  <td className="whitespace-nowrap px-5 py-4 text-right">
                    {canUpdate && (
                      <button type="button" className="text-sm font-medium text-green-700 hover:underline dark:text-green-400" data-edit-user onClick={() => openEdit(c)}>
                        {t('Изменить')}
                      </button>
                    )}{' '}
                    {!(c.account_locked || !canUpdate) && (
                      <ConfirmForm className="ml-3 inline" message={t('Сбросить пароль? Новый пароль покажется на экране один раз.')} onConfirm={() => void onResetPassword(c)}>
                        <button type="submit" className="text-sm font-medium text-gray-600 hover:underline dark:text-gray-300">
                          {t('Сбросить пароль')}
                        </button>
                      </ConfirmForm>
                    )}
                  </td>
                </tr>
              ))}
              {data && items.length === 0 && (
                <tr>
                  <td colSpan={7} className="px-5 py-10 text-center text-gray-500 dark:text-gray-400">
                    {t('Пользователей пока нет')}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>

        {(pager.prevHref !== null || pager.nextHref !== null) && (
          <div className="border-t border-gray-100 px-5 py-4 dark:border-gray-800">
            <SimplePagination {...pager} />
          </div>
        )}
      </div>
    </div>
  );
}
