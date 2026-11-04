//go:build staging

// Проверки поднятого staging (deploy/staging, docs/deploy.md §9): task staging:up && task staging:seed && task staging:smoke.
// Не входит ни в обычные, ни в интеграционные тесты (тег staging): работает против запущенного окружения из образа и
// управляет его контейнерами (docker compose stop/start). Клиент агента — как в app_integration_test.go.
package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
)

// Адреса staging (deploy/staging/compose.yaml).
const (
	stgDomain     = "staging.zafiro.test"
	stgNginxIP    = "172.29.66.10"
	stgStaffPhone = "+77010000002" // владелец клуба 1 (tools/visual/fixtures/demo.json)
	stgGuestPhone = "+77011000009" // клиент клуба 1 с балансом
	stgPassword   = "zafiro-demo-2026"
	stgZoneID     = 2 // «Основной зал», 600 ₸/ч
)

var stgInstances = map[string]string{"app1": "http://127.0.0.1:18081", "app2": "http://127.0.0.1:18082"}

type stgEnv struct {
	dir   string
	vars  map[string]string
	web   *http.Client
	pg    *pgxpool.Pool
	vk    valkey.Client
	roots *x509.CertPool // корень PKI агентов
}

func stgSetup(t *testing.T) *stgEnv {
	t.Helper()
	dir := os.Getenv("ZAFIRO_STAGING_DIR")
	if dir == "" {
		t.Skip("ZAFIRO_STAGING_DIR не задан (task staging:smoke)")
	}
	e := &stgEnv{dir: dir, vars: stgReadEnv(t, filepath.Join(dir, ".env.staging"))}

	webCA, err := os.ReadFile(filepath.Join(dir, "secrets/web/ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	webRoots := x509.NewCertPool()
	webRoots.AppendCertsFromPEM(webCA)
	httpsPort := e.v("STG_HTTPS_PORT", "443")
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	e.web = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		Proxy:           nil, // staging на этой машине: мимо HTTPS_PROXY среды
		TLSClientConfig: &tls.Config{RootCAs: webRoots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, _, _ := net.SplitHostPort(addr); strings.HasSuffix(host, stgDomain) {
				addr = net.JoinHostPort("127.0.0.1", httpsPort) // DNS staging-доменов — на nginx
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}}

	rootPEM, err := os.ReadFile(filepath.Join(dir, "secrets/pki/root-ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	e.roots = x509.NewCertPool()
	e.roots.AppendCertsFromPEM(rootPEM)

	ctx := context.Background()
	dsn := fmt.Sprintf("postgres://%s:%s@127.0.0.1:15432/%s?sslmode=disable", e.v("STG_DB_OWNER_USER", ""), e.v("STG_DB_OWNER_PASSWORD", ""), e.v("STG_POSTGRES_DB", "zafiro"))
	if e.pg, err = pgxpool.New(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.pg.Close)
	if e.vk, err = valkey.NewClient(valkey.ClientOption{InitAddress: []string{"127.0.0.1:16379"}, Password: e.v("STG_VALKEY_PASSWORD", ""), DisableCache: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.vk.Close)
	return e
}

func (e *stgEnv) v(k, def string) string {
	if v := e.vars[k]; v != "" {
		return v
	}
	return def
}

func stgReadEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			out[k] = v
		}
	}
	return out
}

// compose — docker compose staging (stop/start инстансов).
func (e *stgEnv) compose(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"compose", "-f", filepath.Join(e.dir, "compose.yaml"), "--env-file", filepath.Join(e.dir, ".env.staging")}, args...)...) // #nosec G204 -- тест staging
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
}

