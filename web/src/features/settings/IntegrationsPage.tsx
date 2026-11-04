import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { SettingsCard } from '../../components/SettingsCard';
import { SettingsLayout } from '../../components/SettingsLayout';
import { formatDate } from '../../lib/datetime';
import { allErrors, deliveriesPage, integrationsApi, integrationsQuery, type CreatedSecret, type DeliveryPage } from './api';
import { unwrap } from '../../api/http';
import { useSettingsResult } from './status';

/*
 * Настройки → API и интеграции — перенос resources/views/settings/integrations.blade.php (IntegrationController):
 * ключи API, вебхуки, журнал доставок (keyset: «Показать ещё»), доступ сотрудников, последние запросы к API.
 * Новый секрет показывается один раз — из ответа, который его создал (в URL и хранилище браузера не попадает).
 */

const FIELD = 'w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100';
const CARD = 'rounded-xl border border-gray-200 p-4 dark:border-gray-700';
const TONES = {
  green: 'bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-300',
  red: 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300',
  amber: 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300',
  gray: 'bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-300',
} as const;
const badge = (tone: keyof typeof TONES) => `${TONES[tone]} inline-block rounded-full px-2 py-0.5 text-xs font-medium`;
const ROLE_LABELS: Record<string, string> = { admin: 'Администратор', manager: 'Менеджер', cashier: 'Кассир', operator: 'Оператор' };

function formValues(form: HTMLFormElement) {
  const fd = new FormData(form);
  return { get: (k: string) => String(fd.get(k) ?? ''), all: (k: string) => fd.getAll(k).map(String) };
}

