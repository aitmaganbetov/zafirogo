package migrate

import (
	"context"
	"fmt"
	"strconv"
)

// Журнал денег платформы (platform_money_log, 0038, решение UQ-6): перенесённые оплаты счетов, начисления и
// выплаты реселлерам получают по строке журнала с теми же ключами идемпотентности, что пишет сервер
// (invoice_paid:<id счёта>, reseller_accrual:<id счёта>, reseller_payout:<id выплаты>), — журнал полон с первого
// дня, а повторный перенос (после purge или повторный платформенный шаг) строк не удваивает. meta.source = "mysql".
//
// Начисление по счёту, у которого реселлер уже удалён (reseller_id = NULL, reseller_amount осталась — BR-189),
// в журнал не пишется: его некому приписать; сумма остаётся в счёте.

// logInvoiceMoney — строки журнала по оплаченным счетам клуба (apply, после копирования таблиц).
func (r *run) logInvoiceMoney(ctx context.Context) error {
	paid, err := r.pg.Exec(ctx, `
		INSERT INTO platform_money_log (kind, idempotency_key, amount, club_id, invoice_id, reseller_id, actor_user_id, meta, created_at)
		SELECT 'invoice_paid', 'invoice_paid:' || i.id, i.amount, i.club_id, i.id, i.reseller_id, i.confirmed_by,
		       jsonb_build_object('source', 'mysql', 'months', i.months, 'computers', i.computers), i.paid_at
		FROM subscription_invoices i
		WHERE i.club_id = $1 AND i.status = 'paid'
		ORDER BY i.id
		ON CONFLICT (idempotency_key) DO NOTHING`, r.club.ID)
	if err != nil {
		return fmt.Errorf("pg: platform money log (invoices): %w", err)
	}
	accrued, err := r.pg.Exec(ctx, `
		INSERT INTO platform_money_log (kind, idempotency_key, amount, club_id, invoice_id, reseller_id, actor_user_id, meta, created_at)
		SELECT 'reseller_accrual', 'reseller_accrual:' || i.id, i.reseller_amount, i.club_id, i.id, i.reseller_id, i.confirmed_by,
		       jsonb_build_object('source', 'mysql', 'reseller_bp', i.reseller_bp), i.paid_at
		FROM subscription_invoices i
		WHERE i.club_id = $1 AND i.status = 'paid' AND i.reseller_id IS NOT NULL AND i.reseller_amount IS NOT NULL
		ORDER BY i.id
		ON CONFLICT (idempotency_key) DO NOTHING`, r.club.ID)
	if err != nil {
		return fmt.Errorf("pg: platform money log (accruals): %w", err)
	}
	if n := paid.RowsAffected() + accrued.RowsAffected(); n > 0 {
		r.rep.table("platform_money_log").Written += n
	}
	return nil
}

