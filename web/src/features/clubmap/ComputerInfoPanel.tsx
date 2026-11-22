import { lazy, Suspense } from 'react';
import { useTranslation } from 'react-i18next';
import type { ComputerCard, ComputerState, Zone } from './api';

// Раздел «Состояние» с графиком (Chart.js) — только когда открыта панель ПК (FE-08).
const HealthSections = lazy(() => import('./ComputerHealth').then((m) => ({ default: m.HealthSections })));

const STATE_LABELS: Record<string, string> = {
  online: 'Онлайн', offline: 'Офлайн', available: 'Доступен', busy: 'Занят', service: 'Сервис', restarting: 'Перезапуск',
  reserved: 'Забронирован', idle: 'Ожидает', in_session: 'В сессии', locked: 'Заблокирован', shutting_down: 'Выключается',
};
const STAFF_KINDS: Record<string, string> = { pin: 'Выход из агента по PIN-коду', signin: 'Сотрудник вошёл на ПК', panel: 'Техрежим из панели' };

/** «Информация о ПК» справа от плана (#computer-info-panel шаблона карты клуба). */
export function ComputerInfoPanel({ info, state, zones, locale, tz, onClose }: {
  readonly info: ComputerCard | undefined;
  readonly state: ComputerState | undefined;
  readonly zones: readonly Zone[];
  readonly locale: string;
  readonly tz: string;
  readonly onClose: () => void;
}) {
  const { t } = useTranslation();
  const fmt = (v?: string | null) => {
    if (!v) return t('Нет данных');
    const d = new Date(v);
    return Number.isNaN(d.getTime()) ? t('Нет данных') : new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short', timeZone: tz }).format(d);
  };
  const label = (v?: string) => (v ? (STATE_LABELS[v] ? t(STATE_LABELS[v] ?? v) : v) : t('Нет данных'));

  return (
    <aside id="computer-info-panel" className="cm-info-panel" hidden={!info} aria-live="polite">
      <div className="cm-info-heading"><h3>{t('Информация о ПК')}</h3><button id="computer-info-close" type="button" className="cm-info-close" title={t('Закрыть')} aria-label={t('Закрыть')} onClick={onClose}><i className="fas fa-xmark"></i></button></div>
      <dl className="cm-info-list">
        <div><dt>{t('Компьютер')}</dt><dd id="computer-info-name">{info ? `${info.name} (#${info.number})` : '—'}</dd></div>
        <div><dt>{t('Зона')}</dt><dd id="computer-info-zone">{info ? zones.find((z) => z.id === info.zone_id)?.name || t('Нет данных') : '—'}</dd></div>
        <div><dt>{t('Статус')}</dt><dd id="computer-info-status">{info ? label(state?.status) : '—'}</dd></div>
        <div><dt>{t('Подключение')}</dt><dd id="computer-info-connection">{info ? label(state?.connection) : '—'}</dd></div>
        <div><dt>{t('Последний сигнал')}</dt><dd id="computer-info-last-seen">{info ? fmt(state?.last_seen) : '—'}</dd></div>
        <div><dt>{t('Версия агента')}</dt><dd id="computer-info-agent-version" className={state?.agent_outdated ? 'cm-info-warning' : undefined}>{info ? (state?.agent_version ? (state.agent_outdated ? t('{{version}} · устарела', { version: state.agent_version }) : state.agent_version) : t('Нет данных')) : '—'}</dd></div>
      </dl>
      <section id="computer-session-info" className="cm-info-section" hidden={!(state && (state.has_session || state.agent_state === 'in_session'))}>
        <h4>{t('Активная сессия')}</h4>
        <dl className="cm-info-list">
          <div><dt>{t('Состояние сессии')}</dt><dd id="computer-info-session-state">{state?.agent_state ? label(state.agent_state) : t('В сессии')}</dd></div>
          <div><dt>{t('Сессия закончится')}</dt><dd id="computer-info-session-end">{fmt(state?.session_ends_at)}</dd></div>
          <div><dt>{t('Аккаунт')}</dt><dd id="computer-info-session-user">{state?.session_user || t('Нет данных')}</dd></div>
          <div><dt>{t('Куплено')}</dt><dd id="computer-info-session-tariff">{state?.session_tariff || t('Нет данных')}</dd></div>
        </dl>
      </section>
      <section id="computer-staff-info" className="cm-info-section" hidden={!state?.staff_until}>
        <h4>{t('Обслуживание')}</h4>
        <dl className="cm-info-list">
          <div><dt>{t('Как начато')}</dt><dd id="computer-info-staff-kind">{t(STAFF_KINDS[state?.staff_kind ?? ''] ?? 'Нет данных')}</dd></div>
          <div><dt>{t('Сотрудник')}</dt><dd id="computer-info-staff-name">{state?.staff_name || t('Не указан (общий PIN-код)')}</dd></div>
          <div><dt>{t('До')}</dt><dd id="computer-info-staff-until">{fmt(state?.staff_until)}</dd></div>
        </dl>
      </section>
      <section id="computer-reservation-info" className="cm-info-section" hidden={!state?.is_reserved}><h4>{t('Информация о брони')}</h4><p>{t('Бронь активна')}</p></section>
      {info && <Suspense fallback={null}><HealthSections key={info.id} computerId={info.id} fmt={fmt} locale={locale} tz={tz} /></Suspense>}
    </aside>
  );
}
