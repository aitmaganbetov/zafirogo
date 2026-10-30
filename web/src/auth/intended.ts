/*
 * redirect()->intended(): куда вернуть после входа. В Laravel — url.intended в сессии; здесь — sessionStorage,
 * чтобы адрес страницы входа остался прежним (/login без параметров).
 */

const KEY = 'zafiro.intended';

export function rememberIntended(href: string): void {
  try {
    sessionStorage.setItem(KEY, href);
  } catch {
    // нет хранилища — после входа на /dashboard
  }
}

/** Сохранённый адрес (только путь этого сайта) или fallback; читается один раз. */
export function takeIntended(fallback: string): string {
  try {
    const v = sessionStorage.getItem(KEY);
    sessionStorage.removeItem(KEY);
    if (v && v.startsWith('/') && !v.startsWith('//')) return v;
  } catch {
    // нет хранилища
  }
  return fallback;
}

export function peekIntended(): string | null {
  try {
    return sessionStorage.getItem(KEY);
  } catch {
    return null;
  }
}
