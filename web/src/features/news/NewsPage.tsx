import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { can, useSession } from '../../auth/session';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatDate } from '../../lib/datetime';
import { allErrors, createNews, deleteNews, newsQuery, updateNews, type NewsItem, type NewsPage as NewsPageData } from './api';

/*
 * Новости клуба — перенос resources/views/news/index.blade.php в режиме клуба (NewsController::index(),
 * layout layouts.admin, $routes news.*). Режим платформы (суперадминка, Superadmin\NewsController) — группа 6.
 * Формы отправляются как в Blade: FormData формы (hidden is_published=0 + checkbox 1, файл картинки).
 */

const FIELD = 'w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-white';
const LABEL = 'mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400';

/**
 * Режим платформы (Superadmin\NewsController::index(): layout суперадминки, свои маршруты и вступление, все действия
 * доступны). По умолчанию — новости клуба.
 */
export interface NewsMode {
  readonly key: readonly string[];
  readonly list: (page?: number) => Promise<NewsPageData>;
  readonly create: (form: FormData) => Promise<{ message: string }>;
  readonly update: (id: number, form: FormData) => Promise<{ message: string }>;
  readonly remove: (id: number) => Promise<{ message: string }>;
  readonly intro: string;
  readonly hrefFor: (page: number) => string;
  /** Сообщение и ошибки — ещё и в layout (session('status') / $errors в layouts/superadmin.blade.php). */
  readonly onStatus?: (message: string) => void;
  readonly onErrors?: (errors: string[]) => void;
}

