//go:build integration

package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/authtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram/telegramtest"
)

const botToken = "123456:ABCdefGhIJKlmNoPQRsTUVwxyZ0123456789"

var (
	tdb  *dbtest.DB
	vk   valkey.Client
	box  *secretbox.Box
	tseq atomic.Int64
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	d, c1, err := dbtest.Start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	v, c2, err := authtest.StartValkey(ctx)
	if err != nil {
		c1()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tdb, vk = d, v
	if box, err = secretbox.New(strings.Repeat("m", 32)); err != nil {
		panic(err)
	}
	code := m.Run()
	c2()
	c1()
	os.Exit(code)
}

type staticOnline map[int64]int64

func (s staticOnline) OnlineByClub(context.Context, time.Time) (map[int64]int64, error) {
	return s, nil
}

// fakeAlertBot — бот оповещений подключён (чат 42, язык ru), Telegram поддельный. Каждому тесту — своя база
// состояния (префикс Valkey); таблицы метрик очищаются.
func newWorkers(t *testing.T, connected bool) (*Workers, *telegramtest.Server) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{"DELETE FROM platform_settings WHERE key = 'telegram_alerts'", "DELETE FROM server_metrics",
		"DELETE FROM request_metrics", "DELETE FROM club_load_snapshots"} {
		if _, err := tdb.Owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if connected {
		sealed, _ := box.Seal(botToken)
		raw, _ := json.Marshal(map[string]string{"token": sealed, "chat_id": "42", "locale": "ru"})
		if _, err := tdb.Owner.Exec(ctx, "INSERT INTO platform_settings (key, value, created_at, updated_at) VALUES ('telegram_alerts', $1, now(), now())", raw); err != nil {
			t.Fatal(err)
		}
	}
	tg := telegramtest.New(t)
	bot := &telegram.Alerts{API: &telegram.BotClient{URL: tg.BotURL()}, Store: &telegram.Store{Pool: tdb.Platform, Box: box}}
	return &Workers{Platform: tdb.Platform, Recorder: NewRecorder(nil),
		Alerts: &Alerts{Valkey: vk, Prefix: fmt.Sprintf("t%d:alert.", tseq.Add(1)), Bot: bot}}, tg
}

func wantMessages(t *testing.T, tg *telegramtest.Server, want ...string) {
	t.Helper()
	got := tg.Messages()
	if len(got) != len(want) {
		t.Fatalf("messages = %q, want %q", got, want)
	}
	for i := range want {
		if !strings.Contains(got[i], want[i]) {
			t.Fatalf("message %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func i64(v int64) *int64 { return &v }

// GS-392: оповещение один раз при начале проблемы и один раз по окончании (очередь).
func TestAlerts_QueueOnceStartOnceEnd(t *testing.T) {
	w, tg := newWorkers(t, true)
	ctx := context.Background()
	now := time.Now()
	full := Snapshot{QueuePending: 100, QueueOldestSeconds: i64(30)}
	for range 2 {
		if err := w.checkServer(ctx, now, full); err != nil {
			t.Fatal(err)
		}
	}
	wantMessages(t, tg, "⚠️ Копится очередь: 100 заданий ждут, самое старое — 0 мин.")
	if err := w.checkServer(ctx, now.Add(time.Minute), Snapshot{}); err != nil {
		t.Fatal(err)
	}
	wantMessages(t, tg, "Копится очередь: 100 заданий", "✅ Очередь разобрана.")
	if r := tg.Requests()[0]; r.Token != botToken || r.Body["chat_id"] != "42" || r.Body["disable_web_page_preview"] != true {
		t.Fatalf("request = %+v", r)
	}
}

// GS-393: доля 5xx за 5 минут.
func TestAlerts_5xxShare(t *testing.T) {
	w, tg := newWorkers(t, true)
	ctx := context.Background()
	now := time.Now()
	if _, err := tdb.Owner.Exec(ctx, "INSERT INTO request_metrics (minute, channel, requests, errors, duration_ms) VALUES ($1, 'panel', 100, 20, 1000)", now.Unix()/60-1); err != nil {
		t.Fatal(err)
	}
	if err := w.checkServer(ctx, now, Snapshot{}); err != nil {
		t.Fatal(err)
	}
	wantMessages(t, tg, "Ошибки 5xx: 20% запросов за 5 минут (20 из 100).")
}

// GS-394: «планировщик молчит» замечает любой инстанс; задача сбора сообщает, что он снова работает.
func TestAlerts_SilentScheduler(t *testing.T) {
	w, tg := newWorkers(t, true)
	ctx := context.Background()
	now := time.Now()
	if err := w.Watchdog(ctx, now); err != nil { // снимков нет — ничего
		t.Fatal(err)
	}
	if _, err := tdb.Owner.Exec(ctx, "INSERT INTO server_metrics (minute) VALUES ($1)", now.Unix()/60-10); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := w.Watchdog(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	wantMessages(t, tg, "Планировщик молчит 10 мин")
	w.Probe = Probe{Root: "/nonexistent-zafiro", Proc: t.TempDir()} // без диска машины теста
	if _, err := w.Collect(ctx, now); err != nil {
		t.Fatal(err)
	}
	wantMessages(t, tg, "Планировщик молчит 10 мин", "✅ Планировщик снова работает.")
}

// Диск: (int) round(used * 100 / total) > 85.
func TestAlerts_Disk(t *testing.T) {
	w, tg := newWorkers(t, true)
	ctx := context.Background()
	if err := w.checkServer(ctx, time.Now(), Snapshot{DiskTotalMB: i64(1000), DiskUsedMB: i64(855)}); err != nil {
		t.Fatal(err)
	}
	if err := w.checkServer(ctx, time.Now(), Snapshot{DiskTotalMB: i64(1000), DiskUsedMB: i64(500)}); err != nil {
		t.Fatal(err)
	}
	wantMessages(t, tg, "⚠️ Диск сервера заполнен на 86%.", "✅ Место на диске освободилось: занято 50%.")
}

// Бот не подключён: состояние ведётся, сообщений нет (Alerts::update() без configured()).
func TestAlerts_BotNotConnected(t *testing.T) {
	w, tg := newWorkers(t, false)
	ctx := context.Background()
	if err := w.checkServer(ctx, time.Now(), Snapshot{QueuePending: 100}); err != nil {
		t.Fatal(err)
	}
	if open, err := w.Alerts.IsOpen(ctx, "queue"); err != nil || !open {
		t.Fatalf("queue open = %v, %v", open, err)
	}
	wantMessages(t, tg)
}

// GS-395: все ПК клуба с подпиской ушли офлайн (было ≥ 3) — оповещение; снова в сети — «восстановлено».
func TestClubSnapshots_HeatMapOfflineAlert(t *testing.T) {
	w, tg := newWorkers(t, true)
	ctx := context.Background()
	club := tdb.Club(t, fmt.Sprintf("mon%d", tseq.Add(1)))
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tdb.Owner.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO saas_plans (slug, name, features, created_at, updated_at) VALUES ($1, 'План', '{}'::jsonb, now(), now())`, fmt.Sprintf("p%d", tseq.Add(1)))
	exec(`INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, current_period_ends_at)
		VALUES ($1, (SELECT max(id) FROM saas_plans), 'active', now(), NULL)`, club)
	var zone int64
	if err := tdb.Owner.QueryRow(ctx, "INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Hall', now(), now()) RETURNING id", club).Scan(&zone); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		exec("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, $3, now(), now())", club, zone, i)
	}
	t0 := time.Now().Truncate(time.Minute)
	w.Online = staticOnline{club: 4}
	if _, err := w.SnapshotClubs(ctx, t0); err != nil {
		t.Fatal(err)
	}
	w.Online = staticOnline{}
	if _, err := w.SnapshotClubs(ctx, t0.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantMessages(t, tg, "все ПК ушли офлайн (было в сети: 4). Возможно, у клуба пропал интернет.")
	if _, err := w.SnapshotClubs(ctx, t0.Add(10*time.Minute)); err != nil { // всё ещё офлайн — без повтора
		t.Fatal(err)
	}
	w.Online = staticOnline{club: 3}
	if _, err := w.SnapshotClubs(ctx, t0.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantMessages(t, tg, "все ПК ушли офлайн", "снова в сети: ПК онлайн — 3.")
}
