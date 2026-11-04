import { useSuspenseQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatDate } from '../../lib/datetime';
import { useFlash } from '../../lib/flash';
import { formatMoney } from '../../lib/money';
import { receiptQuery } from './api';
import { RefundButton } from './RefundButton';

/*
 * Чек — перенос cash-register/receipt.blade.php (CashRegisterController::receipt(), только приход): печать —
 * window.print() и @media print в CSS. Итог — сумма операции с сервера (решение Q-019).
 */
export function ReceiptPage({ id }: { id: number }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const tz = me?.club?.timezone ?? 'UTC';
  const { data } = useSuspenseQuery(receiptQuery(id));
  const flash = useFlash('status');
  const [status, setStatus] = useState<string | null>(flash);
  const [error, setError] = useState<string | null>(null);
  const tx = data.transaction;
  useAdminPageTitle(t('Чек №{{number}}', { number: tx.id }));

  return (
    <div className="receipt-page">
      <div className="receipt-wrap">
        {status && <div className="mb-3 rounded-lg bg-green-50 p-3 text-sm text-green-700">{t(status)}</div>}
        <div className="receipt-actions">
          <a href="/cash-register">{t('В кассу')}</a>
          <button type="button" onClick={() => window.print()}>
            {t('Распечатать чек')}
          </button>
          <RefundButton receipt={data} onStatus={setStatus} onError={setError} />
        </div>
        {error && (
          <div className="mb-3 rounded-lg bg-red-50 p-3 text-sm text-red-700" role="alert">
            {t(error)}
          </div>
        )}
        <article className="receipt">
          <div className="receipt-logo">
            <strong>ZAFIRO</strong>
            <span>{t('Кассовый чек')}</span>
          </div>
          <div className="receipt-dash"></div>
          <div className="receipt-row">
            <span>{t('Чек')}</span>
            <strong>№{tx.id}</strong>
          </div>
          <div className="receipt-row">
            <span>{t('Смена')}</span>
            <strong>№{tx.shift_id}</strong>
          </div>
          <div className="receipt-row">
            <span>{t('Дата')}</span>
            <strong>{formatDate(tx.created_at, tz, 'd.m.Y H:i')}</strong>
          </div>
          <div className="receipt-dash"></div>
          {tx.items && tx.items.length > 0 ? (
            tx.items.map((item, i) => (
              <div key={i} className="receipt-item">
                <div className="receipt-item-name">{item.name}</div>
                <div className="receipt-item-detail">
                  <span>
                    {item.quantity} × {formatMoney(item.price_tiyn, 2)}
                  </span>
                  <strong>{formatMoney(item.total_tiyn, 2)} KZT</strong>
                </div>
              </div>
            ))
          ) : (
            <div className="receipt-row">
              <span>{tx.description}</span>
              <strong>{formatMoney(tx.amount_tiyn, 2)} KZT</strong>
            </div>
          )}
          <div className="receipt-dash"></div>
          <div className="receipt-total">
            <span>{t('ИТОГО')}</span>
            <span>{formatMoney(tx.amount_tiyn, 2)} KZT</span>
          </div>
          <div className="receipt-thanks">
            {t('Спасибо за покупку!')}
            <br />
            {t('Сохраняйте чек до конца обслуживания.')}
          </div>
        </article>
      </div>
    </div>
  );
}
