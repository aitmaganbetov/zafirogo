// CRUD владельца клуба через интерфейс против настоящего сервера стенда tools/visual (не мок API): каждый шаг —
// действие на странице и успешный ответ API (2xx), затем результат на странице. Сценарии меняют данные стенда;
// повторный прогон — после `tools/visual/new.sh seed` и `new.sh start --no-build`.
// Запуск: npx playwright test -c e2e/visual/playwright.qa.config.ts crud
import { expect, test, type Locator, type Page } from '@playwright/test';
import { baseURL, login } from './support';

test.describe.configure({ mode: 'serial' });

const base = baseURL.new;
const stamp = Date.now().toString().slice(-6);

let page: Page;
const problems: string[] = [];

/** Действие страницы и ответ API (метод из набора, путь по шаблону); не 2xx — ошибка с телом ответа. */
async function api(methods: string[], path: RegExp, action: () => Promise<unknown>) {
  const res = page.waitForResponse((r) => methods.includes(r.request().method()) && path.test(new URL(r.url()).pathname));
  await action();
  const r = await res;
  expect(r.status(), `${r.request().method()} ${new URL(r.url()).pathname}: ${await r.text().catch(() => '')}`).toBeLessThan(300);
  return r;
}

async function open(path: string) {
  await page.goto(base + path);
  await page.waitForLoadState('networkidle');
}

/** Показывает форму, скрытую за кнопкой-переключателем (если она скрыта). */
async function reveal(form: Locator, toggle: string) {
  if (!(await form.isVisible())) await page.getByRole('button', { name: toggle }).first().click();
  await expect(form).toBeVisible();
}

test.beforeAll(async ({ browser }) => {
  page = await browser.newPage();
  page.on('dialog', (d) => void d.accept()); // подтверждения удаления (confirm)
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('response', (r) => {
    if (r.status() >= 500) problems.push(`http ${r.status()} ${r.request().method()} ${r.url()}`);
  });
  await login(page, 'new', 'owner', base);
});

test.afterAll(async () => {
  await page.close();
});

test.afterEach(() => {
  expect(problems.splice(0), 'ошибки на странице').toEqual([]);
});

test('услуги: добавить, изменить цену, удалить', async () => {
  const name = `QA услуга ${stamp}`;
  await open('/services');
  const create = page.locator('form[action="/services"]');
  await reveal(create, 'Добавить услугу');
  await create.locator('[name="name"]').fill(name);
  await create.locator('[name="price"]').fill('750');
  await create.locator('[name="unit"]').fill('шт');
  await api(['POST'], /^\/api\/services$/, () => create.locator('button[type="submit"], button:not([type])').last().click());
  const row = () => page.locator('tr', { has: page.locator(`[name="name"][value="${name}"]`) });
  await expect(row()).toHaveCount(1);

  await row().locator('[name="price"]').fill('800');
  await api(['PATCH', 'PUT'], /^\/api\/services\/\d+$/, () => row().getByRole('button', { name: 'Сохранить' }).click());
  await open('/services');
  await expect(row().locator('[name="price"]')).toHaveValue(/^800/);

  await api(['DELETE'], /^\/api\/services\/\d+$/, () => row().getByRole('button', { name: 'Удалить' }).click());
  await open('/services');
  await expect(row()).toHaveCount(0);
});

test('задачи: добавить, отметить выполненной, удалить', async () => {
  const title = `QA задача ${stamp}`;
  await open('/todos');
  const create = page.locator('form[action="/todos"][method="POST"], form[action="/todos"][method="post"]');
  await reveal(create, 'Добавить задачу');
  await create.locator('[name="title"]').fill(title);
  await api(['POST'], /^\/api\/todos$/, () => create.locator('button').last().click());
  const item = page.locator('li, .todo-item, article, tr', { hasText: title }).last();
  await expect(item).toBeVisible();
  await api(['PATCH', 'POST'], /^\/api\/todos\/\d+\/toggle$/, () => item.locator('input[type="checkbox"], button').first().click());
  await api(['DELETE'], /^\/api\/todos\/\d+$/, () => item.locator('button', { hasText: /Удалить|×/ }).or(item.locator('button[aria-label*="Удалить"]')).first().click());
  await open('/todos');
  await expect(page.getByText(title)).toHaveCount(0);
});

