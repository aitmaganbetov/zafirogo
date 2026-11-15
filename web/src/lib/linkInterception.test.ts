import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { interceptLinks, internalHref, PRELOAD_DELAY_MS } from './linkInterception';

describe('ссылки из Blade: переход роутера и предзагрузка по намерению (FE-06)', () => {
  let off: () => void;
  const router = { navigate: vi.fn(), preloadRoute: vi.fn(async () => undefined) };

  beforeEach(() => {
    vi.useFakeTimers();
    router.navigate.mockClear();
    router.preloadRoute.mockClear();
    document.body.innerHTML = `
      <a id="users" href="/users?search=a"><i class="fas fa-users"></i><span>Клиенты</span></a>
      <a id="ext" href="https://example.com/x">ext</a>
      <a id="api" href="/api/export">api</a>
      <a id="blank" href="/wiki" target="_blank">wiki</a>
      <a id="reload" href="/docs/api" data-reload>docs</a>
      <button id="btn">btn</button>`;
    off = interceptLinks(router);
  });
  afterEach(() => {
    off();
    vi.useRealTimers();
  });

  const el = (id: string) => document.getElementById(id) as HTMLElement;
  const over = (node: Element, relatedTarget: Element | null = null) => node.dispatchEvent(new MouseEvent('mouseover', { bubbles: true, relatedTarget }));
  const out = (node: Element, relatedTarget: Element | null = null) => node.dispatchEvent(new MouseEvent('mouseout', { bubbles: true, relatedTarget }));

  it('внутренний адрес: только свои ссылки без target/download/data-reload, не /api и не /ws', () => {
    expect(internalHref(el('users') as HTMLAnchorElement)).toBe('/users?search=a');
    for (const id of ['ext', 'api', 'blank', 'reload']) expect(internalHref(el(id) as HTMLAnchorElement)).toBeNull();
    expect(internalHref(null)).toBeNull();
  });

  it('клик — переход роутера без перезагрузки; с Ctrl — как обычная ссылка', () => {
    const ev = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 });
    el('users').querySelector('span')?.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(true);
    expect(router.navigate).toHaveBeenCalledWith({ href: '/users?search=a' });
    const ctrl = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ctrlKey: true });
    el('users').dispatchEvent(ctrl);
    expect(ctrl.defaultPrevented).toBe(false);
    expect(router.navigate).toHaveBeenCalledTimes(1);
  });

  it('наведение дольше задержки — одна предзагрузка; движение внутри ссылки её не повторяет', async () => {
    const a = el('users');
    over(a);
    over(a.querySelector('i') as Element, a);
    over(a.querySelector('span') as Element, a);
    expect(router.preloadRoute).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(PRELOAD_DELAY_MS + 1);
    expect(router.preloadRoute).toHaveBeenCalledTimes(1);
    expect(router.preloadRoute).toHaveBeenCalledWith({ href: '/users?search=a' });
  });

  it('проведение мышью (ушли раньше задержки) ничего не грузит', async () => {
    const a = el('users');
    over(a);
    out(a, el('btn'));
    await vi.advanceTimersByTimeAsync(PRELOAD_DELAY_MS * 4);
    expect(router.preloadRoute).not.toHaveBeenCalled();
  });

  it('фокус с клавиатуры и касание тоже предзагружают; чужие ссылки — нет', async () => {
    el('users').dispatchEvent(new FocusEvent('focusin', { bubbles: true }));
    await vi.advanceTimersByTimeAsync(PRELOAD_DELAY_MS + 1);
    expect(router.preloadRoute).toHaveBeenCalledTimes(1);
    el('users').dispatchEvent(new Event('touchstart', { bubbles: true }));
    await vi.advanceTimersByTimeAsync(0);
    expect(router.preloadRoute).toHaveBeenCalledTimes(2);
    for (const id of ['ext', 'api', 'blank', 'reload', 'btn']) over(el(id));
    await vi.advanceTimersByTimeAsync(PRELOAD_DELAY_MS + 1);
    expect(router.preloadRoute).toHaveBeenCalledTimes(2);
  });

  it('ошибка предзагрузки не всплывает', async () => {
    router.preloadRoute.mockRejectedValueOnce(new Error('offline'));
    over(el('users'));
    await vi.advanceTimersByTimeAsync(PRELOAD_DELAY_MS + 1);
    expect(router.preloadRoute).toHaveBeenCalledTimes(1);
  });
});
