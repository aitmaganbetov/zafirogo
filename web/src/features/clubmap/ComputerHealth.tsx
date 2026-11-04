import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import type { TFunction } from 'i18next';
import { useEffect, useMemo, useState, useSyncExternalStore } from 'react';
import { useTranslation } from 'react-i18next';
import {
  computerHealthQuery,
  healthEventsQuery,
  healthHourlyQuery,
  healthWeeklyQuery,
  type ComputerState,
  type HealthEvent,
  type HealthHour,
  type HealthView,
  type HealthWeek,
} from './api';
import { HealthChart, type ChartSeries } from './HealthChart';
import { peripheralLabel, percent, problemLabel, reasonLabel, uptime, verdictLabel } from './healthText';
import { pushSample, sampleFromView, samplesOf, SERIES_COLORS, subscribeSamples } from './liveSeries';

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

/** Пометка бездискового ПК (S7-H-18): дисков нет — вместо строк дисков и SMART. */
function DisklessRow({ t }: { readonly t: TFunction }) {
  return <div><dt>{t('Бездисковый ПК')}</dt><dd>{t('загрузка по сети, диски не проверяются')}</dd></div>;
}

function HealthList({ view, t }: { readonly view: HealthView; readonly t: TFunction }) {
  const h = view.health;
  const rows: { label: string; value: string; warn?: boolean }[] = [];
  // Бездисковый ПК грузится по сети с сервера образов — сеть сразу после проблем, дисков нет.
  const network = h?.link_mbps != null
    ? { label: t('Сеть'), value: t('{{value}} Мбит/с', { value: h.link_mbps }), warn: view.problems.includes('network_slow') }
    : null;
  if (view.diskless && network) rows.push(network);
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
    for (const d of view.diskless ? [] : h.disks) {
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
    if (network && !view.diskless) rows.push(network);
    if (h.uptime_seconds != null) rows.push({ label: t('Время работы'), value: uptime(t, h.uptime_seconds) });
    rows.push({
      label: t('Сбои за 7 суток'),
      value: t('выключений: {{shutdowns}}, «синих экранов»: {{bugchecks}}', { shutdowns: h.unexpected_shutdowns_7d, bugchecks: h.bugchecks_7d }),
      warn: view.problems.includes('unstable'),
    });
  }
  return (
    <dl className="cm-info-list">
      {view.diskless && <DisklessRow t={t} />}
      {view.problems.length > 0 && (
        <div><dt>{t('Проблемы')}</dt><dd id="computer-info-problems" className="cm-info-warning">{view.problems.map((p) => problemLabel(t, p)).join(', ')}</dd></div>
      )}
      {rows.map((r) => (
        <div key={r.label}><dt>{r.label}</dt><dd className={r.warn ? 'cm-info-warning' : undefined}>{r.value}</dd></div>
      ))}
    </dl>
  );
}

/** Живой график «Сейчас»: последние 15 минут из живых событий (liveSeries). Меньше двух точек — графика нет. */
function LiveChart({ computerId, timeLabel }: { readonly computerId: number; readonly timeLabel: (ms: number) => string }) {
  const { t } = useTranslation();
  const samples = useSyncExternalStore(subscribeSamples, () => samplesOf(computerId));
  const labels = useMemo(() => samples.map((s) => timeLabel(s.at)), [samples, timeLabel]);
  const cpu = t('Загрузка CPU');
  const ram = t('ОЗУ');
  const cpuTemp = t('Температура CPU');
  const gpuTemp = t('Температура GPU');
  const series = useMemo<ChartSeries[]>(
    () => [
      { label: cpu, data: samples.map((s) => s.cpu ?? null), color: SERIES_COLORS.cpu, axis: 'load' },
      { label: ram, data: samples.map((s) => s.ram ?? null), color: SERIES_COLORS.ram, axis: 'load' },
      { label: cpuTemp, data: samples.map((s) => s.cpuTemp ?? null), color: SERIES_COLORS.cpuTemp, axis: 'temp' },
      { label: gpuTemp, data: samples.map((s) => s.gpuTemp ?? null), color: SERIES_COLORS.gpuTemp, axis: 'temp' },
    ],
    [samples, cpu, ram, cpuTemp, gpuTemp],
  );
  if (samples.length < 2) return null;
  return <HealthChart id="computer-health-live-chart" labels={labels} series={series} />;
}

