import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { mockApi, renderPage, signIn } from '../../test/pages';
import { WarehousePage } from './WarehousePage';

/* FE-11: склад — цены из тиынов, новый товар (цена → тиыны), движение товара; FE-14 — без лишнего запроса. */

const cola = { id: 1, sku: 'DRK-001', name: 'Coca-Cola 0,5 л', category: 'Напитки', product_category_id: 2, price_tiyn: 45050, quantity: 43 };
const water = { id: 2, sku: 'DRK-002', name: 'Вода 0,5 л', category: 'Напитки', product_category_id: 2, price_tiyn: 20000, quantity: 3 };
const overview = { categories: [{ id: 2, name: 'Напитки', products_count: 2 }], products_total: 2, movements: [] };
const products = { items: [cola, water], next_cursor: null, total: 2, per_page: 50 };

describe('склад: деньги и операции', () => {
  beforeEach(() => signIn('owner', ['warehouse.view', 'warehouse.create', 'warehouse.update', 'warehouse.delete']));
  afterEach(() => {
    vi.unstubAllGlobals();
    session.clear();
  });

  const routes = (extra: Record<string, unknown> = {}) =>
    mockApi({ 'GET /api/warehouse/overview': overview, 'GET /api/warehouse/products': products, ...extra });

  it('цены каталога — из тиынов; при открытии — два запроса, без отдельного запроса «первого товара» (FE-14)', async () => {
    const api = routes();
    renderPage(<WarehousePage page={1} />);
    await screen.findByText('Coca-Cola 0,5 л');
    expect(screen.getByText('451 KZT')).toBeInTheDocument(); // 450,50 → number_format(…, 0): половина вверх
    expect(screen.getByText('200 KZT')).toBeInTheDocument();
    // Поле «Товар» формы движения: как у <select>, выбран первый товар — из уже загруженной страницы каталога.
    await waitFor(() => expect((document.getElementById('product_id') as HTMLInputElement).value).toBe('Coca-Cola 0,5 л (остаток: 43)'));
    expect(api.calls.map((c) => `${c.method} ${c.path}?${c.search.toString()}`).sort()).toEqual([
      'GET /api/warehouse/overview?',
      'GET /api/warehouse/products?page=1&limit=50',
    ]);
  });

  it('новый товар: цена «199.99» → 19 999 тиынов', async () => {
    const api = routes({ 'POST /api/warehouse/products': { message: 'Товар создан.' } });
    renderPage(<WarehousePage page={1} />);
    await screen.findByText('Coca-Cola 0,5 л');
    fireEvent.click(screen.getByRole('button', { name: 'Новый товар' }));
    const form = document.querySelector('#product-modal form') as HTMLFormElement;
    const set = (name: string, value: string) => fireEvent.change(form.querySelector(`[name="${name}"]`) as HTMLInputElement, { target: { value } });
    set('sku', 'SNK-010');
    set('name', 'Чипсы');
    set('product_category_id', '2');
    set('price', '199.99');
    set('quantity', '12');
    fireEvent.submit(form);
    expect(await screen.findByText('Товар создан.')).toBeInTheDocument();
    expect(api.of('POST', '/api/warehouse/products')[0]?.body).toEqual({ sku: 'SNK-010', name: 'Чипсы', product_category_id: 2, price_tiyn: 19999, quantity: 12 });
  });

  it('цена с тремя знаками — ошибка, товар не создаётся (Q-002)', async () => {
    const api = routes({ 'POST /api/warehouse/products': { message: 'Товар создан.' } });
    renderPage(<WarehousePage page={1} />);
    await screen.findByText('Coca-Cola 0,5 л');
    fireEvent.click(screen.getByRole('button', { name: 'Новый товар' }));
    const form = document.querySelector('#product-modal form') as HTMLFormElement;
    const set = (name: string, value: string) => fireEvent.change(form.querySelector(`[name="${name}"]`) as HTMLInputElement, { target: { value } });
    set('sku', 'SNK-010');
    set('name', 'Чипсы');
    set('product_category_id', '2');
    set('price', '1.999');
    fireEvent.submit(form);
    await waitFor(() => expect(document.querySelector('.bg-red-50 li')).not.toBeNull());
    expect(api.of('POST', '/api/warehouse/products')).toHaveLength(0);
  });

  it('движение товара: списание 5 шт. выбранного товара с комментарием', async () => {
    const api = routes({ 'POST /api/warehouse/stock-movements': { message: 'ok' } });
    renderPage(<WarehousePage page={1} />);
    await screen.findByText('Coca-Cola 0,5 л');
    await waitFor(() => expect((document.getElementById('product_id') as HTMLInputElement).value).toContain('Coca-Cola'));
    fireEvent.change(document.getElementById('type') as HTMLSelectElement, { target: { value: 'write_off' } });
    fireEvent.change(document.getElementById('quantity') as HTMLInputElement, { target: { value: '5' } });
    fireEvent.change(document.getElementById('note') as HTMLInputElement, { target: { value: 'Бой' } });
    fireEvent.submit(document.querySelector('form[action="/warehouse/stock-movements"]') as HTMLFormElement);
    expect(await screen.findByText('Складская операция выполнена')).toBeInTheDocument();
    expect(api.of('POST', '/api/warehouse/stock-movements')[0]?.body).toEqual({ product_id: 1, type: 'write_off', quantity: 5, note: 'Бой' });
  });

  it('выбор товара с клавиатуры: стрелки и Enter выбирают, Escape закрывает список (UI-10)', async () => {
    routes();
    renderPage(<WarehousePage page={1} />);
    await screen.findByText('Coca-Cola 0,5 л');
    const input = document.getElementById('product_id') as HTMLInputElement;
    await waitFor(() => expect(input.value).toContain('Coca-Cola'));
    fireEvent.focus(input);
    const list = document.getElementById('product_id-options') as HTMLElement;
    await waitFor(() => expect(list.querySelectorAll('[role="option"]')).toHaveLength(2));
    expect(list.hidden).toBe(false);
    fireEvent.keyDown(input, { key: 'Escape' });
    expect(list.hidden).toBe(true);
    fireEvent.keyDown(input, { key: 'ArrowDown' }); // открыть
    fireEvent.keyDown(input, { key: 'ArrowDown' }); // первый
    fireEvent.keyDown(input, { key: 'ArrowDown' }); // второй
    expect(input.getAttribute('aria-activedescendant')).toBe('product_id-option-2');
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(list.hidden).toBe(true);
    expect(input.value).toBe('Вода 0,5 л (остаток: 3)');
  });
});
