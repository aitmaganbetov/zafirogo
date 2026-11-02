package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Q-005 (S5-M2-О1 → S5-M3-3): пересчёт балансов клиентов из истории старой базы — «сумма движений» против остатка
// member_balances. Старый код менял баланс обновлением поля (increment/update), отдельного журнала денег не было;
// движения восстанавливаются из того, что он записывал при каждой операции:
//
//   - пополнение на кассе — cash_transactions (приход, receipt_data.top_up: user_id, balance_type, bonus)
//     (CashRegisterService::topUpBalance());
//   - возврат пополнения — расход с refund_of_id на такой приход; бонус, забранный назад, — details.bonus_taken_back
//     записи cash.refund (CashRegisterService::refund());
//   - покупка тарифа и минут — tariff.purchased / time.purchased: details.main_balance_amount, bonus_balance_amount
//     (AgentTariffPurchaseService::purchase(), purchaseMinutes());
//   - возврат за неиспользованное время — time.refunded, клиент — subject (SessionSettlement::settle());
//   - заказ из агента и его отмена — shop.purchased (user_id) / shop.order_cancel… (subject), details.amount
//     (AgentShop::order(), cancel());
//   - перевод другу — balance.transferred: user_id → details.to_user_id, details.amount (Friends::transfer());
//   - пополнение и списание владельцем без кассы — user.balance_topped_up с details.without_cash, user.balance_written_off
//     (UserController::update()); поле пополнения — по подписи details.balance_type (__('основной баланс') на языке
//     оператора: ключи и переводы translations).
//
// Не восстанавливается: начальный баланс клиента, созданного сотрудником (UserController::store() пишет user.created
// без суммы), и всё, что было до появления журнала. Поэтому расхождение — предупреждение (не блокер): список клиентов
// с суммой — в отчёте plan/apply/verify (Report.BalanceHistory). Остаток переносится как есть (Q-004), а сверка
// переноса по-прежнему требует точного совпадения остатка MySQL × 100 с ledger.

// CodeBalanceHistory — остаток не равен сумме движений по истории старой базы.
const CodeBalanceHistory = "Q-005"

// BalanceHistoryDiff — клиент, у которого остаток member_balances не равен сумме движений истории (тиыны).
type BalanceHistoryDiff struct {
	UserID int64 `json:"user_id"`
	// Main, Bonus — остаток в member_balances (основной, бонусный).
	Main  int64 `json:"main_tiyn"`
	Bonus int64 `json:"bonus_tiyn"`
	// HistoryMain, HistoryBonus — сумма движений по истории.
	HistoryMain  int64 `json:"history_main_tiyn"`
	HistoryBonus int64 `json:"history_bonus_tiyn"`
	// DiffMain, DiffBonus — остаток минус история (> 0 — на балансе больше, чем объясняет история).
	DiffMain  int64 `json:"diff_main_tiyn"`
	DiffBonus int64 `json:"diff_bonus_tiyn"`
	// NoBalanceRow — движения есть, а строки баланса нет.
	NoBalanceRow bool `json:"no_balance_row,omitempty"`
}

type historyDelta struct{ main, bonus money.Tiyn }

