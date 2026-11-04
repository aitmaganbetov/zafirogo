import { useQuery, useQueryClient, useSuspenseQuery } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatDate } from '../../lib/datetime';
import { formatMoney } from '../../lib/money';
import { serverErrors } from '../auth/serverErrors';
import { closeShift, issueZNonce, markZPrinted, xReportQuery, zReportQuery, type CashReport } from './api';

/*
 * X/Z-отчёт — перенос cash-register/report.blade.php (CashRegisterController::xReport(), zReport()).
 * Z открытой смены: «Закрыть смену» активна только после печати (Q-015, RQ-7): nonce — POST …/z-report/nonce при
 * показе отчёта (в старом — GET Z-отчёта клал его в сессию), после диалога печати (afterprint) —
 * POST …/z-report/printed с nonce. Печатная вёрстка — @media print в CSS (как в старом).
 */

export function XReportPage() {
  const { data } = useSuspenseQuery(xReportQuery());
  return <Report report={data} />;
}

export function ZReportPage({ shiftId }: { shiftId: number }) {
  const { data } = useSuspenseQuery(zReportQuery(shiftId));
  return <Report report={data} />;
}

function Report({ report }: { report: CashReport }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { me } = useSession();
  const tz = me?.club?.timezone ?? 'UTC';
  const type = report.type;
  const shift = report.shift;
  const isOpen = shift.status === 'open';
  const zOpen = type === 'Z' && isOpen;
  useAdminPageTitle(t('{{type}}-отчёт', { type }));

  // Nonce печати выдаётся при показе Z-отчёта открытой смены (один на показ; повтор запроса не нужен).
  const nonce = useQuery({
    queryKey: ['cash', 'z-nonce', shift.id],
    queryFn: () => issueZNonce(shift.id),
    enabled: zOpen && me?.read_only !== true,
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
  });
  const [printed, setPrinted] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [closing, setClosing] = useState(false);
  const printRequested = useRef(false);
  const nonceValue = nonce.data?.nonce;

  useEffect(() => {
    if (!zOpen) return;
    const onAfterPrint = () => {
      if (!printRequested.current || !nonceValue) return;
      printRequested.current = false;
      markZPrinted(shift.id, nonceValue)
        .then(() => setPrinted(true))
        .catch(() => undefined); // как в старом: без отметки кнопка остаётся неактивной
    };
    window.addEventListener('afterprint', onAfterPrint);
    return () => window.removeEventListener('afterprint', onAfterPrint);
  }, [zOpen, nonceValue, shift.id]);

  const close = async () => {
    setClosing(true);
    try {
      await closeShift(shift.id);
      setError(null);
      await qc.invalidateQueries({ queryKey: ['cash'] });
    } catch (e) {
      setError(serverErrors(e).first);
    } finally {
      setClosing(false);
    }
  };

  const money = (tiyn: number) => formatMoney(tiyn, 2);
  const itemsQty = report.items.reduce((s, i) => s + i.quantity, 0);
  const formedAt = shift.closed_at ?? new Date().toISOString();

  return (
    <div className="report-page">
      <div className="report-wrap">
        {error && <div className="report-error">{t(error)}</div>}
        <div className="report-toolbar">
          <a className="report-back" href="/cash-register">
            {t('← Вернуться в кассу')}
          </a>
          <div className="report-actions">
            <button
              type="button"
              id="print-report"
              className="report-btn primary"
              onClick={() => {
                printRequested.current = true;
                window.print();
              }}
            >
              {printed ? t('Z-отчёт распечатан') : t('Распечатать {{type}}-отчёт', { type })}
            </button>
            {zOpen && (
              <form
                method="POST"
                action={`/cash-register/shifts/${shift.id}/close`}
                onSubmit={(e) => {
                  e.preventDefault();
                  void close();
                }}
              >
                <button id="close-shift" className="report-btn danger" disabled={!printed || closing || me?.read_only === true}>
                  {t('Закрыть смену')}
                </button>
              </form>
            )}
          </div>
        </div>
        <article className="report-paper">
          <header className="report-head">
            <div>
              <span className="report-type">{type} REPORT</span>
              <h1>{t('{{type}}-отчёт', { type })}</h1>
              <p>{type === 'X' ? t('Промежуточный отчёт без закрытия смены') : t('Итоговый отчёт кассовой смены')}</p>
            </div>
            <span className="report-state">{isOpen ? t('Смена открыта') : t('Смена закрыта')}</span>
          </header>
          <div className="report-body">
            <div className="report-info">
              <div>
                <span>{t('Номер смены')}</span>
                <strong>#{shift.id}</strong>
              </div>
              <div>
                <span>{t('Открыта')}</span>
                <strong>{formatDate(shift.opened_at, tz, 'd.m.Y H:i')}</strong>
              </div>
              <div>
                <span>{shift.closed_at ? t('Закрыта') : t('Сформирован')}</span>
                <strong>{formatDate(formedAt, tz, 'd.m.Y H:i')}</strong>
              </div>
              <div>
                <span>{t('Кассир')}</span>
                <strong>{report.cashier_name || '—'}</strong>
              </div>
              {report.closed_by_name && (
                <div>
                  <span>{t('Закрыл смену')}</span>
                  <strong>{report.closed_by_name}</strong>
                </div>
              )}
              <div>
                <span>{t('Оператор')}</span>
                <strong>{report.operator_name}</strong>
              </div>
            </div>
            <div className="report-metrics">
              <div className="report-metric">
                <span>{t('Начальный остаток')}</span>
                <strong>{money(shift.opening_balance_tiyn)} KZT</strong>
              </div>
              <div className="report-metric income">
                <span>{t('Приход')}</span>
                <strong>+ {money(report.totals.income_tiyn)} KZT</strong>
              </div>
              <div className="report-metric expense">
                <span>{t('Расход')}</span>
                <strong>− {money(report.totals.expense_tiyn)} KZT</strong>
              </div>
            </div>
            <div className="report-total">
              <span>{t('Итог в кассе')}</span>
              <strong>{money(report.totals.balance_tiyn)} KZT</strong>
            </div>
            <section className="report-section">
              <h2>{t('Приход по видам')}</h2>
              <table className="report-table">
                <thead>
                  <tr>
                    <th>{t('Вид')}</th>
                    <th className="num">{t('Операций')}</th>
                    <th className="num">{t('Сумма')}</th>
                  </tr>
                </thead>
                <tbody>
                  {report.kinds.map((k) => (
                    <tr key={k.label}>
                      <td>{t(k.label)}</td>
                      <td className="num">{k.count}</td>
                      <td className="num">{money(k.amount_tiyn)} KZT</td>
                    </tr>
                  ))}
                </tbody>
                {report.refunds.count > 0 && (
                  <tfoot>
                    <tr>
                      <td>{t('Возвраты (расход)')}</td>
                      <td className="num">{report.refunds.count}</td>
                      <td className="num">− {money(report.refunds.amount_tiyn)} KZT</td>
                    </tr>
                  </tfoot>
                )}
              </table>
            </section>
            <section className="report-section">
              <h2>{t('Продано товаров')}</h2>
              {report.items.length === 0 ? (
                <p className="report-empty">{t('За смену товары не продавались.')}</p>
              ) : (
                <table className="report-table">
                  <thead>
                    <tr>
                      <th>{t('Товар')}</th>
                      <th className="num">{t('Кол-во')}</th>
                      <th className="num">{t('Остаток')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {report.items.map((i) => (
                      <tr key={i.name}>
                        <td>{i.name}</td>
                        <td className="num">{i.quantity}</td>
                        <td className="num">{i.stock ?? '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                  <tfoot>
                    <tr>
                      <td>{t('Всего')}</td>
                      <td className="num">{itemsQty}</td>
                      <td></td>
                    </tr>
                    {report.discount_tiyn > 0 && (
                      <tr>
                        <td colSpan={2}>{t('Скидки')}</td>
                        <td className="num">− {money(report.discount_tiyn)} KZT</td>
                      </tr>
                    )}
                  </tfoot>
                </table>
              )}
            </section>
            {report.staff.length > 0 && (
              <section className="report-section">
                <h2>{t('Сотрудники')}</h2>
                <table className="report-table">
                  <thead>
                    <tr>
                      <th>{t('Сотрудник')}</th>
                      <th className="num">{t('Операций')}</th>
                      <th className="num">{t('Приход')}</th>
                      <th className="num">{t('Расход')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {report.staff.map((p) => (
                      <tr key={p.user_id}>
                        <td>{p.name}</td>
                        <td className="num">{p.count}</td>
                        <td className="num">{money(p.income_tiyn)} KZT</td>
                        <td className="num">{money(p.expense_tiyn)} KZT</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </section>
            )}
            {zOpen && (
              <div className="report-note">
                {t('Смена останется открытой, пока Z-отчёт не будет отправлен на печать. После завершения диалога печати кнопка закрытия станет активной.')}
              </div>
            )}
          </div>
        </article>
      </div>
    </div>
  );
}
