import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { can, useSession } from '../../auth/session';
import { ConfirmForm } from '../../components/ConfirmForm';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { setFlash, useFlash } from '../../lib/flash';
import { formatMoney, parseMoney, PARSE_MONEY_MESSAGES } from '../../lib/money';
import {
  ALWAYS,
  createTariff,
  deleteTariff,
  firstError,
  perMinuteTiyn,
  priceInput,
  splitMinutes,
  tariffsPageQuery,
  updateTariff,
  type PageTariff,
  type TariffInput,
  type WeeklyPeriod,
} from './api';
import { TariffPeriods } from './TariffPeriods';

/*
 * Зоны и тарифы — перенос resources/views/tariffs/index.blade.php (+ tariffs/periods.blade.php),
 * TariffController::page(). /tariffs/{id}/edit — тот же экран с открытой формой; закрытие формы — к списку.
 */

const FIELD = 'w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-white';
const SELECT = 'rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-white';
const SMALL_INPUT = 'w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm dark:border-gray-700 dark:bg-gray-800';
const SMALL_SELECT = 'rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm dark:border-gray-700 dark:bg-gray-800';
const LABEL = 'mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300';

interface FormValues {
  payment_method: string;
  name: string;
  duration_value: string;
  duration_unit: string;
  price: string;
  discount_enabled: boolean;
  zone_ids: string[];
  usage_limit_enabled: boolean;
  usage_limit_value: string;
  usage_limit_unit: string;
  quantity_limit_enabled: boolean;
  quantity_limit: string;
  sale_periods: WeeklyPeriod[];
  validity_periods: WeeklyPeriod[];
  is_active: boolean;
}

const DEFAULTS: FormValues = {
  payment_method: 'main_balance_only',
  name: '',
  duration_value: '1',
  duration_unit: 'hours',
  price: '',
  discount_enabled: false,
  zone_ids: [],
  usage_limit_enabled: false,
  usage_limit_value: '',
  usage_limit_unit: 'hours',
  quantity_limit_enabled: false,
  quantity_limit: '',
  sale_periods: ALWAYS,
  validity_periods: ALWAYS,
  is_active: true,
};

/** TariffController::formValues(): поля формы изменяемого тарифа. */
function formValues(t: PageTariff): FormValues {
  const [duration, durationUnit] = splitMinutes(t.duration_minutes);
  const [usage, usageUnit] = splitMinutes(t.usage_limit_minutes ?? 0);
  return {
    payment_method: t.payment_method,
    name: t.name,
    duration_value: String(duration),
    duration_unit: durationUnit,
    price: priceInput(t.price_tiyn),
    discount_enabled: t.discount_enabled,
    zone_ids: t.zone_ids.map(String),
    usage_limit_enabled: t.usage_limit_enabled,
    usage_limit_value: t.usage_limit_enabled ? String(usage) : '',
    usage_limit_unit: usageUnit,
    quantity_limit_enabled: t.quantity_limit_enabled,
    quantity_limit: t.quantity_limit == null ? '' : String(t.quantity_limit),
    sale_periods: t.sale_periods.length > 0 ? t.sale_periods : ALWAYS,
    validity_periods: t.validity_periods.length > 0 ? t.validity_periods : ALWAYS,
    is_active: t.is_active,
  };
}

const int = (s: string): number | null => (/^-?\d+$/.test(s.trim()) ? Number(s.trim()) : null);

