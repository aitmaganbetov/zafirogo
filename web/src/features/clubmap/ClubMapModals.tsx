import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { idempotencyKey, isApiError } from '../../api/http';
import { durationLabel, firstError } from './errors';
import { formatMoney, parseMoney, PARSE_MONEY_MESSAGES, tiynToInput } from '../../lib/money';
import {
  bookComputers,
  controlComputers,
  notifyComputers,
  searchCustomers,
  sellGuestSession,
  type ComputerCard,
  type MapTariff,
  type Zone,
  type ZoneInput,
} from './api';
import { BOOKING_DURATIONS, DEFAULT_BOOKING_DURATION, formatPreviewSum, guestPreview } from './logic';

/*
 * Диалоги карты клуба — разметка и классы из club-map/index.blade.php (zone-modal, computer-modal, booking-modal,
 * notify-modal, guest-session-modal). Тексты ошибок сервера уже на языке клуба (F1-5); t() их не портит.
 */

// ---------------------------------------------------------------------------
// Зона
// ---------------------------------------------------------------------------

export interface ZoneModalState {
  readonly zone: Zone | null; // null — новая зона
  readonly sortOrder: number;
}

const ICONS: readonly [string, string][] = [
  ['desktop', 'Компьютер'], ['crown', 'Корона'], ['gamepad', 'Геймпад'], ['mug-hot', 'Бар'], ['couch', 'Диван'],
  ['dice', 'Игры'], ['door-open', 'Помещение'], ['bong', 'Кальян'], ['smoking', 'Курилка'],
];
const THEMES: readonly [string, string][] = [['neutral', 'Серый'], ['green', 'Зелёный'], ['yellow', 'Жёлтый'], ['purple', 'Фиолетовый'], ['blue', 'Синий']];
const STATUSES: readonly [string, string][] = [['available', 'Свободно'], ['busy', 'Занято'], ['service', 'Сервисная зона']];

