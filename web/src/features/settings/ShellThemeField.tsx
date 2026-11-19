import { useTranslation } from 'react-i18next';
import type { components } from '../../api/gen/settings';

type ShellThemeOption = components['schemas']['ShellThemeOption'];

/*
 * «Тема оформления на ПК» карточки «Агент на ПК» — перенос fieldset из resources/views/settings/club.blade.php
 * (коммит старого e38b764): радиокнопки тем со свотчами ShellThemes::swatch(), названия — ShellThemes::names().
 * Цвета приходят с сервера (shell_themes): один источник с тем, что получает агент.
 */

/** ShellThemes::names(): ключ → название (ключ перевода). */
const THEME_NAMES: Record<string, string> = {
  standard: 'Стандартная (фиолетовая)',
  blue: 'Синяя',
  green: 'Зелёная',
  red: 'Красная',
  orange: 'Оранжевая',
  teal: 'Бирюзовая',
  pink: 'Розовая',
};

export function ShellThemeField({ value, themes }: { value: string; themes: ShellThemeOption[] }) {
  const { t } = useTranslation();
  return (
    <fieldset className="rounded-xl border border-gray-200 p-4 md:col-span-2 dark:border-gray-700">
      <legend className="px-1 text-sm font-medium text-gray-700 dark:text-gray-300">{t('Тема оформления на ПК')}</legend>
      <p className="mb-3 text-xs text-gray-500">{t('Цвета экрана гостя и экрана входа на ПК клуба. Агент применит тему в течение минуты.')}</p>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {themes.flatMap((theme) => {
          const name = THEME_NAMES[theme.key];
          return name ? [{ theme, name }] : [];
        }).map(({ theme, name }) => (
          <label key={theme.key} className="flex cursor-pointer items-center gap-2 rounded-lg border border-gray-200 p-2 text-sm text-gray-700 has-[:checked]:border-gray-900 has-[:checked]:ring-1 has-[:checked]:ring-gray-900 has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-blue-500 dark:border-gray-700 dark:text-gray-300 dark:has-[:checked]:border-white dark:has-[:checked]:ring-white">
            <input type="radio" name="agent_shell_theme" value={theme.key} className="sr-only" defaultChecked={value === theme.key} />
            <span className="flex h-7 w-10 flex-none items-end gap-0.5 rounded p-1" style={{ background: theme.bg_1 }} aria-hidden="true">
              <span className="h-full flex-1 rounded-sm" style={{ background: theme.accent }}></span>
              <span className="h-2/3 flex-1 rounded-sm" style={{ background: theme.accent_2 }}></span>
            </span>
            {t(name)}
          </label>
        ))}
      </div>
    </fieldset>
  );
}
