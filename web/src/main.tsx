import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider } from '@tanstack/react-router';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
// Шрифты и иконки — из npm, версии закреплены (решение Q-064; в старом — Google Fonts и cdnjs):
// Inter 400/500/600/700 как partials/fonts.blade.php, Font Awesome 6.4.0 all.min.css как layouts/*.blade.php.
import '@fontsource/inter/400.css';
import '@fontsource/inter/500.css';
import '@fontsource/inter/600.css';
import '@fontsource/inter/700.css';
import '@fortawesome/fontawesome-free/css/all.min.css';
import './i18n';
import './styles/app.css';
import { createAppRouter, createQueryClient } from './router';

const queryClient = createQueryClient();
const router = createAppRouter(queryClient);

const root = document.getElementById('root');
if (!root) throw new Error('#root not found');

// Service worker (public/sw.js) — группа 7 (PWA, решение Q-068).

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
