//go:build integration

package legacy_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// recNotifier — billingsvc.SessionNotifier, запоминающий BalanceChanged.
type recNotifier struct {
	mu       sync.Mutex
	balances map[int64]*agentv1.Balance
}

func (n *recNotifier) SessionStarted(context.Context, int64, int64, *agentv1.ActiveSession, int64) error {
	return nil
}

func (n *recNotifier) SessionEnded(context.Context, int64, int64, *agentv1.EndSession, int64) error {
	return nil
}

func (n *recNotifier) BalanceChanged(_ context.Context, _, userID int64, b *agentv1.Balance) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.balances == nil {
		n.balances = map[int64]*agentv1.Balance{}
	}
	n.balances[userID] = b
	return nil
}

func (n *recNotifier) balance(userID int64) *agentv1.Balance {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.balances[userID]
}

// Перевод другу по коду из Telegram (мобильное приложение): ПК, где вошли отправитель и друг, получают
// BalanceChanged после коммита — как перевод агента (guestapi.transferResponse).
func TestTransferConfirmNotifiesBothPCs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.exec(`UPDATE clubs SET settings = '{"guests": {"allow_adding_friends": true, "allow_balance_transfer_to_friends": true}}'::jsonb WHERE id = $1`, e.club)
	aID, aPhone := e.user(e.club, "Отправитель", "client", 0)
	bID, _ := e.user(e.club, "Друг", "client", 0)
	a := e.token(aPhone, "mobileclient")
	fid := tdb.Friendship(t, e.club, aID, bID, true)
	if err := db.InTenantTx(ctx, tdb.App, e.club, func(q *store.Queries) error {
		_, err := ledger.Post(ctx, q, ledger.Operation{
			ClubID: e.club, Kind: ledger.KindOwnerTopUp, IdempotencyKey: fmt.Sprintf("seed-%d", aID),
			Entries: []ledger.Entry{
				{Account: ledger.Main(aID), Amount: 20000},
				{Account: ledger.ClubAccount(ledger.AccountOwnerAdjustment), Amount: -20000},
			},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.MinCost)
	e.exec(`INSERT INTO transfer_codes (club_id, user_id, friendship_id, amount_cents, code_hash, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, 10000, $4, $5, now(), now())`, e.club, aID, fid, string(hash), time.Now().Add(5*time.Minute))

	expect(t, e.do("POST", fmt.Sprintf("/api/guest/friends/%d/transfer/confirm", fid), a, map[string]any{"amount": "100", "code": "123456"}), 200,
		map[string]any{"balance": "100.00"})
	from, to := e.notifier.balance(aID), e.notifier.balance(bID)
	if from.GetMainTiyn() != 10000 || from.GetCurrency() != "KZT" {
		t.Fatalf("sender BalanceChanged = %+v", from)
	}
	if to.GetMainTiyn() != 10000 || to.GetCurrency() != "KZT" {
		t.Fatalf("friend BalanceChanged = %+v", to)
	}
}
