import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { catalogQuery, productsQuery, shiftQuery } from '../../../features/cash/api';
import { CashRegisterPage } from '../../../features/cash/CashRegisterPage';

// GET /cash-register (routes/web.php:270): club-area → cash.view.
export const Route = createFileRoute('/_admin/cash-register/')({
  beforeLoad: requirePermission('cash.view'),
  // Каталог и первая страница витрины — до первого рендера кассы: иначе в одноколоночной раскладке панель заказа
  // сначала рисуется вверху, а подгрузившаяся витрина сталкивает её за экран (CLS 0,3–0,67, UI-05). Ошибка этих
  // запросов экран не блокирует (prefetch) — витрина покажет её как раньше.
  loader: async ({ context: { queryClient } }) => {
    const state = await queryClient.ensureQueryData(shiftQuery());
    if (state.shift != null) {
      await Promise.all([queryClient.prefetchQuery(catalogQuery()), queryClient.prefetchInfiniteQuery(productsQuery(''))]);
    }
    return state;
  },
  component: CashRegisterPage,
});
