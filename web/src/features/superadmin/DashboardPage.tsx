import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { isApiError } from '../../api/http';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { clearAgentRelease, dashboardQuery, firstError, qs, setAgentRelease, type DashboardSearch } from './api';
import { LineChartView, LogRows } from './components';
import { saHref } from './host';
import { useSuperadminFlash } from './flash';

/*
 * «Сервер» — перенос resources/views/superadmin/dashboard.blade.php (DashboardController, AgentReleaseController).
 * Живой снимок, история по бакетам и нагрузка клубов считаются на сервере; автообновления нет (как в старом).
 */

const CARD = 'rounded-xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-900';
const BAD = 'text-red-600';
const NORMAL = 'text-gray-900 dark:text-white';
const INPUT = 'w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100';

export function DashboardPage({ search }: { readonly search: DashboardSearch }) {
  const { t } = useTranslation();
  useAdminPageTitle(t('Сервер'));
  const flash = useSuperadminFlash();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data } = useQuery(dashboardQuery(search));
  const [autoError, setAutoError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string[] | null>(null);

  const done = (message: string) => {
    flash.status(message);
    setAutoError(null);
    return queryClient.invalidateQueries({ queryKey: ['superadmin'] });
  };
  const fail = (e: unknown) => {
    setAutoError(isApiError(e) ? (e.field('auto_update') ?? null) : null);
    flash.error(firstError(e));
  };
  const release = useMutation({ mutationFn: setAgentRelease, onSuccess: (r) => done(r.message), onError: fail });
  const clear = useMutation({ mutationFn: clearAgentRelease, onSuccess: (r) => done(r.message), onError: fail });

  if (!data) return null;
  const live = data.live;
  const silent = data.scheduler_minutes_ago == null || data.scheduler_minutes_ago > data.scheduler_silent_minutes;
  const clubsQuery = search.clubs;
  const checked = selected ?? data.selected_clubs.map(String);

  const submitRelease = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    release.mutate({
      latest_version: String(f.get('latest_version') ?? ''),
      released_at: String(f.get('released_at') ?? ''),
      download_url: String(f.get('download_url') ?? '') || null,
      auto_update: f.get('auto_update') === '1',
    });
  };
  const submitClear = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (window.confirm(t('Снять объявление? Клубы перестанут получать уведомление о новой версии.'))) clear.mutate();
  };
  const submitClubs = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    void navigate({ href: saHref('/' + qs({ period: data.period, clubs: checked })) + '#club-load' });
  };
  const toggleClub = (id: string, on: boolean) => setSelected(on ? [...checked, id] : checked.filter((c) => c !== id));
  const rel = data.agent_release;

  return (
    <>
      {/* Планировщик — главный индикатор: без него вебхуки, окончание сессий и сбор метрик молча встают. */}
      <section
        className={`mb-4 rounded-xl border p-4 ${silent ? 'border-red-300 bg-red-50 dark:border-red-800 dark:bg-red-950' : 'border-green-200 bg-green-50 dark:border-green-900 dark:bg-green-950'}`}
        aria-labelledby="scheduler-title"
      >
        <h3 id="scheduler-title" className={`font-semibold ${silent ? 'text-red-800 dark:text-red-200' : 'text-green-800 dark:text-green-200'}`}>
          <i className={`fas ${silent ? 'fa-triangle-exclamation' : 'fa-circle-check'} mr-1`} aria-hidden="true"></i>
          {data.scheduler_minutes_ago == null
            ? t('Планировщик ещё не запускался')
            : silent
              ? t('Планировщик молчит {{count}} минуту|Планировщик молчит {{count}} минуты|Планировщик молчит {{count}} минут', { count: data.scheduler_minutes_ago })
              : t('Планировщик работает')}
        </h3>
        {silent ? (
          <p className="mt-1 text-sm text-red-800 dark:text-red-300">
            {t('Вебхуки, окончание сессий и подписок не обрабатываются. Проверьте строку schedule:run в crontab пользователя сервера.')}
          </p>
        ) : (
          <p className="mt-1 text-sm text-green-800 dark:text-green-300">
            {t('Последний запуск {{count}} минуту назад|Последний запуск {{count}} минуты назад|Последний запуск {{count}} минут назад', {
              count: data.scheduler_minutes_ago ?? 0,
            })}
          </p>
        )}
      </section>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4" data-live>
        <div className={CARD}>
          <p className="text-sm text-gray-500">{t('Нагрузка CPU (load average)')}</p>
          <p className={`mt-1 text-3xl font-bold tabular-nums ${live.load_alert ? BAD : NORMAL}`}>{live.load_1 ?? '—'}</p>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-400">
            {t('5 мин: {{five}} · 15 мин: {{fifteen}} · ядер: {{cores}}', { five: live.load_5 ?? '—', fifteen: live.load_15 ?? '—', cores: live.cpu_count ?? '—' })}
          </p>
        </div>
        <div className={CARD}>
          <p className="text-sm text-gray-500">{t('Память')}</p>
          <p className={`mt-1 text-3xl font-bold tabular-nums ${(live.memory_percent ?? 0) > 90 ? BAD : NORMAL}`}>
            {live.memory_percent != null ? `${live.memory_percent}%` : '—'}
          </p>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-400">
            {live.memory_total_gb ? t('{{used}} из {{total}} ГБ', { used: live.memory_used_gb, total: live.memory_total_gb }) : t('Нет данных')}
          </p>
        </div>
        <div className={CARD}>
          <p className="text-sm text-gray-500">{t('Диск')}</p>
          <p className={`mt-1 text-3xl font-bold tabular-nums ${(live.disk_percent ?? 0) > live.disk_alert_percent ? BAD : NORMAL}`}>
            {live.disk_percent != null ? `${live.disk_percent}%` : '—'}
          </p>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-400">
            {live.disk_total_gb ? t('{{used}} из {{total}} ГБ', { used: live.disk_used_gb, total: live.disk_total_gb }) : t('Нет данных')}
          </p>
        </div>
        <div className={CARD}>
          <p className="text-sm text-gray-500">{t('Время работы')}</p>
          <p className={`mt-1 text-3xl font-bold tabular-nums ${NORMAL}`}>
            {live.uptime_days != null ? t('{{count}} день|{{count}} дня|{{count}} дней', { count: live.uptime_days }) : '—'}
          </p>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-400">
            {t('ПК клубов в сети: {{online}} из {{total}}', { online: data.computers.online, total: data.computers.total })}
          </p>
        </div>
      </div>

      <section className="mt-6" aria-labelledby="history-title">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
          <h3 id="history-title" className="font-semibold text-gray-900 dark:text-white">
            {t('История')}
          </h3>
          <nav className="flex gap-1 text-sm" aria-label={t('Период')}>
            {(
              [
                ['24h', t('24 часа')],
                ['7d', t('7 дней')],
              ] as const
            ).map(([key, label]) => (
              <a
                key={key}
                href={saHref('/' + qs({ period: key, clubs: clubsQuery }))}
                className={`rounded-lg px-3 py-1.5 ${data.period === key ? 'bg-violet-700 text-white' : 'border border-gray-300 text-gray-700 dark:border-gray-700 dark:text-gray-300'}`}
                aria-current={data.period === key ? 'page' : undefined}
              >
                {label}
              </a>
            ))}
          </nav>
        </div>
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
          <LineChartView chart={data.charts.load} title={t('Нагрузка CPU')} unit="" />
          <LineChartView chart={data.charts.memory} title={t('Память')} unit="%" />
          <LineChartView chart={data.charts.requests} title={t('Запросов в минуту')} unit={t('запр./мин')} />
          <LineChartView chart={data.charts.duration} title={t('Среднее время ответа')} unit={t('мс')} />
          <LineChartView chart={data.charts.errors} title={t('Доля ошибок 5xx')} unit="%" />
        </div>
      </section>

      {/* Нагрузка каждого клуба за тот же период: таблица (самые нагруженные сверху), отмеченные — на графике. */}
      <section id="club-load" className="mt-6 scroll-mt-6" aria-labelledby="club-load-title">
        <h3 id="club-load-title" className="mb-1 font-semibold text-gray-900 dark:text-white">
          {t('Нагрузка по клубам')}
        </h3>
        <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">
          {t('Запросы агентов ПК, API, панели сотрудников и приложения гостей каждого клуба. Отметьте клубы (до {{max}}), чтобы сравнить их на графике.', { max: 8 })}
        </p>
        {data.club_load.length === 0 ? (
          <p className={`${CARD} text-sm text-gray-500`}>{t('За этот период запросов клубов ещё нет.')}</p>
        ) : (
          <>
            <LineChartView chart={data.charts.clubs} title={t('Запросов в минуту по клубам')} unit={t('запр./мин')} />
            <form method="GET" action={saHref('/') + '#club-load'} className={`${CARD} mt-4`} onSubmit={submitClubs}>
              <input type="hidden" name="period" value={data.period} />
              <div className="overflow-x-auto">
                <table className="w-full min-w-[860px] text-sm">
                  <thead>
                    <tr className="text-left text-gray-500">
                      <th className="py-2">
                        <span className="sr-only">{t('На графике')}</span>
                      </th>
                      <th className="py-2">{t('Клуб')}</th>
                      <th className="py-2 text-right">{t('Запр./мин')}</th>
                      <th className="py-2 text-right">{t('Пик, запр./мин')}</th>
                      <th className="py-2 text-right">{t('Агенты ПК')}</th>
                      <th className="py-2 text-right">{t('API v1')}</th>
                      <th className="py-2 text-right">{t('Панель')}</th>
                      <th className="py-2 text-right">{t('Приложение гостя')}</th>
                      <th className="py-2 text-right">{t('Ответ, мс')}</th>
                      <th className="py-2 text-right">{t('Ошибки 5xx')}</th>
                      <th className="py-2 text-right">{t('Доля времени сервера')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.club_load.map((row) => (
                      <tr key={row.club_id} className="border-t border-gray-100 tabular-nums dark:border-gray-800">
                        <td className="py-2 pr-2">
                          <input
                            type="checkbox"
                            name="clubs[]"
                            value={row.club_id}
                            className="rounded border-gray-300"
                            aria-label={t('Показать на графике: {{club}}', { club: row.name })}
                            checked={checked.includes(String(row.club_id))}
                            onChange={(e) => toggleClub(String(row.club_id), e.currentTarget.checked)}
                          />
                        </td>
                        <td className="py-2 text-gray-800 dark:text-gray-200">
                          <a href={saHref(`/clubs/${row.club_id}`)} className="hover:underline">
                            {row.name}
                          </a>
                        </td>
                        <td className="py-2 text-right">{row.requests_per_minute}</td>
                        <td className="py-2 text-right">{row.peak}</td>
                        <td className="py-2 text-right">{row.agent}</td>
                        <td className="py-2 text-right">{row.api}</td>
                        <td className="py-2 text-right">{row.panel}</td>
                        <td className="py-2 text-right">{row.guest}</td>
                        <td className="py-2 text-right">{row.response_ms ?? '—'}</td>
                        <td className={`py-2 text-right ${row.has_errors ? BAD : ''}`}>{row.errors_percent ?? '—'}</td>
                        <td className="py-2 text-right">{row.server_share ?? '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <div className="mt-3 flex flex-wrap items-center gap-3">
                <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">
                  {t('Показать на графике')}
                </button>
                <span className="text-xs text-gray-500">
                  {t('Без отметок — {{count}} самых нагруженных. Пик — максимум запросов за одну минуту.', { count: 5 })}
                </span>
              </div>
            </form>
          </>
        )}
      </section>

      <div className="mt-6 grid grid-cols-1 gap-4 xl:grid-cols-2">
        <section className={CARD} aria-labelledby="channels-title">
          <h3 id="channels-title" className="mb-3 font-semibold text-gray-900 dark:text-white">
            {t('Запросы за 15 минут')}
          </h3>
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-gray-500">
                <th className="py-2">{t('Канал')}</th>
                <th className="py-2 text-right">{t('Запр./мин')}</th>
                <th className="py-2 text-right">{t('Ответ, мс')}</th>
                <th className="py-2 text-right">{t('Ошибки 5xx')}</th>
              </tr>
            </thead>
            <tbody data-live>
              {data.channels.map((row) => (
                <tr key={row.channel} className="border-t border-gray-100 tabular-nums dark:border-gray-800">
                  <td className="py-2 text-gray-800 dark:text-gray-200">{t(row.label)}</td>
                  <td className="py-2 text-right">{row.requests_per_minute}</td>
                  <td className="py-2 text-right">{row.response_ms ?? '—'}</td>
                  <td className={`py-2 text-right ${row.has_errors ? 'text-red-600' : ''}`}>{row.errors_percent ?? '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>

        <section className={CARD} aria-labelledby="queue-title">
          <h3 id="queue-title" className="mb-3 font-semibold text-gray-900 dark:text-white">
            {t('Очередь и обслуживание')}
          </h3>
          <dl className="space-y-2 text-sm">
            <div className="flex justify-between gap-3">
              <dt className="text-gray-500">{t('Ждут выполнения')}</dt>
              <dd className={`tabular-nums ${live.queue_pending >= live.queue_pending_alert ? 'text-red-600' : ''}`}>
                {live.queue_pending}
                {live.queue_oldest_minutes != null && <> · {t('самое старое — {{minutes}} мин', { minutes: live.queue_oldest_minutes })}</>}
              </dd>
            </div>
            <div className="flex justify-between gap-3">
              <dt className="text-gray-500">{t('Упавшие задания')}</dt>
              <dd className={`tabular-nums ${live.queue_failed > 0 ? 'text-red-600' : ''}`}>{live.queue_failed}</dd>
            </div>
            <div className="flex justify-between gap-3">
              <dt className="text-gray-500">{t('Последний бэкап')}</dt>
              <dd className={data.backup.stale ? 'text-red-600' : ''}>
                {!data.backup.configured ? t('Не настроен (BACKUP_PATH)') : !data.backup.at_human ? t('Файлов нет') : data.backup.at_human}
              </dd>
            </div>
            {data.certificate_hosts.length > 0 ? (
              data.certificate_hosts.map((c) => (
                <div key={c.host} className="flex justify-between gap-3">
                  <dt className="text-gray-500">{t('SSL {{host}}', { host: c.host })}</dt>
                  <dd className={`tabular-nums ${c.days == null || c.days < 14 ? 'text-red-600' : ''}`}>
                    {c.days != null
                      ? t('осталось {{count}} день|осталось {{count}} дня|осталось {{count}} дней', { count: c.days })
                      : c.checked
                        ? t('Не удалось проверить')
                        : t('Ещё не проверялся')}
                  </dd>
                </div>
              ))
            ) : (
              <div className="flex justify-between gap-3">
                <dt className="text-gray-500">{t('SSL-сертификаты')}</dt>
                <dd>{t('Домены не заданы (MONITORING_SSL_HOSTS)')}</dd>
              </div>
            )}
          </dl>
        </section>
      </div>

      <div className="mt-6 grid grid-cols-1 gap-4 xl:grid-cols-2">
        <section className={CARD} aria-labelledby="database-title">
          <h3 id="database-title" className="mb-1 font-semibold text-gray-900 dark:text-white">
            {t('База данных')}
          </h3>
          <p className="mb-3 text-sm text-gray-600 dark:text-gray-400" data-live>
            {live.database_mb != null ? t('Размер: {{size}} МБ', { size: live.database_mb }) : t('Размер недоступен')}
          </p>
          {data.tables.map((table) => (
            <div key={table.name} className="flex items-center justify-between border-t border-gray-100 py-2 text-sm dark:border-gray-800">
              <span className="font-mono text-gray-800 dark:text-gray-200">{table.name}</span>
              <span className="tabular-nums text-gray-600">
                {t('{{size}} МБ · ~{{rows}} строк', { size: table.megabytes, rows: String(table.rows).replace(/\B(?=(\d{3})+(?!\d))/g, ' ') })}
              </span>
            </div>
          ))}
        </section>

        <section className={CARD} aria-labelledby="errors-title">
          <h3 id="errors-title" className="mb-3 font-semibold text-gray-900 dark:text-white">
            {t('Последние ошибки в логе')}
          </h3>
          {data.log_errors.length === 0 ? (
            <p className="text-sm text-gray-500">{t('Ошибок в логе нет.')}</p>
          ) : (
            data.log_errors.map((error, i) => (
              <div key={i} className="border-t border-gray-100 py-2 text-sm dark:border-gray-800">
                <div className="text-xs tabular-nums text-gray-500">
                  {error.at} · <span className="text-red-600">{error.level}</span>
                </div>
                <div className="break-words font-mono text-xs text-gray-800 dark:text-gray-200">{error.message}</div>
              </div>
            ))
          )}
        </section>
      </div>

      <section className={`${CARD} mt-6`} aria-labelledby="agent-versions-title">
        <h3 id="agent-versions-title" className="mb-1 font-semibold text-gray-900 dark:text-white">
          {t('Версии агента на ПК')}
        </h3>
        <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">
          {data.latest_agent
            ? t('Объявлена версия {{version}}: клубы, где агент старше, видят уведомление.', { version: data.latest_agent })
            : t('Новая версия не объявлена: клубы не получают уведомлений.')}
          {data.agent_download_url && (
            <>{' '}<a href={data.agent_download_url} target="_blank" rel="noopener noreferrer" className="text-violet-700 hover:underline">
              {t('Ссылка на скачивание')}
            </a></>
          )}
        </p>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <div>
            {data.agent_versions.length === 0 ? (
              <p className="text-sm text-gray-500">{t('Агенты ещё не сообщали версию.')}</p>
            ) : (
              data.agent_versions.map((v) => (
                <div key={v.version} className="flex items-center justify-between border-t border-gray-100 py-2 text-sm dark:border-gray-800">
                  <span className={`font-mono ${v.outdated ? 'text-amber-700' : 'text-gray-800 dark:text-gray-200'}`}>{v.version}</span>
                  <span className="tabular-nums text-gray-600">{t('{{count}} ПК|{{count}} ПК|{{count}} ПК', { count: v.count })}</span>
                </div>
              ))
            )}
          </div>
          <div>
            {/* Объявление новой версии: клубы со старым агентом получают уведомление в панели и на странице «Список ПК». */}
            <form method="POST" action="/agent-release" className="space-y-3 rounded-lg bg-gray-50 p-4 dark:bg-gray-800" onSubmit={submitRelease}>
              <h4 className="text-sm font-medium text-gray-900 dark:text-white">{t('Объявить новую версию агента')}</h4>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <label className="text-sm">
                  <span className="mb-1 block text-gray-600">{t('Версия')}</span>
                  <input
                    name="latest_version"
                    required
                    pattern="\d{1,4}\.\d{1,4}\.\d{1,6}"
                    placeholder="1.2.0"
                    defaultValue={rel?.latest_version ?? ''}
                    className={`${INPUT} font-mono`}
                  />
                </label>
                <label className="text-sm">
                  <span className="mb-1 block text-gray-600">{t('Дата выпуска')}</span>
                  <input type="date" name="released_at" required defaultValue={rel ? rel.released_at.slice(0, 10) : data.today} className={INPUT} />
                </label>
              </div>
              <label className="block text-sm">
                <span className="mb-1 block text-gray-600">{t('Ссылка на скачивание (https)')}</span>
                <input
                  type="url"
                  name="download_url"
                  maxLength={500}
                  placeholder="https://github.com/…/releases/tag/agent-v1.2.0"
                  defaultValue={rel?.download_url ?? ''}
                  className={INPUT}
                />
              </label>
              <label className="flex items-start gap-2 text-sm text-gray-700 dark:text-gray-300">
                <input type="checkbox" name="auto_update" value="1" className="mt-1" defaultChecked={rel?.auto_update ?? false} />
                <span>
                  {t(
                    'Автообновление: служба агента на свободных ПК сама установит эту версию с GitHub (сборка agent-v… с контрольной суммой). Клуб может выключить его в настройках.',
                  )}
                </span>
              </label>
              {autoError && <p className="text-sm text-red-600">{autoError}</p>}
              {rel?.auto_update && rel.installer_sha256 && (
                <p className="break-all text-xs text-gray-500">
                  {t('Установщик: {{url}}', { url: rel.installer_url ?? '' })} · SHA-256 <span className="font-mono">{rel.installer_sha256}</span>
                </p>
              )}
              <div className="flex flex-wrap gap-2">
                <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm" disabled={release.isPending}>
                  {t('Объявить версию')}
                </button>
              </div>
            </form>
            {rel && (
              <form method="POST" action="/agent-release" className="mt-2" onSubmit={submitClear}>
                <button type="submit" className="rounded-lg border border-gray-300 px-4 py-2 text-sm">
                  {t('Снять объявление')}
                </button>
              </form>
            )}
          </div>
        </div>
      </section>

      <section className={`${CARD} mt-6`} aria-labelledby="recent-title">
        <div className="mb-3 flex items-center justify-between">
          <h3 id="recent-title" className="font-semibold text-gray-900 dark:text-white">
            {t('Последние действия суперадминов')}
          </h3>
          <a href={saHref('/logs')} className="text-sm text-violet-700 hover:underline">
            {t('Весь журнал')}
          </a>
        </div>
        <LogRows logs={data.recent_logs} platform />
      </section>
    </>
  );
}
