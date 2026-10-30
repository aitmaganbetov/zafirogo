import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { can, useSession } from '../../auth/session';
import { ConfirmForm } from '../../components/ConfirmForm';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { addDays, formatDate } from '../../lib/datetime';
import { setFlash, useFlash } from '../../lib/flash';
import { useInvalidateOn } from '../../realtime/hooks';
import {
  allErrors,
  arriveReservation,
  cancelReservation,
  createReservation,
  reservationsQuery,
  searchCustomers,
  type Reservation,
  type ReservationCustomer,
  type ReservationsSearch,
} from './api';

/*
 * Брони — перенос resources/views/reservations/index.blade.php (ComputerReservationController::index()) с поиском
 * клиента (inline-скрипт: debounce 250 мс, AbortController, выпадающий список .pos-user-options).
 */

const FIELD = 'w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-white';
const LABEL = 'mb-2 block text-xs font-medium text-gray-600 dark:text-gray-400';
const DURATIONS = [30, 60, 90, 120, 180, 240, 360, 480, 720];

const STATES: Record<Reservation['state'], readonly [string, string]> = {
  pending: ['Ожидается', 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'],
  active: ['Идёт', 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'],
  arrived: ['Клиент пришёл', 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'],
  finished: ['Завершена', 'bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-300'],
  cancelled: ['Отменена', 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'],
};

const MINUTES = '{{count}} минута|{{count}} минуты|{{count}} минут';
const HOURS = '{{count}} час|{{count}} часа|{{count}} часов';

function hrefFor(date: string, page?: number): string {
  const p = new URLSearchParams({ date });
  if (page && page > 1) p.set('page', String(page));
  return `/reservations?${p.toString()}`;
}

export function ReservationsPage({ search }: { search: ReservationsSearch }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  useAdminPageTitle(t('Брони'));
  useInvalidateOn(['reservation.*'], ['reservations']);
  const flash = useFlash('status');
  const [status, setStatus] = useState<string | null>(flash);
  const [errors, setErrors] = useState<string[]>([]);
  const { data } = useQuery(reservationsQuery(search));
  const tz = me?.club?.timezone ?? 'UTC';
  const readOnly = me?.read_only === true;

  const [computerId, setComputerId] = useState<string>('');
  const [startsAt, setStartsAt] = useState<string | null>(null);
  const [duration, setDuration] = useState('60');
  const [note, setNote] = useState('');
  const [dayInput, setDayInput] = useState<string | null>(null);

  const ok = (message: string) => {
    setStatus(message);
    setErrors([]);
    return queryClient.invalidateQueries({ queryKey: ['reservations'] });
  };
  const fail = (e: unknown) => setErrors(allErrors(e));
  const cancel = useMutation({ mutationFn: cancelReservation, onSuccess: (r) => ok(r.message), onError: fail });
  const arrive = useMutation({ mutationFn: arriveReservation, onSuccess: (r) => ok(r.message), onError: fail });
  const create = useMutation({
    mutationFn: createReservation,
    onSuccess: (r) => {
      setFlash('status', r.message);
      void queryClient.invalidateQueries({ queryKey: ['reservations'] });
      void navigate({ href: hrefFor(r.date) });
    },
    onError: fail,
  });

  // --- Поиск клиента (data-customer-picker) ---
  const pickerRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const [userId, setUserId] = useState('');
  const [label, setLabel] = useState('');
  const [query, setQuery] = useState('');
  const [options, setOptions] = useState<ReservationCustomer[] | null>(null);
  const timer = useRef<number | undefined>(undefined);
  const request = useRef<AbortController | null>(null);

  const close = () => setOptions(null);
  const find = async (q: string) => {
    if (q.trim().length < 2) {
      close();
      return;
    }
    request.current?.abort();
    const ctrl = new AbortController();
    request.current = ctrl;
    try {
      setOptions(await searchCustomers(q.trim(), ctrl.signal));
    } catch (e) {
      if (!(e instanceof DOMException && e.name === 'AbortError') && !ctrl.signal.aborted) close();
    }
  };
  useEffect(() => {
    const onClick = (e: MouseEvent) => {
      if (e.target instanceof Node && !pickerRef.current?.contains(e.target)) close();
    };
    document.addEventListener('click', onClick);
    return () => {
      document.removeEventListener('click', onClick);
      window.clearTimeout(timer.current);
      request.current?.abort();
    };
  }, []);

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!userId) {
      searchRef.current?.setCustomValidity(t('Выберите клиента из пользователей клуба.'));
      searchRef.current?.reportValidity();
      return;
    }
    const pc = computerId || String(data?.computers[0]?.id ?? '');
    create.mutate({
      club_computer_id: pc ? Number(pc) : null,
      user_id: Number(userId),
      starts_at: startsAt ?? data?.default_start ?? '',
      duration_minutes: Number(duration),
      note: note === '' ? null : note,
    });
  };

  if (!data) return null;

  const groups = new Map<string, typeof data.computers>();
  for (const c of data.computers) {
    const zone = c.zone_name ?? t('Без зоны');
    groups.set(zone, [...(groups.get(zone) ?? []), c]);
  }
  const durationLabel = (m: number) =>
    m < 60 ? t(MINUTES, { count: m }) : t(HOURS, { count: Math.floor(m / 60) }) + (m % 60 ? ` ${t(MINUTES, { count: m % 60 })}` : '');
  const timeRange = (r: Reservation) => {
    const sameDay = formatDate(r.ends_at, tz, 'Y-m-d') === formatDate(r.starts_at, tz, 'Y-m-d');
    return `${formatDate(r.starts_at, tz, 'H:i')} – ${formatDate(r.ends_at, tz, sameDay ? 'H:i' : 'd.m H:i')}`;
  };

  return (
    <div className="p-6 lg:p-8">
      <header className="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <h3 className="mb-2 text-2xl font-bold text-gray-900 dark:text-white">{t('Брони')}</h3>
          <p className="text-sm text-gray-600 dark:text-gray-400">
            {t('Забронированный ПК в это время не продаёт тарифы другим клиентам. Между бронями и сессиями остаётся {{count}} мин (настройка «Интервал между бронированиями»).', {
              count: data.gap_minutes,
            })}
          </p>
        </div>
      </header>

      {status && <div className="mb-5 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700 dark:border-green-800 dark:bg-green-900/20 dark:text-green-300">{status}</div>}
      {errors.length > 0 && (
        <div className="mb-5 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300" role="alert">
          {errors.map((error, i) => (
            <div key={i}>{error}</div>
          ))}
        </div>
      )}

      <section className="mb-6 rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-800 dark:bg-gray-900" aria-labelledby="new-reservation-title">
        <h4 id="new-reservation-title" className="mb-4 font-semibold text-gray-900 dark:text-white">
          {t('Новая бронь')}
        </h4>
        {data.computers.length === 0 ? (
          <p className="text-sm text-gray-600 dark:text-gray-400">{t('В клубе ещё нет ПК. Добавьте их в разделе «Зоны».')}</p>
        ) : (
          <>
            {can(me, 'reservations.create') && (
              <form method="POST" action="/reservations" className="grid grid-cols-1 items-end gap-3 md:grid-cols-2 xl:grid-cols-4" onSubmit={submit}>
                <div>
                  <label htmlFor="reservation-computer" className={LABEL}>
                    {t('ПК')}
                  </label>
                  <select id="reservation-computer" name="club_computer_id" required className={FIELD} value={computerId} onChange={(e) => setComputerId(e.target.value)}>
                    {[...groups].map(([zone, pcs]) => (
                      <optgroup key={zone} label={zone}>
                        {pcs.map((c) => (
                          <option key={c.id} value={c.id}>
                            {c.name} (#{c.number})
                          </option>
                        ))}
                      </optgroup>
                    ))}
                  </select>
                </div>
                <div>
                  <label htmlFor="reservation-start" className={LABEL}>
                    {t('Начало')}
                  </label>
                  <input id="reservation-start" name="starts_at" type="datetime-local" required value={startsAt ?? data.default_start} onChange={(e) => setStartsAt(e.target.value)} className={FIELD} />
                </div>
                <div>
                  <label htmlFor="reservation-duration" className={LABEL}>
                    {t('Длительность')}
                  </label>
                  <select id="reservation-duration" name="duration_minutes" required className={FIELD} value={duration} onChange={(e) => setDuration(e.target.value)}>
                    {DURATIONS.map((m) => (
                      <option key={m} value={m}>
                        {durationLabel(m)}
                      </option>
                    ))}
                  </select>
                </div>
                <div className="pos-user-picker" data-customer-picker ref={pickerRef}>
                  <label htmlFor="reservation-customer" className={LABEL}>
                    {t('Клиент')}
                  </label>
                  <input type="hidden" name="user_id" value={userId} data-customer-id />
                  <input type="hidden" name="customer_label" value={label} data-customer-label />
                  <input
                    ref={searchRef}
                    id="reservation-customer"
                    type="search"
                    role="combobox"
                    aria-autocomplete="list"
                    aria-controls="reservation-customer-options"
                    aria-expanded={options !== null ? 'true' : 'false'}
                    autoComplete="off"
                    required
                    value={query}
                    placeholder={t('Поиск по имени или телефону')}
                    className={FIELD}
                    data-customer-search
                    onChange={(e) => {
                      const v = e.target.value;
                      setQuery(v);
                      setUserId('');
                      setLabel('');
                      e.target.setCustomValidity('');
                      window.clearTimeout(timer.current);
                      timer.current = window.setTimeout(() => void find(v), 250);
                    }}
                  />
                  <div id="reservation-customer-options" className="pos-user-options" role="listbox" hidden={options === null} data-customer-options>
                    {options?.length === 0 && <p className="pos-user-no-results">{t('Пользователь не найден')}</p>}
                    {options?.map((c) => (
                      <button
                        key={c.id}
                        type="button"
                        className="pos-user-option"
                        role="option"
                        aria-selected={String(c.id) === userId}
                        onClick={() => {
                          const l = `${c.name} · ${c.phone ?? ''}`;
                          setUserId(String(c.id));
                          setLabel(l);
                          setQuery(l);
                          searchRef.current?.setCustomValidity('');
                          close();
                        }}
                      >
                        <strong>{c.name}</strong>
                        <small>{c.phone ?? ''}</small>
                      </button>
                    ))}
                  </div>
                </div>
                <div className="md:col-span-2">
                  <label htmlFor="reservation-note" className={LABEL}>
                    {t('Комментарий')}
                  </label>
                  <input id="reservation-note" name="note" maxLength={500} value={note} onChange={(e) => setNote(e.target.value)} className={FIELD} />
                </div>
                <div>
                  <button type="submit" className="brand-button w-full justify-center" disabled={readOnly || create.isPending}>
                    <i className="fas fa-calendar-plus"></i>
                    {t('Забронировать')}
                  </button>
                </div>
              </form>
            )}
            <p className="mt-3 text-xs text-gray-500 dark:text-gray-400">{t('Во время брони только этот клиент может купить тариф на этом ПК. Нет в списке — добавьте его в разделе «Пользователи».')}</p>
          </>
        )}
      </section>

      <form
        method="GET"
        action="/reservations"
        className="mb-4 flex flex-wrap items-end gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          void navigate({ href: hrefFor(dayInput ?? data.date) });
        }}
      >
        <div>
          <label htmlFor="reservation-day" className={LABEL}>
            {t('День')}
          </label>
          <input id="reservation-day" name="date" type="date" value={dayInput ?? data.date} onChange={(e) => setDayInput(e.target.value)} className={FIELD} />
        </div>
        <button type="submit" className="brand-button">
          <i className="fas fa-filter"></i>
          {t('Показать')}
        </button>
        <a href={hrefFor(addDays(data.date, -1))} className="rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-700 dark:border-gray-700 dark:text-gray-300" aria-label={t('Предыдущий день')}>
          <i className="fas fa-chevron-left"></i>
        </a>
        <a href={hrefFor(addDays(data.date, 1))} className="rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-700 dark:border-gray-700 dark:text-gray-300" aria-label={t('Следующий день')}>
          <i className="fas fa-chevron-right"></i>
        </a>
      </form>

      <div className="overflow-x-auto rounded-xl border border-gray-200 bg-white dark:border-gray-800 dark:bg-gray-900">
        <table className="w-full min-w-[850px] text-left text-sm">
          <thead className="border-b border-gray-200 bg-gray-50 text-xs uppercase text-gray-500 dark:border-gray-800 dark:bg-gray-950 dark:text-gray-400">
            <tr>
              <th className="px-4 py-3 font-semibold">{t('Время')}</th>
              <th className="px-4 py-3 font-semibold">{t('ПК')}</th>
              <th className="px-4 py-3 font-semibold">{t('Клиент')}</th>
              <th className="px-4 py-3 font-semibold">{t('Статус')}</th>
              <th className="px-4 py-3 font-semibold">
                <span className="sr-only">{t('Действия')}</span>
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {data.items.length === 0 && (
              <tr>
                <td colSpan={5} className="px-4 py-12 text-center text-sm text-gray-500 dark:text-gray-400">
                  {t('На этот день броней нет.')}
                </td>
              </tr>
            )}
            {data.items.map((r) => {
              const [stateLabel, stateClass] = STATES[r.state];
              const open = r.state === 'pending' || r.state === 'active';
              return (
                <tr key={r.id}>
                  <td className="whitespace-nowrap px-4 py-3 text-gray-800 dark:text-gray-200">{timeRange(r)}</td>
                  <td className="px-4 py-3 text-gray-800 dark:text-gray-200">
                    {r.computer_name ?? '—'}
                    {r.zone_name && <div className="text-xs text-gray-500">{r.zone_name}</div>}
                  </td>
                  <td className="px-4 py-3 text-gray-700 dark:text-gray-300">
                    {r.customer_label || '—'}
                    {r.note && <div className="text-xs text-gray-500">{r.note}</div>}
                  </td>
                  <td className="px-4 py-3">
                    <span className={`inline-flex rounded-full px-2 py-1 text-xs font-medium ${stateClass}`}>{t(stateLabel)}</span>
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-right">
                    {open && can(me, 'reservations.update') && (
                      <form
                        method="POST"
                        className="inline"
                        onSubmit={(e) => {
                          e.preventDefault();
                          arrive.mutate(r.id);
                        }}
                      >
                        <button type="submit" className="rounded-lg border border-green-300 px-3 py-1.5 text-xs font-medium text-green-700 dark:border-green-800 dark:text-green-400" disabled={readOnly}>
                          {t('Клиент пришёл')}
                        </button>
                      </form>
                    )}
                    {open && can(me, 'reservations.update') && can(me, 'reservations.delete') && ' '}
                    {open && can(me, 'reservations.delete') && (
                      <ConfirmForm message={t('Отменить бронь?')} onConfirm={() => cancel.mutate(r.id)} className="inline">
                        <button type="submit" className="rounded-lg border border-red-300 px-3 py-1.5 text-xs font-medium text-red-700 dark:border-red-800 dark:text-red-400" disabled={readOnly}>
                          {t('Отменить')}
                        </button>
                      </ConfirmForm>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div className="mt-4">
        <Pagination page={data.page} perPage={data.per_page} total={data.total} count={data.items.length} hrefFor={(p) => hrefFor(data.date, p)} />
      </div>
    </div>
  );
}
