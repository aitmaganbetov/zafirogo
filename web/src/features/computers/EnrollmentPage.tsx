import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { isApiError } from '../../api/http';
import { useSession } from '../../auth/session';
import { SettingsCard } from '../../components/SettingsCard';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatDate } from '../../lib/datetime';
import { approvePending, cancelCode, createCode, createImageKey, enrollmentQuery, rejectPending, reregisterComputer, revokeComputer, revokeImageKey } from './api';

/*
 * Подключение ПК — новый экран (в старом его не было: ПК подключался «Ключом установки клуба» из
 * settings/club.blade.php). Одноразовые коды регистрации, ключи образа для бездисковых ПК и ожидающие
 * подтверждения ПК (решение Q-037, сервис agent/enroll). Разметка — по образцу карточки «Ключ установки клуба»
 * (<x-settings.card>, поле ключа с кнопкой «Копировать») и таблицы списка ПК. Минимально, в стиле старых.
 */

const LABEL = 'mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300';
const FIELD = 'brand-focus w-full rounded-lg border border-gray-300 dark:border-gray-700 bg-gray-50 dark:bg-gray-800 px-4 py-2 text-gray-900 dark:text-gray-100 focus:outline-none';
const SECRET = 'min-w-0 flex-1 rounded-lg border border-gray-300 bg-white px-3 py-2 font-mono text-sm dark:border-gray-700 dark:bg-gray-900 dark:text-white';
const SMALL_BUTTON = 'rounded-lg border border-gray-300 px-3 text-sm dark:border-gray-700';
const TH = 'px-3 py-3 font-semibold';
const TD = 'px-3 py-3';

function Secret({ value }: { value: string }) {
  const { t } = useTranslation();
  return (
    <div className="flex gap-2">
      <input readOnly value={value} className={SECRET} onFocus={(e) => e.currentTarget.select()} />
      <button type="button" className={SMALL_BUTTON} onClick={() => void navigator.clipboard?.writeText(value)}>{t('Копировать')}</button>
    </div>
  );
}

