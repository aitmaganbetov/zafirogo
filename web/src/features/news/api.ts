import { queryOptions } from '@tanstack/react-query';
import { apiFetch, createApiClient, errorFromResponse, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/news';
import { intParam } from '../../lib/searchParams';

/*
 * Новости клуба (api/news.yaml). Laravel: NewsController, NewsManager. Формы — multipart (картинка), как
 * enctype="multipart/form-data" старого шаблона. Ключи Query — ['news', …].
 */

export const newsApi = createApiClient<paths>();

type S = components['schemas'];
export type NewsItem = S['NewsItem'];
export type NewsPage = S['NewsPage'];
export type NewsMessage = S['NewsMessage'];

export interface NewsSearch {
  readonly page?: number;
}

export function parseNewsSearch(s: Record<string, unknown>): NewsSearch {
  return { page: intParam(s.page, 2) };
}

export const newsQuery = (page?: number) =>
  queryOptions({
    queryKey: ['news', page ?? 1],
    queryFn: ({ signal }) => unwrap(newsApi.GET('/api/news', { params: { query: { page } }, signal })),
  });

/** Отправка формы новости как multipart (поля формы и файл — как браузер отправлял форму Blade). */
async function sendForm(method: 'POST' | 'PATCH', url: string, form: FormData): Promise<NewsMessage> {
  const res = await apiFetch(new Request(new URL(url, location.origin), { method, body: form, credentials: 'same-origin' }));
  if (!res.ok) throw await errorFromResponse(res);
  return (await res.json()) as NewsMessage;
}

export const createNews = (form: FormData) => sendForm('POST', '/api/news', form);
export const updateNews = (id: number, form: FormData) => sendForm('PATCH', `/api/news/${id}`, form);
export const deleteNews = (id: number) => unwrap(newsApi.DELETE('/api/news/{newsId}', { params: { path: { newsId: id } } }));
