import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatDate } from '../../lib/datetime';
import { useInvalidateOn } from '../../realtime/hooks';
import { computersQuery, formatRam, REGISTER_ROLES } from './api';

/*
 * Список ПК — перенос resources/views/club-computers/index.blade.php (ClubComputerListController). Только чтение,
 * как в старом; соединение — из присутствия агента и обновляется по WebSocket (computer.*), в старом — только
 * при перезагрузке. Новое: ссылка «Подключение ПК» (коды регистрации, ключи образа — Q-037) для ролей,
 * которые подключают ПК.
 */
export function ComputersPage({ page = 1 }: { page?: number }) {
  const { t } = useTranslation();
  const { me } = useSession();
  useAdminPageTitle(t('Список ПК'));
  useInvalidateOn(['computer.*'], ['computers', 'list']);
  const { data } = useQuery(computersQuery(page));
  const tz = me?.club?.timezone ?? 'UTC';
  if (!data) return null;
  const upd = data.agent_update;
  const dt = (v?: string | null, empty = '—') => (v ? formatDate(v, tz, 'd.m.Y H:i:s') : empty);
  const canEnroll = !me?.read_only && REGISTER_ROLES.includes(me?.role ?? '');
  return (
    <div className="p-6 lg:p-8">
      <div className="mb-5 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h3 className="text-xl font-semibold text-gray-900 dark:text-white">{t('Список ПК')}</h3>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-400">
            {t('Компьютеров клуба: {{count}}', { count: data.total })}
            {canEnroll && <> · <a href="/club-computers/enrollment" className="brand-link font-medium">{t('Подключение ПК')}</a></>}
          </p>
        </div>
      </div>

      {upd && (
        <section className="mb-5 rounded-lg border border-amber-300 bg-amber-50 p-4 text-sm text-amber-900 dark:border-amber-700 dark:bg-amber-900/20 dark:text-amber-200" aria-labelledby="agent-update-title">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0 flex-1 basis-80">
              <h4 id="agent-update-title" className="font-semibold">{t('Новая версия агента {{version}}', { version: upd.version })}</h4>
              <p className="mt-1">{t('Старая версия агента на {{count}} ПК из {{total}}. Агент не обновляется сам: установите новую версию.', { count: upd.outdated, total: upd.total })}</p>
            </div>
            {upd.download_url && (
              <a href={upd.download_url} target="_blank" rel="noopener noreferrer" className="brand-button inline-flex items-center gap-2 rounded-lg px-4 py-2"><i className="fas fa-download"></i>{t('Скачать установщик')}</a>
            )}
          </div>
          <p className="mt-3 font-semibold">{t('Бездисковая система — обновить один раз в образе:')}</p>
          <ol className="mt-1 list-decimal space-y-1 pl-5">
            <li>{t('На сервере бездисковой загрузки включите режим обновления образа (супер-рабочая станция) и загрузите с него один ПК.')}</li>
            <li>{t('На этом ПК выйдите из агента по PIN-коду (Ctrl+Alt+P) и запустите установщик поверх старой версии.')}</li>
            <li>{t('Сохраните образ и перезагрузите ПК клуба: после перезагрузки на всех будет новая версия.')}</li>
          </ol>
          <p className="mt-2 text-xs">{t('ПК с обычным диском обновляются установщиком на каждом ПК.')}</p>
        </section>
      )}

      <div className="overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-gray-800 dark:bg-gray-900">
        <div className="overflow-x-auto">
          <table className="w-full min-w-[1900px] text-left text-sm">
            <thead className="bg-gray-50 text-xs text-gray-500 dark:bg-gray-800 dark:text-gray-400">
              <tr>
                <th className="px-3 py-3 font-semibold">ID / №</th>
                <th className="px-3 py-3 font-semibold">{t('ПК / зона')}</th>
                <th className="px-3 py-3 font-semibold">{t('Соединение')}</th>
                <th className="px-3 py-3 font-semibold">{t('Статус / бронь')}</th>
                <th className="px-3 py-3 font-semibold">{t('Позиция')}</th>
                <th className="px-3 py-3 font-semibold">{t('Состояние агента')}</th>
                <th className="px-3 py-3 font-semibold">MAC</th>
                <th className="px-3 py-3 font-semibold">IP LAN / WAN</th>
                <th className="px-3 py-3 font-semibold">CPU</th>
                <th className="px-3 py-3 font-semibold">RAM</th>
                <th className="px-3 py-3 font-semibold">GPU</th>
                <th className="px-3 py-3 font-semibold">{t('ОС')}</th>
                <th className="px-3 py-3 font-semibold">Device ID</th>
                <th className="px-3 py-3 font-semibold">{t('Сессия')}</th>
                <th className="px-3 py-3 font-semibold">{t('Агент')}</th>
                <th className="px-3 py-3 font-semibold">{t('Последний heartbeat')}</th>
                <th className="px-3 py-3 font-semibold">{t('Данные оборудования')}</th>
                <th className="px-3 py-3 font-semibold">{t('Создан')}</th>
                <th className="px-3 py-3 font-semibold">{t('Обновлён')}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
              {data.items.length === 0 ? (
                <tr><td colSpan={19} className="px-4 py-12 text-center text-gray-500 dark:text-gray-400">{t('В этом клубе пока нет компьютеров.')}</td></tr>
              ) : (
                data.items.map((c) => (
                  <tr key={c.id} className="align-top text-gray-700 dark:text-gray-300">
                    <td className="whitespace-nowrap px-3 py-3 tabular-nums">#{c.id} <span className="text-gray-500">/</span> {c.number}</td>
                    <td className="px-3 py-3"><div className="font-medium text-gray-900 dark:text-white">{c.name}</div><div className="mt-1 text-xs text-gray-500">{c.zone_name ?? '—'} (#{c.zone_id})</div></td>
                    <td className="whitespace-nowrap px-3 py-3"><span className={`inline-flex rounded-full px-2 py-1 text-xs ${c.connection === 'online' ? 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300' : 'bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400'}`}>{c.connection === 'online' ? 'Online' : 'Offline'}</span></td>
                    <td className="whitespace-nowrap px-3 py-3">{c.status}{c.is_reserved ? ' · ' + t('Забронирован') : ''}</td>
                    <td className="whitespace-nowrap px-3 py-3 tabular-nums">X {c.position_x} · Y {c.position_y}</td>
                    <td className="px-3 py-3">{c.agent_state ?? '—'}</td>
                    <td className="whitespace-nowrap px-3 py-3 font-mono text-xs">{c.mac_address ?? '—'}</td>
                    <td className="px-3 py-3 font-mono text-xs"><div>{c.local_ip ?? '—'}</div><div className="mt-1 text-gray-500">{c.remote_ip ?? '—'}</div></td>
                    <td className="max-w-56 px-3 py-3"><div className="max-w-56 truncate" title={c.cpu_model ?? undefined}>{c.cpu_model ?? '—'}</div><div className="mt-1 text-xs text-gray-500">{c.cpu_cores ? t('{{count}} ядро|{{count}} ядра|{{count}} ядер', { count: c.cpu_cores }) : ''}</div></td>
                    <td className="whitespace-nowrap px-3 py-3">{c.ram_total_mb ? formatRam(c.ram_total_mb) : '—'}</td>
                    <td className="max-w-56 px-3 py-3"><div className="max-w-56 truncate" title={c.gpu_model ?? undefined}>{c.gpu_model ?? '—'}</div></td>
                    <td className="max-w-48 px-3 py-3"><div className="max-w-48 truncate" title={c.os_version ?? undefined}>{c.os_version ?? '—'}</div></td>
                    <td className="max-w-48 px-3 py-3 font-mono text-xs"><div className="max-w-48 truncate" title={c.device_id ?? undefined}>{c.device_id ?? '—'}</div></td>
                    <td className="max-w-40 px-3 py-3 font-mono text-xs"><div className="max-w-40 truncate" title={c.current_session_id ?? undefined}>{c.current_session_id ?? '—'}</div></td>
                    <td className={`whitespace-nowrap px-3 py-3 ${c.agent_outdated ? 'font-semibold text-amber-700 dark:text-amber-400' : ''}`}>{c.agent_version ?? '—'}{c.agent_outdated && <> <span className="text-xs font-normal">· {t('устарела')}</span></>}</td>
                    <td className="whitespace-nowrap px-3 py-3 text-xs">{dt(c.last_seen_at, t('Нет данных'))}</td>
                    <td className="whitespace-nowrap px-3 py-3 text-xs">{dt(c.hardware_reported_at, t('Нет данных'))}</td>
                    <td className="whitespace-nowrap px-3 py-3 text-xs">{dt(c.created_at)}</td>
                    <td className="whitespace-nowrap px-3 py-3 text-xs">{dt(c.updated_at)}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
        {data.total > data.per_page && (
          <div className="border-t border-gray-100 px-4 py-3 dark:border-gray-800">
            <Pagination page={data.page} perPage={data.per_page} total={data.total} count={data.items.length} hrefFor={(p) => (p > 1 ? `/club-computers?page=${p}` : '/club-computers')} />
          </div>
        )}
      </div>
    </div>
  );
}
