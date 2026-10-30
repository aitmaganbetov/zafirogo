import { useTranslation } from 'react-i18next';
import type { Chart, LogRow, SubscriptionBadge } from './api';
import { saHref } from './host';

/** superadmin/logs/rows.blade.php: строки журнала платформы ($platform) или журналов клубов. */
export function LogRows({ logs, platform }: { readonly logs: readonly LogRow[]; readonly platform: boolean }) {
  const { t } = useTranslation();
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-gray-500">
            <th className="px-3 py-2">{t('Время')}</th>
            <th className="px-3 py-2">{t('Клуб')}</th>
            <th className="px-3 py-2">{t('Кто')}</th>
            <th className="px-3 py-2">{t('Событие')}</th>
            <th className="px-3 py-2">{t('Описание')}</th>
            <th className="px-3 py-2">IP</th>
          </tr>
        </thead>
        <tbody>
          {logs.map((log) => (
            <tr key={log.id} className="border-t border-gray-100 align-top dark:border-gray-800">
              <td className="whitespace-nowrap px-3 py-2 tabular-nums text-gray-600">{log.at_utc} UTC</td>
              <td className="px-3 py-2">
                {log.club_id != null ? (
                  <a href={saHref(`/clubs/${log.club_id}`)} className="text-violet-700 hover:underline">
                    {log.club_name}
                  </a>
                ) : (
                  <span className="text-gray-400">—</span>
                )}
              </td>
              <td className="px-3 py-2 text-gray-800 dark:text-gray-200">{log.user_name || (platform && log.system ? t('Система') : '—')}</td>
              <td className="px-3 py-2 text-gray-700 dark:text-gray-300">{log.event_label ? t(log.event_label) : log.event}</td>
              <td className="px-3 py-2 text-gray-800 dark:text-gray-200">{log.description}</td>
              <td className="px-3 py-2 font-mono text-xs text-gray-500">{log.ip_address}</td>
            </tr>
          ))}
          {logs.length === 0 && (
            <tr>
              <td colSpan={6} className="px-3 py-6 text-center text-gray-500">
                {t('Записей нет.')}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

/** superadmin/clubs/subscription-badge.blade.php. */
export function SubscriptionBadgeView({ badge }: { readonly badge: SubscriptionBadge }) {
  const { t } = useTranslation();
  return (
    <span className={`rounded-full px-2 py-1 text-xs font-medium ${badge.active ? 'bg-green-100 text-green-800' : 'bg-red-100 text-red-800'}`}>
      {t(badge.label)}
    </span>
  );
}

// App\Support\LineChart::WIDTH / HEIGHT.
const WIDTH = 600;
const HEIGHT = 160;

/** superadmin/partials/chart.blade.php: SVG-график (точки посчитал сервер, LineChart::make()). */
export function LineChartView({ chart, title, unit }: { readonly chart: Chart; readonly title: string; readonly unit: string }) {
  const { t } = useTranslation();
  return (
    <figure className="rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-900">
      <figcaption className="mb-2 flex flex-wrap items-baseline justify-between gap-2">
        <span className="font-semibold text-gray-900 dark:text-white">{title}</span>
        <span className="text-xs text-gray-500">{t('Шкала до {{max}} {{unit}}', { max: chart.max_label, unit })}</span>
      </figcaption>
      <svg viewBox={`0 0 ${WIDTH} ${HEIGHT}`} preserveAspectRatio="none" className="h-40 w-full" role="img" aria-label={title}>
        {[0.25, 0.5, 0.75].map((grid) => (
          <line
            key={grid}
            x1="0"
            x2={WIDTH}
            y1={HEIGHT * grid}
            y2={HEIGHT * grid}
            stroke="currentColor"
            className="text-gray-200 dark:text-gray-800"
            strokeWidth="1"
            vectorEffect="non-scaling-stroke"
          />
        ))}
        {chart.lines.map((line, i) =>
          line.segments.map((points, j) => (
            <polyline
              key={`${i}-${j}`}
              points={points}
              fill="none"
              stroke={line.color}
              strokeWidth="2"
              strokeLinejoin="round"
              vectorEffect="non-scaling-stroke"
            />
          )),
        )}
      </svg>
      <div className="mt-1 flex justify-between text-xs tabular-nums text-gray-500">
        {chart.from_label && (
          <>
            <span>{chart.from_label}</span>
            <span>{chart.to_label}</span>
          </>
        )}
      </div>
      {chart.lines.length > 1 && (
        <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-600 dark:text-gray-400">
          {chart.lines.map((line, i) => (
            <li key={i}>
              <span className="mr-1 inline-block h-2 w-2 rounded-full" style={{ backgroundColor: line.color }}></span>
              {line.translate ? t(line.label) : line.label}
            </li>
          ))}
        </ul>
      )}
    </figure>
  );
}
