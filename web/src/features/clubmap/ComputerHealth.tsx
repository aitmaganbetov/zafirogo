import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { computerHealthQuery, healthEventsQuery, type ComputerState, type HealthEvent, type HealthView } from './api';
import { peripheralLabel, percent, problemLabel, uptime } from './healthText';

/*
 * Состояние ПК (Этап 7, нового в старом Laravel не было; осознанное отличие от дизайна старой карты —
 * docs/open-questions/stage7-health.md): на карточке ПК — загрузка CPU/ОЗУ полосками и значок проблем, в панели
 * «Информация о ПК» — раздел «Состояние» и история проблем. Нет данных (ПК офлайн, старый агент) — ничего не
 * показывается, разметка карты как в старом шаблоне.
 */

/** Загрузка и проблемы на карточке ПК: две полоски (CPU, ОЗУ) и значок с подсказкой. Нет данных — ничего. */
export function PcLoad({ state }: { readonly state: ComputerState | undefined }) {
  const { t } = useTranslation();
  if (!state || state.connection !== 'online') return null;
  const problems = state.problems ?? [];
  return (
    <>
      {state.load && (
        <span className="cm-pc-load" aria-hidden="true">
          <i style={{ width: `${percent(state.load.cpu_bp)}%` }}></i>
          <i style={{ width: `${percent(state.load.ram_bp)}%` }}></i>
        </span>
      )}
      {problems.length > 0 && (
        <span className="cm-pc-alert" title={problems.map((p) => problemLabel(t, p)).join(', ')} aria-label={problems.map((p) => problemLabel(t, p)).join(', ')}>
          <i className="fas fa-triangle-exclamation"></i>
        </span>
      )}
    </>
  );
}

const gb = (mb: number) => Math.round(mb / 1024);
const deg = (deci: number) => Math.round(deci / 10);

function HealthList({ view, t }: { readonly view: HealthView; readonly t: TFunction }) {
  const h = view.health;
  const rows: { label: string; value: string; warn?: boolean }[] = [];
  if (view.load) {
    rows.push({ label: t('Загрузка CPU'), value: t('{{value}} %', { value: percent(view.load.cpu_bp) }) });
    rows.push({ label: t('ОЗУ'), value: t('{{value}} %', { value: percent(view.load.ram_bp) }) });
    rows.push({ label: t('Бездействие'), value: uptime(t, view.load.idle_seconds) });
  }
  if (view.rtt_ms != null) rows.push({ label: t('Пинг'), value: t('{{value}} мс', { value: view.rtt_ms }) });
  if (h) {
    if (h.cpu_temp_deci != null) rows.push({ label: t('Температура CPU'), value: t('{{value}} °C', { value: deg(h.cpu_temp_deci) }), warn: view.problems.includes('cpu_hot') });
    if (h.gpu_temp_deci != null) rows.push({ label: t('Температура GPU'), value: t('{{value}} °C', { value: deg(h.gpu_temp_deci) }), warn: view.problems.includes('gpu_hot') });
    if (h.gpu_load_bp != null) {
      rows.push({ label: t('Загрузка GPU'), value: t('{{value}} %, память {{memory}} %', { value: percent(h.gpu_load_bp), memory: percent(h.gpu_memory_bp ?? 0) }) });
    }
    for (const d of h.disks) {
      const smart = d.smart === 'failing' ? t('SMART: отказ') : d.smart === 'warning' ? t('SMART: предупреждение') : '';
      rows.push({
        label: t('Диск {{mount}}', { mount: d.mount }),
        value: [t('свободно {{free}} из {{total}} ГБ', { free: gb(d.free_mb), total: gb(d.total_mb) }), smart].filter(Boolean).join(' · '),
        warn: smart !== '',
      });
    }
    for (const p of h.peripherals) {
      rows.push({
        label: peripheralLabel(t, p.kind),
        value: [p.name, p.connected ? t('подключено') : t('не подключено')].filter(Boolean).join(' · '),
        warn: !p.connected && p.kind !== 'headset',
      });
    }
    if (h.link_mbps != null) rows.push({ label: t('Сеть'), value: t('{{value}} Мбит/с', { value: h.link_mbps }), warn: view.problems.includes('network_slow') });
    if (h.uptime_seconds != null) rows.push({ label: t('Время работы'), value: uptime(t, h.uptime_seconds) });
    rows.push({
      label: t('Сбои за 7 суток'),
      value: t('выключений: {{shutdowns}}, «синих экранов»: {{bugchecks}}', { shutdowns: h.unexpected_shutdowns_7d, bugchecks: h.bugchecks_7d }),
      warn: view.problems.includes('unstable'),
    });
  }
  return (
    <dl className="cm-info-list">
      {view.problems.length > 0 && (
        <div><dt>{t('Проблемы')}</dt><dd id="computer-info-problems" className="cm-info-warning">{view.problems.map((p) => problemLabel(t, p)).join(', ')}</dd></div>
      )}
      {rows.map((r) => (
        <div key={r.label}><dt>{r.label}</dt><dd className={r.warn ? 'cm-info-warning' : undefined}>{r.value}</dd></div>
      ))}
    </dl>
  );
}

/**
 * Разделы «Состояние» и «История проблем» панели ПК. Данные — по REST при открытии панели, обновляются событием
 * computer.load (useMapLive). Пусто — разделов нет.
 */
export function HealthSections({ computerId, fmt }: { readonly computerId: number; readonly fmt: (v?: string | null) => string }) {
  const { t } = useTranslation();
  const { data: view } = useQuery(computerHealthQuery(computerId));
  const events = useInfiniteQuery(healthEventsQuery(computerId));
  const items: HealthEvent[] = events.data?.pages.flatMap((p) => p.items) ?? [];
  const hasState = !!view && (!!view.load || !!view.health || view.problems.length > 0);
  return (
    <>
      {hasState && (
        <section id="computer-health-info" className="cm-info-section">
          <h4>{t('Состояние')}</h4>
          <HealthList view={view} t={t} />
        </section>
      )}
      {items.length > 0 && (
        <section id="computer-health-history" className="cm-info-section">
          <h4>{t('История проблем')}</h4>
          <dl className="cm-info-list">
            {items.map((e) => (
              <div key={e.id}>
                <dt>{problemLabel(t, e.kind)}</dt>
                <dd className={e.cleared_at ? undefined : 'cm-info-warning'}>
                  {e.cleared_at ? t('{{from}} — {{to}}', { from: fmt(e.raised_at), to: fmt(e.cleared_at) }) : t('с {{from}}, сейчас', { from: fmt(e.raised_at) })}
                </dd>
              </div>
            ))}
          </dl>
          {events.hasNextPage && (
            <button type="button" className="cm-info-more" disabled={events.isFetchingNextPage} onClick={() => void events.fetchNextPage()}>{t('Показать ещё')}</button>
          )}
        </section>
      )}
    </>
  );
}
