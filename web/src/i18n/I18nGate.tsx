import { useEffect, useState, type ReactNode } from 'react';
import { useSession } from '../auth/session';
import { applyLanguage, clubLanguage, enabledLanguages, guestLanguage } from './load';

/**
 * Язык интерфейса (SetLocale + LocaleResolver): вошедший — язык клуба (me.club.locale), гость — язык браузера.
 * Пока первый пакет не загружен, страница не рисуется (без мигания русского текста у kk/en).
 */
export function I18nGate({ children }: { children: ReactNode }) {
  const { status, me } = useSession();
  const [ready, setReady] = useState(false);
  const clubLocale = me?.club?.locale;

  useEffect(() => {
    if (status === 'unknown') return;
    let cancelled = false;
    void (async () => {
      const enabled = (await enabledLanguages()).map((l) => l.code);
      const code = status === 'authenticated' && clubLocale ? clubLanguage(clubLocale, enabled) : guestLanguage(enabled);
      await applyLanguage(code);
      if (!cancelled) setReady(true);
    })();
    return () => {
      cancelled = true;
    };
  }, [status, clubLocale]);

  // До загрузки сессии язык неизвестен; маршруты сами дождутся сессии в beforeLoad.
  if (!ready && status !== 'unknown') return null;
  return <>{children}</>;
}
