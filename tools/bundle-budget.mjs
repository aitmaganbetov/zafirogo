#!/usr/bin/env node
// Бюджет на размер фронта после `npm run build` (аудит FE-11, CI: .github/workflows/web.yml).
//
//   node tools/bundle-budget.mjs [каталог dist]      # по умолчанию web/dist
//
// Что считается (gzip уровня 6 — как отдаёт сервер/nginx; байты):
//   main     — входной скрипт из index.html (<script type="module" src=…>);
//   initial  — входной скрипт + все <link rel="modulepreload"> (то, что браузер качает до первого экрана);
//   css      — стили из index.html (<link rel="stylesheet">), сумма;
//   chunk    — самый большой из остальных .js в assets (страница или библиотека по требованию).
//
// Пороги — «текущее значение + запас ≈ 10 %» на 15.11.2026 (сборка a77ce01: main 83 462, initial 145 490,
// css 46 534, chunk 378 398 — apidocs, справка /docs/api). Это защита от роста, а не цель: целевые значения аудита
// FE-11 — main ≤ 90 КБ, CSS ≤ 50 КБ, чанк страницы ≤ 30 КБ. Пороги УМЕНЬШАЮТСЯ вместе с работами по фронту (исполнитель F4: FE-01…FE-12), вверх —
// только осознанно, с записью в docs/open-questions/audit-ci.md.
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { gzipSync } from "node:zlib";

const BUDGET = {
  main: 92_000,
  initial: 160_000,
  css: 51_000,
  chunk: 416_000,
};

const dist = process.argv[2] ?? "web/dist";
const html = readFileSync(join(dist, "index.html"), "utf8");
const gz = (rel) => gzipSync(readFileSync(join(dist, rel)), { level: 6 }).length;
const attr = (tag, name) => new RegExp(`\\s${name}="([^"]*)"`).exec(tag)?.[1];
const local = (url) => (url && url.startsWith("/") && !url.startsWith("//") ? url.slice(1) : null);

const entries = [];
const preloads = [];
const styles = [];
for (const tag of html.match(/<(script|link)\b[^>]*>/g) ?? []) {
  if (tag.startsWith("<script")) {
    const src = local(attr(tag, "src"));
    if (src && attr(tag, "type") === "module") entries.push(src);
    continue;
  }
  const href = local(attr(tag, "href"));
  if (!href) continue;
  const rel = attr(tag, "rel");
  if (rel === "modulepreload") preloads.push(href);
  if (rel === "stylesheet") styles.push(href);
}
if (entries.length !== 1) {
  console.error(`bundle-budget: в ${dist}/index.html ожидается один входной <script type="module">, найдено ${entries.length}`);
  process.exit(2);
}

const sum = (files) => files.reduce((n, f) => n + gz(f), 0);
const initialSet = new Set([...entries, ...preloads]);
let chunk = 0;
let chunkName = "—";
for (const name of readdirSync(join(dist, "assets"))) {
  const rel = `assets/${name}`;
  if (!name.endsWith(".js") || initialSet.has(rel)) continue;
  const size = gz(rel);
  if (size > chunk) [chunk, chunkName] = [size, name];
}

const got = {
  main: [sum(entries), entries[0]],
  initial: [sum([...initialSet]), `${initialSet.size} файлов`],
  css: [sum(styles), styles.join(", ") || "—"],
  chunk: [chunk, chunkName],
};

let failed = false;
for (const [key, limit] of Object.entries(BUDGET)) {
  const [size, what] = got[key];
  const over = size > limit;
  failed ||= over;
  console.log(`${over ? "ПРЕВЫШЕН" : "ok      "} ${key.padEnd(8)} ${String(size).padStart(8)} / ${String(limit).padStart(8)} байт gzip  (${what})`);
}
if (failed) {
  console.error("bundle-budget: размер фронта вырос сверх бюджета (tools/bundle-budget.mjs). Уменьшите чанк или поднимите порог осознанно.");
  process.exit(1);
}
