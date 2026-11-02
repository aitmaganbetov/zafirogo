// Тесты общего модуля страницы агента (Frontend/core.js): деньги в тиынах, формулы цены минут, переводы, мост к
// службе, телефон. Без WebView2 и без зависимостей: node --test agent/tests/frontend (Node ≥ 20).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const Z = require('../../src/Zafiro.Agent.UI/Frontend/core.js');

// Пробелы групп разрядов в Intl — неразрывные (U+00A0 / U+202F): сравниваем без них.
const plain = (s) => s.replace(/[  ]/g, ' ');

test('int64: строки и числа proto JSON → безопасные целые', () => {
  assert.equal(Z.int64('150050'), 150050);
  assert.equal(Z.int64('-25'), -25);
  assert.equal(Z.int64(42), 42);
  assert.equal(Z.int64('9007199254740993'), 0); // больше Number.MAX_SAFE_INTEGER — не угадываем
  assert.equal(Z.int64('12.5'), 0);
  assert.equal(Z.int64(1.5), 0);
  assert.equal(Z.int64(undefined), 0);
  assert.equal(Z.int64('1e5'), 0);
});

test('formatAmount: целые тиыны → тенге без float, лишние нули дробной части не показываются', () => {
  assert.equal(plain(Z.formatAmount('150000', 'ru-RU')), '1 500');
  assert.equal(plain(Z.formatAmount('150050', 'ru-RU')), '1 500,5');
  assert.equal(plain(Z.formatAmount('150005', 'ru-RU')), '1 500,05');
  assert.equal(plain(Z.formatAmount(1205, 'en-GB')), '12.05');
  assert.equal(plain(Z.formatAmount(-99, 'ru-RU')), '-0,99');
  assert.equal(plain(Z.formatAmount(0, 'ru-RU')), '0');
  // Наибольшее безопасное целое: деление без потери точности.
  assert.equal(plain(Z.formatAmount('9007199254740991', 'en-GB')), '90,071,992,547,409.91');
  assert.equal(plain(Z.formatPrice('250000', 'ru-RU', 'KZT')), '2 500 KZT');
});

test('parseAmount: сумма перевода из поля ввода → тиыны или null', () => {
  assert.equal(Z.parseAmount('500'), 50000);
  assert.equal(Z.parseAmount('250.50'), 25050);
  assert.equal(Z.parseAmount('250,5'), 25050);
  assert.equal(Z.parseAmount(' 0.01 '), 1);
  assert.equal(Z.parseAmount('0'), null);
  assert.equal(Z.parseAmount('1.005'), null);
  assert.equal(Z.parseAmount('-5'), null);
  assert.equal(Z.parseAmount('1e3'), null);
  assert.equal(Z.parseAmount('12345678901'), null); // больше 10 цифр
  assert.equal(Z.parseAmount('9999999999.99'), 999999999999);
});

test('minutesPrice: floor((час × минуты + 30) / 60) — как AgentTariffPurchaseService::minutesPriceInCents', () => {
  assert.equal(Z.minutesPrice(60000, 60), 60000);
  assert.equal(Z.minutesPrice(60000, 1), 1000);
  assert.equal(Z.minutesPrice(100, 1), 2); // (100 + 30) / 60 = 2.16
  assert.equal(Z.minutesPrice(1, 300), 5);
  assert.equal(Z.minutesPrice(1, 329), 5);
  assert.equal(Z.minutesPrice(1, 330), 6);
});

test('переводы: строки сервера поверх встроенных, ключ — русский текст, подстановки и множественное число', () => {
  const i18n = Z.createI18n();
  assert.equal(i18n.t('Войти'), 'Войти');
  i18n.setLocale('kk', { 'Войти': 'Кіру (встроенный)', 'ПК :number': 'ДК :number' }, { 'Войти': 'Кіру' });
  assert.equal(i18n.locale, 'kk');
  assert.equal(i18n.t('Войти'), 'Кіру');
  assert.equal(i18n.t('ПК :number', { number: 7 }), 'ДК 7');
  assert.equal(i18n.t(':count и :countX', { count: 1, countX: 2 }), '1 и 2');
  i18n.setLocale('ru');
  assert.equal(i18n.tc(':count товар|:count товара|:count товаров', 1), '1 товар');
  assert.equal(i18n.tc(':count товар|:count товара|:count товаров', 3), '3 товара');
  assert.equal(i18n.tc(':count товар|:count товара|:count товаров', 11), '11 товаров');
  i18n.setLocale('en', { ':count item|:count items': ':count item|:count items' });
  assert.equal(i18n.tc(':count item|:count items', 2), '2 items');
  i18n.setLocale('../x');
  assert.equal(i18n.locale, 'ru');
});

