//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/aitmaganbetov/zafirogo/server/internal/app"
	"github.com/aitmaganbetov/zafirogo/server/internal/config"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate/migratetest"
	"github.com/aitmaganbetov/zafirogo/server/internal/webhook"
)

func imageOr(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// hookCatcher — получатель вебхуков на 127.0.0.x:80 (порт из WebhookUrlGuard::check()); "" — порт не занять.
type hookCatcher struct {
	url string
	got chan *http.Request
}

func catchHooks(t *testing.T) hookCatcher {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.37:80")
	if err != nil {
		t.Logf("webhook receiver: %v — доставка новым сервером не проверяется (только HMAC)", err)
		return hookCatcher{}
	}
	h := hookCatcher{url: "http://127.0.0.37/hook", got: make(chan *http.Request, 4)}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		h.got <- r
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return h
}

// Сквозная проверка (Этап 5, M2 п. 7): MySQL с демо-данными → platform apply → apply клуба → verify (0 расхождений)
// → новый сервер (app.Build + Run) на перенесённой базе: вход сотрудника по старому паролю (bcrypt), X-отчёт открытой
// смены = касса MySQL, токен мобильного приложения из personal_access_tokens, ключ интегратора в /api/v1, вебхук
// подписан расшифрованным секретом (HMAC тот же, что дал бы секрет старого сервера).
func TestMigratedClubOnNewServer(t *testing.T) {
	ctx := context.Background()
	my, mysqlDSN := migratetest.StartMySQL(t)
	pg := dbtest.New(t)
	now := time.Now().UTC()
	data := migratetest.InjectM2(t, my, now)
	// Демо-подписка закончилась 01.11.2026; переводимый клуб — действующий (иначе 402 до кассы). Заморозка перед
	// переводом — как в README: «Приостановить подписку» в суперадминке старого Laravel (suspend()).
	if _, err := my.Exec("UPDATE club_subscriptions SET current_period_ends_at = ?, status = 'suspended', canceled_at = ? WHERE club_id = 1",
		now.Add(30*24*time.Hour).Format(time.DateTime), now.Format(time.DateTime)); err != nil {
		t.Fatal(err)
	}
	hooks := catchHooks(t)
	if hooks.url != "" {
		// Вебхук клуба — на локальный получатель, одна доставка в очереди старого сервера.
		if _, err := my.Exec("UPDATE webhook_endpoints SET url = ? WHERE id = ?", hooks.url, data.WebhookID); err != nil {
			t.Fatal(err)
		}
		if _, err := my.Exec(`INSERT INTO webhook_deliveries (club_id, webhook_endpoint_id, event_id, event, payload, status, attempts,
			next_attempt_at, created_at, updated_at)
			VALUES (1, ?, '0c4b7b0e-5e2f-4bd7-9a52-3d6b8f9b0002', 'stock.changed', '{"event": "stock.changed", "data": {"quantity": 7}}',
			'pending', 0, ?, ?, ?)`, data.WebhookID, now.Add(-time.Minute).Format(time.DateTime),
			now.Add(-time.Minute).Format(time.DateTime), now.Add(-time.Minute).Format(time.DateTime)); err != nil {
			t.Fatal(err)
		}
	}

	dataKeys := "k2026:" + strings.Repeat("s", 40)
	t.Setenv("ZAFIRO_MIGRATE_MYSQL_DSN", mysqlDSN)
	t.Setenv("ZAFIRO_MIGRATE_PG_DSN", pg.Owner.Config().ConnString())
	t.Setenv("ZAFIRO_MIGRATE_LARAVEL_APP_KEY", data.Fixture.AppKey)
	t.Setenv("ZAFIRO_MIGRATE_LARAVEL_PREVIOUS_KEYS", data.Fixture.PreviousKeys)
	t.Setenv("ZAFIRO_SECURITY_DATA_KEYS", dataKeys)
	seed, _ := filepath.Abs("../../../db/seed")
	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"platform", "apply", "--laravel-storage", data.Storage, "--translations-seed", seed}, "Итого: блокеров 0"},
		{[]string{"platform", "verify"}, "Итого: блокеров 0"},
		{[]string{"plan", "--club", "zafiro-arena", "--laravel-storage", data.Storage, "--thaw-subscription", "active"}, "заморозка снята"},
		{[]string{"apply", "--club", "zafiro-arena", "--laravel-storage", data.Storage, "--thaw-subscription", "active"}, "записано"},
		{[]string{"verify", "--club", "zafiro-arena", "--laravel-storage", data.Storage}, "Итого: блокеров 0"},
	} {
		var out, errOut bytes.Buffer
		if code := run(step.args, &out, &errOut); code != exitOK {
			t.Fatalf("%v: код %d\n%s\n%s", step.args, code, out.String(), errOut.String())
		}
		if !strings.Contains(out.String(), step.want) {
			t.Fatalf("%v: нет %q\n%s", step.args, step.want, out.String())
		}
		if strings.Contains(out.String()+errOut.String(), data.WebhookSecret) || strings.Contains(out.String(), data.TelegramToken) {
			t.Fatalf("%v: секрет в выводе", step.args)
		}
	}

	// --- Новый сервер на перенесённой базе ---
	if _, err := db.EnsurePartitions(ctx, pg.Owner, now, 2); err != nil {
		t.Fatal(err)
	}
	user, pass := "platform_e2e_"+randHex(t, 4), randHex(t, 12)
	if _, err := pg.Owner.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' IN ROLE zafiro_platform", user, pass)); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(pg.AppDSN)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, pass)
	vk, err := testcontainers.Run(ctx, imageOr("ZAFIRO_TEST_VALKEY_IMAGE", "valkey/valkey:9.1.3-alpine"),
		testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("6379/tcp").WithStartupTimeout(60*time.Second)))
	if err != nil {
		t.Fatalf("valkey: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(vk) })
	valkeyAddr, err := vk.PortEndpoint(ctx, "6379/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	nc, err := tcnats.Run(ctx, imageOr("ZAFIRO_TEST_NATS_IMAGE", "nats:2.15.2-alpine"))
	if err != nil {
		t.Fatalf("nats: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(nc) })
	natsURL, err := nc.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"ZAFIRO_ENV=test", "ZAFIRO_HTTP_ADDR=127.0.0.1:0", "ZAFIRO_AGENT_GRPC_LISTEN=127.0.0.1:0",
		"ZAFIRO_AGENT_INSTANCE_ID=e2e-" + randHex(t, 3), "ZAFIRO_AGENT_PKI_DEV_DIR=" + filepath.Join(t.TempDir(), "pki"),
		"ZAFIRO_DATABASE_URL=" + pg.AppDSN, "ZAFIRO_DATABASE_PLATFORM_URL=" + u.String(),
		"ZAFIRO_VALKEY_ADDR=" + valkeyAddr, "ZAFIRO_NATS_URL=" + natsURL, "ZAFIRO_SHUTDOWN_TIMEOUT=20s",
		"ZAFIRO_SECURITY_DATA_KEYS=" + dataKeys,
		"ZAFIRO_WEBHOOKS_REQUIRE_HTTPS=false", "ZAFIRO_WEBHOOKS_ALLOW_PRIVATE_HOSTS=true", "ZAFIRO_WEBHOOKS_POLL_INTERVAL=200ms",
	}
	cfg, err := config.Load("", func() []string { return env })
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	runCtx, cancel := context.WithCancel(ctx)
	a, err := app.Build(runCtx, cfg, log)
	if err != nil {
		cancel()
		t.Fatalf("Build: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(40 * time.Second):
			t.Error("Run did not return")
		}
	})
	base := "http://" + a.HTTPAddr()

	call := func(method, path, auth string, body any) (int, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// 1. Вход владельца демо-клуба по старому паролю (bcrypt Laravel, перехеш — Q-051).
	code, sess := call(http.MethodPost, "/api/auth/login", "", map[string]any{"phone": "+77010000002", "password": "zafiro-demo-2026"})
	if code != http.StatusOK {
		t.Fatalf("login: %d %v", code, sess)
	}
	access, _ := sess["access_token"].(string)
	if access == "" {
		t.Fatalf("login: no access token: %v", sess)
	}
	if !strings.HasPrefix(scalar[string](t, pg, "SELECT password FROM users WHERE id = 2"), "$argon2id$") {
		t.Error("password not rehashed to Argon2id after login")
	}

	// 2. X-отчёт открытой смены = касса MySQL (остаток, приходы, расходы, виды прихода).
	code, x := call(http.MethodGet, "/api/cash/reports/x", access, nil)
	if code != http.StatusOK {
		t.Fatalf("x-report: %d %v", code, x)
	}
	totals, _ := x["totals"].(map[string]any)
	var opening, income, expense string
	if err := my.QueryRow(`SELECT s.opening_balance,
		  COALESCE(SUM(CASE WHEN t.type = 'income' THEN t.amount END), 0), COALESCE(SUM(CASE WHEN t.type = 'expense' THEN t.amount END), 0)
		FROM cash_shifts s LEFT JOIN cash_transactions t ON t.cash_shift_id = s.id
		WHERE s.club_id = 1 AND s.status = 'open' GROUP BY s.id, s.opening_balance`).Scan(&opening, &income, &expense); err != nil {
		t.Fatal(err)
	}
	tiyn := func(s string) float64 {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatal(err)
		}
		return float64(int64(f*100 + 0.5))
	}
	for name, want := range map[string]float64{
		"opening_tiyn": tiyn(opening), "income_tiyn": tiyn(income), "expense_tiyn": tiyn(expense),
		"balance_tiyn": tiyn(opening) + tiyn(income) - tiyn(expense),
	} {
		if totals[name] != want {
			t.Errorf("x-report %s = %v, MySQL %v", name, totals[name], want)
		}
	}
	var topUps, guests int64
	if err := my.QueryRow(`SELECT
		  SUM(EXISTS (SELECT 1 FROM activity_logs l WHERE l.event = 'cash.income' AND l.subject_id = t.id AND l.subject_type = 'App\\Models\\CashTransaction'
		              AND json_unquote(json_extract(l.details, '$."balance_type"')) IS NOT NULL)),
		  SUM(EXISTS (SELECT 1 FROM activity_logs l WHERE l.event = 'cash.income' AND l.subject_id = t.id AND l.subject_type = 'App\\Models\\CashTransaction'
		              AND json_unquote(json_extract(l.details, '$."computers"')) IS NOT NULL))
		FROM cash_transactions t JOIN cash_shifts s ON s.id = t.cash_shift_id WHERE s.club_id = 1 AND s.status = 'open' AND t.type = 'income'`).
		Scan(&topUps, &guests); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]float64{}
	list, _ := x["kinds"].([]any)
	for _, k := range list {
		row, _ := k.(map[string]any)
		label, _ := row["label"].(string)
		kinds[label], _ = row["count"].(float64)
	}
	if kinds["Пополнение балансов"] != float64(topUps) || kinds["Гостевые сессии"] != float64(guests) || guests != 1 {
		t.Errorf("x-report kinds %v, MySQL top-ups %d, guests %d", kinds, topUps, guests)
	}

	// 3. Токен мобильного приложения (personal_access_tokens, тот же id) принимает слой старого REST.
	code, me := call(http.MethodGet, "/api/auth/me", strconv.FormatInt(data.MobileTokenID, 10)+"|"+data.MobileToken, nil)
	if code != http.StatusOK {
		t.Fatalf("mobile /api/auth/me: %d %v", code, me)
	}
	if u, _ := me["user"].(map[string]any); u == nil || u["id"] != float64(9) {
		t.Fatalf("mobile me: %v", me)
	}

	// 4. Ключ интегратора — /api/v1 (тот же SHA-256, без перевыпуска).
	code, v1me := call(http.MethodGet, "/api/v1/me", data.APIKey, nil)
	if code != http.StatusOK {
		t.Fatalf("/api/v1/me: %d %v", code, v1me)
	}

	// 5. Вебхук из очереди старого сервера доставлен с подписью расшифрованным секретом.
	if hooks.url == "" {
		return
	}
	select {
	case r := <-hooks.got:
		body, _ := io.ReadAll(r.Body)
		ts := r.Header.Get(webhook.HeaderTimestamp)
		if !webhook.Verify(data.WebhookSecret, ts, r.Header.Get(webhook.HeaderSignature), body, time.Now(), 5*time.Minute) {
			t.Fatalf("webhook signature does not verify with the old secret (event %s)", r.Header.Get(webhook.HeaderEvent))
		}
	case <-time.After(30 * time.Second):
		t.Fatal("webhook not delivered")
	}
}

func scalar[T any](t *testing.T, pg *dbtest.DB, q string, args ...any) T {
	t.Helper()
	var v T
	if err := pg.Owner.QueryRow(context.Background(), q, args...).Scan(&v); err != nil {
		t.Fatalf("pg: %s: %v", q, err)
	}
	return v
}
