import { useTranslation } from 'react-i18next';
import { paginationElements } from './paginationElements';

/*
 * Пагинация — вид Laravel `$paginator->links()`: vendor/laravel/framework/src/Illuminate/Pagination/resources/views/
 * tailwind.blade.php (разметка и классы один-в-один), элементы — UrlWindow (onEachSide = 3).
 * Ссылки — обычные <a href> (переход перехватывает роутер). Без страниц (lastPage ≤ 1) — ничего.
 */

export interface PaginationProps {
  /** Текущая страница (с 1). */
  readonly page: number;
  readonly perPage: number;
  /** Всего записей. */
  readonly total: number;
  /** Записей на текущей странице. */
  readonly count: number;
  /** URL страницы (сохраните остальные параметры запроса, как withQueryString()). */
  readonly hrefFor: (page: number) => string;
}

const unescape = (s: string) => s.replace(/&laquo;/g, '«').replace(/&raquo;/g, '»').replace(/&amp;/g, '&');

const PREV_PATH = 'M12.707 5.293a1 1 0 010 1.414L9.414 10l3.293 3.293a1 1 0 01-1.414 1.414l-4-4a1 1 0 010-1.414l4-4a1 1 0 011.414 0z';
const NEXT_PATH = 'M7.293 14.707a1 1 0 010-1.414L10.586 10 7.293 6.707a1 1 0 011.414-1.414l4 4a1 1 0 010 1.414l-4 4a1 1 0 01-1.414 0z';

const MOBILE_DISABLED =
  'inline-flex items-center px-4 py-2 text-sm font-medium text-gray-600 bg-white border border-gray-300 cursor-not-allowed leading-5 rounded-md dark:text-gray-300 dark:bg-gray-700 dark:border-gray-600';
const MOBILE_LINK =
  'inline-flex items-center px-4 py-2 text-sm font-medium text-gray-800 bg-white border border-gray-300 leading-5 rounded-md hover:text-gray-700 focus:outline-none focus:ring ring-gray-300 focus:border-blue-300 active:bg-gray-100 active:text-gray-800 transition ease-in-out duration-150 dark:bg-gray-800 dark:border-gray-600 dark:text-gray-200 dark:focus:border-blue-700 dark:active:bg-gray-700 dark:active:text-gray-300 hover:bg-gray-100 dark:hover:bg-gray-900 dark:hover:text-gray-200';

function Arrow({ d }: { d: string }) {
  return (
    <svg className="w-5 h-5" fill="currentColor" viewBox="0 0 20 20">
      <path fillRule="evenodd" d={d} clipRule="evenodd" />
    </svg>
  );
}

