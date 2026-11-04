import { useEffect, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useDocumentTitle } from '../lib/title';

/*
 * Экран входа — перенос resources/views/layouts/auth.blade.php (разметка и классы один-в-один):
 * <body class="auth-body">, .auth-shell: визуальная часть с логотипом и карточка формы.
 */
export function AuthLayout({ title, children }: { title: string; children: ReactNode }) {
  const { t } = useTranslation();
  useDocumentTitle(title);

  useEffect(() => {
    document.body.classList.add('auth-body');
    return () => {
      document.body.classList.remove('auth-body');
    };
  }, []);

  return (
    <div className="auth-shell">
      <section className="auth-visual">
        <div className="auth-grid"></div>
        <div className="auth-brand">
          <img src="/images/logo.svg" alt="Zafiro" />
        </div>
        <div className="auth-message">
          <span>{t('Система управления клубом')}</span>
          <h1>{t('Всё необходимое для эффективной работы')}</h1>
          <p>{t('Управляйте залом, кассой, складом и настройками клуба из единого пространства.')}</p>
        </div>
      </section>
      <main className="auth-main">
        <div className="auth-card">{children}</div>
      </main>
    </div>
  );
}
