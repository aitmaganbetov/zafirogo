// Дерево маршрутов TanStack Router: src/routes/** → src/routeTree.gen.ts (в git не хранится — агенты разделов
// добавляют файлы маршрутов параллельно без конфликтов). Vite-плагин делает то же в dev/build; этот скрипт —
// для typecheck/test без Vite. Конфигурация — tsr.config.json (общая с плагином).
import { Generator, getConfig } from '@tanstack/router-generator';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const config = getConfig({}, root);
await new Generator({ config, root }).run();
