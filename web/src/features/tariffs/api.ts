import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/tariffs';
import { intParam } from '../../lib/searchParams';

/*
 * Зоны и тарифы (api/tariffs.yaml). Laravel: TariffController. Ключи Query — ['tariffs', …].
 */

export const tariffsApi = createApiClient<paths>();

type S = components['schemas'];
export type PageTariff = S['PageTariff'];
export type TariffInput = S['TariffInput'];
export type WeeklyPeriod = S['WeeklyPeriod'];

export interface TariffsSearch {
  readonly zone?: number;
}

export function parseTariffsSearch(s: Record<string, unknown>): TariffsSearch {
  return { zone: intParam(s.zone) };
}

export const tariffsPageQuery = (zone?: number) =>
  queryOptions({
    queryKey: ['tariffs', 'page', zone ?? 0],
    queryFn: ({ signal }) => unwrap(tariffsApi.GET('/api/tariffs/page', { params: { query: { zone } }, signal })),
  });

export const createTariff = (body: TariffInput) => unwrap(tariffsApi.POST('/api/tariffs', { body }));
export const updateTariff = (id: number, body: TariffInput) => unwrap(tariffsApi.PATCH('/api/tariffs/{tariffId}', { params: { path: { tariffId: id } }, body }));
export const deleteTariff = (id: number) => unwrap(tariffsApi.DELETE('/api/tariffs/{tariffId}', { params: { path: { tariffId: id } } }));

export type DurationUnit = 'minutes' | 'hours' | 'days';

/** TariffController::splitMinutes(): минуты — самой крупной целой единицей (дни, часы, минуты). */
export function splitMinutes(minutes: number): [number, DurationUnit] {
  if (minutes > 0 && minutes % 1440 === 0) return [minutes / 1440, 'days'];
  if (minutes > 0 && minutes % 60 === 0) return [minutes / 60, 'hours'];
  return [minutes, 'minutes'];
}

/**
 * Цена минуты в тиынах — number_format((float) $zone->price_per_hour / 60, 2): округление до тиына, половина — вверх
 * (PHP round, цены неотрицательные), целыми без float.
 */
export function perMinuteTiyn(perHourTiyn: number): number {
  return Math.floor((perHourTiyn + 30) / 60);
}

const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'] as const;

/** Новая строка периода — последняя на день позже (пн 10:00–17:00 → вт 10:00–17:00), как скрипт шаблона. */
export function nextPeriod(last: WeeklyPeriod | undefined): WeeklyPeriod {
  if (!last) return { start_day: 'mon', start_time: '00:00', end_day: 'sun', end_time: '23:59' };
  const next = (d: string) => DAYS[(DAYS.indexOf(d as (typeof DAYS)[number]) + 1) % 7] ?? 'mon';
  return { start_day: next(last.start_day), start_time: last.start_time, end_day: next(last.end_day), end_time: last.end_time };
}

/** $alwaysPeriods. */
export const ALWAYS: WeeklyPeriod[] = [{ start_day: 'mon', start_time: '00:00', end_day: 'sun', end_time: '23:59' }];