export function Pagination({ page, perPage, total, count, hrefFor }: PaginationProps) {
  const { t } = useTranslation();
  const lastPage = Math.max(1, Math.ceil(total / perPage));
  if (lastPage <= 1) return null; // @if ($paginator->hasPages())
  const onFirst = page <= 1;
  const hasMore = page < lastPage;
  const firstItem = count > 0 ? (page - 1) * perPage + 1 : null;
  const lastItem = firstItem !== null ? firstItem + count - 1 : null;
  const prev = unescape(t('pagination.previous'));
  const next = unescape(t('pagination.next'));

  return (
    <nav role="navigation" aria-label={t('Pagination Navigation')}>
      <div className="flex gap-2 items-center justify-between sm:hidden">
        {onFirst ? (
          <span className={MOBILE_DISABLED}>{prev}</span>
        ) : (
          <a href={hrefFor(page - 1)} rel="prev" className={MOBILE_LINK}>
            {prev}
          </a>
        )}
        {hasMore ? (
          <a href={hrefFor(page + 1)} rel="next" className={MOBILE_LINK}>
            {next}
          </a>
        ) : (
          <span className={MOBILE_DISABLED}>{next}</span>
        )}
      </div>

      <div className="hidden sm:flex-1 sm:flex sm:gap-2 sm:items-center sm:justify-between">
        <div>
          <p className="text-sm text-gray-700 leading-5 dark:text-gray-600">
            {t('Showing')}{' '}
            {firstItem !== null ? (
              <>
                <span className="font-medium">{firstItem}</span> {t('to')} <span className="font-medium">{lastItem}</span>
              </>
            ) : (
              count
            )}{' '}
            {t('of')} <span className="font-medium">{total}</span> {t('results')}
          </p>
        </div>

        <div>
          <span className="inline-flex rtl:flex-row-reverse shadow-sm rounded-md">
            {onFirst ? (
              <span aria-disabled="true" aria-label={prev}>
                <span className="inline-flex items-center px-2 py-2 text-sm font-medium text-gray-500 bg-white border border-gray-300 cursor-not-allowed rounded-l-md leading-5 dark:bg-gray-700 dark:border-gray-600 dark:text-gray-400" aria-hidden="true">
                  <Arrow d={PREV_PATH} />
                </span>
              </span>
            ) : (
              <a href={hrefFor(page - 1)} rel="prev" className="inline-flex items-center px-2 py-2 text-sm font-medium text-gray-500 bg-white border border-gray-300 rounded-l-md leading-5 hover:text-gray-400 focus:outline-none focus:ring ring-gray-300 focus:border-blue-300 active:bg-gray-100 active:text-gray-500 transition ease-in-out duration-150 dark:bg-gray-800 dark:border-gray-600 dark:active:bg-gray-700 dark:focus:border-blue-800 dark:text-gray-300 dark:hover:bg-gray-900 dark:hover:text-gray-300" aria-label={prev}>
                <Arrow d={PREV_PATH} />
              </a>
            )}

            {paginationElements(page, lastPage).map((el, i) =>
              el === '...' ? (
                <span key={`dots-${i}`} aria-disabled="true">
                  <span className="inline-flex items-center px-4 py-2 -ml-px text-sm font-medium text-gray-700 bg-white border border-gray-300 cursor-default leading-5 dark:bg-gray-800 dark:border-gray-600 dark:text-gray-300">...</span>
                </span>
              ) : el === page ? (
                <span key={el} aria-current="page">
                  <span className="inline-flex items-center px-4 py-2 -ml-px text-sm font-medium text-gray-700 bg-gray-200 border border-gray-300 cursor-default leading-5 dark:bg-gray-700 dark:border-gray-600 dark:text-gray-300">{el}</span>
                </span>
              ) : (
                <a key={el} href={hrefFor(el)} className="inline-flex items-center px-4 py-2 -ml-px text-sm font-medium text-gray-700 bg-white border border-gray-300 leading-5 hover:text-gray-700 focus:outline-none focus:ring ring-gray-300 focus:border-blue-300 active:bg-gray-100 active:text-gray-700 transition ease-in-out duration-150 dark:bg-gray-800 dark:border-gray-600 dark:text-gray-300 dark:hover:text-gray-300 dark:active:bg-gray-700 dark:focus:border-blue-800 hover:bg-gray-100 dark:hover:bg-gray-900" aria-label={t('Go to page {{page}}', { page: el })}>
                  {el}
                </a>
              ),
            )}

            {hasMore ? (
              <a href={hrefFor(page + 1)} rel="next" className="inline-flex items-center px-2 py-2 -ml-px text-sm font-medium text-gray-500 bg-white border border-gray-300 rounded-r-md leading-5 hover:text-gray-400 focus:outline-none focus:ring ring-gray-300 focus:border-blue-300 active:bg-gray-100 active:text-gray-500 transition ease-in-out duration-150 dark:bg-gray-800 dark:border-gray-600 dark:active:bg-gray-700 dark:focus:border-blue-800 dark:text-gray-300 dark:hover:bg-gray-900 dark:hover:text-gray-300" aria-label={next}>
                <Arrow d={NEXT_PATH} />
              </a>
            ) : (
              <span aria-disabled="true" aria-label={next}>
                <span className="inline-flex items-center px-2 py-2 -ml-px text-sm font-medium text-gray-500 bg-white border border-gray-300 cursor-not-allowed rounded-r-md leading-5 dark:bg-gray-700 dark:border-gray-600 dark:text-gray-400" aria-hidden="true">
                  <Arrow d={NEXT_PATH} />
                </span>
              </span>
            )}
          </span>
        </div>
      </div>
    </nav>
  );
}
