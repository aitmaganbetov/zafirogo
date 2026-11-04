// Общее для страниц агента: мост к службе (window.chrome.webview ↔ процесс UI ↔ IPC службы), деньги в тиынах,
// переводы, телефон. Без DOM — проверяется тестами Node (agent/tests/frontend). В браузере — window.Zafiro.
//
// Мост: запрос {id, method, params} → ответ {id, result} | {id, error}; события службы — {event: {...}},
// события процесса UI — {local: "..."}. JSON — каноническое отображение proto3 (agent/v1, ipc.proto):
// имена lowerCamelCase, int64 (тиыны, id) — строкой с целым числом, enum — именем, время — RFC 3339.
(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.Zafiro = api;
})(typeof self !== 'undefined' ? self : this, () => {
  'use strict';

  // ---------------------------------------------------------------------------------------------
  // Целые числа и деньги. Суммы — только целые тиыны (1 тенге = 100 тиынов), никаких float.
  // ---------------------------------------------------------------------------------------------

  const INT64 = /^-?\d{1,16}$/;

  /** int64 из JSON proto (строка или число) → безопасное целое; иное — 0. */
  const int64 = (value) => {
    if (typeof value === 'number') return Number.isSafeInteger(value) ? value : 0;
    if (typeof value === 'string' && INT64.test(value)) {
      const n = Number(value);
      return Number.isSafeInteger(n) ? n : 0;
    }
    return 0;
  };

  /** Целая часть и тиыны без деления с плавающей точкой: (abs − abs % 100) / 100 — точное целое. */
  const splitTiyn = (tiyn) => {
    const abs = Math.abs(tiyn);
    const fraction = abs % 100;
    return { negative: tiyn < 0, whole: (abs - fraction) / 100, fraction };
  };

  const decimalSeparators = new Map();
  const decimalSeparator = (intlLocale) => {
    if (!decimalSeparators.has(intlLocale)) {
      const part = new Intl.NumberFormat(intlLocale).formatToParts(1.5).find((p) => p.type === 'decimal');
      decimalSeparators.set(intlLocale, part ? part.value : ',');
    }
    return decimalSeparators.get(intlLocale);
  };

  /**
   * Сумма в тенге для показа: «1 500», «1 500,5», «12,05» — как toLocaleString(…, {maximumFractionDigits: 2})
   * старого агента (лишние нули дробной части не показываются), но из целых тиынов.
   */
  const formatAmount = (tiyn, intlLocale) => {
    const value = int64(tiyn);
    const { negative, whole, fraction } = splitTiyn(value);
    let text = new Intl.NumberFormat(intlLocale, { maximumFractionDigits: 0 }).format(whole);
    if (fraction) text += decimalSeparator(intlLocale) + String(fraction).padStart(2, '0').replace(/0$/, '');
    return (negative ? '-' : '') + text;
  };

  /** Цена с валютой: «1 500 KZT» (formatPrice старого агента). */
  const formatPrice = (tiyn, intlLocale, currency = 'KZT') => `${formatAmount(tiyn, intlLocale)} ${currency || 'KZT'}`;

  /**
   * Сумма перевода из поля ввода («500», «250.50», «250,5») → целые тиыны; иначе null.
   * Формат старого агента: /^\d{1,10}(\.\d{1,2})?$/ после замены запятой на точку (TransferForm в main.js).
   */
  const parseAmount = (input) => {
    const text = String(input ?? '').trim().replace(',', '.');
    const match = /^(\d{1,10})(?:\.(\d{1,2}))?$/.exec(text);
    if (!match) return null;
    const tiyn = Number(match[1]) * 100 + Number((match[2] ?? '').padEnd(2, '0'));
    return tiyn > 0 ? tiyn : null;
  };

  /** floor(a / b) для неотрицательных целых без деления с плавающей точкой. */
  const intDiv = (a, b) => (a - (a % b)) / b;

  /**
   * Цена минут по цене часа (тиыны): floor((hour × minutes + 30) / 60) — только для показа цены введённых минут
   * (списывает сервер). «На весь баланс» и автобиллинг считает сервер (решение Q-008, AG-12).
   * Laravel: app/Services/AgentTariffPurchaseService.php minutesPriceInCents(); Go: billing/pricing.MinutesPrice.
   */
  const minutesPrice = (hourTiyn, minutes) => intDiv(hourTiyn * minutes + 30, 60);

  // ---------------------------------------------------------------------------------------------
  // Переводы: ключ — русский текст; строки службы (agent.GetTranslations) поверх встроенных (i18n/<язык>.json).
  // ---------------------------------------------------------------------------------------------

  const intlLocales = { ru: 'ru-RU', kk: 'kk-KZ', en: 'en-GB' };
  const pluralOrder = ['zero', 'one', 'two', 'few', 'many', 'other'];

  /** Подстановки :name — длинные имена раньше коротких (:count не задевает :countX). */
  const fill = (text, params) => Object.keys(params)
    .sort((a, b) => b.length - a.length)
    .reduce((result, name) => result.split(`:${name}`).join(String(params[name])), text);

  const createI18n = () => {
    const state = { locale: 'ru', builtin: {}, server: {} };
    const intlLocale = () => intlLocales[state.locale] ?? state.locale;
    const line = (key) => state.server[key] || state.builtin[key] || key;
    const t = (key, params = {}) => fill(line(key), params);
    // Формы через «|» в порядке категорий Intl.PluralRules: ru — one|few|many, en — one|other, kk — одна форма.
    const tc = (key, count, params = {}) => {
      const forms = line(key).split('|');
      const rules = new Intl.PluralRules(intlLocale());
      const order = pluralOrder.filter((category) => rules.resolvedOptions().pluralCategories.includes(category));
      const index = Math.max(0, Math.min(order.indexOf(rules.select(count)), forms.length - 1));
      return fill(forms[index], { count, ...params });
    };
    return {
      t,
      tc,
      intlLocale,
      get locale() { return state.locale; },
      setLocale(locale, builtin = {}, server = {}) {
        state.locale = typeof locale === 'string' && /^[a-z]{2,8}$/.test(locale) ? locale : 'ru';
        state.builtin = builtin && typeof builtin === 'object' ? builtin : {};
        state.server = server && typeof server === 'object' ? server : {};
      },
      setServerLines(lines) {
        state.server = lines && typeof lines === 'object' ? lines : {};
      }
    };
  };

  // ---------------------------------------------------------------------------------------------
  // Мост к службе.
  // ---------------------------------------------------------------------------------------------

  /** Ошибка вызова: код контракта (ERROR_CODE_*), текст сервера (LocalizedMessage) или пусто. */
  class BridgeError extends Error {
    constructor(error) {
      super(typeof error?.message === 'string' ? error.message : '');
      this.name = 'BridgeError';
      this.code = typeof error?.errorCode === 'string' ? error.errorCode : 'ERROR_CODE_UNSPECIFIED';
      this.grpcStatus = Number(error?.grpcStatus) || 0;
      this.retryAfterMs = int64(error?.retryAfterMs);
      this.violations = Array.isArray(error?.violations) ? error.violations : [];
    }

    /** Текст для показа: сообщение сервера как есть, иначе перевод запасного ключа. */
    text(t, fallbackKey = 'Не удалось выполнить действие.') {
      return this.message || t(fallbackKey);
    }
  }

  const DEFAULT_TIMEOUT_MS = 70000;
  const LONG_METHODS = { 'GuestAuthService/WatchQrLogin': 11 * 60 * 1000 };

  /**
   * Мост поверх window.chrome.webview. call(method, params) → Promise результата или BridgeError;
   * onEvent(fn) — события службы ({snapshot}, {showMessage}, …), onLocal(fn) — события процесса UI.
   */
  const createBridge = (webview, { setTimer = setTimeout, clearTimer = clearTimeout } = {}) => {
    const pending = new Map();
    const eventHandlers = [];
    const localHandlers = [];
    let nextId = 1;

    if (webview) {
      webview.addEventListener('message', (message) => {
        const data = message?.data;
        if (!data || typeof data !== 'object') return;
        if (data.event && typeof data.event === 'object') {
          eventHandlers.forEach((fn) => fn(data.event));
          return;
        }
        if (typeof data.local === 'string') {
          localHandlers.forEach((fn) => fn(data.local));
          return;
        }
        const entry = pending.get(data.id);
        if (!entry) return;
        pending.delete(data.id);
        clearTimer(entry.timer);
        if (data.error) entry.reject(new BridgeError(data.error));
        else entry.resolve(data.result ?? {});
      });
    }

    const call = (method, params = {}) => new Promise((resolve, reject) => {
      if (!webview) {
        reject(new BridgeError({ errorCode: 'ERROR_CODE_UNSPECIFIED', grpcStatus: 14 }));
        return;
      }
      const id = nextId++;
      const timer = setTimer(() => {
        if (!pending.delete(id)) return;
        reject(new BridgeError({ errorCode: 'ERROR_CODE_UNSPECIFIED', grpcStatus: 4 }));
      }, LONG_METHODS[method] ?? DEFAULT_TIMEOUT_MS);
      pending.set(id, { resolve, reject, timer });
      webview.postMessage({ id, method, params });
    });

    return {
      available: Boolean(webview),
      call,
      onEvent: (fn) => eventHandlers.push(fn),
      onLocal: (fn) => localHandlers.push(fn),
      get pendingCount() { return pending.size; }
    };
  };

  // ---------------------------------------------------------------------------------------------
  // Телефон (вход, регистрация, друзья): Казахстан, +7 и 10 цифр.
  // Перенесено из старого агента: Services/PhoneNumberFormatter.cs.
  // ---------------------------------------------------------------------------------------------

  const phoneDigits = (input) => {
    let digits = String(input ?? '').replace(/\D+/g, '');
    if (digits.length === 11 && (digits[0] === '7' || digits[0] === '8')) digits = digits.slice(1);
    return digits;
  };

  /** «+7XXXXXXXXXX» или null (NormalizeKazakhstanNumber). */
  const normalizePhone = (input) => {
    const digits = phoneDigits(input);
    return digits.length === 10 ? `+7${digits}` : null;
  };

  /** Цифры после «+7» по мере ввода: «700 123-45-67» (FormatLocal). */
  const formatLocalPhone = (input) => {
    const digits = phoneDigits(input).slice(0, 10);
    let text = '';
    for (let i = 0; i < digits.length; i++) {
      if (i === 3) text += ' ';
      else if (i === 6 || i === 8) text += '-';
      text += digits[i];
    }
    return text;
  };

  /** Длительность proto JSON («30s», «1.5s») → целые секунды; иное — 0. */
  const durationSeconds = (value) => {
    const match = /^(\d{1,9})(?:\.\d+)?s$/.exec(String(value ?? ''));
    return match ? Number(match[1]) : 0;
  };

  /** Время proto JSON (RFC 3339) → мс от эпохи или null. */
  const timestampMs = (value) => {
    if (typeof value !== 'string' || !value) return null;
    const ms = Date.parse(value);
    return Number.isNaN(ms) ? null : ms;
  };

  return {
    int64,
    formatAmount,
    formatPrice,
    parseAmount,
    minutesPrice,
    fill,
    createI18n,
    intlLocales,
    BridgeError,
    createBridge,
    normalizePhone,
    formatLocalPhone,
    durationSeconds,
    timestampMs
  };
});
