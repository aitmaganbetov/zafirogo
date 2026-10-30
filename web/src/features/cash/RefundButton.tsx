import { useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { ConfirmForm } from '../../components/ConfirmForm';
import { formatMoney } from '../../lib/money';
import { serverErrors } from '../auth/serverErrors';
import { refundReceipt, type CashReceipt } from './api';

/*
 * Возврат по чеку — перенос cash-register/refund-button.blade.php: «Возвращено», если возврат уже был; кнопка — если
 * есть право (cash.delete), операция возвращается (продажа или пополнение) и смена открыта.
 */
export function RefundButton({ receipt, onStatus, onError }: { receipt: CashReceipt; onStatus: (s: string) => void; onError: (s: string | null) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { me } = useSession();
  const tx = receipt.transaction;
  if (receipt.refund) {
    return <span className="text-sm font-medium text-gray-500 dark:text-gray-400">{t('Возвращено')}</span>;
  }
  if (!receipt.can_refund || !receipt.refundable || !receipt.shift_open) return null;
  const message = t('Вернуть {{amount}} KZT по чеку №{{number}}? Деньги выдаются из кассы.', { amount: formatMoney(tx.amount_tiyn, 2), number: tx.id });
  return (
    <ConfirmForm
      message={message}
      className="inline"
      onConfirm={() => {
        refundReceipt(tx.id)
          .then(async (res) => {
            onError(null);
            onStatus(res.message);
            await qc.invalidateQueries({ queryKey: ['cash'] });
          })
          .catch((e: unknown) => onError(serverErrors(e).fields.refund ?? serverErrors(e).first));
      }}
    >
      <button type="submit" className="text-sm font-medium text-red-600 hover:underline dark:text-red-400" disabled={me?.read_only === true}>
        {t('Возврат')}
      </button>
    </ConfirmForm>
  );
}
