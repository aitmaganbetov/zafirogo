import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useMemo, useRef, useState, type MouseEvent as ReactMouseEvent, type PointerEvent as ReactPointerEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { can, useSession } from '../../auth/session';
import { useAdminPageTitle } from '../../layouts/adminSession';
import {
  addComputer,
  clubMapKey,
  clubMapQuery,
  controlComputers,
  createZone,
  deleteComputer,
  deleteZone,
  endSessions,
  saveComputerLayout,
  saveZoneLayout,
  updateComputer,
  updateZone,
  type ClubMapPage as Page,
  type ComputerCard,
  type ComputerState,
  type Zone,
} from './api';
import { HealthSections, PcLoad } from './ComputerHealth';
import { loadTitle } from './healthText';
import { BookingModal, ComputerModal, GuestSessionModal, NotifyModal, ZoneModal, type ZoneModalState } from './ClubMapModals';
import { allErrors, firstError } from './errors';
import { canvasHeight, collides, computerClasses, PC_GRID_X, PC_GRID_Y, snapComputer, snapSize, snapZone, zoneMinSize, type Rect } from './logic';
import { useMapLive } from './useMapLive';

/*
 * Карта клуба — перенос resources/views/club-map/index.blade.php (ClubZoneController::index()) один-в-один:
 * разметка и классы шаблона, 8 inline-скриптов — компонентами: план (перетаскивание и размер зон по сетке 20,
 * ПК по сетке 47×42, поворот дивана), информация о ПК, выбор ПК и панель управления, диалоги зоны, ПК, брони,
 * уведомления и гостевой сессии. Опрос /club-map/state раз в 15 с заменён событиями computer.state (useMapLive).
 * Осознанные отличия — docs/open-questions/stage3-clubmap.md.
 */

const SAVING = 'Сохраняем расположение…';

interface Layout extends Rect {
  readonly rotation: number;
}

type Drag =
  | { kind: 'zone'; id: number; startX: number; startY: number; start: Layout; mode: 'move' | 'resize' }
  | { kind: 'pc'; id: number; startX: number; startY: number; left: number; top: number; limitX: number; limitY: number };

const STATE_LABELS: Record<string, string> = {
  online: 'Онлайн', offline: 'Офлайн', available: 'Доступен', busy: 'Занят', service: 'Сервис', restarting: 'Перезапуск',
  reserved: 'Забронирован', idle: 'Ожидает', in_session: 'В сессии', locked: 'Заблокирован', shutting_down: 'Выключается',
};
const STAFF_KINDS: Record<string, string> = { pin: 'Выход из агента по PIN-коду', signin: 'Сотрудник вошёл на ПК', panel: 'Техрежим из панели' };
const ZONE_STATUS: Record<string, string> = { available: 'Свободно', busy: 'Занято', service: 'Сервис' };

