import { useSuspenseQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { landingQuery } from './api';
import { LandingLayout } from './LandingLayout';
import { AgentPhoto, PointsCardItem, TextCardItem, type PointsCard, type TextCard } from './parts';

/*
 * «Агент на ПК» — перенос resources/views/product/agent.blade.php (LandingController::agent()).
 * Разметка и классы — как в шаблоне; ссылка на установщик — GET /api/landing (download_url).
 */
export function AgentPage() {
  const { t } = useTranslation();
  const { data } = useSuspenseQuery(landingQuery());
  const client: ReadonlyArray<PointsCard> = [
    ['fa-right-to-bracket', t('Вход'), [t('Телефон и пароль или QR-код с телефона'), t('Регистрация прямо на ПК за минуту'), t('«Забыли пароль?» — код приходит в Telegram')]],
    ['fa-house', t('Главная'), [t('Остаток времени, основной и бонусный баланс'), t('Новости клуба'), t('Язык агента: русский, казахский или английский')]],
    ['fa-stopwatch', t('Тарифы'), [t('Тарифы зоны этого ПК'), t('Любое число минут по цене часа и «на весь баланс»'), t('Абонементы: запуск сохранённого времени без оплаты')]],
    ['fa-bag-shopping', t('Магазин'), [t('Товары клуба по категориям с фото'), t('Корзина и оплата с баланса'), t('Заказ приходит сотрудникам с номером ПК')]],
    ['fa-bell', t('Во время игры'), [t('Уведомления за 10, 5, 3 и 1 минуту — со звуком, поверх игры'), t('Продлить сессию или пересесть на свободный ПК'), t('Ушёл раньше — неиспользованные минуты вернутся на баланс')]],
    ['fa-user', t('Профиль и история'), [t('Покупки, пополнения, возвраты и входы с фильтрами'), t('Друзья и перевод баланса, если клуб их включил'), t('Чат с сотрудниками клуба')]],
  ];
  const protection: ReadonlyArray<TextCard> = [
    ['fa-lock', t('ПК закрыт до оплаты'), t('Без купленного времени клиент видит только экран входа. Время закончилось — агент снова закрывает ПК, а клуб может перезагрузить или выключить его.')],
    ['fa-shield-halved', t('Служба Windows'), t('Служба запускает агент и поднимает его снова, если его закрыли или он завис. Агентом она признаёт только программу из Program Files.')],
    ['fa-user-lock', t('Защищённая учётная запись'), t('В учётной записи клиента нет диспетчера задач, редактора реестра, командной строки, PowerShell, «Выполнить» и панели управления.')],
    ['fa-key', t('Сотрудник — по PIN-коду'), t('Ctrl+Alt+P и PIN-код клуба, который проверяет сервер, или техрежим кнопкой на карте. Такой ПК отмечен на карте красным.')],
  ];
  const install: ReadonlyArray<readonly [string, string]> = [
    [t('Скачайте установщик'), t('Один файл ZafiroAgentSetup.exe для Windows 10 и 11.')],
    [t('Запустите от администратора'), t('Отметьте защиту учётной записи клиента и значок на рабочем столе.')],
    [t('Подключите ПК'), t('Введите ключ установки клуба, телефон и пароль сотрудника.')],
    [t('Готово'), t('ПК появляется на карте клуба, агент показывает экран входа.')],
  ];
  const panel = [
    t('Карта клуба: кто на каком ПК и сколько осталось'),
    t('Включение по сети, перезагрузка и выключение нескольких ПК сразу'),
    t('Сообщение на экраны выбранных ПК поверх игры'),
    t('Время гостю без аккаунта с карты клуба'),
    t('Ответы в чат из раздела «Помощь» агента'),
    t('Список ПК с версией агента, железом и IP'),
  ];

  return (
    <LandingLayout
      current="product.agent"
      title={t('Агент Zafiro на ПК — вход, тарифы, магазин и защита ПК')}
      description={t('Агент Zafiro для Windows: клиент сам входит, покупает время и товары, ПК закрыт до оплаты, а сотрудник выходит только по PIN-коду.')}
    >
      <section className="mx-auto grid max-w-6xl items-center gap-12 px-4 pb-16 pt-14 sm:px-6 lg:grid-cols-2 lg:pt-20">
        <div>
          <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Продукты')}</p>
          <h1 className="mt-3 text-4xl font-extrabold leading-tight text-gray-900 sm:text-5xl">{t('Агент Zafiro на ПК')}</h1>
          <p className="mt-6 max-w-xl text-lg text-gray-600">{t('Программа для Windows на каждом ПК клуба. Клиент сам входит, покупает время и заказывает товары, а ПК закрыт, пока время не оплачено.')}</p>
          <div className="mt-8 flex flex-wrap gap-3">
            <a href={data.download_url} className="inline-flex items-center gap-2 rounded-xl bg-[#ff0169] px-6 py-3 font-semibold shadow-xl shadow-[#ff0169]/30 hover:bg-[#e6005e] text-white" rel="noopener"><i className="fab fa-windows" aria-hidden="true"></i>{t('Скачать агент для Windows')}</a>
            <a href="#install" className="rounded-xl border border-gray-300 px-6 py-3 font-semibold text-gray-800 hover:border-gray-400">{t('Как установить')}</a>
          </div>
          <dl className="mt-10 grid max-w-lg grid-cols-3 gap-4 text-center sm:text-left">
            <div><dt className="text-sm text-gray-500">{t('Система')}</dt><dd className="text-xl font-bold">Windows 10/11</dd></div>
            <div><dt className="text-sm text-gray-500">{t('Обновление')}</dt><dd className="text-xl font-bold">{t('само')}</dd></div>
            <div><dt className="text-sm text-gray-500">{t('Языки')}</dt><dd className="text-xl font-bold">RU · KZ · EN</dd></div>
          </dl>
        </div>
        <AgentPhoto lazy={false} />
      </section>

      <section className="border-t border-gray-100 py-16">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Для клиента')}</p>
          <h2 className="mt-3 max-w-2xl text-3xl font-bold">{t('Клиенту не нужно идти на кассу')}</h2>
          <div className="mt-10 grid gap-5 sm:grid-cols-2 lg:grid-cols-3">
            {client.map((card) => <PointsCardItem key={card[0]} card={card} />)}
          </div>
        </div>
      </section>

      <section className="border-t border-gray-100 py-16">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Защита ПК')}</p>
          <h2 className="mt-3 max-w-2xl text-3xl font-bold">{t('Обойти оплату не получится')}</h2>
          <div className="mt-10 grid gap-5 md:grid-cols-2">
            {protection.map((card) => <TextCardItem key={card[0]} card={card} />)}
          </div>
        </div>
      </section>

      <section className="border-t border-gray-100 py-16">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Из панели клуба')}</p>
          <h2 className="mt-3 max-w-2xl text-3xl font-bold">{t('Весь зал под рукой')}</h2>
          <ul className="mt-8 grid gap-4 text-gray-700 sm:grid-cols-2">
            {panel.map((point) => (
              <li key={point} className="flex gap-3"><i className="fas fa-circle-check mt-1 text-[#ff0169]" aria-hidden="true"></i><span>{point}</span></li>
            ))}
          </ul>
        </div>
      </section>

      <section id="install" className="scroll-mt-20 border-t border-gray-100 py-16">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <p className="text-sm font-semibold uppercase tracking-[0.2em] text-[#e6005e]">{t('Установка')}</p>
          <h2 className="mt-3 text-3xl font-bold">{t('ПК на карте за пару минут')}</h2>
          <ol className="mt-10 grid gap-5 md:grid-cols-2 lg:grid-cols-4">
            {install.map(([title, text], i) => (
              <li key={title} className="rounded-2xl border border-gray-200 bg-gray-50 p-6">
                <span className="mb-4 flex h-10 w-10 items-center justify-center rounded-full bg-gradient-to-br from-[#ff0169] to-[#ff7aa8] font-bold text-white">{i + 1}</span>
                <h3 className="text-lg font-semibold">{title}</h3>
                <p className="mt-2 text-sm text-gray-600">{text}</p>
              </li>
            ))}
          </ol>
          <div className="mt-8 grid gap-5 md:grid-cols-2">
            <div className="rounded-2xl border border-gray-200 bg-gray-50 p-6">
              <h3 className="flex items-center gap-2 text-lg font-semibold"><i className="fas fa-rotate text-[#e6005e]" aria-hidden="true"></i>{t('Обновление')}</h3>
              <p className="mt-2 text-sm leading-relaxed text-gray-600">{t('С автообновлением новая версия ставится сама, когда ПК свободен. Вручную — новый установщик предложит «Обновить», настройки и подключение ПК сохранятся.')}</p>
            </div>
            <div className="rounded-2xl border border-gray-200 bg-gray-50 p-6">
              <h3 className="flex items-center gap-2 text-lg font-semibold"><i className="fas fa-hard-drive text-[#e6005e]" aria-hidden="true"></i>{t('Бездисковые клубы')}</h3>
              <p className="mt-2 text-sm leading-relaxed text-gray-600">{t('Агент ставится один раз в образ суперклиента. После загрузки каждый ПК сам подключается к клубу по его ключу.')}</p>
            </div>
          </div>
          <a href="/wiki" data-reload className="mt-8 inline-flex items-center gap-2 font-semibold text-[#d90058] hover:text-gray-900">{t('Подробная инструкция в Wiki')}<i className="fas fa-arrow-right" aria-hidden="true"></i></a>
        </div>
      </section>
    </LandingLayout>
  );
}
