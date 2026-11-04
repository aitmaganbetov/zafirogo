import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { isApiError } from '../../api/http';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { createLanguage, deleteLanguage, exportTranslations, firstError, importTranslations, languagesQuery, updateLanguage, type ImportResult } from './api';
import { saHref } from './host';
import { useSuperadminFlash } from './flash';

/*
 * Языки и переводы — перенос resources/views/superadmin/languages.blade.php (LanguageController: index, store, update,
 * destroy, export, import). Ошибки файла импорта — своим блоком над формами, как в шаблоне.
 */

export function LanguagesPage() {
  const { t } = useTranslation();
  useAdminPageTitle(t('Языки и переводы'));
  const flash = useSuperadminFlash();
  const queryClient = useQueryClient();
  const { data } = useQuery(languagesQuery());
  const [fileError, setFileError] = useState<string | null>(null);
  const [invalid, setInvalid] = useState<ImportResult | null>(null);

  const ok = (r: { message: string }) => {
    flash.status(r.message);
    return queryClient.invalidateQueries({ queryKey: ['superadmin'] });
  };
  const fail = (e: unknown) => flash.error(firstError(e));
  const toggle = useMutation({ mutationFn: ({ id, on }: { id: number; on: boolean }) => updateLanguage(id, { is_enabled: on }), onSuccess: ok, onError: fail });
  const remove = useMutation({ mutationFn: deleteLanguage, onSuccess: ok, onError: fail });
  const create = useMutation({ mutationFn: ({ code, name }: { code: string; name: string }) => createLanguage(code, name), onSuccess: ok, onError: fail });
  const upload = useMutation({
    mutationFn: importTranslations,
    onSuccess: (r) => {
      setFileError(null);
      setInvalid(r.invalid_count > 0 ? r : null);
      return ok(r);
    },
    onError: (e) => {
      setInvalid(null);
      setFileError(isApiError(e) ? (e.field('file') ?? e.field('language') ?? null) : null);
      fail(e);
    },
  });
  const download = useMutation({ mutationFn: ({ code, missing }: { code: string; missing: boolean }) => exportTranslations(code, missing), onError: fail });
  if (!data) return null;
  const others = data.items.filter((l) => l.code !== 'ru');

  const submitCreate = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    const f = new FormData(form);
    create.mutate({ code: String(f.get('code') ?? ''), name: String(f.get('name') ?? '') }, { onSuccess: () => form.reset() });
  };
  const submitExport = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    download.mutate({ code: String(f.get('language') ?? ''), missing: f.get('missing') === '1' });
  };
  const submitImport = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    upload.mutate(new FormData(e.currentTarget));
  };

  return (
    <>
      <p className="mb-4 text-sm text-gray-600 dark:text-gray-400">
        {t('Языки и переводы общие для всех клубов. Клуб без своего выбора видит язык по умолчанию; язык, выбранный хотя бы одним клубом, выключить нельзя.')}
      </p>
      <section className="mt-6 rounded-lg border border-gray-200 p-4 dark:border-gray-700" aria-labelledby="languages-title">
        <h5 id="languages-title" className="mb-2 font-semibold text-gray-900 dark:text-white">
          {t('Языки')}
        </h5>
        <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Список языков и переводы общие для всех клубов. Выключенные языки нельзя выбрать.')}</p>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-gray-600">
                <th className="px-3 py-2">{t('Язык')}</th>
                <th className="px-3 py-2">{t('Код')}</th>
                <th className="px-3 py-2 text-right">{t('Переведено')}</th>
                <th className="px-3 py-2">{t('Показывать')}</th>
                <th className="px-3 py-2 text-right">{t('Клубов')}</th>
                <th className="px-3 py-2"></th>
              </tr>
            </thead>
            <tbody>
              {data.items.map((language) => (
                <tr key={language.id} className="border-t border-gray-100">
                  <td className="px-3 py-2 font-medium text-gray-900">
                    {language.name}
                    {language.is_default && (
                      <>
                        {' '}
                        <span className="ml-1 rounded-full bg-green-100 px-2 py-1 text-xs text-green-800">{t('по умолчанию')}</span>
                      </>
                    )}
                  </td>
                  <td className="px-3 py-2 font-mono text-gray-700">{language.code}</td>
                  <td className="px-3 py-2 text-right tabular-nums text-gray-700">
                    {language.code === 'ru' ? t('исходный') : `${language.translated_count} / ${data.translation_total}`}
                  </td>
                  <td className="px-3 py-2">
                    <form
                      method="POST"
                      onSubmit={(e) => {
                        e.preventDefault();
                        toggle.mutate({ id: language.id, on: !language.is_enabled });
                      }}
                    >
                      <button
                        type="submit"
                        className="rounded-lg border border-gray-300 px-3 py-1 text-sm"
                        aria-label={language.is_enabled ? t('Выключить язык {{name}}', { name: language.name }) : t('Включить язык {{name}}', { name: language.name })}
                      >
                        {language.is_enabled ? t('Да') : t('Нет')}
                      </button>
                    </form>
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums text-gray-700">{language.clubs_count}</td>
                  <td className="px-3 py-2">
                    <div className="flex flex-wrap items-center justify-end gap-2">
                      <a href={saHref(`/languages/${language.id}/translations`)} className="rounded-lg border border-gray-300 px-3 py-1 text-sm">
                        {t('Переводы')}
                      </a>
                      {!language.is_system && (
                        <form
                          method="POST"
                          onSubmit={(e) => {
                            e.preventDefault();
                            if (window.confirm(t('Удалить язык {{name}} и все его переводы?', { name: language.name }))) remove.mutate(language.id);
                          }}
                        >
                          <button type="submit" className="rounded-lg border border-red-200 px-3 py-1 text-sm text-red-700">
                            {t('Удалить')}
                          </button>
                        </form>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        <form method="POST" className="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-[120px_minmax(0,1fr)_auto] sm:items-end" onSubmit={submitCreate}>
          <label>
            <span className="mb-1 block text-sm font-medium text-gray-700">{t('Код')}</span>
            <input name="code" required maxLength={16} pattern="[a-z]{2,3}(-[a-z0-9]{2,8})?" placeholder="uz" className="w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 font-mono" />
          </label>
          <label>
            <span className="mb-1 block text-sm font-medium text-gray-700">{t('Название')}</span>
            <input name="name" required maxLength={64} placeholder={t('Например: Oʻzbekcha')} className="w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2" />
          </label>
          <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">
            {t('Добавить язык')}
          </button>
        </form>
      </section>

      <section className="mt-6 rounded-lg border border-gray-200 p-4 dark:border-gray-700" aria-labelledby="translation-files-title">
        <h5 id="translation-files-title" className="mb-2 font-semibold text-gray-900 dark:text-white">
          {t('Экспорт и импорт переводов')}
        </h5>
        <p className="mb-4 text-sm text-gray-600 dark:text-gray-400">
          {t('Выгрузите язык в CSV, переведите столбец с кодом языка в Excel или Google Таблицах и загрузите файл обратно. Пустые ячейки ничего не удаляют.')}
        </p>

        {fileError && (
          <div className="mb-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700" role="alert">
            {fileError}
          </div>
        )}
        {invalid && (
          <div className="mb-4 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900" role="status">
            <p className="font-medium">
              {t(
                'Пропущена {{count}} строка: подстановки ({{name}} и т. п.) в переводе не совпадают с оригиналом.|Пропущено {{count}} строки: подстановки ({{name}} и т. п.) в переводе не совпадают с оригиналом.|Пропущено {{count}} строк: подстановки ({{name}} и т. п.) в переводе не совпадают с оригиналом.',
                // В ключе Laravel — «:name» как текст: каталог превратил его в {{name}}, возвращаем подстановкой.
                { count: invalid.invalid_count, name: ':name' },
              )}
            </p>
            <ul className="mt-2 list-disc pl-5">
              {invalid.invalid.map((source, i) => (
                <li key={i}>{source}</li>
              ))}
            </ul>
          </div>
        )}

        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <form method="GET" className="space-y-3 rounded-lg bg-gray-50 p-4 dark:bg-gray-800" onSubmit={submitExport}>
            <h6 className="font-medium text-gray-900 dark:text-white">{t('Экспорт')}</h6>
            <label className="block">
              <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Язык')}</span>
              <select name="language" className="w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-gray-900">
                {others.map((l) => (
                  <option key={l.code} value={l.code}>
                    {l.name} ({l.code})
                  </option>
                ))}
              </select>
            </label>
            <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input type="checkbox" name="missing" value="1" /> {t('Только непереведённые')}
            </label>
            <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">
              <i className="fas fa-file-arrow-down mr-1"></i>
              {t('Скачать CSV')}
            </button>
          </form>

          <form method="POST" encType="multipart/form-data" className="space-y-3 rounded-lg bg-gray-50 p-4 dark:bg-gray-800" onSubmit={submitImport}>
            <h6 className="font-medium text-gray-900 dark:text-white">{t('Импорт')}</h6>
            <label className="block">
              <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Язык')}</span>
              <select name="language" className="w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-gray-900">
                {others.map((l) => (
                  <option key={l.code} value={l.code}>
                    {l.name} ({l.code})
                  </option>
                ))}
              </select>
            </label>
            <label className="block">
              <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Файл CSV')}</span>
              <input type="file" name="file" accept=".csv,text/csv" required className="block w-full text-sm text-gray-700 dark:text-gray-300" />
            </label>
            <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm" disabled={upload.isPending}>
              <i className="fas fa-file-arrow-up mr-1"></i>
              {t('Загрузить')}
            </button>
          </form>
        </div>
      </section>
    </>
  );
}