test('склад: категория, товар, приход, удаление товара', async () => {
  const cat = `QA категория ${stamp}`;
  const name = `QA товар ${stamp}`;
  await open('/warehouse');
  const catForm = page.locator('form[action="/warehouse/categories"]');
  await reveal(catForm, 'Новая категория');
  await catForm.locator('[name="name"]').fill(cat);
  await api(['POST'], /^\/api\/warehouse\/categories$/, () => catForm.getByRole('button', { name: 'Создать категорию' }).click());

  await open('/warehouse');
  const prod = page.locator('form[action="/warehouse/products"]');
  await reveal(prod, 'Новый товар');
  await prod.locator('[name="sku"]').fill(`QA-${stamp}`);
  await prod.locator('[name="name"]').fill(name);
  await prod.locator('[name="product_category_id"]').selectOption({ label: cat });
  await prod.locator('[name="price"]').fill('350');
  await prod.locator('[name="quantity"]').fill('10');
  await api(['POST'], /^\/api\/warehouse\/products$/, () => prod.getByRole('button', { name: 'Создать товар' }).click());
  await open('/warehouse');
  await expect(page.getByText(name).first()).toBeVisible();

  const mv = page.locator('form[action="/warehouse/stock-movements"]');
  await expect(mv).toBeVisible();
  // Поле «Товар» — комбобокс с поиском (ProductPicker): ввод имени, выбор из списка.
  await mv.locator('#product_id').fill(name);
  await page.locator('#product_id-options [role="option"]', { hasText: name }).first().click();
  await mv.locator('#type').selectOption('receipt');
  await mv.locator('[name="quantity"]').fill('5');
  await api(['POST'], /^\/api\/warehouse\/stock-movements$/, () => mv.getByRole('button', { name: 'Провести операцию' }).click());
});

test('клиенты: создать клиента и найти его поиском', async () => {
  const name = `QA Клиент ${stamp}`;
  await open('/users');
  const create = page.locator('form[action="/users"][method="POST"], form[action="/users"][method="post"]');
  await reveal(create, 'Добавить клиента');
  await create.locator('[name="name"]').fill(name);
  await create.locator('[name="phone"]').pressSequentially(`700${stamp}1`.slice(0, 10));
  await create.locator('[name="password"]').fill('QaPassword-2026');
  await create.locator('[name="password_confirmation"]').fill('QaPassword-2026');
  await api(['POST'], /^\/api\/customers$/, () => create.getByRole('button', { name: 'Создать' }).click());
  await open(`/users?search=${encodeURIComponent(name)}`);
  await expect(page.getByText(name).first()).toBeVisible();
});

test('новости: добавить и удалить новость', async () => {
  const title = `QA новость ${stamp}`;
  await open('/news');
  const create = page.locator('form[action="/news"]');
  await reveal(create, 'Добавить новость');
  await create.locator('[name="title"]').fill(title);
  await create.locator('[name="body"]').fill('Проверка QA: публикация новости клуба.');
  await api(['POST'], /^\/api\/news$/, () => create.locator('button[type="submit"], button:not([type])').last().click());
  await open('/news');
  // Новость — свёрнутая карточка <details>: форма правки и удаление — после раскрытия.
  const card = page.locator('details', { has: page.locator(`[name="title"][value="${title}"]`) });
  await expect(card).toHaveCount(1);
  await card.locator('summary').click();
  await api(['DELETE'], /^\/api\/news\/\d+$/, () => card.getByRole('button', { name: 'Удалить новость' }).click());
  await open('/news');
  await expect(page.locator(`[name="title"][value="${title}"]`)).toHaveCount(0);
});

test('брони: забронировать ПК и отменить бронь', async () => {
  await open('/reservations');
  const form = page.locator('form[action="/reservations"][method="POST"], form[action="/reservations"][method="post"]');
  await expect(form).toBeVisible();
  await form.locator('[name="club_computer_id"]').selectOption({ index: 1 });
  const starts = form.locator('[name="starts_at"]');
  // Завтра 15:00 по часам стенда (2026-10-15) — свободное время.
  await starts.fill('2026-10-16T15:00');
  await form.locator('[name="duration_minutes"]').selectOption({ index: 0 }).catch(() => form.locator('[name="duration_minutes"]').fill('60'));
  // Клиент — только из пользователей клуба (Laravel ReservationController: user_id required|exists).
  await form.locator('#reservation-customer').fill('Мария');
  await page.locator('#reservation-customer-options [role="option"]', { hasText: 'Мария Иванова' }).first().click();
  const created = await api(['POST'], /^\/api\/reservations$/, () => form.getByRole('button', { name: 'Забронировать' }).click());
  expect(created.status()).toBe(201);
  await open('/reservations?date=2026-10-16');
  const row = page.locator('tr, li, article', { hasText: 'Мария Иванова' }).filter({ hasText: '15:00' }).last();
  await expect(row).toBeVisible();
  await api(['POST'], /^\/api\/reservations\/\d+\/cancel$/, () => row.getByRole('button', { name: 'Отменить' }).click());
});

test('работники: добавить сотрудника по телефону и убрать', async () => {
  await open('/workers');
  const form = page.locator('form[action="/workers"]');
  // Клиент клуба из демо-данных (Тимур Сапаров) становится кассиром.
  await form.locator('[name="phone"]').pressSequentially('7011000008');
  await form.locator('[name="role"]').selectOption('cashier');
  await api(['POST'], /^\/api\/workers$/, () => form.getByRole('button', { name: 'Добавить' }).click());
  await open('/workers');
  const row = page.locator('tr, li, article', { hasText: 'Тимур Сапаров' }).last();
  await expect(row).toBeVisible();
  await api(['DELETE'], /^\/api\/workers\/\d+$/, () => row.getByRole('button', { name: 'Убрать' }).click());
});
