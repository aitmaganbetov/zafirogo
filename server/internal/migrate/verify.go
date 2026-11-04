package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// compare — сверка клуба MySQL ↔ Postgres (в apply — до коммита, в той же транзакции). Расхождения — блокеры
// CodeVerifyMismatch (Q-005: расхождение в 1 тиын недопустимо).
func (r *run) compare(ctx context.Context) error {
	tables := coreTables()
	usersCond, usersArgs := usersWhere(tables, r.o)
	var planWhere string
	var planArgs []any
	for i := range tables {
		switch tables[i].Name {
		case "users":
			tables[i].Where = func(*Options) (string, []any) { return usersCond, usersArgs }
		case "saas_plans":
			planWhere, planArgs = tables[i].Where(r.o)
		}
	}
	userIDs, err := r.mysqlIDs(ctx, "users", usersCond, bindClub(usersArgs, r.club.ID))
	if err != nil {
		return err
	}
	planIDs, err := r.mysqlIDs(ctx, "saas_plans", planWhere, bindClub(planArgs, r.club.ID))
	if err != nil {
		return err
	}

	// 1. Число строк по таблицам.
	for _, t := range tables {
		where, args := t.Where(r.o)
		my, err := r.src.count(ctx, t.Name, where, bindClub(args, r.club.ID))
		if err != nil {
			return err
		}
		ids := userIDs
		if t.Name == "saas_plans" {
			ids = planIDs
		}
		cond := t.PGWhere
		if cond == "" {
			cond = "club_id = {club}"
		}
		cond, pgArgs := pgWhere(cond, r.club.ID, ids, r.o.retentionCutoff())
		var pgCount int64
		if err := r.pg.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", pgx.Identifier{t.Name}.Sanitize(), cond), pgArgs...).
			Scan(&pgCount); err != nil {
			return fmt.Errorf("pg: count %s: %w", t.Name, err)
		}
		stat := r.rep.table(t.Name)
		stat.Source, stat.Target = my, pgCount
		if my != pgCount {
			r.rep.add(Blocker, CodeVerifyMismatch, t.Name, "", "", "", fmt.Sprintf("строк: MySQL %d, Postgres %d", my, pgCount))
		}
	}

	// 2. Балансы клиентов (основной, бонусный) — по каждому клиенту и суммой; ledger = проекция.
	if err := r.compareBalances(ctx); err != nil {
		return err
	}
	// 3. total_spent (лояльность), остатки склада, суммы кассы, число ПК.
	for _, c := range []struct {
		name, my, pg string
	}{
		{"club_memberships.total_spent", "SELECT COALESCE(SUM(total_spent), 0) FROM club_memberships WHERE club_id = ?",
			"SELECT COALESCE(sum(total_spent), 0)::bigint FROM club_memberships WHERE club_id = $1"},
		{"cash_transactions income", "SELECT COALESCE(SUM(amount), 0) FROM cash_transactions WHERE club_id = ? AND type = 'income'",
			"SELECT COALESCE(sum(amount), 0)::bigint FROM cash_transactions WHERE club_id = $1 AND type = 'income'"},
		{"cash_transactions expense", "SELECT COALESCE(SUM(amount), 0) FROM cash_transactions WHERE club_id = ? AND type = 'expense'",
			"SELECT COALESCE(sum(amount), 0)::bigint FROM cash_transactions WHERE club_id = $1 AND type = 'expense'"},
	} {
		if err := r.compareMoney(ctx, c.name, c.my, c.pg); err != nil {
			return err
		}
	}
	for _, c := range []struct {
		name, my, pg string
	}{
		{"products.quantity (склад)", "SELECT COALESCE(SUM(quantity), 0) FROM products WHERE club_id = ?",
			"SELECT COALESCE(sum(quantity), 0)::bigint FROM products WHERE club_id = $1"},
		{"club_computers (ПК)", "SELECT COUNT(*) FROM club_computers WHERE club_id = ?",
			"SELECT count(*) FROM club_computers WHERE club_id = $1"},
	} {
		if err := r.compareInt(ctx, c.name, c.my, c.pg); err != nil {
			return err
		}
	}
	if err := r.compareProducts(ctx); err != nil {
		return err
	}
	if err := r.compareDrawer(ctx); err != nil {
		return err
	}
	// Q-005: остаток против истории старой базы — предупреждение, не расхождение сверки.
	began := time.Now()
	if err := r.balanceHistory(ctx); err != nil {
		return err
	}
	r.rep.timed("Q-005: история балансов", began)
	for _, ext := range r.o.Extensions {
		began := time.Now()
		if err := ext.Verify(ctx, r.env()); err != nil {
			return fmt.Errorf("extension %s: %w", ext.Name(), err)
		}
		r.rep.timed("сверка: "+ext.Name(), began)
	}
	return nil
}