export function NewsPage({ page, mode }: { page?: number; mode?: NewsMode }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const queryClient = useQueryClient();
  useAdminPageTitle(t('Новости'));
  const { data } = useQuery(mode ? { queryKey: [...mode.key, page ?? 1] as (string | number)[], queryFn: () => mode.list(page) } : newsQuery(page));
  const [status, setStatus] = useState<string | null>(null);
  const [errors, setErrors] = useState<string[]>([]);
  const readOnly = mode ? false : me?.read_only === true;
  const tz = me?.club?.timezone ?? 'UTC';
  // В клубе — по правам сотрудника; новости платформы суперадмина — всегда.
  const may = (action: string) => mode !== undefined || can(me, `news.${action}`);

  const done = (message: string) => {
    setStatus(message);
    setErrors([]);
    mode?.onStatus?.(message);
    return queryClient.invalidateQueries({ queryKey: mode ? mode.key : ['news'] });
  };
  const fail = (e: unknown) => {
    const list = allErrors(e);
    setErrors(list);
    mode?.onErrors?.(list);
  };

  const create = useMutation({ mutationFn: (f: FormData) => (mode ? mode.create(f) : createNews(f)), onError: fail });
  const update = useMutation({ mutationFn: ({ id, form }: { id: number; form: FormData }) => (mode ? mode.update(id, form) : updateNews(id, form)), onError: fail });
  const remove = useMutation({ mutationFn: (id: number) => (mode ? mode.remove(id) : deleteNews(id)), onSuccess: (r) => done(r.message), onError: fail });

  const submitCreate = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    create.mutate(new FormData(form), {
      onSuccess: (r) => {
        form.reset();
        void done(r.message);
      },
    });
  };
  const submitUpdate = (id: number) => (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    update.mutate({ id, form: new FormData(e.currentTarget) }, { onSuccess: (r) => void done(r.message) });
  };
  const submitDelete = (id: number) => (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (window.confirm(t('Удалить новость?'))) remove.mutate(id);
  };

  if (!data) return null;

  return (
    // Layout суперадминки сам даёт отступы (в Blade — класс только для layouts.admin).
    <div className={mode ? '' : 'p-6 lg:p-8'}>
      <header className="mb-6">
        <h3 className="mb-2 text-2xl font-bold text-gray-900 dark:text-white">{t('Новости')}</h3>
        <p className="text-sm text-gray-600 dark:text-gray-400">
          {mode ? mode.intro : t('Новости клуба показываются на главном экране агента на ПК клуба (вторая карточка).')}
        </p>
      </header>

      {status && <div className="mb-5 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700 dark:border-green-800 dark:bg-green-900/20 dark:text-green-300">{status}</div>}
      {errors.length > 0 && (
        <div className="mb-5 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300" role="alert">
          {errors.map((error, i) => (
            <div key={i}>{error}</div>
          ))}
        </div>
      )}

      {may('create') && (
        <form method="POST" action="/news" encType="multipart/form-data" className="mb-8 grid gap-3 rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-800 dark:bg-gray-900" onSubmit={submitCreate}>
          <h4 className="font-semibold text-gray-900 dark:text-white">{t('Новая новость')}</h4>
          <div>
            <label htmlFor="news-title" className={LABEL}>
              {t('Заголовок')}
            </label>
            <input id="news-title" name="title" maxLength={150} required className={FIELD} />
          </div>
          <div>
            <label htmlFor="news-body" className={LABEL}>
              {t('Текст')}
            </label>
            <textarea id="news-body" name="body" rows={5} maxLength={5000} required className={FIELD}></textarea>
          </div>
          <div className="flex flex-wrap items-end gap-4">
            <div>
              <label htmlFor="news-image" className={LABEL}>
                {t('Картинка (PNG или JPG, до 2 МБ)')}
              </label>
              <input id="news-image" type="file" name="image" accept="image/png,image/jpeg" className="text-sm text-gray-700 dark:text-gray-300" />
            </div>
            <label className="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input type="hidden" name="is_published" value="0" />
              <input type="checkbox" name="is_published" value="1" defaultChecked /> {t('Опубликовать сразу')}
            </label>
            <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm" disabled={readOnly || create.isPending}>
              {t('Добавить новость')}
            </button>
          </div>
        </form>
      )}

      <div className="space-y-3">
        {data.items.length === 0 && (
          <p className="rounded-xl border border-dashed border-gray-300 p-8 text-center text-sm text-gray-500 dark:border-gray-700 dark:text-gray-400">{t('Новостей пока нет.')}</p>
        )}
        {data.items.map((item: NewsItem) => (
          <details key={item.id} className="rounded-xl border border-gray-200 bg-white dark:border-gray-800 dark:bg-gray-900">
            <summary className="flex cursor-pointer items-center gap-4 p-4">
              {item.image_url && <img src={item.image_url} alt="" className="h-14 w-24 flex-none rounded-lg object-cover" />}
              <span className="min-w-0 flex-1">
                <strong className="block truncate text-gray-900 dark:text-white">{item.title}</strong>
                <small className="text-gray-500 dark:text-gray-400">{item.created_at ? formatDate(item.created_at, tz, 'd.m.Y H:i') : ''}</small>
              </span>
              <span
                className={`rounded-full px-2 py-1 text-xs font-medium ${item.is_published ? 'bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-300' : 'bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400'}`}
              >
                {item.is_published ? t('Опубликована') : t('Черновик')}
              </span>
            </summary>
            <div className="grid gap-3 border-t border-gray-100 p-4 dark:border-gray-800">
              {may('update') && (
                <form method="POST" encType="multipart/form-data" className="grid gap-3" onSubmit={submitUpdate(item.id)}>
                  <div>
                    <label htmlFor={`news-title-${item.id}`} className={LABEL}>
                      {t('Заголовок')}
                    </label>
                    <input id={`news-title-${item.id}`} name="title" maxLength={150} required defaultValue={item.title} className={FIELD} />
                  </div>
                  <div>
                    <label htmlFor={`news-body-${item.id}`} className={LABEL}>
                      {t('Текст')}
                    </label>
                    <textarea id={`news-body-${item.id}`} name="body" rows={5} maxLength={5000} required defaultValue={item.body} className={FIELD}></textarea>
                  </div>
                  <div className="flex flex-wrap items-end gap-4">
                    <div>
                      <label htmlFor={`news-image-${item.id}`} className={LABEL}>
                        {t('Новая картинка')}
                      </label>
                      <input id={`news-image-${item.id}`} type="file" name="image" accept="image/png,image/jpeg" className="text-sm text-gray-700 dark:text-gray-300" />
                    </div>
                    {item.has_image && (
                      <label className="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                        <input type="checkbox" name="remove_image" value="1" /> {t('Убрать картинку')}
                      </label>
                    )}
                    <label className="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                      <input type="hidden" name="is_published" value="0" />
                      <input type="checkbox" name="is_published" value="1" defaultChecked={item.is_published} /> {t('Опубликована')}
                    </label>
                    <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm" disabled={readOnly || update.isPending}>
                      {t('Сохранить')}
                    </button>
                  </div>
                </form>
              )}
              {may('delete') && (
                <form method="POST" onSubmit={submitDelete(item.id)}>
                  <button type="submit" className="text-sm text-red-600 hover:underline" disabled={readOnly}>
                    {t('Удалить новость')}
                  </button>
                </form>
              )}
            </div>
          </details>
        ))}
      </div>
      <div className="mt-4">
        <Pagination page={data.page} perPage={data.per_page} total={data.total} count={data.items.length} hrefFor={mode ? mode.hrefFor : (p) => (p > 1 ? `/news?page=${p}` : '/news')} />
      </div>
    </div>
  );
}
