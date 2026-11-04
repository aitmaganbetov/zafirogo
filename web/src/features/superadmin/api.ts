import { queryOptions } from '@tanstack/react-query';
import { apiFetch, createApiClient, errorFromResponse, isApiError, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/superadmin';

/*
 * Кабинет суперадмина (api/superadmin.yaml). Laravel: app/Http/Controllers/Superadmin/*. Ключи Query — ['superadmin', …].
 * Списки — страницы с номерами (как paginate() старых экранов: ссылки Laravel-пагинации).
 */

export const saApi = createApiClient<paths>();

type S = components['schemas'];
export type Dashboard = S['Dashboard'];
export type Chart = S['Chart'];
export type LogRow = S['LogRow'];
export type ClubList = S['ClubList'];
export type ClubCard = S['ClubCard'];
export type SubscriptionBadge = S['SubscriptionBadge'];
export type AccountList = S['AccountList'];
export type Plan = S['Plan'];
export type PlanForm = S['PlanForm'];
export type LogList = S['LogList'];
export type PlatformNewsPage = S['PlatformNewsPage'];
export type PlatformNewsItem = S['PlatformNewsItem'];
export type LanguageList = S['LanguageList'];
export type TranslationPage = S['TranslationPage'];
export type TelegramState = S['TelegramState'];
export type ImportResult = S['ImportResult'];

/** Число из параметра адреса (страница, id) или undefined. */
export function intParam(v: unknown, min = 1): number | undefined {
  const n = typeof v === 'number' ? v : typeof v === 'string' && v !== '' ? Number(v) : NaN;
  return Number.isSafeInteger(n) && n >= min ? n : undefined;
}

export function strParam(v: unknown): string | undefined {
  if (typeof v === 'number') return String(v);
  return typeof v === 'string' && v !== '' ? v : undefined;
}

/** Строка запроса без пустых значений (withQueryString()). */
export function qs(params: Record<string, string | number | boolean | readonly (string | number)[] | undefined | null>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '' || v === false) continue;
    if (Array.isArray(v)) for (const item of v) q.append(k, String(item));
    else q.set(k, v === true ? '1' : String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : '';
}

// ---------------------------------------------------------------------------
// Сервер
// ---------------------------------------------------------------------------

export interface DashboardSearch {
  readonly period?: '24h' | '7d';
  readonly clubs?: readonly string[];
}

export function parseDashboardSearch(s: Record<string, unknown>): DashboardSearch {
  const period = s.period === '7d' || s.period === '24h' ? s.period : undefined;
  const raw = s.clubs ?? s['clubs[]'];
  const list = (Array.isArray(raw) ? raw : raw === undefined ? [] : [raw]).map((v) => String(v)).filter((v) => /^\d+$/.test(v));
  return { period, clubs: list.length > 0 ? list : undefined };
}

export const dashboardQuery = (s: DashboardSearch) =>
  queryOptions({
    queryKey: ['superadmin', 'dashboard', s.period ?? '', s.clubs ?? []],
    queryFn: () => unwrap(saApi.GET('/api/superadmin/dashboard', { params: { query: { period: s.period, clubs: s.clubs ? [...s.clubs] : undefined } } })),
  });

export const setAgentRelease = (body: S['AgentReleaseForm']) => unwrap(saApi.PUT('/api/superadmin/agent-release', { body }));
export const clearAgentRelease = () => unwrap(saApi.DELETE('/api/superadmin/agent-release'));

// ---------------------------------------------------------------------------
// Клубы
// ---------------------------------------------------------------------------

export interface ClubsSearch {
  readonly search?: string;
  readonly subscription?: 'active' | 'inactive';
  readonly sort?: string;
  readonly direction?: 'asc' | 'desc';
  readonly page?: number;
}

export function parseClubsSearch(s: Record<string, unknown>): ClubsSearch {
  return {
    search: strParam(s.search),
    subscription: s.subscription === 'active' || s.subscription === 'inactive' ? s.subscription : undefined,
    sort: strParam(s.sort),
    direction: s.direction === 'asc' || s.direction === 'desc' ? s.direction : undefined,
    page: intParam(s.page, 2),
  };
}

export const clubsQuery = (s: ClubsSearch) =>
  queryOptions({
    queryKey: ['superadmin', 'clubs', s],
    queryFn: () =>
      unwrap(
        saApi.GET('/api/superadmin/clubs', {
          params: { query: { search: s.search, subscription: s.subscription, sort: s.sort as never, direction: s.direction, page: s.page } },
        }),
      ),
  });

export const clubQuery = (id: number, page?: number) =>
  queryOptions({
    queryKey: ['superadmin', 'club', id, page ?? 1],
    queryFn: () => unwrap(saApi.GET('/api/superadmin/clubs/{clubId}', { params: { path: { clubId: id }, query: { page } } })),
  });

export const extendSubscription = (clubId: number, body: S['ExtendForm']) =>
  unwrap(saApi.POST('/api/superadmin/clubs/{clubId}/subscription/extend', { params: { path: { clubId } }, body }));
export const suspendSubscription = (clubId: number) =>
  unwrap(saApi.POST('/api/superadmin/clubs/{clubId}/subscription/suspend', { params: { path: { clubId } } }));
export const changeClubPlan = (clubId: number, planId: number) =>
  unwrap(saApi.POST('/api/superadmin/clubs/{clubId}/subscription/plan', { params: { path: { clubId } }, body: { saas_plan_id: planId } }));

// ---------------------------------------------------------------------------
// Пользователи, тарифы, журнал
// ---------------------------------------------------------------------------

export interface AccountsSearch {
  readonly search?: string;
  readonly filter?: 'blocked' | 'superadmin';
  readonly page?: number;
}

export function parseAccountsSearch(s: Record<string, unknown>): AccountsSearch {
  return {
    search: strParam(s.search),
    filter: s.filter === 'blocked' || s.filter === 'superadmin' ? s.filter : undefined,
    page: intParam(s.page, 2),
  };
}

export const accountsQuery = (s: AccountsSearch) =>
  queryOptions({
    queryKey: ['superadmin', 'accounts', s],
    queryFn: () => unwrap(saApi.GET('/api/superadmin/accounts', { params: { query: { search: s.search, filter: s.filter, page: s.page } } })),
  });

export type AccountAction = 'block' | 'unblock' | 'grant' | 'revoke' | 'reset-second-factor';

export function accountAction(userId: number, action: AccountAction) {
  const path = { params: { path: { userId } } };
  switch (action) {
    case 'block':
      return unwrap(saApi.POST('/api/superadmin/accounts/{userId}/block', path));
    case 'unblock':
      return unwrap(saApi.POST('/api/superadmin/accounts/{userId}/unblock', path));
    case 'grant':
      return unwrap(saApi.POST('/api/superadmin/accounts/{userId}/superadmin', path));
    case 'revoke':
      return unwrap(saApi.DELETE('/api/superadmin/accounts/{userId}/superadmin', path));
    case 'reset-second-factor': // Этап 7: сброс TOTP другого суперадмина
      return unwrap(saApi.DELETE('/api/superadmin/accounts/{userId}/second-factor', path));
  }
}

export const plansQuery = () =>
  queryOptions({ queryKey: ['superadmin', 'plans'], queryFn: () => unwrap(saApi.GET('/api/superadmin/plans')) });
export const createPlan = (body: PlanForm) => unwrap(saApi.POST('/api/superadmin/plans', { body }));
export const updatePlan = (planId: number, body: PlanForm) =>
  unwrap(saApi.PATCH('/api/superadmin/plans/{planId}', { params: { path: { planId } }, body }));

export interface LogsSearch {
  readonly tab?: 'clubs';
  readonly club?: number;
  readonly event?: string;
  readonly page?: number;
}

export function parseLogsSearch(s: Record<string, unknown>): LogsSearch {
  return { tab: s.tab === 'clubs' ? 'clubs' : undefined, club: intParam(s.club), event: strParam(s.event), page: intParam(s.page, 2) };
}

export const logsQuery = (s: LogsSearch) =>
  queryOptions({
    queryKey: ['superadmin', 'logs', s],
    queryFn: () =>
      unwrap(saApi.GET('/api/superadmin/logs', { params: { query: { tab: s.tab ?? 'platform', club: s.club, event: s.event, page: s.page } } })),
  });

// ---------------------------------------------------------------------------
// Новости платформы (формы — multipart, как в разделе новостей клуба)
// ---------------------------------------------------------------------------

export const listPlatformNews = (page?: number) => unwrap(saApi.GET('/api/superadmin/platform-news', { params: { query: { page } } }));

export const platformNewsQuery = (page?: number) =>
  queryOptions({ queryKey: ['superadmin', 'news', page ?? 1], queryFn: () => listPlatformNews(page) });

async function sendForm<T>(method: 'POST' | 'PATCH', url: string, form: FormData): Promise<T> {
  const res = await apiFetch(new Request(new URL(url, location.origin), { method, body: form, credentials: 'same-origin' }));
  if (!res.ok) throw await errorFromResponse(res);
  return (await res.json()) as T;
}

export const createPlatformNews = (form: FormData) => sendForm<S['Message']>('POST', '/api/superadmin/platform-news', form);
export const updatePlatformNews = (id: number, form: FormData) => sendForm<S['Message']>('PATCH', `/api/superadmin/platform-news/${id}`, form);
export const deletePlatformNews = (id: number) =>
  unwrap(saApi.DELETE('/api/superadmin/platform-news/{newsId}', { params: { path: { newsId: id } } }));

// ---------------------------------------------------------------------------
// Языки и переводы
// ---------------------------------------------------------------------------

export const languagesQuery = () =>
  queryOptions({ queryKey: ['superadmin', 'languages'], queryFn: () => unwrap(saApi.GET('/api/superadmin/languages')) });
export const createLanguage = (code: string, name: string) => unwrap(saApi.POST('/api/superadmin/languages', { body: { code, name } }));
export const updateLanguage = (id: number, body: S['LanguageUpdate']) =>
  unwrap(saApi.PATCH('/api/superadmin/languages/{languageId}', { params: { path: { languageId: id } }, body }));
export const deleteLanguage = (id: number) =>
  unwrap(saApi.DELETE('/api/superadmin/languages/{languageId}', { params: { path: { languageId: id } } }));
export const importTranslations = (form: FormData) => sendForm<ImportResult>('POST', '/api/superadmin/languages/import', form);

/** Скачать CSV (GET languages.export с токеном): файл сохраняется под именем из Content-Disposition. */
export async function exportTranslations(language: string, missing: boolean): Promise<void> {
  const url = new URL('/api/superadmin/languages/export' + qs({ language, missing }), location.origin);
  const res = await apiFetch(new Request(url, { method: 'GET', credentials: 'same-origin' }));
  if (!res.ok) throw await errorFromResponse(res);
  const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? 'translations.csv';
  const href = URL.createObjectURL(await res.blob());
  const a = document.createElement('a');
  a.href = href;
  a.download = name;
  a.click();
  URL.revokeObjectURL(href);
}

export interface TranslationsSearch {
  readonly search?: string;
  readonly missing?: boolean;
  readonly page?: number;
}

export function parseTranslationsSearch(s: Record<string, unknown>): TranslationsSearch {
  const missing = s.missing === true || s.missing === 1 || s.missing === '1' || s.missing === 'true';
  return { search: strParam(s.search), missing: missing || undefined, page: intParam(s.page, 2) };
}

export const translationsQuery = (languageId: number, s: TranslationsSearch) =>
  queryOptions({
    queryKey: ['superadmin', 'translations', languageId, s],
    queryFn: () =>
      unwrap(
        saApi.GET('/api/superadmin/languages/{languageId}/translations', {
          params: { path: { languageId }, query: { search: s.search, missing: s.missing, page: s.page } },
        }),
      ),
  });

export const saveTranslations = (languageId: number, translations: Record<string, string>) =>
  unwrap(saApi.PUT('/api/superadmin/languages/{languageId}/translations', { params: { path: { languageId } }, body: { translations } }));

// ---------------------------------------------------------------------------
// Telegram
// ---------------------------------------------------------------------------

export const telegramQuery = () =>
  queryOptions({ queryKey: ['superadmin', 'telegram'], queryFn: () => unwrap(saApi.GET('/api/superadmin/telegram')) });
export const setGateway = (token: string) => unwrap(saApi.POST('/api/superadmin/telegram', { body: { token } }));
export const clearGateway = () => unwrap(saApi.DELETE('/api/superadmin/telegram'));
export const setAlerts = (botToken: string, chatId: string) =>
  unwrap(saApi.POST('/api/superadmin/telegram/alerts', { body: { bot_token: botToken, chat_id: chatId || null } }));
export const testAlerts = () => unwrap(saApi.POST('/api/superadmin/telegram/alerts/test'));
export const clearAlerts = () => unwrap(saApi.DELETE('/api/superadmin/telegram/alerts'));

/** Первая ошибка ответа ($errors->first()). */
export function firstError(e: unknown): string {
  if (isApiError(e)) {
    const list = Object.values(e.fieldErrors).flat();
    return list[0] ?? e.message;
  }
  return e instanceof Error ? e.message : String(e);
}