// compareInvoices — сверка счетов клуба: суммы (× 100 точно), проценты реселлера в базисных пунктах и журнал
// денег платформы (каждый оплаченный счёт MySQL — строка invoice_paid на ту же сумму).
func (r *run) compareInvoices(ctx context.Context) error {
	for _, c := range []struct{ name, my, pg string }{
		{"subscription_invoices.amount", "SELECT COALESCE(SUM(amount), 0) FROM subscription_invoices WHERE club_id = ?",
			"SELECT COALESCE(sum(amount), 0)::bigint FROM subscription_invoices WHERE club_id = $1"},
		{"subscription_invoices.price_per_computer", "SELECT COALESCE(SUM(price_per_computer), 0) FROM subscription_invoices WHERE club_id = ?",
			"SELECT COALESCE(sum(price_per_computer), 0)::bigint FROM subscription_invoices WHERE club_id = $1"},
		{"subscription_invoices.reseller_amount", "SELECT COALESCE(SUM(reseller_amount), 0) FROM subscription_invoices WHERE club_id = ?",
			"SELECT COALESCE(sum(reseller_amount), 0)::bigint FROM subscription_invoices WHERE club_id = $1"},
		{"журнал денег платформы: оплаты счетов", "SELECT COALESCE(SUM(amount), 0) FROM subscription_invoices WHERE club_id = ? AND status = 'paid'",
			"SELECT COALESCE(sum(amount), 0)::bigint FROM platform_money_log WHERE club_id = $1 AND kind = 'invoice_paid'"},
		{"журнал денег платформы: начисления реселлерам",
			"SELECT COALESCE(SUM(reseller_amount), 0) FROM subscription_invoices WHERE club_id = ? AND status = 'paid' AND reseller_id IS NOT NULL",
			"SELECT COALESCE(sum(amount), 0)::bigint FROM platform_money_log WHERE club_id = $1 AND kind = 'reseller_accrual'"},
	} {
		if err := r.compareMoney(ctx, c.name, c.my, c.pg); err != nil {
			return err
		}
	}
	for _, c := range []struct{ name, my, pg string }{
		// Проценты: decimal(5,2) × 100 = базисные пункты (целое в обеих базах).
		{"subscription_invoices.reseller_bp (базисные пункты)",
			"SELECT CAST(COALESCE(SUM(reseller_percent), 0) * 100 AS SIGNED) FROM subscription_invoices WHERE club_id = ?",
			"SELECT COALESCE(sum(reseller_bp), 0)::bigint FROM subscription_invoices WHERE club_id = $1"},
		{"subscription_invoices: неоплаченные", "SELECT COUNT(*) FROM subscription_invoices WHERE club_id = ? AND status = 'pending'",
			"SELECT count(*) FROM subscription_invoices WHERE club_id = $1 AND status = 'pending'"},
		{"журнал денег платформы: оплаченных счетов", "SELECT COUNT(*) FROM subscription_invoices WHERE club_id = ? AND status = 'paid'",
			"SELECT count(*) FROM platform_money_log WHERE club_id = $1 AND kind = 'invoice_paid'"},
		// Оплата броней из приложения — уже тиыны в MySQL (paid_*_cents): без × 100.
		{"computer_reservations.paid_main_cents (тиыны)", "SELECT CAST(COALESCE(SUM(paid_main_cents), 0) AS SIGNED) FROM computer_reservations WHERE club_id = ?",
			"SELECT COALESCE(sum(paid_main_cents), 0)::bigint FROM computer_reservations WHERE club_id = $1"},
		{"computer_reservations.paid_bonus_cents (тиыны)", "SELECT CAST(COALESCE(SUM(paid_bonus_cents), 0) AS SIGNED) FROM computer_reservations WHERE club_id = ?",
			"SELECT COALESCE(sum(paid_bonus_cents), 0)::bigint FROM computer_reservations WHERE club_id = $1"},
		{"computer_reservations: из приложения", "SELECT COUNT(*) FROM computer_reservations WHERE club_id = ? AND from_app = 1",
			"SELECT count(*) FROM computer_reservations WHERE club_id = $1 AND from_app"},
	} {
		if err := r.compareInt(ctx, c.name, c.my, c.pg); err != nil {
			return err
		}
	}
	return nil
}

// logPayoutMoney — строки журнала по выплатам реселлерам (платформенный шаг, apply).
func (p *platform) logPayoutMoney(ctx context.Context) error {
	ids, err := p.r.mysqlIDs(ctx, "reseller_payouts", "1 = 1", nil)
	if err != nil || len(ids) == 0 {
		return err
	}
	tag, err := p.r.pg.Exec(ctx, `
		INSERT INTO platform_money_log (kind, idempotency_key, amount, reseller_id, payout_id, actor_user_id, meta, created_at)
		SELECT 'reseller_payout', 'reseller_payout:' || o.id, o.amount, o.reseller_id, o.id, o.created_by,
		       jsonb_build_object('source', 'mysql'), COALESCE(o.created_at, now())
		FROM reseller_payouts o
		WHERE o.id = ANY($1)
		ORDER BY o.id
		ON CONFLICT (idempotency_key) DO NOTHING`, ids)
	if err != nil {
		return fmt.Errorf("pg: platform money log (payouts): %w", err)
	}
	if n := tag.RowsAffected(); n > 0 {
		p.r.rep.table("platform_money_log").Written += n
	}
	return nil
}