/** computer — открыть карточку этого ПК (?computer=7: переход из колокольчика «Проблемы с ПК»). */
export function ClubMapPage({ computer }: { readonly computer?: number } = {}) {
  const { t, i18n } = useTranslation();
  const { me } = useSession();
  const qc = useQueryClient();
  useAdminPageTitle(t('Карта клуба'));
  useMapLive();
  const { data } = useQuery(clubMapQuery());

  const readOnly = me?.read_only === true;
  const may = (right: string) => !readOnly && can(me, right);
  const canEdit = may('club_map.update');
  const tz = me?.club?.timezone ?? 'UTC';
  const locale = me?.club?.locale ?? i18n.language ?? 'ru';

  const [editMode, setEditMode] = useState(false);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<ReadonlySet<number>>(new Set());
  const [infoId, setInfoId] = useState<number | null>(computer ?? null);
  const [notice, setNotice] = useState<{ text: string; show: boolean }>({ text: t(SAVING), show: false });
  const noticeTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [zoneLayout, setZoneLayout] = useState<Record<number, Layout>>({});
  const [pcLayout, setPcLayout] = useState<Record<number, { x: number; y: number }>>({});
  const [blocked, setBlocked] = useState<{ kind: 'zone' | 'pc'; id: number } | null>(null);
  const [active, setActive] = useState<{ kind: 'zone' | 'pc'; id: number; mode: 'dragging' | 'resizing' | 'moving' } | null>(null);
  const drag = useRef<Drag | null>(null);
  const [zoneModal, setZoneModal] = useState<ZoneModalState | null>(null);
  const [pcModal, setPcModal] = useState<ComputerCard | null>(null);
  const [pcErrors, setPcErrors] = useState<string[]>([]);
  const [booking, setBooking] = useState(false);
  const [notify, setNotify] = useState(false);
  const [guest, setGuest] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => () => { if (noticeTimer.current) clearTimeout(noticeTimer.current); }, []);
  // Новый переход из колокольчика, когда карта уже открыта (смена параметра — во время рендера, без эффекта).
  const [focused, setFocused] = useState(computer);
  if (computer !== focused) {
    setFocused(computer);
    if (computer != null) setInfoId(computer);
  }

  // Свежие данные с сервера заменяют локальные правки расположения.
  useEffect(() => {
    if (drag.current) return;
    setZoneLayout({});
    setPcLayout({});
  }, [data]);

  const states = useMemo(() => new Map((data?.states ?? []).map((s) => [s.id, s])), [data]);
  if (!data) return null;

  // Уведомление внизу (#layout-status): текст, показ ms, затем — «Сохраняем расположение…».
  const showNotice = (text: string, ms: number, reset = false) => {
    if (noticeTimer.current) clearTimeout(noticeTimer.current);
    setNotice({ text, show: true });
    noticeTimer.current = setTimeout(() => setNotice((n) => ({ text: reset ? t(SAVING) : n.text, show: false })), ms);
  };
  const reload = () => qc.invalidateQueries({ queryKey: clubMapKey });
  const flash = (message: string) => {
    setError(null);
    setStatus(message);
    void reload();
  };
  const fail = (e: unknown) => {
    setStatus(null);
    setError(firstError(e, t('Не удалось сохранить.')));
  };

  const layoutOf = (z: Zone): Layout => zoneLayout[z.id] ?? { x: z.position_x, y: z.position_y, width: z.width, height: z.height, rotation: z.rotation };
  const pcPos = (c: ComputerCard) => pcLayout[c.id] ?? { x: c.position_x, y: c.position_y };
  const height = canvasHeight(data.zones.map(layoutOf));
  const selectedCards = data.computers.filter((c) => selected.has(c.id));
  const stateOf = (id: number): ComputerState | undefined => states.get(id);

  // ---- перетаскивание и размер зон, место ПК (pointer-события, как JS шаблона) ----
  const onZonePointerDown = (e: ReactPointerEvent<HTMLElement>, z: Zone, mode: 'move' | 'resize') => {
    if (!editMode) return;
    if (mode === 'move' && (e.target as HTMLElement).closest('button,form,.cm-resize,.cm-computer')) return;
    e.preventDefault();
    e.stopPropagation();
    e.currentTarget.setPointerCapture(e.pointerId);
    drag.current = { kind: 'zone', id: z.id, startX: e.clientX, startY: e.clientY, start: layoutOf(z), mode };
    setActive({ kind: 'zone', id: z.id, mode: mode === 'move' ? 'dragging' : 'resizing' });
  };
  const onZonePointerMove = (e: ReactPointerEvent<HTMLElement>) => {
    const d = drag.current;
    if (!d || d.kind !== 'zone') return;
    const z = data.zones.find((x) => x.id === d.id);
    if (!z) return;
    const others = data.zones.filter((o) => o.id !== d.id).map(layoutOf);
    let next: Layout;
    if (d.mode === 'move') {
      next = { ...d.start, x: snapZone(d.start.x, e.clientX - d.startX), y: snapZone(d.start.y, e.clientY - d.startY) };
    } else {
      const min = zoneMinSize(z.icon === 'couch');
      next = { ...d.start, width: snapSize(d.start.width, e.clientX - d.startX, min.width), height: snapSize(d.start.height, e.clientY - d.startY, min.height) };
    }
    const cur = layoutOf(z);
    if (collides(others, next.x, next.y, next.width, next.height)) {
      setBlocked({ kind: 'zone', id: d.id });
      return;
    }
    setBlocked(null);
    if (next.x !== cur.x || next.y !== cur.y || next.width !== cur.width || next.height !== cur.height) setZoneLayout((s) => ({ ...s, [d.id]: next }));
  };
  const onZonePointerUp = () => {
    const d = drag.current;
    if (!d || d.kind !== 'zone') return;
    drag.current = null;
    setActive(null);
    setBlocked(null);
    const z = data.zones.find((x) => x.id === d.id);
    if (!z) return;
    const l = layoutOf(z);
    setNotice({ text: t(SAVING), show: true });
    saveZoneLayout(z.id, { position_x: l.x, position_y: l.y, width: Math.round(l.width), height: Math.round(l.height) })
      .then(() => {
        qc.setQueryData<Page>(clubMapKey, (old) => old && { ...old, zones: old.zones.map((x) => (x.id === z.id ? { ...x, position_x: l.x, position_y: l.y, width: l.width, height: l.height } : x)) });
        showNotice(t('Расположение сохранено'), 1200, true);
      })
      .catch(() => {
        setZoneLayout((s) => ({ ...s, [z.id]: d.start }));
        showNotice(t('Карточки не могут пересекаться'), 1200, true);
      });
  };

  const rotate = (z: Zone) => {
    const l = layoutOf(z);
    const rotation = l.rotation === 0 ? 90 : 0;
    setZoneLayout((s) => ({ ...s, [z.id]: { ...l, rotation } }));
    setNotice({ text: t('Сохраняем поворот…'), show: true });
    saveZoneLayout(z.id, { position_x: l.x, position_y: l.y, width: Math.round(l.width), height: Math.round(l.height), rotation })
      .then(() => {
        qc.setQueryData<Page>(clubMapKey, (old) => old && { ...old, zones: old.zones.map((x) => (x.id === z.id ? { ...x, rotation } : x)) });
        showNotice(t('Диван повернут'), 1200, true);
      })
      .catch(() => {
        setZoneLayout((s) => ({ ...s, [z.id]: l }));
        showNotice(t('Не удалось повернуть диван'), 1200, true);
      });
  };

  const onPcPointerDown = (e: ReactPointerEvent<HTMLSpanElement>, c: ComputerCard) => {
    if (!editMode) return;
    if ((e.target as HTMLElement).closest('.cm-computer-edit')) return;
    e.preventDefault();
    e.stopPropagation();
    const el = e.currentTarget;
    const area = el.parentElement;
    el.setPointerCapture(e.pointerId);
    const p = pcPos(c);
    drag.current = {
      kind: 'pc', id: c.id, startX: e.clientX, startY: e.clientY, left: p.x, top: p.y,
      limitX: Math.max(0, (area?.scrollWidth ?? 0) - el.offsetWidth), limitY: Math.max(0, (area?.scrollHeight ?? 0) - el.offsetHeight),
    };
    setActive({ kind: 'pc', id: c.id, mode: 'moving' });
  };
  const onPcPointerMove = (e: ReactPointerEvent<HTMLSpanElement>) => {
    const d = drag.current;
    if (!d || d.kind !== 'pc') return;
    const c = data.computers.find((x) => x.id === d.id);
    if (!c) return;
    const x = snapComputer(d.left, e.clientX - d.startX, PC_GRID_X, d.limitX);
    const y = snapComputer(d.top, e.clientY - d.startY, PC_GRID_Y, d.limitY);
    const occupied = data.computers.some((o) => o.id !== c.id && o.zone_id === c.zone_id && pcPos(o).x === x && pcPos(o).y === y);
    setBlocked(occupied ? { kind: 'pc', id: c.id } : null);
    if (!occupied) setPcLayout((s) => ({ ...s, [c.id]: { x, y } }));
  };
  const onPcPointerUp = (c: ComputerCard) => {
    const d = drag.current;
    if (!d || d.kind !== 'pc') {
      if (editMode) setInfoId(c.id); // в редакторе информация о ПК — по pointerup
      return;
    }
    drag.current = null;
    setActive(null);
    setBlocked(null);
    setInfoId(c.id);
    const p = pcPos(c);
    setNotice({ text: t(SAVING), show: true });
    saveComputerLayout(c.id, { position_x: p.x, position_y: p.y })
      .then(() => {
        qc.setQueryData<Page>(clubMapKey, (old) => old && { ...old, computers: old.computers.map((x) => (x.id === c.id ? { ...x, position_x: p.x, position_y: p.y } : x)) });
        showNotice(t('Место ПК сохранено'), 1200, true);
      })
      .catch(() => {
        setPcLayout((s) => ({ ...s, [c.id]: { x: d.left, y: d.top } }));
        showNotice(t('Ячейка занята'), 1200, true);
      });
  };
  const onPcClick = (e: ReactMouseEvent, c: ComputerCard) => {
    if ((e.target as HTMLElement).closest('.cm-computer-edit')) return;
    setInfoId(c.id);
    if (editMode) return;
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(c.id)) n.delete(c.id);
      else n.add(c.id);
      return n;
    });
  };

  // ---- действия ----
  const submitZone = async (zone: Zone | null, body: Parameters<typeof createZone>[0]) => {
    try {
      const r = zone ? await updateZone(zone.id, body) : await createZone(body);
      setZoneModal(null);
      flash(r.message);
    } catch (e) {
      fail(e);
    }
  };
  const removeZone = (z: Zone) => {
    if (!window.confirm(t('Удалить зону «{{name}}»?', { name: z.name }))) return;
    deleteZone(z.id).then((r) => flash(r.message), fail);
  };
  const addPc = (z: Zone) => {
    addComputer(z.id).then((r) => flash(r.message), fail);
  };
  const savePc = (pc: ComputerCard, body: Parameters<typeof updateComputer>[1]) => {
    updateComputer(pc.id, body).then(
      (r) => {
        setPcModal(null);
        setPcErrors([]);
        flash(r.message);
      },
      (e: unknown) => setPcErrors(allErrors(e, t('Не удалось сохранить.'))),
    );
  };
  const removePc = (pc: ComputerCard) => {
    deleteComputer(pc.id).then(
      (r) => {
        setPcModal(null);
        setSelected((s) => new Set([...s].filter((id) => id !== pc.id)));
        flash(r.message);
      },
      (e: unknown) => setPcErrors(allErrors(e, t('Не удалось сохранить.'))),
    );
  };
  const ids = () => [...selected];
  const control = async (action: string, confirmText?: string) => {
    if (selected.size === 0) return;
    if (confirmText && !window.confirm(confirmText)) return;
    setBusy(true);
    try {
      const r = await controlComputers(ids(), action);
      if (r.status) {
        const st = r.status;
        qc.setQueryData<Page>(clubMapKey, (old) => old && { ...old, states: old.states.map((s) => (selected.has(s.id) ? { ...s, status: st } : s)) });
      }
      if (r.message) showNotice(r.message, 6000);
    } catch {
      /* как в старом: ошибка — кнопки снова доступны */
    } finally {
      setBusy(false);
    }
  };
  const finish = async () => {
    if (selected.size === 0 || !window.confirm(t('Завершить сессии на выбранных ПК? Неиспользованное время вернётся клиентам.'))) return;
    setBusy(true);
    try {
      const r = await endSessions(ids());
      showNotice(r.message, 6000);
    } catch (e) {
      showNotice(firstError(e, t('Не удалось завершить сессию.')), 6000);
    } finally {
      setBusy(false);
    }
  };

  const info = infoId != null ? data.computers.find((c) => c.id === infoId) : undefined;
  const infoState = info ? stateOf(info.id) : undefined;
  const fmt = (v?: string | null) => {
    if (!v) return t('Нет данных');
    const d = new Date(v);
    return Number.isNaN(d.getTime()) ? t('Нет данных') : new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short', timeZone: tz }).format(d);
  };
  const label = (v?: string) => (v ? (STATE_LABELS[v] ? t(STATE_LABELS[v] ?? v) : v) : t('Нет данных'));
  const hint = editMode
    ? t('Перетаскивайте карточки мышью. Размер меняется за зелёный угол снизу справа.')
    : canEdit
      ? t('Включите редактор, чтобы изменить карту клуба.')
      : t('Выберите ПК на карте, чтобы включить, перезагрузить или забронировать его.');

  return (
    <div className={`cm-page${editMode ? ' edit-mode' : ''}`} id="club-map-page">
      <div className="cm-head">
        <div><h2>{t('План помещений')}</h2><p id="editor-hint">{hint}</p></div>
        <div className="cm-editor-actions" hidden={!canEdit}>
          <label className="cm-editor-switch"><input type="checkbox" id="editor-toggle" checked={editMode} onChange={(e) => setEditMode(e.target.checked)} /><span className="cm-switch-track"></span><span>{t('Редактор')}</span></label>
          <button type="button" id="add-zone" className="cm-add" onClick={() => setZoneModal({ zone: null, sortOrder: data.zones.length * 10 })}><i className="fas fa-plus"></i> {t('Добавить зону')}</button>
        </div>
      </div>
      {status && <div className="cm-alert ok">{status}</div>}
      {error && <div className="cm-alert error">{error}</div>}
      <div className="cm-legend" aria-label={t('Цвета ПК')}><span><i className="pc-free"></i>{t('Включён, свободен')}</span><span><i className="pc-account"></i>{t('Клиент с аккаунтом')}</span><span><i className="pc-guest"></i>{t('Гостевая сессия')}</span><span><i className="pc-staff"></i>{t('Сотрудник, снят по PIN')}</span><span><i className="pc-reserved"></i>{t('Забронирован')}</span><span><i></i>{t('Выключен')}</span></div>
      <div className="cm-workspace">
        <div className="cm-board">
          <div className="cm-plan" id="map-canvas" style={{ height: `${height}px` }}>
            {data.zones.length === 0 && <div className="cm-empty">{t('На карте пока нет зон. Нажмите «Добавить зону».')}</div>}
            {data.zones.map((z) => {
              const l = layoutOf(z);
              const pcs = data.computers.filter((c) => c.zone_id === z.id);
              const sofa = z.icon === 'couch';
              const cls = ['cm-zone', z.theme, z.status + (sofa ? ' sofa' : '') + (sofa && l.rotation === 90 ? ' rotated-90' : '')];
              if (active?.kind === 'zone' && active.id === z.id) cls.push(active.mode);
              if (blocked?.kind === 'zone' && blocked.id === z.id) cls.push('blocked');
              return (
                <section key={z.id} className={cls.join(' ')} data-rotation={l.rotation} data-zone-id={z.id} style={{ left: `${l.x}px`, top: `${l.y}px`, width: `${l.width}px`, height: `${l.height}px` }}
                  onPointerDown={(e) => onZonePointerDown(e, z, 'move')} onPointerMove={onZonePointerMove} onPointerUp={onZonePointerUp}>
                  {pcs.length > 0 && <span className={`cm-status ${z.status}`}>{z.status in ZONE_STATUS ? t(ZONE_STATUS[z.status] ?? '') : ''}</span>}
                  <div className="cm-tools">
                    <button type="button" className="cm-tool edit-zone" title={t('Редактировать')} onClick={() => setZoneModal({ zone: z, sortOrder: z.sort_order })}><i className="fas fa-pen"></i></button>
                    <form method="POST" onSubmit={(e) => { e.preventDefault(); removeZone(z); }}><button className="cm-tool delete" title={t('Удалить')}><i className="fas fa-trash"></i></button></form>
                    {z.icon === 'desktop' && <form method="POST" onSubmit={(e) => { e.preventDefault(); addPc(z); }}><button type="submit" className="cm-tool add-computer" title={t('Добавить компьютер')} aria-label={t('Добавить компьютер')}><i className="fas fa-plus"></i></button></form>}
                  </div>
                  {sofa && <button type="button" className="cm-tool cm-rotate" title={t('Повернуть диван')} aria-label={t('Повернуть диван')} onClick={(e) => { e.preventDefault(); e.stopPropagation(); rotate(z); }}><i className="fas fa-rotate-right"></i></button>}
                  {sofa && <div className="cm-sofa-art" aria-hidden="true"><span className="cm-sofa-arm left"></span><span className="cm-sofa-arm right"></span><span className="cm-sofa-back"></span><span className="cm-sofa-seat"></span><span className="cm-sofa-seat"></span><span className="cm-sofa-seat"></span></div>}
                  {!sofa && <div className="cm-zone-info"><span className="cm-icon"><i className={`fas fa-${z.icon}`}></i></span><h3>{z.name}</h3>{pcs.length > 0 && <small>{z.description}</small>}</div>}
                  {pcs.length > 0 && (
                    <div className="cm-computers" aria-label={t('Компьютеры в зоне {{name}}', { name: z.name })}>
                      {pcs.map((c) => {
                        const st = stateOf(c.id);
                        const p = pcPos(c);
                        let classes = computerClasses(st?.status ?? 'offline', st?.light ?? '', selected.has(c.id));
                        if (active?.kind === 'pc' && active.id === c.id) classes += ' moving';
                        if (blocked?.kind === 'pc' && blocked.id === c.id) classes += ' blocked';
                        return (
                          <span key={c.id} className={classes} data-id={c.id} style={{ left: `${p.x}px`, top: `${p.y}px` }}
                            title={[c.name, st?.status, c.details, loadTitle(t, st)].filter(Boolean).join(' · ')}
                            onClick={(e) => onPcClick(e, c)} onPointerDown={(e) => onPcPointerDown(e, c)} onPointerMove={onPcPointerMove} onPointerUp={() => onPcPointerUp(c)}>
                            <span>{c.number}</span>
                            <PcLoad state={st} />
                            <button type="button" className="cm-computer-edit" title={t('Редактировать компьютер')} aria-label={t('Редактировать компьютер')}
                              onPointerDown={(e) => e.stopPropagation()} onClick={(e) => { e.preventDefault(); e.stopPropagation(); setPcErrors([]); setPcModal(c); }}><i className="fas fa-pen"></i></button>
                          </span>
                        );
                      })}
                    </div>
                  )}
                  {pcs.length > 0 && <span className="cm-capacity">{z.capacity != null ? t('{{count}} место|{{count}} места|{{count}} мест', { count: z.capacity }) : t('Без мест')}</span>}
                  <span className="cm-resize" title={t('Изменить размер')} onPointerDown={(e) => onZonePointerDown(e, z, 'resize')} onPointerMove={onZonePointerMove} onPointerUp={onZonePointerUp}></span>
                </section>
              );
            })}
          </div>
        </div>
        <aside id="computer-info-panel" className="cm-info-panel" hidden={!info} aria-live="polite">
          <div className="cm-info-heading"><h3>{t('Информация о ПК')}</h3><button id="computer-info-close" type="button" className="cm-info-close" title={t('Закрыть')} aria-label={t('Закрыть')} onClick={() => setInfoId(null)}><i className="fas fa-xmark"></i></button></div>
          <dl className="cm-info-list">
            <div><dt>{t('Компьютер')}</dt><dd id="computer-info-name">{info ? `${info.name} (#${info.number})` : '—'}</dd></div>
            <div><dt>{t('Зона')}</dt><dd id="computer-info-zone">{info ? data.zones.find((z) => z.id === info.zone_id)?.name || t('Нет данных') : '—'}</dd></div>
            <div><dt>{t('Статус')}</dt><dd id="computer-info-status">{info ? label(infoState?.status) : '—'}</dd></div>
            <div><dt>{t('Подключение')}</dt><dd id="computer-info-connection">{info ? label(infoState?.connection) : '—'}</dd></div>
            <div><dt>{t('Последний сигнал')}</dt><dd id="computer-info-last-seen">{info ? fmt(infoState?.last_seen) : '—'}</dd></div>
            <div><dt>{t('Версия агента')}</dt><dd id="computer-info-agent-version" className={infoState?.agent_outdated ? 'cm-info-warning' : undefined}>{info ? (infoState?.agent_version ? (infoState.agent_outdated ? t('{{version}} · устарела', { version: infoState.agent_version }) : infoState.agent_version) : t('Нет данных')) : '—'}</dd></div>
          </dl>
          <section id="computer-session-info" className="cm-info-section" hidden={!(infoState && (infoState.has_session || infoState.agent_state === 'in_session'))}>
            <h4>{t('Активная сессия')}</h4>
            <dl className="cm-info-list">
              <div><dt>{t('Состояние сессии')}</dt><dd id="computer-info-session-state">{infoState?.agent_state ? label(infoState.agent_state) : t('В сессии')}</dd></div>
              <div><dt>{t('Сессия закончится')}</dt><dd id="computer-info-session-end">{fmt(infoState?.session_ends_at)}</dd></div>
              <div><dt>{t('Аккаунт')}</dt><dd id="computer-info-session-user">{infoState?.session_user || t('Нет данных')}</dd></div>
              <div><dt>{t('Куплено')}</dt><dd id="computer-info-session-tariff">{infoState?.session_tariff || t('Нет данных')}</dd></div>
            </dl>
          </section>
          <section id="computer-staff-info" className="cm-info-section" hidden={!infoState?.staff_until}>
            <h4>{t('Обслуживание')}</h4>
            <dl className="cm-info-list">
              <div><dt>{t('Как начато')}</dt><dd id="computer-info-staff-kind">{t(STAFF_KINDS[infoState?.staff_kind ?? ''] ?? 'Нет данных')}</dd></div>
              <div><dt>{t('Сотрудник')}</dt><dd id="computer-info-staff-name">{infoState?.staff_name || t('Не указан (общий PIN-код)')}</dd></div>
              <div><dt>{t('До')}</dt><dd id="computer-info-staff-until">{fmt(infoState?.staff_until)}</dd></div>
            </dl>
          </section>
          <section id="computer-reservation-info" className="cm-info-section" hidden={!infoState?.is_reserved}><h4>{t('Информация о брони')}</h4><p>{t('Бронь активна')}</p></section>
          {info && <HealthSections key={info.id} computerId={info.id} fmt={fmt} locale={locale} tz={tz} />}
        </aside>
      </div>
      <div id="layout-status" className={`cm-saving${notice.show ? ' show' : ''}`}>{notice.text}</div>

      <div id="computer-controls" className={`cm-control-panel${selected.size > 0 ? ' show' : ''}`}>
        <div className="cm-control-count"><strong><span id="selected-count">{selected.size}</span> {t('ПК')}</strong>{t('выбрано')}</div>
        {may('pc_control.update') && (
          <>
            <button type="button" className="cm-control-button power" disabled={busy} onClick={() => void control('power_on')}><i className="fas fa-power-off"></i> {t('Включить')}</button>
            <button type="button" className="cm-control-button" disabled={busy} onClick={() => void control('restart')}><i className="fas fa-rotate-right"></i> {t('Перезагрузить')}</button>
            <button type="button" className="cm-control-button off" disabled={busy} onClick={() => void control('power_off')}><i className="fas fa-stop"></i> {t('Выключить')}</button>
            <button type="button" id="reserve-selected" className="cm-control-button reserve" title={t('Забронировать')} aria-label={t('Забронировать')} onClick={() => { if (selected.size > 0) setBooking(true); }}><i className="fas fa-bookmark"></i> {t('Бронь')}</button>
            <button type="button" className="cm-control-button service" disabled={busy} title={t('Как Ctrl+Alt+P: агент закроется для сотрудника')} onClick={() => void control('service', t('Открыть техрежим на выбранных ПК? Агент закроется, как после PIN-кода (Ctrl+Alt+P).'))}><i className="fas fa-screwdriver-wrench"></i> {t('Техрежим')}</button>
            <button type="button" id="notify-open" className="cm-control-button notify" onClick={() => { if (selected.size > 0) setNotify(true); }}><i className="fas fa-bell"></i> {t('Уведомление')}</button>
          </>
        )}
        {may('pc_control.delete') && <> <button type="button" className="cm-control-button off" id="end-sessions" disabled={busy} onClick={() => void finish()}><i className="fas fa-circle-stop"></i> {t('Завершить сессию')}</button></>}
        {may('cash.create') && data.guest_mode && <> <button type="button" id="guest-session-open" className="cm-control-button tariff" onClick={() => { if (selected.size > 0) setGuest(true); }}><i className="fas fa-play"></i> {t('Подключить тариф')}</button></>}
      </div>

      <ZoneModal state={zoneModal} tariffs={data.tariffs} onClose={() => setZoneModal(null)} onSubmit={submitZone} onInvalid={(m) => { setStatus(null); setError(m); }} />
      <ComputerModal computer={pcModal} zones={data.zones} errors={pcErrors} onClose={() => setPcModal(null)} onSave={savePc} onDelete={removePc} />
      <BookingModal key={booking ? 'open' : 'closed'} open={booking} selected={selectedCards.map((c) => ({ id: c.id, name: c.name }))}
        reservedSelected={selectedCards.filter((c) => stateOf(c.id)?.status === 'reserved').map((c) => c.id)}
        defaultStart={data.booking_start} onClose={() => setBooking(false)} onNotice={showNotice} onBooked={() => void reload()} />
      {may('pc_control.update') && <NotifyModal key={notify ? 'open' : 'closed'} open={notify} ids={[...selected]} onClose={() => setNotify(false)} onNotice={showNotice} />}
      {may('cash.create') && (
        <GuestSessionModal key={guest ? 'open' : 'closed'} open={guest} selected={selectedCards.map((c) => ({ id: c.id, zoneId: c.zone_id }))} zones={data.zones} tariffs={data.tariffs}
          locale={locale} onClose={() => setGuest(false)} onNotice={showNotice} />
      )}
    </div>
  );
}
