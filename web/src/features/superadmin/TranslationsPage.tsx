import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { firstError, qs, saveTranslations, translationsQuery, type TranslationsSearch } from './api';
import { saHref } from './host';
import { useSuperadminFlash } from './flash';

/*
 * Переводы языка — перенос resources/views/superadmin/translations.blade.php (LanguageController::translations(),
 * saveTranslations()). Тексты — в формате Laravel (:name), как в старом редакторе (Q-065); в формат i18next их
 * переводит сервер при выдаче пакета.
 */

export function TranslationsPage({ languageId, search }: { readonly languageId: number; readonly search: TranslationsSearch }) {
  const { t } = useTranslation();
  const flash = useSuperadminFlash();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data } = useQuery(translationsQuery(languageId, search));
  useAdminPageTitle(data ? t('Переводы: {{name}}', { name: data.language.name }) : '');
  const save = useMutation({
    mutationFn: (values: Record<string, string>) => saveTranslations(languageId, values),
    onSuccess: (r) => {
      flash.status(r.message);
      return queryClient.invalidateQueries({ queryKey: ['superadmin'] });
    },
    onError: (e) => flash.error(firstError(e)),
  });
  if (!data) return null;

  const submitFilter = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    void navigate({ href: saHref(`/languages/${languageId}/translations` + qs({ search: String(f.get('search') ?? ''), missing: f.get('missing') === '1' })) });
  };
  const submitSave = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const values: Record<string, string> = {};
    for (const [name, value] of new FormData(e.currentTarget).entries()) {
      const m = /^translations\[(.+)\]$/.exec(name);
      if (m?.[1]) values[m[1]] = String(value);
    }
    save.mutate(values);
  };

  return (
    <div>
      <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <a href={saHref('/languages')} className="text-sm text-gray-600 hover:underline">
            {t('← Языки')}
          </a>
          <h3 className="mt-2 text-2xl font-bold text-gray-900">
            {t('Переводы: {{name}}', { name: data.language.name })} <span className="font-mono text-base text-gray-500">({data.language.code})</span>
          </h3>
          <p className="mt-1 text-sm text-gray-600">
            {t('Переведено {{translated}} из {{total}}. Пустое поле — используется английский текст, а если его нет — русский.', {
              translated: data.translated,
              total: data.source_total,
            })}
          </p>
        </div>
      </div>

      <form method="GET" className="mb-4 flex flex-wrap items-end gap-3 rounded-xl border border-gray-200 bg-white p-4" onSubmit={submitFilter}>
        <label className="min-w-56 flex-1">
          <span className="mb-1 block text-sm font-medium text-gray-700">{t('Поиск')}</span>
          <input type="search" name="search" defaultValue={search.search ?? ''} placeholder={t('Текст на русском или перевод')} className="w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2" />
        </label>
        <label className="flex items-center gap-2 text-sm text-gray-700">
          <input type="checkbox" name="missing" value="1" defaultChecked={search.missing === true} /> {t('Только непереведённые')}
        </label>
        <button type="submit" className="rounded-lg border border-gray-300 px-4 py-2 text-sm">
          {t('Показать')}
        </button>
      </form>

      <form method="POST" className="rounded-xl border border-gray-200 bg-white" onSubmit={submitSave} key={JSON.stringify(data.items)}>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b border-gray-200">
              <tr className="text-left text-gray-600">
                <th className="w-1/2 px-4 py-3">{t('Исходный текст (русский)')}</th>
                <th className="px-4 py-3">{t('Перевод')}</th>
              </tr>
            </thead>
            <tbody>
              {data.items.map((row) => (
                <tr key={row.hash} className="border-b border-gray-100 align-top">
                  <td className="px-4 py-3 text-gray-900">
                    <label htmlFor={`t-${row.hash}`}>{row.source}</label>
                    {row.label !== row.source && <div className="mt-1 font-mono text-xs text-gray-500">{row.label}</div>}
                  </td>
                  <td className="px-4 py-3">
                    <textarea
                      id={`t-${row.hash}`}
                      name={`translations[${row.hash}]`}
                      rows={Array.from(row.source).length > 80 ? 3 : 1}
                      className={`w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 ${row.value === '' ? 'border-amber-300' : ''}`}
                      defaultValue={row.value}
                    ></textarea>
                  </td>
                </tr>
              ))}
              {data.items.length === 0 && (
                <tr>
                  <td colSpan={2} className="px-4 py-8 text-center text-gray-600">
                    {t('Ничего не найдено.')}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <div className="flex flex-wrap items-center justify-between gap-3 p-4">
          <div>
            <Pagination
              page={data.page}
              perPage={data.per_page}
              total={data.total}
              count={data.items.length}
              hrefFor={(p) => saHref(`/languages/${languageId}/translations` + qs({ search: search.search, missing: search.missing, page: p > 1 ? p : undefined }))}
            />
          </div>
          {data.items.length > 0 && (
            <button type="submit" className="brand-button rounded-lg px-5 py-2 text-sm" disabled={save.isPending}>
              {t('Сохранить переводы на странице')}
            </button>
          )}
        </div>
      </form>
    </div>
  );
}
