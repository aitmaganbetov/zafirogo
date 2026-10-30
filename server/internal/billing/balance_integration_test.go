//go:build integration

package billing_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

var fixDB *dbtest.DB

func TestMain(m *testing.M) {
	d, cleanup, err := dbtest.Start(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fixDB = d
	code := m.Run()
	cleanup()
	os.Exit(code)
}

var (
	keyMu sync.Mutex
	keyN  int
)

func key(prefix string) string {
	keyMu.Lock()
	defer keyMu.Unlock()
	keyN++
	return fmt.Sprintf("%s-%d", prefix, keyN)
}

func tx(t *testing.T, club int64, fn func(ctx context.Context, q *store.Queries) error) error {
	t.Helper()
	ctx := context.Background()
	return db.InTenantTx(ctx, fixDB.App, club, func(q *store.Queries) error { return fn(ctx, q) })
}

// customer — клиент с начальными балансами (тенге × 100).
func customer(t *testing.T, club int64, name string, main, bonus money.Tiyn) int64 {
	t.Helper()
	id := fixDB.Customer(t, club, name)
	if err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
		_, err := billing.SetInitialBalance(ctx, q, billing.InitialBalanceParams{ClubID: club, UserID: id, Main: main, Bonus: bonus})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func wantBalances(t *testing.T, club, user int64, main, bonus money.Tiyn) {
	t.Helper()
	gotMain, gotBonus := fixDB.Balances(t, club, user)
	if money.Tiyn(gotMain) != main || money.Tiyn(gotBonus) != bonus {
		t.Fatalf("balances = %s / %s, want %s / %s", money.Tiyn(gotMain), money.Tiyn(gotBonus), main, bonus)
	}
}

func wantBalanceError(t *testing.T, err error, field, message string) {
	t.Helper()
	var be *billing.BalanceError
	if !errors.As(err, &be) || be.Field != field || be.Message != message {
		t.Fatalf("err = %v, want %s: %s", err, field, message)
	}
}

func ownerAdjust(t *testing.T, club, user int64, field billing.BalanceField, writeOff bool, amount string) error {
	t.Helper()
	tiyn, err := billing.ParseOwnerAdjustmentAmount(amount)
	if err != nil {
		return err
	}
	return tx(t, club, func(ctx context.Context, q *store.Queries) error {
		_, err := billing.OwnerAdjust(ctx, q, billing.OwnerAdjustParams{
			ClubID: club, UserID: user, Field: field, WriteOff: writeOff, Amount: tiyn,
			IdempotencyKey: key("owner"), Actor: billing.BalanceActor{Kind: "staff"},
		})
		return err
	})
}

func countCashTransactions(t *testing.T, club int64) int {
	t.Helper()
	var n int
	if err := fixDB.Owner.QueryRow(context.Background(), "SELECT count(*) FROM cash_transactions WHERE club_id = $1", club).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// OwnerTopUpTest.php: пополнение и списание владельцем без кассы (BR-058).
func TestOwnerAdjust(t *testing.T) {
	t.Run("GS-018", func(t *testing.T) {
		club := fixDB.Club(t, "gs-018")
		arman := customer(t, club, "Арман", 10000, 1000)
		if err := ownerAdjust(t, club, arman, billing.FieldMain, false, "500"); err != nil {
			t.Fatal(err)
		}
		if err := ownerAdjust(t, club, arman, billing.FieldBonus, false, "40.5"); err != nil {
			t.Fatal(err)
		}
		wantBalances(t, club, arman, 60000, 5050)
		if n := countCashTransactions(t, club); n != 0 {
			t.Fatalf("cash_transactions = %d, want 0", n)
		}
	})
	t.Run("GS-022", func(t *testing.T) { // деньги: 100 + 300
		club := fixDB.Club(t, "gs-022")
		arman := customer(t, club, "Арман", 10000, 1000)
		if err := ownerAdjust(t, club, arman, billing.FieldMain, false, "300"); err != nil {
			t.Fatal(err)
		}
		wantBalances(t, club, arman, 40000, 1000)
	})
	t.Run("GS-023", func(t *testing.T) { // деньги: 100 + 50
		club := fixDB.Club(t, "gs-023")
		arman := customer(t, club, "Арман", 10000, 1000)
		if err := ownerAdjust(t, club, arman, billing.FieldMain, false, "50"); err != nil {
			t.Fatal(err)
		}
		wantBalances(t, club, arman, 15000, 1000)
	})
	t.Run("GS-024", func(t *testing.T) {
		club := fixDB.Club(t, "gs-024")
		arman := customer(t, club, "Арман", 10000, 1000)
		if err := ownerAdjust(t, club, arman, billing.FieldMain, true, "30"); err != nil {
			t.Fatal(err)
		}
		if err := ownerAdjust(t, club, arman, billing.FieldBonus, true, "10"); err != nil {
			t.Fatal(err)
		}
		err := ownerAdjust(t, club, arman, billing.FieldMain, true, "500")
		wantBalanceError(t, err, "top_up_amount", "На балансе клиента меньше суммы списания: :amount.")
		var be *billing.BalanceError
		_ = errors.As(err, &be)
		if be.Params["amount"] != "70.00" {
			t.Fatalf("amount param = %q, want 70.00", be.Params["amount"])
		}
		wantBalances(t, club, arman, 7000, 0)
		if n := countCashTransactions(t, club); n != 0 {
			t.Fatalf("cash_transactions = %d, want 0", n)
		}
	})
	t.Run("GAP-020", func(t *testing.T) {
		// Пополнение владельцем не начисляет бонус за пополнение (UserController::update() не вызывает TopUpBonus).
		club := fixDB.Club(t, "gap-020")
		arman := customer(t, club, "Арман", 0, 0)
		if err := ownerAdjust(t, club, arman, billing.FieldMain, false, "2000"); err != nil {
			t.Fatal(err)
		}
		wantBalances(t, club, arman, 200000, 0)
		if n := countCashTransactions(t, club); n != 0 {
			t.Fatalf("cash_transactions = %d, want 0", n)
		}
	})
	t.Run("BR-058/rounding", func(t *testing.T) {
		// Решение Q-002 (docs/DECISIONS.md): было — "1234.565" округлялось до 1234.57; стало — 422, баланс не меняется.
		club := fixDB.Club(t, "br-058")
		arman := customer(t, club, "Арман", 0, 0)
		if err := ownerAdjust(t, club, arman, billing.FieldMain, false, "1234.565"); !errors.Is(err, billing.ErrBalanceAmountDecimals) {
			t.Fatalf("err = %v, want ErrBalanceAmountDecimals", err)
		}
		wantBalances(t, club, arman, 0, 0)
		if err := ownerAdjust(t, club, arman, billing.FieldMain, false, "1234.57"); err != nil {
			t.Fatal(err)
		}
		wantBalances(t, club, arman, 123457, 0)
	})
}

func topUp(t *testing.T, club, user int64, field billing.BalanceField, amount money.Tiyn, bonus billing.TopUpBonusFunc) (billing.TopUpResult, error) {
	t.Helper()
	var res billing.TopUpResult
	err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = billing.TopUp(ctx, q, billing.TopUpParams{
			ClubID: club, UserID: user, Field: field, Amount: amount, PaymentMethod: "cash",
			IdempotencyKey: key("topup"), Bonus: bonus, Actor: billing.BalanceActor{Kind: "staff"},
		})
		return err
	})
	return res, err
}

func cashDrawer(t *testing.T, club int64) money.Tiyn {
	t.Helper()
	var sum store.SumClubAccountByPeriodRow
	if err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
		var err error
		sum, err = q.SumClubAccountByPeriod(ctx, store.SumClubAccountByPeriodParams{
			ClubID: club, Kind: string(ledger.AccountCashDrawer),
			FromCreatedAt: time.Now().Add(-time.Hour), ToCreatedAt: time.Now().Add(time.Hour),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return -money.Tiyn(sum.Total) // касса — актив: остаток = −SUM
}

// Пополнение на кассе (BR-053), без расчёта бонуса: бонус — точка подключения TopUpBonusFunc.
func TestTopUp(t *testing.T) {
	t.Run("GS-012", func(t *testing.T) {
		club := fixDB.Club(t, "gs-012")
		c := customer(t, club, "Клиент POS", 10000, 2500)
		res, err := topUp(t, club, c, billing.FieldMain, 75000, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.NewBalance != 85000 || res.Bonus != 0 {
			t.Fatalf("result = %+v", res)
		}
		wantBalances(t, club, c, 85000, 2500)
		if got := cashDrawer(t, club); got != 75000 {
			t.Fatalf("cash drawer = %s, want 750.00", got)
		}
	})
	t.Run("GS-003", func(t *testing.T) {
		// Пополнение бонусного баланса — без бонуса, даже если бонус включён (CashRegisterService::topUpBalance() ~стр. 114).
		club := fixDB.Club(t, "gs-003")
		c := customer(t, club, "Клиент", 10000, 2500)
		called := false
		hook := func(amount money.Tiyn) (money.Tiyn, error) { called = true; return amount / 2, nil }
		if _, err := topUp(t, club, c, billing.FieldBonus, 100000, hook); err != nil {
			t.Fatal(err)
		}
		if called {
			t.Fatal("bonus hook called for a bonus-balance top-up")
		}
		wantBalances(t, club, c, 10000, 102500)
	})
	t.Run("BR-041/hook", func(t *testing.T) {
		// Точка подключения бонуса: сумма бонуса уходит в бонусный баланс из бонусного фонда, касса — только деньги.
		club := fixDB.Club(t, "br-041-hook")
		c := customer(t, club, "Клиент", 10000, 2500)
		res, err := topUp(t, club, c, billing.FieldMain, 250000, func(money.Tiyn) (money.Tiyn, error) { return 12500, nil })
		if err != nil {
			t.Fatal(err)
		}
		if res.Bonus != 12500 || res.NewBalance != 260000 || res.NewBonusBalance != 15000 {
			t.Fatalf("result = %+v", res)
		}
		wantBalances(t, club, c, 260000, 15000)
		if got := cashDrawer(t, club); got != 250000 {
			t.Fatalf("cash drawer = %s, want 2500.00 (bonus is not cash)", got)
		}
	})
	t.Run("BR-053/limits", func(t *testing.T) {
		club := fixDB.Club(t, "br-053")
		c := customer(t, club, "Клиент", 0, 0)
		if _, err := topUp(t, club, c, billing.FieldMain, 0, nil); !errors.Is(err, billing.ErrBalanceAmountOutOfRange) {
			t.Fatalf("zero: %v", err)
		}
		if _, err := topUp(t, club, c, billing.FieldMain, billing.MaxCashTopUp+1, nil); !errors.Is(err, billing.ErrBalanceAmountOutOfRange) {
			t.Fatalf("over max: %v", err)
		}
		if _, err := topUp(t, club, c, billing.FieldMain, billing.MaxCashTopUp, nil); err != nil {
			t.Fatalf("max: %v", err)
		}
		// decimal(12,2): баланс больше 9 999 999 999.99 — ошибка и откат.
		if _, err := topUp(t, club, c, billing.FieldMain, 1, nil); !errors.Is(err, ledger.ErrBalanceOverflow) {
			t.Fatalf("overflow: %v", err)
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		club := fixDB.Club(t, "topup-idem")
		c := customer(t, club, "Клиент", 0, 0)
		params := billing.TopUpParams{ClubID: club, UserID: c, Field: billing.FieldMain, Amount: 1000, PaymentMethod: "qr", IdempotencyKey: "pos-req-1"}
		var first, second billing.TopUpResult
		for _, res := range []*billing.TopUpResult{&first, &second} {
			if err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
				var err error
				*res, err = billing.TopUp(ctx, q, params)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		if !second.Ledger.Replayed || second.NewBalance != 1000 || first.Ledger.TransactionID != second.Ledger.TransactionID {
			t.Fatalf("second = %+v", second)
		}
		wantBalances(t, club, c, 1000, 0)
		params.Amount = 2000
		err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
			_, err := billing.TopUp(ctx, q, params)
			return err
		})
		if !errors.Is(err, ledger.ErrIdempotencyConflict) {
			t.Fatalf("err = %v, want ErrIdempotencyConflict", err)
		}
	})
}

func refundTopUp(t *testing.T, club, user int64, field billing.BalanceField, amount, bonus money.Tiyn, incomeID int64) (billing.RefundTopUpResult, error) {
	t.Helper()
	var res billing.RefundTopUpResult
	err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = billing.RefundTopUp(ctx, q, billing.RefundTopUpParams{
			ClubID: club, UserID: user, Field: field, Amount: amount, Bonus: bonus, IncomeCashTransactionID: incomeID,
		})
		return err
	})
	return res, err
}

func spend(t *testing.T, club, user int64, main, bonus money.Tiyn) {
	t.Helper()
	if err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
		_, err := billing.Charge(ctx, q, billing.ChargeParams{
			ClubID: club, UserID: user, Kind: ledger.KindShopOrder, Revenue: ledger.AccountRevenueShop,
			Main: main, Bonus: bonus, IdempotencyKey: key("spend"),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// Возврат пополнения по чеку (BR-055, ветка top_up). Бонус пополнения задан напрямую (без расчёта).
func TestRefundTopUp(t *testing.T) {
	t.Run("GS-005", func(t *testing.T) {
		club := fixDB.Club(t, "gs-005")
		c := customer(t, club, "Клиент", 0, 0)
		if _, err := topUp(t, club, c, billing.FieldMain, 200000, func(money.Tiyn) (money.Tiyn, error) { return 20000, nil }); err != nil {
			t.Fatal(err)
		}
		wantBalances(t, club, c, 200000, 20000)
		spend(t, club, c, 50000, 0) // основной 1500.00
		_, err := refundTopUp(t, club, c, billing.FieldMain, 200000, 20000, 1)
		wantBalanceError(t, err, "refund", "Клиент уже потратил часть пополнения: на балансе меньше суммы возврата.")
		if err := ownerAdjust(t, club, c, billing.FieldMain, false, "1000"); err != nil { // основной 2500.00
			t.Fatal(err)
		}
		res, err := refundTopUp(t, club, c, billing.FieldMain, 200000, 20000, 1)
		if err != nil {
			t.Fatal(err)
		}
		if res.NewBalance != 50000 || res.BonusTakenBack != 20000 || res.NewBonusBalance != 0 {
			t.Fatalf("result = %+v", res)
		}
		wantBalances(t, club, c, 50000, 0)
		if got := cashDrawer(t, club); got != 0 {
			t.Fatalf("cash drawer = %s, want 0.00", got)
		}
		// Повторный возврат того же прихода — прежний результат, не второе списание.
		again, err := refundTopUp(t, club, c, billing.FieldMain, 200000, 20000, 1)
		if err != nil || !again.Ledger.Replayed {
			t.Fatalf("again = %+v, %v", again, err)
		}
		wantBalances(t, club, c, 50000, 0)
	})
	t.Run("GAP-024", func(t *testing.T) {
		club := fixDB.Club(t, "gap-024")
		c := customer(t, club, "Клиент", 0, 0)
		if _, err := topUp(t, club, c, billing.FieldMain, 200000, func(money.Tiyn) (money.Tiyn, error) { return 20000, nil }); err != nil {
			t.Fatal(err)
		}
		spend(t, club, c, 0, 15000) // бонусов осталось 50.00
		res, err := refundTopUp(t, club, c, billing.FieldMain, 200000, 20000, 7)
		if err != nil {
			t.Fatal(err)
		}
		if res.BonusTakenBack != 5000 {
			t.Fatalf("bonus taken back = %s, want 50.00", res.BonusTakenBack)
		}
		wantBalances(t, club, c, 0, 0)
		if got := cashDrawer(t, club); got != 0 { // +2000 −2000: бонус в кассу не входил
			t.Fatalf("cash drawer = %s, want 0.00", got)
		}
	})
	t.Run("GAP-025", func(t *testing.T) {
		club := fixDB.Club(t, "gap-025")
		c := customer(t, club, "Клиент", 10000, 0)
		if _, err := topUp(t, club, c, billing.FieldBonus, 50000, nil); err != nil {
			t.Fatal(err)
		}
		spend(t, club, c, 0, 1)
		_, err := refundTopUp(t, club, c, billing.FieldBonus, 50000, 0, 9)
		wantBalanceError(t, err, "refund", "Клиент уже потратил часть пополнения: на балансе меньше суммы возврата.")
		if err := ownerAdjust(t, club, c, billing.FieldBonus, false, "0.01"); err != nil {
			t.Fatal(err)
		}
		if _, err := refundTopUp(t, club, c, billing.FieldBonus, 50000, 0, 9); err != nil {
			t.Fatal(err)
		}
		wantBalances(t, club, c, 10000, 0)
	})
}

func transfer(t *testing.T, club, user, friendship int64, input string) (billing.FriendTransferResult, error) {
	t.Helper()
	var res billing.FriendTransferResult
	err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = billing.FriendTransfer(ctx, q, billing.FriendTransferParams{
			ClubID: club, UserID: user, FriendshipID: friendship, Amount: money.ParseTransferAmount(input),
			IdempotencyKey: key("transfer"), Actor: billing.BalanceActor{UserID: user, Kind: "guest"},
		})
		return err
	})
	return res, err
}

// Переводы друзьям (BR-091).
func TestFriendTransfer(t *testing.T) {
	const amountRange = "Сумма перевода — от 0,01 до 1 000 000."
	t.Run("GS-075", func(t *testing.T) { // деньги; пароль проверяет вызывающий
		club := fixDB.Club(t, "gs-075")
		arman := customer(t, club, "Арман", 10000, 0)
		dana := customer(t, club, "Дана", 0, 0)
		f := fixDB.Friendship(t, club, arman, dana, true)
		res, err := transfer(t, club, arman, f, "10")
		if err != nil {
			t.Fatal(err)
		}
		if res.Balance.Tenge() != "90.00" {
			t.Fatalf("balance = %s, want 90.00", res.Balance)
		}
	})
	t.Run("GS-102", func(t *testing.T) {
		club := fixDB.Club(t, "gs-102")
		arman := customer(t, club, "Арман", 10000, 5000)
		dana := customer(t, club, "Дана", 1000, 0)
		stranger := customer(t, club, "Незнакомец", 0, 0)
		other := customer(t, club, "Другой", 0, 0)
		f := fixDB.Friendship(t, club, arman, dana, true)
		pending := fixDB.Friendship(t, club, arman, stranger, false)
		foreign := fixDB.Friendship(t, club, dana, other, true)
		for _, in := range []string{"100.01"} {
			_, err := transfer(t, club, arman, f, in)
			wantBalanceError(t, err, "amount", "Недостаточно средств на основном счёте.")
		}
		for _, in := range []string{"0", "-5"} {
			_, err := transfer(t, club, arman, f, in)
			wantBalanceError(t, err, "amount", amountRange)
		}
		_, err := transfer(t, club, arman, pending, "10")
		wantBalanceError(t, err, "friendship", "Переводить можно только друзьям.")
		_, err = transfer(t, club, arman, foreign, "10")
		wantBalanceError(t, err, "friendship", "Переводить можно только друзьям.")
		res, err := transfer(t, club, arman, f, "40,50")
		if err != nil {
			t.Fatal(err)
		}
		if res.Balance.Tenge() != "59.50" || res.Amount.Tenge() != "40.50" || res.FriendID != dana {
			t.Fatalf("result = %+v", res)
		}
		wantBalances(t, club, arman, 5950, 5000)
		wantBalances(t, club, dana, 5050, 0)
	})
	t.Run("GAP-032", func(t *testing.T) {
		club := fixDB.Club(t, "gap-032")
		arman := customer(t, club, "Арман", 100_000_000, 0)
		dana := customer(t, club, "Дана", 0, 0)
		f := fixDB.Friendship(t, club, arman, dana, true)
		if got := money.ParseTransferAmount("40,50"); got != 4050 {
			t.Fatalf(`"40,50" = %d`, got)
		}
		if got := money.ParseTransferAmount(" 5 "); got != 500 {
			t.Fatalf(`" 5 " = %d`, got)
		}
		_, err := transfer(t, club, arman, f, "10.555")
		wantBalanceError(t, err, "amount", amountRange)
		_, err = transfer(t, club, arman, f, "1000000.01")
		wantBalanceError(t, err, "amount", amountRange)
		if _, err := transfer(t, club, arman, f, "1000000"); err != nil {
			t.Fatalf("max transfer: %v", err)
		}
		wantBalances(t, club, arman, 0, 0)
		wantBalances(t, club, dana, 100_000_000, 0)
	})
	t.Run("GAP-033", func(t *testing.T) {
		club := fixDB.Club(t, "gap-033")
		arman := customer(t, club, "Арман", 10000, 300)
		dana := customer(t, club, "Дана", 1000, 0)
		f := fixDB.Friendship(t, club, dana, arman, true) // Арман — получатель заявки
		if _, err := transfer(t, club, arman, f, "40"); err != nil {
			t.Fatal(err)
		}
		wantBalances(t, club, arman, 6000, 300)
		wantBalances(t, club, dana, 5000, 0)
		var spent int64
		if err := fixDB.Owner.QueryRow(context.Background(),
			"SELECT coalesce(sum(total_spent), 0) FROM club_memberships WHERE club_id = $1", club).Scan(&spent); err != nil {
			t.Fatal(err)
		}
		if spent != 0 {
			t.Fatalf("total_spent = %d, want 0", spent)
		}
	})
	t.Run("BR-091/example", func(t *testing.T) {
		club := fixDB.Club(t, "br-091")
		a := customer(t, club, "A", 150000, 0)
		b := customer(t, club, "B", 0, 0)
		f := fixDB.Friendship(t, club, a, b, true)
		if _, err := transfer(t, club, a, f, "250,5"); err != nil {
			t.Fatal(err)
		}
		wantBalances(t, club, a, 124950, 0)
		wantBalances(t, club, b, 25050, 0)
	})
	t.Run("BR-091/opposite-transfers-concurrently", func(t *testing.T) {
		// Два встречных перевода не взаимоблокируются: оба баланса — FOR UPDATE в порядке user_id.
		club := fixDB.Club(t, "br-091-concurrent")
		a := customer(t, club, "A", 100000, 0)
		b := customer(t, club, "B", 100000, 0)
		f := fixDB.Friendship(t, club, a, b, true)
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				from := a
				if i%2 == 1 {
					from = b
				}
				if _, err := transfer(t, club, from, f, "1"); err != nil {
					t.Error(err)
				}
			}(i)
		}
		wg.Wait()
		wantBalances(t, club, a, 100000, 0)
		wantBalances(t, club, b, 100000, 0)
	})
	t.Run("idempotent", func(t *testing.T) {
		club := fixDB.Club(t, "transfer-idem")
		a := customer(t, club, "A", 10000, 0)
		b := customer(t, club, "B", 0, 0)
		f := fixDB.Friendship(t, club, a, b, true)
		p := billing.FriendTransferParams{ClubID: club, UserID: a, FriendshipID: f, Amount: 10000, IdempotencyKey: "agent-req-1"}
		for i := 0; i < 2; i++ {
			// Второй раз баланс уже 0 — но повтор получает прежний ответ, а не «Недостаточно средств».
			if err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
				res, err := billing.FriendTransfer(ctx, q, p)
				if err == nil && res.Balance != 0 {
					return fmt.Errorf("balance = %s", res.Balance)
				}
				return err
			}); err != nil {
				t.Fatalf("attempt %d: %v", i, err)
			}
		}
		wantBalances(t, club, a, 0, 0)
		wantBalances(t, club, b, 10000, 0)
	})
}

