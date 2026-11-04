import { useQuery } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { productSearchQuery, type Product } from './api';

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
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const h = setTimeout(() => setDebounced((text ?? '').trim()), 250);
    return () => clearTimeout(h);
  }, [text]);
  const first = useQuery(productSearchQuery(''));
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
        onFocus={() => setOpen(true)}
        onChange={(e) => {
          setText(e.target.value);
          onChange(null);
          setOpen(true);
        }}
      />
      <div id={`${id}-options`} className="pos-user-options" role="listbox" hidden={!open}>
        {items.map((p) => (
          <button
            key={p.id}
            type="button"
            className="pos-user-option"
            role="option"
            aria-selected={value?.id === p.id}
            onClick={() => {
              onChange(p);
              setText(null);
              setOpen(false);
            }}
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
