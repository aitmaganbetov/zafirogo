import { useNavigate } from '@tanstack/react-router';
import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useDocumentTitle } from '../lib/title';
import { isApiError } from '../api/http';
import { isForbidden } from './errors';

/*
 * Страницы ошибок — стандартные шаблоны Laravel (своих resources/views/errors в старом проекте нет):
 * vendor/laravel/framework/src/Illuminate/Foundation/Exceptions/views/minimal.blade.php, 403.blade.php, 404.blade.php,
 * 500.blade.php. Разметка и классы — как в minimal.blade.php.
 */

function Minimal({ code, message }: { code: string; message: string }) {
  // <body class="antialiased bg-white text-black">
  useEffect(() => {
    const classes = ['antialiased', 'bg-white', 'text-black'];
    document.body.classList.add(...classes);
    return () => document.body.classList.remove(...classes);
  }, []);
  return (
    <div className="relative flex items-top justify-center min-h-screen bg-white text-black sm:items-center sm:pt-0" role="main">
      <div className="max-w-xl mx-auto sm:px-6 lg:px-8">
        <div className="flex items-center pt-8 sm:justify-start sm:pt-0">
          <h1 className="px-4 text-lg text-black border-r border-gray-400">{code}</h1>
          <div className="ml-4 text-lg text-black">{message}</div>
        </div>
      </div>
    </div>
  );
}

/** 403.blade.php: title __('Forbidden'), message __($exception->getMessage() ?: 'Forbidden'). */
export function ForbiddenPage({ message }: { message?: string }) {
  const { t } = useTranslation();
  useDocumentTitle(t('Forbidden'));
  return <Minimal code="403" message={t(message || 'Forbidden')} />;
}

/** 404.blade.php: title и message __('Not Found'). */
export function NotFoundPage() {
  const { t } = useTranslation();
  useDocumentTitle(t('Not Found'));
  return <Minimal code="404" message={t('Not Found')} />;
}

/** 500.blade.php: __('Server Error'). */
export function ServerErrorPage() {
  const { t } = useTranslation();
  useDocumentTitle(t('Server Error'));
  return <Minimal code="500" message={t('Server Error')} />;
}

/**
 * RequireActiveSubscription: подписка клуба не активна (API 402) — на страницу подписки
 * (redirect()->route('billing.show')).
 */
function SubscriptionRedirect() {
  const navigate = useNavigate();
  useEffect(() => {
    void navigate({ href: '/billing', replace: true }); // маршрут — экран подписки (настройки клуба)
  }, [navigate]);
  return null;
}

/** errorComponent маршрутов: 402 — на /billing, 403 (ForbiddenError или ответ API 403), 404 (ответ API 404), иначе 500. */
export function RouteErrorPage({ error }: { error: unknown }) {
  if (isApiError(error) && error.status === 402) return <SubscriptionRedirect />;
  if (isForbidden(error)) return <ForbiddenPage message={error.message} />;
  if (isApiError(error) && error.kind === 'forbidden') return <ForbiddenPage message={error.message} />;
  if (isApiError(error) && error.kind === 'not_found') return <NotFoundPage />;
  return <ServerErrorPage />;
}