// historyMovements — суммы движений по клиентам (агрегаты — в MySQL, DECIMAL, без float).
var historyMovements = []struct {
	name string
	sql  string // колонки: user_id, основной, бонусный
	args []any  // clubArg — id клуба
}{
	{"cash top-up", `SELECT CAST(JSON_UNQUOTE(JSON_EXTRACT(receipt_data, '$.top_up.user_id')) AS UNSIGNED) AS uid,
		SUM(CASE WHEN JSON_UNQUOTE(JSON_EXTRACT(receipt_data, '$.top_up.balance_type')) = 'bonus_balance' THEN 0 ELSE amount END),
		SUM(CASE WHEN JSON_UNQUOTE(JSON_EXTRACT(receipt_data, '$.top_up.balance_type')) = 'bonus_balance' THEN amount ELSE 0 END
		    + COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(receipt_data, '$.top_up.bonus')) AS DECIMAL(20, 2)), 0))
		FROM cash_transactions
		WHERE club_id = ? AND type = 'income' AND JSON_EXTRACT(receipt_data, '$.top_up.user_id') IS NOT NULL
		GROUP BY uid`, []any{clubArg}},
	{"cash top-up refund", `SELECT CAST(JSON_UNQUOTE(JSON_EXTRACT(i.receipt_data, '$.top_up.user_id')) AS UNSIGNED) AS uid,
		-SUM(CASE WHEN JSON_UNQUOTE(JSON_EXTRACT(i.receipt_data, '$.top_up.balance_type')) = 'bonus_balance' THEN 0 ELSE r.amount END),
		-SUM(CASE WHEN JSON_UNQUOTE(JSON_EXTRACT(i.receipt_data, '$.top_up.balance_type')) = 'bonus_balance' THEN r.amount ELSE 0 END
		     + COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(a.details, '$.bonus_taken_back')) AS DECIMAL(20, 2)), 0))
		FROM cash_transactions r
		JOIN cash_transactions i ON i.id = r.refund_of_id AND i.club_id = r.club_id
		LEFT JOIN activity_logs a ON a.club_id = r.club_id AND a.event = 'cash.refund'
		     AND a.subject_type = ? AND a.subject_id = r.id
		WHERE r.club_id = ? AND r.type = 'expense' AND JSON_EXTRACT(i.receipt_data, '$.top_up.user_id') IS NOT NULL
		GROUP BY uid`, []any{morphCashTransaction, clubArg}},
	{"purchases and agent orders", `SELECT user_id AS uid,
		-SUM(COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(details, IF(event = 'shop.purchased', '$.amount', '$.main_balance_amount'))) AS DECIMAL(20, 2)), 0)),
		-SUM(IF(event = 'shop.purchased', 0, COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(details, '$.bonus_balance_amount')) AS DECIMAL(20, 2)), 0)))
		FROM activity_logs
		WHERE club_id = ? AND event IN ('tariff.purchased', 'time.purchased', 'shop.purchased')
		GROUP BY uid`, []any{clubArg}},
	{"refunds to the customer", `SELECT subject_id AS uid,
		SUM(COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(details, IF(event = 'time.refunded', '$.main_balance_amount', '$.amount'))) AS DECIMAL(20, 2)), 0)),
		SUM(IF(event = 'time.refunded', COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(details, '$.bonus_balance_amount')) AS DECIMAL(20, 2)), 0), 0))
		FROM activity_logs
		WHERE club_id = ? AND event IN ('time.refunded', 'shop.order_cancelled') AND subject_type = ?
		GROUP BY uid`, []any{clubArg, morphUser}},
	{"friend transfer (sent)", `SELECT user_id AS uid,
		-SUM(COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(details, '$.amount')) AS DECIMAL(20, 2)), 0)), 0
		FROM activity_logs WHERE club_id = ? AND event = 'balance.transferred' GROUP BY uid`, []any{clubArg}},
	{"friend transfer (received)", `SELECT CAST(JSON_UNQUOTE(JSON_EXTRACT(details, '$.to_user_id')) AS UNSIGNED) AS uid,
		SUM(COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(details, '$.amount')) AS DECIMAL(20, 2)), 0)), 0
		FROM activity_logs WHERE club_id = ? AND event = 'balance.transferred' AND JSON_EXTRACT(details, '$.to_user_id') IS NOT NULL
		GROUP BY uid`, []any{clubArg}},
	{"owner write-off", `SELECT subject_id AS uid,
		-SUM(IF(JSON_UNQUOTE(JSON_EXTRACT(details, '$.balance_field')) = 'bonus_balance', 0,
		        COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(details, '$.amount')) AS DECIMAL(20, 2)), 0))),
		-SUM(IF(JSON_UNQUOTE(JSON_EXTRACT(details, '$.balance_field')) = 'bonus_balance',
		        COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(details, '$.amount')) AS DECIMAL(20, 2)), 0), 0))
		FROM activity_logs
		WHERE club_id = ? AND event = 'user.balance_written_off' AND subject_type = ?
		GROUP BY uid`, []any{clubArg, morphUser}},
}

// morphUser — subject_type клиента (morph class App\Models\User).
const morphUser = `App\Models\User`

// ownerTopUpsSQL — пополнения владельцем без кассы: поле — по подписи (сопоставляется в Go).
const ownerTopUpsSQL = `SELECT subject_id, COALESCE(JSON_UNQUOTE(JSON_EXTRACT(details, '$.balance_type')), ''),
	SUM(COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(details, '$.amount')) AS DECIMAL(20, 2)), 0))
	FROM activity_logs
	WHERE club_id = ? AND event = 'user.balance_topped_up' AND subject_type = ?
	  AND JSON_UNQUOTE(JSON_EXTRACT(details, '$.without_cash')) = 'true'
	GROUP BY subject_id, 2`