// verifyResellers — сверка реселлеров и выплат с MySQL: проценты (базисные пункты), суммы выплат (× 100 точно),
// журнал денег платформы. Реселлер, изменённый суперадмином zafirogo после переноса, — расхождение уровня
// diffSeverity (как остальные платформенные данные).
func (p *platform) verifyResellers(ctx context.Context) error {
	ids, err := p.r.mysqlIDs(ctx, "resellers", "1 = 1", nil)
	if err != nil {
		return err
	}
	var myBP, pgBP int64
	if err := p.r.src.conn.QueryRowContext(ctx, "SELECT CAST(COALESCE(SUM(commission_percent), 0) * 100 AS SIGNED) FROM resellers").Scan(&myBP); err != nil {
		return fmt.Errorf("mysql: resellers: %w", err)
	}
	if err := p.r.pg.QueryRow(ctx, "SELECT COALESCE(sum(commission_bp), 0)::bigint FROM resellers WHERE id = ANY($1)", ids).Scan(&pgBP); err != nil {
		return fmt.Errorf("pg: resellers: %w", err)
	}
	p.r.rep.Totals["resellers.commission_bp (базисные пункты)"] = fmt.Sprintf("MySQL %d / Postgres %d", myBP, pgBP)
	if myBP != pgBP {
		p.r.rep.add(p.diffSeverity(), CodePlatform, "resellers", "commission_bp", "", "",
			fmt.Sprintf("сумма процентов реселлеров: MySQL %d, Postgres %d (базисные пункты)", myBP, pgBP))
	}

	payoutIDs, err := p.r.mysqlIDs(ctx, "reseller_payouts", "1 = 1", nil)
	if err != nil {
		return err
	}
	var mySum string
	if err := p.r.src.conn.QueryRowContext(ctx, "SELECT COALESCE(SUM(amount), 0) FROM reseller_payouts").Scan(&mySum); err != nil {
		return fmt.Errorf("mysql: reseller_payouts: %w", err)
	}
	my, err := tiynFromDecimal(mySum)
	if err != nil {
		return fmt.Errorf("mysql: reseller_payouts: %w", err)
	}
	// Выплаты удалённого в zafirogo реселлера исчезают каскадом (BR-189), а журнал остаётся: сумма сверяется и по
	// таблице (расхождение ожидаемо после удаления — diffSeverity), и по журналу (всегда).
	var pgSum, logSum, logCount int64
	if err := p.r.pg.QueryRow(ctx, "SELECT COALESCE(sum(amount), 0)::bigint FROM reseller_payouts WHERE id = ANY($1)", payoutIDs).Scan(&pgSum); err != nil {
		return fmt.Errorf("pg: reseller_payouts: %w", err)
	}
	if err := p.r.pg.QueryRow(ctx, `SELECT COALESCE(sum(amount), 0)::bigint, count(*) FROM platform_money_log
		WHERE kind = 'reseller_payout' AND payout_id = ANY($1)`, payoutIDs).Scan(&logSum, &logCount); err != nil {
		return fmt.Errorf("pg: platform money log: %w", err)
	}
	p.r.rep.Totals["reseller_payouts.amount"] = fmt.Sprintf("MySQL %s / Postgres %s / журнал %s", tiynString(int64(my)), tiynString(pgSum), tiynString(logSum))
	if int64(my) != pgSum {
		p.r.rep.add(p.diffSeverity(), CodePlatform, "reseller_payouts", "amount", "", "",
			fmt.Sprintf("сумма выплат: MySQL %s, Postgres %s", tiynString(int64(my)), tiynString(pgSum)))
	}
	if int64(my) != logSum || logCount != int64(len(payoutIDs)) {
		p.r.rep.add(Blocker, CodeVerifyMismatch, "platform_money_log", "amount", "", "",
			fmt.Sprintf("журнал денег платформы, выплаты: MySQL %s (%s шт.), журнал %s (%d шт.)",
				tiynString(int64(my)), strconv.Itoa(len(payoutIDs)), tiynString(logSum), logCount))
	}
	return nil
}
