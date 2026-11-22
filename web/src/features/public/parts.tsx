import { useTranslation } from 'react-i18next';

/*
 * Общие куски страниц сайта: макеты телефонов (partials/phone-guest.blade.php, phone-staff.blade.php) и карточки,
 * которые в шаблонах product/*.blade.php повторяются слово в слово. Разметка и классы — как в Blade.
 */

/** A stylised guest app phone with the balance screen. */
export function PhoneGuest() {
  const { t } = useTranslation();
  return (
    <div className="relative rounded-[2.5rem] border-[10px] border-gray-900 bg-white p-4 shadow-2xl">
      <div className="mx-auto mb-4 h-1.5 w-16 rounded-full bg-gray-200"></div>
      <p className="text-xs text-gray-500">{t('Клуб')}</p>
      <p className="font-bold">Cyber Arena</p>
      <div className="mt-4 rounded-2xl bg-gradient-to-br from-[#ff0169] to-[#b0004a] p-4 text-white">
        <p className="text-xs text-white/80">{t('Основной баланс')}</p>
        <p className="text-3xl font-extrabold">4 500 ₸</p>
        <div className="mt-3 flex justify-between text-xs"><span>{t('Бонусы')}: 350</span><span>{t('Скидка')}: 5%</span></div>
      </div>
      <div className="mt-4 flex items-center gap-3 rounded-2xl border border-gray-200 p-3">
        <span className="flex h-10 w-10 items-center justify-center rounded-xl bg-[#ff0169]/15 text-[#e6005e]"><i className="fas fa-qrcode"></i></span>
        <span className="text-sm font-semibold">{t('Войти на ПК по QR-коду')}</span>
      </div>
      <div className="mt-4 grid grid-cols-5 gap-1 border-t border-gray-100 pt-3 text-center text-[10px] text-gray-400">
        <span className="flex flex-col items-center gap-1 text-[#e6005e]"><i className="fas fa-wallet text-base"></i>{t('Баланс')}</span>
        <span className="flex flex-col items-center gap-1"><i className="fas fa-newspaper text-base"></i>{t('Новости')}</span>
        <span className="flex flex-col items-center gap-1"><i className="fas fa-user-group text-base"></i>{t('Друзья')}</span>
        <span className="flex flex-col items-center gap-1"><i className="fas fa-clock-rotate-left text-base"></i>{t('История')}</span>
        <span className="flex flex-col items-center gap-1"><i className="fas fa-user text-base"></i>{t('Профиль')}</span>
      </div>
    </div>
  );
}

type TileState = 'free' | 'busy' | 'guest' | 'staff' | 'booked';
const TILES: ReadonlyArray<TileState> = ['free', 'busy', 'busy', 'guest', 'free', 'busy', 'staff', 'busy', 'free', 'busy', 'busy', 'booked'];
const TILE_COLORS: Record<TileState, string> = {
  free: 'bg-emerald-50 text-emerald-700 ring-emerald-300',
  busy: 'bg-[#ff0169]/10 text-[#d90058] ring-[#ff0169]/40',
  guest: 'bg-sky-50 text-sky-700 ring-sky-300',
  staff: 'bg-red-50 text-red-700 ring-red-300',
  booked: 'bg-amber-50 text-amber-700 ring-amber-300',
};

/** A stylised staff app phone with the club map. */
export function PhoneStaff() {
  const { t } = useTranslation();
  return (
    <div className="relative rounded-[2.5rem] border-[10px] border-gray-900 bg-white p-4 shadow-2xl">
      <div className="mx-auto mb-4 h-1.5 w-16 rounded-full bg-gray-200"></div>
      <div className="flex items-center justify-between">
        <p className="font-bold">{t('Карта клуба')}</p>
        <span className="rounded-full bg-gray-100 px-2 py-0.5 text-[10px] text-gray-500">{t('Смена открыта')}</span>
      </div>
      <p className="mt-3 text-[10px] font-semibold uppercase tracking-wider text-gray-400">Bootcamp</p>
      <div className="mt-2 grid grid-cols-4 gap-2">
        {TILES.map((state, i) => (
          <span key={i} className={`flex h-11 items-center justify-center rounded-xl text-sm font-bold ring-1 ${TILE_COLORS[state]}`}>{i + 1}</span>
        ))}
      </div>
      <div className="mt-4 grid grid-cols-3 gap-2 text-center text-[10px] font-semibold">
        <span className="flex flex-col items-center gap-1 rounded-xl bg-gray-100 py-2"><i className="fas fa-rotate-right text-sm text-gray-600"></i>{t('Перезагрузить')}</span>
        <span className="flex flex-col items-center gap-1 rounded-xl bg-gray-100 py-2"><i className="fas fa-power-off text-sm text-gray-600"></i>{t('Выключить')}</span>
        <span className="flex flex-col items-center gap-1 rounded-xl bg-[#ff0169] py-2 text-white"><i className="fas fa-stopwatch text-sm"></i>{t('Время гостю')}</span>
      </div>
    </div>
  );
}

