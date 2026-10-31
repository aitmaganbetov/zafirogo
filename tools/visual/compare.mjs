#!/usr/bin/env node
// Сравнение снимков старого и нового приложения (pixelmatch) и отчёт tools/visual/report/index.html + summary.json.
// Снимки делает web/e2e/visual/snapshot.visual.ts: report/shots/<id>/<viewport>-<theme>/{old,new}.png и .json.
//
//   node tools/visual/compare.mjs [--page id[,id]] [--report dir]
//
// Код выхода: 1 — есть ошибка снимка или перенесённая страница (status: ported) сверх порога; иначе 0.
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import pixelmatch from 'pixelmatch';
import { PNG } from 'pngjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const arg = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const REPORT = path.resolve(arg('--report') ?? process.env.VISUAL_REPORT_DIR ?? path.join(HERE, 'report'));
const only = (arg('--page') ?? process.env.VISUAL_PAGES ?? '').split(',').map((s) => s.trim()).filter(Boolean);
const pagesFile = JSON.parse(readFileSync(path.join(HERE, 'pages.json'), 'utf8'));
const defaults = pagesFile.defaults;

// Отступ меньшего снимка до общего размера (прозрачный): разница высот считается расхождением.
const PAD = [0, 0, 0, 0];

function readPng(file) {
  return existsSync(file) ? PNG.sync.read(readFileSync(file)) : null;
}

function padTo(img, width, height) {
  if (img.width === width && img.height === height) return img;
  const out = new PNG({ width, height });
  for (let i = 0; i < out.data.length; i += 4) out.data.set(PAD, i);
  PNG.bitblt(img, out, 0, 0, img.width, img.height, 0, 0);
  return out;
}

function readMeta(file) {
  return existsSync(file) ? JSON.parse(readFileSync(file, 'utf8')) : null;
}

function compareShot(spec, variant) {
  const dir = path.join(REPORT, 'shots', spec.id, variant);
  const old = readPng(path.join(dir, 'old.png'));
  const neu = readPng(path.join(dir, 'new.png'));
  const metaOld = readMeta(path.join(dir, 'old.json'));
  const metaNew = readMeta(path.join(dir, 'new.json'));
  const status = spec.status ?? defaults.status;
  const maxDiffPercent = spec.maxDiffPercent ?? defaults.maxDiffPercent;
  const threshold = spec.pixelThreshold ?? defaults.pixelThreshold;
  const row = {
    id: spec.id, variant, status, note: spec.note ?? '', maxDiffPercent, threshold,
    old: { path: `shots/${spec.id}/${variant}/old.png`, size: old && [old.width, old.height], meta: metaOld },
    new: { path: `shots/${spec.id}/${variant}/new.png`, size: neu && [neu.width, neu.height], meta: metaNew },
    diff: null, diffPixels: null, diffPercent: null, verdict: 'error', errors: [],
  };
  for (const m of [metaOld, metaNew]) if (m?.error) row.errors.push(`${m.app}: ${m.error}`);
  if (!old || !neu) {
    row.errors.push(`нет снимка: ${!old ? 'old.png ' : ''}${!neu ? 'new.png' : ''}`.trim());
    return row;
  }
  const width = Math.max(old.width, neu.width);
  const height = Math.max(old.height, neu.height);
  const a = padTo(old, width, height);
  const b = padTo(neu, width, height);
  const diff = new PNG({ width, height });
  const pixels = pixelmatch(a.data, b.data, diff.data, width, height, {
    threshold,
    includeAA: false,
    alpha: 0.2,
    diffColor: [255, 0, 0], // в новом светлее / другое
    diffColorAlt: [0, 160, 255], // в новом темнее
  });
  writeFileSync(path.join(dir, 'diff.png'), PNG.sync.write(diff));
  row.diff = `shots/${spec.id}/${variant}/diff.png`;
  row.diffPixels = pixels;
  row.diffPercent = Math.round((pixels / (width * height)) * 1e4) / 100;
  if (row.errors.length > 0) row.verdict = 'error';
  else if (row.diffPercent <= maxDiffPercent) row.verdict = 'pass';
  else row.verdict = status === 'ported' ? 'fail' : 'pending';
  return row;
}

const rows = [];
const shotsRoot = path.join(REPORT, 'shots');
for (const spec of pagesFile.pages) {
  if (only.length > 0 && !only.includes(spec.id)) continue;
  const dir = path.join(shotsRoot, spec.id);
  if (!existsSync(dir)) continue;
  for (const variant of readdirSync(dir).sort()) rows.push(compareShot(spec, variant));
}

