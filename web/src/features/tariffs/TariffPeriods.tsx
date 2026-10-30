import { useTranslation } from 'react-i18next';
import { nextPeriod, type WeeklyPeriod } from './api';

/*
 * Дни и время недели (время продажи или период действия тарифа) — перенос resources/views/tariffs/periods.blade.php
 * и его скрипта: «+» добавляет строку (последняя на день позже), корзина удаляет строку, если она не последняя.
 */

const FIELD = 'mt-1 w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800';
const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'] as const;
const DAY_NAMES: Record<(typeof DAYS)[number], string> = { mon: 'Пн', tue: 'Вт', wed: 'Ср', thu: 'Чт', fri: 'Пт', sat: 'Сб', sun: 'Вс' };

export function TariffPeriods({ field, periods, onChange }: { field: string; periods: WeeklyPeriod[]; onChange: (p: WeeklyPeriod[]) => void }) {
  const { t } = useTranslation();
  const update = (i: number, part: keyof WeeklyPeriod, v: string) => onChange(periods.map((p, j) => (j === i ? { ...p, [part]: v } : p)));
  const add = () => onChange([...periods, nextPeriod(periods.at(-1))]);
  const remove = (i: number) => {
    if (periods.length > 1) onChange(periods.filter((_, j) => j !== i));
  };
  const days = DAYS.map((d) => (
    <option key={d} value={d}>
      {t(DAY_NAMES[d])}
    </option>
  ));
  return (
    <div className="tariff-period-list space-y-3" data-period-list>
      {periods.map((p, i) => (
        <div key={i} className="tariff-validity-period" data-period>
          <label className="text-xs text-gray-500">
            {t('С')}
            <select name={`${field}[${i}][start_day]`} required className={FIELD} value={p.start_day} onChange={(e) => update(i, 'start_day', e.target.value)}>
              {days}
            </select>
          </label>
          <label className="text-xs text-gray-500">
            {t('Время')}
            <input name={`${field}[${i}][start_time]`} type="time" value={p.start_time} required className={FIELD} onChange={(e) => update(i, 'start_time', e.target.value)} />
          </label>
          <label className="text-xs text-gray-500">
            {t('До')}
            <select name={`${field}[${i}][end_day]`} required className={FIELD} value={p.end_day} onChange={(e) => update(i, 'end_day', e.target.value)}>
              {days}
            </select>
          </label>
          <label className="text-xs text-gray-500">
            {t('Время')}
            <input name={`${field}[${i}][end_time]`} type="time" value={p.end_time} required className={FIELD} onChange={(e) => update(i, 'end_time', e.target.value)} />
          </label>
          <button type="button" data-add-period className="brand-period-add mb-1 grid h-9 w-9 place-items-center rounded-lg" aria-label={t('Добавить период')} onClick={add}>
            <i className="fas fa-plus"></i>
          </button>
          <button
            type="button"
            data-remove-period
            className="mb-1 grid h-9 w-9 place-items-center rounded-lg text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-900/20"
            aria-label={t('Удалить период')}
            onClick={() => remove(i)}
          >
            <i className="fas fa-trash"></i>
          </button>
        </div>
      ))}
    </div>
  );
}
