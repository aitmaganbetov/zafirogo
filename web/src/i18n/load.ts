import i18n, { DEFAULT_GUEST_LANGUAGE, DEFAULT_LANGUAGE } from './index';

/*
 * Загрузка переводов с сервера (api/i18n.yaml). Браузер сам перепроверяет пакет по ETag (Cache-Control: no-cache
 * → If-None-Match → 304), поэтому повторная загрузка дешёвая.
 */

export interface LanguageInfo {
  readonly code: string;
  readonly name: string;
}

let languages: readonly LanguageInfo[] | null = null;
const loaded = new Map<string, Promise<void>>();

/** Включённые языки (TranslationCatalog::enabledCodes()); при ошибке — ru и en. */
export async function enabledLanguages(): Promise<readonly LanguageInfo[]> {
  if (languages) return languages;
  try {
    const res = await fetch('/api/i18n', { headers: { Accept: 'application/json' } });
    if (!res.ok) throw new Error(String(res.status));
    const body = (await res.json()) as { languages: LanguageInfo[] };
    languages = body.languages;
  } catch {
    languages = [
      { code: 'ru', name: 'Русский' },
      { code: 'en', name: 'English' },
    ];
  }
  return languages;
}

/** Пакет языка в i18next (один запрос на язык за жизнь вкладки, если не force). */
export function loadLanguage(code: string, force = false): Promise<void> {
  const cached = loaded.get(code);
  if (cached && !force) return cached;
  const p = (async () => {
    const res = await fetch(`/api/i18n/${encodeURIComponent(code)}`, {
      headers: { Accept: 'application/json' },
      cache: 'no-cache',
    });
    if (!res.ok) throw new Error(`i18n ${code}: HTTP ${res.status}`);
    const bundle = (await res.json()) as Record<string, string>;
    i18n.addResourceBundle(code, 'translation', bundle, false, true);
  })();
  loaded.set(code, p);
  p.catch(() => loaded.delete(code));
  return p;
}

/** LocaleResolver::forGuest(): язык браузера среди включённых, иначе en (если включён), иначе ru. */
export function guestLanguage(enabled: readonly string[], preferred: readonly string[] = navigator.languages ?? []): string {
  for (const p of preferred) {
    const lower = p.toLowerCase().replace('_', '-');
    for (const candidate of [lower, lower.split('-')[0] ?? lower]) {
      if (enabled.includes(candidate)) return candidate;
    }
  }
  if (enabled.includes(DEFAULT_GUEST_LANGUAGE)) return DEFAULT_GUEST_LANGUAGE;
  return enabled.includes(DEFAULT_LANGUAGE) ? DEFAULT_LANGUAGE : (enabled[0] ?? DEFAULT_GUEST_LANGUAGE);
}

/** LocaleResolver::forClub(): язык клуба, если включён, иначе ru (или первый включённый). */
export function clubLanguage(clubLocale: string | null | undefined, enabled: readonly string[]): string {
  const code = clubLocale || DEFAULT_LANGUAGE;
  if (enabled.includes(code)) return code;
  return enabled.includes(DEFAULT_LANGUAGE) ? DEFAULT_LANGUAGE : (enabled[0] ?? DEFAULT_LANGUAGE);
}

/** Переключить интерфейс на язык (загрузив пакет). Ошибка загрузки — остаются ключи (русский текст). */
export async function applyLanguage(code: string): Promise<void> {
  try {
    await loadLanguage(code);
  } catch {
    // сервер недоступен — ключи
  }
  if (i18n.language !== code) await i18n.changeLanguage(code);
  document.documentElement.lang = code;
}
