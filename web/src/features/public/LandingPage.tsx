import { useSuspenseQuery } from '@tanstack/react-query';
import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { landingQuery } from './api';

/*
 * Лендинг — перенос resources/views/welcome.blade.php (LandingController). Разметка и классы — как в шаблоне;
 * @auth — вошедший пользователь (сессия SPA). Данные — GET /api/landing (пробный период, ссылка на установщик).
 * route('wiki') — /wiki (с отдельным хостом wiki сервер отвечает 301 на него, решение Q-069).
 */

const BODY_CLASSES = ['min-h-screen', 'bg-[#0b0d10]', 'text-white', 'antialiased'];
const BODY_FONT = "Inter, 'Segoe UI', system-ui, sans-serif";

type HallState = 'free' | 'busy' | 'booked';
const HALL: ReadonlyArray<readonly [string, ...HallState[]]> = [
  ['VIP', 'free', 'busy', 'busy', 'free', 'busy', 'booked'],
  ['Bootcamp', 'busy', 'busy', 'free', 'busy', 'busy', 'busy', 'free', 'busy'],
  ['PS5', 'busy', 'free'],
];
/** Номер первого ПК зоны (++$pcNumber шаблона идёт сквозь зоны). */
const HALL_FIRST = HALL.map((_, i) => HALL.slice(0, i).reduce((n, z) => n + z.length - 1, 0) + 1);
const HALL_CLASSES: Record<HallState, string> = {
  free: 'bg-emerald-500/15 text-emerald-300 ring-1 ring-emerald-400/40',
  busy: 'bg-[#ff0169]/20 text-[#ff7aa8] ring-1 ring-[#ff0169]/50',
  booked: 'bg-amber-400/15 text-amber-300 ring-1 ring-amber-300/40',
};

function useMeta(name: string, content: string, attr: 'name' | 'property' = 'name') {
  useEffect(() => {
    const el = document.createElement('meta');
    el.setAttribute(attr, name);
    el.content = content;
    document.head.appendChild(el);
    return () => el.remove();
  }, [attr, name, content]);
}

