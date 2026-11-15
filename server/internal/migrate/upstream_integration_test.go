//go:build integration

package migrate_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate/migratetest"
)

// Новое в старом Zafiro после 26.10.2026 (docs/upstream-2026-11.md, миграции 0034–0038): реселлеры и выплаты
// (платформенный шаг), счета за подписку, скидка, город/адрес/профиль клуба, оборудование зон, фото клуба, брони из
// приложения (с клубом), журнал денег платформы. Суммы × 100 через строку, проценты → базисные пункты,
// paid_*_cents — без × 100, id сохраняются.
func TestMigrateUpstream2026_11(t *testing.T) {
	ctx := context.Background()
	my, _ := migratetest.StartMySQL(t)
	pgdb := dbtest.New(t)
	owner := pgdb.Owner
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).UTC().Format(time.DateTime) }

	// --- Старая база: реселлеры ---
	// Аккаунты 100 и 101 не состоят ни в одном клубе: 100 переносит платформенный шаг, 101 (реселлер, созданный
	// после платформенного шага) — клуб, на счёт которого он ссылается.
	for _, u := range []struct {
		id          int
		name, phone string
	}{{100, "Реселлер Первый", "+77050000100"}, {101, "Реселлер Поздний", "+77050000101"}, {102, "Реселлер Без клубов", "+77050000102"}} {
		execMy(t, my, "INSERT INTO users (id, name, phone, password, created_at, updated_at) VALUES (?, ?, ?, 'hash', ?, ?)",
			u.id, u.name, u.phone, at(-72*time.Hour), at(-72*time.Hour))
	}
	execMy(t, my, `INSERT INTO resellers (id, user_id, code, name, commission_percent, discount_percent, is_active, created_at, updated_at)
		VALUES (5, 100, 'astana-pro', 'Astana Pro', 12.35, 10, 1, ?, ?), (6, 102, 'no-clubs', 'Без клубов', 0.01, 0, 0, ?, ?)`,
		at(-70*time.Hour), at(-70*time.Hour), at(-70*time.Hour), at(-70*time.Hour))
	execMy(t, my, `INSERT INTO reseller_payouts (id, reseller_id, amount, note, created_by, created_at, updated_at)
		VALUES (11, 5, 500.00, 'Kaspi, ноябрь', 1, ?, ?), (12, 5, 94.04, NULL, NULL, ?, ?), (13, 6, 0.01, 'проверка', 1, ?, ?)`,
		at(-10*time.Hour), at(-10*time.Hour), at(-9*time.Hour), at(-9*time.Hour), at(-8*time.Hour), at(-8*time.Hour))

	// --- Старая база: клуб 1 ---
	execMy(t, my, `UPDATE clubs SET city = 'aktobe', address = 'пр. Абилкайыр хана, 1', reseller_id = 5, reseller_attached_at = ?,
		profile = '{"description":"Клуб у парка","phone":"+7 700 000 00 00","hours":"Круглосуточно","instagram":"asia.game"}' WHERE id = 1`, at(-60*time.Hour))
	execMy(t, my, `UPDATE club_zones SET devices = '{"monitor":"ASUS 240 Гц","mouse":"Logitech G102"}' WHERE id = 1`)
	execMy(t, my, "UPDATE club_subscriptions SET discount_percent = 15, discount_until = '2026-12-31', discount_note = 'Первый клуб города' WHERE club_id = 1")
	// Счета: оплаченный с реселлером (GAP-040: 4810 ₸ × 12,35 % = 594,04), оплаченный после удаления реселлера
	// (reseller_id NULL, начисление осталось — BR-189), отменённый, неоплаченный.
	execMy(t, my, `INSERT INTO subscription_invoices (id, club_id, saas_plan_id, months, computers, price_per_computer, discount_percent, amount,
			reseller_id, reseller_percent, reseller_amount, status, created_by, confirmed_by, paid_at, period_ends_at, created_at, updated_at)
		VALUES (21, 1, 1, 1, 13, 370.00, 0, 4810.00, 5, 12.35, 594.04, 'paid', 2, 1, ?, ?, ?, ?),
		       (22, 1, 1, 3, 12, 1250.50, 15, 38265.00, NULL, 5.00, 100.00, 'paid', 2, 1, ?, ?, ?, ?),
		       (23, 1, 1, 12, 12, 1250.50, 15, 153061.00, 5, 12.35, NULL, 'canceled', 2, NULL, NULL, NULL, ?, ?),
		       (24, 1, 1, 6, 12, 1250.50, 15, 76531.00, 5, 12.35, NULL, 'pending', 2, NULL, NULL, NULL, ?, ?)`,
		at(-50*time.Hour), at(670*time.Hour), at(-51*time.Hour), at(-50*time.Hour),
		at(-40*time.Hour), at(2000*time.Hour), at(-41*time.Hour), at(-40*time.Hour),
		at(-30*time.Hour), at(-29*time.Hour), at(-2*time.Hour), at(-2*time.Hour))
	// Бронь из приложения: два ПК одной оплатой (код один), тарифом; оплата — уже тиыны.
	execMy(t, my, `INSERT INTO computer_reservations (id, club_id, club_computer_id, user_id, from_app, paid_main_cents, paid_bonus_cents, tariff_id, code,
			starts_at, ends_at, created_by, created_at, updated_at)
		VALUES (501, 1, 1, 6, 1, 55000, 5000, 1, '482913', ?, ?, 6, ?, ?), (502, 1, 2, 6, 1, 60000, 0, 1, '482913', ?, ?, 6, ?, ?)`,
		at(24*time.Hour), at(25*time.Hour), at(-time.Hour), at(-time.Hour), at(24*time.Hour), at(25*time.Hour), at(-time.Hour), at(-time.Hour))
	// Фото: PNG и WebP на диске, у третьего файла нет (предупреждение, строка переносится, обложка — sort_order).
	storage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(storage, "club-photos", "1"), 0o750); err != nil {
		t.Fatal(err)
	}
	pngPath := "club-photos/1/" + strings.Repeat("a", 40) + ".png"
	webpPath := "club-photos/1/" + strings.Repeat("b", 40) + ".webp"
	lostPath := "club-photos/1/" + strings.Repeat("c", 40) + ".jpg"
	pngData := migratetest.PNG(t, 320, 240)
	webpData := append([]byte("RIFF\x1a\x00\x00\x00WEBPVP8L"), bytes.Repeat([]byte{7}, 14)...)
	for p, data := range map[string][]byte{pngPath: pngData, webpPath: webpData} {
		if err := os.WriteFile(filepath.Join(storage, filepath.FromSlash(p)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	execMy(t, my, `INSERT INTO club_photos (id, club_id, path, sort_order, created_at, updated_at) VALUES (31, 1, ?, 1, ?, ?), (32, 1, ?, 0, ?, ?), (33, 1, ?, 2, ?, ?)`,
		pngPath, at(-time.Hour), at(-time.Hour), webpPath, at(-time.Hour), at(-time.Hour), lostPath, at(-time.Hour), at(-time.Hour))

	popts := migrate.PlatformOptions{Now: now, IDReserve: idReserve}
	opts := func() migrate.Options {
		return migrate.Options{Club: "zafiro-arena", IDReserve: idReserve, Now: now, BatchSize: 3,
			Extensions: []migrate.Extension{migrate.Files()}, LaravelStorage: storage}
	}

	// --- Платформенный шаг: реселлеры, их аккаунты, выплаты, журнал денег платформы ---
	if r, err := migrate.PlatformPlan(ctx, my, owner, popts); err != nil || r.Count(migrate.Blocker) != 0 {
		t.Fatalf("platform plan: %v\n%s", err, reportText(r))
	}
	pr, err := migrate.PlatformApply(ctx, my, owner, popts)
	if err != nil {
		t.Fatalf("platform apply: %v\n%s", err, reportText(pr))
	}
	if got := pr.Stat("resellers").Converted["commission_bp"]; got != "базисные пункты ×100" {
		t.Fatalf("resellers.commission_bp: %q\n%s", got, reportText(pr))
	}
	if got := pr.Stat("reseller_payouts").Converted["amount"]; got != "тиыны ×100" {
		t.Fatalf("reseller_payouts.amount: %q", got)
	}
	if got := scalarPG[string](t, owner, `SELECT string_agg(id || ':' || user_id || ':' || code || ':' || commission_bp || ':' || discount_percent || ':' || is_active, ' ' ORDER BY id)
		FROM resellers`); got != "5:100:astana-pro:1235:10:true 6:102:no-clubs:1:0:false" {
		t.Fatalf("resellers: %s", got)
	}
	if got := scalarPG[string](t, owner, "SELECT string_agg(id || ':' || name, ' ' ORDER BY id) FROM users WHERE id IN (100, 101, 102)"); got != "100:Реселлер Первый 102:Реселлер Без клубов" {
		t.Fatalf("аккаунты реселлеров после платформенного шага: %s", got)
	}
	if got := scalarPG[string](t, owner, `SELECT string_agg(id || ':' || reseller_id || ':' || amount || ':' || coalesce(note, '-') || ':' || coalesce(created_by::text, '-'), ' ' ORDER BY id)
		FROM reseller_payouts`); got != "11:5:50000:Kaspi, ноябрь:1 12:5:9404:-:- 13:6:1:проверка:1" {
		t.Fatalf("reseller_payouts: %s", got)
	}
	payoutLog := `SELECT coalesce(string_agg(idempotency_key || '=' || amount || '@' || reseller_id, ' ' ORDER BY payout_id), '')
		FROM platform_money_log WHERE kind = 'reseller_payout'`
	if got := scalarPG[string](t, owner, payoutLog); got != "reseller_payout:11=50000@5 reseller_payout:12=9404@5 reseller_payout:13=1@6" {
		t.Fatalf("журнал выплат: %s", got)
	}
	if got, want := scalarPG[time.Time](t, owner, "SELECT created_at FROM platform_money_log WHERE payout_id = 11"),
		scalarPG[time.Time](t, owner, "SELECT created_at FROM reseller_payouts WHERE id = 11"); !got.Equal(want) {
		t.Fatalf("время выплаты в журнале: %s, выплата %s", got, want)
	}
	// Последовательности — выше id всей MySQL (Q-029): новый реселлер и выплата zafirogo не столкнутся со старыми.
	for _, tbl := range []string{"resellers", "reseller_payouts"} {
		if got := scalarPG[int64](t, owner, "SELECT nextval(pg_get_serial_sequence($1, 'id'))", tbl); got <= idReserve {
			t.Fatalf("последовательность %s: %d", tbl, got)
		}
	}
	// Повтор безопасен: ни строк, ни записей журнала не прибавилось; сверка без блокеров.
	if r, err := migrate.PlatformApply(ctx, my, owner, popts); err != nil {
		t.Fatalf("platform apply again: %v\n%s", err, reportText(r))
	}
	if got := scalarPG[string](t, owner, "SELECT (SELECT count(*) FROM resellers) || ' ' || (SELECT count(*) FROM reseller_payouts) || ' ' || (SELECT count(*) FROM platform_money_log)"); got != "2 3 3" {
		t.Fatalf("после повторного платформенного шага: %s", got)
	}
	pv, err := migrate.PlatformVerify(ctx, my, owner, popts)
	if err != nil {
		t.Fatalf("platform verify: %v\n%s", err, reportText(pv))
	}
	if got := pv.Totals["reseller_payouts.amount"]; got != "MySQL 594.05 / Postgres 594.05 / журнал 594.05" {
		t.Fatalf("сверка выплат: %q", got)
	}

	// Реселлер, появившийся в старой базе после платформенного шага: его счёт клуба переносит реселлера и аккаунт.
	execMy(t, my, `INSERT INTO resellers (id, user_id, code, name, commission_percent, discount_percent, is_active, created_at, updated_at)
		VALUES (7, 101, 'late-one', 'Поздний', 100.00, 100, 1, ?, ?)`, at(-5*time.Hour), at(-5*time.Hour))
	execMy(t, my, `INSERT INTO subscription_invoices (id, club_id, saas_plan_id, months, computers, price_per_computer, discount_percent, amount,
			reseller_id, reseller_percent, reseller_amount, status, created_by, confirmed_by, paid_at, period_ends_at, created_at, updated_at)
		VALUES (25, 1, 1, 1, 1, 999.99, 100, 0.00, 7, 100.00, 0.00, 'paid', 2, 1, ?, ?, ?, ?)`, at(-4*time.Hour), at(700*time.Hour), at(-4*time.Hour), at(-4*time.Hour))

	// --- Клуб: plan → apply → verify ---
	plan, err := migrate.Plan(ctx, my, owner, opts())
	if err != nil || plan.Count(migrate.Blocker) != 0 {
		t.Fatalf("plan: %v\n%s", err, reportText(plan))
	}
	requireFinding(t, plan, migrate.Warning, migrate.CodeFile) // фото без файла
	for col, want := range map[string]string{"amount": "тиыны ×100", "price_per_computer": "тиыны ×100", "reseller_amount": "тиыны ×100",
		"reseller_bp": "базисные пункты ×100"} {
		if got := plan.Stat("subscription_invoices").Converted[col]; got != want {
			t.Fatalf("subscription_invoices.%s: %q, want %q", col, got, want)
		}
	}
	// Оплата брони — уже тиыны: колонка не помечена как «× 100».
	if got, ok := plan.Stat("computer_reservations").Converted["paid_main_cents"]; ok {
		t.Fatalf("computer_reservations.paid_main_cents преобразуется: %q", got)
	}
	if scalarPG[bool](t, owner, "SELECT EXISTS (SELECT 1 FROM subscription_invoices) OR EXISTS (SELECT 1 FROM resellers WHERE id = 7)") {
		t.Fatal("plan что-то записал")
	}
	applied, err := migrate.Apply(ctx, my, owner, opts())
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, reportText(applied))
	}
	verified, err := migrate.Verify(ctx, my, owner, opts())
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, reportText(verified))
	}
	for name, want := range map[string]string{
		"subscription_invoices.amount":                        "MySQL 272667.00 / Postgres 272667.00",
		"subscription_invoices.reseller_amount":               "MySQL 694.04 / Postgres 694.04",
		"subscription_invoices.reseller_bp (базисные пункты)": "MySQL 14205 / Postgres 14205",
		"журнал денег платформы: оплаты счетов":               "MySQL 43075.00 / Postgres 43075.00",
		"журнал денег платформы: начисления реселлерам":       "MySQL 594.04 / Postgres 594.04",
		"журнал денег платформы: оплаченных счетов":           "MySQL 3 / Postgres 3",
		"computer_reservations.paid_main_cents (тиыны)":       "MySQL 115000 / Postgres 115000",
		"computer_reservations.paid_bonus_cents (тиыны)":      "MySQL 5000 / Postgres 5000",
		"computer_reservations: из приложения":                "MySQL 2 / Postgres 2",
	} {
		if got := verified.Totals[name]; got != want {
			t.Errorf("сверка %q: %q, want %q", name, got, want)
		}
	}

	// Клуб: город, адрес, профиль, реселлер; скидка подписки; оборудование зоны.
	if got := scalarPG[string](t, owner, `SELECT city || '|' || address || '|' || (profile->>'instagram') || '|' || (profile->>'hours') || '|' || reseller_id || '|' ||
		(reseller_attached_at = $1)::text FROM clubs WHERE id = 1`, now.Add(-60*time.Hour).Truncate(time.Second)); got != "aktobe|пр. Абилкайыр хана, 1|asia.game|Круглосуточно|5|true" {
		t.Fatalf("clubs: %s", got)
	}
	if got := scalarPG[string](t, owner, "SELECT discount_percent || '|' || discount_until::text || '|' || discount_note FROM club_subscriptions WHERE club_id = 1"); got != "15|2026-12-31|Первый клуб города" {
		t.Fatalf("club_subscriptions: %s", got)
	}
	if got := scalarPG[string](t, owner, "SELECT (devices->>'monitor') || '|' || (devices->>'mouse') || '|' || (SELECT count(*) FROM club_zones WHERE club_id = 1 AND devices IS NOT NULL) FROM club_zones WHERE id = 1"); got != "ASUS 240 Гц|Logitech G102|1" {
		t.Fatalf("club_zones.devices: %s", got)
	}
	// Счета: id (номер ZF-…) сохранён, суммы в тиынах, проценты в базисных пунктах, статусы и даты как были.
	if got := scalarPG[string](t, owner, `SELECT string_agg(concat_ws(':', id, months, computers, price_per_computer, discount_percent, amount, coalesce(reseller_id::text, '-'),
			coalesce(reseller_bp::text, '-'), coalesce(reseller_amount::text, '-'), status, coalesce(confirmed_by::text, '-'), (paid_at IS NOT NULL)::text), ' ' ORDER BY id)
		FROM subscription_invoices WHERE club_id = 1`); got != "21:1:13:37000:0:481000:5:1235:59404:paid:1:true "+
		"22:3:12:125050:15:3826500:-:500:10000:paid:1:true "+
		"23:12:12:125050:15:15306100:5:1235:-:canceled:-:false "+
		"24:6:12:125050:15:7653100:5:1235:-:pending:-:false "+
		"25:1:1:99999:100:0:7:10000:0:paid:1:true" {
		t.Fatalf("subscription_invoices:\n%s", got)
	}
	if got, want := scalarPG[time.Time](t, owner, "SELECT period_ends_at FROM subscription_invoices WHERE id = 21"), now.Add(670*time.Hour).Truncate(time.Second); !got.Equal(want) {
		t.Fatalf("period_ends_at: %s, want %s", got, want)
	}
	// Реселлер 7 и его аккаунт пришли вместе с клубом; реселлер 5 не задублирован и не изменён.
	if got := scalarPG[string](t, owner, "SELECT string_agg(id || ':' || code || ':' || commission_bp, ' ' ORDER BY id) FROM resellers"); got != "5:astana-pro:1235 6:no-clubs:1 7:late-one:10000" {
		t.Fatalf("resellers после клуба: %s", got)
	}
	if !scalarPG[bool](t, owner, "SELECT EXISTS (SELECT 1 FROM users WHERE id = 101 AND phone = '+77050000101')") {
		t.Fatal("аккаунт реселлера 7 не перенесён")
	}
	// Журнал денег платформы: оплата каждого оплаченного счёта; начисление — только где есть реселлер; время — оплаты.
	if got := scalarPG[string](t, owner, `SELECT string_agg(concat_ws(':', kind, idempotency_key, amount, coalesce(reseller_id::text, '-'), coalesce(actor_user_id::text, '-'), meta->>'source'), ' ' ORDER BY invoice_id, kind)
		FROM platform_money_log WHERE club_id = 1`); got != "invoice_paid:invoice_paid:21:481000:5:1:mysql reseller_accrual:reseller_accrual:21:59404:5:1:mysql "+
		"invoice_paid:invoice_paid:22:3826500:-:1:mysql "+
		"invoice_paid:invoice_paid:25:0:7:1:mysql reseller_accrual:reseller_accrual:25:0:7:1:mysql" {
		t.Fatalf("журнал денег платформы клуба:\n%s", got)
	}
	if !scalarPG[bool](t, owner, "SELECT l.created_at = i.paid_at FROM platform_money_log l JOIN subscription_invoices i ON i.id = l.invoice_id WHERE l.idempotency_key = 'invoice_paid:21'") {
		t.Fatal("время записи журнала ≠ времени оплаты счёта")
	}
	// Брони из приложения: оплата без × 100, код, тариф.
	if got := scalarPG[string](t, owner, `SELECT string_agg(concat_ws(':', id, club_computer_id, from_app::text, paid_main_cents, paid_bonus_cents, tariff_id, code), ' ' ORDER BY id)
		FROM computer_reservations WHERE club_id = 1 AND from_app`); got != "501:1:true:55000:5000:1:482913 502:2:true:60000:0:1:482913" {
		t.Fatalf("computer_reservations: %s", got)
	}
	// Фото: три строки (путь и порядок как были), два файла; обложка — первая по sort_order.
	if got := scalarPG[string](t, owner, `SELECT string_agg(p.id || ':' || p.sort_order || ':' || coalesce(i.content_type, '-'), ' ' ORDER BY p.sort_order, p.id)
		FROM club_photos p LEFT JOIN club_photo_images i ON i.photo_id = p.id WHERE p.club_id = 1`); got != "32:0:image/webp 31:1:image/png 33:2:-" {
		t.Fatalf("club_photos: %s", got)
	}
	if got := scalarPG[[]byte](t, owner, "SELECT data FROM club_photo_images WHERE photo_id = 31"); sha256.Sum256(got) != sha256.Sum256(pngData) {
		t.Fatal("байты фото отличаются от файла")
	}
	if got := scalarPG[string](t, owner, "SELECT path FROM club_photos WHERE id = 31"); got != pngPath {
		t.Fatalf("club_photos.path: %s", got)
	}
	if got := verified.Totals["картинки (логотип, вход, новости, фото клуба)"]; got != "2 из 3" {
		t.Fatalf("картинки: %q", got)
	}
	if got := scalarPG[int64](t, owner, "SELECT count(*) FROM zafiro_tables_without_rls()"); got != 0 {
		t.Fatalf("таблицы без RLS: %d", got)
	}
	// Новый счёт zafirogo получает id выше всех id старой базы (номер ZF-… не повторится).
	if got := scalarPG[int64](t, owner, "SELECT nextval(pg_get_serial_sequence('subscription_invoices', 'id'))"); got <= idReserve {
		t.Fatalf("последовательность subscription_invoices: %d", got)
	}

	// Тот же код реселлера у другого id в zafirogo — блокер, клуб 2 не переносится (и ничего не записано).
	execMy(t, my, `INSERT INTO resellers (id, user_id, code, name, commission_percent, discount_percent, is_active, created_at, updated_at)
		VALUES (8, 3, 'taken-code', 'Занятый код', 1.00, 0, 1, ?, ?)`, at(-time.Hour), at(-time.Hour))
	execMy(t, my, "UPDATE clubs SET reseller_id = 8 WHERE id = 2")
	if _, err := owner.Exec(ctx, `INSERT INTO resellers (id, user_id, code, name, created_at, updated_at) VALUES (900, 100, 'x', 'x', now(), now())`); err == nil {
		t.Fatal("код реселлера короче 3 знаков принят")
	}
	if _, err := owner.Exec(ctx, "UPDATE resellers SET code = 'taken-code' WHERE id = 6"); err != nil {
		t.Fatal(err)
	}
	lite := migrate.Options{Club: "zafiro-lite", IDReserve: idReserve, Now: now}
	if r, err := migrate.Apply(ctx, my, owner, lite); err == nil || !r.Has(migrate.Blocker, migrate.CodeResellerConflict) {
		t.Fatalf("apply клуба 2 с занятым кодом реселлера: %v\n%s", err, reportText(r))
	}
	if scalarPG[bool](t, owner, "SELECT EXISTS (SELECT 1 FROM clubs WHERE id = 2)") {
		t.Fatal("клуб 2 записан несмотря на блокер")
	}

	// Удаление перенесённого клуба до открытия (purge) убирает и его строки журнала денег платформы; выплаты
	// (платформа) остаются; повторный перенос записывает журнал заново, без дублей.
	if r, err := migrate.Purge(ctx, owner, "zafiro-arena", "zafiro-arena"); err != nil {
		t.Fatalf("purge: %v\n%s", err, reportText(r))
	}
	if got := scalarPG[string](t, owner, `SELECT (SELECT count(*) FROM platform_money_log WHERE club_id = 1) || ' ' || (SELECT count(*) FROM platform_money_log WHERE kind = 'reseller_payout')
		|| ' ' || (SELECT count(*) FROM subscription_invoices) || ' ' || (SELECT count(*) FROM club_photo_images) || ' ' || (SELECT count(*) FROM resellers WHERE id IN (5, 7))`); got != "0 3 0 0 2" {
		t.Fatalf("после purge: %s", got)
	}
	if r, err := migrate.Apply(ctx, my, owner, opts()); err != nil {
		t.Fatalf("apply после purge: %v\n%s", err, reportText(r))
	}
	if got := scalarPG[int64](t, owner, "SELECT count(*) FROM platform_money_log WHERE club_id = 1"); got != 5 {
		t.Fatalf("журнал после повторного переноса: %d", got)
	}
}
