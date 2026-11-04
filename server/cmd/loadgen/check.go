//go:build loadgen

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jackc/pgx/v5/pgxpool"
)

// cmdCheck — инварианты денег после прогона:
//  1. сумма проводок каждой операции ledger = 0;
//  2. member_balances.balance / bonus_balance = сумма проводок счетов member_main / member_bonus клиента;
//  3. каждый ключ идемпотентности успешной операции генератора (keys.json) — ровно в одной операции ledger
//     (ключ сервиса содержит UUID клиента);
//  4. нет дублей ключа идемпотентности в ledger_transactions клуба.
func cmdCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	dir := fs.String("staging", "../deploy/staging", "каталог deploy/staging")
	keysFile := fs.String("keys", "", "keys.json прогона (необязательно)")
	_ = fs.Parse(args)
	env, err := loadStaging(*dir)
	if err != nil {
		return err
	}
	pg, err := pgxpool.New(ctx, env.superDSN())
	if err != nil {
		return err
	}
	defer pg.Close()
	failed := false
	one := func(name, q string) int64 {
		var n int64
		if err := pg.QueryRow(ctx, q).Scan(&n); err != nil {
			fmt.Printf("check: %s: %v\n", name, err)
			failed = true
			return -1
		}
		ok := n == 0
		if !ok {
			failed = true
		}
		fmt.Printf("check: %-60s %d %s\n", name, n, map[bool]string{true: "OK", false: "НАРУШЕНО"}[ok])
		return n
	}
	var tx, entries, accounts int64
	_ = pg.QueryRow(ctx, `SELECT (SELECT count(*) FROM ledger_transactions), (SELECT count(*) FROM ledger_entries), (SELECT count(*) FROM ledger_accounts)`).Scan(&tx, &entries, &accounts)
	fmt.Printf("check: ledger: операций %d, проводок %d, счетов %d\n", tx, entries, accounts)
	one("операций с суммой проводок ≠ 0", `SELECT count(*) FROM (SELECT transaction_id FROM ledger_entries GROUP BY transaction_id HAVING sum(amount) <> 0) x`)
	one("операций без проводок", `SELECT count(*) FROM ledger_transactions t WHERE NOT EXISTS (SELECT 1 FROM ledger_entries e WHERE e.transaction_id = t.id)`)
	one("балансов ≠ ledger (основной)", `SELECT count(*) FROM member_balances mb
		LEFT JOIN ledger_accounts a ON a.club_id = mb.club_id AND a.user_id = mb.user_id AND a.kind = 'member_main'
		LEFT JOIN LATERAL (SELECT coalesce(sum(amount), 0) s FROM ledger_entries e WHERE e.account_id = a.id) l ON true
		WHERE mb.balance <> coalesce(l.s, 0)`)
	one("балансов ≠ ledger (бонусный)", `SELECT count(*) FROM member_balances mb
		LEFT JOIN ledger_accounts a ON a.club_id = mb.club_id AND a.user_id = mb.user_id AND a.kind = 'member_bonus'
		LEFT JOIN LATERAL (SELECT coalesce(sum(amount), 0) s FROM ledger_entries e WHERE e.account_id = a.id) l ON true
		WHERE mb.bonus_balance <> coalesce(l.s, 0)`)
	one("отрицательных балансов", `SELECT count(*) FROM member_balances WHERE balance < 0 OR bonus_balance < 0`)
	one("дублей ключа идемпотентности ledger", `SELECT count(*) FROM (SELECT club_id, idempotency_key FROM ledger_transactions GROUP BY 1, 2 HAVING count(*) > 1) x`)

	if *keysFile != "" {
		b, err := os.ReadFile(filepath.Clean(*keysFile))
		if err != nil {
			return err
		}
		var keys []string
		if err := json.Unmarshal(b, &keys); err != nil {
			return err
		}
		rows, err := pg.Query(ctx, `SELECT idempotency_key FROM ledger_transactions WHERE club_id >= 1`)
		if err != nil {
			return err
		}
		re := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
		found := map[string]int{}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			for _, u := range re.FindAllString(k, -1) {
				found[u]++
			}
		}
		rows.Close()
		var once, none, many int
		for _, k := range keys {
			switch found[k] {
			case 0:
				none++
			case 1:
				once++
			default:
				many++
			}
		}
		fmt.Printf("check: ключей успешных операций генератора %d: ровно одна операция ledger — %d, ни одной — %d, больше одной — %d\n",
			len(keys), once, none, many)
		if many > 0 {
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("инварианты нарушены")
	}
	fmt.Println("check: инварианты денег выполняются")
	return nil
}
