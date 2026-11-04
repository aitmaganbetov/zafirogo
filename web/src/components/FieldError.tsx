import { useTranslation } from 'react-i18next';

/**
 * Ошибка поля под инпутом — `@error('field')<p class="…">{{ $message }}</p>@enderror`. Классы — из шаблона экрана
 * (у разных экранов разные), текст — ключ перевода с сервера.
 */
export function FieldError({ message, className }: { message: string | null | undefined; className: string }) {
  const { t } = useTranslation();
  if (!message) return null;
  return <p className={className}>{t(message)}</p>;
}
