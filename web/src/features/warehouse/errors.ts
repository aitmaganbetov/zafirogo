import { isApiError } from '../../api/http';

/** Все ошибки формы сразу — `@foreach ($errors->all() as $error)` (страница склада). */
export function allErrors(e: unknown): string[] {
  if (isApiError(e)) {
    const list = Object.values(e.fieldErrors).flat();
    if (list.length > 0) return list;
    if (e.message) return [e.message];
  }
  return ['Server Error'];
}
