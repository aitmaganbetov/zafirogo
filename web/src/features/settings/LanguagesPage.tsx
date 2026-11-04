import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { session, useSession } from '../../auth/session';
import { SettingsCard } from '../../components/SettingsCard';
import { SettingsLayout } from '../../components/SettingsLayout';
import { firstError, languagesQuery, saveLanguage } from './api';
import { SettingsAnchors } from './SettingsAnchors';
import { useSettingsResult } from './status';

/*
 * Настройки → Языки — перенос resources/views/settings/languages.blade.php (SettingsController::languages(),
 * LanguageController::select()). После смены язык интерфейса переключается сразу: /api/auth/me перечитывается
 * (me.club.locale → I18nGate).
 */
export function LanguagesPage() {
  const { t } = useTranslation();
  const { me } = useSession();
  const qc = useQueryClient();
  const { data } = useQuery(languagesQuery());
  const { status, done, fail, errorOf } = useSettingsResult();
  const select = useMutation({
    mutationFn: (locale: string) => saveLanguage(locale),
    onSuccess: async (r) => {
      await session.reloadMe();
      done(r.message);
      await qc.invalidateQueries({ queryKey: ['settings'] });
    },
    onError: fail('locale'),
  });
  if (!data) return null;
  const error = firstError(errorOf('locale'), 'language', 'locale', 'code', 'name');
  const anchors: ReadonlyArray<readonly [string, string]> = [
    ['interface-language', t('Язык интерфейса')],
    ['club-languages', t('Языки')],
  ];

  return (
    <SettingsLayout status={status} anchors={<SettingsAnchors anchors={anchors} />}>
      <h4 className="text-xl font-semibold text-gray-900 dark:text-white">{t('Языки')}</h4>
      {error && <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</div>}

      <SettingsCard id="interface-language" aria-labelledby="interface-language-title">
        <h5 id="interface-language-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Язык интерфейса')}</h5>
        <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Все пользователи клуба видят систему на этом языке.')}</p>
        <form
          method="POST"
          action="/settings/language"
          className="flex flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (me?.read_only) return;
            select.mutate(String(new FormData(e.currentTarget).get('locale') ?? ''));
          }}
        >
          <label className="min-w-48 flex-1">
            <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Язык')}</span>
            <select key={data.locale} name="locale" className="w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-gray-900" defaultValue={data.locale}>
              {data.languages.filter((l) => l.is_enabled).map((l) => (
                <option key={l.code} value={l.code}>{l.name} ({l.code})</option>
              ))}
            </select>
          </label>
          <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">{t('Применить')}</button>
        </form>
      </SettingsCard>

      <SettingsCard id="club-languages" aria-labelledby="languages-title">
        <h5 id="languages-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Языки')}</h5>
        <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Список языков и переводы общие для всех клубов, их ведёт администрация Zafiro. Выключенные языки нельзя выбрать.')}</p>
        <ul className="flex flex-wrap gap-2 text-sm">
          {data.languages.map((l) => (
            <li key={l.code} className={`rounded-full border border-gray-200 px-3 py-1 ${l.is_enabled ? 'text-gray-800 dark:text-gray-200' : 'text-gray-400 line-through'}`}>
              {l.name} <span className="font-mono text-xs text-gray-500">{l.code}</span>
            </li>
          ))}
        </ul>
      </SettingsCard>
    </SettingsLayout>
  );
}