const counts = { pass: 0, fail: 0, pending: 0, error: 0 };
for (const r of rows) counts[r.verdict] += 1;
const summary = {
  generatedAt: new Date().toISOString(),
  durationSeconds: process.env.VISUAL_DURATION ? Number(process.env.VISUAL_DURATION) : undefined,
  counts,
  ok: counts.fail === 0 && counts.error === 0 && rows.length > 0,
  results: rows,
};
writeFileSync(path.join(REPORT, 'summary.json'), JSON.stringify(summary, null, 2));
writeFileSync(path.join(REPORT, 'index.html'), renderHtml(summary));

// Вывод в терминал.
const mark = { pass: 'OK  ', fail: 'FAIL', pending: 'WAIT', error: 'ERR ' };
for (const r of rows) {
  const pct = r.diffPercent === null ? '   —   ' : `${r.diffPercent.toFixed(2).padStart(6)}%`;
  const sizes = r.old.size && r.new.size ? `  ${r.old.size.join('×')} | ${r.new.size.join('×')}` : '';
  console.log(`${mark[r.verdict]} ${pct} (порог ${r.maxDiffPercent}%)  ${r.id} ${r.variant}${sizes}`);
  for (const e of r.errors) console.log(`       ! ${e}`);
}
if (rows.length === 0) console.log('нет снимков: сначала playwright test -c e2e/visual/playwright.config.ts');
console.log(
  `\nИтог: ${counts.pass} совпало, ${counts.fail} расходится (перенесённые), ${counts.pending} расходится (не перенесены), ${counts.error} ошибок`,
);
console.log(`Отчёт: ${path.join(REPORT, 'index.html')}`);
process.exit(summary.ok ? 0 : 1);

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
}

function metaBlock(m) {
  if (!m) return '<p class="muted">нет данных</p>';
  const list = (title, items) =>
    items?.length ? `<details><summary>${esc(title)} (${items.length})</summary><ul>${items.map((i) => `<li>${esc(i)}</li>`).join('')}</ul></details>` : '';
  return `<dl>
    <dt>URL</dt><dd>${esc(m.finalUrl || m.url)}${m.status ? ` · HTTP ${m.status}` : ''}</dd>
    <dt>Вход</dt><dd>${esc(m.login ?? 'без входа')}</dd>
    <dt>Маски</dt><dd>${m.masks?.length ? esc(m.masks.join(', ')) : '—'}</dd>
    ${m.hidden?.length ? `<dt>Скрыто</dt><dd>${esc(m.hidden.join(', '))}</dd>` : ''}
    ${m.retriedAfter ? `<dt>Повтор</dt><dd>первая попытка упала: ${esc(m.retriedAfter)}</dd>` : ''}
    ${m.truncatedFrom ? `<dt>Обрезано</dt><dd>снят верх страницы высотой ${esc(m.truncatedFrom)} px</dd>` : ''}
  </dl>${m.error ? `<p class="err">${esc(m.error)}</p>` : ''}${list('Ошибки консоли', m.consoleErrors)}${list(
    'Внешние запросы заблокированы',
    m.network?.blocked,
  )}${list('Внешние запросы из кэша/npm', m.network?.served)}${list('Неудачные запросы', m.network?.failed)}`;
}

