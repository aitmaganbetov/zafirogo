// Поднабор Font Awesome (FE-05): из 2 452 иконок пакета интерфейс использует около сотни, а all.min.css и
// шрифты целиком — это 102 КБ CSS и 280 КБ woff2 на первом экране.
//
//   node scripts/gen-icons.mjs          — пересобрать src/styles/icons/* (CSS + три woff2)
//   node scripts/gen-icons.mjs --check  — только проверить, что поднабор покрывает все используемые иконки
//
// Источник — закреплённый npm-пакет @fortawesome/fontawesome-free (решение Q-064: версия закреплена, CDN нет).
// CSS берётся из all.min.css без изменений, кроме: правила иконок (`.fa-имя:before{content}`) — только
// используемые; @font-face — только семейства Font Awesome 6 и только woff2. Глифы в шрифтах — те же контуры
// (hb-subset), поэтому вид иконок не меняется. Результат коммитится (как остальной сгенерированный код);
// тест src/styles/icons/icons.test.ts падает, если в коде появилась иконка, которой нет в поднаборе.
import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const web = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const pkg = join(web, 'node_modules/@fortawesome/fontawesome-free');
const out = join(web, 'src/styles/icons');

// Иконки, имя которых приходит из данных, а не из литерала в коде фронта:
//  - значок зоны на карте (`fa-${zone.icon}`): значения ограничены CHECK club_zones_icon_check (db/migrations/0001_init.sql);
//  - значки колокольчика (`fas ${item.icon}`) — литералы "fa-…" в коде сервера, они сканируются ниже.
export const DATA_ICONS = ['crown', 'desktop', 'gamepad', 'mug-hot', 'couch', 'dice', 'door-open', 'bong', 'smoking'];

const FONTS = [
  { file: 'fa-solid-900.woff2', family: 'Font Awesome 6 Free', weight: '900' },
  { file: 'fa-regular-400.woff2', family: 'Font Awesome 6 Free', weight: '400' },
  { file: 'fa-brands-400.woff2', family: 'Font Awesome 6 Brands', weight: '400' },
];

function walk(dir, exts, acc = []) {
  if (!existsSync(dir)) return acc;
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'gen' || name === 'dist' || name === 'icons') continue;
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, exts, acc);
    else if (exts.some((e) => name.endsWith(e))) acc.push(p);
  }
  return acc;
}

/** Все токены fa-… в исходниках фронта, wiki и в литералах сервера (значки уведомлений). */
export function usedTokens() {
  const tokens = new Set(DATA_ICONS.map((n) => `fa-${n}`));
  const files = [
    ...walk(join(web, 'src'), ['.ts', '.tsx']),
    ...walk(join(web, 'wiki'), ['.ts', '.tsx', '.html']),
    join(web, 'index.html'),
    ...walk(resolve(web, '../server/internal'), ['.go']),
  ];
  for (const f of files) {
    if (!existsSync(f) || /\.test\.tsx?$|_test\.go$/.test(f)) continue;
    const text = readFileSync(f, 'utf8');
    const re = f.endsWith('.go') ? /"(fa-[a-z0-9]+(?:-[a-z0-9]+)*)"/g : /(?<![\w-])(fa-[a-z0-9]+(?:-[a-z0-9]+)*)/g;
    for (const m of text.matchAll(re)) tokens.add(m[1]);
  }
  return tokens;
}

/** Правила верхнего уровня минифицированного CSS (скобки сбалансированы; вложенность — только @media/@keyframes). */
function topLevelRules(css) {
  const rules = [];
  let depth = 0;
  let start = 0;
  for (let i = 0; i < css.length; i++) {
    const ch = css[i];
    if (ch === '"') {
      i = css.indexOf('"', i + 1);
      continue;
    }
    if (ch === '{') depth++;
    else if (ch === '}' && --depth === 0) {
      rules.push(css.slice(start, i + 1));
      start = i + 1;
    }
  }
  return rules;
}

const ICON_RULE = /^([^{}@]+)\{content:"(\\[0-9a-f]+|.)"\}$/;
const ICON_SELECTOR = /^\.(fa-[a-z0-9-]+):{1,2}(?:before|after)$/;

