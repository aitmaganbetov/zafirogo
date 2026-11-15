import { useSuspenseQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { formatMoney } from '../../lib/money';
import { landingQuery, pricingQuery } from './api';
import { LandingLayout } from './LandingLayout';

/*
 * «Тарифы» — перенос resources/views/pricing.blade.php (LandingController::pricing()). Разметка и классы — как в
 * шаблоне. Тарифы — GET /api/pricing (активные тарифы платформы, цена — целые тиыны «за ПК в месяц»).
 */
export function PricingPage() {
  const { t } = useTranslation();
  const { status } = useSession();
  const { data } = useSuspenseQuery(landingQuery());
  const { data: pricing } = useSuspenseQuery(pricingQuery());
  const authed = status === 'authenticated';

  const questions: ReadonlyArray<readonly [string, string]> = [
    [t('Нужна ли карта для пробного периода?'), t('Нет. Зарегистрируйте клуб - пробный период начнётся сразу, без карты и договора.')],
    [t('Что будет, когда подписка закончится?'), t('Панель, агенты на ПК и API клуба перестанут работать, пока подписку не продлят. Данные клуба при этом сохраняются.')],
    [t('Можно ли сменить тариф?'), t('Да. Тариф меняется без переноса данных: клуб, ПК, клиенты и история остаются на месте.')],
    [t('Есть ли скидки?'), t('Для сетей клубов и по договорённости Zafiro даёт клубу индивидуальную скидку на тариф.')],
  ];

  return (
    <LandingLayout
      current="pricing"
      title={t('Тарифы Zafiro — подписка для компьютерного клуба')}
      description={t('Тарифы Zafiro для компьютерных и PS-клубов: пробный период без карты и договора, все возможности в любом тарифе.')}
    >
      <section className="mx-auto max-w-6xl px-4 pb-12 pt-14 text-center sm:px-6 lg:pt-20">
        <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Тарифы')}</p>
        <h1 className="mx-auto mt-3 max-w-3xl text-4xl font-extrabold leading-tight text-gray-900 sm:text-5xl">{t('Простая подписка для клуба')}</h1>
        <p className="mx-auto mt-6 max-w-2xl text-lg text-gray-600">{t('Начните с {{days}} дней бесплатно, без карты и договора. Все возможности Zafiro — в любом тарифе.', { days: data.trial_days })}</p>
      </section>

      <section className="pb-16">
        <div className="mx-auto flex max-w-6xl flex-wrap justify-center gap-5 px-4 sm:px-6">
          {pricing.plans.length === 0 && <p className="text-gray-600">{t('Тарифы скоро появятся.')}</p>}
          {pricing.plans.map((plan) => {
            const price = plan.price_monthly ?? null;
            const free = price === null && plan.trial_days > 0;
            return (
              <article key={plan.id} className="flex w-full flex-col rounded-3xl border border-gray-200 bg-white p-7 shadow-sm sm:w-80">
                <h2 className="text-lg font-semibold">{plan.name}</h2>
                <p className="mt-4">
                  {price !== null ? (
                    <>
                      {/* number_format((float) $plan->price_monthly, 0, '.', ' ') — до целых тенге, половина — от нуля */}
                      <span className="block text-4xl font-extrabold">{formatMoney(price, 0, '.', ' ')} ₸</span>
                      <span className="mt-1 block text-gray-500">{t('за ПК в месяц')}</span>
                    </>
                  ) : free ? (
                    <span className="text-4xl font-extrabold">{t('Бесплатно')}</span>
                  ) : (
                    <span className="text-4xl font-extrabold">{t('По запросу')}</span>
                  )}
                </p>
                {plan.trial_days > 0 && (
                  <p className="mt-2 text-sm text-[#e6005e]">
                    {free
                      ? t('{{count}} день|{{count}} дня|{{count}} дней', { count: plan.trial_days })
                      : t('{{count}} день бесплатно|{{count}} дня бесплатно|{{count}} дней бесплатно', { count: plan.trial_days })}
                  </p>
                )}
                <ul className="mt-6 flex-1 space-y-3 text-sm text-gray-700">
                  {plan.features.length > 0 ? (
                    plan.features.map((feature, i) => (
                      <li key={i} className="flex gap-2"><i className="fas fa-check mt-1 text-xs text-[#e6005e]" aria-hidden="true"></i><span>{feature}</span></li>
                    ))
                  ) : (
                    <li className="flex gap-2"><i className="fas fa-check mt-1 text-xs text-[#e6005e]" aria-hidden="true"></i><span>{t('Все возможности Zafiro')}</span></li>
                  )}
                </ul>
                <a href={authed ? '/dashboard' : '/register'} className="mt-8 rounded-xl bg-[#ff0169] px-5 py-3 text-center font-semibold text-white shadow-lg shadow-[#ff0169]/30 hover:bg-[#e6005e]">{authed ? t('Открыть панель') : t('Зарегистрировать клуб')}</a>
              </article>
            );
          })}
        </div>
      </section>

      <section className="border-t border-gray-100 py-16">
        <div className="mx-auto max-w-3xl px-4 sm:px-6">
          <h2 className="text-center text-3xl font-bold">{t('Вопросы об оплате')}</h2>
          <div className="mt-10 space-y-3">
            {questions.map(([question, answer]) => (
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