export function LandingPage() {
  const { t } = useTranslation();
  const { status } = useSession();
  const { data } = useSuspenseQuery(landingQuery());
  const authed = status === 'authenticated';
  const days = data.trial_days;

  const title = t('Zafiro — система управления компьютерным клубом');
  const description = t('Касса, карта зала, брони, тарифы, магазин и агент на ПК в одной системе. Подключите свой компьютерный клуб за один вечер.');
  useEffect(() => {
    document.title = title; // <title> шаблона — без « · Zafiro»
  }, [title]);
  useMeta('description', description);
  useMeta('og:title', title, 'property');
  useMeta('og:description', description, 'property');
  useMeta('og:image', `${location.origin}/icons/icon-512.png`, 'property');
  useMeta('og:type', 'website', 'property');

  useEffect(() => {
    const body = document.body;
    const html = document.documentElement;
    body.classList.add(...BODY_CLASSES);
    body.style.fontFamily = BODY_FONT;
    html.classList.add('scroll-smooth');
    return () => {
      body.classList.remove(...BODY_CLASSES);
      body.style.fontFamily = '';
      html.classList.remove('scroll-smooth');
    };
  }, []);

  const features: ReadonlyArray<readonly [string, string, string]> = [
    ['fa-map-location-dot', t('Карта зала'), t('Зоны и ПК на схеме клуба: кто играет, сколько осталось, что забронировано и где идёт обслуживание.')],
    ['fa-cash-register', t('Касса и смены'), t('Продажи, пополнения балансов, X- и Z-отчёты с разбивкой по товарам и сотрудникам, печать на 80-мм принтер.')],
    ['fa-desktop', t('Агент на ПК'), t('Блокирует ПК до оплаты, показывает время и баланс, сам выключает или перезагружает ПК после сессии.')],
    ['fa-calendar-check', t('Брони'), t('Бронирование ПК на время для клиента: с карты зала или из списка, без накладок.')],
    ['fa-stopwatch', t('Тарифы и поминутно'), t('Пакеты часов и оплата по минутам по цене часа зоны, бонусный и основной счёт.')],
    ['fa-bag-shopping', t('Магазин в агенте'), t('Клиент заказывает напитки и еду с ПК и платит с баланса, кассир видит заказ и несёт его к месту.')],
    ['fa-user-group', t('Гости и друзья'), t('Вход по QR-коду, регистрация прямо на ПК, друзья, переводы и пересадка на свободный ПК.')],
    ['fa-hard-drive', t('Бездисковые клубы'), t('Один раз настроили суперклиент — остальные ПК подключаются сами после загрузки образа.')],
    ['fa-plug', t('API и вебхуки'), t('Интеграция с вашими сервисами: ключи с правами, события продаж и склада.')],
  ];
  const steps: ReadonlyArray<readonly [string, string]> = [
    [t('Зарегистрируйте клуб'), t('{{days}} дней бесплатно, без карты и договора.', { days })],
    [t('Нарисуйте зал'), t('Создайте зоны, расставьте ПК на карте и задайте тарифы.')],
    [t('Установите агент'), t('Запустите установщик на ПК и введите ключ клуба — ПК появится на карте.')],
  ];
  const faq: ReadonlyArray<readonly [string, string]> = [
    [t('Нужен ли свой сервер?'), t('Нет. Zafiro работает в облаке: панель открывается в браузере, а на ПК клуба ставится только агент.')],
    [t('Что если пропадёт интернет?'), t('Оплаченная сессия продолжается, а агент сам переподключится к серверу, когда связь вернётся.')],
    [t('Подходит для бездисковой системы?'), t('Да. Агент ставится в образ суперклиента, и каждый ПК после загрузки сам получает свой номер.')],
    [t('Клиент может обойти блокировку?'), t('Агент работает со службой Windows, закрывается только по PIN-коду сотрудника и сам запускается снова.')],
    [t('Сколько это стоит?'), t('Первые {{days}} дней бесплатно. Стоимость подписки зависит от количества ПК — напишите нам.', { days })],
  ];
  const points = [
    t('Вход по телефону, QR-коду или регистрация прямо на ПК'),
    t('Тарифы, оплата поминутно и магазин клуба с оплатой с баланса'),
    t('Блокировка Windows до оплаты и выход сотрудника только по PIN-коду'),
    t('Обновление установщиком в одну кнопку «Обновить»'),
  ];
  const hallStates = HALL.flatMap((zone) => zone.slice(1) as HallState[]);

  return (
    <>
      <div className="pointer-events-none fixed inset-0 overflow-hidden" aria-hidden="true">
        <div className="absolute -top-40 left-1/2 h-[560px] w-[900px] -translate-x-1/2 rounded-full bg-[#ff0169]/20 blur-[140px]"></div>
        <div className="absolute bottom-0 right-[-200px] h-[420px] w-[520px] rounded-full bg-[#7c3aed]/10 blur-[140px]"></div>
      </div>

      <header className="sticky top-0 z-30 border-b border-white/5 bg-[#0b0d10]/80 backdrop-blur">
        <nav className="mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-3 sm:px-6">
          <a href="/" className="flex items-center gap-2 font-bold tracking-wide">
            <img src="/images/logo.svg" alt="" className="h-9 w-auto" />
            <span className="text-lg">ZAFIRO</span>
          </a>
          <div className="hidden items-center gap-6 text-sm text-white/70 md:flex">
            <a href="#features" className="hover:text-white">{t('Возможности')}</a>
            <a href="#agent" className="hover:text-white">{t('Агент')}</a>
            <a href="#start" className="hover:text-white">{t('Как начать')}</a>
            <a href="#faq" className="hover:text-white">{t('Вопросы')}</a>
            <a href="/wiki" data-reload className="hover:text-white">{t('Wiki')}</a>
          </div>
          <div className="flex items-center gap-2">
            <a href="/wiki" data-reload className="rounded-lg px-3 py-2 text-sm text-white/80 hover:text-white md:hidden">{t('Wiki')}</a>
            {authed ? (
              <a href="/dashboard" className="rounded-lg bg-[#ff0169] px-4 py-2 text-sm font-semibold shadow-lg shadow-[#ff0169]/30 hover:bg-[#e6005e]">{t('Открыть панель')}</a>
            ) : (
              <>
                <a href="/login" className="rounded-lg px-3 py-2 text-sm text-white/80 hover:text-white">{t('Войти')}</a>
                <a href="/register" className="rounded-lg bg-[#ff0169] px-4 py-2 text-sm font-semibold shadow-lg shadow-[#ff0169]/30 hover:bg-[#e6005e]">{t('Попробовать')}</a>
              </>
            )}
          </div>
        </nav>
      </header>

      <main className="relative">
        {/* Hero */}
        <section className="mx-auto grid max-w-6xl items-center gap-12 px-4 pb-20 pt-14 sm:px-6 lg:grid-cols-2 lg:pt-20">
          <div>
            <p className="mb-5 inline-flex items-center gap-2 rounded-full border border-[#ff0169]/40 bg-[#ff0169]/10 px-3 py-1 text-sm text-[#ff7aa8]"><i className="fas fa-bolt" aria-hidden="true"></i>{t('Для компьютерных клубов и PS-клубов')}</p>
            <h1 className="bg-gradient-to-r from-white via-white to-[#ff7aa8] bg-clip-text text-4xl font-extrabold leading-tight text-transparent sm:text-5xl lg:text-6xl">{t('Весь клуб — в одном окне')}</h1>
            <p className="mt-6 max-w-xl text-lg text-white/70">{description}</p>
            <div className="mt-8 flex flex-wrap gap-3">
              {authed ? (
                <a href="/dashboard" className="rounded-xl bg-[#ff0169] px-6 py-3 font-semibold shadow-xl shadow-[#ff0169]/30 hover:bg-[#e6005e]">{t('Открыть панель')}</a>
              ) : (
                <a href="/register" className="rounded-xl bg-[#ff0169] px-6 py-3 font-semibold shadow-xl shadow-[#ff0169]/30 hover:bg-[#e6005e]">{t('{{days}} дней бесплатно', { days })}</a>
              )}
              <a href="#features" className="rounded-xl border border-white/15 px-6 py-3 font-semibold text-white/90 hover:border-white/40">{t('Посмотреть возможности')}</a>
            </div>
            <dl className="mt-10 grid max-w-lg grid-cols-3 gap-4 text-center sm:text-left">
              <div><dt className="text-sm text-white/50">{t('Запуск')}</dt><dd className="text-xl font-bold">{t('1 вечер')}</dd></div>
              <div><dt className="text-sm text-white/50">{t('Оборудование')}</dt><dd className="text-xl font-bold">{t('не нужно')}</dd></div>
              <div><dt className="text-sm text-white/50">{t('Языки')}</dt><dd className="text-xl font-bold">RU · KZ · EN</dd></div>
            </dl>
          </div>

          {/* A stylised club map, as staff see it in the panel. */}
          <div className="relative" aria-hidden="true">
            <div className="absolute -inset-4 rounded-[2rem] bg-gradient-to-br from-[#ff0169]/30 to-transparent blur-2xl"></div>
            <div className="relative rounded-3xl border border-white/10 bg-[#11151a]/90 p-5 shadow-2xl">
              <div className="mb-4 flex items-center justify-between">
                <div className="flex items-center gap-2"><span className="h-3 w-3 rounded-full bg-[#ff0169]"></span><span className="text-sm font-semibold">{t('Карта клуба')}</span></div>
                <span className="rounded-full bg-white/5 px-3 py-1 text-xs text-white/60">{t('Сейчас занято: {{busy}} из {{total}}', { busy: hallStates.filter((s) => s === 'busy').length, total: hallStates.length })}</span>
              </div>
              <div className="grid gap-3">
                {HALL.map((zone, zi) => (
                  <div key={zone[0]} className="rounded-2xl border border-white/10 bg-white/[0.03] p-3">
                    <p className="mb-2 text-xs font-semibold uppercase tracking-wider text-white/50">{zone[0]}</p>
                    <div className="flex flex-wrap gap-2">
                      {(zone.slice(1) as HallState[]).map((state, i) => (
                        <span key={i} className={`flex h-11 w-11 items-center justify-center rounded-xl text-sm font-bold ${HALL_CLASSES[state]}`}>{(HALL_FIRST[zi] ?? 1) + i}</span>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
              <div className="mt-4 flex flex-wrap gap-4 text-xs text-white/60">
                <span className="flex items-center gap-1.5"><i className="h-2.5 w-2.5 rounded-full bg-emerald-400"></i>{t('Свободен')}</span>
                <span className="flex items-center gap-1.5"><i className="h-2.5 w-2.5 rounded-full bg-[#ff0169]"></i>{t('Идёт сессия')}</span>
                <span className="flex items-center gap-1.5"><i className="h-2.5 w-2.5 rounded-full bg-amber-300"></i>{t('Бронь')}</span>
              </div>
            </div>
          </div>
        </section>

        {/* Features */}
        <section id="features" className="scroll-mt-20 border-t border-white/5 py-20">
          <div className="mx-auto max-w-6xl px-4 sm:px-6">
            <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#ff4d8f]">{t('Возможности')}</p>
            <h2 className="mt-3 max-w-2xl text-3xl font-bold sm:text-4xl">{t('Всё, что нужно клубу каждый день')}</h2>
            <div className="mt-12 grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
              {features.map(([icon, title, text]) => (
                <article key={icon} className="group rounded-2xl border border-white/10 bg-white/[0.03] p-6 transition hover:-translate-y-1 hover:border-[#ff0169]/50 hover:bg-[#ff0169]/[0.06]">
                  <span className="mb-5 flex h-12 w-12 items-center justify-center rounded-xl bg-[#ff0169]/15 text-xl text-[#ff4d8f] group-hover:bg-[#ff0169] group-hover:text-white"><i className={`fas ${icon}`} aria-hidden="true"></i></span>
                  <h3 className="text-lg font-semibold">{title}</h3>
                  <p className="mt-2 text-sm leading-relaxed text-white/60">{text}</p>
                </article>
              ))}
            </div>
          </div>
        </section>

        {/* Agent */}
        <section id="agent" className="scroll-mt-20 py-20">
          <div className="mx-auto grid max-w-6xl items-center gap-12 px-4 sm:px-6 lg:grid-cols-2">
            <div className="relative overflow-hidden rounded-3xl border border-white/10">
              <img src="/images/landing/club.jpg" alt={t('Игровое место в компьютерном клубе')} className="h-full max-h-[460px] w-full object-cover" loading="lazy" />
              <div className="absolute inset-0 bg-gradient-to-t from-[#0b0d10] via-transparent to-transparent"></div>
              <div className="absolute bottom-5 left-5 right-5 rounded-2xl border border-white/10 bg-black/50 p-4 backdrop-blur">
                <p className="text-sm text-white/60">{t('Осталось времени')}</p>
                <p className="text-3xl font-bold">{t('{{hours}} ч {{minutes}} мин', { hours: 1, minutes: 27 })}</p>
              </div>
            </div>
            <div>
              <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#ff4d8f]">{t('Агент на ПК')}</p>
              <h2 className="mt-3 text-3xl font-bold sm:text-4xl">{t('Клиент сам покупает время, а ПК защищён')}</h2>
              <ul className="mt-8 space-y-4 text-white/75">
                {points.map((point) => (
                  <li key={point} className="flex gap-3"><i className="fas fa-circle-check mt-1 text-[#ff0169]" aria-hidden="true"></i><span>{point}</span></li>
                ))}
              </ul>
              <a href={data.download_url} className="mt-8 inline-flex items-center gap-2 rounded-xl border border-[#ff0169]/60 px-5 py-3 font-semibold text-[#ff7aa8] hover:bg-[#ff0169] hover:text-white" rel="noopener"><i className="fab fa-windows" aria-hidden="true"></i>{t('Скачать агент для Windows')}</a>
            </div>
          </div>
        </section>

        {/* How to start */}
        <section id="start" className="scroll-mt-20 border-t border-white/5 py-20">
          <div className="mx-auto max-w-6xl px-4 sm:px-6">
            <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#ff4d8f]">{t('Как начать')}</p>
            <h2 className="mt-3 text-3xl font-bold sm:text-4xl">{t('Три шага до первой сессии')}</h2>
            <ol className="mt-12 grid gap-5 md:grid-cols-3">
              {steps.map(([title, text], i) => (
                <li key={title} className="relative rounded-2xl border border-white/10 bg-white/[0.03] p-6">
                  <span className="mb-4 flex h-10 w-10 items-center justify-center rounded-full bg-gradient-to-br from-[#ff0169] to-[#ff7aa8] font-bold">{i + 1}</span>
                  <h3 className="text-lg font-semibold">{title}</h3>
                  <p className="mt-2 text-sm text-white/60">{text}</p>
                </li>
              ))}
            </ol>
          </div>
        </section>

        {/* FAQ */}
        <section id="faq" className="scroll-mt-20 py-20">
          <div className="mx-auto max-w-3xl px-4 sm:px-6">
            <h2 className="text-center text-3xl font-bold sm:text-4xl">{t('Частые вопросы')}</h2>
            <div className="mt-10 space-y-3">
              {faq.map(([question, answer]) => (
                <details key={question} className="group rounded-2xl border border-white/10 bg-white/[0.03] p-5 open:border-[#ff0169]/40">
                  <summary className="flex cursor-pointer list-none items-center justify-between gap-4 font-semibold">{question}<i className="fas fa-plus text-[#ff4d8f] transition group-open:rotate-45" aria-hidden="true"></i></summary>
                  <p className="mt-3 text-sm leading-relaxed text-white/65">{answer}</p>
                </details>
              ))}
            </div>
          </div>
        </section>

        {/* Call to action */}
        <section className="px-4 pb-24 sm:px-6">
          <div className="mx-auto max-w-6xl overflow-hidden rounded-3xl bg-gradient-to-br from-[#ff0169] to-[#b0004a] p-10 text-center shadow-2xl shadow-[#ff0169]/30 sm:p-14">
            <h2 className="text-3xl font-extrabold sm:text-4xl">{t('Готовы навести порядок в клубе?')}</h2>
            <p className="mx-auto mt-4 max-w-xl text-white/85">{t('Подключите клуб сегодня — {{days}} дней бесплатно, без карты и договора.', { days })}</p>
            <a href={authed ? '/dashboard' : '/register'} className="mt-8 inline-block rounded-xl bg-white px-7 py-3 font-semibold text-[#d90058] hover:bg-white/90">{authed ? t('Открыть панель') : t('Зарегистрировать клуб')}</a>
          </div>
        </section>
      </main>

      <footer className="border-t border-white/5 py-8">
        <div className="mx-auto flex max-w-6xl flex-col items-center justify-between gap-4 px-4 text-sm text-white/50 sm:flex-row sm:px-6">
          <div className="flex items-center gap-2"><img src="/images/logo.svg" alt="" className="h-6 w-auto" /><span>© {new Date().getFullYear()} Zafiro</span></div>
          <div className="flex gap-5">
            <a href="#features" className="hover:text-white">{t('Возможности')}</a>
            <a href="#faq" className="hover:text-white">{t('Вопросы')}</a>
            <a href="/wiki" data-reload className="hover:text-white">{t('Wiki')}</a>
            <a href="/login" className="hover:text-white">{t('Войти')}</a>
          </div>
        </div>
      </footer>
    </>
  );
}
