// Тема клуба на ПК (настройки клуба → «Агент ПК», коммиты старого e38b764 и 8094b1d): цвета из снимка службы —
// CSS-переменные оболочки (styles.css) и экрана входа (lock.css); только "#rrggbb"; карточка входа — 380 px.
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
const read = (name) => readFileSync(join(frontend, name), 'utf8').replace(/\r\n/g, '\n');

const red = { accent: '#DC2626', accent2: '#fb7185', soft: '#fca5a5', highlight: '#fda4af', bg0: '#0a0304', bg1: '#1f0709' };

test('shellThemeVars: цвета темы → переменные оболочки и экрана входа (нижний регистр)', () => {
  const vars = Object.fromEntries(Z.shellThemeVars(red));
  assert.deepEqual(vars, {
    '--accent': '#dc2626', '--accent-2': '#fb7185', '--soft': '#fca5a5', '--highlight': '#fda4af', '--bg-0': '#0a0304', '--bg-1': '#1f0709',
    '--lock-window': '#0a0304', '--lock-panel': '#1f0709', '--lock-accent': '#dc2626', '--lock-kicker': '#fca5a5', '--lock-link': '#fca5a5'
  });
});

test('shellThemeVars: нет темы или хоть один цвет не #rrggbb — стандартная тема (все переменные снимаются)', () => {
  const standard = (theme) => Z.shellThemeVars(theme).every(([, color]) => color === null);
  assert.ok(standard(null));
  assert.ok(standard(undefined));
  assert.ok(standard('red'));
  assert.ok(standard({ ...red, accent: 'red;background:url(x)' }));
  assert.ok(standard({ ...red, bg1: '#1f070' }));
  assert.ok(standard({ ...red, soft: '#fca5a5 ' }));
  assert.ok(standard({ ...red, highlight: 'var(--x)' }));
  assert.ok(standard({ accent: '#dc2626' })); // неполная тема
  assert.equal(Z.shellThemeVars(null).length, 11);
});

// app.js в vm: снимок службы ставит и снимает переменные на <html>.
test('app.js: тема из снимка — на <html>, смена темы и стандартная — без перезагрузки страницы', () => {
  const props = new Map();
  let onMessage = null;
  const element = () => ({ hidden: false, dataset: {}, addEventListener() {} });
  const document = {
    documentElement: { style: { setProperty: (k, v) => props.set(k, v), removeProperty: (k) => props.delete(k) } },
    body: element(),
    getElementById: () => element()
  };
  const window = {
    Zafiro: Z,
    chrome: { webview: { addEventListener: (type, fn) => { if (type === 'message') onMessage = fn; }, postMessage() {} } },
    addEventListener() {}
  };
  const context = vm.createContext({ window, document, fetch: () => Promise.reject(new Error('offline')), setTimeout, clearTimeout, console });
  vm.runInContext(read('app.js'), context);
  const snapshot = (shellTheme) => onMessage({ data: { event: { snapshot: { screen: 'SCREEN_LOCK', translationsVersion: '0', shellTheme } } } });

  snapshot(red);
  assert.equal(props.get('--accent'), '#dc2626');
  assert.equal(props.get('--lock-panel'), '#1f0709');
  snapshot({ ...red, accent: '#16a34a' });
  assert.equal(props.get('--accent'), '#16a34a');
  assert.equal(props.get('--lock-accent'), '#16a34a');
  snapshot(undefined);
  assert.equal(props.size, 0);
  snapshot({ ...red, accent: 'url(javascript:alert(1))' });
  assert.equal(props.size, 0);
});

test('стили: темизируемые цвета — переменные, стандартные значения — как у старого агента', () => {
  const styles = read('styles.css');
  assert.match(styles, /--soft: #86d7c3;/);
  assert.match(styles, /--highlight: #d8f36a;/);
  // Жёстких цветов старой темы (бирюза, лайм, фиолетовый) в правилах больше нет — только в :root.
  const rules = styles.slice(styles.indexOf('}') + 1);
  assert.doesNotMatch(rules, /#86d7c3|#d8f36a|rgba\(139,92,246|rgba\(94,183,255|#3b2a6b/i);

  const lock = read('lock.css');
  const root = /:root \{([^}]+)\}/.exec(lock)?.[1] ?? '';
  for (const [name, color] of [['--lock-window', '#111019'], ['--lock-panel', '#1b1925'], ['--lock-accent', '#7554c8'], ['--lock-kicker', '#a78bfa'], ['--lock-link', '#c4b5fd']]) {
    assert.match(root, new RegExp(`${name}: ${color};`));
  }
  assert.doesNotMatch(lock.replace(root, ''), /#111019|#1b1925|#7554c8|#a78bfa|#c4b5fd|117, 84, 200/i);
  assert.match(lock, /\.lock-power:hover \{\n {2}background: color-mix\(in srgb, var\(--lock-accent\) 80%, transparent\);/);
});

test('карточка входа — постоянные 380 px (8094b1d), QR 112 px, текст QR переносится', () => {
  const lock = read('lock.css');
  const card = /\.lock-card \{([^}]+)\}/.exec(lock)?.[1] ?? '';
  assert.match(card, /width: 380px;/);
  assert.match(card, /padding: 26px 30px;/);
  assert.doesNotMatch(lock, /\.lock-card > \*/);
  assert.match(/\.lock-qr-code img \{([^}]+)\}/.exec(lock)?.[1] ?? '', /width: 112px;\n {2}height: 112px;/);
  assert.match(/\n\n\.lock-qr-text \{([^}]+)\}/.exec(lock)?.[1] ?? '', /flex: 1;\n {2}min-width: 0;/);
});
