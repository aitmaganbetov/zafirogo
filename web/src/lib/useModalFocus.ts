import { useEffect, useRef, type RefObject } from 'react';

/*
 * Клавиатура в модальных окнах, свёрстанных блоком (div.cm-modal карты клуба, окна склада — как в Blade), а не
 * <dialog>: Escape закрывает окно, Tab ходит только по окну (ловушка фокуса), при открытии фокус переходит в
 * окно, после закрытия возвращается на элемент, с которого окно открыли (UI-09). Вид окон не меняется.
 */

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

function focusable(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((el) => !el.hidden && el.closest('[hidden]') === null);
}

export function useModalFocus(ref: RefObject<HTMLElement | null>, open: boolean, onClose: () => void): void {
  const close = useRef(onClose);
  useEffect(() => {
    close.current = onClose;
  });
  useEffect(() => {
    const root = ref.current;
    if (!open || !root) return;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    // Окно само ставит фокус на своё поле (эффекты/таймеры окон) — сюда фокус переносится, только если он остался снаружи.
    const enter = setTimeout(() => {
      if (!root.contains(document.activeElement)) focusable(root)[0]?.focus();
    }, 80);
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if (e.defaultPrevented) return; // выпадающий список внутри окна закрыл себя сам
        e.preventDefault();
        close.current();
        return;
      }
      if (e.key !== 'Tab') return;
      const items = focusable(root);
      const first = items[0];
      const last = items[items.length - 1];
      if (!first || !last) {
        e.preventDefault();
        return;
      }
      const active = document.activeElement;
      if (!root.contains(active)) {
        e.preventDefault();
        (e.shiftKey ? last : first).focus();
      } else if (e.shiftKey && active === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKey);
    return () => {
      clearTimeout(enter);
      document.removeEventListener('keydown', onKey);
      // Фокус — туда, откуда открыли окно (если элемент ещё на странице).
      if (opener && opener.isConnected && (root.contains(document.activeElement) || document.activeElement === document.body)) opener.focus();
    };
  }, [ref, open]);
}
