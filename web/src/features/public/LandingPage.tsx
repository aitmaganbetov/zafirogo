import { useSuspenseQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { landingQuery } from './api';
import { LandingLayout } from './LandingLayout';
import { AgentPhoto, PhoneGuest, PhoneStaff } from './parts';

/*
 * Главная — перенос resources/views/welcome.blade.php (LandingController::__invoke()) на каркасе layouts/landing.
 * Разметка и классы — как в шаблоне. Данные — GET /api/landing (пробный период, ссылка на установщик).
 */
export function LandingPage() {
  const { t } = useTranslation();
  const { status } = useSession();
  const { data } = useSuspenseQuery(landingQuery());
  const authed = status === 'authenticated';
  const days = data.trial_days;

  const description = t('Касса, карта зала, брони, тарифы, магазин и агент на ПК в одной системе. Подключите свой компьютерный клуб за один вечер.');
  const apps: ReadonlyArray<readonly [string, string]> = [
    [t('Приложение для гостей'), t('Клуб в телефоне гостя: баланс и бонусы, вход на ПК по QR-коду, новости клуба и переводы друзьям.')],
    [t('Приложение для сотрудников'), t('Касса, карта клуба и заказы в телефоне администратора: можно работать, не стоя за стойкой.')],
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

  return (
    <LandingLayout current="home" title={t('Zafiro — система управления компьютерным клубом')} description={description}>
      {/* Hero */}
      <section className="mx-auto grid max-w-6xl items-center gap-12 px-4 pb-20 pt-14 sm:px-6 lg:grid-cols-2 lg:pt-20">
        <div>
          <p className="mb-5 inline-flex items-center gap-2 rounded-full border border-[#ff0169]/40 bg-[#ff0169]/10 px-3 py-1 text-sm text-[#d90058]"><i className="fas fa-bolt" aria-hidden="true"></i>{t('Софт для компьютерных клубов')}</p>
          <h1 className="text-4xl font-extrabold leading-tight text-gray-900 sm:text-5xl lg:text-6xl">{t('Весь клуб - в одном окне')}</h1>
          <p className="mt-6 max-w-xl text-lg text-gray-600">{description}</p>
          <div className="mt-8 flex flex-wrap gap-3">
            {authed ? (
              <a href="/dashboard" className="rounded-xl bg-[#ff0169] px-6 py-3 font-semibold shadow-xl shadow-[#ff0169]/30 hover:bg-[#e6005e] text-white">{t('Открыть панель')}</a>
            ) : (
              <a href="/register" className="rounded-xl bg-[#ff0169] px-6 py-3 font-semibold shadow-xl shadow-[#ff0169]/30 hover:bg-[#e6005e] text-white">{t('{{days}} дней бесплатно', { days })}</a>
            )}
            <a href="/product/features" className="rounded-xl border border-gray-300 px-6 py-3 font-semibold text-gray-800 hover:border-gray-400">{t('Посмотреть возможности')}</a>
          </div>
          <dl className="mt-10 grid max-w-lg grid-cols-3 gap-4 text-center sm:text-left">
            <div><dt className="text-sm text-gray-500">{t('Запуск')}</dt><dd className="text-xl font-bold">{t('1 вечер')}</dd></div>
            <div><dt className="text-sm text-gray-500">{t('Пробный период')}</dt><dd className="text-xl font-bold">{t('{{count}} день|{{count}} дня|{{count}} дней', { count: days })}</dd></div>
            <div><dt className="text-sm text-gray-500">{t('Сервер')}</dt><dd className="text-xl font-bold">{t('в облаке')}</dd></div>
          </dl>
        </div>

        {/* The club map, as staff see it in the panel. */}
        <div className="relative">
          <div className="relative rounded-3xl border border-gray-200 bg-white p-5 shadow-2xl">
            <div className="mb-4 flex items-center gap-2"><span className="h-3 w-3 rounded-full bg-[#ff0169]"></span><span className="text-sm font-semibold">{t('Карта клуба')}</span></div>
            {/* Картинка LCP лендинга — высокий приоритет (аудит 22.11.2026, UI-5; preload в <head> замедлял LCP — см. parts.tsx AgentPhoto). */}
            <img src="/images/club-map.webp" width="810" height="800" fetchPriority="high" alt={t('Карта клуба в панели Zafiro: зоны и ПК с их статусами')} className="w-full rounded-2xl border border-gray-100" />
          </div>
        </div>
      </section>

      {/* Mobile apps */}
      <section id="apps" className="scroll-mt-20 py-20">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <div className="grid items-center gap-12 overflow-hidden rounded-[2rem] bg-gray-950 p-8 text-white sm:p-12 lg:grid-cols-[1.15fr_1fr]">
            <div>
              <h2 className="max-w-xl text-3xl font-bold leading-tight sm:text-4xl">{t('Мобильные приложения для гостей и сотрудников')}</h2>
              <p className="mt-4 max-w-xl text-lg text-gray-400">{t('Гость пополняет баланс и входит на ПК по QR-коду, а администратор ведёт кассу и карту клуба прямо с телефона.')}</p>
              <ol className="mt-10 space-y-8">
                {apps.map(([title, text], i) => (
                  <li key={title} className="flex gap-5">
                    <span className="w-14 shrink-0 text-4xl font-extrabold leading-none text-[#ff0169]">{String(i + 1).padStart(2, '0')}</span>
                    <div><h3 className="text-lg font-semibold">{title}</h3><p className="mt-1 max-w-md text-gray-300">{text}</p></div>
                  </li>
                ))}
              </ol>
              <div className="mt-10 flex flex-wrap gap-3">
                <a href="/product/guest-app" className="inline-flex items-center gap-3 rounded-2xl bg-white/10 px-5 py-3 font-semibold hover:bg-white/20"><i className="fab fa-android text-xl text-emerald-400" aria-hidden="true"></i>{t('Для гостей')}</a>
                <a href="/product/staff-app" className="inline-flex items-center gap-3 rounded-2xl bg-white/10 px-5 py-3 font-semibold hover:bg-white/20"><i className="fab fa-android text-xl text-emerald-400" aria-hidden="true"></i>{t('Для сотрудников')}</a>
              </div>
            </div>
            <div className="relative mx-auto h-[400px] w-full max-w-[480px] text-gray-900 sm:h-[460px]" aria-hidden="true">
              <div className="absolute left-0 top-8 hidden w-[230px] -rotate-6 sm:block"><PhoneStaff /></div>
              <div className="absolute inset-x-0 top-0 mx-auto w-[230px] rotate-6 sm:left-auto sm:right-0 sm:mx-0"><PhoneGuest /></div>
            </div>
          </div>
        </div>
      </section>

      {/* Agent */}
      <section id="agent" className="scroll-mt-20 py-20">
        <div className="mx-auto grid max-w-6xl items-center gap-12 px-4 sm:px-6 lg:grid-cols-2">
          <AgentPhoto lazy />
          <div>
            <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Агент на ПК')}</p>
            <h2 className="mt-3 text-3xl font-bold sm:text-4xl">{t('Клиент сам покупает время, а ПК защищён')}</h2>
            <ul className="mt-8 space-y-4 text-gray-700">
              {points.map((point) => (
                <li key={point} className="flex gap-3"><i className="fas fa-circle-check mt-1 text-[#ff0169]" aria-hidden="true"></i><span>{point}</span></li>
              ))}
            </ul>
            <a href={data.download_url} className="mt-8 inline-flex items-center gap-2 rounded-xl border border-[#ff0169]/60 px-5 py-3 font-semibold text-[#d90058] hover:bg-[#ff0169] hover:text-white" rel="noopener"><i className="fab fa-windows" aria-hidden="true"></i>{t('Скачать агент для Windows')}</a>
            <a href="/product/agent" className="ml-4 mt-8 inline-flex items-center gap-2 font-semibold text-gray-700 hover:text-gray-900">{t('Подробнее об агенте')}<i className="fas fa-arrow-right" aria-hidden="true"></i></a>
          </div>
        </div>
      </section>

      {/* How to start */}
      <section id="start" className="scroll-mt-20 border-t border-gray-100 py-20">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Как начать')}</p>
          <h2 className="mt-3 text-3xl font-bold sm:text-4xl">{t('Три шага до первой сессии')}</h2>
          <ol className="mt-12 grid gap-5 md:grid-cols-3">
            {steps.map(([title, text], i) => (
              <li key={title} className="relative rounded-2xl border border-gray-200 bg-gray-50 p-6">
                <span className="mb-4 flex h-10 w-10 items-center justify-center rounded-full bg-gradient-to-br from-[#ff0169] to-[#ff7aa8] font-bold text-white">{i + 1}</span>
                <h3 className="text-lg font-semibold">{title}</h3>
                <p className="mt-2 text-sm text-gray-600">{text}</p>
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
              <details key={question} className="group rounded-2xl border border-gray-200 bg-gray-50 p-5 open:border-[#ff0169]/40">
                <summary className="flex cursor-pointer list-none items-center justify-between gap-4 font-semibold">{question}<i className="fas fa-plus text-[#e6005e] transition group-open:rotate-45" aria-hidden="true"></i></summary>
                <p className="mt-3 text-sm leading-relaxed text-gray-600">{answer}</p>
              </details>
            ))}
          </div>
        </div>
      </section>
    </LandingLayout>
  );
}
