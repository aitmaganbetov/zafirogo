// Экран входа (Frontend/lock.js) без WebView2: режим «Вход по коду брони» — перенос ClubSetupWindow.SetBookingCode /
// EnterBookingCodeAsync старого агента. Страница выполняется в vm с простой заменой DOM (элементы по id: hidden, value,
// textContent, обработчики); мост и снимок службы — подставные.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import vm from 'node:vm';

const require = createRequire(import.meta.url);
const frontend = join(fileURLToPath(new URL('.', import.meta.url)), '../../src/Zafiro.Agent.UI/Frontend');
const Z = require(join(frontend, 'core.js'));
const code = readFileSync(join(frontend, 'lock.js'), 'utf8');
const html = readFileSync(join(frontend, 'index.html'), 'utf8');

const makeElement = (id) => ({
  id,
  hidden: false,
  value: '',
  textContent: '',
  type: '',
  disabled: false,
  open: false,
  dataset: {},
  handlers: {},
  classList: { toggle() {}, add() {}, remove() {} },
  addEventListener(type, fn) {
    (this.handlers[type] ??= []).push(fn);
  },
  fire(type, event = {}) {
    return Promise.all((this.handlers[type] ?? []).map((fn) => fn({ preventDefault() {}, ...event })));
  },
  focus() {
    this.focused = true;
  },
  setAttribute() {},
  getAttribute() {
    return null;
  },
  removeAttribute() {},
  replaceChildren() {},
  showModal() {
    this.open = true;
  },
  close() {
    this.open = false;
  }
});

// Страница с экраном входа подключённого ПК; calls — вызовы моста, reply — что отвечает служба.
const page = () => {
  const elements = new Map();
  const byId = (id) => {
    if (!elements.has(id)) elements.set(id, makeElement(id));
    return elements.get(id);
  };
  const body = makeElement('body');
  body.dataset.screen = 'login';
  const calls = [];
  const state = { reply: async () => ({}), snapshot: [] };
  const app = {
    i18n: { t: (key, params = {}) => Object.entries(params).reduce((text, [name, value]) => text.replace(`:${name}`, value), key), locale: 'ru' },
    bridge: {
      available: true,
      call: (method, params) => {
        calls.push([method, params]);
        // QR-вход экрана входа здесь не нужен: клуб его не включал.
        return method.startsWith('ui.') ? Promise.resolve({}) : state.reply(method, params);
      }
    },
    snapshot: () => ({ config: { clubName: 'Zafiro Arena', guestFeatures: {} }, connection: 'CONNECTION_ONLINE' }),
    languages: () => [],
    setLanguage() {},
    onLocal() {},
    onLanguage() {},
    onSnapshot: (fn) => state.snapshot.push(fn)
  };
  const document = { getElementById: byId, querySelectorAll: () => [], createElement: () => makeElement(''), body };
  vm.runInNewContext(code, { window: { Zafiro: Z, ZafiroApp: app }, document, setTimeout, clearTimeout });
  state.snapshot.forEach((fn) => fn(app.snapshot()));
  return { $: byId, calls, state };
};

test('разметка: поле кода брони — 6 цифр, вместо телефона и пароля; ссылка под «Забыли пароль?»', () => {
  assert.match(html, /<div id="loginBookingPanel" hidden>\s*<label class="lock-label" for="loginBookingCode" data-i18n="Код брони">Код брони<\/label>\s*<input class="lock-input lock-booking-code" id="loginBookingCode" maxlength="6" inputmode="numeric" autocomplete="off" \/>/);
  assert.match(html, /id="loginForgotToggle"[^\n]*\n\s*<button class="lock-link" id="loginBookingToggle" type="button" data-i18n="Есть код брони\?">/);
  assert.match(readFileSync(join(frontend, 'lock.css'), 'utf8'), /\.lock-booking-code \{\s*font-size: 22px;\s*\}/);
  for (const locale of ['ru', 'kk', 'en']) {
    const lines = JSON.parse(readFileSync(join(frontend, `i18n/${locale}.json`), 'utf8')).lines;
    for (const key of ['Код брони', 'Есть код брони?', 'Вход по коду брони', 'Начать по коду', 'Код брони — 6 цифр.', 'Вернуться ко входу',
      'Введите код из приложения Zafiro того, кто бронировал. Код работает только на забронированных ПК и только во время брони.',
      'Не удалось проверить код. Повторите попытку или обратитесь к администратору клуба.']) {
      assert.ok(lines[key], `${locale}: ${key}`);
    }
  }
});

