import { useTranslation } from 'react-i18next';

/*
 * Простая пагинация — вид Laravel `simple-tailwind.blade.php` (vendor/laravel/framework/src/Illuminate/Pagination/
 * resources/views/simple-tailwind.blade.php, разметка и классы один-в-один): «« Назад» / «Вперёд »». Для списков с
 * keyset-пагинацией (CLAUDE.md «Производительность»), где номера страниц неизвестны. Без страниц — ничего
 * (`@if ($paginator->hasPages())`). Ссылки — обычные <a href> (переход перехватывает роутер).
 */

export interface SimplePaginationProps {
  /** URL предыдущей страницы; null — первая страница. */
  readonly prevHref: string | null;
  /** URL следующей страницы; null — последняя. */
  readonly nextHref: string | null;
}

const DISABLED =
  'inline-flex items-center px-4 py-2 text-sm font-medium text-gray-600 bg-white border border-gray-300 cursor-not-allowed leading-5 rounded-md dark:text-gray-300 dark:bg-gray-700 dark:border-gray-600';
const LINK =
  'inline-flex items-center px-4 py-2 text-sm font-medium text-gray-800 bg-white border border-gray-300 leading-5 rounded-md hover:text-gray-700 focus:outline-none focus:ring ring-gray-300 focus:border-blue-300 active:bg-gray-100 active:text-gray-800 transition ease-in-out duration-150 dark:bg-gray-800 dark:border-gray-600 dark:text-gray-200 dark:focus:border-blue-700 dark:active:bg-gray-700 dark:active:text-gray-300 hover:bg-gray-100 dark:hover:bg-gray-900 dark:hover:text-gray-200';

const unescape = (s: string) => s.replace(/&laquo;/g, '«').replace(/&raquo;/g, '»').replace(/&amp;/g, '&');

export function SimplePagination(props: SimplePaginationProps) {
  const { t } = useTranslation();
  if (props.prevHref === null && props.nextHref === null) return null;
  const prev = unescape(t('pagination.previous'));
  const next = unescape(t('pagination.next'));
  return (
    <nav role="navigation" aria-label={t('Pagination Navigation')} className="flex gap-2 items-center justify-between">
      {props.prevHref === null ? (
        <span className={DISABLED}>{prev}</span>
      ) : (
        <a href={props.prevHref} rel="prev" className={LINK}>
          {prev}
        </a>
      )}
      {props.nextHref !== null ? (
        <a href={props.nextHref} rel="next" className={LINK}>
          {next}
        </a>
      ) : (
        <span className={DISABLED}>{next}</span>
      )}
    </nav>
  );
}
