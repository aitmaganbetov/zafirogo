import type { HealthView } from './api';
import type { LoadEvent } from './useMapLive';

/*
 * Живой график панели ПК (Этап 7): последние 15 минут загрузки CPU/ОЗУ и температур CPU/GPU. Копится на клиенте из
 * живых событий computer.load (useMapLive) и ответов GET …/health (открытие панели) — без нового серверного хранения.
 * Точки — по каждому ПК клуба, пока открыта карта (событие приходит не чаще раза в 15 с на ПК): ≈ 60 точек на ПК.
 */

/** Точка графика: время (мс) и значения в процентах / °C (нет измерения — undefined). */
export interface LiveSample {
  readonly at: number;
  readonly cpu?: number;
  readonly ram?: number;
  readonly cpuTemp?: number;
  readonly gpuTemp?: number;
}

/** Окно графика. */
export const LIVE_WINDOW_MS = 15 * 60 * 1000;
/** Точки ближе этого к предыдущей заменяют её (событие и перечитанное состояние после него — одно измерение). */
const MERGE_MS = 2000;

const EMPTY: readonly LiveSample[] = [];
const series = new Map<number, readonly LiveSample[]>();
const listeners = new Set<() => void>();
/** Самое позднее время точки среди всех ПК (часы событий): окно отсчитывается от него. */
let latest = 0;

const pct = (bp: number | null | undefined) => (bp == null ? undefined : Math.round(bp / 100));
const deg = (deci: number | null | undefined) => (deci == null || deci === 0 ? undefined : Math.round(deci / 10));

/** Точка из события computer.load. */
export function sampleFromEvent(ev: LoadEvent, at: number): LiveSample {
  return { at, cpu: pct(ev.load?.cpu_bp), ram: pct(ev.load?.ram_bp), cpuTemp: deg(ev.cpu_temp_deci), gpuTemp: deg(ev.gpu_temp_deci) };
}

/** Точка из ответа GET …/health. */
export function sampleFromView(view: HealthView, at: number): LiveSample {
  return { at, cpu: pct(view.load?.cpu_bp), ram: pct(view.load?.ram_bp), cpuTemp: deg(view.health?.cpu_temp_deci), gpuTemp: deg(view.health?.gpu_temp_deci) };
}

const empty = (s: LiveSample) => s.cpu == null && s.ram == null && s.cpuTemp == null && s.gpuTemp == null;

const notify = () => {
  for (const l of listeners) l();
};

/** Убрать точки не новее cutoff у всех ПК; ПК без точек забывается (Map не растёт по всем когда-либо виденным ПК). */
function dropBefore(cutoff: number): boolean {
  let changed = false;
  for (const [id, pts] of series) {
    const first = pts[0];
    if (first && first.at > cutoff) continue;
    const kept = pts.filter((x) => x.at > cutoff);
    changed = true;
    if (kept.length === 0) series.delete(id);
    else series.set(id, kept);
  }
  return changed;
}

/**
 * Добавить точку ПК. Точки идут по времени: запоздавшая (ответ GET …/health из кэша со старым dataUpdatedAt) встаёт на
 * своё место, а не в конец. Точка ближе MERGE_MS к соседней — то же измерение: более новая остаётся. Старше окна (от
 * самой поздней точки) — отбрасываются у всех ПК. Пустая точка (нет данных агента) не добавляется.
 */
export function pushSample(computerId: number, s: LiveSample): void {
  if (empty(s)) return;
  latest = Math.max(latest, s.at);
  const cutoff = latest - LIVE_WINDOW_MS;
  let changed = dropBefore(cutoff);
  if (s.at > cutoff) {
    const next = [...(series.get(computerId) ?? EMPTY)];
    let i = next.findIndex((x) => x.at > s.at);
    if (i < 0) i = next.length;
    const before = next[i - 1];
    const after = next[i];
    if (after && after.at - s.at < MERGE_MS) {
      // Более новое измерение уже есть.
    } else if (before && s.at - before.at < MERGE_MS) {
      next[i - 1] = s;
      changed = true;
    } else {
      next.splice(i, 0, s);
      changed = true;
    }
    if (changed) series.set(computerId, next);
  }
  if (changed) notify();
}

/**
 * Забыть точки старше окна по часам браузера (now): у ПК без новых событий старые точки не висят бесконечно. Вызывается
 * по таймеру, пока виден живой график.
 */
export function pruneLiveSeries(now: number): void {
  if (dropBefore(now - LIVE_WINDOW_MS)) notify();
}

/** Точки ПК по времени (стабильная ссылка между изменениями — для useSyncExternalStore). */
export function samplesOf(computerId: number): readonly LiveSample[] {
  return series.get(computerId) ?? EMPTY;
}

/** Сколько ПК с точками (тесты). */
export function trackedComputers(): number {
  return series.size;
}

export function subscribeSamples(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** Забыть все точки (тесты). */
export function resetLiveSeries(): void {
  series.clear();
  latest = 0;
  notify();
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

/** Цвета серий графиков здоровья (как у дашборда и полосок карточки ПК). */
export const SERIES_COLORS = {
  cpu: 'rgba(22, 163, 74, 1)',
  ram: 'rgba(14, 165, 233, 1)',
  cpuTemp: 'rgba(245, 158, 11, 1)',
  gpuTemp: 'rgba(225, 29, 72, 1)',
} as const;