const avgDeg = (deci?: number | null) => (deci == null ? null : Math.round(deci / 10));

/** Сводка недели: онлайн, температуры, загрузка, сбои, диск (у бездискового — сеть вместо диска). */
function weekSummary(t: TFunction, w: HealthWeek, diskless: boolean): string[] {
  const out = [t('Онлайн: {{hours}} ч, с проблемами: {{problems}} ч', { hours: Math.round(w.online_minutes / 60), problems: w.problem_hours })];
  if (diskless) out.push(t('Медленная сеть: {{hours}} ч', { hours: w.network_slow_hours }));
  if (w.cpu_temp_avg_deci != null) {
    out.push(t('CPU: в среднем {{avg}} °C, максимум {{max}} °C', { avg: avgDeg(w.cpu_temp_avg_deci), max: avgDeg(w.cpu_temp_max_deci) }));
  }
  if (w.gpu_temp_avg_deci != null) {
    out.push(t('GPU: в среднем {{avg}} °C, максимум {{max}} °C', { avg: avgDeg(w.gpu_temp_avg_deci), max: avgDeg(w.gpu_temp_max_deci) }));
  }
  if (w.cpu_load_avg_bp != null) {
    out.push(t('Загрузка: CPU {{cpu}} %, ОЗУ {{ram}} %', { cpu: percent(w.cpu_load_avg_bp), ram: percent(w.ram_avg_bp ?? 0) }));
  }
  out.push(t('Сбои: выключений {{shutdowns}}, «синих экранов» {{bugchecks}}, отключений периферии {{peripherals}}', {
    shutdowns: w.unexpected_shutdowns, bugchecks: w.bugchecks, peripherals: w.peripheral_disconnects,
  }));
  if (w.system_disk_free_bp != null && !diskless) out.push(t('Системный диск: свободно не меньше {{value}} %', { value: Math.floor(w.system_disk_free_bp / 100) }));
  return out;
}

/** График недели по часам: средние температуры CPU/GPU и загрузка CPU/ОЗУ. */
function WeekChart({ computerId, week, hourLabel }: { readonly computerId: number; readonly week: HealthWeek; readonly hourLabel: (iso: string) => string }) {
  const { t } = useTranslation();
  const { data } = useQuery(healthHourlyQuery(computerId, week.start, week.end));
  const items: readonly HealthHour[] = useMemo(() => data?.items ?? [], [data]);
  const labels = useMemo(() => items.map((h) => hourLabel(h.hour)), [items, hourLabel]);
  const cpu = t('Загрузка CPU');
  const ram = t('ОЗУ');
  const cpuTemp = t('Температура CPU');
  const gpuTemp = t('Температура GPU');
  const pct = (bp?: number | null) => (bp == null ? null : percent(bp));
  const series = useMemo<ChartSeries[]>(
    () => [
      { label: cpuTemp, data: items.map((h) => avgDeg(h.cpu_temp_avg_deci)), color: SERIES_COLORS.cpuTemp, axis: 'temp' },
      { label: gpuTemp, data: items.map((h) => avgDeg(h.gpu_temp_avg_deci)), color: SERIES_COLORS.gpuTemp, axis: 'temp' },
      { label: cpu, data: items.map((h) => pct(h.cpu_load_avg_bp)), color: SERIES_COLORS.cpu, axis: 'load' },
      { label: ram, data: items.map((h) => pct(h.ram_avg_bp)), color: SERIES_COLORS.ram, axis: 'load' },
    ],
    [items, cpu, ram, cpuTemp, gpuTemp],
  );
  if (items.length === 0) return null;
  return <HealthChart id="computer-health-week-chart" labels={labels} series={series} />;
}

