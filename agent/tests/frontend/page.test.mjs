// Статические проверки страницы агента: только локальные ресурсы и CSP, методы моста — только из таблицы IPC
// (Zafiro.Agent.Protocol/Ipc/IpcMethods.cs) или локальные ui.*, никакого HTML из данных.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

const here = fileURLToPath(new URL('.', import.meta.url));
const frontend = join(here, '../../src/Zafiro.Agent.UI/Frontend');
const read = (name) => readFileSync(join(frontend, name), 'utf8');
const scripts = readdirSync(frontend).filter((name) => name.endsWith('.js'));
const html = read('index.html');

test('CSP: только свои ресурсы, без внешних адресов и inline-скриптов', () => {
  const csp = /http-equiv="Content-Security-Policy" content="([^"]+)"/.exec(html)?.[1];
  assert.ok(csp, 'CSP meta');
  assert.match(csp, /default-src 'none'/);
  assert.match(csp, /script-src 'self'(;|$)/);
  assert.doesNotMatch(csp, /https?:|\*|'unsafe-eval'/);
  assert.match(csp, /connect-src 'self'/);
  assert.match(csp, /frame-src 'none'/);
  assert.match(csp, /form-action 'none'/);
  // Скрипты и стили — только локальные файлы, без inline <script>.
  for (const [, src] of html.matchAll(/<script([^>]*)>/g)) assert.match(src, /src="[a-z]+\.js"/);
  for (const [, href] of html.matchAll(/<link[^>]*href="([^"]+)"/g)) assert.match(href, /^[a-z]+\.css$/);
  assert.doesNotMatch(html, /(src|href)="(https?:)?\/\//);
  assert.doesNotMatch(html, /\son[a-z]+="/); // обработчики только из скриптов
});

test('скрипты: без eval, внешних адресов, innerHTML и прямого postMessage мимо моста', () => {
  for (const name of scripts) {
    const code = read(name);
    assert.doesNotMatch(code, /\beval\(|new Function\(|innerHTML|outerHTML|insertAdjacentHTML|document\.write/, name);
    assert.doesNotMatch(code, /['"`]https?:\/\//, name);
    if (name !== 'core.js') assert.doesNotMatch(code, /\.postMessage\(/, `${name}: только Zafiro.createBridge`);
  }
});

test('методы моста: только из таблицы IPC службы или локальные ui.*', () => {
  const table = readFileSync(join(here, '../../src/Zafiro.Agent.Protocol/Ipc/IpcMethods.cs'), 'utf8');
  const known = new Set([...table.matchAll(/new\("([^"]+)"/g)].map((m) => m[1]));
  const router = readFileSync(join(here, '../../src/Zafiro.Agent.UI.Core/Bridge/BridgeRouter.cs'), 'utf8');
  const local = /LocalMethods \{ get; \} = \[([^\]]+)\]/.exec(router)[1];
  for (const [, name] of local.matchAll(/"([^"]+)"/g)) known.add(name);
  assert.ok(known.size > 30);
  const used = new Set();
  for (const name of scripts) {
    for (const [, method] of read(name).matchAll(/bridge\.call\('([^']+)'/g)) used.add(method);
  }
  assert.ok(used.size > 25, `used ${used.size}`);
  for (const method of used) assert.ok(known.has(method), `неизвестный метод ${method}`);
});

test('переводы: встроенные файлы ru/kk/en с одинаковыми ключами', () => {
  const load = (locale) => JSON.parse(read(`i18n/${locale}.json`));
  const ru = load('ru');
  for (const locale of ['kk', 'en']) {
    const other = load(locale);
    assert.equal(other.locale, locale);
    const missing = Object.keys(ru.lines).filter((key) => !(key in other.lines));
    assert.deepEqual(missing, [], `${locale}: нет переводов`);
  }
});
