/// <reference types="vitest/config" />
import tailwindcss from '@tailwindcss/vite';
import { tanstackRouter } from '@tanstack/router-plugin/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// Бэкенд zafiro в dev слушает :8080 (ZAFIRO_HTTP_ADDR).
const backend = process.env.ZAFIRO_DEV_BACKEND ?? 'http://localhost:8080';

export default defineConfig({
  // Маршруты — файлы src/routes (tsr.config.json); плагин до react().
  plugins: [tanstackRouter(), react(), tailwindcss()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': { target: backend, changeOrigin: false },
      '/ws': { target: backend.replace(/^http/, 'ws'), ws: true },
      '/healthz': { target: backend },
      '/readyz': { target: backend },
    },
  },
  preview: {
    port: 4173,
    strictPort: true,
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    assetsDir: 'assets',
    // Три страницы: SPA, справка API (docs/api — Swagger UI без стилей SPA; сервер отдаёт её на /docs/api) и wiki (resources/wiki/index.html старого проекта — статичная страница, Q-069; сервер отдаёт
    // dist/wiki/index.html на /wiki или на корне хоста wiki — server/internal/webui/wiki.go).
    rollupOptions: {
      input: { main: 'index.html', wiki: 'wiki/index.html', apidocs: 'docs/api/index.html' },
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    css: false,
  },
});
