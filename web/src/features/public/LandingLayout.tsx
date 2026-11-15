import { useSuspenseQuery } from '@tanstack/react-query';
import { Fragment, useEffect, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { landingQuery } from './api';

/*
 * Каркас публичного сайта — перенос resources/views/layouts/landing.blade.php и partials/landing-menu-link.blade.php
 * (тема Е, docs/upstream-2026-11.md). Разметка и классы — как в шаблоне; @auth — вошедший пользователь (сессия SPA).
 * Данные — GET /api/landing (LandingController::data()). route('wiki') — /wiki (с отдельным хостом wiki сервер
 * отвечает 301 на него, решение Q-069). Font Awesome и Inter — из сборки (Q-064), не с CDN.
 */

/** Имя маршрута Laravel страницы (request()->routeIs(…)). */
export type LandingRoute = 'home' | 'product.features' | 'product.agent' | 'product.guest-app' | 'product.staff-app' | 'pricing';

const BODY_CLASSES = ['min-h-screen', 'bg-white', 'text-gray-900', 'antialiased'];
const BODY_FONT = "Inter, 'Segoe UI', system-ui, sans-serif";

const SUMMARY = 'flex cursor-pointer list-none items-center gap-1.5 rounded-lg py-2 hover:text-gray-900 group-open:text-gray-900';
const PANEL = 'absolute top-full z-40 mt-2 rounded-2xl border border-gray-200 bg-white p-2 shadow-2xl shadow-gray-900/10';

/** Пункт меню шапки: [url, icon, title, text, route name or null]. */
type MenuLink = readonly [string, string, string, string, LandingRoute | null];

function useMeta(name: string, content: string, attr: 'name' | 'property' = 'name') {
  useEffect(() => {
    // Тег из index.html (сервер подставляет его для страниц сайта, решение L-1) переиспользуется — без дублей.
    const el = document.head.querySelector<HTMLMetaElement>(`meta[${attr}="${name}"]`) ?? document.createElement('meta');
    el.setAttribute(attr, name);
    el.content = content;
    if (!el.isConnected) document.head.appendChild(el);
    return () => el.remove();
  }, [attr, name, content]);
}

/** <head> шаблона: <title> без « · Zafiro», description и Open Graph; классы <html> и <body>. */
function useLandingDocument(title: string, description: string) {
  useEffect(() => {
    document.title = title;
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
}

/** Header menus: one open at a time, closed by a click elsewhere and by Escape (скрипт шаблона). */
function useLandingMenus() {
  useEffect(() => {
    const menus = [...document.querySelectorAll<HTMLDetailsElement>('[data-landing-menu]')];
    const onToggle = (event: Event) => {
      const menu = event.currentTarget as HTMLDetailsElement;
      if (menu.open) menus.forEach((other) => { if (other !== menu) other.open = false; });
    };
    const onClick = (event: MouseEvent) => menus.forEach((menu) => { if (!menu.contains(event.target as Node)) menu.open = false; });
    const onKey = (event: KeyboardEvent) => { if (event.key === 'Escape') menus.forEach((menu) => { menu.open = false; }); };
    menus.forEach((menu) => menu.addEventListener('toggle', onToggle));
    document.addEventListener('click', onClick);
    document.addEventListener('keydown', onKey);
    return () => {
      menus.forEach((menu) => menu.removeEventListener('toggle', onToggle));
      document.removeEventListener('click', onClick);
      document.removeEventListener('keydown', onKey);
    };
  }, []);
}

/** partials/landing-menu-link.blade.php */
function MenuLinkItem({ link, current }: { link: MenuLink; current: LandingRoute }) {
  const [url, icon, title, text, route] = link;
  return (
    <a href={url} className="flex gap-3 rounded-xl p-3 hover:bg-gray-100" aria-current={route !== null && route === current ? 'page' : undefined}>
      <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-[#ff0169]/15 text-[#e6005e]"><i className={`fas ${icon}`} aria-hidden="true"></i></span>
      <span><span className="block font-semibold text-gray-900">{title}</span><span className="block text-xs text-gray-500">{text}</span></span>
    </a>
  );
}

export interface LandingLayoutProps {
  /** Страница (request()->routeIs()). */
  readonly current: LandingRoute;
  /** @section('title') и @section('description') — уже переведённые. */
  readonly title: string;
  readonly description: string;
  readonly children: ReactNode;
}

export function LandingLayout({ current, title, description, children }: LandingLayoutProps) {
  const { t } = useTranslation();
  const { status } = useSession();
  const { data } = useSuspenseQuery(landingQuery());
  const authed = status === 'authenticated';
  const days = data.trial_days;
  useLandingDocument(title, description);
  useLandingMenus();

  // $home: якоря главной — на самой главной без адреса, с остальных страниц — с адресом главной.
  const home = current === 'home' ? '' : '/';
  const menus: ReadonlyArray<readonly [string, ReadonlyArray<MenuLink>]> = [
    [t('О продукте'), [
      ['/product/features', 'fa-layer-group', t('Возможности'), t('Касса, зал, тарифы, клиенты, склад и команда'), 'product.features'],
      [`${home}#start`, 'fa-flag-checkered', t('Как начать'), t('Три шага до первой сессии'), null],
      [`${home}#faq`, 'fa-circle-question', t('Вопросы'), t('Сервер, интернет, бездисковые ПК, цена'), null],
    ]],
    [t('Продукты'), [
      ['/product/agent', 'fa-desktop', t('Агент на ПК'), t('Вход, тарифы, магазин и защита ПК'), 'product.agent'],
      ['/product/guest-app', 'fa-mobile-screen', t('Приложение для гостей'), t('Баланс, вход на ПК по QR-коду, друзья'), 'product.guest-app'],
      ['/product/staff-app', 'fa-user-tie', t('Приложение для сотрудников'), t('Касса, карта клуба и заказы в телефоне'), 'product.staff-app'],
    ]],
  ];

  return (
    <>
      <header className="sticky top-0 z-30 border-b border-gray-100 bg-white/80 shadow-[0_2px_12px_rgba(17,24,39,0.06)] backdrop-blur">
        <nav className="mx-auto flex max-w-6xl items-center justify-between gap-3 px-4 py-3 sm:px-6">
          {/* aria-label — осознанное отличие от Blade (L-2, AU-16): на телефоне «ZAFIRO» скрыто и у ссылки не было имени. */}
          <a href="/" aria-label={`ZAFIRO — ${t('Главная')}`} className="flex shrink-0 items-center gap-2 font-bold tracking-wide">
            <img src="/images/logo-dark.svg" alt="" className="h-9 w-auto" />
            <span className="hidden text-lg sm:inline">ZAFIRO</span>
          </a>
          {/* Wide screens: a dropdown per menu. */}
          <div className="hidden items-center gap-6 text-sm text-gray-600 md:flex">
            {menus.map(([menuTitle, links]) => (
              <details key={menuTitle} className="group relative" data-landing-menu>
                <summary className={SUMMARY}>{menuTitle}<i className="fas fa-chevron-down text-xs transition group-open:rotate-180" aria-hidden="true"></i></summary>
                <div className={`${PANEL} left-1/2 w-80 -translate-x-1/2`}>
                  {links.map((link) => <MenuLinkItem key={link[0]} link={link} current={current} />)}
                </div>
              </details>
            ))}
            <a href="/pricing" className={`hover:text-gray-900 ${current === 'pricing' ? 'text-gray-900' : ''}`}>{t('Тарифы')}</a>
            <a href="/wiki" data-reload className="hover:text-gray-900">{t('Wiki')}</a>
          </div>
          {/* Phones: one menu with both groups. */}
          <details className="group relative text-sm text-gray-600 md:hidden" data-landing-menu>
            <summary className={SUMMARY}>{t('Меню')}<i className="fas fa-chevron-down text-xs transition group-open:rotate-180" aria-hidden="true"></i></summary>
            <div className={`${PANEL} left-1/2 max-h-[75vh] w-[min(20rem,calc(100vw-2rem))] -translate-x-1/2 overflow-y-auto`}>
              {menus.map(([menuTitle, links]) => (
                <Fragment key={menuTitle}>
                  <p className="px-3 pb-1 pt-2 text-xs font-semibold uppercase tracking-wider text-gray-400">{menuTitle}</p>
                  {links.map((link) => <MenuLinkItem key={link[0]} link={link} current={current} />)}
                </Fragment>
              ))}
              <div className="mt-1 border-t border-gray-200 pt-1">
                <a href="/pricing" className="block rounded-xl px-3 py-2 hover:bg-gray-100">{t('Тарифы')}</a>
                <a href="/wiki" data-reload className="block rounded-xl px-3 py-2 hover:bg-gray-100">{t('Wiki')}</a>
                {!authed && <a href="/login" className="block rounded-xl px-3 py-2 hover:bg-gray-100 sm:hidden">{t('Войти')}</a>}
              </div>
            </div>
          </details>
          <div className="flex shrink-0 items-center gap-2">
            {authed ? (
              <a href="/dashboard" className="rounded-lg bg-[#ff0169] px-4 py-2 text-sm font-semibold shadow-lg shadow-[#ff0169]/30 hover:bg-[#e6005e] text-white">{t('Открыть панель')}</a>
            ) : (
              <>
                <a href="/login" className="hidden rounded-lg px-3 py-2 text-sm text-gray-700 hover:text-gray-900 sm:inline">{t('Войти')}</a>
                <a href="/register" className="rounded-lg bg-[#ff0169] px-4 py-2 text-sm font-semibold shadow-lg shadow-[#ff0169]/30 hover:bg-[#e6005e] text-white">{t('Попробовать')}</a>
              </>
            )}
          </div>
        </nav>
      </header>

      <main className="relative">
        {children}

        {/* Call to action */}
        <section className="px-4 pb-24 sm:px-6">
          <div className="mx-auto max-w-6xl overflow-hidden rounded-3xl bg-gradient-to-br from-[#ff0169] to-[#b0004a] p-10 text-center text-white shadow-2xl shadow-[#ff0169]/30 sm:p-14">
            <h2 className="text-3xl font-extrabold sm:text-4xl">{t('Готовы навести порядок в клубе?')}</h2>
            <p className="mx-auto mt-4 max-w-xl text-white/85">{t('Подключите клуб сегодня — {{days}} дней бесплатно, без карты и договора.', { days })}</p>
            <a href={authed ? '/dashboard' : '/register'} className="mt-8 inline-block rounded-xl bg-white px-7 py-3 font-semibold text-[#d90058] hover:bg-white/90">{authed ? t('Открыть панель') : t('Зарегистрировать клуб')}</a>
          </div>
        </section>
      </main>

      <footer className="border-t border-gray-100 py-8">
        <div className="mx-auto flex max-w-6xl flex-col items-center justify-between gap-4 px-4 text-sm text-gray-500 sm:flex-row sm:px-6">
          <div className="flex items-center gap-2"><img src="/images/logo-dark.svg" alt="" className="h-6 w-auto" /><span>© {new Date().getFullYear()} Zafiro</span></div>
          <div className="flex flex-wrap justify-center gap-5">
            <a href="/product/features" className="hover:text-gray-900">{t('Возможности')}</a>
            <a href="/product/agent" className="hover:text-gray-900">{t('Агент на ПК')}</a>
            <a href="/product/guest-app" className="hover:text-gray-900">{t('Приложение для гостей')}</a>
            <a href="/product/staff-app" className="hover:text-gray-900">{t('Приложение для сотрудников')}</a>
            <a href="/pricing" className="hover:text-gray-900">{t('Тарифы')}</a>
            <a href={`${home}#faq`} className="hover:text-gray-900">{t('Вопросы')}</a>
            <a href="/wiki" data-reload className="hover:text-gray-900">{t('Wiki')}</a>
            <a href="/login" className="hover:text-gray-900">{t('Войти')}</a>
          </div>
        </div>
      </footer>
    </>
  );
}
