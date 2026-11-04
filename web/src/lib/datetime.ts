/*
 * Даты в часовом поясе клуба (clubs.timezone, me.club.timezone) — как Carbon в старых шаблонах
 * ($x->timezone($club->timezone)->format('d.m.Y H:i')). Сервер отдаёт время в RFC 3339 (UTC).
 */

type Parts = Record<'year' | 'month' | 'day' | 'hour' | 'minute' | 'second' | 'weekday', string>;

const cache = new Map<string, Intl.DateTimeFormat>();

function partsIn(date: Date, timeZone: string): Parts {
  let f = cache.get(timeZone);
  if (!f) {
    f = new Intl.DateTimeFormat('en-US', {
      timeZone,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      weekday: 'short',
      hourCycle: 'h23',
    });
    cache.set(timeZone, f);
  }
  const out: Partial<Parts> = {};
  for (const p of f.formatToParts(date)) {
    if (p.type in { year: 1, month: 1, day: 1, hour: 1, minute: 1, second: 1, weekday: 1 }) out[p.type as keyof Parts] = p.value;
  }
  return out as Parts;
}

function toDate(v: Date | string | number): Date {
  return v instanceof Date ? v : new Date(v);
}

/**
 * Формат даты в поясе клуба по токенам PHP date(): d, j, m, n, Y, y, H, G, i, s. Прочие символы — как есть;
 * «\» экранирует следующий символ (как в PHP).
 */
export function formatDate(value: Date | string | number, timeZone: string, format = 'd.m.Y H:i'): string {
  const p = partsIn(toDate(value), timeZone);
  let out = '';
  for (let i = 0; i < format.length; i++) {
    const c = format[i];
    switch (c) {
      case 'd':
        out += p.day;
        break;
      case 'j':
        out += String(Number(p.day));
        break;
      case 'm':
        out += p.month;
        break;
      case 'n':
        out += String(Number(p.month));
        break;
      case 'Y':
        out += p.year;
        break;
      case 'y':
        out += p.year.slice(-2);
        break;
      case 'H':
        out += p.hour;
        break;
      case 'G':
        out += String(Number(p.hour));
        break;
      case 'i':
        out += p.minute;
        break;
      case 's':
        out += p.second;
        break;
      case '\\':
        i++;
        out += format[i] ?? '';
        break;
      default:
        out += c;
    }
  }
  return out;
}

/** Локальная дата клуба «YYYY-MM-DD» (Carbon::now($tz)->toDateString()) — для фильтров ?date=. */
export function clubDate(value: Date | string | number, timeZone: string): string {
  return formatDate(value, timeZone, 'Y-m-d');
}

/** Сдвиг даты «YYYY-MM-DD» на n дней (кнопки пред./след. день). */
export function addDays(date: string, n: number): string {
  const [y, m, d] = date.split('-').map(Number);
  const t = new Date(Date.UTC(y ?? 1970, (m ?? 1) - 1, (d ?? 1) + n));
  return t.toISOString().slice(0, 10);
}

/**
 * Часы с поправкой на неверные часы устройства: смещение = время сервера − время устройства в момент
 * получения me (server_time). Возвращает «сейчас» по серверу.
 */
export function serverNow(serverTimeMs: number, receivedAtMs: number, nowMs: number = Date.now()): number {
  return nowMs + (serverTimeMs - receivedAtMs);
}
