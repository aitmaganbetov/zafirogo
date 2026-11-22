import { queryOptions, useMutation, useQuery } from '@tanstack/react-query';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { unwrap } from '../../api/http';
import { SettingsCard } from '../../components/SettingsCard';
import { firstError, settingsApi } from './api';

/*
 * Карточка «Реселлер» страницы «Настройка клуба» — перенос блока #reseller из resources/views/settings/club.blade.php
 * (тема Д, docs/upstream-2026-11.md; SettingsController::reseller()): клуб один раз вводит код реселлера Zafiro —
 * подписка становится дешевле на его скидку. Привязанный клуб видит реселлера; сменить его может только суперадмин.
 * Данные карточки — свой запрос (GET /api/settings/reseller), ключ ['settings', 'reseller'] перечитывается вместе с
 * остальными настройками страницы.
 */

const clubResellerQuery = () =>
  queryOptions({
    queryKey: ['settings', 'reseller'],
    queryFn: ({ signal }) => unwrap(settingsApi.GET('/api/settings/reseller', { signal })),
  });

const attachReseller = (code: string) => unwrap(settingsApi.POST('/api/settings/reseller', { body: { reseller_code: code } }));

export interface ResellerCardProps {
  /** Режим просмотра клуба суперадмином: форма не отправляется. */
  readonly readOnly: boolean;
  /** Успех: текст status страницы (back()->with('status')) — страница перечитывает настройки. */
  readonly onDone: (message: string) => unknown;
  /** Ошибка формы карточки (страница сбрасывает status и запоминает ошибку) и её чтение — как у остальных карточек. */
  readonly onError: (e: unknown) => void;
  readonly error: unknown;
}

export function ResellerCard({ readOnly, onDone, onError, error }: ResellerCardProps) {
  const { t } = useTranslation();
  const { data, error: loadError } = useQuery(clubResellerQuery());
  const attach = useMutation({ mutationFn: attachReseller, onSuccess: (r) => onDone(r.message), onError });
  // Данные не загрузились: карточка остаётся на месте с ошибкой (а не исчезает молча); формы кода нет — неизвестно,
  // привязан ли клуб.
  if (!data && loadError) {
    return (
      <SettingsCard id="reseller" aria-labelledby="reseller-title">
        <h5 id="reseller-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Реселлер')}</h5>
        <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700" role="alert">
          {firstError(loadError) || t('Нет связи с сервером. Повторите попытку.')}
        </div>
      </SettingsCard>
    );
  }
  // Пока ответа нет, карточки нет: иначе привязанный клуб на миг увидел бы форму кода.
  if (!data) return null;
  const reseller = data.reseller;
  const message = firstError(error, 'reseller_code');

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (readOnly) return;
    attach.mutate(String(new FormData(e.currentTarget).get('reseller_code') ?? ''));
  };

  return (
    <SettingsCard id="reseller" aria-labelledby="reseller-title">
      <h5 id="reseller-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Реселлер')}</h5>
      {reseller ? (
        <>
          <p className="text-sm text-gray-700 dark:text-gray-300">
            {t('Клуб подключён через реселлера {{name}} (код {{code}}).', { name: reseller.name, code: reseller.code })}
            {reseller.discount_percent > 0 && <> {t('Скидка на подписку: {{percent}}%.', { percent: reseller.discount_percent })}</>}
          </p>
          <p className="mt-1 text-xs text-gray-500">{t('Сменить реселлера можно через поддержку Zafiro.')}</p>
        </>
      ) : (
        <>
          <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Если вас подключил реселлер Zafiro, введите его код: подписка станет дешевле на его скидку. Код вводится один раз.')}</p>
          {message && (
            <div className="mb-3 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700" role="alert">
              {message}
            </div>
          )}
          <form method="POST" action="/settings/reseller" className="flex flex-wrap items-end gap-3" onSubmit={submit}>
            <label className="min-w-0 flex-1 basis-48">
              <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Код реселлера')}</span>
              <input type="text" name="reseller_code" required maxLength={32} autoCapitalize="none" spellCheck={false} className="w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100" />
            </label>
            <button type="submit" disabled={attach.isPending} className="brand-button rounded-lg px-4 py-2 text-sm">{t('Привязать')}</button>
          </form>
        </>
      )}
    </SettingsCard>
  );
}
