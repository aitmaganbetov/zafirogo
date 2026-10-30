import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { ClubTime } from './types';

/*
 * Часы клуба в верхней панели — перенос <time data-clock> из layouts/admin.blade.php
 * и скрипта часов из resources/js/app.js: время в зоне клуба, с поправкой на серверные часы
 * (неверные часы устройства не мешают), обновление ровно на границе секунды.
 */

function formatter(locale: string, timeZone: string, options: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  try {
    return new Intl.DateTimeFormat(locale || 'ru', { timeZone, ...options });
  } catch {
    return new Intl.DateTimeFormat('ru', { timeZone, ...options });
  }
}

/** Смещение зоны как «+05:00» (Carbon format('P')). */
function utcOffset(timeZone: string, at: Date): string {
  try {
    const part = new Intl.DateTimeFormat('en-US', { timeZone, timeZoneName: 'longOffset' })
      .formatToParts(at)
      .find((item) => item.type === 'timeZoneName')?.value;
    const offset = part?.replace('GMT', '') ?? '';
    return offset === '' ? '+00:00' : offset;
  } catch {
    return '';
  }
}

export function ClubClock({ clubTime }: { readonly clubTime: ClubTime }) {
  const { t, i18n } = useTranslation();
  const { timezone, nowMs } = clubTime;
  const [now, setNow] = useState(() => new Date(nowMs));

  useEffect(() => {
    // Поправка: разница между серверным временем и часами устройства на момент получения данных.
    const skew = nowMs - Date.now();
    let timer = 0;
    // Следующее обновление — сразу после границы секунды (+5 мс), как в resources/js/app.js.
    const schedule = () => {
      const current = Date.now() + skew;
      timer = window.setTimeout(() => {
        setNow(new Date(Date.now() + skew));
        schedule();
      }, 1000 - (current % 1000) + 5);
    };
    schedule();
    return () => { window.clearTimeout(timer); };
  }, [nowMs]);

  const locale = i18n.language;
  const formats = useMemo(
    () => ({
      hoursMinutes: formatter(locale, timezone, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }),
      seconds: formatter(locale, timezone, { second: '2-digit' }),
      date: formatter(locale, timezone, { weekday: 'short', day: 'numeric', month: 'short' }),
    }),
    [locale, timezone],
  );

  return (
    <time
      className="admin-clock"
      data-clock
      dateTime={now.toISOString()}
      title={`${t('Время клуба')}: ${timezone} (UTC${utcOffset(timezone, now)})`}
    >
      <span className="admin-clock-time">
        <span data-clock-hm>{formats.hoursMinutes.format(now)}</span>
        <span className="admin-clock-seconds" data-clock-seconds>
          :{formats.seconds.format(now).padStart(2, '0')}
        </span>
      </span>
      <span className="admin-clock-date" data-clock-date>
        {formats.date.format(now)}
      </span>
    </time>
  );
}