function renderHtml(s) {
  const label = { pass: 'совпадает', fail: 'расходится', pending: 'не перенесено', error: 'ошибка' };
  const cards = s.results
    .map(
      (r) => `<section class="card" data-verdict="${r.verdict}">
  <header>
    <h2>${esc(r.id)} <small>${esc(r.variant)}</small></h2>
    <span class="badge ${r.verdict}">${label[r.verdict]}</span>
    <span class="pct">${r.diffPercent === null ? '—' : `${r.diffPercent.toFixed(2)}%`} <small>порог ${r.maxDiffPercent}% · pixelmatch ${r.threshold}</small></span>
  </header>
  ${r.note ? `<p class="note">${esc(r.note)}</p>` : ''}
  ${r.errors.map((e) => `<p class="err">${esc(e)}</p>`).join('')}
  <div class="grid">
    <figure><figcaption>Старое (Laravel) ${r.old.size ? `· ${r.old.size.join('×')}` : ''}</figcaption>${r.old.size ? `<a href="${r.old.path}" target="_blank"><img loading="lazy" src="${r.old.path}" alt="old"></a>` : ''}${metaBlock(r.old.meta)}</figure>
    <figure><figcaption>Новое (React) ${r.new.size ? `· ${r.new.size.join('×')}` : ''}</figcaption>${r.new.size ? `<a href="${r.new.path}" target="_blank"><img loading="lazy" src="${r.new.path}" alt="new"></a>` : ''}${metaBlock(r.new.meta)}</figure>
    <figure><figcaption>Разница <span class="legend"><i class="red"></i>другое/светлее <i class="blue"></i>темнее <i class="yellow"></i>сглаживание (не считается); полоса, которой нет в меньшем снимке, — расхождение</span></figcaption>${r.diff ? `<a href="${r.diff}" target="_blank"><img loading="lazy" src="${r.diff}" alt="diff"></a>` : ''}</figure>
  </div>
</section>`,
    )
    .join('\n');
  return `<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Визуальное сравнение</title>
<style>
:root{--bg:#f6f7f9;--fg:#111827;--muted:#6b7280;--card:#fff;--line:#e5e7eb;--ok:#15803d;--bad:#b91c1c;--wait:#a16207;--err:#7c3aed}
@media (prefers-color-scheme:dark){:root{--bg:#0b0f14;--fg:#e5e7eb;--muted:#9ca3af;--card:#111827;--line:#1f2937}}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.45 system-ui,sans-serif}
main{max-width:1800px;margin:0 auto;padding:16px}
h1{font-size:20px;margin:0 0 4px}.muted{color:var(--muted)}
.summary{display:flex;flex-wrap:wrap;gap:8px;margin:12px 0}
.summary button{border:1px solid var(--line);background:var(--card);color:var(--fg);border-radius:999px;padding:4px 12px;cursor:pointer}
.summary button[aria-pressed=true]{outline:2px solid currentColor}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:12px;margin:12px 0}
.card header{display:flex;flex-wrap:wrap;align-items:center;gap:12px}.card h2{font-size:16px;margin:0}
.badge{border-radius:999px;padding:2px 10px;color:#fff;font-size:12px}.pass{background:var(--ok)}.fail{background:var(--bad)}.pending{background:var(--wait)}.error{background:var(--err)}
.pct{font-variant-numeric:tabular-nums;font-weight:600}.pct small{font-weight:400;color:var(--muted)}
.grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin-top:8px}
@media (max-width:900px){.grid{grid-template-columns:1fr}}
figure{margin:0;min-width:0}figcaption{font-weight:600;margin-bottom:4px}
img{width:100%;height:auto;border:1px solid var(--line);background:repeating-conic-gradient(#ddd 0 25%,#fff 0 50%) 0 0/16px 16px}
dl{display:grid;grid-template-columns:auto 1fr;gap:2px 8px;margin:6px 0;font-size:12px}dt{color:var(--muted)}dd{margin:0;overflow-wrap:anywhere}
details{font-size:12px}ul{margin:4px 0;padding-left:18px;overflow-wrap:anywhere}
.err{color:var(--bad);font-weight:600}.note{color:var(--muted);margin:6px 0}
.legend{font-weight:400;font-size:12px;color:var(--muted)}.legend i{display:inline-block;width:10px;height:10px;margin:0 3px 0 8px;vertical-align:middle}
.legend .red{background:#f00}.legend .blue{background:#00a0ff}.legend .yellow{background:#ff0}
</style></head><body><main>
<h1>Визуальное сравнение: старый Laravel ↔ новый React</h1>
<p class="muted">${esc(s.generatedAt)}${s.durationSeconds ? ` · прогон ${s.durationSeconds} с` : ''} · совпадает — доля отличающихся пикселей не больше порога страницы (pages.json). Маски живых элементов закрашены пурпурным в обоих снимках.</p>
<div class="summary" role="group" aria-label="Фильтр">
<button data-filter="all" aria-pressed="true">Все (${s.results.length})</button>
<button data-filter="fail">Расходится (${s.counts.fail})</button>
<button data-filter="pending">Не перенесено (${s.counts.pending})</button>
<button data-filter="error">Ошибки (${s.counts.error})</button>
<button data-filter="pass">Совпадает (${s.counts.pass})</button>
</div>
${cards || '<p>Нет снимков.</p>'}
</main>
<script>
for (const b of document.querySelectorAll('[data-filter]')) b.addEventListener('click', () => {
  for (const o of document.querySelectorAll('[data-filter]')) o.setAttribute('aria-pressed', String(o === b));
  for (const c of document.querySelectorAll('.card')) c.hidden = b.dataset.filter !== 'all' && c.dataset.verdict !== b.dataset.filter;
});
</script></body></html>
`;
}
