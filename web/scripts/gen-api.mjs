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
const specs = readdirSync(apiDir)
  .filter((f) => f.endsWith('.yaml'))
  .sort();
for (const f of specs) {
  const name = f.slice(0, -'.yaml'.length);
  execFileSync(bin, [join(apiDir, f), '-o', join(outDir, `${name}.d.ts`)], { stdio: ['ignore', 'ignore', 'inherit'] });
}
console.log(`src/api/gen: ${specs.length} файлов`);
