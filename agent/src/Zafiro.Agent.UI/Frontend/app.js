// Оболочка страницы агента: мост к службе, снимок состояния, переводы, какой экран видим.
// Экран выбирает служба (Snapshot.screen): SETUP — регистрация ПК, LOCK без гостя — вход, иначе — лаунчер гостя.
// main.js (лаунчер) и lock.js (вход, регистрация ПК, PIN сотрудника) подписываются на window.ZafiroApp.
(() => {
  'use strict';
  const Z = window.Zafiro;
  const i18n = Z.createI18n();
  const bridge = Z.createBridge(window.chrome?.webview);
  const snapshotHandlers = [];
  const eventHandlers = [];
  const languageHandlers = [];
  const localHandlers = [];
  let snapshot = null;
  // Язык, выбранный гостем; после его выхода — язык клуба (как L.SetUserLocale(null) старого агента).
  let chosenLocale = null;
  let loadedVersion = -1;
  const builtinLines = new Map();
  const serverLines = new Map();

  const languages = () => (Array.isArray(snapshot?.config?.languages) ? snapshot.config.languages : [])
    .filter((language) => language && typeof language.code === 'string' && /^[a-z]{2,8}$/.test(language.code))
    .map((language) => ({ code: language.code, name: language.name || language.code }));
  const effectiveLocale = () => chosenLocale ?? snapshot?.config?.defaultLocale ?? 'ru';

  // Встроенные тексты (файлы i18n/<язык>.json рядом со страницей): на них показывается всё, пока строк сервера нет.
  const loadBuiltin = async (locale) => {
    if (builtinLines.has(locale)) return builtinLines.get(locale);
    let lines = {};
    try {
      const response = await fetch(`i18n/${locale}.json`);
      if (response.ok) {
        const data = await response.json();
        if (data && typeof data.lines === 'object') lines = data.lines;
      }
    } catch {
      lines = {};
    }
    builtinLines.set(locale, lines);
    return lines;
  };

  // Строки сервера (agent.GetTranslations через службу) — только когда версия новее загруженной.
  const loadTranslations = async (locale) => {
    const version = Z.int64(snapshot?.translationsVersion);
    const builtin = await loadBuiltin(locale);
    let server = serverLines.get(locale);
    if (bridge.available && (!server || server.version !== version)) {
      try {
        const result = await bridge.call('agent.GetTranslations', { locale, knownVersion: String(server?.version ?? 0) });
        server = { version: Z.int64(result.version), lines: result.notModified && server ? server.lines : (result.lines ?? {}) };
        serverLines.set(locale, server);
      } catch {
        // Язык без каталога на сервере, нет связи — встроенные тексты.
      }
    }
    if (locale !== effectiveLocale()) return;
    loadedVersion = version;
    i18n.setLocale(locale, builtin, server?.lines ?? {});
    languageHandlers.forEach((fn) => fn());
  };

  const show = (id, visible) => {
    const element = document.getElementById(id);
    if (element) element.hidden = !visible;
  };

  // Какой экран видим: решает служба (screen), страница только показывает.
  const showScreen = () => {
    const screen = snapshot?.screen;
    const setup = screen === 'SCREEN_SETUP';
    const login = screen === 'SCREEN_LOCK' && !snapshot?.guest;
    const launcher = Boolean(snapshot) && !setup && !login;
    show('lockScreen', setup || login);
    show('app', launcher);
    document.body.dataset.screen = setup ? 'setup' : login ? 'login' : launcher ? 'launcher' : '';
  };

  const applySnapshot = (next) => {
    const previous = snapshot;
    snapshot = next;
    if (previous?.guest && !next.guest) chosenLocale = null;
    showScreen();
    const locale = effectiveLocale();
    if (locale !== i18n.locale || Z.int64(next.translationsVersion) !== loadedVersion) loadTranslations(locale);
    snapshotHandlers.forEach((fn) => fn(next, previous));
  };

  bridge.onEvent((event) => {
    if (event.snapshot) {
      applySnapshot(event.snapshot);
      return;
    }
    if (event.translationsUpdated) {
      loadTranslations(effectiveLocale());
      return;
    }
    eventHandlers.forEach((fn) => fn(event));
  });

  bridge.onLocal((name) => {
    if (name === 'serviceDisconnected') show('serviceOverlay', true);
    if (name === 'serviceConnected') {
      show('serviceOverlay', false);
      bridge.call('agent.GetSnapshot').then(applySnapshot).catch(() => {});
    }
    localHandlers.forEach((fn) => fn(name));
  });

  window.ZafiroApp = {
    i18n,
    bridge,
    snapshot: () => snapshot,
    languages,
    setLanguage(code) {
      if (!languages().some((language) => language.code === code)) return;
      chosenLocale = code;
      loadTranslations(code);
    },
    onSnapshot: (fn) => snapshotHandlers.push(fn),
    onEvent: (fn) => eventHandlers.push(fn),
    onLanguage: (fn) => languageHandlers.push(fn),
    onLocal: (fn) => localHandlers.push(fn)
  };

  loadTranslations('ru');
  // Снимок приходит событием сразу после подключения; запрос — на случай, если страница загрузилась позже.
  window.addEventListener('DOMContentLoaded', () => {
    bridge.call('agent.GetSnapshot').then((snap) => {
      if (!snapshot) applySnapshot(snap);
    }).catch(() => {});
  });
})();