func (r *run) mysqlIDs(ctx context.Context, tableName, where string, args []any) ([]int64, error) {
	list, err := r.src.strings1(ctx, fmt.Sprintf("SELECT id FROM %s WHERE %s", quoteMy(tableName), where), args...)
	if err != nil {
		return nil, fmt.Errorf("mysql: ids of %s: %w", tableName, err)
	}
	out := make([]int64, 0, len(list))
	for _, s := range list {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

func (r *run) check(name string, my, pg int64, format func(int64) string) {
	r.rep.Totals[name] = fmt.Sprintf("MySQL %s / Postgres %s", format(my), format(pg))
	if my != pg {
		r.rep.add(Blocker, CodeVerifyMismatch, "", "", name, "", fmt.Sprintf("расхождение: MySQL %s, Postgres %s", format(my), format(pg)))
	}
}

func tiynString(v int64) string { return money.Tiyn(v).Tenge() }

// compareMoney — сумма decimal тенге в MySQL (× 100 точно) против тиынов в Postgres.
func (r *run) compareMoney(ctx context.Context, name, myQuery, pgQuery string) error {
	var mySum string
	if err := r.src.conn.QueryRowContext(ctx, myQuery, r.club.ID).Scan(&mySum); err != nil {
		return fmt.Errorf("mysql: %s: %w", name, err)
	}
	my, err := tiynFromDecimal(mySum)
	if err != nil {
		return fmt.Errorf("mysql: %s: %w", name, err)
	}
	var pg int64
	if err := r.pg.QueryRow(ctx, pgQuery, r.club.ID).Scan(&pg); err != nil {
		return fmt.Errorf("pg: %s: %w", name, err)
	}
	r.check(name, int64(my), pg, tiynString)
	return nil
}

func (r *run) compareInt(ctx context.Context, name, myQuery, pgQuery string) error {
	var my, pg int64
	if err := r.src.conn.QueryRowContext(ctx, myQuery, r.club.ID).Scan(&my); err != nil {
		return fmt.Errorf("mysql: %s: %w", name, err)
	}
	if err := r.pg.QueryRow(ctx, pgQuery, r.club.ID).Scan(&pg); err != nil {
		return fmt.Errorf("pg: %s: %w", name, err)
	}
	r.check(name, my, pg, func(v int64) string { return strconv.FormatInt(v, 10) })
	return nil
}

// compareBalances — балансы каждого клиента: MySQL × 100 = member_balances = сумма проводок ledger по его счетам.
func (r *run) compareBalances(ctx context.Context) error {
	type pair struct{ main, bonus int64 }
	my := map[int64]pair{}
	rows, err := r.src.conn.QueryContext(ctx, "SELECT user_id, balance, bonus_balance FROM member_balances WHERE club_id = ?", r.club.ID)
	if err != nil {
		return fmt.Errorf("mysql: balances: %w", err)
	}
	defer rows.Close()
	var myMain, myBonus int64
	for rows.Next() {
		var uid int64
		var main, bonus string
		if err := rows.Scan(&uid, &main, &bonus); err != nil {
			return err
		}
		m, err := tiynFromDecimal(main)
		if err != nil {
			return err
		}
		b, err := tiynFromDecimal(bonus)
		if err != nil {
			return err
		}
		my[uid] = pair{int64(m), int64(b)}
		myMain += int64(m)
		myBonus += int64(b)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	pgRows, err := r.pg.Query(ctx, `
		SELECT mb.user_id, mb.balance, mb.bonus_balance,
		       COALESCE(sum(e.amount) FILTER (WHERE a.kind = 'member_main'), 0)::bigint,
		       COALESCE(sum(e.amount) FILTER (WHERE a.kind = 'member_bonus'), 0)::bigint
		FROM member_balances mb
		LEFT JOIN ledger_accounts a ON a.club_id = mb.club_id AND a.user_id = mb.user_id AND a.kind IN ('member_main', 'member_bonus')
		LEFT JOIN ledger_entries e ON e.club_id = a.club_id AND e.account_id = a.id
		WHERE mb.club_id = $1
		GROUP BY mb.user_id, mb.balance, mb.bonus_balance`, r.club.ID)
	if err != nil {
		return fmt.Errorf("pg: balances: %w", err)
	}
	type pgBalance struct {
		UserID, Main, Bonus, LedgerMain, LedgerBonus int64
	}
	list, err := pgx.CollectRows(pgRows, pgx.RowToStructByPos[pgBalance])
	if err != nil {
		return fmt.Errorf("pg: balances: %w", err)
	}
	var pgMain, pgBonus int64
	seen := map[int64]bool{}
	for _, b := range list {
		seen[b.UserID] = true
		pgMain += b.Main
		pgBonus += b.Bonus
		uid := strconv.FormatInt(b.UserID, 10)
		if m, ok := my[b.UserID]; !ok || m.main != b.Main || m.bonus != b.Bonus {
			r.rep.add(Blocker, CodeVerifyMismatch, "member_balances", "", "", uid,
				fmt.Sprintf("баланс клиента: MySQL %s/%s, Postgres %s/%s", tiynString(m.main), tiynString(m.bonus),
					tiynString(b.Main), tiynString(b.Bonus)))
		}
		if b.LedgerMain != b.Main || b.LedgerBonus != b.Bonus {
			r.rep.add(Blocker, CodeVerifyMismatch, "ledger_entries", "", "", uid,
				fmt.Sprintf("проекция member_balances ≠ сумма проводок ledger (%s/%s ≠ %s/%s)",
					tiynString(b.Main), tiynString(b.Bonus), tiynString(b.LedgerMain), tiynString(b.LedgerBonus)))
		}
	}
	for uid := range my {
		if !seen[uid] {
			r.rep.add(Blocker, CodeVerifyMismatch, "member_balances", "", "", strconv.FormatInt(uid, 10), "нет строки баланса в Postgres")
		}
	}
	r.check("member_balances.balance (основной)", myMain, pgMain, tiynString)
	r.check("member_balances.bonus_balance (бонусный)", myBonus, pgBonus, tiynString)
	return nil
}

// compareProducts — остаток каждого товара (склад не «сходится суммой» случайно).
func (r *run) compareProducts(ctx context.Context) error {
	my := map[int64]int64{}
	rows, err := r.src.conn.QueryContext(ctx, "SELECT id, quantity FROM products WHERE club_id = ?", r.club.ID)
	if err != nil {
		return fmt.Errorf("mysql: products: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, q int64
		if err := rows.Scan(&id, &q); err != nil {
			return err
		}
		my[id] = q
	}
	if err := rows.Err(); err != nil {
		return err
	}
	pgRows, err := r.pg.Query(ctx, "SELECT id, quantity FROM products WHERE club_id = $1", r.club.ID)
	if err != nil {
		return fmt.Errorf("pg: products: %w", err)
	}
	type idQty struct{ ID, Qty int64 }
	list, err := pgx.CollectRows(pgRows, pgx.RowToStructByPos[idQty])
	if err != nil {
		return err
	}
	for _, p := range list {
		if q, ok := my[p.ID]; ok && q != p.Qty {
			r.rep.add(Blocker, CodeVerifyMismatch, "products", "quantity", "", strconv.FormatInt(p.ID, 10),
				fmt.Sprintf("остаток товара: MySQL %d, Postgres %d", q, p.Qty))
		}
	}
	return nil
}

// compareDrawer — остаток кассы: открытая смена по документам (opening + приходы − расходы) в обеих базах и
// остаток счёта cash_drawer в ledger (входящий остаток переноса).
func (r *run) compareDrawer(ctx context.Context) error {
	expected, err := r.expectedDrawer(ctx)
	if err != nil {
		return err
	}
	if expected.ShiftID != 0 {
		var pgBalance int64
		err := r.pg.QueryRow(ctx, `
			SELECT s.opening_balance
			       + COALESCE(sum(t.amount) FILTER (WHERE t.type = 'income'), 0)
			       - COALESCE(sum(t.amount) FILTER (WHERE t.type = 'expense'), 0)
			FROM cash_shifts s
			LEFT JOIN cash_transactions t ON t.club_id = s.club_id AND t.cash_shift_id = s.id
			WHERE s.club_id = $1 AND s.id = $2 AND s.status = 'open'
			GROUP BY s.id, s.opening_balance`, r.club.ID, expected.ShiftID).Scan(&pgBalance)
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
			r.rep.add(Blocker, CodeVerifyMismatch, "cash_shifts", "", "", strconv.FormatInt(expected.ShiftID, 10), "нет открытой смены в Postgres")
			return nil
		}
		if err != nil {
			return fmt.Errorf("pg: open shift: %w", err)
		}
		r.check("касса: остаток открытой смены", int64(expected.Amount), pgBalance, tiynString)
	}
	var drawer int64
	if err := r.pg.QueryRow(ctx, `
		SELECT COALESCE(-sum(e.amount), 0)::bigint
		FROM ledger_accounts a JOIN ledger_entries e ON e.club_id = a.club_id AND e.account_id = a.id
		WHERE a.club_id = $1 AND a.kind = 'cash_drawer' AND a.user_id IS NULL`, r.club.ID).Scan(&drawer); err != nil {
		return fmt.Errorf("pg: cash drawer: %w", err)
	}
	r.check("касса: остаток счёта cash_drawer (ledger)", int64(expected.Amount), drawer, tiynString)
	return nil
}
