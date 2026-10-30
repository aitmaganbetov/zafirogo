import { useState } from 'react';

/*
 * Flash-сообщения между страницами — как session('status') / ->with('status', …) Laravel: действие кладёт текст
 * перед переходом, следующая страница показывает его один раз (каждая — своей разметкой, как в старом шаблоне).
 * Хранятся в sessionStorage (переживают перезагрузку, как flash в сессии Laravel).
 */

const PREFIX = 'zafiro.flash.';

export type FlashKey = 'status' | 'success' | 'error';

export function setFlash(key: FlashKey, message: string): void {
  try {
    sessionStorage.setItem(PREFIX + key, message);
  } catch {
    // хранилище недоступно — сообщение не покажется
  }
}

export function takeFlash(key: FlashKey): string | null {
  try {
    const v = sessionStorage.getItem(PREFIX + key);
    if (v !== null) sessionStorage.removeItem(PREFIX + key);
    return v;
  } catch {
    return null;
  }
}

/** Сообщение для этой страницы (читается один раз при монтировании). */
export function useFlash(key: FlashKey): string | null {
  const [value] = useState(() => takeFlash(key));
  return value;
}
