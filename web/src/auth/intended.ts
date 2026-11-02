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
    if (v && isSameSitePath(v)) return v;
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

/**
 * Только путь этого сайта (SEC-020, docs/security-review.md): «//evil» и «/\evil» браузер разбирает как адрес
 * другого хоста (обратная косая в http(s) — та же косая), поэтому проверка — разбором URL, а не по префиксу.
 */
export function isSameSitePath(v: string): boolean {
  if (!v.startsWith('/') || v.includes('\\') || [...v].some((c) => c.charCodeAt(0) < 0x20)) return false;
  try {
    const base = 'https://zafiro.invalid';
    return new URL(v, base).origin === base;
  } catch {
    return false;
  }
}
