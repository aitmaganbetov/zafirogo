import { defineConfig, devices } from '@playwright/test';

// Визуальное сравнение старого Laravel и нового сервера (tools/visual/README.md). Запуск — tools/visual/run.sh
// (task visual): он поднимает оба приложения, затем `playwright test -c e2e/visual/playwright.config.ts`
// и tools/visual/compare.mjs. Серверы здесь не запускаются (webServer нет).
const executablePath = process.env.PW_CHROMIUM_PATH || undefined;

export default defineConfig({
  testDir: '.',
  testMatch: '*.visual.ts',
  outputDir: '../../test-results/visual',
  fullyParallel: true,
  workers: Number(process.env.VISUAL_WORKERS ?? 2),
  retries: 0,
  timeout: 120_000,
  reporter: [['list']],
  use: {
    ...devices['Desktop Chrome'],
    launchOptions: {
      ...(executablePath ? { executablePath } : {}),
      // Общая память рендерера — в /dev/shm (tmpfs), а не во временных файлах на диске (умолчание Playwright в Linux):
      // полностраничные снимки — десятки МБ, и при почти полном диске вкладка падала («Target crashed»).
      ignoreDefaultArgs: ['--disable-dev-shm-usage'],
      // Одинаковый рендеринг шрифтов и цветов в обоих снимках.
      args: ['--font-render-hinting=none', '--disable-skia-runtime-opts', '--force-color-profile=srgb'],
    },
  },
});
