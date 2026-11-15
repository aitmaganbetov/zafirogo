import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { SettingsCard } from '../../components/SettingsCard';
import { SettingsLayout } from '../../components/SettingsLayout';
import { formatMoney } from '../../lib/money';
import {
  clubProfileQuery, coverPhoto, deletePhoto, firstError, saveProfile, saveZoneProfile, uploadImage, type ZoneProfile,
} from './api';
import { SettingsAnchors } from './SettingsAnchors';
import { useSettingsResult } from './status';

/*
 * Настройки → Профиль клуба — перенос resources/views/settings/profile.blade.php (ClubProfileController): что гости
 * видят о клубе в мобильном приложении — описание и контакты, фото (первое — обложка), зоны с оборудованием
 * (подсказки-placeholder — железо, которое сообщают агенты на ПК). Каждая форма сохраняется отдельно.
 */

const INPUT = 'w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100';
const LABEL = 'mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300';

function ErrorBox({ message }: { message?: string }) {
  if (!message) return null;
  return (
    <div className="mb-3 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700" role="alert">
      {message}
    </div>
  );
}

/** Первая ошибка фото: $errors->first('photos') ?: первая из photos.*. */
function photosError(e: unknown): string | undefined {
  const fields = ['photos', ...Array.from({ length: 10 }, (_, i) => `photos.${i}`)];
  return firstError(e, ...fields) ?? (e ? firstError(e) : undefined);
}

