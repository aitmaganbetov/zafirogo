import type { components, paths } from './schema';

// Типы API генерируются из api/openapi.yaml: `npm run gen:api`.
export type Health = components['schemas']['Health'];
export type Readiness = components['schemas']['Readiness'];

type HealthPath = paths['/healthz']['get'];
export type HealthResponse = HealthPath['responses'][200]['content']['application/json'];

export async function fetchHealth(signal?: AbortSignal): Promise<HealthResponse> {
  const res = await fetch('/healthz', { signal, headers: { Accept: 'application/json' } });
  if (!res.ok) throw new Error(`healthz: HTTP ${res.status}`);
  return (await res.json()) as HealthResponse;
}
