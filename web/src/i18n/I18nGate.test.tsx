import { act, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { session, type Me } from '../auth/session';
import { I18nGate } from './I18nGate';
import i18n from './index';
import { resetI18nForTests } from './load';

/* Ревью 22.11.2026: устаревший эффект (язык сменился, пока грузился пакет) интерфейс обратно не переключает. */

const langs = [{ code: 'ru', name: 'Русский' }, { code: 'kk', name: 'Қазақша' }, { code: 'en', name: 'English' }];
const me = (locale: string) =>
  ({ server_time: '', user: { id: 1, name: 'A', phone: '', is_superadmin: false }, club: { id: 1, name: 'C', timezone: 'UTC', locale }, role: 'owner', permissions: [], read_only: false, clubs: [] }) as Me;

describe('I18nGate', () => {
  let packs: Record<string, (bundle: Record<string, string>) => void> = {};
  beforeEach(async () => {
    window.localStorage.clear();
    resetI18nForTests();
    await i18n.changeLanguage('ru');
    packs = {};
    vi.stubGlobal(
      'fetch',
      vi.fn((input: Request | string) => {
        const path = new URL(input instanceof Request ? input.url : input, location.origin).pathname;
        const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200 });
        if (path === '/api/i18n') return Promise.resolve(json({ languages: langs, version: 'v1' }));
        return new Promise<Response>((resolve) => {
          packs[path.split('/').pop() ?? ''] = (bundle) => resolve(json(bundle));
        });
      }),
    );
  });
  afterEach(async () => {
    vi.unstubAllGlobals();
    session.clear();
    resetI18nForTests();
    window.localStorage.clear();
    await i18n.changeLanguage('ru');
  });
  const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });
  const stored = () => (JSON.parse(window.localStorage.getItem('zafiro.i18n.state') ?? '{}') as { lang?: string }).lang;

  it('пакет прежнего языка пришёл после смены языка клуба: язык и запись о нём — новые', async () => {
    const change = vi.spyOn(i18n, 'changeLanguage');
    act(() => session.setMe(me('kk')));
    render(<I18nGate><p>страница</p></I18nGate>);
    await flush();
    expect(Object.keys(packs)).toEqual(['kk']);
    expect(screen.queryByText('страница')).not.toBeInTheDocument();

    // Клуб сменил язык (или сотрудник переключил клуб), пока пакет kk ещё в пути.
    act(() => session.setMe(me('en')));
    await flush();
    packs.en?.({ Касса: 'Cash desk' });
    await flush();
    expect(i18n.language).toBe('en');
    expect(screen.getByText('страница')).toBeInTheDocument();

    packs.kk?.({ Касса: 'Касса (kk)' });
    await flush();
    expect(i18n.language).toBe('en');
    expect(document.documentElement.lang).toBe('en');
    expect(stored()).toBe('en');
    expect(change).not.toHaveBeenCalledWith('kk');
    change.mockRestore();
  });

  it('обычный вход: язык клуба применён и запомнен', async () => {
    act(() => session.setMe(me('kk')));
    render(<I18nGate><p>страница</p></I18nGate>);
    await flush();
    packs.kk?.({ Касса: 'Касса (kk)' });
    await flush();
    expect(screen.getByText('страница')).toBeInTheDocument();
    expect(i18n.language).toBe('kk');
    expect(stored()).toBe('kk');
  });
});
