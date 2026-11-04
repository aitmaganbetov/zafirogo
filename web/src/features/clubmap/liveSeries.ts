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

/** Добавить точку ПК (старше окна — отбрасываются). Пустая точка (нет данных агента) не добавляется. */
export function pushSample(computerId: number, s: LiveSample): void {
  if (empty(s)) return;
  const prev = series.get(computerId) ?? EMPTY;
  const kept = prev.filter((x) => x.at > s.at - LIVE_WINDOW_MS);
  const last = kept[kept.length - 1];
  const next = last && Math.abs(s.at - last.at) < MERGE_MS ? [...kept.slice(0, -1), s] : [...kept, s];
  series.set(computerId, next);
  for (const l of listeners) l();
}

/** Точки ПК (стабильная ссылка между изменениями — для useSyncExternalStore). */
export function samplesOf(computerId: number): readonly LiveSample[] {
  return series.get(computerId) ?? EMPTY;
}

export function subscribeSamples(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** Забыть все точки (тесты). */
export function resetLiveSeries(): void {
  series.clear();
  for (const l of listeners) l();
}

/** Цвета серий графиков здоровья (как у дашборда и полосок карточки ПК). */
export const SERIES_COLORS = {
  cpu: 'rgba(22, 163, 74, 1)',
  ram: 'rgba(14, 165, 233, 1)',
  cpuTemp: 'rgba(245, 158, 11, 1)',
  gpuTemp: 'rgba(225, 29, 72, 1)',
} as const;
