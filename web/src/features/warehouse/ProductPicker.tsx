import { useQuery } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { productSearchQuery, productsPageQuery, type Product } from './api';

/*
 * Выбор товара поиском — решение Q-070 (вместо <select> со всеми товарами WarehouseController): поле с классами
 * старого select, список — как поиск клиента в бронях (pos-user-picker, задержка 250 мс). Как у select, по умолчанию
 * выбран первый товар по имени.
 */
export function ProductPicker({
  id,
  className,
  label,
  value,
  onChange,
}: {
  id: string;
  className: string;
  label: (p: Product) => string;
  value: Product | null;
  onChange: (p: Product | null) => void;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState<string | null>(null);
  const [debounced, setDebounced] = useState('');
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const h = setTimeout(() => setDebounced((text ?? '').trim()), 250);
    return () => clearTimeout(h);
  }, [text]);
  // «Первый товар» — из первой страницы каталога, которую экран склада уже загрузил (тот же маршрут и порядок,
  // общий ключ кэша) — без отдельного запроса при открытии (FE-14).
  const first = useQuery(productsPageQuery(1));
  const results = useQuery({ ...productSearchQuery(debounced), enabled: open });
  // <select> без выбора показывает первый вариант.
  const firstItem = first.data?.items[0];
  useEffect(() => {
    if (value === null && text === null && firstItem) onChange(firstItem);
  }, [value, text, firstItem, onChange]);
  useEffect(() => {
    const onDoc = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('click', onDoc);
    return () => document.removeEventListener('click', onDoc);
  }, []);
  const items = results.data?.items ?? [];
  const shown = text ?? (value ? label(value) : '');
  const pick = (p: Product) => {
    onChange(p);
    setText(null);
    setOpen(false);
    setActive(-1);
  };
  return (
    <div className="pos-user-picker" ref={box}>
      <input
        id={id}
        type="search"
        role="combobox"
        aria-autocomplete="list"
        aria-controls={`${id}-options`}
        aria-expanded={open}
        autoComplete="off"
        required
        value={shown}
        className={className}
        aria-activedescendant={open && active >= 0 && items[active] ? `${id}-option-${items[active].id}` : undefined}
        title={shown}
        onFocus={() => setOpen(true)}
        onChange={(e) => {
          setText(e.target.value);
          onChange(null);
          setOpen(true);
          setActive(-1);
        }}
        onKeyDown={(e) => {
          // Как у <select> и combobox брони: Esc закрывает список, стрелки и Enter выбирают (UI-10).
          if (e.key === 'Escape') {
            if (!open) return;
            e.preventDefault();
            e.stopPropagation();
            setOpen(false);
            setActive(-1);
          } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
            e.preventDefault();
            if (!open) {
              setOpen(true);
              return;
            }
            if (items.length === 0) return;
            const step = e.key === 'ArrowDown' ? 1 : -1;
            setActive((i) => (i + step + items.length) % items.length);
          } else if (e.key === 'Enter' && open && active >= 0) {
            const p = items[active];
            if (!p) return;
            e.preventDefault();
            pick(p);
          }
        }}
      />
      <div id={`${id}-options`} className="pos-user-options" role="listbox" hidden={!open}>
        {items.map((p) => (
          <button
            key={p.id}
            type="button"
            className="pos-user-option"
            role="option"
            id={`${id}-option-${p.id}`}
            aria-selected={active >= 0 ? items[active]?.id === p.id : value?.id === p.id}
            onClick={() => pick(p)}
          >
            <span>
              <strong>{label(p)}</strong>
            </span>
          </button>
        ))}
        <p className="pos-user-no-results" hidden={items.length > 0 || !results.isSuccess}>
          {t('Товары не найдены.')}
        </p>
      </div>
    </div>
  );
}