const fakeWebview = () => {
  const listeners = [];
  const sent = [];
  return {
    sent,
    addEventListener: (type, fn) => type === 'message' && listeners.push(fn),
    postMessage: (message) => sent.push(message),
    deliver: (data) => listeners.forEach((fn) => fn({ data }))
  };
};

test('мост: запрос с id, ответ и ошибка по id, события службы и процесса UI', async () => {
  const webview = fakeWebview();
  const bridge = Z.createBridge(webview);
  const events = [];
  const locals = [];
  bridge.onEvent((e) => events.push(e));
  bridge.onLocal((name) => locals.push(name));

  const ok = bridge.call('GuestService/ListTariffs');
  const bad = bridge.call('GuestService/PurchaseTariff', { tariffId: '5', balanceSource: 'BALANCE_SOURCE_MAIN' });
  assert.deepEqual(webview.sent[0], { id: 1, method: 'GuestService/ListTariffs', params: {} });
  assert.deepEqual(webview.sent[1].params, { tariffId: '5', balanceSource: 'BALANCE_SOURCE_MAIN' });

  webview.deliver({ id: 99, result: {} }); // чужой id — не ломает ожидание
  webview.deliver({ event: { snapshot: { screen: 'SCREEN_LOCK' } } });
  webview.deliver({ local: 'staffPin' });
  webview.deliver({ id: 2, error: { errorCode: 'ERROR_CODE_INSUFFICIENT_FUNDS', grpcStatus: 9, message: 'Недостаточно средств.', retryAfterMs: '0' } });
  webview.deliver({ id: 1, result: { tariffs: [] } });

  assert.deepEqual(await ok, { tariffs: [] });
  await assert.rejects(bad, (err) => {
    assert.ok(err instanceof Z.BridgeError);
    assert.equal(err.code, 'ERROR_CODE_INSUFFICIENT_FUNDS');
    assert.equal(err.text((k) => `t:${k}`), 'Недостаточно средств.');
    return true;
  });
  assert.deepEqual(events, [{ snapshot: { screen: 'SCREEN_LOCK' } }]);
  assert.deepEqual(locals, ['staffPin']);
  assert.equal(bridge.pendingCount, 0);
});

test('мост: ошибка без текста — запасной ключ; без ответа — таймаут; без webview — отказ', async () => {
  const webview = fakeWebview();
  const timers = [];
  const bridge = Z.createBridge(webview, { setTimer: (fn, ms) => timers.push({ fn, ms }) && timers.length, clearTimer: () => {} });
  const p = bridge.call('agent.SignOut');
  webview.deliver({ id: 1, error: { errorCode: 'ERROR_CODE_UNSPECIFIED', grpcStatus: 14 } });
  await assert.rejects(p, (err) => err.text((k) => `t:${k}`) === 't:Не удалось выполнить действие.');

  const slow = bridge.call('GuestService/ListNews');
  const qr = bridge.call('GuestAuthService/WatchQrLogin', { loginId: 'x' });
  assert.equal(timers[1].ms, 70000);
  assert.equal(timers[2].ms, 11 * 60 * 1000); // ожидание QR-входа — до срока QR-кода
  timers[1].fn();
  await assert.rejects(slow, (err) => err.grpcStatus === 4);
  timers[2].fn();
  await assert.rejects(qr);

  await assert.rejects(Z.createBridge(undefined).call('agent.GetSnapshot'), (err) => err.grpcStatus === 14);
});

test('телефон: «700 123-45-67» по мере ввода, +7XXXXXXXXXX для сервера (PhoneNumberFormatter)', () => {
  assert.equal(Z.formatLocalPhone('7001234567'), '700 123-45-67');
  assert.equal(Z.formatLocalPhone('87001234567'), '700 123-45-67');
  assert.equal(Z.formatLocalPhone('700 12'), '700 12');
  assert.equal(Z.formatLocalPhone('77001234567'), '700 123-45-67'); // вставлен номер с 7 — без первой цифры
  assert.equal(Z.formatLocalPhone('700123456789'), '700 123-45-67'); // лишние цифры отбрасываются
  assert.equal(Z.normalizePhone('+7 (700) 123-45-67'), '+77001234567');
  assert.equal(Z.normalizePhone('700123456'), null);
});

test('время и длительность proto JSON', () => {
  assert.equal(Z.durationSeconds('30s'), 30);
  assert.equal(Z.durationSeconds('1.500s'), 1);
  assert.equal(Z.durationSeconds('x'), 0);
  assert.equal(Z.timestampMs('2026-11-02T10:00:00Z'), Date.UTC(2026, 10, 2, 10));
  assert.equal(Z.timestampMs(''), null);
});
