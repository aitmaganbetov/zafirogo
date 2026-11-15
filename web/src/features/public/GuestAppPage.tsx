import { useTranslation } from 'react-i18next';
import { LandingLayout } from './LandingLayout';
import { PhoneGuest, PointsCardItem, TextCardItem, type PointsCard, type TextCard } from './parts';

/*
 * «Приложение для гостей» — перенос resources/views/product/guest-app.blade.php (LandingController::guestApp()).
 * Разметка и классы — как в шаблоне.
 */
export function GuestAppPage() {
  const { t } = useTranslation();
  const features: ReadonlyArray<PointsCard> = [
    ['fa-wallet', t('Баланс и скидка'), [t('Основной и бонусный баланс в клубе'), t('Персональная или накопительная скидка'), t('Обновление одной кнопкой')]],
    ['fa-qrcode', t('Вход на ПК по QR-коду'), [t('Наведите камеру на QR-код на экране входа ПК'), t('Пароль на ПК вводить не нужно'), t('Фонарик для тёмного зала')]],
    ['fa-newspaper', t('Новости'), [t('Новости и акции клуба'), t('Новости Zafiro для всех клубов'), t('Те же новости, что на главной в агенте')]],
    ['fa-user-group', t('Друзья и переводы'), [t('Добавить друга по номеру телефона'), t('Заявки в друзья: принять или отклонить'), t('Перевод баланса с подтверждением кодом в Telegram')]],
    ['fa-clock-rotate-left', t('История'), [t('Пополнения, покупки, возвраты и переводы'), t('Все операции в одной ленте'), t('Подгрузка более старых операций')]],
    ['fa-building', t('Несколько клубов'), [t('Один аккаунт по номеру телефона во всех клубах Zafiro'), t('Выбор клуба при входе'), t('Сменить клуб в любой момент')]],
  ];
  const forClub: ReadonlyArray<TextCard> = [
    ['fa-bolt', t('Быстрый вход'), t('Гость садится за ПК и входит за пару секунд — без ввода номера и пароля на клавиатуре клуба.')],
    ['fa-bullhorn', t('Новости в кармане'), t('Акции и турниры видны гостю не только в клубе, но и дома.')],
    ['fa-handshake', t('Друзья без кассы'), t('Гости сами переводят друг другу баланс, если клуб это разрешил.')],
  ];

  return (
    <LandingLayout
      current="product.guest-app"
      title={t('Приложение Zafiro для гостей — баланс, вход на ПК по QR-коду, друзья')}
      description={t('Мобильное приложение Zafiro для гостей клуба: баланс и бонусы, вход на ПК по QR-коду, новости клуба, друзья и переводы, история операций.')}
    >
      <section className="mx-auto grid max-w-6xl items-center gap-12 px-4 pb-16 pt-14 sm:px-6 lg:grid-cols-[1.2fr_1fr] lg:pt-20">
        <div>
          <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Продукты')}</p>
          <h1 className="mt-3 text-4xl font-extrabold leading-tight text-gray-900 sm:text-5xl">{t('Приложение для гостей')}</h1>
          <p className="mt-6 max-w-xl text-lg text-gray-600">{t('Клуб в телефоне гостя: баланс и бонусы, вход на ПК по QR-коду, новости клуба и переводы друзьям.')}</p>
          <dl className="mt-10 grid max-w-lg grid-cols-3 gap-4 text-center sm:text-left">
            <div><dt className="text-sm text-gray-500">{t('Платформа')}</dt><dd className="text-xl font-bold">Android</dd></div>
            <div><dt className="text-sm text-gray-500">{t('Вход')}</dt><dd className="text-xl font-bold">{t('как на ПК')}</dd></div>
            <div><dt className="text-sm text-gray-500">{t('Клубы')}</dt><dd className="text-xl font-bold">{t('все сразу')}</dd></div>
          </dl>
          <p className="mt-8 max-w-xl rounded-2xl border border-gray-200 bg-gray-50 p-4 text-sm text-gray-600"><i className="fab fa-android mr-2 text-[#e6005e]" aria-hidden="true"></i>{t('Гость входит тем же номером и паролем, что и на ПК клуба. Как установить приложение — уточните в поддержке Zafiro.')}</p>
        </div>

        {/* A stylised phone. */}
        <div className="relative mx-auto w-full max-w-[300px]" aria-hidden="true">
          <PhoneGuest />
        </div>
      </section>

      <section className="border-t border-gray-100 py-16">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Возможности')}</p>
          <h2 className="mt-3 max-w-2xl text-3xl font-bold">{t('Всё, что гостю нужно от клуба, — в телефоне')}</h2>
          <div className="mt-10 grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
            {features.map((card) => <PointsCardItem key={card[0]} card={card} />)}
          </div>
        </div>
      </section>

      <section className="border-t border-gray-100 py-16">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Для клуба')}</p>
          <h2 className="mt-3 max-w-2xl text-3xl font-bold">{t('Что это даёт клубу')}</h2>
          <div className="mt-10 grid gap-5 md:grid-cols-3">
            {forClub.map((card) => <TextCardItem key={card[0]} card={card} />)}
          </div>
          <p className="mt-8 text-sm text-gray-500">{t('Вход по QR-коду, друзья и переводы включаются в настройках клуба.')}</p>
        </div>
      </section>
    </LandingLayout>
  );
}
