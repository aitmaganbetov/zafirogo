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

/** Сдвиг даты «YYYY-MM-DD» на n дней (кнопки пред./след. день). */
export function addDays(date: string, n: number): string {
  const [y, m, d] = date.split('-').map(Number);
  const t = new Date(Date.UTC(y ?? 1970, (m ?? 1) - 1, (d ?? 1) + n));
  return t.toISOString().slice(0, 10);
}

/**
 * Значение поля `datetime-local` («2026-11-20T18:30», время на часах клуба) → момент в RFC 3339 (UTC).
 * Пояс браузера не участвует: администратор в другом поясе вводит и видит время клуба (как Carbon::parse($v, $tz)).
 * Несуществующее время (переход на летнее) сдвигается вперёд, как в PHP; пустая или некорректная строка — null.
 */
export function clubLocalToISO(local: string, timeZone: string): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(local.trim());
  if (!m) return null;
  const wall = Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]), Number(m[4]), Number(m[5]), Number(m[6] ?? 0));
  if (Number.isNaN(wall)) return null;
  // Смещение пояса в момент t: «время на часах клуба как UTC» минус t.
  const offsetAt = (t: number) => {
    const p = partsIn(new Date(t), timeZone);
    return Date.UTC(Number(p.year), Number(p.month) - 1, Number(p.day), Number(p.hour), Number(p.minute), Number(p.second)) - t;
  };
  let utc = wall - offsetAt(wall);
  utc = wall - offsetAt(utc); // уточнение у границы перевода часов
  if (utc + offsetAt(utc) !== wall) {
    // Время в разрыве перевода часов: PHP берёт смещение до перевода, и время уходит вперёд (02:30 → 03:30)
    // в любом полушарии. Сутки назад — гарантированно до перевода (переводы реже раза в двое суток).
    utc = wall - offsetAt(wall - 86_400_000);
  }
  return new Date(utc).toISOString();
}
