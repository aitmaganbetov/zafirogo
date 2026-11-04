import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/services';

/* Услуги клуба (api/services.yaml, ClubServiceController). Ключи — ['services', …]. */

export const servicesApi = createApiClient<paths>();
export type ClubService = components['schemas']['ClubService'];
export type ServiceRequest = components['schemas']['ServiceRequest'];

export const servicesQuery = (page: number) =>
  queryOptions({ queryKey: ['services', page], queryFn: () => unwrap(servicesApi.GET('/api/services', { params: { query: { page } } })) });

export const createService = (body: ServiceRequest) => unwrap(servicesApi.POST('/api/services', { body }));

export const updateService = (serviceId: number, body: ServiceRequest) =>
  unwrap(servicesApi.PATCH('/api/services/{serviceId}', { params: { path: { serviceId } }, body }));

export const deleteService = (serviceId: number) => unwrap(servicesApi.DELETE('/api/services/{serviceId}', { params: { path: { serviceId } } }));
