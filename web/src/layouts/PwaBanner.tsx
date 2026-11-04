import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { STORAGE_KEYS, readStorage, writeStorage } from './storage';

/*
 * Предложение установить приложение — перенос .pwa-banner из layouts/admin.blade.php и логики
 * из resources/js/app.js: Chromium отдаёт событие beforeinstallprompt (кнопка «Установить»),
 * Safari на iOS и Samsung Internet получают короткую подсказку. «Не сейчас» скрывает баннер
 * на 30 дней; установленное приложение баннер не показывает.
 * Регистрация service worker (/sw.js) — в точке входа приложения, см. layouts/README.md.
 */

interface BeforeInstallPromptEvent extends Event {
  prompt: () => Promise<void>;
  readonly userChoice: Promise<{ outcome: 'accepted' | 'dismissed'; platform: string }>;
}

type Hint = 'prompt' | 'ios' | 'samsung';

const DISMISS_DAYS = 30;
const DAY_MS = 864e5;

function isInstalled(): boolean {
  const nav = navigator as Navigator & { standalone?: boolean };
  return window.matchMedia('(display-mode: standalone)').matches || nav.standalone === true;
}

function recentlyDismissed(): boolean {
  return Date.now() - Number(readStorage(STORAGE_KEYS.installDismissedAt) ?? 0) < DISMISS_DAYS * DAY_MS;
}

function manualHint(): Hint | null {
  const ua = navigator.userAgent;
  if (/iPhone|iPad|iPod/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1)) {
    return /CriOS|FxiOS|EdgiOS/.test(ua) ? null : 'ios';
  }
  if (/SamsungBrowser/.test(ua)) return 'samsung';
  return null;
}

export function PwaBanner() {
  const { t } = useTranslation();
  const [hint, setHint] = useState<Hint | null>(null);
  const [installPrompt, setInstallPrompt] = useState<BeforeInstallPromptEvent | null>(null);

  useEffect(() => {
    const show = (next: Hint) => {
      if (isInstalled() || recentlyDismissed()) return;
      setHint(next);
    };
    const onPrompt = (event: Event) => {
      event.preventDefault(); // наш баннер заменяет собственный баннер браузера
      setInstallPrompt(event as BeforeInstallPromptEvent);
      show('prompt');
    };
    const onInstalled = () => {
      setInstallPrompt(null);
      setHint(null);
    };
    window.addEventListener('beforeinstallprompt', onPrompt);
    window.addEventListener('appinstalled', onInstalled);
    const manual = manualHint();
    const timer = manual === null ? 0 : window.setTimeout(() => { show(manual); }, 1500);
    return () => {
      window.removeEventListener('beforeinstallprompt', onPrompt);
      window.removeEventListener('appinstalled', onInstalled);
      window.clearTimeout(timer);
    };
  }, []);

  const accept = async () => {
    if (installPrompt === null) return;
    const prompt = installPrompt;
    setInstallPrompt(null);
    setHint(null);
    await prompt.prompt();
    await prompt.userChoice;
  };

  const dismiss = () => {
    writeStorage(STORAGE_KEYS.installDismissedAt, String(Date.now()));
    setHint(null);
  };

  return (
    <div className="pwa-banner" data-pwa-banner role="region" aria-label={t('Установите Zafiro как приложение')} hidden={hint === null}>
      <img src="/icons/icon-192.png" alt="" width={44} height={44} />
      <div className="pwa-banner-text">
        <strong>{t('Установите Zafiro как приложение')}</strong>
        <p data-pwa-hint="prompt" hidden={hint !== 'prompt'}>
          {t('Запуск с главного экрана, в отдельном окне.')}
        </p>
        <p data-pwa-hint="ios" hidden={hint !== 'ios'}>
          {t('В Safari нажмите «Поделиться», затем «На экран „Домой“».')}
        </p>
        <p data-pwa-hint="samsung" hidden={hint !== 'samsung'}>
          {t('Нажмите значок установки в адресной строке или откройте меню браузера и выберите «Добавить страницу на» → «Главный экран».')}
        </p>
      </div>
      <div className="pwa-banner-actions">
        <button type="button" className="brand-button" data-pwa-accept hidden={hint !== 'prompt'} onClick={() => void accept()}>
          {t('Установить')}
        </button>
        <button type="button" className="pwa-banner-dismiss" data-pwa-dismiss onClick={dismiss}>
          {t('Не сейчас')}
        </button>
      </div>
    </div>
  );
}