export function TariffsPage({ zone, editId }: { zone?: number; editId?: number }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  useAdminPageTitle(t('Зоны и тарифы'));
  const flash = useFlash('status');
  const { data } = useQuery(tariffsPageQuery(zone));
  const readOnly = me?.read_only === true;
  const editing = editId !== undefined ? (data?.tariffs.find((x) => x.id === editId) ?? null) : null;

  const dialogRef = useRef<HTMLDialogElement>(null);
  const [values, setValues] = useState<FormValues>(() => (editing ? formValues(editing) : DEFAULTS));
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(flash);
  const set = <K extends keyof FormValues>(k: K, v: FormValues[K]) => setValues((old) => ({ ...old, [k]: v }));

  // Изменение открывает ту же форму заполненной (data-open-on-load).
  useEffect(() => {
    if (editing && !dialogRef.current?.open) dialogRef.current?.showModal();
  }, [editing]);

  const close = () => dialogRef.current?.close();
  // data-close-href: закрытие формы изменения — к списку.
  const onClose = () => {
    setError(null);
    if (editId !== undefined) void navigate({ to: '/tariffs' });
  };

  const save = useMutation({
    mutationFn: (body: TariffInput) => (editing ? updateTariff(editing.id, body) : createTariff(body)),
    onSuccess: (r) => {
      setFlash('status', r.message);
      void queryClient.invalidateQueries({ queryKey: ['tariffs'] });
      setValues(DEFAULTS);
      setStatus(r.message);
      // redirect()->route('tariffs.index'): к списку без фильтра зоны.
      if (editId === undefined) close();
      void navigate({ to: '/tariffs' });
    },
    onError: (e) => setError(firstError(e)),
  });
  const remove = useMutation({
    mutationFn: deleteTariff,
    onSuccess: (r) => {
      setStatus(r.message);
      return queryClient.invalidateQueries({ queryKey: ['tariffs'] });
    },
    onError: (e) => setStatus(firstError(e)),
  });

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const price = parseMoney(values.price);
    if (!price.ok) {
      setError(t(PARSE_MONEY_MESSAGES[price.error], { attribute: 'price' }));
      return;
    }
    save.mutate({
      payment_method: values.payment_method,
      name: values.name.trim(),
      duration_value: int(values.duration_value) ?? 0,
      duration_unit: values.duration_unit,
      price_tiyn: price.tiyn,
      discount_enabled: values.discount_enabled,
      zone_ids: values.zone_ids.map(Number),
      usage_limit_enabled: values.usage_limit_enabled,
      usage_limit_value: int(values.usage_limit_value),
      usage_limit_unit: values.usage_limit_unit,
      quantity_limit_enabled: values.quantity_limit_enabled,
      quantity_limit: int(values.quantity_limit),
      sale_periods: values.sale_periods,
      validity_periods: values.validity_periods,
      is_active: values.is_active,
    });
  };

  if (!data) return null;
  const currency = data.currency;

  return (
    <div className="p-6 lg:p-8">
      <header className="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <h3 className="mb-2 text-2xl font-bold text-gray-900 dark:text-white">{t('Зоны и тарифы')}</h3>
          <p className="text-sm text-gray-600 dark:text-gray-400">{t('Настройте стоимость и длительность игровых сеансов.')}</p>
        </div>
        {can(me, 'tariffs.create') && (
          <button
            id="open-tariff-dialog"
            type="button"
            className="tariff-add-button"
            onClick={() => {
              setValues(DEFAULTS);
              setError(null);
              dialogRef.current?.showModal();
            }}
          >
            <i className="fas fa-plus"></i>
            {t('Добавить тариф')}
          </button>
        )}
      </header>

      {status && <div className="mb-5 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700 dark:border-green-800 dark:bg-green-900/20 dark:text-green-300">{status}</div>}

      <dialog
        id="tariff-dialog"
        className="tariff-dialog"
        ref={dialogRef}
        onClose={onClose}
        onClick={(e) => {
          if (e.target === dialogRef.current) close();
        }}
      >
        <div className="tariff-dialog-shell">
          <header className="tariff-dialog-head">
            <h4 className="text-lg font-semibold text-gray-900 dark:text-white">{editing ? t('Изменить тариф') : t('Новый тариф')}</h4>
            <button
              type="button"
              data-close-tariff-dialog
              className="grid h-9 w-9 place-items-center rounded-lg text-gray-500 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-gray-800"
              aria-label={t('Закрыть')}
              onClick={close}
            >
              <i className="fas fa-times"></i>
            </button>
          </header>
          <div className="tariff-dialog-body">
            {error && <div className="mb-5 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">{error}</div>}
            <form method="POST" className="space-y-7" onSubmit={submit}>
              <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
                <div>
                  <label htmlFor="payment-method" className={LABEL}>
                    {t('Как клиент может оплатить этот абонемент?')}
                  </label>
                  <select id="payment-method" name="payment_method" required className={FIELD} value={values.payment_method} onChange={(e) => set('payment_method', e.target.value)}>
                    <option value="main_balance_only">{t('Можно приобрести только за основной баланс')}</option>
                    <option value="main_or_bonus">{t('Можно приобрести за основной или бонусный баланс')}</option>
                  </select>
                </div>
                <div>
                  <label htmlFor="tariff-name" className={LABEL}>
                    {t('Название')}
                  </label>
                  <input id="tariff-name" name="name" value={values.name} onChange={(e) => set('name', e.target.value)} required maxLength={100} className={FIELD} />
                </div>
                <div>
                  <label htmlFor="tariff-duration" className={LABEL}>
                    {t('Длительность')}
                  </label>
                  <div className="tariff-duration-fields">
                    <input id="tariff-duration" name="duration_value" type="number" value={values.duration_value} onChange={(e) => set('duration_value', e.target.value)} required min={1} max={10080} className={FIELD} />
                    <select name="duration_unit" className={SELECT} value={values.duration_unit} onChange={(e) => set('duration_unit', e.target.value)}>
                      <option value="hours">{t('Часов')}</option>
                      <option value="minutes">{t('Минут')}</option>
                      <option value="days">{t('Дней')}</option>
                    </select>
                  </div>
                </div>
                <div>
                  <label htmlFor="tariff-price" className={LABEL}>
                    {t('Стоимость')}
                  </label>
                  <input id="tariff-price" name="price" type="number" value={values.price} onChange={(e) => set('price', e.target.value)} required min={0} max="9999999999.99" step="0.01" className={FIELD} />
                </div>
                <div className="flex items-center gap-3 pt-6">
                  <input id="discount-enabled" type="checkbox" name="discount_enabled" value="1" checked={values.discount_enabled} onChange={(e) => set('discount_enabled', e.target.checked)} className="brand-check rounded border-gray-300" />
                  <label htmlFor="discount-enabled" className="text-sm font-medium text-gray-700 dark:text-gray-300">
                    {t('Применять скидку')}
                  </label>
                </div>
              </div>

              <div className="grid grid-cols-1 gap-4 border-t border-gray-200 pt-6 dark:border-gray-800 md:grid-cols-2">
                <div>
                  <label htmlFor="tariff-zones" className={LABEL}>
                    {t('Зоны')}
                  </label>
                  <select
                    id="tariff-zones"
                    name="zone_ids[]"
                    multiple
                    size={3}
                    className={FIELD}
                    value={values.zone_ids}
                    onChange={(e) =>
                      set(
                        'zone_ids',
                        [...e.target.selectedOptions].map((o) => o.value),
                      )
                    }
                  >
                    {data.zones.map((z) => (
                      <option key={z.id} value={z.id}>
                        {z.name}
                      </option>
                    ))}
                  </select>
                  {data.zones.length === 0 && <p className="mt-1 text-xs text-gray-500">{t('Сначала создайте зону.')}</p>}
                </div>
                <div className="space-y-4">
                  <div>
                    <label className="flex items-center gap-2 text-sm font-medium text-gray-700 dark:text-gray-300">
                      <input type="checkbox" name="usage_limit_enabled" value="1" checked={values.usage_limit_enabled} onChange={(e) => set('usage_limit_enabled', e.target.checked)} className="brand-check rounded border-gray-300" />
                      {t('Ограничить использование')}
                    </label>
                    <p className="mb-2 text-xs text-gray-500">{t('Абонемент сгорит через указанное время после активации.')}</p>
                    <div className="tariff-duration-fields">
                      <input name="usage_limit_value" type="number" min={1} max={10080} value={values.usage_limit_value} onChange={(e) => set('usage_limit_value', e.target.value)} className={SMALL_INPUT} />
                      <select name="usage_limit_unit" className={SMALL_SELECT} value={values.usage_limit_unit} onChange={(e) => set('usage_limit_unit', e.target.value)}>
                        <option value="hours">{t('Часов')}</option>
                        <option value="minutes">{t('Минут')}</option>
                        <option value="days">{t('Дней')}</option>
                      </select>
                    </div>
                  </div>
                  <div>
                    <label className="flex items-center gap-2 text-sm font-medium text-gray-700 dark:text-gray-300">
                      <input type="checkbox" name="quantity_limit_enabled" value="1" checked={values.quantity_limit_enabled} onChange={(e) => set('quantity_limit_enabled', e.target.checked)} className="brand-check rounded border-gray-300" />
                      {t('Ограничить количество')}
                    </label>
                    <p className="mb-2 text-xs text-gray-500">{t('Сколько абонементов будет доступно к продаже.')}</p>
                    <input name="quantity_limit" type="number" min={1} max={1000000} value={values.quantity_limit} onChange={(e) => set('quantity_limit', e.target.value)} className={SMALL_INPUT} />
                  </div>
                </div>
              </div>

              <section className="border-t border-gray-200 pt-6 dark:border-gray-800">
                <div className="mb-3">
                  <h5 className="text-sm font-semibold text-gray-900 dark:text-white">{t('Время продажи')}</h5>
                  <p className="text-xs text-gray-500">{t('Дни и время, когда тариф продаётся в агенте и на кассе. В другое время он не показывается.')}</p>
                </div>
                <TariffPeriods field="sale_periods" periods={values.sale_periods} onChange={(p) => set('sale_periods', p)} />
              </section>

              <section className="border-t border-gray-200 pt-6 dark:border-gray-800">
                <div className="mb-3">
                  <h5 className="text-sm font-semibold text-gray-900 dark:text-white">{t('Период действия')}</h5>
                  <p className="text-xs text-gray-500">{t('Дни и время, когда абонемент можно использовать.')}</p>
                </div>
                <TariffPeriods field="validity_periods" periods={values.validity_periods} onChange={(p) => set('validity_periods', p)} />
              </section>

              <div className="flex flex-wrap items-center justify-between gap-4 border-t border-gray-200 pt-5 dark:border-gray-800">
                <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                  <input type="checkbox" name="is_active" value="1" checked={values.is_active} onChange={(e) => set('is_active', e.target.checked)} className="brand-check rounded border-gray-300" />
                  {t('Активен')}
                </label>
                <button type="submit" className="brand-button min-h-10 px-5" disabled={readOnly || save.isPending}>
                  {editing ? (
                    <>
                      <i className="fas fa-check"></i>
                      {t('Сохранить тариф')}
                    </>
                  ) : (
                    <>
                      <i className="fas fa-plus"></i>
                      {t('Создать тариф')}
                    </>
                  )}
                </button>
              </div>
            </form>
          </div>
        </div>
      </dialog>

      <section className="mb-8">
        <div className="mb-3 flex flex-wrap items-baseline justify-between gap-3">
          <h4 className="text-lg font-semibold text-gray-900 dark:text-white">{t('Поминутная оплата по зонам')}</h4>
          <a href="/club-map" className="text-sm font-medium text-green-700 hover:underline dark:text-green-400">
            {t('Изменить цену часа в зонах')}
          </a>
        </div>
        <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('В агенте клиент покупает любое число минут по цене часа своей зоны. Без цены часа в зоне продаются только тарифы.')}</p>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[480px] text-left text-sm">
            <thead className="border-b border-gray-200 text-xs uppercase text-gray-500 dark:border-gray-800 dark:text-gray-400">
              <tr>
                <th className="px-3 py-3 font-semibold">{t('Зона')}</th>
                <th className="px-3 py-3 font-semibold">{t('Цена за час')}</th>
                <th className="px-3 py-3 font-semibold">{t('Цена за минуту')}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {data.zones.length === 0 && (
                <tr>
                  <td colSpan={3} className="px-3 py-8 text-center text-sm text-gray-500 dark:text-gray-400">
                    {t('Добавьте ПК в зону на карте клуба.')}
                  </td>
                </tr>
              )}
              {data.zones.map((z) => (
                <tr key={z.id}>
                  <td className="px-3 py-3 font-medium text-gray-900 dark:text-white">{z.name}</td>
                  {z.price_per_hour_tiyn != null ? (
                    <>
                      <td className="whitespace-nowrap px-3 py-3 font-semibold text-gray-900 dark:text-white">
                        {formatMoney(z.price_per_hour_tiyn, 2)} {currency}
                      </td>
                      <td className="whitespace-nowrap px-3 py-3 text-gray-700 dark:text-gray-300">
                        {formatMoney(perMinuteTiyn(z.price_per_hour_tiyn), 2)} {currency}
                      </td>
                    </>
                  ) : (
                    <td colSpan={2} className="px-3 py-3 text-gray-500 dark:text-gray-400">
                      {t('Не продаётся поминутно')}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section>
        <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
          <h4 className="text-lg font-semibold text-gray-900 dark:text-white">
            {t('Список тарифов')} <span className="text-xs font-normal text-gray-500 dark:text-gray-400">{data.tariffs.length}</span>
          </h4>
          <form method="GET" action="/tariffs" className="flex items-center gap-2" onSubmit={(e) => e.preventDefault()}>
            <label htmlFor="tariff-zone-filter" className="text-sm text-gray-600 dark:text-gray-400">
              {t('Зона')}
            </label>
            <select
              id="tariff-zone-filter"
              name="zone"
              className={SELECT}
              value={data.zone_filter ?? ''}
              onChange={(e) => void navigate({ to: '/tariffs', search: e.target.value ? { zone: Number(e.target.value) } : {} })}
            >
              <option value="">{t('Все зоны')}</option>
              {data.zones.map((z) => (
                <option key={z.id} value={z.id}>
                  {z.name}
                </option>
              ))}
            </select>
          </form>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full min-w-[600px] text-left text-sm">
            <thead className="border-b border-gray-200 text-xs uppercase text-gray-500 dark:border-gray-800 dark:text-gray-400">
              <tr>
                <th className="px-3 py-3 font-semibold">{t('Название')}</th>
                <th className="px-3 py-3 font-semibold">{t('Зоны')}</th>
                <th className="px-3 py-3 font-semibold">{t('Длительность')}</th>
                <th className="px-3 py-3 font-semibold">{t('Цена')}</th>
                <th className="px-3 py-3 font-semibold">{t('Статус')}</th>
                <th className="px-3 py-3">
                  <span className="sr-only">{t('Действия')}</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {data.tariffs.length === 0 && (
                <tr>
                  <td colSpan={6} className="px-3 py-10 text-center text-sm text-gray-500 dark:text-gray-400">
                    {data.zone_filter != null ? t('В этой зоне тарифов нет.') : t('Тарифов пока нет.')}
                  </td>
                </tr>
              )}
              {data.tariffs.map((tariff) => {
                const exhausted = tariff.quantity_limit != null && tariff.sold >= tariff.quantity_limit;
                return (
                  <tr key={tariff.id}>
                    <td className="px-3 py-3 font-medium text-gray-900 dark:text-white">
                      {tariff.name}
                      {tariff.discount_enabled && <span className="mt-1 block text-xs font-normal text-gray-500 dark:text-gray-400">{t('Со скидкой')}</span>}{' '}
                      {tariff.quantity_limit_enabled && tariff.quantity_limit != null && (
                        <span className={`mt-1 block text-xs font-normal ${exhausted ? 'text-red-600 dark:text-red-400' : 'text-gray-500 dark:text-gray-400'}`}>
                          {exhausted
                            ? t('Продано {{sold}} из {{limit}} — лимит количества исчерпан, тариф не продаётся', { sold: tariff.sold, limit: tariff.quantity_limit })
                            : t('Продано {{sold}} из {{limit}}', { sold: tariff.sold, limit: tariff.quantity_limit })}
                        </span>
                      )}
                      {!tariff.on_sale && <span className="mt-1 block text-xs font-normal text-amber-600 dark:text-amber-400">{t('Сейчас не продаётся: вне времени продажи')}</span>}
                      {!tariff.in_validity_period && <span className="mt-1 block text-xs font-normal text-amber-600 dark:text-amber-400">{t('Сейчас не продаётся: вне периода действия')}</span>}
                    </td>
                    <td className="px-3 py-3 text-gray-700 dark:text-gray-300">{tariff.zone_names?.length ? tariff.zone_names.join(', ') : t('Все зоны')}</td>
                    <td className="whitespace-nowrap px-3 py-3 text-gray-700 dark:text-gray-300">{t('{{minutes}} мин.', { minutes: tariff.duration_minutes })}</td>
                    <td className="whitespace-nowrap px-3 py-3 font-semibold text-gray-900 dark:text-white">
                      {formatMoney(tariff.price_tiyn, 2)} {tariff.currency}
                    </td>
                    <td className="px-3 py-3">
                      <span
                        className={`rounded-full px-2 py-1 text-xs font-medium ${tariff.is_active ? 'bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-300' : 'bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400'}`}
                      >
                        {tariff.is_active ? t('Активен') : t('Выключен')}
                      </span>
                    </td>
                    <td className="whitespace-nowrap px-3 py-3 text-right">
                      {can(me, 'tariffs.update') && (
                        <a href={`/tariffs/${tariff.id}/edit`} className="text-sm font-medium text-green-700 hover:underline dark:text-green-400">
                          {t('Изменить')}
                        </a>
                      )}{' '}
                      {data.can_delete && (
                        <ConfirmForm
                          className="ml-3 inline"
                          message={t('Удалить тариф «{{tariff}}»? Он пропадёт из агента и кассы. Купленные абонементы и история покупок сохранятся.', { tariff: tariff.name })}
                          onConfirm={() => remove.mutate(tariff.id)}
                        >
                          <button type="submit" className="text-sm font-medium text-red-600 hover:underline dark:text-red-400" disabled={readOnly}>
                            {t('Удалить')}
                          </button>
                        </ConfirmForm>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
