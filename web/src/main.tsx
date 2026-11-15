import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider } from '@tanstack/react-router';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
// Шрифты и иконки — из npm, версии закреплены (решение Q-064; в старом — Google Fonts и cdnjs):
// Inter 400/500/600/700 как partials/fonts.blade.php, Font Awesome 6.4.0 как layouts/*.blade.php — поднабор
// используемых иконок из того же пакета (scripts/gen-icons.mjs, FE-05): те же классы и глифы, без 2 350 лишних.
import '@fontsource/inter/400.css';
import '@fontsource/inter/500.css';
import '@fontsource/inter/600.css';
import '@fontsource/inter/700.css';
import './styles/icons/fa-subset.css';
import './i18n';
import { warmI18n } from './i18n/load';
import './styles/app.css';
import { createAppRouter, createQueryClient } from './router';
import { registerServiceWorker } from './pwa/register';

// Переводы — параллельно с сессией и данными страницы (FE-04).
warmI18n();

const queryClient = createQueryClient();
const router = createAppRouter(queryClient);

const root = document.getElementById('root');
if (!root) throw new Error('#root not found');

// Service worker (public/sw.js, решение Q-068) — только в prod-сборке (в dev Vite отдаёт исходники, кэш мешал бы).
registerServiceWorker(import.meta.env.PROD);

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