export function EnrollmentPage() {
  const { t } = useTranslation();
  const { me } = useSession();
  const queryClient = useQueryClient();
  useAdminPageTitle(t('Подключение ПК'));
  const { data } = useQuery(enrollmentQuery());
  const tz = me?.club?.timezone ?? 'UTC';
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [code, setCode] = useState<{ code: string; expires_at: string } | null>(null);
  const [key, setKey] = useState<string | null>(null);
  const [codeZone, setCodeZone] = useState('');
  const [pendingZone, setPendingZone] = useState<Record<number, string>>({});
  const [computer, setComputer] = useState('');
  const [reCode, setReCode] = useState<{ code: string; expires_at: string } | null>(null);

  const refresh = () => queryClient.invalidateQueries({ queryKey: ['computers'] });
  const fail = (e: unknown) => {
    setStatus(null);
    if (isApiError(e)) setError(t(Object.values(e.fieldErrors).flat()[0] ?? e.message));
    else setError(e instanceof Error ? e.message : String(e));
  };
  const ok = (message: string) => {
    setError(null);
    setStatus(message);
    void refresh();
  };
  const zoneOf = (v: string) => (v === '' ? null : Number(v));

  const issue = useMutation({ mutationFn: () => createCode(zoneOf(codeZone)), onSuccess: (r) => { setCode(r); setError(null); void refresh(); }, onError: fail });
  const cancel = useMutation({ mutationFn: cancelCode, onSuccess: (r) => ok(r.message), onError: fail });
  const makeKey = useMutation({ mutationFn: createImageKey, onSuccess: (r) => { setKey(r.key); setError(null); void refresh(); }, onError: fail });
  const revoke = useMutation({ mutationFn: revokeImageKey, onSuccess: (r) => ok(r.message), onError: fail });
  const approve = useMutation({ mutationFn: ({ id, zone }: { id: number; zone: number | null }) => approvePending(id, zone), onSuccess: (r) => ok(r.message), onError: fail });
  const reject = useMutation({ mutationFn: rejectPending, onSuccess: (r) => ok(r.message), onError: fail });
  const reregister = useMutation({ mutationFn: reregisterComputer, onSuccess: (r) => { setReCode(r); setStatus(null); setError(null); void refresh(); }, onError: fail });
  const disconnect = useMutation({ mutationFn: revokeComputer, onSuccess: (r) => ok(r.message), onError: fail });

  if (!data) return null;
  const zoneName = (id?: number | null) => data.zones.find((z) => z.id === id)?.name ?? t('Не назначена');
  const dt = (v?: string | null) => (v ? formatDate(v, tz, 'd.m.Y H:i') : '—');
  const selected = () => (data.computers ?? []).find((c) => String(c.id) === computer);

  const submitKey = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const expires = String(f.get('expires_at') ?? '');
    makeKey.mutate({
      name: String(f.get('name') ?? ''),
      subnets: String(f.get('subnets') ?? '').split(/[\s,]+/).filter(Boolean),
      expires_at: expires === '' ? null : new Date(expires).toISOString(),
    });
  };
  const zoneSelect = (value: string, onChange: (v: string) => void, id?: string) => (
    <select id={id} value={value} onChange={(e) => onChange(e.target.value)} className={FIELD}>
      <option value="">{t('Не назначена')}</option>
      {data.zones.map((z) => <option key={z.id} value={z.id}>{z.name}</option>)}
    </select>
  );

  return (
    <div className="p-6 lg:p-8 space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h3 className="text-xl font-semibold text-gray-900 dark:text-white">{t('Подключение ПК')}</h3>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-400">{t('Агент на ПК подключается к клубу одноразовым кодом или ключом образа бездисковой системы.')}</p>
        </div>
        <a href="/club-computers" className="brand-link text-sm font-medium">{t('Список ПК')}</a>
      </div>
      {status && <div className="rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700" role="status">{status}</div>}
      {error && <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700" role="alert">{error}</div>}

      {data.can_register && (
        <SettingsCard id="enrollment-codes" aria-labelledby="enrollment-codes-title">
          <h5 id="enrollment-codes-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Код подключения ПК')}</h5>
          <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Одноразовый код действует 15 минут. Введите его в агенте на ПК: ПК получит сертификат и появится на карте клуба.')}</p>
          <div className="flex flex-wrap items-end gap-3">
            <div className="min-w-56 flex-1">
              <label htmlFor="code-zone" className={LABEL}>{t('Зона нового ПК')}</label>
              {zoneSelect(codeZone, setCodeZone, 'code-zone')}
            </div>
            <button type="button" disabled={issue.isPending} onClick={() => issue.mutate()} className="brand-button inline-flex items-center gap-2 rounded-lg px-4 py-2"><i className="fas fa-plug"></i>{t('Выдать код')}</button>
          </div>
          {code && (
            <div className="mt-4">
              <label className={LABEL}>{t('Код (показывается один раз)')}</label>
              <Secret value={code.code} />
              <p className="mt-2 text-xs text-gray-500">{t('Действует до {{time}}.', { time: dt(code.expires_at) })}</p>
            </div>
          )}
          {data.codes.length > 0 && (
            <ul className="mt-4 divide-y divide-gray-100 text-sm dark:divide-gray-800" aria-label={t('Действующие коды')}>
              {data.codes.map((c) => (
                <li key={c.id} className="flex flex-wrap items-center justify-between gap-3 py-2 text-gray-700 dark:text-gray-300">
                  <span>{t('Код #{{id}}', { id: c.id })} · {c.computer_id ? t('ПК #{{id}}', { id: c.computer_id }) : zoneName(c.zone_id)} · {t('до {{time}}', { time: dt(c.expires_at) })}</span>
                  <button type="button" className={SMALL_BUTTON + ' py-1'} onClick={() => cancel.mutate(c.id)}>{t('Отменить')}</button>
                </li>
              ))}
            </ul>
          )}
        </SettingsCard>
      )}

      {data.can_register && (data.computers ?? []).length > 0 && (
        <SettingsCard id="club-computers-certs" aria-labelledby="club-computers-certs-title">
          <h5 id="club-computers-certs-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Перерегистрация и отключение ПК')}</h5>
          <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('«Перерегистрировать» отключает агент ПК и выдаёт код для этой карточки: введите его в агенте, и ПК вернётся на своё место на карте. «Отключить» отзывает сертификат: агент не подключится без нового кода.')}</p>
          <div className="flex flex-wrap items-end gap-3">
            <div className="min-w-56 flex-1">
              <label htmlFor="cert-computer" className={LABEL}>{t('ПК')}</label>
              <select id="cert-computer" value={computer} onChange={(e) => setComputer(e.target.value)} className={FIELD}>
                <option value="">{t('Выберите ПК')}</option>
                {(data.computers ?? []).map((c) => <option key={c.id} value={c.id}>{c.name} · {zoneName(c.zone_id)}</option>)}
              </select>
            </div>
            <button type="button" disabled={computer === '' || reregister.isPending} className="brand-button inline-flex items-center gap-2 rounded-lg px-4 py-2"
              onClick={() => { const c = selected(); if (c && window.confirm(t('Перерегистрировать ПК «{{name}}»? Агент отключится до ввода нового кода.', { name: c.name }))) reregister.mutate(c.id); }}>
              <i className="fas fa-rotate"></i>{t('Перерегистрировать')}
            </button>
            <button type="button" disabled={computer === '' || disconnect.isPending} className={SMALL_BUTTON + ' py-2'}
              onClick={() => { const c = selected(); if (c && window.confirm(t('Отключить ПК «{{name}}»? Агент не подключится без нового кода.', { name: c.name }))) disconnect.mutate(c.id); }}>
              {t('Отключить')}
            </button>
          </div>
          {reCode && (
            <div className="mt-4">
              <label className={LABEL}>{t('Код (показывается один раз)')}</label>
              <Secret value={reCode.code} />
              <p className="mt-2 text-xs text-gray-500">{t('Действует до {{time}}.', { time: dt(reCode.expires_at) })}</p>
            </div>
          )}
        </SettingsCard>
      )}

      {data.can_manage_image_keys && (
        <SettingsCard id="image-keys" aria-labelledby="image-keys-title">
          <h5 id="image-keys-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Ключи образа')}</h5>
          <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Для бездисковых ПК: ключ записывается в образ один раз. ПК с известным клубу MAC-адресом подключается сам, новый ПК ждёт подтверждения ниже. Ключ действует только из указанных подсетей.')}</p>
          <form onSubmit={submitKey} className="grid grid-cols-1 gap-4 md:grid-cols-3">
            <div>
              <label htmlFor="key-name" className={LABEL}>{t('Название')}</label>
              <input id="key-name" name="name" maxLength={100} required className={FIELD} />
            </div>
            <div>
              <label htmlFor="key-subnets" className={LABEL}>{t('Подсети клуба')}</label>
              <input id="key-subnets" name="subnets" required placeholder="192.168.10.0/24" className={FIELD} />
            </div>
            <div>
              <label htmlFor="key-expires" className={LABEL}>{t('Действует до')}</label>
              <input id="key-expires" name="expires_at" type="datetime-local" className={FIELD} />
            </div>
            <div className="md:col-span-3">
              <button type="submit" disabled={makeKey.isPending} className="brand-button inline-flex items-center gap-2 rounded-lg px-4 py-2"><i className="fas fa-key"></i>{t('Создать ключ образа')}</button>
            </div>
          </form>
          {key && (
            <div className="mt-4">
              <label className={LABEL}>{t('Ключ образа (показывается один раз)')}</label>
              <Secret value={key} />
            </div>
          )}
          {data.image_keys.length > 0 && (
            <div className="mt-4 overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead className="bg-gray-50 text-xs text-gray-500 dark:bg-gray-800 dark:text-gray-400">
                  <tr>
                    <th className={TH}>{t('Название')}</th>
                    <th className={TH}>{t('Ключ')}</th>
                    <th className={TH}>{t('Подсети клуба')}</th>
                    <th className={TH}>{t('Создан')}</th>
                    <th className={TH}>{t('Последнее использование')}</th>
                    <th className={TH}></th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                  {data.image_keys.map((k) => (
                    <tr key={k.id} className="align-top text-gray-700 dark:text-gray-300">
                      <td className={TD}>{k.name}</td>
                      <td className={TD + ' font-mono text-xs'}>{k.prefix}••••••••</td>
                      <td className={TD + ' font-mono text-xs'}>{k.subnets.join(', ')}</td>
                      <td className={TD + ' text-xs'}>{dt(k.created_at)}</td>
                      <td className={TD + ' text-xs'}>{dt(k.last_used_at)}</td>
                      <td className={TD + ' text-right'}>
                        {k.revoked_at ? (
                          <span className="text-xs text-gray-500">{t('Отозван')}</span>
                        ) : (
                          <button type="button" className={SMALL_BUTTON + ' py-1'} onClick={() => { if (window.confirm(t('Отозвать ключ образа «{{name}}»?', { name: k.name }))) revoke.mutate(k.id); }}>{t('Отозвать')}</button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </SettingsCard>
      )}

      {data.can_register && (
        <SettingsCard id="pending-computers" aria-labelledby="pending-title">
          <h5 id="pending-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('ПК, ожидающие подтверждения')}</h5>
          {data.pending.length === 0 ? (
            <p className="text-sm text-gray-600 dark:text-gray-400">{t('Новых ПК нет.')}</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead className="bg-gray-50 text-xs text-gray-500 dark:bg-gray-800 dark:text-gray-400">
                  <tr>
                    <th className={TH}>MAC</th>
                    <th className={TH}>{t('Имя ПК')}</th>
                    <th className={TH}>IP</th>
                    <th className={TH}>{t('Последнее обращение')}</th>
                    <th className={TH}>{t('Зона')}</th>
                    <th className={TH}></th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                  {data.pending.map((p) => (
                    <tr key={p.id} className="align-top text-gray-700 dark:text-gray-300">
                      <td className={TD + ' font-mono text-xs'}>{p.mac_address}</td>
                      <td className={TD}>{p.hostname ?? '—'}</td>
                      <td className={TD + ' font-mono text-xs'}>{p.ip ?? '—'}</td>
                      <td className={TD + ' text-xs'}>{dt(p.last_seen_at)}</td>
                      <td className={TD}>{zoneSelect(pendingZone[p.id] ?? '', (v) => setPendingZone((s) => ({ ...s, [p.id]: v })))}</td>
                      <td className={TD + ' whitespace-nowrap text-right'}>
                        <button type="button" className="brand-button rounded-lg px-3 py-1 text-sm" onClick={() => approve.mutate({ id: p.id, zone: zoneOf(pendingZone[p.id] ?? '') })}>{t('Подтвердить')}</button>{' '}
                        <button type="button" className={SMALL_BUTTON + ' py-1'} onClick={() => reject.mutate(p.id)}>{t('Отклонить')}</button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </SettingsCard>
      )}
    </div>
  );
}
