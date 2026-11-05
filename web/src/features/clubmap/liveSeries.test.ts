import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest';
import { weekHours } from './healthSeries';
import { LIVE_WINDOW_MS, pruneLiveSeries, pushSample, resetLiveSeries, samplesOf, trackedComputers } from './liveSeries';

// Окно — от часов браузера: «сейчас» — сразу после последней точки теста.
let now: MockInstance<() => number>;
beforeEach(() => {
  now = vi.spyOn(Date, 'now').mockReturnValue(60_000);
});
afterEach(() => {
  now.mockRestore();
  resetLiveSeries();
});

describe('live series', () => {
  // Ответ GET …/health из кэша приходит со старым dataUpdatedAt после событий — точка встаёт по времени, а не в конец.
  it('keeps samples in time order', () => {
    pushSample(7, { at: 10_000, cpu: 10 });
    pushSample(7, { at: 50_000, cpu: 50 });
    pushSample(7, { at: 30_000, cpu: 30 });
    expect(samplesOf(7).map((s) => s.at)).toEqual([10_000, 30_000, 50_000]);
    // То же измерение (ближе 2 с): более новое остаётся — и раньше, и позже соседа.
    pushSample(7, { at: 49_000, cpu: 49 });
    pushSample(7, { at: 31_000, cpu: 31 });
    expect(samplesOf(7).map((s) => s.cpu)).toEqual([10, 31, 50]);
  });

  // У ПК без новых событий старые точки уходят по часам браузера, а ПК без точек забывается.
  it('drops samples older than the window and forgets idle PCs', () => {
    now.mockReturnValue(100_000 + LIVE_WINDOW_MS - 1_000);
    pushSample(7, { at: 100_000, cpu: 10 });
    pushSample(8, { at: 100_000 + LIVE_WINDOW_MS - 1_000, cpu: 20 });
    pruneLiveSeries(100_000 + LIVE_WINDOW_MS);
    expect(samplesOf(7)).toEqual([]);
    expect(samplesOf(8).map((s) => s.cpu)).toEqual([20]);
    expect(trackedComputers()).toBe(1);
    // Событие другого ПК позже окна не трогает чужие точки (обрезается только свой ПК) — их убирает таймер.
    now.mockReturnValue(100_000 + 2 * LIVE_WINDOW_MS);
    pushSample(9, { at: 100_000 + 2 * LIVE_WINDOW_MS, cpu: 30 });
    expect(samplesOf(8).map((s) => s.cpu)).toEqual([20]);
    pruneLiveSeries();
    expect(samplesOf(8)).toEqual([]);
    expect(trackedComputers()).toBe(1);
    // Точка старше окна не добавляется; свежая точка ПК убирает его старые.
    pushSample(7, { at: 100_000 + LIVE_WINDOW_MS, cpu: 40 });
    expect(samplesOf(7)).toEqual([]);
    pushSample(9, { at: 100_000 + 2 * LIVE_WINDOW_MS - 10_000, cpu: 31 });
    now.mockReturnValue(100_000 + 3 * LIVE_WINDOW_MS - 5_000);
    pushSample(9, { at: 100_000 + 3 * LIVE_WINDOW_MS - 5_000, cpu: 32 });
    expect(samplesOf(9).map((s) => s.cpu)).toEqual([30, 32]); // 31 — старше окна
  });

  // Ось недели — все часы [start, end): офлайн-часы — null (линия рвётся), а не пропадают.
  it('builds a continuous hourly axis of a week', () => {
    const rows = [{ hour: '2026-11-02T01:00:00Z', v: 1 }, { hour: '2026-11-02T04:00:00Z', v: 4 }];
    const hours = weekHours('2026-11-02T00:00:00+00:00', '2026-11-02T06:00:00Z', rows);
    expect(hours.map((h) => new Date(h.at).toISOString().slice(11, 13))).toEqual(['00', '01', '02', '03', '04', '05']);
    expect(hours.map((h) => h.row?.v ?? null)).toEqual([null, 1, null, null, 4, null]);
    expect(weekHours('2026-11-02T00:00:00+05:00', '2026-11-09T00:00:00+05:00', [])).toHaveLength(168);
  });
});
