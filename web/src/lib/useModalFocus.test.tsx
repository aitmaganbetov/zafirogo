import { act, fireEvent, render } from '@testing-library/react';
import { useRef, useState, type ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useModalFocus } from './useModalFocus';

/* UI-09: окна карты клуба и склада (блок, не <dialog>) — Escape, ловушка фокуса, возврат фокуса. */

function Screen({ extra }: { extra?: ReactNode }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useModalFocus(ref, open, () => setOpen(false));
  return (
    <>
      <a href="/menu" id="menu">меню</a>
      <button id="opener" onClick={() => setOpen(true)}>открыть</button>
      <div ref={ref} id="modal" hidden={!open}>
        <button id="close">×</button>
        <input id="field" />
        <input type="hidden" name="h" />
        <button id="save">Сохранить</button>
        {extra}
      </div>
    </>
  );
}

describe('useModalFocus', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());
  const el = (id: string) => document.getElementById(id) as HTMLElement;
  const key = (k: string, shiftKey = false) => fireEvent.keyDown(document.activeElement ?? document.body, { key: k, shiftKey });

  function open(extra?: ReactNode) {
    render(<Screen extra={extra} />);
    el('opener').focus();
    fireEvent.click(el('opener'));
    act(() => void vi.advanceTimersByTime(100));
  }

  it('при открытии фокус переходит в окно, Tab не выходит из окна', () => {
    open();
    expect(document.activeElement).toBe(el('close'));
    el('save').focus();
    key('Tab');
    expect(document.activeElement).toBe(el('close'));
    key('Tab', true);
    expect(document.activeElement).toBe(el('save'));
    // Фокус оказался под окном (щелчок по странице) — Tab возвращает его в окно.
    el('menu').focus();
    key('Tab');
    expect(document.activeElement).toBe(el('close'));
  });

  // Ревью 22.11.2026: скрытые CSS элементы (вкладка, свёрнутый блок) в ловушку фокуса не входят.
  it('элементы, скрытые CSS, Tab пропускает', () => {
    open(
      <>
        <style>{'.tw-hidden { display: none; }'}</style>
        <div style={{ display: 'none' }}><button id="in-none">в скрытом блоке</button></div>
        <div className="tw-hidden"><button id="in-class">в блоке с классом</button></div>
        <button id="invisible" style={{ visibility: 'hidden' }}>невидимая</button>
        <div hidden><button id="in-hidden">в hidden</button></div>
      </>,
    );
    // Последний видимый — «Сохранить»: с него Tab уходит на первый, Shift+Tab с первого — на него.
    el('save').focus();
    key('Tab');
    expect(document.activeElement).toBe(el('close'));
    key('Tab', true);
    expect(document.activeElement).toBe(el('save'));
  });

  it('Escape закрывает окно и возвращает фокус на кнопку, которой его открыли', () => {
    open();
    el('field').focus();
    key('Escape');
    expect(el('modal').hidden).toBe(true);
    expect(document.activeElement).toBe(el('opener'));
  });

  it('Escape, уже обработанный внутри окна (выпадающий список), окно не закрывает; закрытое окно клавиши не слушает', () => {
    open();
    const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
    ev.preventDefault();
    act(() => void el('field').dispatchEvent(ev));
    expect(el('modal').hidden).toBe(false);
    key('Escape');
    expect(el('modal').hidden).toBe(true);
    el('menu').focus();
    key('Tab');
    expect(document.activeElement).toBe(el('menu'));
  });
});
