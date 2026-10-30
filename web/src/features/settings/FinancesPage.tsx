import { useQuery } from '@tanstack/react-query';
import { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { SettingsCard } from '../../components/SettingsCard';
import { SettingsLayout } from '../../components/SettingsLayout';
import { clubSettingsQuery, firstError, hundredthsToInput, minutesToInput, type PercentLevel } from './api';
import { SettingsAnchors } from './SettingsAnchors';
import { SettingsForm } from './SettingsForm';
import { useSettingsResult } from './status';

/*
 * Настройки → Финансы — перенос resources/views/settings/finances.blade.php (SettingsController::finances()) и
 * таблиц уровней settings/percent-levels.blade.php. Уровни приходят в тиынах и базисных пунктах (решение Q-003),
 * в полях — числа в тенге и процентах, как в старом шаблоне.
 */

const INPUT = 'brand-focus w-full rounded-lg border border-gray-300 bg-gray-50 px-4 py-2 text-gray-900 focus:outline-none dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100';
const REMOVE = 'h-10 w-10 rounded-lg text-gray-400 transition hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20';

interface Row {
  readonly key: number;
  readonly threshold: string;
  readonly percent: string;
}

/** Таблица уровней «от порога — процент» (percent-levels.blade.php). */
function PercentLevels({ field, threshold, thresholdLabel, percentLabel, levels, error }: {
  readonly field: string;
  readonly threshold: 'spent' | 'amount';
  readonly thresholdLabel: string;
  readonly percentLabel: string;
  readonly levels: readonly PercentLevel[];
  readonly error?: string;
}) {
  const { t } = useTranslation();
  const next = useRef(levels.length);
  const list = useRef<HTMLDivElement>(null);
  const [rows, setRows] = useState<Row[]>(() =>
    levels.map((l, i) => ({ key: i, threshold: hundredthsToInput(l.threshold_tiyn), percent: hundredthsToInput(l.percent_bp) })));
  const add = () => {
    const key = next.current++;
    setRows((r) => [...r, { key, threshold: '', percent: '' }]);
    requestAnimationFrame(() => list.current?.querySelector<HTMLInputElement>(`[data-level="${key}"] input`)?.focus());
  };
  const remove = (key: number) => setRows((r) => r.filter((x) => x.key !== key));

  return (
    <>
      {error && <div className="mb-3 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700" role="alert">{error}</div>}

      <div className="max-w-2xl" data-percent-levels={field}>
        <div className="mb-2 hidden grid-cols-[1fr_1fr_2.5rem] gap-3 text-xs font-semibold uppercase text-gray-500 sm:grid dark:text-gray-400">
          <span>{thresholdLabel}</span>
          <span>{percentLabel}</span>
        </div>
        {/* Without levels the field still comes, so removing the last level is saved. */}
        <input type="hidden" name={field} value="" />
        <div className="space-y-3" data-levels-list ref={list}>
          {rows.map((row) => (
            <div key={row.key} className="grid grid-cols-[1fr_1fr_2.5rem] items-center gap-3" data-level={row.key}>
              <input type="number" name={`${field}[${row.key}][${threshold}]`} defaultValue={row.threshold} min="0.01" step="0.01" required aria-label={thresholdLabel} className={INPUT} />
              <input type="number" name={`${field}[${row.key}][percent]`} defaultValue={row.percent} min="0.01" max="100" step="0.01" required aria-label={percentLabel} className={INPUT} />
              <button type="button" data-remove-level className={REMOVE} title={t('Удалить уровень')} aria-label={t('Удалить уровень')} onClick={() => remove(row.key)}><i className="fas fa-trash"></i></button>
            </div>
          ))}
        </div>
        <p className={`text-sm text-gray-500 dark:text-gray-400 ${rows.length === 0 ? '' : 'hidden'}`} data-levels-empty>{t('Уровней пока нет — добавьте первый.')}</p>
        <button type="button" data-add-level className="mt-3 inline-flex items-center gap-2 rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-700 transition hover:bg-gray-100 dark:border-gray-700 dark:text-gray-300 dark:hover:bg-gray-800" onClick={add}>
          <i className="fas fa-plus"></i>{t('Добавить уровень')}
        </button>
      </div>
    </>
  );
}

/** $errors->first($field) ?: $errors->first($field.'.*'). */
function levelError(e: unknown, field: string): string | undefined {
  const direct = firstError(e, field);
  if (direct) return direct;
  if (e && typeof e === 'object' && 'fieldErrors' in e) {
    const errs = (e as { fieldErrors: Record<string, readonly string[]> }).fieldErrors;
    const key = Object.keys(errs).find((k) => k.startsWith(`${field}.`));
    return key ? errs[key]?.[0] : undefined;
  }
  return undefined;
}

export function FinancesPage() {
  const { t } = useTranslation();
  const { me } = useSession();
  const { data } = useQuery(clubSettingsQuery());
  const { status, done, fail, errorOf } = useSettingsResult();
  if (!data) return null;
  const f = data.settings.finances;
  const currency = f.currency || 'KZT';
  const err = errorOf('finances');
  const fieldErr = (name: string) => {
    const m = firstError(err, name);
    return m ? <p className="mt-1 text-sm text-red-600">{m}</p> : null;
  };
  const anchors: ReadonlyArray<readonly [string, string]> = [
    ['currency-settings', t('Валюта')],
    ['payment-rules', t('Оплата времени')],
    ['loyalty', t('Накопительная скидка')],
    ['topup-bonus', t('Бонусы за пополнение')],
  ];
  const toggle = (name: keyof typeof f, label: string, checked: boolean, cls = 'flex items-center gap-3') => (
    <div className={cls}>
      <label htmlFor={name} className="text-sm font-medium text-gray-700 dark:text-gray-300">{label}</label>
      <input type="hidden" name={name} value="0" />
      <input id={name} type="checkbox" role="switch" name={name} value="1" className="settings-switch" defaultChecked={checked} />
    </div>
  );

  return (
    <SettingsLayout status={status} anchors={<SettingsAnchors anchors={anchors} />}>
      <SettingsForm key={JSON.stringify(f)} section="finances" readOnly={me?.read_only === true} onDone={done} onError={fail('finances')}>
        <h4 className="text-xl font-semibold text-gray-900 dark:text-white">{t('Финансы')}</h4>
        <SettingsCard id="currency-settings" className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <div>
            <label htmlFor="currency" className="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Валюта')}</label>
            <select id="currency" name="currency" className={INPUT} defaultValue={currency}>
              <option value="KZT">KZT</option>
              <option value="RUB">RUB</option>
              <option value="$">$</option>
            </select>
          </div>
        </SettingsCard>

        <SettingsCard id="payment-rules">
          <h5 className="mb-1 text-lg font-semibold text-gray-900 dark:text-white">{t('Оплата времени')}</h5>
          <p className="mb-4 max-w-2xl text-sm text-gray-500 dark:text-gray-400">{t('Как клиенты платят за время в агенте: где действует их скидка (персональная или накопительная), какую часть цены можно оплатить бонусами и можно ли платить сразу с двух балансов. Клиент сам выбирает при покупке: с основного баланса или бонусами.')}</p>
          <div className="grid max-w-2xl grid-cols-1 gap-4 md:grid-cols-2">
            <div>
              <label htmlFor="discount_scope" className="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Где действует скидка клиента')}</label>
              <select id="discount_scope" name="discount_scope" className={INPUT} defaultValue={f.discount_scope}>
                <option value="tariffs">{t('На тарифы с отметкой «Применять скидку»')}</option>
                <option value="hourly">{t('Только на почасовую оплату (поминутно по цене зоны)')}</option>
                <option value="all">{t('Везде: на тарифы с отметкой и на почасовую оплату')}</option>
              </select>
              {fieldErr('discount_scope')}
            </div>
            <div>
              <label htmlFor="bonus_max_percent" className="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Сколько процентов цены можно оплатить бонусами')}</label>
              <input id="bonus_max_percent" name="bonus_max_percent" type="number" min="0" max="100" step="1" required defaultValue={minutesToInput(f.bonus_max_percent)} className={INPUT} />
              <p className="mt-1 text-xs text-gray-500">{t('100 — бонусами можно оплатить всю цену, 0 — бонусами за время платить нельзя.')}</p>
              {fieldErr('bonus_max_percent')}
            </div>
          </div>
          <div className="mt-4 max-w-2xl rounded-xl border border-gray-200 p-4 dark:border-gray-700">
            {toggle('allow_mixed_payment', t('Разрешить смешанную оплату'), f.allow_mixed_payment)}
            <p className="mt-2 text-xs text-gray-500">{t('Включено — клиент платит за тариф сразу с двух балансов: бонусами (до процента выше), остальное — с основного. Выключено — целиком с одного баланса: основного или бонусного (бонусами — только если можно оплатить 100% цены).')}</p>
          </div>
          <div className="mt-4 max-w-2xl rounded-xl border border-gray-200 p-4 dark:border-gray-700">
            {toggle('allow_whole_balance', t('Разрешить игру на весь баланс'), f.allow_whole_balance)}
            <p className="mt-2 text-xs text-gray-500">{t('Клиент без тарифа нажимает в агенте «На весь баланс» и играет, пока хватает денег на основном балансе, по цене часа своей зоны: 2000 при цене 1000 за час — 2 часа (не больше 12 часов за раз). Ушёл раньше — неиспользованные минуты вернутся на баланс.')}</p>
          </div>
          <div className="mt-4 max-w-2xl rounded-xl border border-gray-200 p-4 dark:border-gray-700">
            {toggle('auto_billing', t('Поминутная оплата сразу после входа'), f.auto_billing)}
            <p className="mt-2 text-xs text-gray-500">{t('Клиент вошёл на ПК и не купил тариф — после паузы начинается поминутная оплата по цене часа зоны с основного баланса. Закончился тариф — поминутная оплата продолжается сразу. Деньги списываются только за сыгранные минуты: при завершении сессии остаток возвращается.')}</p>
            <label htmlFor="auto_billing_pause_minutes" className="mt-3 mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Пауза после входа до начала поминутной оплаты (в минутах)')}</label>
            <input id="auto_billing_pause_minutes" name="auto_billing_pause_minutes" type="number" min="0" max="60" step="1" defaultValue={minutesToInput(f.auto_billing_pause_minutes)} className={`${INPUT} max-w-[160px]`} />
            <p className="mt-1 text-xs text-gray-500">{t('Столько минут после входа клиент может бесплатно выбрать тариф. 0 — оплата начинается сразу.')}</p>
            {fieldErr('auto_billing_pause_minutes')}
          </div>
        </SettingsCard>

        <SettingsCard id="loyalty">
          <h5 className="mb-1 text-lg font-semibold text-gray-900 dark:text-white">{t('Накопительная скидка')}</h5>
          <p className="mb-4 max-w-2xl text-sm text-gray-500 dark:text-gray-400">{t('Клиент получает скидку, когда его траты в клубе достигают порога. Считаются тарифы, поминутное время и заказы в агенте, оплаченные с основного баланса. Где действует скидка — в блоке «Оплата времени»; если у клиента есть персональная скидка, применяется большая.')}</p>
          {toggle('loyalty_enabled', t('Включить накопительную скидку'), f.loyalty_enabled, 'mb-4 inline-flex items-center gap-3 rounded-xl border border-gray-200 p-4 dark:border-gray-700')}
          <PercentLevels field="loyalty_levels" threshold="spent" thresholdLabel={t('Потратил от, {{currency}}', { currency })} percentLabel={t('Скидка, %')}
            levels={f.loyalty_levels} error={levelError(err, 'loyalty_levels')} />
        </SettingsCard>

        <SettingsCard id="topup-bonus">
          <h5 className="mb-1 text-lg font-semibold text-gray-900 dark:text-white">{t('Бонусы за пополнение')}</h5>
          <p className="mb-4 max-w-2xl text-sm text-gray-500 dark:text-gray-400">{t('Когда клиенту пополняют основной баланс на кассе, на бонусный баланс добавляется процент от суммы: по уровню, которого достигла сумма пополнения. Выключено — бонусы не начисляются.')}</p>
          {toggle('topup_bonus_enabled', t('Включить бонусы за пополнение'), f.topup_bonus_enabled, 'mb-4 inline-flex items-center gap-3 rounded-xl border border-gray-200 p-4 dark:border-gray-700')}
          <PercentLevels field="topup_bonus_levels" threshold="amount" thresholdLabel={t('Пополнение от, {{currency}}', { currency })} percentLabel={t('Бонус, %')}
            levels={f.topup_bonus_levels} error={levelError(err, 'topup_bonus_levels')} />
        </SettingsCard>
      </SettingsForm>
    </SettingsLayout>
  );
}
