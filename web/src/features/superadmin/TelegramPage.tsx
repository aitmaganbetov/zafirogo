import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { clearAlerts, clearGateway, firstError, setAlerts, setGateway, telegramQuery, testAlerts } from './api';
import { useSuperadminFlash } from './flash';

/*
 * Telegram — перенос resources/views/superadmin/telegram.blade.php (TelegramController). Токены в форму не
 * возвращаются. Сообщение и ошибка показываются и в layout, и на странице — как в старом шаблоне.
 */

export function TelegramPage() {
  const { t } = useTranslation();
  useAdminPageTitle(t('Telegram'));
  const flash = useSuperadminFlash();
  const queryClient = useQueryClient();
  const { data } = useQuery(telegramQuery());
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [chatId, setChatId] = useState<string | null>(null);

  const handlers = {
    onSuccess: (r: { message: string }) => {
      setError(null);
      setStatus(r.message);
      flash.status(r.message);
      return queryClient.invalidateQueries({ queryKey: ['superadmin', 'telegram'] });
    },
    onError: (e: unknown) => {
      setStatus(null);
      setError(firstError(e));
      flash.error(firstError(e));
    },
  };
  const gateway = useMutation({ mutationFn: (token: string) => setGateway(token), ...handlers });
  const gatewayOff = useMutation({ mutationFn: () => clearGateway(), ...handlers });
  const alerts = useMutation({ mutationFn: ({ token, chat }: { token: string; chat: string }) => setAlerts(token, chat), ...handlers });
  const test = useMutation({ mutationFn: () => testAlerts(), ...handlers });
  const alertsOff = useMutation({ mutationFn: () => clearAlerts(), ...handlers });
  if (!data) return null;
  const alertsOn = data.alerts_chat_id != null;

  const submitGateway = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    gateway.mutate(String(new FormData(form).get('token') ?? ''), { onSuccess: () => form.reset() });
  };
  const submitAlerts = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    const f = new FormData(form);
    alerts.mutate({ token: String(f.get('bot_token') ?? ''), chat: String(f.get('chat_id') ?? '') });
  };
  const confirmThen = (question: string, run: () => void) => (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (window.confirm(question)) run();
  };

  return (
    <>
      <p className="mb-4 text-sm text-gray-600 dark:text-gray-400">
        {t(
          'Через Telegram Gateway клиенты и сотрудники сами восстанавливают пароль: «Забыли пароль?» на экране входа в агенте и на сайте, код придёт в Telegram на номер аккаунта. Каждый код платный — пополните баланс на gateway.telegram.org.',
        )}
      </p>
      {status && <div className="mb-4 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700">{status}</div>}
      {error && (
        <div className="mb-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700" role="alert">
          {error}
        </div>
      )}

      <div className="rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-900">
        <h3 className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Восстановление пароля (Telegram Gateway)')}</h3>
        {data.configured ? (
          <p className="mb-3 text-sm text-gray-700 dark:text-gray-300">
            {t('Telegram Gateway подключён')} · {t('{{count}} код за 30 дней|{{count}} кода за 30 дней|{{count}} кодов за 30 дней', { count: data.sent_last_month })}
          </p>
        ) : (
          <p className="mb-3 text-sm text-amber-700">{t('Telegram Gateway не подключён: восстановить пароль можно только через сотрудника клуба.')}</p>
        )}
        <form method="POST" className="flex flex-wrap items-end gap-3" autoComplete="off" onSubmit={submitGateway}>
          <label className="min-w-0 flex-1 basis-80">
            <span className="mb-1 block text-xs font-medium text-gray-600">{t('Токен API из настроек аккаунта gateway.telegram.org')}</span>
            <input
              type="password"
              name="token"
              required
              maxLength={255}
              autoComplete="off"
              className="w-full rounded-lg border border-gray-300 px-3 py-2 font-mono text-sm dark:border-gray-700 dark:bg-gray-800 dark:text-white"
            />
          </label>
          <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">
            {data.configured ? t('Заменить токен') : t('Подключить')}
          </button>
        </form>
        {data.configured && (
          <form
            method="POST"
            className="mt-3"
            onSubmit={confirmThen(t('Отключить Telegram Gateway? Восстановить пароль самостоятельно будет нельзя.'), () => gatewayOff.mutate())}
          >
            <button type="submit" className="text-sm text-red-600 hover:underline">
              {t('Отключить')}
            </button>
          </form>
        )}
      </div>

      {/* Оповещения о сбоях: Gateway шлёт только коды подтверждения, поэтому для сообщений нужен свой бот. */}
      <section className="mt-6 rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-900" aria-labelledby="alerts-title">
        <h3 id="alerts-title" className="mb-2 font-semibold text-gray-900 dark:text-white">
          {t('Оповещения о сбоях')}
        </h3>
        <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">
          {t(
            'Бот пишет в чат, когда планировщик молчит больше 5 минут, диск заполнен больше чем на 85%, растёт доля ошибок 5xx, копится очередь или клуб целиком ушёл офлайн, и ещё раз — когда всё наладилось.',
          )}
        </p>
        {alertsOn ? (
          <p className="mb-3 text-sm text-gray-700 dark:text-gray-300">{t('Оповещения включены, чат {{chat}}.', { chat: data.alerts_chat_id })}</p>
        ) : (
          <ol className="mb-3 list-decimal space-y-1 pl-5 text-sm text-gray-600 dark:text-gray-400">
            <li>{t('Создайте бота у @BotFather и скопируйте его токен.')}</li>
            <li>{t('Отправьте боту /start или добавьте его в группу, куда должны приходить оповещения.')}</li>
            <li>{t('Вставьте токен ниже. Чат можно не указывать: возьмём последний, который писал боту.')}</li>
          </ol>
        )}
        <form method="POST" className="flex flex-wrap items-end gap-3" autoComplete="off" onSubmit={submitAlerts}>
          <label className="min-w-0 flex-1 basis-80">
            <span className="mb-1 block text-xs font-medium text-gray-600">{t('Токен бота от @BotFather')}</span>
            <input
              type="password"
              name="bot_token"
              required
              maxLength={255}
              autoComplete="off"
              className="w-full rounded-lg border border-gray-300 px-3 py-2 font-mono text-sm dark:border-gray-700 dark:bg-gray-800 dark:text-white"
            />
          </label>
          <label className="basis-48">
            <span className="mb-1 block text-xs font-medium text-gray-600">{t('ID чата (необязательно)')}</span>
            <input
              name="chat_id"
              maxLength={64}
              value={chatId ?? data.alerts_chat_id ?? ''}
              onChange={(e) => setChatId(e.currentTarget.value)}
              className="w-full rounded-lg border border-gray-300 px-3 py-2 font-mono text-sm dark:border-gray-700 dark:bg-gray-800 dark:text-white"
            />
          </label>
          <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">
            {alertsOn ? t('Сохранить бота') : t('Подключить бота')}
          </button>
        </form>
        {alertsOn && (
          <div className="mt-3 flex flex-wrap gap-4">
            <form
              method="POST"
              onSubmit={(e) => {
                e.preventDefault();
                test.mutate();
              }}
            >
              <button type="submit" className="text-sm text-violet-700 hover:underline">
                {t('Отправить проверочное сообщение')}
              </button>
            </form>
            <form method="POST" onSubmit={confirmThen(t('Отключить оповещения о сбоях?'), () => alertsOff.mutate())}>
              <button type="submit" className="text-sm text-red-600 hover:underline">
                {t('Отключить оповещения')}
              </button>
            </form>
          </div>
        )}
      </section>
    </>
  );
}
