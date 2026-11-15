//go:build integration

package migrate_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/reseller"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тема Д (docs/upstream-2026-11.md, UQ-3): после переноса кабинет реселлера (internal/reseller.Dashboard, пул
// zafiro_platform — как обработчик GET /api/reseller/dashboard) показывает те же суммы, что старый
// ResellerController::dashboard() на исходной базе MySQL — до тиына: «заработано» — сумма reseller_amount оплаченных
// счетов реселлера, «выплачено» — сумма выплат. userID — аккаунт реселлера, resellerID — его запись в старой базе.
func assertResellerCabinetMatchesMySQL(t *testing.T, my *sql.DB, pg *dbtest.DB, userID, resellerID int64) reseller.Cabinet {
	t.Helper()
	ctx := context.Background()
	// Старые запросы кабинета; decimal(12,2) → тиыны умножением в самой MySQL (точная десятичная арифметика).
	earned := scalarMy[int64](t, my, `SELECT CAST(COALESCE(SUM(reseller_amount), 0) * 100 AS SIGNED) FROM subscription_invoices
		WHERE reseller_id = ? AND status = 'paid'`, resellerID)
	paidOut := scalarMy[int64](t, my, "SELECT CAST(COALESCE(SUM(amount), 0) * 100 AS SIGNED) FROM reseller_payouts WHERE reseller_id = ?", resellerID)
	clubs := scalarMy[int64](t, my, "SELECT COUNT(*) FROM clubs WHERE reseller_id = ?", resellerID)
	earnings := scalarMy[int64](t, my, "SELECT COUNT(*) FROM subscription_invoices WHERE reseller_id = ? AND status = 'paid'", resellerID)
	code := scalarMy[string](t, my, "SELECT code FROM resellers WHERE id = ?", resellerID)
	bp := scalarMy[int64](t, my, "SELECT CAST(commission_percent * 100 AS SIGNED) FROM resellers WHERE id = ?", resellerID)

	var c reseller.Cabinet
	if err := db.InPlatformTx(ctx, pg.Platform, func(q *store.Queries) error {
		var err error
		c, err = reseller.Dashboard(ctx, q, userID, 1, 1)
		return err
	}); err != nil {
		t.Fatalf("кабинет реселлера %d: %v", resellerID, err)
	}
	if c.Code != code || int64(c.CommissionBP) != bp {
		t.Fatalf("реселлер %d: код %q процент %d б.п., в MySQL %q и %d", resellerID, c.Code, c.CommissionBP, code, bp)
	}
	if c.Earned != earned || c.PaidOut != paidOut || c.Balance != earned-paidOut {
		t.Fatalf("реселлер %d: заработано %d, выплачено %d, к выплате %d тиын; в MySQL — %d и %d", resellerID, c.Earned, c.PaidOut, c.Balance, earned, paidOut)
	}
	if c.ClubsTotal != clubs || c.EarningsTotal != earnings || int64(len(c.Clubs)) != clubs || int64(len(c.Earnings)) != earnings {
		t.Fatalf("реселлер %d: клубов %d, начислений %d; в MySQL — %d и %d", resellerID, c.ClubsTotal, c.EarningsTotal, clubs, earnings)
	}
	return c
}

// assertPlatformMoneyVerified — проверка 8 учений восстановления (deploy/backup/verify.sql «деньги платформы: документы
// = журнал») на базе теста: запрос берётся из самого файла учений, чтобы тест и учения не разошлись.
func assertPlatformMoneyVerified(t *testing.T, pg *dbtest.DB) {
	t.Helper()
	raw, err := os.ReadFile("../../../deploy/backup/verify.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "-- 8. Деньги платформы")
	if start < 0 {
		t.Fatal("verify.sql: нет проверки 8 «Деньги платформы»")
	}
	text = text[start:]
	from, end := strings.Index(text, "WITH missing AS ("), strings.Index(text, "FROM missing;")
	if from < 0 || end < 0 {
		t.Fatal("verify.sql: проверка 8 изменила вид — обновите разбор в тесте")
	}
	var (
		ord    int
		name   string
		ok     bool
		detail string
	)
	if err := pg.Owner.QueryRow(context.Background(), text[from:end]+"FROM missing").Scan(&ord, &name, &ok, &detail); err != nil {
		t.Fatalf("verify.sql, проверка 8: %v", err)
	}
	if ord != 8 || !ok {
		t.Fatalf("verify.sql, проверка %d «%s»: %s", ord, name, detail)
	}
}
