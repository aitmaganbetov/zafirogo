//go:build integration

package webhook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox/natstest"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/webhook"
)

type received struct {
	headers http.Header
	body    []byte
}

// receiver — получатель вебхуков: первые fail запросов отвечает 500.
type receiver struct {
	mu    sync.Mutex
	fail  int32
	calls atomic.Int32
	got   []received
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.got = append(r.got, received{req.Header.Clone(), body})
	r.mu.Unlock()
	if r.calls.Add(1) <= r.fail {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testKeys — ключи данных тестов (security.data_keys): секрет в базе только зашифрованным (0025).
var testKeys, _ = secretbox.NewKeyring([]secretbox.Key{{ID: "t1", Secret: strings.Repeat("k", 32)}}, "")

// endpoint — вебхук с секретом, зашифрованным testKeys (как его сохраняет раздел integrations).
func endpoint(t *testing.T, d *dbtest.DB, club int64, url, secret string, events string) int64 {
	t.Helper()
	sealed, err := testKeys.SealWith(secret, webhook.SecretContext(club))
	if err != nil {
		t.Fatal(err)
	}
	return endpointRaw(t, d, club, url, sealed, events)
}

func endpointRaw(t *testing.T, d *dbtest.DB, club int64, url, secret string, events string) int64 {
	t.Helper()
	ctx := context.Background()
	var user, id int64
	if err := d.Owner.QueryRow(ctx,
		"INSERT INTO users (name, phone, password, created_at, updated_at) VALUES ('owner', '+7' || lpad(floor(random()*1e10)::bigint::text, 10, '0'), 'x', now(), now()) RETURNING id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := d.Owner.QueryRow(ctx,
		`INSERT INTO webhook_endpoints (club_id, created_by, url, secret, events, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5::jsonb, now(), now()) RETURNING id`, club, user, url, secret, events).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

type deliveryRow struct {
	status   string
	attempts int
	code     *int
	lastErr  *string
	eventID  string
}

func deliveries(t *testing.T, d *dbtest.DB, endpointID int64) []deliveryRow {
	t.Helper()
	rows, err := d.Owner.Query(context.Background(),
		"SELECT status, attempts, last_status_code, last_error, event_id::text FROM webhook_deliveries WHERE webhook_endpoint_id = $1 ORDER BY id", endpointID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []deliveryRow
	for rows.Next() {
		var r deliveryRow
		if err := rows.Scan(&r.status, &r.attempts, &r.code, &r.lastErr, &r.eventID); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func addEvent(t *testing.T, d *dbtest.DB, club int64, typ string, data any) string {
	t.Helper()
	var id string
	if err := db.InTenantTx(context.Background(), d.App, club, func(q *store.Queries) error {
		a, err := outbox.Add(context.Background(), q, outbox.Event{ClubID: club, Type: typ, Data: data})
		id = a.EventID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Событие из outbox → JetStream → durable consumer → доставка с HMAC; 500 → повтор по бэкоффу → 2xx.
func TestWebhookEndToEndWithRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d := dbtest.New(t)
	n := natstest.New(t, outbox.StreamName)
	club := d.Club(t, "wh-club")
	other := d.Club(t, "wh-other")

	recv := &receiver{fail: 1}
	srv := httptest.NewServer(recv)
	defer srv.Close()
	const secret = "whsec_test"
	ep := endpoint(t, d, club, srv.URL+"/hook", secret, `["sale.completed", "stock.changed"]`)
	notSubscribed := endpoint(t, d, club, srv.URL+"/other", secret, `["product.created"]`)
	foreign := endpoint(t, d, other, srv.URL+"/foreign", secret, `["sale.completed"]`)

	pub, _ := outbox.NewPublisher(outbox.PublisherOptions{Pool: d.Platform, JS: n.JS, Interval: 100 * time.Millisecond, LeaderRetry: 100 * time.Millisecond})
	go func() { _ = pub.Run(ctx) }()
	del, err := webhook.NewDeliverer(webhook.DelivererOptions{
		Platform: d.Platform, App: d.App, Secrets: testKeys,
		Guard:   webhook.Guard{AllowPrivate: true, Ports: []int{}}, // httptest: http, 127.0.0.1, случайный порт
		Backoff: []time.Duration{50 * time.Millisecond, 50 * time.Millisecond}, PollInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = del.Run(ctx) }()
	cons, err := webhook.NewConsumer(webhook.ConsumerOptions{JS: n.JS, App: d.App, Wake: del.Wake})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = cons.Run(ctx) }()

	// Данные как у кассы (cash/service/sale.go): price/total строк чека — float PHP (PHPFloat).
	eventID := addEvent(t, d, club, "sale.completed", map[string]any{"sale_id": 42, "amount": "1500.00", "amount_tiyn": 150000,
		"items": []map[string]any{{"name": "Кола", "quantity": 2, "price": json.Number("750.0"), "price_tiyn": 75000,
			"total": json.Number("1500.0"), "total_tiyn": 150000, "product_id": 7}}})
	addEvent(t, d, club, "computer.state", map[string]any{"computer_id": 1}) // не тип вебхука

	waitFor(t, "delivery delivered", func() bool {
		r := deliveries(t, d, ep)
		return len(r) == 1 && r[0].status == webhook.StatusDelivered
	})
	r := deliveries(t, d, ep)[0]
	if r.attempts != 2 || r.code == nil || *r.code != http.StatusNoContent || r.lastErr != nil {
		t.Fatalf("delivery = %+v", r)
	}
	if want := webhook.DeliveryID(eventID, ep).String(); r.eventID != want {
		t.Fatalf("delivery id = %s, want %s", r.eventID, want)
	}
	if len(deliveries(t, d, notSubscribed)) != 0 || len(deliveries(t, d, foreign)) != 0 {
		t.Fatal("deliveries to a not subscribed or foreign endpoint")
	}
	var failures int
	var lastSuccess *time.Time
	if err := d.Owner.QueryRow(ctx, "SELECT failure_count, last_success_at FROM webhook_endpoints WHERE id = $1", ep).Scan(&failures, &lastSuccess); err != nil {
		t.Fatal(err)
	}
	if failures != 0 || lastSuccess == nil {
		t.Fatalf("endpoint failure_count=%d last_success_at=%v", failures, lastSuccess)
	}

	recv.mu.Lock()
	got := append([]received(nil), recv.got...)
	recv.mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("receiver calls = %d, want 2 (500, затем 204)", len(got))
	}
	for _, g := range got {
		h := g.headers
		if !webhook.Verify(secret, h.Get(webhook.HeaderTimestamp), h.Get(webhook.HeaderSignature), g.body, time.Now(), webhook.DefaultSignatureTTL) {
			t.Fatalf("bad signature: %v", h)
		}
		if h.Get(webhook.HeaderEvent) != "sale.completed" || h.Get(webhook.HeaderDelivery) != r.eventID || h.Get("User-Agent") != webhook.UserAgent {
			t.Fatalf("headers: %v", h)
		}
		var body struct {
			ID        string         `json:"id"`
			Event     string         `json:"event"`
			CreatedAt string         `json:"created_at"`
			ClubID    int64          `json:"club_id"`
			Data      map[string]any `json:"data"`
		}
		if err := json.Unmarshal(g.body, &body); err != nil {
			t.Fatal(err)
		}
		if body.ID != r.eventID || body.Event != "sale.completed" || body.ClubID != club || body.Data["amount"] != "1500.00" {
			t.Fatalf("body: %s", g.body)
		}
		// Решение RQ-3 (docs/DECISIONS.md): суммы строкой с двумя знаками + *_tiyn, JSON-чисел с дробной частью нет.
		items, _ := body.Data["items"].([]any)
		item, _ := items[0].(map[string]any)
		if len(items) != 1 || item["price"] != "750.00" || item["total"] != "1500.00" || item["price_tiyn"] != float64(75000) {
			t.Fatalf("items: %s", g.body)
		}
		dec := json.NewDecoder(bytes.NewReader(g.body))
		dec.UseNumber()
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			if n, ok := tok.(json.Number); ok && strings.ContainsAny(n.String(), ".eE") {
				t.Fatalf("float %s in webhook body %s", n, g.body)
			}
		}
		if _, err := time.Parse("2006-01-02T15:04:05-07:00", body.CreatedAt); err != nil || body.CreatedAt[len(body.CreatedAt)-6:] != "+00:00" {
			t.Fatalf("created_at %q", body.CreatedAt)
		}
	}

	// Повторная обработка того же события (повторная доставка из JetStream) не создаёт вторую доставку.
	env := outbox.Envelope{ID: eventID, Type: "sale.completed", ClubID: club, CreatedAt: time.Now(), Data: json.RawMessage(`{}`)}
	if k, err := cons.Enqueue(ctx, env); err != nil || k != 0 {
		t.Fatalf("re-enqueue: %d %v", k, err)
	}
}

// Все попытки неудачны → failed, failure_count++; при пороге endpoint выключается; выключенный — skipped.
func TestWebhookFailureDisablesEndpoint(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)
	club := d.Club(t, "wh-fail")
	recv := &receiver{fail: 1 << 20}
	srv := httptest.NewServer(recv)
	defer srv.Close()
	ep := endpoint(t, d, club, srv.URL, "s", `["stock.changed"]`)
	var disabled atomic.Int64
	del, _ := webhook.NewDeliverer(webhook.DelivererOptions{
		Platform: d.Platform, App: d.App, Guard: webhook.Guard{AllowPrivate: true, Ports: []int{}}, Secrets: testKeys,
		Backoff: []time.Duration{time.Millisecond, time.Millisecond}, DisableAfter: 1,
		OnEndpointDisabled: func(_, id int64) { disabled.Store(id) },
	})
	n := natstest.New(t, outbox.StreamName)
	cons, _ := webhook.NewConsumer(webhook.ConsumerOptions{JS: n.JS, App: d.App})

	enqueue := func(id string) {
		t.Helper()
		k, err := cons.Enqueue(ctx, outbox.Envelope{ID: id, Type: "stock.changed", ClubID: club, CreatedAt: time.Now(), Data: json.RawMessage(`{"product_id":1}`)})
		if err != nil || k != 1 {
			t.Fatalf("enqueue: %d %v", k, err)
		}
	}
	enqueue("00000000-0000-0000-0000-000000000001")
	waitFor(t, "failed", func() bool {
		if _, err := del.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		r := deliveries(t, d, ep)
		return r[0].status == webhook.StatusFailed
	})
	r := deliveries(t, d, ep)[0]
	if r.attempts != 3 || r.lastErr == nil || *r.lastErr != "HTTP 500" || recv.calls.Load() != 3 {
		t.Fatalf("failed delivery = %+v, calls %d", r, recv.calls.Load())
	}
	var active bool
	var failures int
	if err := d.Owner.QueryRow(ctx, "SELECT is_active, failure_count FROM webhook_endpoints WHERE id = $1", ep).Scan(&active, &failures); err != nil {
		t.Fatal(err)
	}
	if active || failures != 1 || disabled.Load() != ep {
		t.Fatalf("endpoint active=%v failures=%d disabled=%d", active, failures, disabled.Load())
	}

	// Выключенный endpoint больше не слушает; доставка, созданная до выключения, — skipped.
	if k, err := cons.Enqueue(ctx, outbox.Envelope{ID: "00000000-0000-0000-0000-000000000002", Type: "stock.changed", ClubID: club, CreatedAt: time.Now(), Data: json.RawMessage(`{}`)}); err != nil || k != 0 {
		t.Fatalf("inactive endpoint got a delivery: %d %v", k, err)
	}
	if _, err := d.Owner.Exec(ctx, "UPDATE webhook_endpoints SET is_active = true WHERE id = $1", ep); err != nil {
		t.Fatal(err)
	}
	enqueue("00000000-0000-0000-0000-000000000003")
	if _, err := d.Owner.Exec(ctx, "UPDATE webhook_endpoints SET is_active = false WHERE id = $1", ep); err != nil {
		t.Fatal(err)
	}
	if _, err := del.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if r := deliveries(t, d, ep); r[1].status != webhook.StatusSkipped || r[1].attempts != 0 {
		t.Fatalf("skipped = %+v", r[1])
	}

	// SSRF: без разрешения частных сетей адрес 127.0.0.1 блокируется.
	strict, _ := webhook.NewDeliverer(webhook.DelivererOptions{Platform: d.Platform, App: d.App, Secrets: testKeys,
		Guard: webhook.Guard{Ports: []int{}}, Backoff: []time.Duration{time.Hour}})
	if _, err := d.Owner.Exec(ctx, "UPDATE webhook_endpoints SET is_active = true WHERE id = $1", ep); err != nil {
		t.Fatal(err)
	}
	enqueue("00000000-0000-0000-0000-000000000004")
	calls := recv.calls.Load()
	if _, err := strict.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if r := deliveries(t, d, ep); r[2].status != webhook.StatusPending || r[2].lastErr == nil || *r[2].lastErr != "blocked: private_address" || recv.calls.Load() != calls {
		t.Fatalf("ssrf = %+v", r[2])
	}
}

// Q-050: секрет в базе зашифрован, подпись — открытым секретом; после ротации ключа данных (новый первым, старый для
// расшифровки) доставка подписывается тем же секретом; без ключа — доставка не уходит; открытый текст в столбец не
// записывается (CHECK webhook_endpoints_secret_sealed).
func TestWebhookSecretEncrypted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d := dbtest.New(t)
	club := d.Club(t, "wh-sealed")
	recv := &receiver{}
	srv := httptest.NewServer(recv)
	defer srv.Close()
	const secret = "whsec_sealed_secret"
	ep := endpoint(t, d, club, srv.URL, secret, `["stock.changed"]`)

	var stored string
	if err := d.Owner.QueryRow(ctx, "SELECT secret FROM webhook_endpoints WHERE id = $1", ep).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, secret) || !strings.HasPrefix(stored, "v2:t1:") {
		t.Fatalf("stored secret %q", stored)
	}
	if _, err := d.Owner.Exec(ctx, "UPDATE webhook_endpoints SET secret = 'whsec_plain' WHERE id = $1", ep); err == nil {
		t.Fatal("plaintext secret accepted by CHECK")
	}

	n := natstest.New(t, outbox.StreamName)
	cons, _ := webhook.NewConsumer(webhook.ConsumerOptions{JS: n.JS, App: d.App})
	deliver := func(id string, keys *secretbox.Box) deliveryRow {
		t.Helper()
		if _, err := cons.Enqueue(ctx, outbox.Envelope{ID: id, Type: "stock.changed", ClubID: club, CreatedAt: time.Now(), Data: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		del, _ := webhook.NewDeliverer(webhook.DelivererOptions{Platform: d.Platform, App: d.App, Secrets: keys,
			Guard: webhook.Guard{AllowPrivate: true, Ports: []int{}}, Backoff: []time.Duration{time.Hour}})
		if _, err := del.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		r := deliveries(t, d, ep)
		return r[len(r)-1]
	}
	verifyLast := func() {
		t.Helper()
		recv.mu.Lock()
		g := recv.got[len(recv.got)-1]
		recv.mu.Unlock()
		if !webhook.Verify(secret, g.headers.Get(webhook.HeaderTimestamp), g.headers.Get(webhook.HeaderSignature), g.body, time.Now(), webhook.DefaultSignatureTTL) {
			t.Fatalf("bad signature: %v", g.headers)
		}
	}
	if r := deliver("00000000-0000-0000-0000-0000000000a1", testKeys); r.status != webhook.StatusDelivered {
		t.Fatalf("delivery = %+v", r)
	}
	verifyLast()

	rotated, _ := secretbox.NewKeyring([]secretbox.Key{{ID: "t2", Secret: strings.Repeat("n", 32)}, {ID: "t1", Secret: strings.Repeat("k", 32)}}, "")
	if r := deliver("00000000-0000-0000-0000-0000000000a2", rotated); r.status != webhook.StatusDelivered {
		t.Fatalf("delivery after rotation = %+v", r)
	}
	verifyLast()

	unknown, _ := secretbox.NewKeyring([]secretbox.Key{{ID: "t2", Secret: strings.Repeat("n", 32)}}, "")
	calls := recv.calls.Load()
	if r := deliver("00000000-0000-0000-0000-0000000000a3", unknown); r.lastErr == nil || *r.lastErr != "secret unavailable" || recv.calls.Load() != calls {
		t.Fatalf("delivery without key = %+v", r)
	}
}