/** Карточка со значком и списком: [icon, title, points]. */
export type PointsCard = readonly [string, string, ReadonlyArray<string>];
/** Карточка со значком и текстом: [icon, title, text]. */
export type TextCard = readonly [string, string, string];

export function PointsCardItem({ card }: { card: PointsCard }) {
  const [icon, title, points] = card;
  return (
    <article className="rounded-2xl border border-gray-200 bg-gray-50 p-6">
      <span className="mb-5 flex h-12 w-12 items-center justify-center rounded-xl bg-[#ff0169]/15 text-xl text-[#e6005e]"><i className={`fas ${icon}`} aria-hidden="true"></i></span>
      <h3 className="text-lg font-semibold">{title}</h3>
      <ul className="mt-3 space-y-2 text-sm leading-relaxed text-gray-600">
        {points.map((point) => (
          <li key={point} className="flex gap-2"><i className="fas fa-check mt-1 text-xs text-[#e6005e]" aria-hidden="true"></i><span>{point}</span></li>
        ))}
      </ul>
    </article>
  );
}

export function TextCardItem({ card }: { card: TextCard }) {
  const [icon, title, text] = card;
  return (
    <article className="flex gap-5 rounded-2xl border border-gray-200 bg-gray-50 p-6">
      <span className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl bg-[#ff0169]/15 text-xl text-[#e6005e]"><i className={`fas ${icon}`} aria-hidden="true"></i></span>
      <div><h3 className="text-lg font-semibold">{title}</h3><p className="mt-2 text-sm leading-relaxed text-gray-600">{text}</p></div>
    </article>
  );
}

/**
 * Снимок агента на игровом месте с остатком времени (welcome.blade.php и product/agent.blade.php).
 * В Blade — public/images/landing/club.jpg 1600×1226 (112 КБ) в блок шириной до 528 px; здесь — тот же снимок в webp трёх
 * ширин по srcset (13/26/53 КБ; аудит 22.11.2026, UI-5). Без lazy (герой /product/agent) — картинка LCP: высокий
 * приоритет. <link rel="preload"> в <head> проверен и не взят: LCP ждёт отрисовки SPA, а preload отнимает канал у
 * main.js — Lighthouse mobile 3,7–3,8 с без него против 3,8–4,0 с ним.
 */
export function AgentPhoto({ lazy }: { lazy: boolean }) {
  const { t } = useTranslation();
  return (
    <div className="relative overflow-hidden rounded-3xl border border-gray-200">
      <img src="/images/landing/club-1024.webp" srcSet="/images/landing/club-640.webp 640w, /images/landing/club-1024.webp 1024w, /images/landing/club-1600.webp 1600w"
        sizes="(min-width: 1024px) 528px, 100vw" width={1600} height={1226}
        alt={t('Игровое место в компьютерном клубе')} className="h-full max-h-[460px] w-full object-cover" loading={lazy ? 'lazy' : undefined} fetchPriority={lazy ? undefined : 'high'} />
      <div className="absolute inset-0 bg-gradient-to-t from-black/60 via-transparent to-transparent"></div>
      <div className="absolute bottom-5 left-5 right-5 rounded-2xl border border-white/10 bg-black/50 p-4 text-white backdrop-blur">
        <p className="text-sm text-white/60">{t('Осталось времени')}</p>
        <p className="text-3xl font-bold">{t('{{hours}} ч {{minutes}} мин', { hours: 1, minutes: 27 })}</p>
      </div>
    </div>
  );
}
