//go:build integration

package migrate_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate/migratetest"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/shop"
	"github.com/aitmaganbetov/zafirogo/server/internal/webhook"
)

// M2: платформа → клуб с расширениями → сверка; конвертация JSON до тиына, производные данные, секреты APP_KEY
// (значения зашифрованы настоящим PHP), токены, файлы; повторный платформенный шаг безопасен.
func TestMigrateM2(t *testing.T) {
	ctx := context.Background()
	my, _ := migratetest.StartMySQL(t)
	pgdb := dbtest.New(t)
	owner := pgdb.Owner
	now := time.Now().UTC()
	data := migratetest.InjectM2(t, my, now)

	laravel, err := migrate.ParseLaravelKeys(data.Fixture.AppKey, data.Fixture.PreviousKeys)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.NewKeyring([]secretbox.Key{{ID: "k2026", Secret: strings.Repeat("d", 40)}}, "")
	if err != nil {
		t.Fatal(err)
	}
	popts := migrate.PlatformOptions{Now: now, IDReserve: idReserve, LaravelKeys: laravel, DataKeys: box, LaravelStorage: data.Storage}
	opts := func() migrate.Options {
		return migrate.Options{Club: "zafiro-arena", IDReserve: idReserve, Now: now, BatchSize: 3,
			Extensions: migrate.DefaultExtensions(), LaravelKeys: laravel, DataKeys: box, LaravelStorage: data.Storage}
	}

	// План клуба до платформы: предупреждение «сначала platform apply», блокеров нет.
	plan, err := migrate.Plan(ctx, my, owner, opts())
	if err != nil || plan.Count(migrate.Blocker) != 0 {
		t.Fatalf("plan: %v\n%s", err, reportText(plan))
	}
	requireFinding(t, plan, migrate.Warning, migrate.CodePlatform)
	requireFinding(t, plan, migrate.Warning, migrate.CodeFile) // новость без файла
	requireFinding(t, plan, migrate.Info, migrate.CodeReceiptID)
	if plan.Has("", migrate.CodeSkippedTable) {
		for _, f := range plan.Find(migrate.CodeSkippedTable) {
			if strings.Contains(f.Message, "webhook") || f.Table == "api_keys" {
				t.Fatalf("M2 table reported as skipped: %+v", f)
			}
		}
	}
	// Без APP_KEY — блокер (секрет вебхука не перенести).
	noKey := opts()
	noKey.LaravelKeys = nil
	if r, _ := migrate.Plan(ctx, my, owner, noKey); !r.Has(migrate.Blocker, migrate.CodeSecret) {
		t.Fatalf("plan without APP_KEY: no %s blocker\n%s", migrate.CodeSecret, reportText(r))
	}

	// --- Платформа ---
	if r, err := migrate.PlatformPlan(ctx, my, owner, popts); err != nil || r.Count(migrate.Blocker) != 0 {
		t.Fatalf("platform plan: %v\n%s", err, reportText(r))
	}
	pr, err := migrate.PlatformApply(ctx, my, owner, popts)
	if err != nil {
		t.Fatalf("platform apply: %v\n%s", err, reportText(pr))
	}
	t.Logf("платформа:\n%s", reportText(pr))
	if got := scalarPG[string](t, owner, `SELECT t.value FROM translations t JOIN languages l ON l.id = t.language_id
		WHERE l.code = 'kk' AND t.key = 'наличные'`); got != "қолма-қол" {
		t.Fatalf("translation kk: %q", got)
	}
	stored := scalarPG[string](t, owner, "SELECT value->>'token' FROM platform_settings WHERE key = 'telegram_gateway'")
	if !secretbox.IsSealed(stored) || strings.Contains(stored, data.TelegramToken) {
		t.Fatal("telegram token is not sealed with the data key")
	}
	if got, err := box.Open(stored); err != nil || got != data.TelegramToken {
		t.Fatalf("telegram token: %v", err)
	}
	if n := scalarPG[int64](t, owner, "SELECT count(*) FROM news WHERE club_id IS NULL"); n != 1 {
		t.Fatalf("platform news: %d", n)
	}
	// Повторный платформенный шаг: ничего не меняет, сверка — без блокеров.
	before := scalarPG[int64](t, owner, "SELECT count(*) FROM translations")
	if r, err := migrate.PlatformApply(ctx, my, owner, popts); err != nil {
		t.Fatalf("platform apply again: %v\n%s", err, reportText(r))
	}
	if after := scalarPG[int64](t, owner, "SELECT count(*) FROM translations"); after != before {
		t.Fatalf("translations %d → %d", before, after)
	}
	if r, err := migrate.PlatformVerify(ctx, my, owner, popts); err != nil {
		t.Fatalf("platform verify: %v\n%s", err, reportText(r))
	}

	// --- Клуб ---
	applied, err := migrate.Apply(ctx, my, owner, opts())
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, reportText(applied))
	}
	verified, err := migrate.Verify(ctx, my, owner, opts())
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, reportText(verified))
	}
	t.Logf("сверка:\n%s", reportText(verified))

	// Настройки: v2, нормализованы, читаются сервером так же.
	settings := scalarPG[string](t, owner, "SELECT settings::text FROM clubs WHERE id = 1")
	var doc map[string]map[string]json.RawMessage
	if err := json.Unmarshal([]byte(settings), &doc); err != nil {
		t.Fatal(err)
	}
	if got := string(doc["club"]["session_idle_timeout"]); got != "0.5" {
		t.Errorf("session_idle_timeout = %s", got)
	}
	if got := string(doc["club"]["agent_shop_enabled"]); got != "true" {
		t.Errorf("agent_shop_enabled = %s", got)
	}
	if !strings.Contains(string(doc["finances"]["loyalty_levels"]), `"v": 2`) {
		t.Errorf("loyalty levels not v2: %s", doc["finances"]["loyalty_levels"])
	}
	s, err := bonus.ParseSettings([]byte(settings))
	if err != nil || len(s.LoyaltyLevelsInForce()) != 2 || bonus.LoyaltyPercentFor(s.LoyaltyLevelsInForce(), 6_000_000) != 10 {
		t.Fatalf("loyalty read back: %+v %v", s, err)
	}

	// Чеки v2: суммы в тиынах, CQ-9 — product_id однозначного имени.
	rows, err := owner.Query(ctx, "SELECT id, receipt_data::text FROM cash_transactions WHERE club_id = 1 AND receipt_data IS NOT NULL")
	if err != nil {
		t.Fatal(err)
	}
	var snickers bool
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(raw, `"v": 2`) {
			t.Errorf("receipt %d not v2: %s", id, raw)
		}
		r, err := cashsvc.DecodeReceipt(json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range r.Items {
			if l.Name == "Snickers" && l.Total == 80000 {
				snickers = l.ProductID == 5
			}
			if l.Name == "Неизвестный" && (l.ProductID != 0 || l.Total != 10050) {
				t.Errorf("unknown product line: %+v", l)
			}
		}
	}
	rows.Close()
	if !snickers {
		t.Error("CQ-9: Snickers line without product_id 5")
	}
	items := scalarPG[string](t, owner, "SELECT items::text FROM shop_orders WHERE id = 3")
	lines, err := shop.DecodeItems(json.RawMessage(items))
	if err != nil || !strings.Contains(items, `"v": 2`) || len(lines) != 2 || lines[0].Total != 180000 {
		t.Fatalf("shop order v2: %s %+v %v", items, lines, err)
	}

	// Производные данные.
	if n := scalarPG[int64](t, owner, "SELECT sold FROM tariff_sales WHERE club_id = 1 AND tariff_id = 1"); n != 4 {
		t.Errorf("tariff_sales.sold = %d, want 4", n)
	}
	if n := scalarPG[int64](t, owner, "SELECT count(*) FROM cash_transactions WHERE club_id = 1 AND income_kind = 'top_up'"); n != 5 {
		t.Errorf("top_up incomes: %d", n)
	}
	if got := scalarPG[string](t, owner, "SELECT income_kind || '/' || payment_method FROM cash_transactions WHERE club_id = 1 AND description LIKE 'Гостевая%'"); got != "guest_session/cash" {
		t.Errorf("guest income: %s", got)
	}
	if got := scalarPG[string](t, owner, "SELECT payment_method FROM cash_transactions WHERE club_id = 1 AND id = 2"); got != "card" {
		t.Errorf("top-up payment_method: %s", got)
	}
	if got := scalarPG[string](t, owner, `SELECT string_agg(source || ':' || zone_id, ',' ORDER BY id) FROM session_purchases
		WHERE club_id = 1 AND session_id = $1`, data.SessionID); got != "tariff:1,minutes:1" {
		t.Errorf("session parts: %s", got)
	}

	// Интеграции: ключ API (хэш тот же), аккаунт создателя перенесён, секрет вебхука — тот же HMAC.
	sum := sha256.Sum256([]byte(data.APIKey))
	if n := scalarPG[int64](t, owner, "SELECT count(*) FROM api_keys WHERE club_id = 1 AND key_hash = $1", hex.EncodeToString(sum[:])); n != 1 {
		t.Errorf("api key: %d", n)
	}
	if n := scalarPG[int64](t, owner, "SELECT count(*) FROM users WHERE id = 14"); n != 1 {
		t.Errorf("api key creator not migrated")
	}
	if n := scalarPG[int64](t, owner, "SELECT count(*) FROM api_idempotency_keys WHERE club_id = 1"); n != 1 {
		t.Errorf("idempotency keys: %d", n)
	}
	sealed := scalarPG[string](t, owner, "SELECT secret FROM webhook_endpoints WHERE id = $1", data.WebhookID)
	if strings.Contains(sealed, data.WebhookSecret) {
		t.Fatal("webhook secret stored in plain text")
	}
	plain, err := box.OpenWith(sealed, webhook.SecretContext(1))
	if err != nil {
		t.Fatalf("open webhook secret: %v", err)
	}
	body := []byte(`{"event":"stock.changed"}`)
	if webhook.Sign(plain, 1_700_000_000, body) != webhook.Sign(data.WebhookSecret, 1_700_000_000, body) {
		t.Fatal("webhook signature differs from the old secret")
	}
	if _, err := box.OpenWith(sealed, webhook.SecretContext(2)); err == nil {
		t.Fatal("webhook secret opens in another club context")
	}
	if n := scalarPG[int64](t, owner, "SELECT count(*) FROM mobile_api_tokens WHERE club_id = 1"); n != 1 {
		t.Errorf("mobile tokens: %d", n)
	}
	if id := scalarPG[int64](t, owner, "SELECT id FROM mobile_api_tokens WHERE club_id = 1"); id != data.MobileTokenID {
		t.Errorf("mobile token id %d, want %d", id, data.MobileTokenID)
	}
	// Файлы: логотип перенесён байт в байт.
	logo := scalarPG[[]byte](t, owner, "SELECT data FROM club_images WHERE club_id = 1 AND kind = 'logo'")
	ls := sha256.Sum256(logo)
	if hex.EncodeToString(ls[:]) != data.LogoSHA256 {
		t.Error("logo bytes differ")
	}

	// Сверка ловит подмену: испорченный чек v2 и секрет другого клуба — блокеры.
	if _, err := owner.Exec(ctx, `UPDATE cash_transactions SET receipt_data = jsonb_set(receipt_data, '{items,0,total_tiyn}', '1')
		WHERE club_id = 1 AND id = 1`); err != nil {
		t.Fatal(err)
	}
	other, _ := box.SealWith(data.WebhookSecret+"x", webhook.SecretContext(1))
	if _, err := owner.Exec(ctx, "UPDATE webhook_endpoints SET secret = $1 WHERE id = $2", other, data.WebhookID); err != nil {
		t.Fatal(err)
	}
	broken, err := migrate.Verify(ctx, my, owner, opts())
	if err == nil {
		t.Fatalf("verify after tampering passed\n%s", reportText(broken))
	}
	var receipt, secret bool
	for _, f := range broken.Find(migrate.CodeVerifyMismatch) {
		receipt = receipt || f.Table == "cash_transactions"
		secret = secret || f.Table == "webhook_endpoints"
	}
	if !receipt || !secret {
		t.Fatalf("tampering not detected (receipt %v, secret %v)\n%s", receipt, secret, reportText(broken))
	}
	if strings.Contains(reportText(broken), data.WebhookSecret) {
		t.Fatal("report contains the webhook secret")
	}
}