// balanceLabels — подпись поля баланса (__('основной баланс') / __('бонусный баланс') на языке оператора) → поле.
func (r *run) balanceLabels(ctx context.Context) (map[string]string, error) {
	keys := map[string]string{"основной баланс": "balance", "бонусный баланс": "bonus_balance"}
	out := map[string]string{}
	for k, v := range keys {
		out[k] = v
	}
	rows, err := r.src.conn.QueryContext(ctx,
		"SELECT `key`, value FROM translations WHERE `group` = '*' AND `key` IN ('основной баланс', 'бонусный баланс')")
	if err != nil {
		return nil, fmt.Errorf("mysql: balance labels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		label := strings.ToLower(strings.TrimSpace(v))
		if prev, ok := out[label]; ok && prev != keys[k] {
			out[label] = "" // одна подпись у обоих полей — не угадываем
			continue
		}
		out[label] = keys[k]
	}
	return out, rows.Err()
}

// balanceHistory — Q-005: остаток каждого клиента против суммы движений по истории старой базы (только MySQL,
// тот же снимок). Расхождения — предупреждение и список в Report.BalanceHistory.
func (r *run) balanceHistory(ctx context.Context) error {
	history := map[int64]*historyDelta{}
	add := func(uid int64, main, bonus string) error {
		m, err := tiynFromDecimal(main)
		if err != nil {
			return err
		}
		b, err := tiynFromDecimal(bonus)
		if err != nil {
			return err
		}
		d := history[uid]
		if d == nil {
			d = &historyDelta{}
			history[uid] = d
		}
		d.main += m
		d.bonus += b
		return nil
	}
	for _, mv := range historyMovements {
		if err := r.scanMovements(ctx, mv.sql, bindClub(mv.args, r.club.ID), add); err != nil {
			return fmt.Errorf("mysql: balance history (%s): %w", mv.name, err)
		}
	}
	labels, err := r.balanceLabels(ctx)
	if err != nil {
		return err
	}
	rows, err := r.src.conn.QueryContext(ctx, ownerTopUpsSQL, r.club.ID, morphUser)
	if err != nil {
		return fmt.Errorf("mysql: balance history (owner top-up): %w", err)
	}
	for rows.Next() {
		var subject sql.NullInt64
		var label, sum string
		if err := rows.Scan(&subject, &label, &sum); err != nil {
			_ = rows.Close()
			return err
		}
		if !subject.Valid {
			continue
		}
		uid := subject.Int64
		switch labels[strings.ToLower(strings.TrimSpace(label))] {
		case "balance":
			err = add(uid, sum, "0")
		case "bonus_balance":
			err = add(uid, "0", sum)
		default:
			r.rep.add(Warning, CodeBalanceHistory, "activity_logs", "details.balance_type", label, strconv.FormatInt(uid, 10),
				"пополнение владельцем без кассы: поле баланса по подписи не определяется — в пересчёт не вошло")
		}
		if err != nil {
			_ = rows.Close()
			return err
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	balances, err := r.src.conn.QueryContext(ctx, "SELECT user_id, balance, bonus_balance FROM member_balances WHERE club_id = ? ORDER BY user_id", r.club.ID)
	if err != nil {
		return fmt.Errorf("mysql: balances: %w", err)
	}
	var diffs []BalanceHistoryDiff
	var clients, matched int64
	var sumMain, sumBonus int64
	seen := map[int64]bool{}
	for balances.Next() {
		var uid int64
		var main, bonus string
		if err := balances.Scan(&uid, &main, &bonus); err != nil {
			_ = balances.Close()
			return err
		}
		m, err := tiynFromDecimal(main)
		if err != nil {
			_ = balances.Close()
			return err
		}
		b, err := tiynFromDecimal(bonus)
		if err != nil {
			_ = balances.Close()
			return err
		}
		clients++
		seen[uid] = true
		h := history[uid]
		if h == nil {
			h = &historyDelta{}
		}
		d := BalanceHistoryDiff{UserID: uid, Main: int64(m), Bonus: int64(b), HistoryMain: int64(h.main), HistoryBonus: int64(h.bonus)}
		d.DiffMain, d.DiffBonus = d.Main-d.HistoryMain, d.Bonus-d.HistoryBonus
		if d.DiffMain == 0 && d.DiffBonus == 0 {
			matched++
			continue
		}
		sumMain += d.DiffMain
		sumBonus += d.DiffBonus
		diffs = append(diffs, d)
	}
	_ = balances.Close()
	if err := balances.Err(); err != nil {
		return err
	}
	orphans := make([]int64, 0)
	for uid, h := range history {
		if !seen[uid] && (h.main != 0 || h.bonus != 0) {
			orphans = append(orphans, uid)
		}
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i] < orphans[j] })
	for _, uid := range orphans {
		h := history[uid]
		diffs = append(diffs, BalanceHistoryDiff{UserID: uid, HistoryMain: int64(h.main), HistoryBonus: int64(h.bonus),
			DiffMain: -int64(h.main), DiffBonus: -int64(h.bonus), NoBalanceRow: true})
	}

	r.rep.BalanceHistory = diffs
	r.rep.Totals["Q-005: остаток = сумма движений истории"] = fmt.Sprintf("%d из %d клиентов", matched, clients)
	if len(diffs) == 0 {
		return nil
	}
	r.rep.Totals["Q-005: расхождение остаток − история (основной/бонусный)"] =
		tiynString(sumMain) + " / " + tiynString(sumBonus)
	for _, d := range diffs {
		msg := "остаток не равен сумме движений по истории старой базы (начальный баланс при создании клиента сотрудником не журналировался) — переносится остаток"
		if d.NoBalanceRow {
			msg = "движения по истории есть, а строки баланса клиента нет"
		}
		r.rep.add(Warning, CodeBalanceHistory, "member_balances", "", "", strconv.FormatInt(d.UserID, 10), msg)
	}
	return nil
}

func (r *run) scanMovements(ctx context.Context, q string, args []any, add func(uid int64, main, bonus string) error) error {
	rows, err := r.src.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var uid sql.NullInt64
		var main, bonus string
		if err := rows.Scan(&uid, &main, &bonus); err != nil {
			return err
		}
		if !uid.Valid {
			continue
		}
		if err := add(uid.Int64, main, bonus); err != nil {
			return err
		}
	}
	return rows.Err()
}