export function ZoneModal({ state, tariffs, onClose, onSubmit, onInvalid }: {
  state: ZoneModalState | null;
  tariffs: readonly MapTariff[];
  onClose: () => void;
  onSubmit: (zone: Zone | null, body: ZoneInput) => Promise<void>;
  /** Ошибка формы до отправки (цена часа не разобралась: > 2 знаков — Q-002). */
  onInvalid: (message: string) => void;
}) {
  const { t } = useTranslation();
  const form = useRef<HTMLFormElement>(null);
  const zone = state?.zone ?? null;
  // Поля заполняются при открытии (как JS шаблона: form.reset() / значения data-* кнопки).
  useEffect(() => {
    const f = form.current;
    if (!state || !f) return;
    f.reset();
    const el = f.elements;
    const set = (name: string, v: string) => {
      const input = el.namedItem(name) as HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement | null;
      if (input) input.value = v;
    };
    set('name', zone?.name ?? '');
    set('description', zone?.description ?? '');
    set('capacity', zone?.capacity != null ? String(zone.capacity) : '');
    set('price_per_hour', zone?.price_per_hour_tiyn != null ? tiynToInput(zone.price_per_hour_tiyn) : '');
    set('icon', zone?.icon ?? 'desktop');
    set('theme', zone?.theme ?? 'neutral');
    set('status', zone?.status ?? 'available');
    const select = el.namedItem('tariff_ids[]') as HTMLSelectElement | null;
    if (select) for (const o of Array.from(select.options)) o.selected = !!zone?.tariff_ids.includes(Number(o.value));
  }, [state, zone]);

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const str = (k: string) => String(f.get(k) ?? '');
    const priceRaw = str('price_per_hour').trim();
    let price: number | null = null;
    if (priceRaw !== '') {
      const p = parseMoney(priceRaw);
      if (!p.ok) {
        onInvalid(t(PARSE_MONEY_MESSAGES[p.error], { attribute: 'price per hour' }));
        return;
      }
      price = p.tiyn;
    }
    const capacity = str('capacity').trim();
    void onSubmit(zone, {
      name: str('name'),
      description: str('description') || null,
      capacity: capacity === '' ? null : Number(capacity),
      price_per_hour_tiyn: price,
      icon: str('icon'),
      theme: str('theme'),
      status: str('status'),
      is_featured: false, // hidden is_featured=0 шаблона
      sort_order: state?.sortOrder ?? 0,
      tariff_ids: f.getAll('tariff_ids[]').map(Number).filter((n) => n > 0),
    });
  };

  return (
    <div id="zone-modal" className={`cm-modal${state ? ' open' : ''}`} onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div className="cm-dialog">
        <div className="cm-dialog-head"><h3 id="modal-title">{zone ? t('Редактировать зону') : t('Добавить зону')}</h3><button type="button" className="cm-close" onClick={onClose}>×</button></div>
        <form id="zone-form" ref={form} method="POST" onSubmit={submit}>
          <div className="cm-form-grid">
            <div className="cm-field"><label>{t('Название')}</label><input id="zone-name" name="name" maxLength={100} required /></div>
            <div className="cm-field"><label>{t('Количество мест')}</label><input id="zone-capacity" type="number" name="capacity" min={0} max={10000} /></div>
            <div className="cm-field full"><label htmlFor="zone-price-per-hour">{t('Цена за час')}</label><input id="zone-price-per-hour" type="number" name="price_per_hour" min="0.01" max="1000000" step="0.01" placeholder={t('Не продаётся поминутно')} /><small className="cm-field-hint">{t('Клиенты покупают в агенте любое число минут: цена за минуту — цена часа / 60.')}</small></div>
            <div className="cm-field full"><label>{t('Описание')}</label><textarea id="zone-description" name="description" maxLength={180}></textarea></div>
            <div className="cm-field"><label>{t('Иконка')}</label><select id="zone-icon" name="icon">{ICONS.map(([v, l]) => <option key={v} value={v}>{t(l)}</option>)}</select></div>
            <div className="cm-field"><label>{t('Цвет карточки')}</label><select id="zone-theme" name="theme">{THEMES.map(([v, l]) => <option key={v} value={v}>{t(l)}</option>)}</select></div>
            <div className="cm-field"><label>{t('Статус')}</label><select id="zone-status" name="status">{STATUSES.map(([v, l]) => <option key={v} value={v}>{t(l)}</option>)}</select></div>
          </div>
          <div className="cm-field full"><label htmlFor="zone-tariffs">{t('Тарифы зоны')}</label><select id="zone-tariffs" name="tariff_ids[]" multiple size={4}><option disabled value="">{t('Выберите один или несколько тарифов')}</option>{tariffs.map((tf) => <option key={tf.id} value={tf.id}>{tf.name} · {t('{{minutes}} мин.', { minutes: tf.duration_minutes })} · {formatMoney(tf.price_tiyn, 2, ',', ' ')}</option>)}</select></div>
          <div className="cm-dialog-actions"><button type="button" className="cm-cancel" onClick={onClose}>{t('Отмена')}</button><button className="cm-save">{t('Сохранить')}</button></div>
        </form>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Компьютер
// ---------------------------------------------------------------------------

export function ComputerModal({ computer, zones, errors, onClose, onSave, onDelete }: {
  computer: ComputerCard | null;
  zones: readonly Zone[];
  errors: readonly string[];
  onClose: () => void;
  onSave: (pc: ComputerCard, body: { number: number; display_name: string | null; mac_address: string | null; club_zone_id: number }) => void;
  onDelete: (pc: ComputerCard) => void;
}) {
  const { t } = useTranslation();
  const form = useRef<HTMLFormElement>(null);
  useEffect(() => {
    const f = form.current;
    if (!computer || !f) return;
    (f.elements.namedItem('number') as HTMLInputElement).value = String(computer.number);
    (f.elements.namedItem('display_name') as HTMLInputElement).value = computer.name;
    (f.elements.namedItem('mac_address') as HTMLInputElement).value = computer.mac_address ?? '';
    (f.elements.namedItem('club_zone_id') as HTMLSelectElement).value = String(computer.zone_id);
  }, [computer]);
  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!computer) return;
    const f = new FormData(e.currentTarget);
    const s = (k: string) => String(f.get(k) ?? '');
    onSave(computer, { number: Number(s('number')), display_name: s('display_name') || null, mac_address: s('mac_address').trim() || null, club_zone_id: Number(s('club_zone_id')) });
  };
  const remove = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!computer || !form.current) return;
    const name = (form.current.elements.namedItem('display_name') as HTMLInputElement).value || (form.current.elements.namedItem('number') as HTMLInputElement).value;
    if (window.confirm(t('Удалить ПК {{computer}} с карты клуба? Брони этого ПК тоже удалятся.', { computer: name }))) onDelete(computer);
  };
  return (
    <div id="computer-modal" className={`cm-modal${computer ? ' open' : ''}`} onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div className="cm-dialog">
        <div className="cm-dialog-head"><h3>{t('Редактировать компьютер')}</h3><button type="button" className="cm-close" onClick={onClose}>×</button></div>
        {errors.length > 0 && <div id="computer-form-errors" className="cm-form-errors" role="alert">{errors.map((er, i) => <p key={i}>{er}</p>)}</div>}
        <form id="computer-form" ref={form} method="POST" onSubmit={submit}>
          <div className="cm-form-grid">
            <div className="cm-field"><label htmlFor="computer-number">{t('Номер')}</label><input id="computer-number" type="number" name="number" min={1} max={4294967295} required /></div>
            <div className="cm-field"><label htmlFor="computer-name">{t('Название')}</label><input id="computer-name" name="display_name" maxLength={255} /></div>
            <div className="cm-field full"><label htmlFor="computer-mac">{t('MAC-адрес')}</label><input id="computer-mac" name="mac_address" maxLength={17} placeholder="AA:BB:CC:DD:EE:FF" autoComplete="off" spellCheck={false} /></div>
            <div className="cm-field full"><label htmlFor="computer-zone">{t('Зона')}</label><select id="computer-zone" name="club_zone_id" required>{zones.map((z) => <option key={z.id} value={z.id}>{z.name}</option>)}</select></div>
          </div>
          <div className="cm-dialog-actions"><button type="submit" form="computer-delete-form" className="cm-cancel cm-delete-computer"><i className="fas fa-trash"></i> {t('Удалить ПК')}</button><button type="button" className="cm-cancel" onClick={onClose}>{t('Отмена')}</button><button className="cm-save">{t('Сохранить')}</button></div>
        </form>
        <form id="computer-delete-form" method="POST" onSubmit={remove}></form>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Бронь выбранных ПК
// ---------------------------------------------------------------------------

export function BookingModal({ open, selected, reservedSelected, defaultStart, onClose, onNotice, onBooked }: {
  open: boolean;
  selected: readonly { id: number; name: string }[];
  reservedSelected: readonly number[];
  defaultStart: string;
  onClose: () => void;
  onNotice: (text: string, ms: number) => void;
  onBooked: () => void;
}) {
  const { t } = useTranslation();
  const [query, setQuery] = useState('');
  const [userId, setUserId] = useState<number | null>(null);
  const [options, setOptions] = useState<{ id: number; name: string; phone?: string | null }[] | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const search = useRef<HTMLInputElement>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const request = useRef<AbortController | null>(null);

  // Диалог монтируется заново при каждом открытии (key у родителя): состояние — с нуля.
  useEffect(() => {
    if (open) search.current?.focus();
  }, [open]);
  useEffect(() => () => { if (timer.current) clearTimeout(timer.current); request.current?.abort(); }, []);

  // Поиск клиента: debounce 250 мс, AbortController, от 2 символов (как bookings).
  const onSearch = (value: string) => {
    setQuery(value);
    setUserId(null);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      const q = value.trim();
      if (q.length < 2) {
        setOptions(null);
        return;
      }
      request.current?.abort();
      const ctrl = new AbortController();
      request.current = ctrl;
      searchCustomers(q, ctrl.signal)
        .then((list) => setOptions(list))
        .catch((e: unknown) => {
          if (!(e instanceof DOMException && e.name === 'AbortError')) setOptions(null);
        });
    }, 250);
  };

  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!userId) {
      setError(t('Выберите клиента из пользователей клуба.'));
      return;
    }
    const f = new FormData(e.currentTarget);
    setBusy(true);
    setError('');
    try {
      const r = await bookComputers({
        computer_ids: selected.map((c) => c.id), user_id: userId, starts_at: String(f.get('starts_at') ?? ''),
        duration_minutes: Number(f.get('duration_minutes')), note: String(f.get('note') ?? '') || null,
      });
      onClose();
      onNotice(r.message, 2500);
      window.setTimeout(onBooked, 1200);
    } catch (err) {
      setError(isApiError(err) && err.kind !== 'network' ? firstError(err, '') : t('Нет связи с сервером. Повторите попытку.'));
    } finally {
      setBusy(false);
    }
  };

  // Старый ручной флаг брони без времени — только снять с ПК, у которых он есть.
  const clearFlag = async () => {
    if (reservedSelected.length === 0) return;
    try {
      await controlComputers([...reservedSelected], 'reserve');
      onClose();
      onNotice(t('Бронь снята'), 2500);
      window.setTimeout(onBooked, 800);
    } catch {
      /* как в старом: без ответа — ничего */
    }
  };

  return (
    <div id="booking-modal" className={`cm-modal${open ? ' open' : ''}`}>
      <div className="cm-dialog">
        <div className="cm-dialog-head"><h3>{t('Забронировать ПК')}</h3><button type="button" className="cm-close" aria-label={t('Закрыть')} onClick={onClose}>×</button></div>
        <form id="booking-form" className="cm-guest-form" autoComplete="off" onSubmit={(e) => void submit(e)}>
          <p className="cm-guest-note" id="booking-summary">{open ? t('ПК: {{list}}', { list: selected.map((c) => c.name).join(', ') }) : ''}</p>
          <div className="cm-field pos-user-picker">
            <label htmlFor="booking-customer">{t('Клиент')}</label>
            <input id="booking-customer" ref={search} type="search" role="combobox" aria-autocomplete="list" aria-controls="booking-customer-options" aria-expanded={options !== null} placeholder={t('Поиск по имени или телефону')} value={query} onChange={(e) => onSearch(e.target.value)} />
            <div id="booking-customer-options" className="pos-user-options" role="listbox" hidden={options === null}>
              {options !== null && options.length === 0 && <p className="pos-user-no-results">{t('Пользователь не найден')}</p>}
              {options?.map((c) => (
                <button key={c.id} type="button" className="pos-user-option" data-id={c.id} onClick={() => { setUserId(c.id); setQuery(`${c.name} · ${c.phone ?? ''}`); setOptions(null); }}>
                  <strong>{c.name}</strong><small>{c.phone ?? ''}</small>
                </button>
              ))}
            </div>
          </div>
          <div className="cm-form-grid">
            <div className="cm-field"><label htmlFor="booking-start">{t('Начало')}</label><input id="booking-start" name="starts_at" type="datetime-local" required defaultValue={defaultStart} /></div>
            <div className="cm-field"><label htmlFor="booking-duration">{t('Длительность')}</label><select id="booking-duration" name="duration_minutes" defaultValue={DEFAULT_BOOKING_DURATION}>{BOOKING_DURATIONS.map((m) => <option key={m} value={m}>{durationLabel(t, m)}</option>)}</select></div>
          </div>
          <div className="cm-field"><label htmlFor="booking-note">{t('Комментарий')}</label><input id="booking-note" name="note" maxLength={500} /></div>
          <p className="cm-guest-error" id="booking-error" role="alert">{error}</p>
          <div className="cm-dialog-actions"><button type="button" className="cm-cancel" id="booking-flag-clear" hidden={reservedSelected.length === 0} onClick={() => void clearFlag()}>{t('Снять бронь без времени')}</button><button type="button" className="cm-cancel" onClick={onClose}>{t('Отмена')}</button><button type="submit" className="cm-save" disabled={busy}><i className="fas fa-bookmark"></i> {t('Забронировать')}</button></div>
        </form>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Уведомление на ПК
// ---------------------------------------------------------------------------

export function NotifyModal({ open, ids, onClose, onNotice }: { open: boolean; ids: readonly number[]; onClose: () => void; onNotice: (text: string, ms: number) => void }) {
  const { t } = useTranslation();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const text = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (open) text.current?.focus();
  }, [open]);
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (ids.length === 0) return;
    const form = e.currentTarget;
    setBusy(true);
    setError('');
    try {
      const r = await notifyComputers([...ids], text.current?.value ?? '');
      onClose();
      form.reset();
      onNotice(r.message, 6000);
    } catch (err) {
      setError(isApiError(err) && err.kind !== 'network' ? firstError(err, t('Не удалось отправить уведомление.')) : t('Не удалось отправить уведомление.'));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div id="notify-modal" className={`cm-modal${open ? ' open' : ''}`}>
      <div className="cm-dialog">
        <div className="cm-dialog-head"><h3>{t('Уведомление на ПК')}</h3><button type="button" className="cm-close" aria-label={t('Закрыть')} onClick={onClose}>×</button></div>
        <form id="notify-form" className="cm-guest-form" onSubmit={(e) => void submit(e)}>
          <p className="cm-guest-note">{t('Текст появится в правом верхнем углу экрана выбранных ПК, даже поверх игры.')}</p>
          <div className="cm-field"><label htmlFor="notify-text">{t('Сообщение')}</label><textarea id="notify-text" ref={text} name="message" rows={3} maxLength={300} required></textarea></div>
          <p className="cm-guest-error" id="notify-error" role="alert">{error}</p>
          <div className="cm-dialog-actions"><button type="button" className="cm-cancel" onClick={onClose}>{t('Отмена')}</button><button type="submit" className="cm-save" id="notify-submit" disabled={busy}><i className="fas fa-paper-plane"></i>{t('Отправить')}</button></div>
        </form>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Подключить тариф гостю
// ---------------------------------------------------------------------------

export function GuestSessionModal({ open, selected, zones, tariffs, locale, onClose, onNotice }: {
  open: boolean;
  selected: readonly { id: number; zoneId: number }[];
  zones: readonly Zone[];
  tariffs: readonly MapTariff[];
  locale: string;
  onClose: () => void;
  onNotice: (text: string, ms: number) => void;
}) {
  const { t } = useTranslation();
  const sellable = useMemo(() => tariffs.filter((tf) => tf.guest_sellable), [tariffs]);
  // Тарифы зон выбранных ПК (тариф без зон — во всех зонах).
  const zoneIds = [...new Set(selected.map((c) => c.zoneId))];
  const zonesOf = (tariffId: number) => zones.filter((z) => z.tariff_ids.includes(tariffId)).map((z) => z.id);
  const fits = (tariffId: number) => {
    const own = zonesOf(tariffId);
    return own.length === 0 || zoneIds.every((z) => own.includes(z));
  };
  const firstFit = sellable.find((tf) => fits(tf.id));
  // Диалог монтируется заново при каждом открытии (key у родителя): выбор — первый подходящий тариф или
  // «Поминутно» (showZoneTariffs шаблона).
  const [choice, setChoice] = useState<string>(() => (firstFit ? `tariff:${firstFit.id}` : 'minutes'));
  const [minutes, setMinutes] = useState('60');
  const [payment, setPayment] = useState('cash');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const key = useRef(idempotencyKey());

  const prices = selected.map((c) => zones.find((z) => z.id === c.zoneId)?.price_per_hour_tiyn ?? null);
  const tariff = choice.startsWith('tariff:') ? sellable.find((tf) => tf.id === Number(choice.slice(7))) : undefined;
  const preview = choice === 'minutes' ? guestPreview({ kind: 'minutes', minutes: Number(minutes) || 0 }, prices) : guestPreview({ kind: 'tariff', priceTiyn: tariff?.price_tiyn ?? 0 }, prices);
  const total = preview.missing
    ? t('В зоне одного из ПК нет цены часа: поминутно не продать.')
    : t('К оплате: {{sum}} за {{count}} ПК', { sum: formatPreviewSum(preview.sumTiyn, locale), count: selected.length });

  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (selected.length === 0) return;
    const body: { computer_ids: number[]; tariff_id?: number; minutes?: number; payment_method: string } = { computer_ids: selected.map((c) => c.id), payment_method: payment };
    if (choice === 'minutes') body.minutes = Number(minutes);
    else body.tariff_id = Number(choice.slice(7));
    setBusy(true);
    setError('');
    try {
      const r = await sellGuestSession(body, key.current);
      key.current = idempotencyKey();
      onClose();
      onNotice(t('Тариф подключён: {{count}} ПК, к оплате {{amount}}.', { count: r.computers.length, amount: formatMoney(r.amount_tiyn, 2, ',', ' ') }), 3000);
    } catch (err) {
      setError(isApiError(err) && err.kind !== 'network' ? firstError(err, t('Не удалось подключить тариф.')) : t('Нет связи с сервером. Повторите попытку.'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div id="guest-session-modal" className={`cm-modal${open ? ' open' : ''}`}>
      <div className="cm-dialog">
        <div className="cm-dialog-head"><h3>{t('Подключить тариф')}</h3><button type="button" className="cm-close" aria-label={t('Закрыть')} onClick={onClose}>×</button></div>
        <form id="guest-session-form" className="cm-guest-form" onSubmit={(e) => void submit(e)}>
          <p className="cm-guest-note">{t('Для гостя без аккаунта: ПК откроется без входа, пока не закончится время. Оплата проводится в открытую смену кассы.')}</p>
          <fieldset className="cm-guest-options"><legend>{t('Что подключить')}</legend>
            {sellable.map((tf) => (
              <label key={tf.id} className="cm-guest-option" data-tariff-zones={zonesOf(tf.id).join(',')} hidden={open && !fits(tf.id)}>
                <input type="radio" name="choice" value={`tariff:${tf.id}`} checked={choice === `tariff:${tf.id}`} onChange={() => setChoice(`tariff:${tf.id}`)} />
                <span><strong>{tf.name}</strong><small>{t('{{minutes}} мин.', { minutes: tf.duration_minutes })} · {formatMoney(tf.price_tiyn, 0, ',', ' ')} {tf.currency}</small></span>
              </label>
            ))}
            <label className="cm-guest-option"><input type="radio" name="choice" value="minutes" checked={choice === 'minutes'} onChange={() => setChoice('minutes')} /><span><strong>{t('Поминутно')}</strong><small>{t('по цене часа зоны ПК')} · <input type="number" name="minutes" min={1} max={720} value={minutes} onChange={(e) => setMinutes(e.target.value)} aria-label={t('Минут')} className="cm-guest-minutes" /> {t('мин.')}</small></span></label>
          </fieldset>
          <div className="cm-field"><label htmlFor="guest-payment">{t('Оплата')}</label><select id="guest-payment" name="payment_method" value={payment} onChange={(e) => setPayment(e.target.value)}><option value="cash">{t('Наличные')}</option><option value="card">{t('Карта')}</option><option value="qr">{t('QR-код')}</option></select></div>
          <p className="cm-guest-note" id="guest-session-no-tariffs" hidden={!open || firstFit !== undefined}>{t('Для зоны выбранных ПК нет тарифов: подключите время поминутно.')}</p>
          <p className="cm-guest-total" id="guest-session-total">{open ? total : ''}</p>
          <p className="cm-guest-error" id="guest-session-error" role="alert">{error}</p>
          <div className="cm-dialog-actions"><button type="button" className="cm-cancel" onClick={onClose}>{t('Отмена')}</button><button type="submit" className="cm-save" id="guest-session-submit" disabled={busy}><i className="fas fa-play"></i>{t('Подключить и принять оплату')}</button></div>
        </form>
      </div>
    </div>
  );
}