export function ProfilePage() {
  const { t } = useTranslation();
  const { me } = useSession();
  const qc = useQueryClient();
  const { data } = useQuery(clubProfileQuery());
  const { status, done, fail, errorOf } = useSettingsResult();
  const readOnly = me?.read_only === true;

  const refresh = async (message: string) => {
    done(message);
    await qc.invalidateQueries({ queryKey: ['settings', 'profile'] });
  };
  const about = useMutation({ mutationFn: saveProfile, onSuccess: (r) => refresh(r.message), onError: fail('about') });
  const photos = useMutation({
    mutationFn: (f: FormData) => uploadImage('/api/settings/profile/photos', f),
    onSuccess: (r) => refresh(r.message),
    onError: fail('photos'),
  });
  const cover = useMutation({ mutationFn: coverPhoto, onSuccess: (r) => refresh(r.message), onError: fail('photos') });
  const remove = useMutation({ mutationFn: deletePhoto, onSuccess: (r) => refresh(r.message), onError: fail('photos') });
  const zone = useMutation({
    mutationFn: ({ id, body }: { id: number; body: Parameters<typeof saveZoneProfile>[1] }) => saveZoneProfile(id, body),
    // redirect()->to(route('settings.profile').'#zone-'.$zone->id): после сохранения — к форме зоны.
    onSuccess: async (r, { id }) => {
      await refresh(r.message);
      document.getElementById(`zone-${id}`)?.scrollIntoView?.();
    },
    onError: (e, { id }) => fail(`zone-${id}`)(e),
  });

  if (!data) return null;
  const profile = data.profile;
  const str = (f: FormData, k: string) => String(f.get(k) ?? '');
  const fd = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    return new FormData(e.currentTarget);
  };
  const anchors: ReadonlyArray<readonly [string, string]> = [
    ['club-about', t('О клубе')],
    ['club-photos', t('Фотографии')],
    ['club-zones', t('Зоны и оборудование')],
  ];
  const zoneError = (z: ZoneProfile) => {
    const e = errorOf(`zone-${z.id}`);
    return e ? firstError(e, 'description', ...data.device_fields.map((f) => `devices.${f.key}`)) ?? firstError(e) : undefined;
  };

  return (
    <SettingsLayout status={status} anchors={<SettingsAnchors anchors={anchors} />}>
      <SettingsCard id="club-about" aria-labelledby="club-about-title">
        <h5 id="club-about-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('О клубе')}</h5>
        <p className="mb-4 text-sm text-gray-600 dark:text-gray-400">{t('Гости видят это на странице клуба в мобильном приложении. Город и адрес — в «Настройка клуба».')}</p>
        <ErrorBox message={firstError(errorOf('about'), 'description', 'phone', 'hours', 'instagram')} />
        <form method="POST" action="/settings/profile" className="grid gap-4 md:grid-cols-3" onSubmit={(e) => { const f = fd(e); if (!readOnly) about.mutate({ description: str(f, 'description'), phone: str(f, 'phone'), hours: str(f, 'hours'), instagram: str(f, 'instagram') }); }}>
          <label className="md:col-span-3">
            <span className={LABEL}>{t('Описание')}</span>
            <textarea name="description" rows={4} maxLength={2000} className={INPUT} placeholder={t('Чем ваш клуб хорош: зоны, турниры, кухня, парковка…')} defaultValue={profile.description ?? ''}></textarea>
          </label>
          <label>
            <span className={LABEL}>{t('Телефон')}</span>
            <input type="tel" name="phone" maxLength={32} defaultValue={profile.phone ?? ''} placeholder="+7 700 000 00 00" className={INPUT} />
          </label>
          <label>
            <span className={LABEL}>{t('Часы работы')}</span>
            <input type="text" name="hours" maxLength={100} defaultValue={profile.hours ?? ''} placeholder={t('Круглосуточно')} className={INPUT} />
          </label>
          <label>
            <span className={LABEL}>Instagram</span>
            <input type="text" name="instagram" maxLength={64} defaultValue={profile.instagram ? `@${profile.instagram}` : ''} placeholder="@club" className={INPUT} />
          </label>
          <div className="md:col-span-3 flex justify-end">
            <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">{t('Сохранить')}</button>
          </div>
        </form>
      </SettingsCard>

      <SettingsCard id="club-photos" aria-labelledby="club-photos-title">
        <h5 id="club-photos-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Фотографии')}</h5>
        <p className="mb-4 text-sm text-gray-600 dark:text-gray-400">{t('До {{count}} фото зала, зон и входа: JPG, PNG или WebP, до 3 МБ. Первое фото — обложка клуба в списке.', { count: data.photo_limit })}</p>
        <ErrorBox message={photosError(errorOf('photos'))} />
        {data.photos.length > 0 && (
          <ul className="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
            {data.photos.map((photo, i) => (
              <li key={photo.id} className="overflow-hidden rounded-xl border border-gray-200 dark:border-gray-700">
                <img src={photo.url} alt={t('Фото клуба')} className="aspect-[4/3] w-full object-cover" loading="lazy" />
                <div className="flex items-center justify-between gap-2 px-3 py-2 text-sm">
                  {i === 0 ? (
                    <span className="font-medium text-[#d90058]">{t('Обложка')}</span>
                  ) : (
                    <form method="POST" action={`/settings/profile/photos/${photo.id}/cover`} onSubmit={(e) => { e.preventDefault(); if (!readOnly) cover.mutate(photo.id); }}>
                      <button type="submit" className="text-gray-600 hover:underline dark:text-gray-300">{t('Сделать обложкой')}</button>
                    </form>
                  )}
                  <form method="POST" action={`/settings/profile/photos/${photo.id}`} onSubmit={(e) => { e.preventDefault(); if (!readOnly && window.confirm(t('Удалить фото?'))) remove.mutate(photo.id); }}>
                    <button type="submit" className="text-red-600 hover:underline">{t('Удалить')}</button>
                  </form>
                </div>
              </li>
            ))}
          </ul>
        )}
        {data.photos.length < data.photo_limit && (
          <form method="POST" action="/settings/profile/photos" encType="multipart/form-data" className="flex flex-wrap items-center gap-3" onSubmit={(e) => { const f = fd(e); const form = e.currentTarget; if (!readOnly) photos.mutate(f, { onSuccess: () => form.reset() }); }}>
            <input type="file" name="photos[]" accept="image/jpeg,image/png,image/webp" multiple required className="min-w-0 text-sm text-gray-700 dark:text-gray-300" />
            <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">{t('Добавить фото')}</button>
          </form>
        )}
      </SettingsCard>

      <SettingsCard id="club-zones" aria-labelledby="club-zones-title">
        <h5 id="club-zones-title" className="mb-2 font-semibold text-gray-900 dark:text-white">{t('Зоны и оборудование')}</h5>
        <p className="mb-4 text-sm text-gray-600 dark:text-gray-400">
          {t('Гости видят зоны с ценой за час, числом ПК и оборудованием. Процессор, видеокарту и память можно не заполнять — их сообщает агент на ПК (подсказка в поле). Зоны и цены меняются на карте клуба.')}
          {' '}
          <a href="/club-map" className="font-medium text-[#d90058] hover:underline">{t('Карта клуба')}</a>
        </p>
        {data.zones.length === 0 && <p className="text-sm text-gray-500">{t('Зон пока нет: создайте их на карте клуба.')}</p>}
        {data.zones.map((z) => (
          <form
            key={z.id}
            id={`zone-${z.id}`}
            method="POST"
            action={`/settings/profile/zones/${z.id}`}
            className="mb-4 scroll-mt-6 rounded-xl border border-gray-200 p-4 last:mb-0 dark:border-gray-700"
            onSubmit={(e) => {
              const f = fd(e);
              if (readOnly) return;
              const devices: Record<string, string> = {};
              for (const field of data.device_fields) devices[field.key] = str(f, `devices[${field.key}]`);
              zone.mutate({ id: z.id, body: { description: str(f, 'description'), devices } });
            }}
          >
            <ErrorBox message={zoneError(z)} />
            <div className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
              <h6 className="font-semibold text-gray-900 dark:text-white">{z.name}</h6>
              <span className="text-sm text-gray-500">
                {t('{{count}} ПК|{{count}} ПК|{{count}} ПК', { count: z.computers_count })} · {t('{{price}} ₸/час', { price: formatMoney(z.price_per_hour ?? 0, 0, '.', ' ') })}
              </span>
            </div>
            <label className="mb-3 block">
              <span className={LABEL}>{t('Описание зоны')}</span>
              <input type="text" name="description" maxLength={500} defaultValue={z.description ?? ''} placeholder={t('Например: отдельная комната на 5 ПК')} className={INPUT} />
            </label>
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
              {data.device_fields.map((field) => (
                <label key={field.key}>
                  <span className={LABEL}>{field.label}</span>
                  <input type="text" name={`devices[${field.key}]`} maxLength={120} defaultValue={z.devices[field.key] ?? ''} placeholder={z.detected[field.key] ?? ''} className={INPUT} />
                </label>
              ))}
            </div>
            <div className="mt-3 flex justify-end">
              <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">{t('Сохранить зону')}</button>
            </div>
          </form>
        ))}
      </SettingsCard>
    </SettingsLayout>
  );
}
