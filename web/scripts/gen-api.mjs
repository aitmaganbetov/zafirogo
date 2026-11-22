// Типы API для фронта: api/<раздел>.yaml → src/api/gen/<раздел>.d.ts (openapi-typescript, по файлу на раздел,
// чтобы агенты разделов не правили общий файл). Ссылки на common.yaml встраиваются в components раздела.
// Запуск: npm run gen:api (или task gen:web). Конвенции — web/README.md.
import { execFileSync } from 'node:child_process';
import { mkdirSync, readdirSync, rmSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const web = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const apiDir = resolve(web, '../api');
const outDir = join(web, 'src/api/gen');
const bin = join(web, 'node_modules/.bin/openapi-typescript');

rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });
// Разделы без вызовов из фронта — TS-типы не генерируются (аудит 22.11.2026, CODE-11: файлы коммитились, но нигде
// не импортировались). common — общие схемы, встраиваются в типы разделов; i18n — пакеты переводов грузит
// i18n/load.ts простым fetch; system — /healthz, /readyz, /metrics; v1 — интеграционный API для внешних систем.
// Фронт начал вызывать раздел — убрать его отсюда и перегенерировать (npm run gen:api).
const NOT_USED_BY_WEB = new Set(['common', 'i18n', 'system', 'v1']);
const specs = readdirSync(apiDir)
  .filter((f) => f.endsWith('.yaml') && !NOT_USED_BY_WEB.has(f.slice(0, -'.yaml'.length)))
  .sort();
for (const f of specs) {
  const name = f.slice(0, -'.yaml'.length);
  execFileSync(bin, [join(apiDir, f), '-o', join(outDir, `${name}.d.ts`)], { stdio: ['ignore', 'ignore', 'inherit'] });
}
console.log(`src/api/gen: ${specs.length} файлов`);
