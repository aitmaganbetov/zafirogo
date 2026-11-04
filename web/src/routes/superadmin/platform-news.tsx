import { createFileRoute } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { NewsPage, type NewsMode } from '../../features/news/NewsPage';
import { createPlatformNews, deletePlatformNews, intParam, listPlatformNews, platformNewsQuery, updatePlatformNews } from '../../features/superadmin/api';
import { saHref } from '../../features/superadmin/host';
import { useSuperadminFlash } from '../../features/superadmin/flash';

// GET /platform-news — superadmin.news.index (routes/web.php:92): news/index.blade.php в режиме платформы.
export const Route = createFileRoute('/superadmin/platform-news')({
  validateSearch: (s: Record<string, unknown>) => ({ page: intParam(s.page, 2) }),
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(platformNewsQuery(deps.page)),
  component: PlatformNewsRoute,
});

function PlatformNewsRoute() {
  const { t } = useTranslation();
  const { page } = Route.useSearch();
  const flash = useSuperadminFlash();
  const mode: NewsMode = {
    key: ['superadmin', 'news'],
    list: listPlatformNews,
    create: createPlatformNews,
    update: updatePlatformNews,
    remove: deletePlatformNews,
    intro: t('Новости Zafiro показываются на главном экране агента во всех клубах (первая карточка).'),
    hrefFor: (p) => saHref(p > 1 ? `/platform-news?page=${p}` : '/platform-news'),
    onStatus: flash.status,
    onErrors: (list) => flash.error(list[0] ?? ''),
  };
  return <NewsPage page={page} mode={mode} />;
}
