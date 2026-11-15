import { afterEach, describe, expect, it, vi } from 'vitest';
import i18n from './index';
import { applyLanguage, cachedLanguages, clubLanguage, guestLanguage, hasLanguage, loadLanguage, resetI18nForTests, warmI18n } from './load';

describe('выбор языка (LocaleResolver)', () => {
  const enabled = ['ru', 'kk', 'en'];
  it('гость: язык браузера среди включённых, иначе en', () => {
    expect(guestLanguage(enabled, ['kk-KZ', 'ru'])).toBe('kk');
    expect(guestLanguage(enabled, ['de-DE'])).toBe('en');
    expect(guestLanguage(['ru', 'kk'], ['de'])).toBe('ru');
  });
  it('клуб: язык клуба, если включён, иначе ru', () => {
    expect(clubLanguage('kk', enabled)).toBe('kk');
    expect(clubLanguage(null, enabled)).toBe('ru');
    expect(clubLanguage('kk', ['ru', 'en'])).toBe('ru');
  });
});

describe('переводы с сервера в формате i18next', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    void i18n.changeLanguage('ru');
  });

  it('подстановки и множественное число', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(
          JSON.stringify({
            Касса: 'Касса',
            'Создан товар: {{product}}': 'Тауар құрылды: {{product}}',
            '{{count}} минута|{{count}} минуты|{{count}} минут_one': '{{count}} минута',
            '{{count}} минута|{{count}} минуты|{{count}} минут_few': '{{count}} минуты',
            '{{count}} минута|{{count}} минуты|{{count}} минут_many': '{{count}} минут',
            '{{count}} минута|{{count}} минуты|{{count}} минут_other': '{{count}} минут',
          }),
          { status: 200 },
        ),
      ),
    );
    await loadLanguage('ru', true);
    await applyLanguage('ru');
    const key = '{{count}} минута|{{count}} минуты|{{count}} минут';
    expect(i18n.t(key, { count: 1 })).toBe('1 минута');
    expect(i18n.t(key, { count: 3 })).toBe('3 минуты');
    expect(i18n.t(key, { count: 11 })).toBe('11 минут');
    expect(i18n.t(key, { count: 21 })).toBe('21 минута');
    expect(i18n.t('Создан товар: {{product}}', { product: 'Кола' })).toBe('Тауар құрылды: Кола');
    // нет перевода — ключ (русский текст), точки и двоеточия в ключе не ломают поиск
    expect(i18n.t('Нет перевода: тест. Точка.')).toBe('Нет перевода: тест. Точка.');
    expect(document.documentElement.lang).toBe('ru');
  });
});

