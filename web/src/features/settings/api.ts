import { queryOptions } from '@tanstack/react-query';
import { apiFetch, createApiClient, errorFromResponse, isApiError, unwrap } from '../../api/http';
import type { components as SC, paths as SP } from '../../api/gen/settings';
import type { components as IC, paths as IP } from '../../api/gen/integrations';

/*
 * Настройки клуба (api/settings.yaml) и «API и интеграции» (api/integrations.yaml). Laravel: SettingsController,
 * LanguageController, ClubLogoController, ClubSignInImageController, IntegrationController. Ключи Query —
 * ['settings', …] и ['integrations', …].
 */

export const settingsApi = createApiClient<SP>();
export const integrationsApi = createApiClient<IP>();

export type ClubSettingsPage = SC['schemas']['ClubSettingsPage'];
export type PercentLevel = SC['schemas']['PercentLevel'];
export type LanguagesPage = SC['schemas']['LanguagesPage'];
export type IntegrationsPage = IC['schemas']['IntegrationsPage'];
export type CreatedSecret = IC['schemas']['CreatedSecret'];
export type DeliveryPage = IC['schemas']['DeliveryPage'];
export type SettingsSection = 'club' | 'finances' | 'guests';

export const clubSettingsQuery = () =>
  queryOptions({ queryKey: ['settings', 'club'], queryFn: () => unwrap(settingsApi.GET('/api/settings')) });

export const languagesQuery = () =>
  queryOptions({ queryKey: ['settings', 'languages'], queryFn: () => unwrap(settingsApi.GET('/api/settings/languages')) });

export const integrationsQuery = () =>
  queryOptions({ queryKey: ['integrations'], queryFn: () => unwrap(integrationsApi.GET('/api/settings/integrations')) });

export const deliveriesPage = (cursor: string) =>
  unwrap(integrationsApi.GET('/api/settings/integrations/deliveries', { params: { query: { cursor } } }));

export const saveSection = (section: SettingsSection, body: Record<string, unknown>) =>
  unwrap(settingsApi.PUT('/api/settings/sections/{section}', { params: { path: { section } }, body }));
export const saveTimezone = (timezone: string) => unwrap(settingsApi.PUT('/api/settings/timezone', { body: { timezone } }));
export const saveLanguage = (locale: string) => unwrap(settingsApi.PUT('/api/settings/language', { body: { locale } }));
export const saveAgentPin = (pin: string, pin_confirmation: string) =>
  unwrap(settingsApi.PUT('/api/settings/agent-pin', { body: { pin, pin_confirmation } }));
export const saveStaffTime = (minutes: string) => unwrap(settingsApi.PUT('/api/settings/agent-staff-time', { body: { minutes } }));
export const deleteLogo = () => unwrap(settingsApi.DELETE('/api/settings/logo'));
export const deleteSigninImage = () => unwrap(settingsApi.DELETE('/api/settings/signin-image'));

/** Загрузка картинки формой multipart (как enctype="multipart/form-data" старой формы). */
export async function uploadImage(url: '/api/settings/logo' | '/api/settings/signin-image', form: FormData): Promise<{ message: string }> {
  const res = await apiFetch(new Request(new URL(url, location.origin), { method: 'POST', body: form, credentials: 'same-origin' }));
  if (!res.ok) throw await errorFromResponse(res);
  return (await res.json()) as { message: string };
}

/** Первая ошибка поля ответа (@error), иначе общий текст. */
export function firstError(e: unknown, ...fields: string[]): string | undefined {
  if (!isApiError(e)) return e instanceof Error ? e.message : undefined;
  for (const f of fields) {
    const m = e.field(f);
    if (m) return m;
  }
  return fields.length === 0 ? e.message : undefined;
}

/** $errors->all(). */
export function allErrors(e: unknown): string[] {
  if (isApiError(e)) {
    const list = Object.values(e.fieldErrors).flat();
    return list.length > 0 ? list : [e.message];
  }
  return [e instanceof Error ? e.message : String(e)];
}

/** Сотые (тиыны, базисные пункты) → число PHP для поля ввода: 100050 → «1000.5», 500 → «5». */
export function hundredthsToInput(n: number): string {
  const neg = n < 0;
  const abs = Math.abs(n);
  const frac = abs % 100;
  let s = String(Math.floor(abs / 100));
  if (frac !== 0) s += '.' + String(frac).padStart(2, '0').replace(/0$/, '');
  return (neg ? '-' : '') + s;
}

/** Число минут (не деньги) как PHP выводит int|float: 0.5 → «0.5», 15 → «15». */
export function minutesToInput(n: number): string {
  return String(n);
}

/**
 * Поля формы → тело запроса, как PHP разбирает форму: повтор поля (hidden 0 + checkbox 1) — последнее значение;
 * `f[i][k]` — список объектов (уровни); одиночное `f` без строк — пустой список.
 */
export function formToBody(form: HTMLFormElement): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  const lists: Record<string, Map<number, Record<string, string>>> = {};
  for (const [name, value] of new FormData(form).entries()) {
    if (typeof value !== 'string') continue;
    const m = /^(\w+)\[(\d+)\]\[(\w+)\]$/.exec(name);
    if (m) {
      const [, field = '', idx = '0', key = ''] = m;
      const rows = (lists[field] ??= new Map());
      const row = rows.get(Number(idx)) ?? {};
      row[key] = value;
      rows.set(Number(idx), row);
      continue;
    }
    out[name] = value;
  }
  for (const field of Object.keys(out)) {
    if (field.endsWith('_levels') && out[field] === '') out[field] = [];
  }
  for (const [field, rows] of Object.entries(lists)) {
    out[field] = [...rows.entries()].sort((a, b) => a[0] - b[0]).map(([, r]) => r);
  }
  return out;
}
