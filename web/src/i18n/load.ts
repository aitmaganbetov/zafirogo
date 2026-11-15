import i18n, { DEFAULT_GUEST_LANGUAGE, DEFAULT_LANGUAGE } from './index';

/*
 * Загрузка переводов с сервера (api/i18n.yaml).
 *
 * Скорость первого экрана (FE-04):
 *  - список языков и пакет запрашиваются сразу при старте (warmI18n из main.tsx) — параллельно с обновлением
 *    сессии и данными страницы, а не после них;
 *  - пакет, список языков и последний язык хранятся в localStorage с версией переводов (`version` из
 *    GET /api/i18n): при повторном заходе интерфейс рисуется сразу из кэша, пакет перезапрашивается только когда
 *    версия изменилась (правка в суперадминке). Кэш — только ускорение: нет доступа к localStorage, запись
 *    повреждена или устарела — работает как без него (откат к загрузке с сервера);
 *  - язык-источник ru не зависит от пакета: ключа нет — показывается сам ключ с подстановками и нужной формой
 *    числа (missing.ts), поэтому пакет без записей «ключ = значение» достаточен.
 */

export interface LanguageInfo {
  readonly code: string;
  readonly name: string;
}

type Bundle = Record<string, string>;

interface CachedPack {
  readonly version: string;
  readonly bundle: Bundle;
}

const KEY_STATE = 'zafiro.i18n.state';
const keyPack = (code: string) => `zafiro.i18n.pack.${code}`;
/** Пакет больше этого в localStorage не кладётся (квота ~5 МБ на origin, UTF-16). */
const MAX_CACHED_CHARS = 1_000_000;

const FALLBACK_LANGUAGES: readonly LanguageInfo[] = [
  { code: 'ru', name: 'Русский' },
  { code: 'en', name: 'English' },
];

let languages: readonly LanguageInfo[] | null = null;
let version: string | null = null;
let languagesRequest: Promise<readonly LanguageInfo[]> | null = null;
const loaded = new Map<string, Promise<void>>();
/** Версия пакета, уже добавленного в i18next (из кэша или с сервера). */
const applied = new Map<string, string>();

function readJSON<T>(key: string): T | null {
  try {
    const raw = window.localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : null;
  } catch {
    return null;
  }
}

function writeJSON(key: string, value: unknown): void {
  try {
    const raw = JSON.stringify(value);
    if (raw.length > MAX_CACHED_CHARS) return;
    window.localStorage.setItem(key, raw);
  } catch {
    // нет доступа или квота — кэш не обязателен
  }
}

function dropKey(key: string): void {
  try {
    window.localStorage.removeItem(key);
  } catch {
    // нет доступа
  }
}

function isLanguages(v: unknown): v is LanguageInfo[] {
  return Array.isArray(v) && v.length > 0 && v.every((l) => typeof (l as LanguageInfo | null)?.code === 'string' && typeof (l as LanguageInfo).name === 'string');
}

function isBundle(v: unknown): v is Bundle {
  return v !== null && typeof v === 'object' && !Array.isArray(v) && Object.values(v).every((x) => typeof x === 'string');
}

interface CachedState {
  readonly lang?: string;
  readonly languages?: LanguageInfo[];
}

function cachedState(): CachedState {
  const st = readJSON<CachedState>(KEY_STATE);
  if (!st || typeof st !== 'object') return {};
  return { lang: typeof st.lang === 'string' ? st.lang : undefined, languages: isLanguages(st.languages) ? st.languages : undefined };
}

function cachedPack(code: string): CachedPack | null {
  const p = readJSON<CachedPack>(keyPack(code));
  if (!p) return null;
  if (typeof p.version !== 'string' || !isBundle(p.bundle)) {
    dropKey(keyPack(code)); // повреждённая запись — откат к загрузке с сервера
    return null;
  }
  return p;
}

function addBundle(code: string, bundle: Bundle, packVersion: string): void {
  // Пакет заменяется целиком: ключи, удалённые в суперадминке, не остаются от старой версии.
  if (i18n.hasResourceBundle(code, 'translation')) i18n.removeResourceBundle(code, 'translation');
  i18n.addResourceBundle(code, 'translation', bundle, false, true);
  applied.set(code, packVersion);
}