describe('кэш переводов и параллельная загрузка (FE-04)', () => {
  const langs = [{ code: 'ru', name: 'Русский' }, { code: 'kk', name: 'Қазақша' }, { code: 'en', name: 'English' }];
  let serverVersion = 'v1';
  let pack: Record<string, string> = { Касса: 'Касса (v1)' };
  let calls: string[] = [];

  function stubServer() {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string) => {
        const url = new URL(input, location.origin);
        calls.push(url.pathname + url.search);
        if (url.pathname === '/api/i18n') return new Response(JSON.stringify({ languages: langs, version: serverVersion }), { status: 200 });
        return new Response(JSON.stringify(pack), { status: 200 });
      }),
    );
  }
  const settle = () => new Promise((r) => setTimeout(r, 0));

  function reset() {
    resetI18nForTests();
    for (const l of ['ru', 'kk', 'en']) if (i18n.hasResourceBundle(l, 'translation')) i18n.removeResourceBundle(l, 'translation');
    calls = [];
  }

  afterEach(async () => {
    vi.unstubAllGlobals();
    window.localStorage.clear();
    reset();
    await i18n.changeLanguage('ru');
  });

  it('первый заход: пакет с версией в адресе, результат — в кэш; повторный заход рисуется из кэша без запроса пакета', async () => {
    serverVersion = 'v1';
    pack = { Касса: 'Касса (v1)' };
    stubServer();
    reset();
    await applyLanguage('kk');
    expect(calls).toEqual(['/api/i18n', '/api/i18n/kk?v=v1']);
    expect(i18n.t('Касса')).toBe('Касса (v1)');

    // «Перезагрузка страницы»: память модуля пуста, localStorage остался.
    reset();
    await i18n.changeLanguage('ru');
    expect(cachedLanguages()?.map((l) => l.code)).toEqual(['ru', 'kk', 'en']);
    warmI18n();
    // Сразу, до ответа сервера: язык и пакет из кэша.
    expect(i18n.language).toBe('kk');
    expect(hasLanguage('kk')).toBe(true);
    expect(i18n.t('Касса')).toBe('Касса (v1)');
    await settle();
    await settle();
    // Версия та же — пакет не перезапрашивается.
    expect(calls).toEqual(['/api/i18n']);
  });

  it('версия на сервере изменилась — пакет обновляется в фоне и заменяет кэш целиком', async () => {
    serverVersion = 'v1';
    pack = { Касса: 'Касса (v1)', Удалённый: 'был' };
    stubServer();
    reset();
    await applyLanguage('kk');
    reset();
    serverVersion = 'v2';
    pack = { Касса: 'Касса (v2)' };
    warmI18n();
    expect(i18n.t('Касса')).toBe('Касса (v1)');
    await settle();
    await settle();
    await settle();
    expect(calls).toEqual(['/api/i18n', '/api/i18n/kk?v=v2']);
    expect(i18n.t('Касса')).toBe('Касса (v2)');
    expect(i18n.t('Удалённый')).toBe('Удалённый');
    expect(JSON.parse(window.localStorage.getItem('zafiro.i18n.pack.kk') ?? '{}')).toEqual({ version: 'v2', bundle: pack });
  });

  it('повреждённый кэш — откат к загрузке с сервера', async () => {
    serverVersion = 'v1';
    pack = { Касса: 'Касса (v1)' };
    stubServer();
    reset();
    window.localStorage.setItem('zafiro.i18n.state', '{"lang":"kk","languages":"oops"}');
    window.localStorage.setItem('zafiro.i18n.pack.kk', '{"version":1,"bundle":[1,2]}');
    warmI18n();
    expect(hasLanguage('kk')).toBe(false);
    expect(window.localStorage.getItem('zafiro.i18n.pack.kk')).toBeNull();
    await applyLanguage('kk');
    expect(calls).toEqual(['/api/i18n', '/api/i18n/kk?v=v1']);
    expect(i18n.t('Касса')).toBe('Касса (v1)');
  });

  it('первый заход без кэша: список языков и пакет гостя запрашиваются сразу, не дожидаясь сессии', async () => {
    serverVersion = 'v1';
    stubServer();
    reset();
    warmI18n();
    await settle();
    await settle();
    await settle();
    expect(calls[0]).toBe('/api/i18n');
    expect(calls[1]).toMatch(/^\/api\/i18n\/(ru|kk|en)\?v=v1$/);
  });

  it('ru без пакета «ключ = значение»: ключ, подстановки и формы числа работают', async () => {
    serverVersion = 'v1';
    pack = {}; // сервер не отдаёт тождественные записи
    stubServer();
    reset();
    await applyLanguage('ru');
    expect(i18n.t('Касса')).toBe('Касса');
    expect(i18n.t('Создан товар: {{product}}', { product: 'Кола' })).toBe('Создан товар: Кола');
    const key = '{{count}} место|{{count}} места|{{count}} мест';
    expect([1, 3, 11, 21].map((count) => i18n.t(key, { count }))).toEqual(['1 место', '3 места', '11 мест', '21 место']);
  });

  it('сервер недоступен — остаются ключи, приложение работает', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('offline'); }));
    reset();
    await applyLanguage('ru');
    expect(i18n.t('Касса')).toBe('Касса');
  });
});
