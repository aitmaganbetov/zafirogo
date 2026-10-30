import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/warehouse';

/*
 * Склад (api/warehouse.yaml): каталог (WarehouseController, ProductController, ProductCategoryController,
 * StockMovementController, ProductDeleteController) и остатки для кассира (StockController). Ключи — ['warehouse', …].
 */

export const warehouseApi = createApiClient<paths>();

type S = components['schemas'];
export type Product = S['Product'];
export type WarehouseOverview = S['WarehouseOverview'];
export type StockPage = S['StockPage'];

export const overviewQuery = () =>
  queryOptions({ queryKey: ['warehouse', 'overview'], queryFn: () => unwrap(warehouseApi.GET('/api/warehouse/overview')) });

export const productsPageQuery = (page: number) =>
  queryOptions({
    queryKey: ['warehouse', 'products', 'page', page],
    queryFn: () => unwrap(warehouseApi.GET('/api/warehouse/products', { params: { query: { page, limit: 50 } } })),
  });

/** Поиск товара для формы движения и удаления (решение Q-070: вместо выпадающего списка всех товаров). */
export const productSearchQuery = (q: string) =>
  queryOptions({
    queryKey: ['warehouse', 'products', 'search', q],
    queryFn: ({ signal }) => unwrap(warehouseApi.GET('/api/warehouse/products', { params: { query: { q: q || undefined, limit: 20 } }, signal })),
    staleTime: 10_000,
  });

export const stockQuery = (search: string, page: number) =>
  queryOptions({
    queryKey: ['warehouse', 'stock', search, page],
    queryFn: () => unwrap(warehouseApi.GET('/api/stock', { params: { query: { q: search || undefined, page } } })),
  });

export const createCategory = (name: string) => unwrap(warehouseApi.POST('/api/warehouse/categories', { body: { name } }));

export const createProduct = (body: S['CreateProductRequest']) => unwrap(warehouseApi.POST('/api/warehouse/products', { body }));

export const createMovement = (body: S['StockMovementRequest']) => unwrap(warehouseApi.POST('/api/warehouse/stock-movements', { body }));

export const deleteProduct = (productId: number) =>
  unwrap(warehouseApi.DELETE('/api/warehouse/products/{productId}', { params: { path: { productId } } }));

export const receiveStock = (body: S['ReceiveStockRequest']) => unwrap(warehouseApi.POST('/api/stock/receive', { body }));
