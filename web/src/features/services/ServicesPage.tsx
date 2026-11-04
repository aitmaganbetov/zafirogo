import { useQueryClient, useSuspenseQuery } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { ConfirmForm } from '../../components/ConfirmForm';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatMoney, parseMoney, PARSE_MONEY_MESSAGES } from '../../lib/money';
import { serverErrors } from '../auth/serverErrors';
import { createService, deleteService, servicesQuery, updateService, type ClubService, type ServiceRequest } from './api';

/*
 * Услуги — перенос resources/views/services/index.blade.php (ClubServiceController): добавить, изменить строкой
 * (форма на строку, кнопка «Сохранить» — form="service-{id}"), удалить; активные сначала, paginate(50).
 */

const FIELD = 'w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-white';

/** Поля формы услуги → тело запроса; цена — parseMoney (Q-002), иначе текст ошибки. */
function bodyOf(form: HTMLFormElement, t: (k: string, o?: Record<string, string>) => string): ServiceRequest | string {
  const fd = new FormData(form);
  const price = parseMoney(String(fd.get('price') ?? ''));
  if (!price.ok) return t(PARSE_MONEY_MESSAGES[price.error], { attribute: 'price' });
  const active = fd.getAll('is_active');
  return {
    name: String(fd.get('name') ?? ''),
    price_tiyn: price.tiyn,
    unit: String(fd.get('unit') ?? ''),
    // <input type="hidden" name="is_active" value="0"> + checkbox value="1": последнее значение (как $request->boolean()).
    ...(active.length > 0 ? { is_active: active[active.length - 1] === '1' } : {}),
  };
}