test('«Есть код брони?»: поле кода вместо телефона и пароля, тексты режима; «Вернуться ко входу» — обратно', async () => {
  const { $ } = page();
  assert.equal($('loginBookingPanel').hidden, true);
  assert.equal($('loginBookingToggle').textContent, 'Есть код брони?');
  assert.equal($('lockTitle').textContent, 'Вход');

  await $('loginRegisterToggle').fire('click'); // режим выключает регистрацию
  await $('loginBookingToggle').fire('click');
  assert.equal($('loginBookingPanel').hidden, false);
  assert.equal($('loginPhonePanel').hidden, true);
  assert.equal($('loginPasswordPanel').hidden, true);
  assert.equal($('loginNamePanel').hidden, true);
  assert.equal($('loginConfirmPanel').hidden, true);
  assert.equal($('loginCodePanel').hidden, true);
  assert.equal($('loginRegisterToggle').hidden, true);
  assert.equal($('loginForgotToggle').hidden, true);
  assert.equal($('lockTitle').textContent, 'Вход по коду брони');
  assert.equal($('lockClub').textContent, 'Введите код из приложения Zafiro того, кто бронировал. Код работает только на забронированных ПК и только во время брони.');
  assert.equal($('loginSubmit').textContent, 'Начать по коду');
  assert.equal($('loginBookingToggle').textContent, 'Вернуться ко входу');
  assert.equal($('loginBookingCode').focused, true);

  await $('loginBookingToggle').fire('click');
  assert.equal($('loginBookingPanel').hidden, true);
  assert.equal($('loginPhonePanel').hidden, false);
  assert.equal($('loginPasswordPanel').hidden, false);
  assert.equal($('loginRegisterToggle').hidden, false);
  assert.equal($('loginForgotToggle').hidden, false);
  assert.equal($('lockTitle').textContent, 'Вход');
  assert.equal($('lockClub').textContent, 'Клуб: Zafiro Arena');
  assert.equal($('loginSubmit').textContent, 'Войти');
});

test('в поле кода — только цифры, не больше шести', async () => {
  const { $ } = page();
  const input = $('loginBookingCode');
  input.value = '12a3-45 678';
  await input.fire('input');
  assert.equal(input.value, '123456');
});

test('код не из 6 цифр — подсказка без запроса к службе', async () => {
  const { $, calls } = page();
  await $('loginBookingToggle').fire('click');
  for (const value of ['', '12345', '12345a']) {
    $('loginBookingCode').value = value;
    await $('loginForm').fire('submit');
    assert.equal($('loginError').textContent, 'Код брони — 6 цифр.');
  }
  assert.deepEqual(calls.filter(([method]) => method.startsWith('Guest')), []);
});

test('верный код: один запрос службе, форма возвращается ко входу (экран снимет снимок службы)', async () => {
  const { $, calls } = page();
  await $('loginBookingToggle').fire('click');
  $('loginBookingCode').value = '123456';
  await $('loginForm').fire('submit');
  // Параметры созданы в vm (другой realm) — сравниваем как JSON.
  assert.equal(JSON.stringify(calls.filter(([method]) => method.startsWith('Guest'))), JSON.stringify([['GuestAuthService/StartBookingByCode', { code: '123456' }]]));
  assert.equal($('loginError').textContent, '');
  assert.equal($('loginBookingCode').value, '');
  assert.equal($('loginBookingPanel').hidden, true);
  assert.equal($('loginSubmit').disabled, false);
  assert.equal($('lockTitle').textContent, 'Вход');
});

test('отказ сервера — его текст (чужой ПК, рано, лимит); без текста — общий; режим и код остаются', async () => {
  const { $, state } = page();
  await $('loginBookingToggle').fire('click');
  $('loginBookingCode').value = '654321';
  state.reply = async () => {
    throw new Z.BridgeError({ errorCode: 'ERROR_CODE_BOOKING_CODE_OTHER_COMPUTER', grpcStatus: 9, message: 'Этот код брони не для этого ПК: сядьте за забронированный ПК.' });
  };
  await $('loginForm').fire('submit');
  assert.equal($('loginError').textContent, 'Этот код брони не для этого ПК: сядьте за забронированный ПК.');
  assert.equal($('loginBookingPanel').hidden, false);
  assert.equal($('loginBookingCode').value, '654321');
  assert.equal($('loginSubmit').disabled, false);

  state.reply = async () => {
    throw new Z.BridgeError({ errorCode: 'ERROR_CODE_RATE_LIMITED', grpcStatus: 8, message: 'Too Many Attempts.', retryAfterMs: '42000' });
  };
  await $('loginForm').fire('submit');
  assert.equal($('loginError').textContent, 'Too Many Attempts.');

  state.reply = async () => {
    throw new Z.BridgeError({ errorCode: 'ERROR_CODE_UNSPECIFIED', grpcStatus: 14 });
  };
  await $('loginForm').fire('submit');
  assert.equal($('loginError').textContent, 'Не удалось проверить код. Повторите попытку или обратитесь к администратору клуба.');
});
