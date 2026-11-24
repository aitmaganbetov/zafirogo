import { defineConfig, devices } from '@playwright/test';

// Смоук страниц стенда tools/visual (smoke.qa.ts): только новый сервер, без снимков. Сервер не запускается
// (tools/visual/new.sh start).
const executablePath = process.env.PW_CHROMIUM_PATH || undefined;

export default defineConfig({
  testDir: '.',
  testMatch: '*.qa.ts',
  outputDir: '../../test-results/qa',
  fullyParallel: true,
  workers: Number(process.env.QA_WORKERS ?? 4),
  retries: 0,
  timeout: 60_000,
  reporter: [['list']],
  use: {
    ...devices['Desktop Chrome'],
    launchOptions: executablePath ? { executablePath } : {},
  },
});