func (e *stgEnv) api(t *testing.T, method, url, token string, body any) (int, []byte) {
	t.Helper()
	code, b, err := e.apiE(method, url, token, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return code, b
}

// ready — /readyz инстанса отвечает 200 (ошибки соединения — «не готов»).
func (e *stgEnv) ready(name string) bool {
	code, _, err := e.apiE(http.MethodGet, stgInstances[name]+"/readyz", "", nil)
	return err == nil && code == http.StatusOK
}

func (e *stgEnv) apiE(method, url, token string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.web.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}

func (e *stgEnv) login(t *testing.T) string {
	t.Helper()
	code, b := e.api(t, http.MethodPost, "https://app."+stgDomain+"/api/auth/login", "", map[string]any{"phone": stgStaffPhone, "password": stgPassword})
	if code != http.StatusOK {
		t.Fatalf("login = %d %s", code, b)
	}
	var s struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(b, &s); err != nil || s.AccessToken == "" {
		t.Fatalf("login body %s: %v", b, err)
	}
	return s.AccessToken
}

// wsOpen — WebSocket с билетом; base — https://app… (через nginx) или http://127.0.0.1:1808x (инстанс напрямую).
func (e *stgEnv) wsOpen(t *testing.T, token, base string) *websocket.Conn {
	t.Helper()
	code, b := e.api(t, http.MethodPost, "https://app."+stgDomain+"/api/ws-ticket", token, nil)
	if code != http.StatusOK {
		t.Fatalf("ws-ticket = %d %s", code, b)
	}
	var tk struct {
		Ticket string `json:"ticket"`
	}
	_ = json.Unmarshal(b, &tk)
	u := strings.Replace(base, "http", "ws", 1) + "/ws?ticket=" + tk.Ticket
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: e.web})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("ws %s: %v", base, err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

// wsWaitType ждёт сообщение типа typ (с подстрокой want в data).
func wsWaitType(ctx context.Context, c *websocket.Conn, typ, want string) error {
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return err
		}
		var m struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(b, &m) == nil && m.Type == typ && strings.Contains(string(m.Data), want) {
			return nil
		}
	}
}

// ---------------------------------------------------------------------------
// Агент
// ---------------------------------------------------------------------------

type stgPC struct {
	e    *stgEnv
	key  *ecdsa.PrivateKey
	csr  string
	cert string
	addr string
}

func (e *stgEnv) newPC(t *testing.T) *stgPC {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, k)
	if err != nil {
		t.Fatal(err)
	}
	return &stgPC{e: e, key: k, csr: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})),
		addr: "127.0.0.1:" + e.v("STG_AGENT_PORT", "8443")}
}

// dial — канал через nginx (L4, PROXY protocol до сервера), TLS с проверкой имени agent.<домен> по корню PKI.
func (p *stgPC) dial(t *testing.T, withCert bool) *grpc.ClientConn {
	t.Helper()
	cfg := &tls.Config{RootCAs: p.e.roots, ServerName: "agent." + stgDomain, MinVersion: tls.VersionTLS12}
	if withCert {
		c, err := pki.ParseCertificatePEM([]byte(p.cert))
		if err != nil {
			t.Fatal(err)
		}
		cfg.Certificates = []tls.Certificate{{Certificate: [][]byte{c.Raw}, PrivateKey: p.key, Leaf: c}}
	}
	conn, err := grpc.NewClient("passthrough:///"+p.addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)), grpc.WithNoProxy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// stgStream — поток Connect: сообщения сервера в канал.
type stgStream struct {
	conn   *grpc.ClientConn
	cancel context.CancelFunc
	msgs   chan *agentv1.ServerMessage
	errc   chan error
}

func (p *stgPC) connect(t *testing.T, bootID string) (*stgStream, *agentv1.Welcome) {
	t.Helper()
	s, w, err := p.connectE(t, bootID)
	if err != nil {
		t.Fatal(err)
	}
	return s, w
}

func (p *stgPC) connectE(t *testing.T, bootID string) (*stgStream, *agentv1.Welcome, error) {
	t.Helper()
	conn := p.dial(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	st, err := agentv1.NewAgentServiceClient(conn).Connect(ctx)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	if err := st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Hello{Hello: &agentv1.Hello{
		ProtocolVersion: 1, AgentVersion: "2.0.0", BootId: bootID,
		Capabilities: &agentv1.AgentCapabilities{PowerControl: true, ShowMessage: true},
		Device:       &agentv1.DeviceInfo{Hostname: "STAGING-PC", LocalIp: "192.168.1.50"},
		Status:       &agentv1.Status{State: agentv1.ComputerState_COMPUTER_STATE_IDLE},
	}}}); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("hello: %w", err)
	}
	s := &stgStream{conn: conn, cancel: cancel, msgs: make(chan *agentv1.ServerMessage, 64), errc: make(chan error, 1)}
	go func() {
		defer close(s.msgs)
		for {
			m, err := st.Recv()
			if err != nil {
				s.errc <- err
				return
			}
			s.msgs <- m
		}
	}()
	select {
	case m := <-s.msgs:
		if m.GetWelcome() == nil {
			cancel()
			return nil, nil, fmt.Errorf("expected Welcome, got %v", m)
		}
		return s, m.GetWelcome(), nil
	case err := <-s.errc:
		cancel()
		return nil, nil, fmt.Errorf("stream closed before Welcome: %w", err)
	case <-time.After(15 * time.Second):
		cancel()
		return nil, nil, fmt.Errorf("no Welcome")
	}
}