/** «По неделям»: недели (новые сверху) с вердиктом и причинами; выбор недели — график по часам. */
function WeeksList({ computerId, weeks, diskless, day, hourLabel }: {
  readonly computerId: number;
  readonly weeks: readonly HealthWeek[];
  readonly diskless: boolean;
  readonly day: (iso: string) => string;
  readonly hourLabel: (iso: string) => string;
}) {
  const { t } = useTranslation();
  const [selected, setSelected] = useState<string | null>(null);
  const week = weeks.find((w) => w.start === selected);
  return (
    <>
      <dl className="cm-info-list">
        {diskless && <DisklessRow t={t} />}
        {weeks.map((w) => (
          <div key={w.start} data-week={w.start}>
            <dt>
              <button type="button" className="cm-info-more" aria-pressed={w.start === selected} onClick={() => setSelected(w.start === selected ? null : w.start)}>
                {t('{{from}} — {{to}}', { from: day(w.start), to: day(new Date(new Date(w.end).getTime() - 1).toISOString()) })}
              </button>
            </dt>
            <dd className={w.verdict.level === 'ok' ? undefined : 'cm-info-warning'}>
              {[verdictLabel(t, w.verdict.level), ...w.verdict.reasons.map((r) => reasonLabel(t, r))].join(' · ')}
            </dd>
            <dd>{weekSummary(t, w, diskless).join(' · ')}</dd>
          </div>
        ))}
      </dl>
      {week && <WeekChart computerId={computerId} week={week} hourLabel={hourLabel} />}
    </>
  );
}

/**
 * Разделы «Состояние» и «История проблем» панели ПК. «Состояние» — вкладки «Сейчас» (живые значения и график последних
 * 15 минут; данные — по REST при открытии панели, обновляются событием computer.load, useMapLive) и «По неделям»
 * (вердикт исправности и график по часам выбранной недели). Нет данных — нет ни раздела, ни вкладки.
 */
export function HealthSections({ computerId, fmt, locale = 'ru', tz = 'UTC' }: {
  readonly computerId: number;
  readonly fmt: (v?: string | null) => string;
  readonly locale?: string;
  readonly tz?: string;
}) {
  const { t } = useTranslation();
  const { data: view, dataUpdatedAt } = useQuery(computerHealthQuery(computerId));
  const { data: weekly } = useQuery(healthWeeklyQuery(computerId));
  const events = useInfiniteQuery(healthEventsQuery(computerId));
  const [tab, setTab] = useState<'now' | 'weeks'>('now');
  const diskless = !!(view?.diskless || weekly?.diskless);
  // Бездисковый ПК: проблемы дисков в истории — от томов образа (старый агент или до перевода ПК на загрузку по сети).
  const items: HealthEvent[] = (events.data?.pages.flatMap((p) => p.items) ?? []).filter(
    (e) => !diskless || (e.kind !== 'disk_low' && e.kind !== 'disk_smart'),
  );
  const weeks = weekly?.weeks ?? [];
  const hasState = !!view && (!!view.load || !!view.health || view.problems.length > 0);
  const hasWeeks = weeks.length > 0;
  // Свежее состояние панели — точка живого графика (событие без температуры, перечитанное состояние — с ней).
  useEffect(() => {
    if (view && dataUpdatedAt) pushSample(computerId, sampleFromView(view, dataUpdatedAt));
  }, [computerId, view, dataUpdatedAt]);
  const timeLabel = useMemo(() => {
    const f = new Intl.DateTimeFormat(locale, { timeStyle: 'short', timeZone: tz });
    return (ms: number) => f.format(new Date(ms));
  }, [locale, tz]);
  const [day, hourLabel] = useMemo(() => {
    const d = new Intl.DateTimeFormat(locale, { day: '2-digit', month: '2-digit', timeZone: tz });
    const h = new Intl.DateTimeFormat(locale, { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit', timeZone: tz });
    return [(iso: string) => d.format(new Date(iso)), (iso: string) => h.format(new Date(iso))];
  }, [locale, tz]);
  const shown = hasState && hasWeeks ? tab : hasState ? 'now' : 'weeks';
  return (
    <>
      {(hasState || hasWeeks) && (
        <section id="computer-health-info" className="cm-info-section">
          <h4>{t('Состояние')}</h4>
          {hasState && hasWeeks && (
            <div role="tablist">
              <button type="button" role="tab" className="cm-info-more" aria-selected={shown === 'now'} onClick={() => setTab('now')}>{t('Сейчас')}</button>{' '}
              <button type="button" role="tab" className="cm-info-more" aria-selected={shown === 'weeks'} onClick={() => setTab('weeks')}>{t('По неделям')}</button>
            </div>
          )}
          {shown === 'now' && view && (
            <>
              <HealthList view={view} t={t} />
              <LiveChart computerId={computerId} timeLabel={timeLabel} />
            </>
          )}
          {shown === 'weeks' && (
            <WeeksList computerId={computerId} weeks={weeks} diskless={diskless} day={day} hourLabel={hourLabel} />
          )}
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
