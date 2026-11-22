/*
 * Язык для Intl (даты, числа). В Chromium (Chrome, Edge, WebView2) урезанный ICU: для kk данных CLDR нет, и Intl
 * молча отдаёт корневой формат — «M10 15, Thu», «123,456.78» (аудит 22.11.2026, UI-8; так было и в старом
 * resources/js/app.js). Тогда берётся ru: те же разделители (пробел тысяч, запятая дроби) и кириллица.
 * Проверка — по самому формату (supportedLocalesOf в Chromium kk «поддерживает»): корневой формат пишет месяц как «M11».
 */

const cache = new Map<string, string>();

function hasLocaleData(locale: string): boolean {
  try {
    const month = new Intl.DateTimeFormat(locale, { month: 'short', timeZone: 'UTC' })
      .formatToParts(new Date(Date.UTC(2026, 10, 15)))
      .find((p) => p.type === 'month')?.value;
    return month !== undefined && !/^M\d+$/.test(month);
  } catch {
    return false;
  }
}

/** locale, если у Intl есть его данные, иначе «ru». Пустой — «ru». */
export function intlLocale(locale: string | null | undefined): string {
  if (!locale) return 'ru';
  let v = cache.get(locale);
  if (v === undefined) {
    v = hasLocaleData(locale) ? locale : 'ru';
    cache.set(locale, v);
  }
  return v;
}
