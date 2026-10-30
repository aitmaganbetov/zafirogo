import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { type FormEvent, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { can, session, useSession } from '../../auth/session';
import { SettingsCard } from '../../components/SettingsCard';
import { SettingsLayout } from '../../components/SettingsLayout';
import { formatDate } from '../../lib/datetime';
import {
  clubSettingsQuery, deleteLogo, deleteSigninImage, firstError, minutesToInput, saveAgentPin, saveStaffTime, saveTimezone,
  uploadImage,
} from './api';
import { SettingsAnchors } from './SettingsAnchors';
import { SettingsForm } from './SettingsForm';
import { useSettingsResult } from './status';

/*
 * Настройки → Настройка клуба — перенос resources/views/settings/club.blade.php (SettingsController::club()):
 * секция club (форма settings.save), «Ключ установки клуба» (Q-037: заменён кодами регистрации и ключами образа —
 * ссылка на «Подключение ПК»), логотип, картинка экрана входа, часовой пояс, PIN выхода из агента и время
 * обслуживания ПК.
 */

const INPUT = 'brand-focus w-full rounded-lg border border-gray-300 dark:border-gray-700 bg-gray-50 dark:bg-gray-800 px-4 py-2 text-gray-900 dark:text-gray-100 focus:outline-none';
const INPUT2 = 'brand-focus w-full rounded-lg border border-gray-300 bg-gray-50 px-4 py-2 text-gray-900 focus:outline-none dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100';
const SMALL = 'w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100';
const PIN_INPUT = `${SMALL.replace('text-gray-900', 'font-mono text-gray-900')}`;

/** Мутация формы карточки: успех — status и перечитать настройки, ошибка — у своей формы. */
function useSettingsAction<A>(form: string, fn: (a: A) => Promise<{ message: string }>, refresh: (m: string) => Promise<unknown>,
  fail: (form: string) => (e: unknown) => void, after?: () => Promise<unknown>) {
  return useMutation({
    mutationFn: fn,
    onSuccess: async (r) => {
      await after?.();
      await refresh(r.message);
    },
    onError: fail(form),
  });
}

function ErrorBox({ message }: { message?: string }) {
  if (!message) return null;
  return (
    <div className="mb-3 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700" role="alert">
      {message}
    </div>
  );
}

export function ClubSettingsPage() {
  const { t } = useTranslation();
  const { me } = useSession();
  const qc = useQueryClient();
  const { data } = useQuery(clubSettingsQuery());
  const { status, done, fail, errorOf } = useSettingsResult();
  const readOnly = me?.read_only === true;
  const tz = me?.club?.timezone ?? 'UTC';

  const refresh = (message: string) => {
    done(message);
    return qc.invalidateQueries({ queryKey: ['settings'] });
  };
  const useAction = <A,>(form: string, fn: (a: A) => Promise<{ message: string }>, after?: () => Promise<unknown>) =>
    useSettingsAction(form, fn, refresh, fail, after);

  const logo = useAction('logo', (f: FormData) => uploadImage('/api/settings/logo', f));
  const logoDelete = useAction('logo', () => deleteLogo());
  const signin = useAction('image', (f: FormData) => uploadImage('/api/settings/signin-image', f));
  const signinDelete = useAction('image', () => deleteSigninImage());
  // Пояс клуба — в /api/auth/me (часы панели, даты экранов): перечитать.
  const timezone = useAction('timezone', (v: string) => saveTimezone(v), () => session.reloadMe());
  const pin = useAction('pin', ({ p, c }: { p: string; c: string }) => saveAgentPin(p, c));
  const minutes = useAction('minutes', (v: string) => saveStaffTime(v));

  if (!data) return null;
  const club = data.settings.club;
  const fd = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    return new FormData(e.currentTarget);
  };
  const str = (f: FormData, k: string) => String(f.get(k) ?? '');

  const anchors: ReadonlyArray<readonly [string, string]> = [
    ['club-sessions', t('Сессии и ПК')],
    ['agent-settings', t('Агент на ПК')],
    ['shift-limits', t('Сотрудники на смене')],
    ['club-key', t('Ключ установки клуба')],
    ['club-logo', t('Логотип клуба')],
    ['signin-image', t('Картинка экрана входа')],
    ['timezone', t('Часовой пояс')],
    ['agent-pin', t('PIN-код для выхода из агента')],
  ];
  const minuteFields: ReadonlyArray<readonly [keyof typeof club, string]> = [
    ['pc_restart_after_session_end', t('Через сколько минут выполнить действие после завершения сессии')],
    ['session_idle_timeout', t('Время завершения сессии при бездействии (в минутах)')],
    ['session_auto_terminate_when_pc_unavailable', t('Время до автоматического завершения сессии при недоступности ПК (в минутах)')],
    ['booking_interval', t('Интервал между бронированиями (в минутах)')],
  ];
  const actions: ReadonlyArray<readonly [string, string]> = [
    ['none', t('Ничего: только заблокировать ПК')],
    ['restart', t('Перезагрузить ПК')],
    ['shutdown', t('Выключить ПК')],
  ];
  const canPin = data.can_manage_agent_pin;

  const card = (id: string, title: string, children: ReactNode) => (
    <SettingsCard id={id} aria-labelledby={`${id}-title`}>
      <h5 id={`${id}-title`} className="mb-2 font-semibold text-gray-900 dark:text-white">{title}</h5>
      {children}
    </SettingsCard>
  );

  return (
    <SettingsLayout status={status} anchors={<SettingsAnchors anchors={anchors} />}>
      <SettingsForm section="club" readOnly={readOnly} onDone={(m) => void refresh(m)} onError={fail('club')}>
        <h4 className="text-xl font-semibold text-gray-900 dark:text-white">{t('Настройки клуба')}</h4>
        <SettingsCard id="club-sessions" aria-labelledby="club-sessions-title">
          <h5 id="club-sessions-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Сессии и ПК')}</h5>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
              <label htmlFor="pc_action_after_session_end" className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">{t('Действие с ПК после завершения сессии')}</label>
              <select id="pc_action_after_session_end" name="pc_action_after_session_end" className={INPUT} defaultValue={club.pc_action_after_session_end}>
                {actions.map(([v, label]) => (
                  <option key={v} value={v}>{label}</option>
                ))}
              </select>
              <p className="mt-1 text-xs text-gray-500">{t('Агент выполнит действие, если за это время клиент не оплатит новую сессию.')}</p>
            </div>
            {minuteFields.map(([field, label]) => (
              <div key={field}>
                <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">{label}</label>
                <input type="number" step="0.01" min="0" max="10080" name={field} defaultValue={minutesToInput(club[field] as number)} required className={INPUT} />
              </div>
            ))}
          </div>
        </SettingsCard>

        <SettingsCard id="agent-settings" aria-labelledby="agent-settings-title">
          <h5 id="agent-settings-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Агент на ПК')}</h5>
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            <div>
              <label htmlFor="agent_idle_logout_minutes" className="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Если нет активной сессии, через сколько отлогинить пользователя (в минутах)')}</label>
              <input id="agent_idle_logout_minutes" type="number" min="0" max="1440" step="1" name="agent_idle_logout_minutes" defaultValue={minutesToInput(club.agent_idle_logout_minutes)} className={INPUT2} />
              <p className="mt-1 text-xs text-gray-500">{t('Клиент вошёл и не купил тариф или сессия закончилась, а он не вышел: агент сам выйдет из аккаунта и освободит ПК. 0 — не выходить.')}</p>
            </div>
            <div className="flex items-center gap-3 self-start rounded-xl border border-gray-200 p-4 dark:border-gray-700">
              <label htmlFor="agent_shop_enabled" className="text-sm font-medium text-gray-700 dark:text-gray-300">{t('Включить магазин в агенте')}</label>
              <input type="hidden" name="agent_shop_enabled" value="0" />
              <input id="agent_shop_enabled" type="checkbox" role="switch" name="agent_shop_enabled" value="1" className="settings-switch" defaultChecked={club.agent_shop_enabled} />
            </div>
            <div className="rounded-xl border border-gray-200 p-4 md:col-span-2 dark:border-gray-700">
              <div className="flex items-center gap-3">
                <label htmlFor="agent_auto_update" className="text-sm font-medium text-gray-700 dark:text-gray-300">{t('Автообновление агента')}</label>
                <input type="hidden" name="agent_auto_update" value="0" />
                <input id="agent_auto_update" type="checkbox" role="switch" name="agent_auto_update" value="1" className="settings-switch" defaultChecked={club.agent_auto_update} />
              </div>
              <p className="mt-1 text-xs text-gray-500">{t('Новая версия агента ставится сама, когда ПК свободен: никто не вошёл и нет оплаченной сессии. На бездисковой системе обновление пропадает после перезагрузки — выключите автообновление и обновите агент в образе на сервере загрузки.')}</p>
            </div>
          </div>
        </SettingsCard>

        <SettingsCard id="shift-limits" aria-labelledby="shift-limits-title">
          <h5 id="shift-limits-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Сотрудники на смене')}</h5>
          <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Столько кассиров и операторов могут одновременно быть в панели. Следующий войдёт, когда кто-то выйдет (или через 2 часа без действий). 0 — без ограничения.')}</p>
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            {([['max_cashiers_on_shift', t('Сколько кассиров может быть на смене')], ['max_operators_on_shift', t('Сколько операторов может быть на смене')]] as const).map(([field, label]) => (
              <div key={field}>
                <label htmlFor={field} className="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">{label}</label>
                <input id={field} type="number" min="0" max="100" step="1" name={field} defaultValue={minutesToInput(club[field])} className={INPUT2} />
              </div>
            ))}
          </div>
        </SettingsCard>

        <SettingsCard id="club-key" aria-labelledby="club-key-title">
          <h5 id="club-key-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Ключ установки клуба')}</h5>
          {/* Q-037: постоянного ключа клуба нет — ПК подключаются по коду регистрации или ключу образа (G1). Разметка — как у
              блока ключа (подпись, поле и кнопка), чтобы карточка не изменила высоту. */}
          <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('ПК клуба подключаются по коду регистрации или ключу образа.')}</p>
          <label htmlFor="enrollment-link" className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Подключение ПК')}</label>
          <div className="flex gap-2">
            <input id="enrollment-link" readOnly value="/club-computers/enrollment" className="min-w-0 flex-1 rounded-lg border border-gray-300 bg-white px-3 py-2 font-mono text-sm dark:border-gray-700 dark:bg-gray-900 dark:text-white" />
            {can(me, 'club_map.view') && <a href="/club-computers/enrollment" className="flex items-center rounded-lg border border-gray-300 px-3 text-sm dark:border-gray-700">{t('Открыть')}</a>}
          </div>
          <p className="mt-2 text-xs text-gray-500">{t('Код регистрации действует один раз, ключ образа можно отозвать — на странице «Подключение ПК».')}</p>
        </SettingsCard>
      </SettingsForm>

      {card('club-logo', t('Логотип клуба'), (
        <>
          <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Логотип и название клуба показываются на свободных мониторах ПК, пока сессия не оплачена. PNG или JPG, до 1 МБ.')}</p>
          <ErrorBox message={firstError(errorOf('logo'), 'logo')} />
          <div className="flex flex-wrap items-center gap-4">
            <div className="flex h-24 w-40 items-center justify-center rounded-lg bg-black p-3">
              {data.logo_url ? (
                <img src={data.logo_url} alt={t('Логотип клуба')} className="max-h-full max-w-full object-contain" />
              ) : (
                <span className="truncate text-sm font-semibold text-white">{data.club_name}</span>
              )}
            </div>
            <div className="min-w-0 flex-1 basis-64">
              {!data.logo_url && <p className="mb-2 text-sm text-gray-600 dark:text-gray-400">{t('Логотип не загружен: на мониторах показывается только название клуба.')}</p>}
              <form method="POST" action="/settings/logo" encType="multipart/form-data" className="flex flex-wrap items-center gap-3" onSubmit={(e) => { const f = fd(e); const form = e.currentTarget; if (!readOnly) logo.mutate(f, { onSuccess: () => form.reset() }); }}>
                <input type="file" name="logo" accept="image/png,image/jpeg" required className="min-w-0 text-sm text-gray-700 dark:text-gray-300" />
                <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">{t('Загрузить логотип')}</button>
              </form>
              {data.logo_url && (
                <form method="POST" action="/settings/logo" className="mt-2" onSubmit={(e) => { e.preventDefault(); if (!readOnly) logoDelete.mutate(undefined); }}>
                  <button type="submit" className="text-sm text-red-600 hover:underline">{t('Удалить логотип')}</button>
                </form>
              )}
            </div>
          </div>
        </>
      ))}

      {card('signin-image', t('Картинка экрана входа'), (
        <>
          <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Показывается слева на экране входа в агенте на ПК клуба вместо стандартной. Лучше горизонтальная, от 1280×720; PNG или JPG, до 3 МБ. На ПК появится в течение минуты.')}</p>
          <ErrorBox message={firstError(errorOf('image'), 'image')} />
          <div className="flex flex-wrap items-center gap-4">
            <div className="flex h-28 w-48 items-center justify-center overflow-hidden rounded-lg bg-gray-900">
              {data.signin_image_url ? (
                <img src={data.signin_image_url} alt={t('Картинка экрана входа')} className="h-full w-full object-cover" />
              ) : (
                <span className="px-3 text-center text-xs text-gray-400">{t('Стандартная картинка Zafiro')}</span>
              )}
            </div>
            <div className="min-w-0 flex-1 basis-64">
              <form method="POST" action="/settings/signin-image" encType="multipart/form-data" className="flex flex-wrap items-center gap-3" onSubmit={(e) => { const f = fd(e); const form = e.currentTarget; if (!readOnly) signin.mutate(f, { onSuccess: () => form.reset() }); }}>
                <input type="file" name="image" accept="image/png,image/jpeg" required className="min-w-0 text-sm text-gray-700 dark:text-gray-300" />
                <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">{t('Загрузить картинку')}</button>
              </form>
              {data.signin_image_url && (
                <form method="POST" action="/settings/signin-image" className="mt-2" onSubmit={(e) => { e.preventDefault(); if (!readOnly) signinDelete.mutate(undefined); }}>
                  <button type="submit" className="text-sm text-red-600 hover:underline">{t('Вернуть стандартную')}</button>
                </form>
              )}
            </div>
          </div>
        </>
      ))}

      {card('timezone', t('Часовой пояс'), (
        <>
          <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Все время в системе, отчёты «за сегодня» и часы в верхней панели показываются в этом поясе.')}</p>
          <ErrorBox message={firstError(errorOf('timezone'), 'timezone')} />
          <form method="POST" action="/settings/timezone" className="flex flex-wrap items-end gap-3" onSubmit={(e) => { const f = fd(e); if (!readOnly) timezone.mutate(str(f, 'timezone')); }}>
            <label className="min-w-0 flex-1 basis-64">
              <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Пояс')}</span>
              <select name="timezone" className={SMALL} defaultValue={data.timezone}>
                {([['kz', t('Казахстан'), data.timezones_kazakhstan], ['all', t('Все часовые пояса'), data.timezones_all]] as const).map(([group, label, zones]) => (
                  <optgroup key={group} label={label}>
                    {zones.map((z) => (
                      <option key={`${group}-${z.id}`} value={z.id}>{z.label}</option>
                    ))}
                  </optgroup>
                ))}
              </select>
            </label>
            <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">{t('Сохранить')}</button>
          </form>
          <p className="mt-2 text-sm text-gray-600 dark:text-gray-400">{t('Сейчас в клубе: {{time}}', { time: data.club_time })}</p>
        </>
      ))}

      {card('agent-pin', t('PIN-код для выхода из агента'), (
        <>
          <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('На ПК клуба сотрудник нажимает Ctrl+Alt+P и вводит этот PIN, чтобы закрыть агент. PIN проверяется на сервере, поэтому ПК должен быть в сети.')}</p>
          <p className={`mb-3 text-sm font-medium ${data.agent_pin_set_at ? 'text-green-700 dark:text-green-400' : 'text-amber-700 dark:text-amber-400'}`}>
            {data.agent_pin_set_at ? t('PIN задан {{date}}', { date: formatDate(data.agent_pin_set_at, tz, 'd.m.Y H:i') }) : t('PIN не задан: закрыть агент на ПК сейчас нельзя.')}
          </p>
          {canPin && (
            <>
              <ErrorBox message={firstError(errorOf('pin'), 'pin')} />
              <form method="POST" action="/settings/agent-pin" className="flex flex-wrap items-end gap-3" autoComplete="off" onSubmit={(e) => { const f = fd(e); const form = e.currentTarget; if (!readOnly) pin.mutate({ p: str(f, 'pin'), c: str(f, 'pin_confirmation') }, { onSuccess: () => form.reset() }); }}>
                <label className="w-40"><span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Новый PIN')}</span>
                  <input type="password" name="pin" inputMode="numeric" pattern="\d{4,8}" maxLength={8} autoComplete="new-password" className={PIN_INPUT} /></label>
                <label className="w-40"><span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Повторите PIN')}</span>
                  <input type="password" name="pin_confirmation" inputMode="numeric" pattern="\d{4,8}" maxLength={8} autoComplete="new-password" className={PIN_INPUT} /></label>
                <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">{t('Сохранить PIN')}</button>
              </form>
              <p className="mt-2 text-xs text-gray-500">{t('Оставьте поля пустыми и сохраните, чтобы удалить PIN.')}</p>
            </>
          )}

          <h6 className="mb-2 mt-5 font-semibold text-gray-900 dark:text-white">{t('Время обслуживания ПК')}</h6>
          <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Сколько минут сотрудник может работать на ПК после выхода из агента по PIN-коду или входа на ПК. Всё это время ПК отмечен на карте клуба как «Обслуживание», затем агент снова блокирует ПК.')}</p>
          {canPin ? (
            <>
              <ErrorBox message={firstError(errorOf('minutes'), 'minutes')} />
              <form method="POST" action="/settings/agent-staff-time" className="flex flex-wrap items-end gap-3" onSubmit={(e) => { const f = fd(e); if (!readOnly) minutes.mutate(str(f, 'minutes')); }}>
                <label className="w-40"><span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Минут')}</span>
                  <input type="number" name="minutes" min="1" max="240" required defaultValue={data.agent_staff_minutes} className={SMALL} /></label>
                <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">{t('Сохранить время')}</button>
              </form>
            </>
          ) : (
            <p className="text-sm font-medium text-gray-700 dark:text-gray-300">{t('{{count}} минута|{{count}} минуты|{{count}} минут', { count: data.agent_staff_minutes })}</p>
          )}
        </>
      ))}
    </SettingsLayout>
  );
}
