import { screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { mockApi, renderPage, signIn } from '../../test/pages';
import { AgentPage } from './AgentPage';
import { FeaturesPage } from './FeaturesPage';
import { GuestAppPage } from './GuestAppPage';
import { LandingPage } from './LandingPage';
import { PricingPage } from './PricingPage';
import { StaffAppPage } from './StaffAppPage';

/*
 * Публичный сайт (тема Е, docs/upstream-2026-11.md): сценарии tests/Feature/LandingPageTest.php старого проекта —
 * GS-572 (меню шапки ведут на страницы продукта) и GS-573 (страница тарифов из тарифов платформы), плюс главная.
 */

const DOWNLOAD = 'https://github.com/aitmaganbetov/Zafiro/releases/latest/download/ZafiroAgentSetup.exe';
const LANDING = { trial_days: 14, download_url: DOWNLOAD };

const hrefs = (root: ParentNode = document) => [...root.querySelectorAll('a')].map((a) => a.getAttribute('href'));
const meta = (sel: string) => document.head.querySelector<HTMLMetaElement>(sel)?.content;

describe('публичный сайт', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    session.clear();
    document.body.className = '';
  });

  it('главная: новый первый экран, регистрация гостю, панель вошедшему', async () => {
    mockApi({ 'GET /api/landing': { ...LANDING, trial_days: 30 } });
    // Тег, подставленный сервером в index.html (L-1): клиент обновляет его, а не добавляет второй.
    document.head.insertAdjacentHTML('beforeend', '<meta name="description" content="с сервера">');
    const { unmount } = renderPage(<LandingPage />);
    expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('Весь клуб - в одном окне');
    expect(document.title).toBe('Zafiro — система управления компьютерным клубом');
    expect(meta('meta[name="description"]')).toMatch(/^Касса, карта зала, брони/);
    expect(meta('meta[property="og:title"]')).toBe(document.title);
    expect(meta('meta[property="og:image"]')).toBe(`${location.origin}/icons/icon-512.png`);
    expect(meta('meta[property="og:type"]')).toBe('website');
    // Белый фон и шрифт <body> шаблона layouts/landing.
    expect(document.body).toHaveClass('min-h-screen', 'bg-white', 'text-gray-900', 'antialiased');
    expect(document.documentElement).toHaveClass('scroll-smooth');

    const main = within(screen.getByRole('main'));
    expect(main.getByText('Софт для компьютерных клубов')).toBeInTheDocument();
    expect(main.getByRole('link', { name: '30 дней бесплатно' })).toHaveAttribute('href', '/register');
    expect(main.getByRole('link', { name: 'Посмотреть возможности' })).toHaveAttribute('href', '/product/features');
    // Факты первого экрана: пробный период (trans_choice) и облачный сервер.
    expect(main.getByText('Пробный период').nextElementSibling).toHaveTextContent('30 дней');
    expect(main.getByText('Сервер').nextElementSibling).toHaveTextContent('в облаке');
    // Настоящий снимок карты клуба вместо нарисованной схемы с легендой.
    expect(main.getByAltText('Карта клуба в панели Zafiro: зоны и ПК с их статусами')).toHaveAttribute('src', '/images/club-map.webp');
    expect(main.queryByText(/Сейчас занято/)).toBeNull();
    // Тёмный блок мобильных приложений вместо сетки возможностей.
    expect(main.getByRole('heading', { name: 'Мобильные приложения для гостей и сотрудников' })).toBeInTheDocument();
    expect(document.querySelector('#features')).toBeNull();
    expect(document.querySelector('#apps .bg-gray-950')).not.toBeNull();
    expect(main.getByRole('link', { name: 'Скачать агент для Windows' })).toHaveAttribute('href', DOWNLOAD);
    expect(main.getByText('30 дней бесплатно, без карты и договора.')).toBeInTheDocument();
    expect(main.getByText('Подключите клуб сегодня — 30 дней бесплатно, без карты и договора.')).toBeInTheDocument();

    // Шапка: меню «О продукте» и «Продукты», «Тарифы», Wiki (перезагрузкой — другой документ), вход.
    const header = screen.getByRole('banner');
    expect(within(header).getAllByText('О продукте').length).toBeGreaterThan(0);
    expect(within(header).getAllByText('Продукты').length).toBeGreaterThan(0);
    for (const href of ['/product/features', '/product/agent', '/product/guest-app', '/product/staff-app', '/pricing', '/wiki', '/login', '/register', '#start', '#faq']) {
      expect(hrefs(header)).toContain(href);
    }
    expect(header.querySelector('a[href="/wiki"]')).toHaveAttribute('data-reload');
    expect(header.querySelector('img')).toHaveAttribute('src', '/images/logo-dark.svg');
    // L-2 / AU-16: у ссылки-логотипа есть доступное имя и там, где «ZAFIRO» скрыто (телефон); классы — как в Blade.
    const logo = within(header).getByRole('link', { name: 'ZAFIRO — Главная' });
    expect(logo).toHaveAttribute('href', '/');
    expect(logo).toHaveClass('flex', 'shrink-0', 'items-center', 'gap-2', 'font-bold', 'tracking-wide');
    // L-1: заголовки страницы — по одному тегу (тег из index.html сервера переиспользуется, не дублируется).
    for (const sel of ['meta[name="description"]', 'meta[property="og:title"]', 'meta[property="og:description"]', 'meta[property="og:image"]', 'meta[property="og:type"]']) {
      expect(document.head.querySelectorAll(sel)).toHaveLength(1);
    }
    expect(header.querySelectorAll('[aria-current="page"]')).toHaveLength(0);
    unmount();
    expect(document.body).not.toHaveClass('bg-white');

    // Вошедший: «Открыть панель» в шапке, на первом экране и в призыве.
    signIn('owner', []);
    renderPage(<LandingPage />);
    await screen.findByRole('heading', { level: 1 });
    expect(screen.getAllByRole('link', { name: 'Открыть панель' }).map((a) => a.getAttribute('href'))).toEqual(['/dashboard', '/dashboard', '/dashboard']);
    expect(hrefs(screen.getByRole('banner'))).not.toContain('/register');
  });

  it('меню шапки: одно открыто за раз, закрываются по Escape и клику мимо', async () => {
    mockApi({ 'GET /api/landing': LANDING });
    renderPage(<LandingPage />);
    await screen.findByRole('heading', { level: 1 });
    const menus = [...document.querySelectorAll<HTMLDetailsElement>('[data-landing-menu]')];
    expect(menus).toHaveLength(3); // два на широком экране и одно общее на телефоне
    const open = (m: HTMLDetailsElement) => {
      m.open = true;
      m.dispatchEvent(new Event('toggle'));
    };
    open(menus[0]!);
    open(menus[1]!);
    expect(menus.map((m) => m.open)).toEqual([false, true, false]);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(menus.some((m) => m.open)).toBe(false);
    open(menus[2]!);
    screen.getByRole('main').click();
    expect(menus.some((m) => m.open)).toBe(false);
  });

  it('GS-572 TestLanding_MenusAndProductPages: страницы продукта', async () => {
    mockApi({ 'GET /api/landing': LANDING });

    let page = renderPage(<FeaturesPage />);
    expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('Возможности Zafiro');
    expect(document.title).toBe('Возможности Zafiro — касса, зал, тарифы и клиенты клуба');
    expect(screen.getByRole('heading', { name: 'Касса и деньги' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Клиенты и лояльность' })).toBeInTheDocument();
    expect(hrefs()).toEqual(expect.arrayContaining(['/product/agent', '/#faq', '/#start', '#cash', '#more']));
    // Группа из четырёх карточек — в четыре колонки, остальные — в три.
    expect(document.querySelector('#hall .grid')).toHaveClass('lg:grid-cols-4');
    expect(document.querySelector('#cash .grid')).toHaveClass('lg:grid-cols-3');
    expect(document.querySelectorAll('main article')).toHaveLength(19);
    expect(screen.getByRole('banner').querySelector('[aria-current="page"]')).toHaveAttribute('href', '/product/features');
    page.unmount();

    page = renderPage(<AgentPage />);
    expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('Агент Zafiro на ПК');
    expect(screen.getByRole('heading', { name: 'Обойти оплату не получится' })).toBeInTheDocument();
    expect(hrefs()).toEqual(expect.arrayContaining([DOWNLOAD, '/register', '#install']));
    expect(screen.getByRole('link', { name: 'Подробная инструкция в Wiki' })).toHaveAttribute('data-reload');
    page.unmount();

    page = renderPage(<GuestAppPage />);
    expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('Приложение для гостей');
    expect(screen.getByRole('heading', { name: 'Вход на ПК по QR-коду' })).toBeInTheDocument();
    expect(screen.getByText('4 500 ₸')).toBeInTheDocument(); // макет телефона гостя
    page.unmount();

    page = renderPage(<StaffAppPage />);
    expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('Приложение для сотрудников');
    expect(screen.getByRole('heading', { name: 'Права как в панели' })).toBeInTheDocument();
    expect(screen.getByText('Bootcamp')).toBeInTheDocument(); // макет телефона сотрудника
    page.unmount();

    // Вошедший — ссылка на панель.
    signIn('owner', []);
    renderPage(<AgentPage />);
    await screen.findByRole('heading', { level: 1 });
    expect(hrefs()).toContain('/dashboard');
    expect(hrefs()).not.toContain('/register');
  });

  it('GS-573 TestPricing_ListsActivePlans: тарифы платформы, цена «за ПК в месяц»', async () => {
    mockApi({
      'GET /api/landing': LANDING,
      'GET /api/pricing': {
        plans: [
          { id: 3, name: 'Старт', price_monthly: null, trial_days: 14, features: [] },
          { id: 4, name: 'Сеть клубов', price_monthly: null, trial_days: 0, features: ['Любое число ПК'] },
          { id: 1, name: 'Клуб до 30 ПК', price_monthly: 2500000, trial_days: 0, features: ['До 30 ПК'] },
          { id: 2, name: 'Клуб до 60 ПК', price_monthly: 123450, trial_days: 3, features: [] },
        ],
      },
    });
    renderPage(<PricingPage />);
    expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('Простая подписка для клуба');
    expect(document.title).toBe('Тарифы Zafiro — подписка для компьютерного клуба');
    expect(screen.getByText('Начните с 14 дней бесплатно, без карты и договора. Все возможности Zafiro — в любом тарифе.')).toBeInTheDocument();
    const card = (name: string) => screen.getByRole('heading', { name }).closest('article') as HTMLElement;
    // Порядок — как отдал сервер (без цены первыми).
    expect([...document.querySelectorAll('main article h2')].map((h) => h.textContent)).toEqual(['Старт', 'Сеть клубов', 'Клуб до 30 ПК', 'Клуб до 60 ПК']);

    const club = card('Клуб до 30 ПК');
    // Цена и подпись «за ПК в месяц» — отдельными строками (block) в одном абзаце.
    const price = within(club).getByText('25 000 ₸');
    expect(price).toHaveClass('block', 'text-4xl');
    expect(price.nextElementSibling).toHaveTextContent('за ПК в месяц');
    expect(price.nextElementSibling).toHaveClass('mt-1', 'block', 'text-gray-500');
    expect(within(club).getByText('До 30 ПК')).toBeInTheDocument();
    expect(within(club).queryByText('Все возможности Zafiro')).toBeNull();
    expect(within(club).queryByText(/бесплатно/)).toBeNull();
    expect(within(club).getByRole('link', { name: 'Зарегистрировать клуб' })).toHaveAttribute('href', '/register');

    // number_format(…, 0): 1 234,50 ₸ → «1 235 ₸» (половина — от нуля), пробные дни — «3 дня бесплатно».
    expect(within(card('Клуб до 60 ПК')).getByText('1 235 ₸')).toBeInTheDocument();
    expect(within(card('Клуб до 60 ПК')).getByText('3 дня бесплатно')).toBeInTheDocument();
    expect(within(card('Клуб до 60 ПК')).getByText('Все возможности Zafiro')).toBeInTheDocument();
    // Без цены: с пробными днями — «Бесплатно» и «14 дней», без них — «По запросу».
    expect(within(card('Старт')).getByText('Бесплатно')).toBeInTheDocument();
    expect(within(card('Старт')).getByText('14 дней')).toBeInTheDocument();
    expect(within(card('Старт')).queryByText('за ПК в месяц')).toBeNull();
    expect(within(card('Сеть клубов')).getByText('По запросу')).toBeInTheDocument();
    expect(screen.queryByText('Старый тариф')).toBeNull();

    // Вопросы об оплате: четыре, ответ про карту — с дефисом, как в шаблоне.
    const faq = [...document.querySelectorAll('main details')];
    expect(faq).toHaveLength(4);
    expect(faq[0]).toHaveTextContent('Нет. Зарегистрируйте клуб - пробный период начнётся сразу, без карты и договора.');
    expect(screen.getByRole('banner').querySelector('a[href="/pricing"]')).toHaveClass('text-gray-900');
  });

  it('тарифов нет — «Тарифы скоро появятся.»', async () => {
    mockApi({ 'GET /api/landing': LANDING, 'GET /api/pricing': { plans: [] } });
    renderPage(<PricingPage />);
    expect(await screen.findByText('Тарифы скоро появятся.')).toBeInTheDocument();
    expect(document.querySelector('main article')).toBeNull();
  });
});
