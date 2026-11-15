import { useTranslation } from 'react-i18next';
import { LandingLayout } from './LandingLayout';
import { PhoneStaff, PointsCardItem, TextCardItem, type PointsCard, type TextCard } from './parts';

/*
 * «Приложение для сотрудников» — перенос resources/views/product/staff-app.blade.php (LandingController::staffApp()).
 * Разметка и классы — как в шаблоне.
 */
export function StaffAppPage() {
  const { t } = useTranslation();
  const features: ReadonlyArray<PointsCard> = [
    ['fa-map-location-dot', t('Карта клуба'), [t('ПК по зонам и статусам, обновление каждые 10 секунд'), t('Включить, перезагрузить, выключить, завершить сессию'), t('Бронь и время гостю — сразу на несколько ПК')]],
    ['fa-cash-register', t('Касса'), [t('Открытие и закрытие смены, X- и Z-отчёт'), t('Пополнение баланса и продажа товаров'), t('Приход и расход, последние операции')]],
    ['fa-users', t('Клиенты'), [t('Поиск по имени и телефону'), t('Баланс, бонусы и скидка клиента'), t('Пополнение прямо из карточки')]],
    ['fa-bag-shopping', t('Заказы и чат'), [t('Заказы из магазина агента с номером ПК'), t('«Выдан» или «Отменить» одной кнопкой'), t('Ответы гостям в чат клуба')]],
    ['fa-boxes-stacked', t('Склад'), [t('Остатки товаров'), t('Приход товара'), t('Списание — у кого есть право')]],
    ['fa-chart-line', t('Отчёт за день'), [t('Доход и расход за сегодня и за 7 дней'), t('ПК в сети и в брони'), t('Товары, которых осталось мало')]],
  ];
  const access: ReadonlyArray<TextCard> = [
    ['fa-user-shield', t('Права как в панели'), t('Сотрудник видит в приложении только те разделы и кнопки, на которые у него есть права в клубе.')],
    ['fa-key', t('Вход и пароль'), t('Вход по номеру и паролю сотрудника, «Забыли пароль?» — код приходит в Telegram.')],
    ['fa-building', t('Несколько клубов'), t('Если человек работает в нескольких клубах, он выбирает клуб после входа.')],
    ['fa-bell', t('Уведомления'), t('Колокольчик в шапке: новые заказы, сообщения, ПК не в сети, мало товара.')],
  ];

  return (
    <LandingLayout
      current="product.staff-app"
      title={t('Приложение Zafiro для сотрудников — касса, карта клуба и заказы в телефоне')}
      description={t('Мобильное приложение Zafiro для сотрудников клуба: карта ПК, касса и смены, клиенты, заказы из агента, чат, склад и отчёт за день.')}
    >
      <section className="mx-auto grid max-w-6xl items-center gap-12 px-4 pb-16 pt-14 sm:px-6 lg:grid-cols-[1.2fr_1fr] lg:pt-20">
        <div>
          <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Продукты')}</p>
          <h1 className="mt-3 text-4xl font-extrabold leading-tight text-gray-900 sm:text-5xl">{t('Приложение для сотрудников')}</h1>
          <p className="mt-6 max-w-xl text-lg text-gray-600">{t('Касса, карта клуба и заказы в телефоне администратора: можно работать, не стоя за стойкой.')}</p>
          <dl className="mt-10 grid max-w-lg grid-cols-3 gap-4 text-center sm:text-left">
            <div><dt className="text-sm text-gray-500">{t('Платформа')}</dt><dd className="text-xl font-bold">Android</dd></div>
            <div><dt className="text-sm text-gray-500">{t('Для кого')}</dt><dd className="text-xl font-bold">{t('5 ролей')}</dd></div>
            <div><dt className="text-sm text-gray-500">{t('Тема')}</dt><dd className="text-xl font-bold">{t('день и ночь')}</dd></div>
          </dl>
          <p className="mt-8 max-w-xl rounded-2xl border border-gray-200 bg-gray-50 p-4 text-sm text-gray-600"><i className="fab fa-android mr-2 text-[#e6005e]" aria-hidden="true"></i>{t('Для владельца, администратора, менеджера, кассира и оператора. Как установить приложение — уточните в поддержке Zafiro.')}</p>
        </div>

        {/* A stylised phone. */}
        <div className="relative mx-auto w-full max-w-[300px]" aria-hidden="true">
          <PhoneStaff />
        </div>
      </section>

      <section className="border-t border-gray-100 py-16">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Возможности')}</p>
          <h2 className="mt-3 max-w-2xl text-3xl font-bold">{t('Главное из панели клуба — в кармане')}</h2>
          <div className="mt-10 grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
            {features.map((card) => <PointsCardItem key={card[0]} card={card} />)}
          </div>
        </div>
      </section>

      <section className="border-t border-gray-100 py-16">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Доступ')}</p>
          <h2 className="mt-3 max-w-2xl text-3xl font-bold">{t('Каждому — своё')}</h2>
          <div className="mt-10 grid gap-5 md:grid-cols-2">
            {access.map((card) => <TextCardItem key={card[0]} card={card} />)}
          </div>
        </div>
      </section>
    </LandingLayout>
  );
}