export function IntegrationsPage() {
  const { t } = useTranslation();
  const { me } = useSession();
  const qc = useQueryClient();
  const { data } = useQuery(integrationsQuery());
  const { status, done } = useSettingsResult();
  const [errors, setErrors] = useState<string[]>([]);
  const [created, setCreated] = useState<CreatedSecret | null>(null);
  const [copied, setCopied] = useState(false);
  const [more, setMore] = useState<DeliveryPage | null>(null);
  const readOnly = me?.read_only === true;
  const tz = me?.club?.timezone ?? 'UTC';
  const fmt = (v: string | null | undefined, f: string) => (v ? formatDate(v, tz, f) : null);

  const reload = () => {
    setMore(null);
    return qc.invalidateQueries({ queryKey: ['integrations'] });
  };
  const onError = (e: unknown) => {
    setCreated(null);
    setErrors(allErrors(e));
    window.scrollTo?.({ top: 0 });
  };
  const run = useMutation({
    mutationFn: (fn: () => Promise<{ message: string } | CreatedSecret>) => fn(),
    onSuccess: async (r, _v, _c) => {
      setErrors([]);
      setCopied(false);
      if ('secret' in r) {
        setCreated(r);
        window.scrollTo?.({ top: 0 });
      } else {
        setCreated(null);
        done(r.message);
      }
      await reload();
    },
    onError,
  });
  const act = (fn: () => Promise<{ message: string } | CreatedSecret>, confirmText?: string) => (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (readOnly || (confirmText && !window.confirm(confirmText))) return;
    run.mutate(fn);
  };

  if (!data) return null;
  const scopeMeta = new Map(data.scopes.map((s) => [s.scope, s]));
  const deliveries = [...data.deliveries.items, ...(more?.items ?? [])];
  const nextCursor = more ? more.next_cursor : data.deliveries.next_cursor;
  const loadMore = async () => {
    if (!nextCursor) return;
    const page = await deliveriesPage(nextCursor);
    setMore((prev) => ({ items: [...(prev?.items ?? []), ...page.items], next_cursor: page.next_cursor }));
  };
  const deliveryTone = (s: string) => ({ delivered: 'green', failed: 'red', pending: 'amber' } as const)[s as 'delivered'] ?? 'gray';
  const deliveryLabel = (s: string) => ({ delivered: t('доставлено'), failed: t('ошибка'), pending: t('в очереди'), skipped: t('пропущено') } as Record<string, string>)[s] ?? s;

  return (
    <SettingsLayout status={status}>
      <div className="space-y-6">
        <SettingsCard>
          <h4 className="mb-2 text-xl font-semibold text-gray-900 dark:text-white">{t('API и интеграции')}</h4>
          <p className="text-sm text-gray-600 dark:text-gray-400">{t('Ключи API дают внешним системам доступ только к данным этого клуба и только к выбранным операциям.')}</p>
          <div className="mt-3 flex flex-wrap gap-2 text-sm">
            <span className="break-all rounded-lg bg-gray-100 px-3 py-1 font-mono text-gray-700 dark:bg-gray-800 dark:text-gray-300">{`${location.origin}/api/v1`}</span>
            <a href="/docs/api" target="_blank" rel="noopener" className="rounded-lg border border-gray-300 px-3 py-1 text-gray-700 hover:bg-gray-50 dark:border-gray-700 dark:text-gray-300 dark:hover:bg-gray-800"><i className="fas fa-book mr-1"></i>{t('Документация API')}</a>
          </div>
          <p className="mt-3 text-xs text-gray-500 dark:text-gray-400">{t('Не встраивайте ключи в мобильные приложения и сайты: мобильные приложения входят через аккаунт пользователя.')}</p>
        </SettingsCard>

        {errors.length > 0 && (
          <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300" role="alert">
            {errors.map((m, i) => <p key={i}>{m}</p>)}
          </div>
        )}

        {created && (
          <div className="rounded-xl border-2 border-amber-300 bg-amber-50 p-4 dark:border-amber-700 dark:bg-amber-900/20" role="status">
            <p className="mb-2 font-semibold text-amber-900 dark:text-amber-200">
              {created.type === 'key' ? t('Новый API-ключ «{{name}}»', { name: created.name }) : t('Секрет подписи вебхука {{name}}', { name: created.name })}
            </p>
            <div className="flex gap-2">
              <input id="new-secret" readOnly value={created.secret} className="min-w-0 flex-1 rounded-lg border border-amber-300 bg-white px-3 py-2 font-mono text-sm dark:border-amber-700 dark:bg-gray-900 dark:text-white" autoComplete="off" spellCheck={false} />
              <button type="button" data-copy-target="new-secret" className="rounded-lg border border-amber-400 px-3 text-sm text-amber-900 dark:text-amber-200"
                onClick={(e) => {
                  (document.getElementById('new-secret') as HTMLInputElement | null)?.select();
                  void navigator.clipboard?.writeText(created.secret).catch(() => document.execCommand('copy'));
                  setCopied(true);
                  e.currentTarget.blur();
                }}>
                {copied ? t('Скопировано') : t('Копировать')}
              </button>
            </div>
            <p className="mt-2 text-sm text-amber-900 dark:text-amber-200">{t('Сохраните его сейчас: он показывается один раз, в Zafiro хранится только его хеш.')}</p>
          </div>
        )}

        {/* API keys */}
        <SettingsCard>
          <h5 className="mb-3 text-lg font-semibold text-gray-900 dark:text-white">{t('Ключи API')}</h5>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[680px] text-sm">
              <thead><tr className="text-left text-gray-600 dark:text-gray-400">
                <th className="px-3 py-2">{t('Название')}</th><th className="px-3 py-2">{t('Ключ')}</th><th className="px-3 py-2">{t('Права')}</th><th className="px-3 py-2">{t('Ограничения')}</th><th className="px-3 py-2">{t('Последнее использование')}</th><th className="px-3 py-2">{t('Статус')}</th><th className="px-3 py-2"></th>
              </tr></thead>
              <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                {data.keys.length === 0 && <tr><td colSpan={7} className="px-3 py-4 text-gray-500">{t('Ключей пока нет.')}</td></tr>}
                {data.keys.map((k) => (
                  <tr key={k.id} className="align-top">
                    <td className="px-3 py-2"><span className="font-medium text-gray-900 dark:text-white">{k.name}</span><br /><span className="text-xs text-gray-500">{k.creator_name}, {fmt(k.created_at, 'd.m.Y')}</span></td>
                    <td className="px-3 py-2 font-mono text-xs text-gray-700 dark:text-gray-300">{k.prefix}_…</td>
                    <td className="px-3 py-2">
                      {k.scopes.map((s) => (
                        <span key={s} className={`${badge(scopeMeta.get(s)?.dangerous ? 'amber' : 'gray')} mb-1`} title={t(scopeMeta.get(s)?.label ?? s)}>{s}</span>
                      ))}
                    </td>
                    <td className="px-3 py-2 text-xs text-gray-600 dark:text-gray-400">
                      <div>{t('IP')}: {k.allowed_ips.length > 0 ? k.allowed_ips.join(', ') : t('любой')}</div>
                      <div>{t('Лимит')}: {k.rate_limit_per_minute}/{t('мин')}</div>
                      <div>{t('Действует до')}: {fmt(k.expires_at, 'd.m.Y') ?? t('бессрочно')}</div>
                    </td>
                    <td className="px-3 py-2 text-xs text-gray-600 dark:text-gray-400">{k.last_used_at ? `${fmt(k.last_used_at, 'd.m.Y H:i')} · ${k.last_used_ip ?? ''}` : '—'}</td>
                    <td className="px-3 py-2">
                      {k.status === 'revoked' ? <span className={badge('red')}>{t('отозван')}</span>
                        : k.status === 'expired' ? <span className={badge('gray')}>{t('истёк')}</span>
                          : <span className={badge('green')}>{t('активен')}</span>}
                    </td>
                    <td className="px-3 py-2">
                      {k.status !== 'revoked' && (
                        <div className="flex flex-col items-stretch gap-2">
                          <form method="POST" action={`/settings/integrations/keys/${k.id}/rotate`} onSubmit={act(() => unwrap(integrationsApi.POST('/api/settings/integrations/keys/{keyId}/rotate', { params: { path: { keyId: k.id } } })), t('Выпустить новый ключ с теми же правами? Старый перестанет работать сразу.'))}>
                            <button type="submit" className="w-full rounded-lg border border-gray-300 px-3 py-1 text-sm dark:border-gray-700 dark:text-gray-200">{t('Перевыпустить')}</button>
                          </form>
                          <form method="POST" action={`/settings/integrations/keys/${k.id}`} onSubmit={act(() => unwrap(integrationsApi.DELETE('/api/settings/integrations/keys/{keyId}', { params: { path: { keyId: k.id } } })), t('Отозвать ключ? Интеграции с ним перестанут работать.'))}>
                            <button type="submit" className="w-full rounded-lg border border-red-200 px-3 py-1 text-sm text-red-700 dark:border-red-800 dark:text-red-300">{t('Отозвать')}</button>
                          </form>
                        </div>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <form method="POST" action="/settings/integrations/keys" className={`${CARD} mt-4 space-y-4`}
            onSubmit={(e) => {
              const v = formValues(e.currentTarget);
              const form = e.currentTarget;
              act(async () => {
                const r = await unwrap(integrationsApi.POST('/api/settings/integrations/keys', { body: {
                  name: v.get('name'), scopes: v.all('scopes[]'), allowed_ips: v.get('allowed_ips'),
                  rate_limit_per_minute: v.get('rate_limit_per_minute'), expires_at: v.get('expires_at'),
                } }));
                form.reset();
                return r;
              })(e);
            }}>
            <h6 className="font-semibold text-gray-900 dark:text-white">{t('Новый ключ')}</h6>
            <div className="grid gap-4 md:grid-cols-2">
              <label><span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Название')}</span><input name="name" required maxLength={100} placeholder={t('Например: 1С склад')} className={FIELD} /></label>
              <label><span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Действует до')}</span><input type="date" name="expires_at" min={data.min_expires_at} className={FIELD} /><span className="mt-1 block text-xs text-gray-500">{t('Пусто — бессрочно.')}</span></label>
              <label><span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Разрешённые IP или подсети')}</span><textarea name="allowed_ips" rows={2} maxLength={2000} placeholder={'203.0.113.10\n198.51.100.0/24'} className={`${FIELD} font-mono text-sm`}></textarea><span className="mt-1 block text-xs text-gray-500">{t('По одному в строке. Пусто — с любого адреса.')}</span></label>
              <label><span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Запросов в минуту')}</span><input type="number" name="rate_limit_per_minute" min="1" max={data.max_rate_limit} placeholder={String(data.default_rate_limit)} className={FIELD} /></label>
            </div>
            <fieldset>
              <legend className="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">{t('Права ключа')}</legend>
              <div className="grid gap-2 md:grid-cols-2">
                {data.scopes.map((s) => (
                  <label key={s.scope} className={`flex items-start gap-2 rounded-lg border px-3 py-2 text-sm ${s.dangerous ? 'border-amber-300 bg-amber-50 dark:border-amber-700 dark:bg-amber-900/20' : 'border-gray-200 dark:border-gray-700'}`}>
                    <input type="checkbox" name="scopes[]" value={s.scope} className="mt-1" />
                    <span><span className="block text-gray-900 dark:text-gray-100">{t(s.label)}</span><span className="font-mono text-xs text-gray-500">{s.scope}</span>{s.dangerous && <span className="ml-2 text-xs font-medium text-amber-800 dark:text-amber-300">{t('опасное')}</span>}</span>
                  </label>
                ))}
              </div>
            </fieldset>
            <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">{t('Создать ключ')}</button>
          </form>
        </SettingsCard>

        {/* Webhooks */}
        <SettingsCard>
          <h5 className="mb-1 text-lg font-semibold text-gray-900 dark:text-white">{t('Вебхуки')}</h5>
          <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Zafiro отправляет POST-запрос с подписью HMAC-SHA256 в заголовке X-Zafiro-Signature. Неудачные отправки повторяются.')}</p>
          <div className="space-y-3">
            {data.webhooks.length === 0 && <p className="text-sm text-gray-500">{t('Вебхуков пока нет.')}</p>}
            {data.webhooks.map((w) => (
              <div key={w.id} className={CARD}>
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div className="min-w-0">
                    <p className="break-all font-mono text-sm text-gray-900 dark:text-white">{w.url}</p>
                    <p className="mt-1 text-xs text-gray-500">{w.events.join(', ')}</p>
                    <p className="mt-1 text-xs text-gray-500">
                      {t('Успешно')}: {fmt(w.last_success_at, 'd.m.Y H:i') ?? '—'} ·{' '}
                      {t('Ошибка')}: {fmt(w.last_failure_at, 'd.m.Y H:i') ?? '—'} ·{' '}
                      {t('Ошибок подряд')}: {w.failure_count}
                    </p>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <span className={badge(w.is_active ? 'green' : 'gray')}>{w.is_active ? t('включён') : t('выключен')}</span>
                    <form method="POST" action={`/settings/integrations/webhooks/${w.id}`} onSubmit={act(() => unwrap(integrationsApi.PATCH('/api/settings/integrations/webhooks/{webhookId}', { params: { path: { webhookId: w.id } }, body: { is_active: !w.is_active } })))}>
                      <button type="submit" className="rounded-lg border border-gray-300 px-3 py-1 text-sm dark:border-gray-700 dark:text-gray-200">{w.is_active ? t('Выключить') : t('Включить')}</button>
                    </form>
                    <form method="POST" action={`/settings/integrations/webhooks/${w.id}/secret`} onSubmit={act(() => unwrap(integrationsApi.POST('/api/settings/integrations/webhooks/{webhookId}/secret', { params: { path: { webhookId: w.id } } })), t('Выпустить новый секрет? Получатель должен будет обновить его.'))}>
                      <button type="submit" className="rounded-lg border border-gray-300 px-3 py-1 text-sm dark:border-gray-700 dark:text-gray-200">{t('Новый секрет')}</button>
                    </form>
                    <form method="POST" action={`/settings/integrations/webhooks/${w.id}`} onSubmit={act(() => unwrap(integrationsApi.DELETE('/api/settings/integrations/webhooks/{webhookId}', { params: { path: { webhookId: w.id } } })), t('Удалить вебхук?'))}>
                      <button type="submit" className="rounded-lg border border-red-200 px-3 py-1 text-sm text-red-700 dark:border-red-800 dark:text-red-300">{t('Удалить')}</button>
                    </form>
                  </div>
                </div>
              </div>
            ))}
          </div>

          <form method="POST" action="/settings/integrations/webhooks" className={`${CARD} mt-4 space-y-4`}
            onSubmit={(e) => {
              const v = formValues(e.currentTarget);
              const form = e.currentTarget;
              act(async () => {
                const r = await unwrap(integrationsApi.POST('/api/settings/integrations/webhooks', { body: { url: v.get('url'), events: v.all('events[]') } }));
                form.reset();
                return r;
              })(e);
            }}>
            <h6 className="font-semibold text-gray-900 dark:text-white">{t('Новый вебхук')}</h6>
            <label className="block"><span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Адрес (https://)')}</span><input type="url" name="url" required maxLength={500} placeholder="https://example.com/zafiro/webhook" className={`${FIELD} font-mono text-sm`} /></label>
            <fieldset>
              <legend className="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">{t('События')}</legend>
              <div className="grid gap-2 md:grid-cols-2">
                {data.events.map((ev) => (
                  <label key={ev.event} className="flex items-start gap-2 rounded-lg border border-gray-200 px-3 py-2 text-sm dark:border-gray-700">
                    <input type="checkbox" name="events[]" value={ev.event} className="mt-1" />
                    <span><span className="block text-gray-900 dark:text-gray-100">{t(ev.label)}</span><span className="font-mono text-xs text-gray-500">{ev.event}</span></span>
                  </label>
                ))}
              </div>
            </fieldset>
            <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">{t('Добавить вебхук')}</button>
          </form>

          {deliveries.length > 0 && (
            <>
              <h6 className="mb-2 mt-6 font-semibold text-gray-900 dark:text-white">{t('Последние отправки')}</h6>
              <div className="overflow-x-auto">
                <table className="w-full min-w-[640px] text-sm">
                  <thead><tr className="text-left text-gray-600 dark:text-gray-400"><th className="px-3 py-2">{t('Время')}</th><th className="px-3 py-2">{t('Событие')}</th><th className="px-3 py-2">{t('Адрес')}</th><th className="px-3 py-2">{t('Статус')}</th><th className="px-3 py-2">{t('Попыток')}</th><th className="px-3 py-2"></th></tr></thead>
                  <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                    {deliveries.map((d) => (
                      <tr key={d.id}>
                        <td className="px-3 py-2 whitespace-nowrap text-gray-600 dark:text-gray-400">{fmt(d.created_at, 'd.m H:i:s')}</td>
                        <td className="px-3 py-2 font-mono text-xs">{d.event}</td>
                        <td className="px-3 py-2 max-w-[16rem] truncate font-mono text-xs text-gray-600 dark:text-gray-400" title={d.endpoint_url ?? undefined}>{d.endpoint_url}</td>
                        <td className="px-3 py-2">
                          <span className={badge(deliveryTone(d.status))}>{deliveryLabel(d.status)}</span>
                          {d.last_error && <span className="block text-xs text-gray-500">{d.last_error}</span>}
                        </td>
                        <td className="px-3 py-2 tabular-nums">{d.attempts}</td>
                        <td className="px-3 py-2 text-right">
                          {(d.status === 'failed' || d.status === 'skipped') && (
                            <form method="POST" action={`/settings/integrations/deliveries/${d.id}/retry`} onSubmit={act(() => unwrap(integrationsApi.POST('/api/settings/integrations/deliveries/{deliveryId}/retry', { params: { path: { deliveryId: d.id } } })))}><button type="submit" className="rounded-lg border border-gray-300 px-3 py-1 text-sm dark:border-gray-700 dark:text-gray-200">{t('Повторить')}</button></form>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {/* Журнал растёт: в старом — 20 последних; здесь — следующая страница по keyset (отличие, docs/open-questions/stage3-settings.md S-5). */}
              {nextCursor && (
                <button type="button" className="mt-3 rounded-lg border border-gray-300 px-3 py-1 text-sm dark:border-gray-700 dark:text-gray-200" onClick={() => void loadMore()}>{t('Показать ещё')}</button>
              )}
            </>
          )}
        </SettingsCard>

        {/* Who may manage integrations */}
        {data.is_owner && (
          <SettingsCard>
            <h5 className="mb-1 text-lg font-semibold text-gray-900 dark:text-white">{t('Доступ к интеграциям')}</h5>
            <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Владелец управляет интеграциями всегда. Ключи сотрудника перестают работать, когда у него снимают доступ.')}</p>
            <div className="divide-y divide-gray-100 rounded-xl border border-gray-200 dark:divide-gray-800 dark:border-gray-700">
              {data.staff.length === 0 && <p className="px-4 py-3 text-sm text-gray-500">{t('В клубе нет сотрудников.')}</p>}
              {data.staff.map((m) => (
                <div key={m.membership_id} className="flex items-center justify-between gap-3 px-4 py-2 text-sm">
                  <span className="text-gray-900 dark:text-gray-100">{m.name} <span className="text-xs text-gray-500">· {ROLE_LABELS[m.role] !== undefined ? t(ROLE_LABELS[m.role] ?? '') : m.role}</span></span>
                  <form method="POST" action={`/settings/integrations/members/${m.membership_id}`} onSubmit={act(() => unwrap(integrationsApi.PATCH('/api/settings/integrations/members/{membershipId}', { params: { path: { membershipId: m.membership_id } }, body: { can_manage_integrations: !m.can_manage_integrations } })))}>
                    <button type="submit"
                      className={`rounded-lg border px-3 py-1 text-sm ${m.can_manage_integrations ? 'border-green-300 text-green-800 dark:border-green-700 dark:text-green-300' : 'border-gray-300 text-gray-700 dark:border-gray-700 dark:text-gray-300'}`}
                      aria-label={m.can_manage_integrations ? t('Снять доступ к интеграциям: {{user}}', { user: m.name ?? '' }) : t('Выдать доступ к интеграциям: {{user}}', { user: m.name ?? '' })}>
                      {m.can_manage_integrations ? t('Есть доступ') : t('Нет доступа')}
                    </button>
                  </form>
                </div>
              ))}
            </div>
          </SettingsCard>
        )}

        {/* Recent API calls */}
        <SettingsCard>
          <h5 className="mb-3 text-lg font-semibold text-gray-900 dark:text-white">{t('Последние запросы к API')}</h5>
          {data.requests.length === 0 ? (
            <p className="text-sm text-gray-500">{t('Запросов пока не было.')}</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[640px] text-sm">
                <thead><tr className="text-left text-gray-600 dark:text-gray-400"><th className="px-3 py-2">{t('Время')}</th><th className="px-3 py-2">{t('Ключ')}</th><th className="px-3 py-2">{t('Запрос')}</th><th className="px-3 py-2">{t('Статус')}</th><th className="px-3 py-2 text-right">{t('мс')}</th><th className="px-3 py-2">IP</th></tr></thead>
                <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
                  {data.requests.map((r, i) => (
                    <tr key={i}>
                      <td className="px-3 py-2 whitespace-nowrap text-gray-600 dark:text-gray-400">{fmt(r.created_at, 'd.m H:i:s')}</td>
                      <td className="px-3 py-2 font-mono text-xs">{r.key_prefix ?? '—'}</td>
                      <td className="px-3 py-2 font-mono text-xs break-all">{r.method} {r.endpoint}</td>
                      <td className="px-3 py-2"><span className={badge(r.status < 300 ? 'green' : r.status < 500 ? 'amber' : 'red')}>{r.status}</span></td>
                      <td className="px-3 py-2 text-right tabular-nums">{r.duration_ms}</td>
                      <td className="px-3 py-2 font-mono text-xs">{r.ip}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </SettingsCard>
      </div>
    </SettingsLayout>
  );
}
