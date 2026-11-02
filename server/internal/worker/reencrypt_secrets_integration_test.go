//go:build integration

package worker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/webhook"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker"
)

// Перешифровка (Q-050, миграция 0025): открытые секреты вебхуков (строки до 0025), v2 со старым ключом и токены
// Telegram v1 (superadmin.secret_key Этапа 3) — текущим ключом; повтор — без изменений; нерасшифровываемое — как есть.
func TestReencryptSecrets(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)
	club := d.Club(t, "reenc")
	other := d.Club(t, "reenc-2")

	legacySecret := strings.Repeat("L", 32)
	k1 := secretbox.Key{ID: "k1", Secret: strings.Repeat("1", 32)}
	k2 := secretbox.Key{ID: "k2", Secret: strings.Repeat("2", 32)}
	oldRing, _ := secretbox.NewKeyring([]secretbox.Key{k1}, "")
	v1, _ := secretbox.New(legacySecret)
	box, err := secretbox.NewKeyring([]secretbox.Key{k2, k1}, legacySecret)
	if err != nil {
		t.Fatal(err)
	}

	// База до 0025: ограничение ещё не проверено для старых строк.
	if _, err := d.Owner.Exec(ctx, `ALTER TABLE webhook_endpoints DROP CONSTRAINT webhook_endpoints_secret_sealed`); err != nil {
		t.Fatal(err)
	}
	var user int64
	if err := d.Owner.QueryRow(ctx, "INSERT INTO users (name, phone, password, created_at, updated_at) VALUES ('o', '+77000000991', 'x', now(), now()) RETURNING id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	insert := func(clubID int64, secret string) int64 {
		t.Helper()
		var id int64
		if err := d.Owner.QueryRow(ctx, `INSERT INTO webhook_endpoints (club_id, created_by, url, secret, events, created_at, updated_at)
			VALUES ($1, $2, 'https://example.com/h', $3, '[]'::jsonb, now(), now()) RETURNING id`, clubID, user, secret).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	plain := insert(club, "whsec_plain")
	oldSealed, _ := oldRing.SealWith("whsec_old", webhook.SecretContext(other))
	old := insert(other, oldSealed)
	garbage := insert(club, "v2:gone:AAAA")
	if _, err := d.Owner.Exec(ctx, `ALTER TABLE webhook_endpoints ADD CONSTRAINT webhook_endpoints_secret_sealed CHECK (secret ~ '^v[0-9]+:') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	tg, _ := v1.Seal("123456:telegram-token")
	if _, err := d.Owner.Exec(ctx, `INSERT INTO platform_settings (key, value, created_at, updated_at)
		VALUES ('telegram_alerts', jsonb_build_object('token', $1::text, 'chat_id', '-100'), now(), now())`, tg); err != nil {
		t.Fatal(err)
	}

	w := &worker.ReencryptSecrets{Platform: d.Platform, Box: box}
	ran, res, err := w.Tick(ctx)
	if err != nil || !ran || res.Webhooks != 2 || res.Settings != 1 || res.Failed != 1 {
		t.Fatalf("tick: ran=%v res=%+v err=%v", ran, res, err)
	}
	secretOf := func(id int64) string {
		t.Helper()
		var s string
		if err := d.Owner.QueryRow(ctx, "SELECT secret FROM webhook_endpoints WHERE id = $1", id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for id, want := range map[int64]struct {
		club  int64
		plain string
	}{plain: {club, "whsec_plain"}, old: {other, "whsec_old"}} {
		s := secretOf(id)
		if !strings.HasPrefix(s, "v2:k2:") || strings.Contains(s, want.plain) {
			t.Fatalf("endpoint %d secret %q", id, s)
		}
		if got, err := box.OpenWith(s, webhook.SecretContext(want.club)); err != nil || got != want.plain {
			t.Fatalf("endpoint %d: %q %v", id, got, err)
		}
	}
	if secretOf(garbage) != "v2:gone:AAAA" {
		t.Fatal("undecryptable secret changed")
	}
	var token, chat string
	if err := d.Owner.QueryRow(ctx, "SELECT value->>'token', value->>'chat_id' FROM platform_settings WHERE key = 'telegram_alerts'").Scan(&token, &chat); err != nil {
		t.Fatal(err)
	}
	if got, err := box.Open(token); err != nil || got != "123456:telegram-token" || !strings.HasPrefix(token, "v2:k2:") || chat != "-100" {
		t.Fatalf("telegram token %q → %q %v, chat %q", token, got, err, chat)
	}

	// Повтор: всё уже текущим ключом (кроме нерасшифровываемого).
	if _, res, err := w.Tick(ctx); err != nil || res.Webhooks != 0 || res.Settings != 0 || res.Failed != 1 {
		t.Fatalf("second tick: %+v %v", res, err)
	}
	// Без открытых секретов ограничение проверяется целиком (следующая миграция делает то же).
	if _, err := d.Owner.Exec(ctx, "DELETE FROM webhook_endpoints WHERE id = $1", garbage); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Owner.Exec(ctx, "ALTER TABLE webhook_endpoints VALIDATE CONSTRAINT webhook_endpoints_secret_sealed"); err != nil {
		t.Fatal(err)
	}
}
