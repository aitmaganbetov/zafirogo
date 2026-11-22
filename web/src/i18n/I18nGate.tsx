import { useEffect, useState, type ReactNode } from 'react';
import { useSession } from '../auth/session';
import i18n from './index';
import { applyLanguage, cachedLanguages, clubLanguage, enabledLanguages, guestLanguage, hasLanguage } from './load';

/**
 * Язык интерфейса (SetLocale + LocaleResolver): вошедший — язык клуба (me.club.locale), гость — язык браузера.
 * Пока первый пакет не загружен, страница не рисуется (без мигания русского текста у kk/en). Если язык и его
 * пакет известны из кэша прошлого захода (i18n/load.ts), страница рисуется сразу, сверка с сервером — в фоне.
 */

/** Язык по кэшу списка языков — без сети; null — кэша нет или пакет этого языка ещё не подключён. */
function cachedChoice(authenticated: boolean, clubLocale: string | null | undefined): string | null {
  const list = cachedLanguages();
  if (!list) return null;
  const enabled = list.map((l) => l.code);
  const code = authenticated && clubLocale ? clubLanguage(clubLocale, enabled) : guestLanguage(enabled);
  return hasLanguage(code) && i18n.language === code ? code : null;
}

export function I18nGate({ children }: { children: ReactNode }) {
  const { status, me } = useSession();
  const [ready, setReady] = useState(false);
  const clubLocale = me?.club?.locale;

  useEffect(() => {
    if (status === 'unknown') return;
    let cancelled = false;
    void (async () => {
      const enabled = (await enabledLanguages()).map((l) => l.code);
      if (cancelled) return;
      const code = status === 'authenticated' && clubLocale ? clubLanguage(clubLocale, enabled) : guestLanguage(enabled);
      // Отменённый эффект (сессия или язык клуба сменились) язык не переключает и в хранилище его не пишет.
      if (await applyLanguage(code, () => cancelled)) setReady(true);
    })();
    return () => {
      cancelled = true;
    };
  }, [status, clubLocale]);

  // До загрузки сессии язык неизвестен; маршруты сами дождутся сессии в beforeLoad.
  if (!ready && status !== 'unknown' && cachedChoice(status === 'authenticated', clubLocale) === null) return null;
  return <>{children}</>;
}
