import type { TFunction } from 'i18next';
import type { ChartSeries } from './HealthChart';

/* Серии графиков здоровья ПК (Этап 7): живой график «Сейчас» и график недели — одно описание (подпись, цвет, ось). */

/** Серия графика здоровья. */
export type HealthSeriesKey = 'cpu' | 'ram' | 'cpuTemp' | 'gpuTemp';

/** Цвета серий графиков здоровья (как у дашборда и полосок карточки ПК). */
export const SERIES_COLORS: Readonly<Record<HealthSeriesKey, string>> = {
  cpu: 'rgba(22, 163, 74, 1)',
  ram: 'rgba(14, 165, 233, 1)',
  cpuTemp: 'rgba(245, 158, 11, 1)',
  gpuTemp: 'rgba(225, 29, 72, 1)',
};

function seriesLabel(t: TFunction, key: HealthSeriesKey): string {
  switch (key) {
    case 'cpu':
      return t('Загрузка CPU');
    case 'ram':
      return t('ОЗУ');
    case 'cpuTemp':
      return t('Температура CPU');
    case 'gpuTemp':
      return t('Температура GPU');
  }
}

/** Серии в порядке order (порядок легенды): подпись, цвет, ось (загрузка — %, температуры — °C) и значения. */
export function healthSeries<T>(t: TFunction, order: readonly HealthSeriesKey[], points: readonly T[],
  value: (p: T, key: HealthSeriesKey) => number | null | undefined): ChartSeries[] {
  return order.map((key) => ({
    label: seriesLabel(t, key),
    data: points.map((p) => value(p, key) ?? null),
    color: SERIES_COLORS[key],
    axis: key === 'cpu' || key === 'ram' ? 'load' : 'temp',
  }));
}

/** Час недели графика: время начала (мс) и строка почасовой истории (null — строки нет: ПК не был в сети). */
export interface WeekHour<T> {
  readonly at: number;
  readonly row: T | null;
}

/**
 * Непрерывная ось графика недели: все часы [start, end) по порядку, у часов без строки — null (линия рвётся, spanGaps:
 * false, а не соединяет 01:00 и 14:00). Шаг — час UTC: переход на летнее время даёт 167/169 часов, как у сервера.
 */
export function weekHours<T extends { readonly hour: string }>(start: string, end: string, rows: readonly T[]): WeekHour<T>[] {
  const byHour = new Map(rows.map((r) => [Date.parse(r.hour), r]));
  const from = Date.parse(start);
  const to = Date.parse(end);
  const out: WeekHour<T>[] = [];
  for (let at = from; at < to; at += 3_600_000) out.push({ at, row: byHour.get(at) ?? null });
  return out;
}
