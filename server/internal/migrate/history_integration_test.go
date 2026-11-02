//go:build integration

package migrate_test

import (
	"context"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate/migratetest"
)

func historyDiffs(r *migrate.Report) map[int64][2]int64 {
	out := map[int64][2]int64{}
	for _, d := range r.BalanceHistory {
		out[d.UserID] = [2]int64{d.DiffMain, d.DiffBonus}
	}
	return out
}

// Q-005 (S5-M3-3): пересчёт балансов по истории старой базы. Движения, записанные старым кодом (касса, возврат
// пополнения, покупки, возврат времени, заказ и его отмена, перевод другу, пополнение и списание владельцем), объясняют
// изменение остатка — расхождение клиента не меняется; изменение остатка без истории (искусственное) — видно до тиына.
func TestBalanceHistory(t *testing.T) {
	ctx := context.Background()
	my, _ := migratetest.StartMySQL(t)
	pgdb := dbtest.New(t)
	now := time.Now().UTC()
	at := now.Format(time.DateTime)
	opts := migrate.Options{Club: "zafiro-arena", IDReserve: idReserve, Now: now}

	base, err := migrate.Plan(ctx, my, pgdb.Owner, opts)
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, reportText(base))
	}
	t.Logf("демо-данные (баланс сидера записан полем, история неполная):\n%s", reportText(base))
	before := historyDiffs(base)

	user := `App\Models\User`
	logAct := func(event string, userID int64, subjectType any, subjectID any, details string) {
		execMy(t, my, `INSERT INTO activity_logs (event, description, user_id, subject_type, subject_id, details, ip_address, created_at, updated_at, club_id)
			VALUES (?, ?, ?, ?, ?, ?, '127.0.0.1', ?, ?, 1)`, event, event, userID, subjectType, subjectID, details, at, at)
	}
	cashIn := func(amount, receipt string) int64 {
		execMy(t, my, `INSERT INTO cash_transactions (cash_shift_id, user_id, type, amount, description, receipt_data, created_at, updated_at, club_id)
			VALUES (3, 4, 'income', ?, 'Пополнение', ?, ?, ?, 1)`, amount, receipt, at, at)
		return scalarMy[int64](t, my, "SELECT MAX(id) FROM cash_transactions")
	}
	balance := func(uid int64, main, bonus string) {
		execMy(t, my, "UPDATE member_balances SET balance = balance + ?, bonus_balance = bonus_balance + ? WHERE club_id = 1 AND user_id = ?", main, bonus, uid)
	}

	// Клиент 10: пополнение на кассе 50 + бонус 5; второе пополнение 20 + бонус 2 и его возврат (бонус забран 1.50);
	// покупка тарифа 10 + 2 бонусами; пополнение владельцем без кассы 7; перевод другу 3.
	cashIn("50.00", `{"top_up": {"user_id": 10, "balance_type": "balance", "bonus": 5}}`)
	balance(10, "50.00", "5.00")
	topUp := cashIn("20.00", `{"top_up": {"user_id": 10, "balance_type": "balance", "bonus": 2}}`)
	balance(10, "20.00", "2.00")
	execMy(t, my, `INSERT INTO cash_transactions (cash_shift_id, refund_of_id, user_id, type, amount, description, created_at, updated_at, club_id)
		VALUES (3, ?, 4, 'expense', '20.00', 'Возврат', ?, ?, 1)`, topUp, at, at)
	refund := scalarMy[int64](t, my, "SELECT MAX(id) FROM cash_transactions")
	logAct("cash.refund", 4, `App\Models\CashTransaction`, refund, `{"amount": 20, "receipt": 1, "bonus_taken_back": 1.5}`)
	balance(10, "-20.00", "-1.50")
	logAct("tariff.purchased", 10, `App\Models\Tariff`, 1, `{"amount": "12.00", "main_balance_amount": "10.00", "bonus_balance_amount": "2.00"}`)
	balance(10, "-10.00", "-2.00")
	logAct("user.balance_topped_up", 2, user, 10, `{"amount": 7, "balance_type": "основной баланс", "without_cash": true}`)
	balance(10, "7.00", "0")
	logAct("balance.transferred", 10, user, 11, `{"amount": "3.00", "to_user_id": 11}`)
	balance(10, "-3.00", "0")
	balance(11, "3.00", "0")
	// Клиент 11: возврат за время 1.50 + 0.50, заказ из агента 4 и его отмена, списание владельцем 0.25 с бонусного.
	logAct("time.refunded", 4, user, 11, `{"main_balance_amount": "1.50", "bonus_balance_amount": "0.50"}`)
	balance(11, "1.50", "0.50")
	logAct("shop.purchased", 11, `App\Models\ClubComputer`, 1, `{"amount": "4.00"}`)
	logAct("shop.order_cancelled", 4, user, 11, `{"amount": "4.00", "order_id": 1}`) //nolint:misspell // код события старого журнала
	logAct("user.balance_written_off", 2, user, 11, `{"amount": 0.25, "balance_field": "bonus_balance", "without_cash": true}`)
	balance(11, "0", "-0.25")
	// Клиент 12: остаток изменён без истории — расхождение.
	balance(12, "1.23", "0")

	after, err := migrate.Plan(ctx, my, pgdb.Owner, opts)
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, reportText(after))
	}
	got := historyDiffs(after)
	for _, uid := range []int64{10, 11} {
		if got[uid] != before[uid] {
			t.Errorf("клиент %d: расхождение %v → %v (движения истории не учтены)", uid, before[uid], got[uid])
		}
	}
	if want := [2]int64{before[12][0] + 123, before[12][1]}; got[12] != want {
		t.Errorf("клиент 12: расхождение %v, ожидали %v", got[12], want)
	}
	requireFinding(t, after, migrate.Warning, migrate.CodeBalanceHistory)

	// Тот же отчёт в apply и verify; расхождение истории — не блокер (переносится остаток, Q-004).
	applied, err := migrate.Apply(ctx, my, pgdb.Owner, opts)
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, reportText(applied))
	}
	verified, err := migrate.Verify(ctx, my, pgdb.Owner, opts)
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, reportText(verified))
	}
	for name, r := range map[string]*migrate.Report{"apply": applied, "verify": verified} {
		if d := historyDiffs(r); d[12] != got[12] || len(d) != len(got) {
			t.Errorf("%s: история %v, plan %v", name, d, got)
		}
	}
}
