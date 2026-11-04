/*
 * Регистрация service worker — как resources/js/app.js старого проекта: в защищённом контексте, после load,
 * ошибки молча. Только в prod-сборке (enabled = import.meta.env.PROD).
 */
export function registerServiceWorker(enabled: boolean, win: Window = window): boolean {
  if (!enabled || !('serviceWorker' in win.navigator) || !win.isSecureContext) return false;
  win.addEventListener('load', () => {
    win.navigator.serviceWorker.register('/sw.js').catch(() => {});
  });
  return true;
}