// owner — инстанс, который держит поток ПК (Valkey zafiro:agent:pc:<id> = "<instance>|<gen>", docs/agent-protocol.md §13).
func (e *stgEnv) owner(t *testing.T, computerID int64) string {
	t.Helper()
	v, err := e.vk.Do(context.Background(), e.vk.B().Get().Key("zafiro:agent:pc:"+strconv.FormatInt(computerID, 10)).Build()).ToString()
	if err != nil {
		return ""
	}
	inst, _, _ := strings.Cut(v, "|")
	return inst
}

func (e *stgEnv) balance(t *testing.T, phone string) int64 {
	t.Helper()
	var b int64
	if err := e.pg.QueryRow(context.Background(), `SELECT mb.balance FROM member_balances mb JOIN users u ON u.id = mb.user_id
		WHERE mb.club_id = 1 AND u.phone = $1`, phone).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func stgUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func guestCtx(token string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
}

// ---------------------------------------------------------------------------
// Сценарий
// ---------------------------------------------------------------------------

func TestStaging(t *testing.T) {
	e := stgSetup(t)

	t.Run("readyz обоих инстансов", func(t *testing.T) {
		for name := range stgInstances {
			if !e.ready(name) {
				t.Errorf("%s /readyz is not 200", name)
			}
		}
	})

	token := e.login(t)
	t.Run("вход в панель через nginx", func(t *testing.T) {
		code, b := e.api(t, http.MethodGet, "https://app."+stgDomain+"/api/auth/me", token, nil)
		if code != http.StatusOK || !strings.Contains(string(b), stgStaffPhone) {
			t.Fatalf("me = %d %s", code, b)
		}
	})

	t.Run("WebSocket: событие доходит на инстанс сокета, когда действие на другом", func(t *testing.T) {
		viaLB := e.wsOpen(t, token, "https://app."+stgDomain)
		on1 := e.wsOpen(t, token, stgInstances["app1"])
		on2 := e.wsOpen(t, token, stgInstances["app2"])
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		for _, c := range []*websocket.Conn{viaLB, on1, on2} {
			if err := wsWaitType(ctx, c, "hello", ""); err != nil {
				t.Fatalf("hello: %v", err)
			}
		}
		cancel()
		// Действие — на app2 напрямую; сокет on1 открыт на app1: путь outbox → NATS → app1.
		title := "staging-smoke " + stgUUID(t)[:8]
		code, b := e.api(t, http.MethodPost, stgInstances["app2"]+"/api/todos", token, map[string]any{
			"title": title, "due_on": time.Now().AddDate(0, 0, 2).Format("2006-01-02"), // будущий день в любом поясе клуба
		})
		if code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("create todo on app2 = %d %s", code, b)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var wg sync.WaitGroup
		for name, c := range map[string]*websocket.Conn{"nginx": viaLB, "app1": on1, "app2": on2} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := wsWaitType(ctx, c, "todo.changed", `"created"`); err != nil {
					t.Errorf("socket %s: todo.changed not received: %v", name, err)
				}
			}()
		}
		wg.Wait()
	})

	// Агент: код → Enroll через nginx (L4) → Connect по mTLS.
	pc := e.newPC(t)
	var computerID int64
	t.Run("агент: регистрация через L4-проброс, адрес ПК из PROXY protocol", func(t *testing.T) {
		code, b := e.api(t, http.MethodPost, "https://app."+stgDomain+"/api/agent-enrollment/codes", token, map[string]any{"zone_id": stgZoneID})
		if code != http.StatusCreated {
			t.Fatalf("create code = %d %s", code, b)
		}
		var issued struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(b, &issued)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		mac := fmt.Sprintf("02:00:00:%s:%s:%s", stgUUID(t)[0:2], stgUUID(t)[2:4], stgUUID(t)[4:6])
		resp, err := agentv1.NewEnrollmentServiceClient(pc.dial(t, false)).Enroll(ctx, &agentv1.EnrollRequest{
			EnrollmentCode: issued.Code, CsrPem: pc.csr,
			Device: &agentv1.DeviceInfo{HardwareUuid: "STG-" + stgUUID(t), MacAddress: strings.ToUpper(mac), Hostname: "STAGING-PC",
				AgentVersion: "2.0.0", OsVersion: "Windows 11 Pro"},
		})
		if err != nil {
			t.Fatalf("Enroll: %v", err)
		}
		pc.cert, computerID = resp.GetCertificatePem(), resp.GetComputerId()
		if resp.GetAgentEndpoint() != "agent."+stgDomain+":8443" {
			t.Errorf("agent_endpoint = %q", resp.GetAgentEndpoint())
		}
		// IP в журнале регистрации — адрес клиента до nginx (PROXY protocol), а не адрес nginx.
		var ipText string
		if err := e.pg.QueryRow(context.Background(), `SELECT ip_address FROM activity_logs WHERE club_id = 1
			AND event = 'computer.registered' AND subject_id = $1 ORDER BY id DESC LIMIT 1`, computerID).Scan(&ipText); err != nil {
			t.Fatalf("activity computer.registered: %v", err)
		}
		ip, err := netip.ParseAddr(ipText)
		if err != nil || ip.String() == stgNginxIP {
			t.Fatalf("enroll IP = nginx address %s: PROXY protocol not applied", ip)
		}
		t.Logf("computer %d enrolled, client IP seen by server: %s", computerID, ip)
	})
	if computerID == 0 {
		t.FailNow()
	}
	// Уборка: тестовый ПК отключается (повторный прогон регистрирует новый).
	t.Cleanup(func() {
		code, b, err := e.apiE(http.MethodPost, fmt.Sprintf("https://app.%s/api/agent-enrollment/computers/%d/revoke", stgDomain, computerID), token, nil)
		if err != nil || code >= 300 {
			t.Logf("revoke test computer %d: %d %s %v", computerID, code, b, err)
		}
	})

	stream, welcome := pc.connect(t, "boot-1")
	if welcome.GetComputerId() != computerID {
		t.Fatalf("Welcome computer = %d, want %d", welcome.GetComputerId(), computerID)
	}
	var first string
	waitStg(t, "presence owner", 10*time.Second, func() bool { first = e.owner(t, computerID); return first != "" })
	t.Logf("stream of computer %d is held by %s", computerID, first)

	// Гость входит на ПК и покупает минуты (идемпотентно).
	guest := agentv1.NewGuestServiceClient(stream.conn)
	lr, err := agentv1.NewGuestAuthServiceClient(stream.conn).Login(context.Background(), &agentv1.LoginRequest{Phone: stgGuestPhone, Password: stgPassword})
	if err != nil {
		t.Fatalf("guest Login: %v", err)
	}
	gtoken := lr.GetSignIn().GetToken().GetAccessToken()
	// Выход гостя в конце (иначе следующий прогон получит «Вы уже вошли на другом ПК»).
	t.Cleanup(func() {
		if _, err := agentv1.NewGuestAuthServiceClient(pc.dial(t, true)).Logout(guestCtx(gtoken), &agentv1.LogoutRequest{}); err != nil {
			t.Logf("guest Logout: %v", err)
		}
	})
	before := e.balance(t, stgGuestPhone)
	key1 := stgUUID(t)
	p1, err := guest.PurchaseMinutes(guestCtx(gtoken), &agentv1.PurchaseMinutesRequest{IdempotencyKey: key1, Minutes: 10, BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN})
	if err != nil {
		t.Fatalf("PurchaseMinutes: %v", err)
	}
	charge1 := p1.GetPurchase().GetChargedMainTiyn()
	if charge1 <= 0 || e.balance(t, stgGuestPhone) != before-charge1 {
		t.Fatalf("charge1 = %d, balance %d → %d", charge1, before, e.balance(t, stgGuestPhone))
	}

	t.Run("остановка инстанса-владельца: Drain, переподключение ко второму, покупка не теряется и не задваивается", func(t *testing.T) {
		other := map[string]string{"app1": "app2", "app2": "app1"}[first]
		stopped := make(chan time.Duration, 1)
		go func() {
			start := time.Now()
			e.compose(t, "stop", first) // SIGTERM → graceful shutdown (stop_grace_period 45 с)
			stopped <- time.Since(start)
		}()
		t.Cleanup(func() {
			e.compose(t, "start", first)
			waitStg(t, first+" /readyz", 60*time.Second, func() bool { return e.ready(first) })
		})

		// Покупка в момент остановки: ответ мог потеряться — повтор с тем же ключом на новом соединении.
		key2 := stgUUID(t)
		ctx, cancel := context.WithTimeout(guestCtx(gtoken), 5*time.Second)
		_, errDuring := guest.PurchaseMinutes(ctx, &agentv1.PurchaseMinutesRequest{IdempotencyKey: key2, Minutes: 10, BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN})
		cancel()
		t.Logf("purchase during shutdown: err=%v", errDuring)

		// Агент получает Drain (или поток закрывается) и переподключается.
		gotDrain := false
	wait:
		for {
			select {
			case m, ok := <-stream.msgs:
				if !ok {
					break wait
				}
				if m.GetCommand().GetDrain() != nil {
					gotDrain = true
					break wait
				}
			case <-time.After(30 * time.Second):
				t.Fatal("no Drain and stream still open")
			}
		}
		if !gotDrain {
			t.Logf("stream closed without Drain: %v", <-stream.errc)
		}
		stream.cancel()
		_ = stream.conn.Close()

		var s2 *stgStream
		waitStg(t, "reconnect to "+other, 30*time.Second, func() bool {
			s, w, err := pc.connectE(t, "boot-1")
			if err != nil {
				t.Logf("reconnect: %v", err)
				return false
			}
			if w.GetComputerId() != computerID {
				return false
			}
			s2 = s
			return true
		})
		defer s2.cancel()
		waitStg(t, "owner = "+other, 15*time.Second, func() bool { return e.owner(t, computerID) == other })

		g2 := agentv1.NewGuestServiceClient(s2.conn)
		retry, err := g2.PurchaseMinutes(guestCtx(gtoken), &agentv1.PurchaseMinutesRequest{IdempotencyKey: key2, Minutes: 10, BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN})
		if err != nil {
			t.Fatalf("retry key2 on %s: %v", other, err)
		}
		again, err := g2.PurchaseMinutes(guestCtx(gtoken), &agentv1.PurchaseMinutesRequest{IdempotencyKey: key1, Minutes: 10, BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN})
		if err != nil || again.GetPurchase().GetChargedMainTiyn() != charge1 {
			t.Fatalf("replay key1 = %v, %v", again, err)
		}
		charge2 := retry.GetPurchase().GetChargedMainTiyn()
		if after := e.balance(t, stgGuestPhone); after != before-charge1-charge2 {
			t.Fatalf("balance = %d, want %d (before %d, charges %d + %d): operation lost or duplicated", after, before-charge1-charge2, before, charge1, charge2)
		}
		var ops int
		if err := e.pg.QueryRow(context.Background(), `SELECT count(*) FROM ledger_transactions WHERE club_id = 1
			AND idempotency_key LIKE '%' || $1 || '%'`, key2).Scan(&ops); err == nil {
			t.Logf("ledger transactions with key2: %d", ops)
		}

		select {
		case d := <-stopped:
			t.Logf("docker compose stop %s took %s (graceful budget 30s)", first, d.Round(100*time.Millisecond))
			if d > 40*time.Second {
				t.Errorf("stop took %s: graceful shutdown did not finish in budget", d)
			}
		case <-time.After(60 * time.Second):
			t.Fatal("docker compose stop did not finish")
		}
		var exit string
		out, _ := exec.Command("docker", "inspect", "-f", "{{.State.ExitCode}}", "zafirogo-staging-"+first+"-1").Output() // #nosec G204 -- тест staging
		exit = strings.TrimSpace(string(out))
		if exit != "0" {
			t.Errorf("%s exit code = %s, want 0 (graceful)", first, exit)
		}
	})
}

func waitStg(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
