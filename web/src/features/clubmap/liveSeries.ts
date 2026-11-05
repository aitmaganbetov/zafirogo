import type { ComputerState } from './api';
import { degOrNull, pctOrNull } from './healthText';

/*
 * Живой график панели ПК (Этап 7): последние 15 минут загрузки CPU/ОЗУ и температур CPU/GPU. Копится на клиенте из
 * живых событий computer.load (useMapLive; несут загрузку и температуры) и первого ответа GET …/health при открытии
 * панели — без нового серверного хранения. Точки — по каждому ПК клуба, пока открыта карта (событие приходит не чаще
 * раза в 15 с на ПК): ≈ 60 точек на ПК. Окно отсчитывается от часов браузера (Date.now()).
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

/**
 * Точка из загрузки и температур ПК (событие computer.load или ответ GET …/health). Температура 0 — «не измерено»
 * (агент не отдал датчик), загрузка — как пришла.
 */
export function sample(at: number, load: ComputerState['load'], cpuTempDeci?: number | null, gpuTempDeci?: number | null): LiveSample {
  return {
    at,
    cpu: pctOrNull(load?.cpu_bp) ?? undefined,
    ram: pctOrNull(load?.ram_bp) ?? undefined,
    cpuTemp: degOrNull(cpuTempDeci, true) ?? undefined,
    gpuTemp: degOrNull(gpuTempDeci, true) ?? undefined,
  };
}

const empty = (s: LiveSample) => s.cpu == null && s.ram == null && s.cpuTemp == null && s.gpuTemp == null;

const notify = () => {
  for (const l of listeners) l();
};

/** Начало окна по часам браузера. */
const windowStart = (now: number) => now - LIVE_WINDOW_MS;

/**
 * Добавить точку ПК. Точки идут по времени: запоздавшая (ответ GET …/health из кэша со старым dataUpdatedAt) встаёт на
 * своё место, а не в конец. Точка ближе MERGE_MS к соседней — то же измерение: более новая остаётся. Точки ЭТОГО ПК
 * старше окна уходят здесь же, остальных ПК — по таймеру (pruneLiveSeries). Пустая точка (нет данных агента) и точка
 * старше окна не добавляются.
 */
export function pushSample(computerId: number, s: LiveSample): void {
  if (empty(s)) return;
  const cutoff = windowStart(Date.now());
  const pts = series.get(computerId) ?? EMPTY;
  const next = pts[0] && pts[0].at <= cutoff ? pts.filter((x) => x.at > cutoff) : [...pts];
  let changed = next.length !== pts.length;
  if (s.at > cutoff) {
    let i = next.findIndex((x) => x.at > s.at);
    if (i < 0) i = next.length;
    const before = next[i - 1];
    const after = next[i];
    // Более новое измерение рядом уже есть — точка не нужна; иначе заменяет соседнюю (то же измерение) или встаёт между.
    if (!after || after.at - s.at >= MERGE_MS) {
      if (before && s.at - before.at < MERGE_MS) next[i - 1] = s;
      else next.splice(i, 0, s);
      changed = true;
    }
  }
  if (!changed) return;
  if (next.length === 0) series.delete(computerId);
  else series.set(computerId, next);
  notify();
}

/**
 * Забыть точки старше окна по часам браузера (now) у всех ПК: у ПК без новых событий старые точки не висят бесконечно,
 * ПК без точек забывается (Map не растёт по всем когда-либо виденным ПК). Вызывается по таймеру, пока виден живой график.
 */
export function pruneLiveSeries(now: number = Date.now()): void {
  const cutoff = windowStart(now);
  let changed = false;
  for (const [id, pts] of series) {
    const first = pts[0];
    if (first && first.at > cutoff) continue;
    const kept = pts.filter((x) => x.at > cutoff);
    changed = true;
    if (kept.length === 0) series.delete(id);
    else series.set(id, kept);
  }
  if (changed) notify();
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
  notify();
}