export function ServicesPage({ page }: { page: number }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { me } = useSession();
  useAdminPageTitle(t('Услуги'));
  const { data } = useSuspenseQuery(servicesQuery(page));
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [formKey, setFormKey] = useState(0);
  const readOnly = me?.read_only === true;
  const tt = (k: string, o?: Record<string, string>) => t(k, o);

  const run = async (fn: () => Promise<unknown>, message: string) => {
    try {
      await fn();
      setError(null);
      setStatus(message);
      await qc.invalidateQueries({ queryKey: ['services'] });
      return true;
    } catch (e) {
      setError(serverErrors(e).first);
      return false;
    }
  };

  const create = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const body = bodyOf(e.currentTarget, tt);
    if (typeof body === 'string') return setError(body);
    if (await run(() => createService(body), t('Услуга «{{service}}» добавлена.', { service: body.name.trim() }))) setFormKey((k) => k + 1);
  };
  const save = async (e: FormEvent<HTMLFormElement>, s: ClubService) => {
    e.preventDefault();
    const body = bodyOf(e.currentTarget, tt);
    if (typeof body === 'string') return setError(body);
    await run(() => updateService(s.id, body), t('Услуга «{{service}}» сохранена.', { service: body.name.trim() }));
  };

  return (
    <div className="p-6 lg:p-8">
      <header className="mb-6">
        <h3 className="mb-2 text-2xl font-bold text-gray-900 dark:text-white">{t('Услуги')}</h3>
        <p className="max-w-3xl text-sm text-gray-600 dark:text-gray-400">
          {t('То, что клуб продаёт кроме товаров и игрового времени: PlayStation по часам, распечатка, ламинирование… Услуги пробиваются в кассе вместе с товарами (категория «Услуги»), без склада, и попадают в чек, смену и X/Z-отчёт.')}
        </p>
      </header>

      {status && (
        <div className="mb-5 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700 dark:border-green-800 dark:bg-green-900/20 dark:text-green-300" role="status">
          {status}
        </div>
      )}
      {error && (
        <div className="mb-5 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300" role="alert">
          {t(error)}
        </div>
      )}

      {data.can_create && (
        <form
          key={formKey}
          method="POST"
          action="/services"
          className="mb-6 grid gap-3 rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-800 dark:bg-gray-900 md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_auto] md:items-end"
          onSubmit={(e) => void create(e)}
        >
          <div>
            <label htmlFor="service-name" className="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
              {t('Название')}
            </label>
            <input id="service-name" name="name" required maxLength={150} placeholder={t('Например: PS5 — 1 час')} className={FIELD} />
          </div>
          <div>
            <label htmlFor="service-price" className="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
              {t('Цена')}
            </label>
            <input id="service-price" name="price" type="number" min="0" step="0.01" required className={FIELD} />
          </div>
          <div>
            <label htmlFor="service-unit" className="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
              {t('Единица')} <span className="font-normal">{t('необязательно')}</span>
            </label>
            <input id="service-unit" name="unit" maxLength={30} placeholder={t('час, стр., шт.')} className={FIELD} />
          </div>
          <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm" disabled={readOnly}>
            <i className="fas fa-plus"></i> {t('Добавить услугу')}
          </button>
        </form>
      )}

      <div className="overflow-x-auto rounded-xl border border-gray-200 bg-white dark:border-gray-800 dark:bg-gray-900">
        <table className="w-full min-w-[640px] text-left text-sm">
          <thead className="border-b border-gray-200 text-xs uppercase text-gray-500 dark:border-gray-800 dark:text-gray-400">
            <tr>
              <th className="px-4 py-3">{t('Название')}</th>
              <th className="px-4 py-3">{t('Цена')}</th>
              <th className="px-4 py-3">{t('Единица')}</th>
              <th className="px-4 py-3">{t('Статус')}</th>
              <th className="px-4 py-3">
                <span className="sr-only">{t('Действия')}</span>
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {data.items.length === 0 ? (
              <tr>
                <td colSpan={5} className="px-4 py-10 text-center text-gray-500 dark:text-gray-400">
                  {t('Услуг пока нет. Добавьте первую: например, «PS5 — 1 час» или «Распечатка, 1 стр.».')}
                </td>
              </tr>
            ) : (
              data.items.map((s) => (
                <tr key={`${s.id}:${s.name}:${s.price_tiyn}:${s.unit ?? ''}:${s.is_active}`}>
                  {data.can_update ? (
                    <td className="px-4 py-3" colSpan={4}>
                      <form
                        method="POST"
                        action={`/services/${s.id}`}
                        id={`service-${s.id}`}
                        className="grid gap-2 md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_auto] md:items-center"
                        onSubmit={(e) => void save(e, s)}
                      >
                        <input name="name" defaultValue={s.name} required maxLength={150} aria-label={t('Название')} className={FIELD} />
                        <input name="price" type="number" min="0" step="0.01" defaultValue={formatMoney(s.price_tiyn, 2, '.', '')} required aria-label={t('Цена')} className={FIELD} />
                        <input name="unit" defaultValue={s.unit ?? ''} maxLength={30} aria-label={t('Единица')} className={FIELD} />
                        <label className="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                          <input type="hidden" name="is_active" value="0" />
                          <input type="checkbox" name="is_active" value="1" defaultChecked={s.is_active} className="brand-check rounded border-gray-300" />
                          {t('В продаже')}
                        </label>
                      </form>
                    </td>
                  ) : (
                    <>
                      <td className="px-4 py-3 font-medium text-gray-900 dark:text-white">{s.name}</td>
                      <td className="px-4 py-3">{formatMoney(s.price_tiyn, 2)}</td>
                      <td className="px-4 py-3">{s.unit ?? '—'}</td>
                      <td className="px-4 py-3">{s.is_active ? t('В продаже') : t('Выключена')}</td>
                    </>
                  )}
                  <td className="whitespace-nowrap px-4 py-3 text-right">
                    {data.can_update && (
                      <button type="submit" form={`service-${s.id}`} className="text-sm font-medium text-green-700 hover:underline dark:text-green-400" disabled={readOnly}>
                        {t('Сохранить')}
                      </button>
                    )}
                    {/* пробел между кнопкой и формой — как перенос строки в Blade */}{' '}
                    {data.can_delete && (
                      <ConfirmForm
                        className="ml-3 inline"
                        message={t('Удалить услугу «{{service}}»? Прошлые чеки не изменятся.', { service: s.name })}
                        onConfirm={() => void run(() => deleteService(s.id), t('Услуга «{{service}}» удалена.', { service: s.name }))}
                      >
                        <button type="submit" className="text-sm font-medium text-red-600 hover:underline dark:text-red-400" disabled={readOnly}>
                          {t('Удалить')}
                        </button>
                      </ConfirmForm>
                    )}
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
      <Pagination page={page} perPage={data.per_page} total={data.total} count={data.items.length} hrefFor={(p) => `/services?page=${p}`} />
    </div>
  );
}
