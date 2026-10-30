import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { PARSE_MONEY_MESSAGES, parseMoney } from '../../lib/money';
import { createPlan, firstError, plansQuery, updatePlan, type Plan, type PlanForm } from './api';
import { useSuperadminFlash } from './flash';

/* Тарифы платформы — перенос resources/views/superadmin/plans/index.blade.php и plans/fields.blade.php (PlanController). */

const INPUT = 'w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100';

/** price_monthly (decimal:2) в поле формы: «15000.00». */
function priceValue(tiyn: number | null | undefined): string {
  if (tiyn == null) return '';
  return `${Math.floor(tiyn / 100)}.${String(tiyn % 100).padStart(2, '0')}`;
}

/** plans/fields.blade.php — поля тарифа ($plan null — новый). */
function PlanFields({ plan }: { readonly plan: Plan | null }) {
  const { t } = useTranslation();
  return (
    <>
      <div className="grid grid-cols-1 gap-3 md:grid-cols-4">
        <label className="text-sm">
          <span className="mb-1 block text-gray-600">{t('Название')}</span>
          <input name="name" required maxLength={120} defaultValue={plan?.name ?? ''} className={INPUT} />
        </label>
        <label className="text-sm">
          <span className="mb-1 block text-gray-600">{t('Код (латиница)')}</span>
          <input name="slug" required maxLength={80} pattern="[A-Za-z0-9_\-]+" defaultValue={plan?.slug ?? ''} className={`${INPUT} font-mono`} />
        </label>
        <label className="text-sm">
          <span className="mb-1 block text-gray-600">{t('Цена в месяц, ₸')}</span>
          <input type="number" name="price_monthly" min="0" step="0.01" defaultValue={priceValue(plan?.price_monthly_tiyn)} className={INPUT} />
        </label>
        <label className="text-sm">
          <span className="mb-1 block text-gray-600">{t('Пробный период, дней')}</span>
          <input type="number" name="trial_days" min="0" max="365" required defaultValue={plan?.trial_days ?? 0} className={INPUT} />
        </label>
      </div>
      <label className="mt-3 block text-sm">
        <span className="mb-1 block text-gray-600">{t('Что входит (по одному пункту в строке)')}</span>
        <textarea name="features" rows={3} maxLength={2000} className={INPUT} defaultValue={(plan?.features ?? []).join('\n')}></textarea>
      </label>
      <label className="mt-3 flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
        <input type="checkbox" name="is_active" value="1" defaultChecked={plan?.is_active ?? true} /> {t('Доступен для подписки')}
      </label>
    </>
  );
}

export function PlansPage() {
  const { t } = useTranslation();
  useAdminPageTitle(t('Тарифы платформы'));
  const flash = useSuperadminFlash();
  const queryClient = useQueryClient();
  const { data } = useQuery(plansQuery());
  const onSuccess = (r: { message: string }) => {
    flash.status(r.message);
    return queryClient.invalidateQueries({ queryKey: ['superadmin'] });
  };
  const onError = (e: unknown) => flash.error(firstError(e));
  const create = useMutation({ mutationFn: createPlan, onSuccess, onError });
  const update = useMutation({ mutationFn: ({ id, body }: { id: number; body: PlanForm }) => updatePlan(id, body), onSuccess, onError });
  if (!data) return null;

  // Форма → тело: цена — строка в тиыны (Q-002: > 2 знаков — ошибка формы).
  const read = (form: HTMLFormElement): PlanForm | null => {
    const f = new FormData(form);
    const raw = String(f.get('price_monthly') ?? '').trim();
    let price: number | null = null;
    if (raw !== '') {
      const p = parseMoney(raw);
      if (!p.ok) {
        flash.error(t(PARSE_MONEY_MESSAGES[p.error], { attribute: 'price monthly' }));
        return null;
      }
      price = p.tiyn;
    }
    return {
      name: String(f.get('name') ?? ''),
      slug: String(f.get('slug') ?? ''),
      price_monthly_tiyn: price,
      trial_days: Number(f.get('trial_days') ?? 0),
      features: String(f.get('features') ?? ''),
      is_active: f.get('is_active') === '1',
    };
  };
  const submitUpdate = (id: number) => (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const body = read(e.currentTarget);
    if (body) update.mutate({ id, body });
  };
  const submitCreate = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    const body = read(form);
    if (body) create.mutate(body, { onSuccess: () => form.reset() });
  };

  return (
    <>
      <p className="mb-4 text-sm text-gray-600 dark:text-gray-400">
        {t('Тарифы, за которые клубы платят Zafiro. Тариф не удаляется: выключенный тариф нельзя выбрать для новых подписок, у клубов на нём он остаётся.')}
      </p>

      <div className="space-y-4">
        {data.items.map((plan) => (
          <form key={plan.id} method="POST" className="rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-900" onSubmit={submitUpdate(plan.id)}>
            <div className="mb-3 flex flex-wrap items-center gap-3">
              <h3 className="font-semibold text-gray-900 dark:text-white">{plan.name}</h3>
              <span className="text-sm text-gray-500">{t('{{count}} клуб|{{count}} клуба|{{count}} клубов', { count: plan.clubs_count })}</span>
              {!plan.is_active && <span className="rounded-full bg-gray-100 px-2 py-1 text-xs text-gray-700">{t('выключен')}</span>}
            </div>
            <PlanFields key={JSON.stringify(plan)} plan={plan} />
            <button type="submit" className="mt-3 rounded-lg border border-gray-300 px-4 py-2 text-sm">
              {t('Сохранить')}
            </button>
          </form>
        ))}
      </div>

      <form method="POST" className="mt-6 rounded-xl border border-dashed border-violet-300 bg-white p-4 dark:border-violet-800 dark:bg-gray-900" onSubmit={submitCreate}>
        <h3 className="mb-3 font-semibold text-gray-900 dark:text-white">{t('Новый тариф')}</h3>
        <PlanFields plan={null} />
        <button type="submit" className="brand-button mt-3 rounded-lg px-4 py-2 text-sm">
          {t('Создать тариф')}
        </button>
      </form>
    </>
  );
}
