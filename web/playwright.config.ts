import { defineConfig, devices } from '@playwright/test';

// Браузеры Playwright уже установлены в системе (PLAYWRIGHT_BROWSERS_PATH) — `playwright install` не нужен.
// Если задан PW_CHROMIUM_PATH, используется этот исполняемый файл Chromium.
const executablePath = process.env.PW_CHROMIUM_PATH || undefined;
const port = Number(process.env.E2E_PORT ?? 4173);
// E2E_BASE_URL — проверить уже запущенный сервер (например, бинарник zafiro на :8080).
const baseURL = process.env.E2E_BASE_URL ?? `http://localhost:${port}`;

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? 'list' : [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL,
    trace: 'on-first-retry',
    launchOptions: executablePath ? { executablePath } : {},
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: process.env.E2E_BASE_URL
    ? undefined
    : {
        command: `npm run preview -- --port ${port}`,
        url: baseURL,
        reuseExistingServer: !process.env.CI,
        timeout: 60_000,
      },
});
