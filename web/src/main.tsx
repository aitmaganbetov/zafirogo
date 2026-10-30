import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import './i18n';
import './styles/app.css';

const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: 30_000, retry: 1 } },
});

const root = document.getElementById('root');
if (!root) throw new Error('#root not found');

// Service worker (public/sw.js, перенесён из Zafiro) пока НЕ регистрируется: он рассчитан на
// Laravel-пути (/offline, /build/*). См. docs/open-questions/infra.md.

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
);