/** Полный CSS пакета → { css поднабора, коды символов используемых иконок, имена всех иконок пакета }. */
export function subsetCss(source, used) {
  const banner = source.slice(0, source.indexOf('*/') + 2);
  const kept = [];
  const codepoints = new Set();
  const known = new Set();
  for (const rule of topLevelRules(source.slice(banner.length))) {
    const icon = ICON_RULE.exec(rule);
    if (icon) {
      const selectors = icon[1].split(',');
      if (selectors.every((s) => ICON_SELECTOR.test(s))) {
        for (const s of selectors) known.add(ICON_SELECTOR.exec(s)[1]);
        const mine = selectors.filter((s) => used.has(ICON_SELECTOR.exec(s)[1]));
        if (mine.length === 0) continue;
        const code = icon[2];
        codepoints.add(code.startsWith('\\') ? parseInt(code.slice(1), 16) : code.codePointAt(0));
        kept.push(`${mine.join(',')}{content:"${code}"}`);
        continue;
      }
    }
    if (rule.startsWith('@font-face')) {
      const font = FONTS.find((f) => rule.includes(`font-family:"${f.family}"`) && rule.includes(`font-weight:${f.weight}`));
      if (!font) continue; // семейства совместимости Font Awesome 4/5 не используются
      kept.push(rule.replace(/src:[^;}]+/, `src:url(./${font.file}) format("woff2")`));
      continue;
    }
    kept.push(rule);
  }
  return { css: `${banner}\n/* Поднабор: scripts/gen-icons.mjs. Не править руками. */\n${kept.join('')}\n`, codepoints, known };
}

export function build() {
  const source = readFileSync(join(pkg, 'css/all.min.css'), 'utf8');
  const used = usedTokens();
  return { ...subsetCss(source, used), used };
}

async function main() {
  const { css, codepoints, known, used } = build();
  const icons = [...used].filter((t) => known.has(t)).sort();
  const target = join(out, 'fa-subset.css');
  if (process.argv.includes('--check')) {
    // Значки зон в окне зоны (выбор в интерфейсе) должны совпадать со списком «иконок из данных».
    const modals = readFileSync(join(web, 'src/features/clubmap/ClubMapModals.tsx'), 'utf8');
    const block = /const ICONS[^=]*=\s*\[([\s\S]*?)\];/.exec(modals)?.[1] ?? '';
    const zoneIcons = [...block.matchAll(/\['([a-z-]+)',/g)].map((m) => m[1]);
    const missing = zoneIcons.filter((n) => !DATA_ICONS.includes(n));
    if (zoneIcons.length === 0 || missing.length > 0) {
      console.error(`gen-icons: значки зон не из списка DATA_ICONS: ${missing.join(', ') || '(список ICONS не найден)'}`);
      process.exit(1);
    }
    const unknown = [...used].filter((t) => t !== 'fa-subset' && !known.has(t) && !css.includes(`.${t}{`) && !css.includes(`.${t},`) && !css.includes(`.${t}>`) && !css.includes(`.${t}:`));
    if (unknown.length > 0) console.warn(`gen-icons: токены fa-… без правила в пакете (не иконки?): ${unknown.join(', ')}`);
    const current = existsSync(target) ? readFileSync(target, 'utf8') : '';
    if (current !== css) {
      console.error('gen-icons: поднабор иконок устарел — запустите `npm run gen:icons` и закоммитьте src/styles/icons');
      process.exit(1);
    }
    console.log(`gen-icons: актуально (${icons.length} иконок)`);
    return;
  }
  const { default: subsetFont } = await import('subset-font');
  const text = String.fromCodePoint(...codepoints);
  writeFileSync(target, css);
  for (const f of FONTS) {
    const full = readFileSync(join(pkg, 'webfonts', f.file));
    const sub = await subsetFont(full, text, { targetFormat: 'woff2' });
    writeFileSync(join(out, f.file), sub);
    console.log(`${f.file}: ${full.length} → ${sub.length} Б`);
  }
  console.log(`fa-subset.css: ${source_len()} → ${css.length} Б, иконок: ${icons.length} из ${known.size}`);
}

function source_len() {
  return statSync(join(pkg, 'css/all.min.css')).size;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
