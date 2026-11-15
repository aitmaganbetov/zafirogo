import { useLayoutEffect, useRef, useState } from 'react';

/**
 * Обработчики со стабильными ссылками: объект не меняется между рендерами, а вызов всегда попадает в функцию
 * последнего рендера. Нужен, чтобы `memo`-компоненты (плитка ПК, окна карты) не перерисовывались из-за новых
 * замыканий родителя. Вызывать только из событий (не во время рендера).
 */
// eslint-disable-next-line @typescript-eslint/no-explicit-any -- обобщение по сигнатурам обработчиков
export function useStableHandlers<T extends Record<string, (...args: any[]) => unknown>>(handlers: T): T {
  const latest = useRef(handlers);
  useLayoutEffect(() => {
    latest.current = handlers;
  });
  const [stable] = useState(() => {
    const out: Record<string, (...args: unknown[]) => unknown> = {};
    for (const k of Object.keys(handlers)) out[k] = (...args) => latest.current[k]?.(...args);
    return out as T;
  });
  return stable;
}