// Перевод по коду из Telegram (BR-092, FriendTransferCodes::confirm()).
func TestConfirmFriendTransferCode(t *testing.T) {
	club := fixDB.Club(t, "gap-034")
	arman := customer(t, club, "Арман", 10000, 0)
	dana := customer(t, club, "Дана", 0, 0)
	f := fixDB.Friendship(t, club, arman, dana, true)
	other := fixDB.Friendship(t, club, dana, customer(t, club, "Третий", 0, 0), true)
	verify := func(hash, code string) bool { return hash == "hash:"+code }
	newCode := func(friendship int64, cents int64, code string, expiresIn time.Duration) int64 {
		var id int64
		if err := fixDB.Owner.QueryRow(context.Background(),
			`INSERT INTO transfer_codes (club_id, user_id, friendship_id, amount_cents, code_hash, expires_at, created_at, updated_at)
			 VALUES ($6, $1, $2, $3, $4, now() + $5::interval, now(), now()) RETURNING id`,
			arman, friendship, cents, "hash:"+code, fmt.Sprintf("%d seconds", int(expiresIn.Seconds())), club).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	confirm := func(friendship int64, amount, code string) error {
		var mismatch *billing.TransferCodeMismatchError
		err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
			_, err := billing.ConfirmFriendTransferCode(ctx, q, billing.ConfirmTransferCodeParams{
				ClubID: club, UserID: arman, FriendshipID: friendship, AmountInput: amount, Code: code,
				Now: time.Now(), VerifyCode: verify,
			})
			return err
		})
		if errors.As(err, &mismatch) {
			if aerr := tx(t, club, func(ctx context.Context, q *store.Queries) error {
				return billing.RecordTransferCodeAttempt(ctx, q, club, mismatch.CodeID)
			}); aerr != nil {
				t.Fatal(aerr)
			}
		}
		return err
	}
	attempts := func(id int64) int {
		var n int
		if err := fixDB.Owner.QueryRow(context.Background(), "SELECT attempts FROM transfer_codes WHERE id = $1", id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	const wrong = "Код неверный или устарел. Запросите новый код."

	t.Run("GAP-034/expired", func(t *testing.T) {
		newCode(f, 1000, "111111", -time.Second)
		wantBalanceError(t, confirm(f, "10", "111111"), "code", wrong)
		wantBalances(t, club, arman, 10000, 0)
	})
	t.Run("GAP-034/only-latest-code", func(t *testing.T) {
		newCode(f, 1000, "222222", 5*time.Minute)
		latest := newCode(f, 1000, "333333", 5*time.Minute)
		var mismatch *billing.TransferCodeMismatchError
		if err := confirm(f, "10", "222222"); !errors.As(err, &mismatch) || mismatch.CodeID != latest {
			t.Fatalf("err = %v, want mismatch on the latest code", err)
		}
		if attempts(latest) != 1 {
			t.Fatalf("attempts = %d, want 1", attempts(latest))
		}
	})
	t.Run("GAP-034/other-friendship-or-amount", func(t *testing.T) {
		latest := newCode(f, 1000, "444444", 5*time.Minute)
		var mismatch *billing.TransferCodeMismatchError
		if err := confirm(other, "10", "444444"); !errors.As(err, &mismatch) {
			t.Fatalf("other friendship: %v", err)
		}
		if err := confirm(f, "10.01", "444444"); !errors.As(err, &mismatch) {
			t.Fatalf("other amount: %v", err)
		}
		if attempts(latest) != 2 {
			t.Fatalf("attempts = %d, want 2", attempts(latest))
		}
	})
	t.Run("GAP-034/max-attempts", func(t *testing.T) {
		latest := newCode(f, 1000, "555555", 5*time.Minute)
		for i := 0; i < billing.TransferCodeMaxAttempts; i++ {
			_ = confirm(f, "10", "000000")
		}
		if attempts(latest) != billing.TransferCodeMaxAttempts {
			t.Fatalf("attempts = %d", attempts(latest))
		}
		wantBalanceError(t, confirm(f, "10", "555555"), "code", wrong)
		wantBalances(t, club, arman, 10000, 0)
	})
	// Решение Q-028/L-1 (docs/DECISIONS.md): у кода свой club_id; код того же пользователя в другом клубе (новее по id)
	// не перекрывает код этого клуба и не удаляется переводом здесь. Клуб кода не может разойтись с клубом дружбы (FK).
	club2 := fixDB.Club(t, "gap-034-other")
	fixDB.Join(t, club2, arman)
	fixDB.Join(t, club2, dana)
	f2 := fixDB.Friendship(t, club2, arman, dana, true)
	var otherClubCode int64
	t.Run("Q-028/other-club-code", func(t *testing.T) {
		own := newCode(f, 1000, "777777", 5*time.Minute)
		if err := fixDB.Owner.QueryRow(context.Background(),
			`INSERT INTO transfer_codes (club_id, user_id, friendship_id, amount_cents, code_hash, expires_at, created_at, updated_at)
			 VALUES ($1, $2, $3, 1000, 'hash:888888', now() + interval '5 minutes', now(), now()) RETURNING id`,
			club2, arman, f2).Scan(&otherClubCode); err != nil {
			t.Fatal(err)
		}
		var mismatch *billing.TransferCodeMismatchError
		if err := confirm(f, "10", "888888"); !errors.As(err, &mismatch) || mismatch.CodeID != own {
			t.Fatalf("err = %v, want mismatch on this club's code %d", err, own)
		}
		if _, err := fixDB.Owner.Exec(context.Background(),
			`INSERT INTO transfer_codes (club_id, user_id, friendship_id, amount_cents, code_hash, expires_at, created_at, updated_at)
			 VALUES ($1, $2, $3, 1000, 'x', now(), now(), now())`, club2, arman, f); err == nil {
			t.Fatal("code with club_id of another club than its friendship must violate FK")
		}
	})
	t.Run("BR-092/success", func(t *testing.T) {
		newCode(f, 2550, "666666", 5*time.Minute)
		if err := confirm(f, "25,5", "666666"); err != nil {
			t.Fatal(err)
		}
		wantBalances(t, club, arman, 7450, 0)
		wantBalances(t, club, dana, 2550, 0)
		var left int
		if err := fixDB.Owner.QueryRow(context.Background(), "SELECT count(*) FROM transfer_codes WHERE user_id = $1 AND club_id = $2", arman, club).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 0 {
			t.Fatalf("codes left = %d, want 0", left)
		}
		var kept int
		if err := fixDB.Owner.QueryRow(context.Background(), "SELECT count(*) FROM transfer_codes WHERE id = $1", otherClubCode).Scan(&kept); err != nil || kept != 1 {
			t.Fatalf("other club code kept = %d, %v", kept, err)
		}
		wantBalanceError(t, confirm(f, "25,5", "666666"), "code", wrong) // код израсходован
	})
}

// Покупка и возврат покупки (общие Charge/Credit), аудит денежных операций.
func TestChargeCreditAndAudit(t *testing.T) {
	club := fixDB.Club(t, "charge")
	c := customer(t, club, "Клиент", 100000, 20000)
	var charge billing.ChargeResult
	err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
		var err error
		charge, err = billing.Charge(ctx, q, billing.ChargeParams{
			ClubID: club, UserID: c, Kind: ledger.KindTariffPurchase, Revenue: ledger.AccountRevenueTime,
			Main: 70000, Bonus: 30000, IdempotencyKey: "tariff:1", Actor: billing.BalanceActor{UserID: c, Kind: "guest"},
		})
		return err
	})
	var ife *ledger.InsufficientFundsError
	if !errors.As(err, &ife) || ife.Account != ledger.Bonus(c) {
		t.Fatalf("err = %v, want insufficient bonus", err)
	}
	if err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
		var err error
		charge, err = billing.Charge(ctx, q, billing.ChargeParams{
			ClubID: club, UserID: c, Kind: ledger.KindTariffPurchase, Revenue: ledger.AccountRevenueTime,
			Main: 70000, Bonus: 20000, IdempotencyKey: "tariff:2", Actor: billing.BalanceActor{UserID: c, Kind: "guest"},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if charge.NewBalance != 30000 || charge.NewBonusBalance != 0 {
		t.Fatalf("charge = %+v", charge)
	}
	if err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
		_, err := billing.Credit(ctx, q, billing.ChargeParams{
			ClubID: club, UserID: c, Kind: ledger.KindSessionRefund, Revenue: ledger.AccountRevenueTime,
			Main: 3750, IdempotencyKey: "session:s1:refund",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wantBalances(t, club, c, 33750, 0)

	var actions []string
	if err := fixDB.Owner.QueryRow(context.Background(),
		"SELECT array_agg(action ORDER BY id) FROM audit_log WHERE club_id = $1", club).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	want := []string{"money.initial_balance", "money.tariff_purchase", "money.session_refund"}
	if fmt.Sprint(actions) != fmt.Sprint(want) {
		t.Fatalf("audit actions = %v, want %v", actions, want)
	}
	var drift []store.ListMemberBalanceDriftRow
	if err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
		var err error
		drift, err = q.ListMemberBalanceDrift(ctx, club)
		return err
	}); err != nil || len(drift) != 0 {
		t.Fatalf("drift = %+v, %v", drift, err)
	}
}