/** Включённые языки из кэша прошлого захода (до ответа сервера); нет — null. */
export function cachedLanguages(): readonly LanguageInfo[] | null {
  return languages ?? cachedState().languages ?? null;
}

/** Есть ли в i18next пакет языка (из кэша или с сервера). */
export function hasLanguage(code: string): boolean {
  return applied.has(code);
}

/** Включённые языки (TranslationCatalog::enabledCodes()); при ошибке — из кэша, иначе ru и en. */
export function enabledLanguages(): Promise<readonly LanguageInfo[]> {
  if (languages) return Promise.resolve(languages);
  languagesRequest ??= (async () => {
    try {
      const res = await fetch('/api/i18n', { headers: { Accept: 'application/json' } });
      if (!res.ok) throw new Error(String(res.status));
      const body = (await res.json()) as { languages: LanguageInfo[]; version?: string };
      if (!isLanguages(body.languages)) throw new Error('i18n: bad language list');
      languages = body.languages;
      version = typeof body.version === 'string' ? body.version : null;
      writeJSON(KEY_STATE, { ...cachedState(), languages });
    } catch {
      languages = cachedState().languages ?? FALLBACK_LANGUAGES;
    }
    return languages;
  })();
  return languagesRequest;
}

async function fetchPack(code: string): Promise<void> {
  // Версия в адресе: сервер может отдавать пакет с долгим Cache-Control; без версии — как раньше (ETag).
  const url = `/api/i18n/${encodeURIComponent(code)}${version ? `?v=${encodeURIComponent(version)}` : ''}`;
  const res = await fetch(url, { headers: { Accept: 'application/json' } });
  if (!res.ok) throw new Error(`i18n ${code}: HTTP ${res.status}`);
  const bundle: unknown = await res.json();
  if (!isBundle(bundle)) throw new Error(`i18n ${code}: bad pack`);
  const packVersion = version ?? '';
  addBundle(code, bundle, packVersion);
  if (version) writeJSON(keyPack(code), { version, bundle } satisfies CachedPack);
}

/**
 * Пакет языка в i18next (один запрос на язык за жизнь вкладки, если не force). Пакет из кэша той же версии,
 * что на сервере, не перезапрашивается; другой версии — показывается сразу и обновляется в фоне.
 */
export function loadLanguage(code: string, force = false): Promise<void> {
  const cached = loaded.get(code);
  if (cached && !force) return cached;
  const p = (async () => {
    if (!force && !applied.has(code)) {
      const pack = cachedPack(code);
      if (pack) addBundle(code, pack.bundle, pack.version);
    }
    if (!force && applied.has(code)) {
      // Есть что показать: сверка версии — в фоне, страницу не задерживает.
      void enabledLanguages().then(() => {
        if (version !== null && applied.get(code) === version) return;
        return fetchPack(code).catch(() => undefined);
      });
      return;
    }
    await enabledLanguages();
    await fetchPack(code);
  })();
  loaded.set(code, p);
  p.catch(() => loaded.delete(code));
  return p;
}

/**
 * Старт загрузки переводов до первого рендера (main.tsx): список языков и пакет последнего языка идут параллельно
 * с обновлением сессии. Пакет из кэша подключается синхронно — повторный заход рисуется без ожидания сети.
 */
export function warmI18n(): void {
  const st = cachedState();
  if (st.lang) {
    const pack = cachedPack(st.lang);
    if (pack) {
      addBundle(st.lang, pack.bundle, pack.version);
      if (i18n.language !== st.lang) void i18n.changeLanguage(st.lang);
      document.documentElement.lang = st.lang;
    }
    void loadLanguage(st.lang).catch(() => undefined);
    return;
  }
  // Первый заход: язык гостя известен после списка языков — пакет запрашивается сразу за ним, не дожидаясь сессии.
  void enabledLanguages().then((list) => loadLanguage(guestLanguage(list.map((l) => l.code))).catch(() => undefined));
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
  writeJSON(KEY_STATE, { ...cachedState(), lang: code });
}

/** Сбросить состояние модуля (тесты). */
export function resetI18nForTests(): void {
  languages = null;
  version = null;
  languagesRequest = null;
  loaded.clear();
  applied.clear();
}
