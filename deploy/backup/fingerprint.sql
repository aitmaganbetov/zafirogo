-- Отпечаток денежного состояния базы на момент запуска: по клубам — число операций ledger и проводок,
-- сумма балансов и бонусов клиентов, остатки счетов клуба по видам, число кассовых операций.
-- Учения сравнивают отпечаток исходной базы в момент T с отпечатком копии, восстановленной PITR на T:
-- совпадение до тиына = восстановлено ровно состояние на T (ни одной лишней/потерянной операции).
--
--   psql -X -At -f fingerprint.sql
\set QUIET on
SELECT format('club=%s tx=%s entries=%s', t.club_id, t.n, (SELECT count(*) FROM ledger_entries e WHERE e.club_id = t.club_id))
FROM (SELECT club_id, count(*) AS n FROM ledger_transactions GROUP BY club_id) t
ORDER BY t.club_id;
SELECT format('club=%s members=%s balance=%s bonus=%s', club_id, count(*), sum(balance), sum(bonus_balance))
FROM member_balances GROUP BY club_id ORDER BY club_id;
SELECT format('club=%s account=%s sum=%s', a.club_id, a.kind, sum(e.amount))
FROM ledger_accounts a JOIN ledger_entries e ON e.club_id = a.club_id AND e.account_id = a.id
WHERE a.user_id IS NULL
GROUP BY a.club_id, a.kind ORDER BY a.club_id, a.kind;
SELECT format('club=%s cash_transactions=%s products=%s', c.id,
              (SELECT count(*) FROM cash_transactions x WHERE x.club_id = c.id),
              (SELECT count(*) FROM products p WHERE p.club_id = c.id))
FROM clubs c ORDER BY c.id;
