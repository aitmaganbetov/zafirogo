import { describe, expect, it, vi } from 'vitest';
import { registerServiceWorker } from './register';

function fakeWindow(opts: { sw?: boolean; secure?: boolean }) {
  const register = vi.fn(() => Promise.resolve({}));
  const listeners: Record<string, () => void> = {};
  const win = {
    navigator: opts.sw === false ? {} : { serviceWorker: { register } },
    isSecureContext: opts.secure !== false,
    addEventListener: (type: string, fn: () => void) => {
      listeners[type] = fn;
    },
  } as unknown as Window;
  return { win, register, listeners };
}

describe('registerServiceWorker', () => {
  it('регистрирует /sw.js после load в prod-сборке', () => {
    const { win, register, listeners } = fakeWindow({});
    expect(registerServiceWorker(true, win)).toBe(true);
    expect(register).not.toHaveBeenCalled();
    listeners.load?.();
    expect(register).toHaveBeenCalledWith('/sw.js');
  });

  it('не регистрирует в dev, без поддержки и вне защищённого контекста', () => {
    for (const [enabled, opts] of [[false, {}], [true, { sw: false }], [true, { secure: false }]] as const) {
      const { win, listeners } = fakeWindow(opts);
      expect(registerServiceWorker(enabled, win)).toBe(false);
      expect(listeners.load).toBeUndefined();
    }
  });
});
