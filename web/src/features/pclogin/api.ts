import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/pclogin';

/*
 * Вход на ПК по QR с телефона (api/pclogin.yaml; QrLoginController). Ключи Query — ['pclogin', code].
 */

export const pcLoginApi = createApiClient<paths>();
export type PcLogin = components['schemas']['PcLogin'];

export const pcLoginQuery = (code: string) =>
  queryOptions({
    queryKey: ['pclogin', code],
    queryFn: () => unwrap(pcLoginApi.GET('/api/pc-login/{code}', { params: { path: { code } } })),
    staleTime: 0,
  });

export const approvePcLogin = (code: string) =>
  unwrap(pcLoginApi.POST('/api/pc-login/{code}', { params: { path: { code } } }));

/** where('code', '[A-Za-z0-9]{40}') маршрута Laravel: другой код — 404. */
export const PC_LOGIN_CODE = /^[A-Za-z0-9]{40}$/;
