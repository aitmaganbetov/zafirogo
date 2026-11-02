//go:build integration

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/enroll"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// scrape читает /metrics внутреннего порта.
func scrape(t *testing.T, addr string) string {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/metrics") //nolint:noctx // тест
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics = %d", resp.StatusCode)
	}
	return string(b)
}

// metricValue — значение ряда name{…} с указанными метками (подстроки внутри фигурных скобок); -1 — нет ряда.
func metricValue(body, name string, labels ...string) float64 {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+"{") && !strings.HasPrefix(line, name+" ") {
			continue
		}
		ok := true
		for _, l := range labels {
			if !strings.Contains(line, l) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		var v float64
		if _, err := fmt.Sscan(line[strings.LastIndex(line, " ")+1:], &v); err == nil {
			return v
		}
	}
	return -1
}

// TestObservabilityMetrics — сервер с внутренним портом метрик: вход с неверным паролем, операции ledger (фиксация,
// откат, ошибка базы), регистрация ПК и поток агента; на /metrics появляются соответствующие ряды, в метках нет
// идентификаторов клубов, пользователей и ПК; публичный HTTP-порт /metrics не отдаёт.
func TestObservabilityMetrics(t *testing.T) {
	e := startEnv(t)
	pkiDir := filepath.Join(t.TempDir(), "pki")
	r := start(t, e.config(t, pkiDir, "ZAFIRO_OBSERVABILITY_METRICS_LISTEN=127.0.0.1:0"))
	a := r.app
	ctx := context.Background()
	base := "http://" + a.HTTPAddr()
	if a.MetricsAddr() == "" || a.MetricsAddr() == a.HTTPAddr() {
		t.Fatalf("metrics addr = %q", a.MetricsAddr())
	}

	// Публичный порт /metrics не обслуживает (SPA или 404, но не текст метрик).
	if resp, err := http.Get(base + "/metrics"); err == nil { //nolint:noctx // тест
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if strings.Contains(string(b), "zafiro_http_requests_total") {
			t.Fatal("/metrics is exposed on the public HTTP port")
		}
	}

	clubID, staffID := fixtures(t, e.db)
	var phone string
	if err := e.db.Owner.QueryRow(ctx, `SELECT phone FROM users WHERE id = $1`, staffID).Scan(&phone); err != nil {
		t.Fatal(err)
	}

	// Неудачный вход (пароль в фикстуре — не хеш, вход невозможен).
	resp, err := http.Post(base+"/api/auth/login", "application/json", //nolint:noctx // тест
		strings.NewReader(fmt.Sprintf(`{"phone":%q,"password":"wrong-password"}`, phone)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("login with wrong password = %d", resp.StatusCode)
	}

	// Деньги: пополнение владельцем (committed), то же с откатом транзакции (rolled_back) и ошибка запроса ledger
	// на стороне базы (CHECK вида операции).
	if _, err := e.db.Owner.Exec(ctx, `INSERT INTO member_balances (club_id, user_id, balance, bonus_balance, created_at, updated_at)
		VALUES ($1, $2, 0, 0, now(), now()) ON CONFLICT DO NOTHING`, clubID, staffID); err != nil {
		t.Fatal(err)
	}
	errRollback := errors.New("rollback")
	post := func(key string, fail bool) error {
		return db.InTenantTx(ctx, a.AppPool, clubID, func(q *store.Queries) error {
			if _, err := ledger.Post(ctx, q, ledger.Operation{
				ClubID: clubID, Kind: ledger.KindOwnerTopUp, IdempotencyKey: key,
				Entries: []ledger.Entry{
					{Account: ledger.Main(staffID), Amount: 10000},
					{Account: ledger.ClubAccount(ledger.AccountOwnerAdjustment), Amount: -10000},
				},
			}); err != nil {
				return err
			}
			if fail {
				return errRollback
			}
			return nil
		})
	}
	if err := post("obs-topup-"+randHex(t, 4), false); err != nil {
		t.Fatalf("top up: %v", err)
	}
	if err := post("obs-rollback-"+randHex(t, 4), true); !errors.Is(err, errRollback) {
		t.Fatalf("rolled back top up: %v", err)
	}
	if err := db.InTenantTx(ctx, a.AppPool, clubID, func(q *store.Queries) error {
		return q.InsertLedgerTransaction(ctx, store.InsertLedgerTransactionParams{
			ID: 1 << 40, ClubID: clubID, Kind: "bogus", IdempotencyKey: "obs-bogus", CreatedAt: time.Now(),
		})
	}); err == nil {
		t.Fatal("bogus ledger kind must fail")
	}

	// Агент: регистрация (unary gRPC) и поток Connect.
	issued, err := a.Enroll.CreateCode(ctx, principal.Principal{UserID: staffID, ClubID: clubID, Role: "admin"},
		enroll.CodeRequest{IP: netip.MustParseAddr("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	p := newPC(t, pkiDir, a.GRPCAddr())
	anon := p.dial(t, "")
	enr, err := agentv1.NewEnrollmentServiceClient(anon).Enroll(ctx, &agentv1.EnrollRequest{
		EnrollmentCode: issued.Code, CsrPem: p.csr,
		Device: &agentv1.DeviceInfo{HardwareUuid: "U-OBS", MacAddress: "AA:BB:CC:00:00:09", Hostname: "DESKTOP-OBS"},
	})
	_ = anon.Close()
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	conn := p.dial(t, enr.GetCertificatePem())
	defer conn.Close()
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	st, err := agentv1.NewAgentServiceClient(conn).Connect(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Send(hello()); err != nil {
		t.Fatal(err)
	}
	msgs, _ := recv(st)
	select {
	case <-msgs:
	case <-time.After(10 * time.Second):
		t.Fatal("no Welcome")
	}

	var body string
	want := []struct {
		name   string
		labels []string
	}{
		{"zafiro_http_requests_total", []string{`route="/api/auth/login"`}},
		{"zafiro_auth_logins_total", []string{`result="failed"`, `reason="invalid_credentials"`}},
		{"zafiro_ledger_operations_total", []string{`kind="owner_top_up"`, `result="committed"`}},
		{"zafiro_ledger_operations_total", []string{`kind="owner_top_up"`, `result="rolled_back"`}},
		{"zafiro_ledger_errors_total", []string{`query="InsertLedgerTransaction"`}},
		{"zafiro_grpc_requests_total", []string{`method="/zafiro.agent.v1.EnrollmentService/Enroll"`, `code="OK"`}},
		{"zafiro_grpc_streams_active", []string{`method="/zafiro.agent.v1.AgentService/Connect"`}},
		{"zafiro_agent_streams_active", nil},
		{"zafiro_agent_online_computers", nil},
		{"zafiro_outbox_oldest_unpublished_age_seconds", nil},
		{"zafiro_webhook_deliveries_pending", nil},
		{"zafiro_db_pool_acquired_connections", []string{`pool="app"`}},
		{"zafiro_worker_runs_total", []string{`job="agent:auto-billing"`}},
		{"zafiro_worker_leader", []string{`job="outbox-publisher"`}},
		{"zafiro_worker_up", []string{`worker="outbox-publisher"`}},
		{"zafiro_tls_certificate_expiry_timestamp_seconds", []string{`cert="agent_ca"`}},
		{"zafiro_tls_certificate_expiry_timestamp_seconds", []string{`cert="agent_server"`}},
		{"zafiro_nats_connected", nil},
		{"zafiro_valkey_commands_total", nil},
		{"go_goroutines", nil},
	}
	waitFor(t, "metrics", 15*time.Second, func() bool {
		body = scrape(t, a.MetricsAddr())
		for _, w := range want {
			if metricValue(body, w.name, w.labels...) < 0 {
				return false
			}
		}
		return metricValue(body, "zafiro_agent_online_computers") >= 1 && metricValue(body, "zafiro_agent_streams_active") >= 1 &&
			metricValue(body, "zafiro_worker_leader", `job="outbox-publisher"`) == 1
	})
	for _, w := range want {
		if metricValue(body, w.name, w.labels...) < 0 {
			t.Errorf("missing %s%v", w.name, w.labels)
		}
	}

	// Ни одной метки с идентификаторами тенанта/людей/ПК и ни одного значения, похожего на id.
	forbidden := regexp.MustCompile(`(?i)[{,](club|user|customer|computer|pc|phone|session)[a-z_]*="`)
	ids := regexp.MustCompile(fmt.Sprintf(`[{,]([a-z_]+)="[^"]*\b(%d|%d|%d)\b[^"]*"`, clubID, staffID, enr.GetComputerId()))
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if forbidden.MatchString(line) {
			t.Errorf("forbidden label: %s", line)
		}
		for _, m := range ids.FindAllStringSubmatch(line, -1) {
			if m[1] != "le" && m[1] != "quantile" && m[1] != "code" && m[1] != "version" {
				t.Errorf("label value contains an id: %s", line)
			}
		}
	}

	scancel()
	_ = conn.Close()
	if err := r.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
