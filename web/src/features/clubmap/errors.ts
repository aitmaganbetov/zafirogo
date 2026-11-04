import { isApiError } from '../../api/http';

/* Ошибки ответов и подписи для диалогов карты клуба. */

/** Первая ошибка ответа: ошибка поля или message (как Object.values(result.errors).flat()[0] ?? result.message). */
export function firstError(e: unknown, fallback: string): string {
  if (isApiError(e)) return Object.values(e.fieldErrors).flat()[0] ?? (e.message || fallback);
  return fallback;
}

/** Все ошибки ответа ($errors->all()). */
export function allErrors(e: unknown, fallback: string): string[] {
  if (isApiError(e)) {
    const list = Object.values(e.fieldErrors).flat();
    return list.length > 0 ? [...list] : [e.message || fallback];
  }
  return [fallback];
}

/** Подпись длительности: «30 минут», «1 час», «1 час 30 минут» (trans_choice шаблона). */
export function durationLabel(t: (k: string, o?: Record<string, unknown>) => string, minutes: number): string {
  const m = (n: number) => t('{{count}} минута|{{count}} минуты|{{count}} минут', { count: n });
  if (minutes < 60) return m(minutes);
  const h = t('{{count}} час|{{count}} часа|{{count}} часов', { count: Math.floor(minutes / 60) });
  return minutes % 60 ? `${h} ${m(minutes % 60)}` : h;
}

