import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { STORAGE_KEYS, applyStoredTheme, writeStorage } from './storage';

/*
 * Дневная / ночная тема — перенос кнопки [data-theme-toggle] и её скрипта из layouts/admin.blade.php.
 * Класс .dark на <html> включает все dark:-стили (@custom-variant dark в styles/app.css).
 * Выбор хранится в браузере (zafiro.theme). Чтобы страница не мигала при загрузке, тот же ключ
 * читает inline-скрипт в web/index.html (см. layouts/README.md).
 */

export function ThemeToggle() {
  const { t } = useTranslation();
  const [dark, setDark] = useState(applyStoredTheme);

  const toggle = () => {
    const next = document.documentElement.classList.toggle('dark');
    writeStorage(STORAGE_KEYS.theme, next ? 'dark' : 'light');
    setDark(next);
  };

  const label = dark ? t('Дневная тема') : t('Ночная тема');

  return (
    <button type="button" className="admin-icon-button" data-theme-toggle aria-pressed={dark} aria-label={label} title={label} onClick={toggle}>
      <i className={dark ? 'fas fa-sun' : 'fas fa-moon'} aria-hidden="true"></i>
    </button>
  );
}
