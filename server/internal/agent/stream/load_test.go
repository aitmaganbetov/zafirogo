//go:build integration && load

// Нагрузочный тест стрима агента (не в CI):
//
//	cd server && go test -tags 'integration load' -run '^TestLoad$' -v -timeout 30m ./internal/agent/stream/
//
// Сервер (Service + gRPC без TLS на 127.0.0.1, идентичность ПК — тестовым перехватчиком) работает в ОТДЕЛЬНОМ
// процессе, чтобы CPU/RAM клиентов не попадали в замер. Родитель поднимает Postgres/Valkey/NATS, заводит
// ZAFIRO_LOAD_PCS ПК (по умолчанию 10 000), открывает по TCP-соединению и стриму на ПК, шлёт Status раз в
// ZAFIRO_LOAD_STATUS_EVERY (30 с) и замеряет за ZAFIRO_LOAD_WINDOW (60 с): CPU и RSS процесса сервера,
// горутины и кучу Go, записи в Postgres (pg_stat_user_tables) и время доставки команды.
//
// ZAFIRO_LOAD_TLS=1.2|1.3 — стримы по mTLS, как в production: серверный TLS grpcserver.TLSConfig (dev-PKI во временном
// каталоге), у каждого ПК свой клиентский сертификат ECDSA P-256 от выпускающего CA (pki.CA.Issue), версия TLS
// клиента ограничена указанной (агенты на Windows 10 — TLS 1.2). Идентичность ПК по-прежнему — тестовый перехватчик.
package stream

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	mrand "math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/valkey-io/valkey-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
)

// loadTLSVersion — ZAFIRO_LOAD_TLS: "" (без TLS), "1.2" или "1.3".
func loadTLSVersion(t *testing.T) uint16 {
	switch v := os.Getenv("ZAFIRO_LOAD_TLS"); v {
	case "":
		return 0
	case "1.2":
		return tls.VersionTLS12
	case "1.3", "1":
		return tls.VersionTLS13
	default:
		t.Fatalf("ZAFIRO_LOAD_TLS=%q: ожидается 1.2 или 1.3", v)
		return 0
	}
}

// clientTLS — mTLS клиента ПК: свой ключ ECDSA P-256 и сертификат от выпускающего CA, доверие — dev-корню.
func clientTLS(t *testing.T, ca *pki.CA, roots *x509.CertPool, club, pc int64, version uint16) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		t.Fatal(err)
	}
	iss, err := ca.Issue(pki.IssueRequest{CSR: csr, ClubID: club, ComputerID: pc})
	if err != nil {
		t.Fatal(err)
	}
	chain := [][]byte{iss.Cert.Raw, ca.Certificate().Raw}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: chain, PrivateKey: key, Leaf: iss.Cert}},
		RootCAs:      roots, ServerName: "localhost", MinVersion: version, MaxVersion: version,
	}
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func envDur(name string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

// childStats — строка "STATS {json}" от процесса-сервера.
type childStats struct {
	Goroutines int    `json:"goroutines"`
	HeapInuse  uint64 `json:"heap_inuse"`
	StackInuse uint64 `json:"stack_inuse"`
	Sys        uint64 `json:"sys"`
	Streams    int    `json:"streams"`
	PGWrites   int64  `json:"pg_writes"`
}

// TestLoadServerChild — процесс-сервер (запускается родителем TestLoad).
func TestLoadServerChild(t *testing.T) {
	if os.Getenv(loadChildEnv) == "" {
		t.Skip("child process of TestLoad")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("ZAFIRO_LOAD_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	vk, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{os.Getenv("ZAFIRO_LOAD_VALKEY")}, DisableCache: true})
	if err != nil {
		t.Fatal(err)
	}
	defer vk.Close()
	nc, err := nats.Connect(os.Getenv("ZAFIRO_LOAD_NATS"))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	svc, err := New(Options{Instance: "load", Pool: pool, Valkey: vk, NATS: nc, KeyPrefix: "load:", SubjectPrefix: "load"})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	// Те же лимиты транспорта, что у боевого сервера (grpcserver.DefaultLimits); TLS — если задан каталог PKI.
	lim := grpcserver.DefaultLimits()
	var creds []grpc.ServerOption
	if dir := os.Getenv("ZAFIRO_LOAD_PKI_DIR"); dir != "" {
		po := pki.Options{DevDir: dir}
		ca, err := pki.LoadCA(po)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := pki.LoadServerCertificate(po)
		if err != nil {
			t.Fatal(err)
		}
		creds = append(creds, grpc.Creds(credentials.NewTLS(grpcserver.TLSConfig(cert, ca.ClientCAs()))))
	}
	gs := grpc.NewServer(append(creds, grpc.StreamInterceptor(testIdentity),
		grpc.MaxRecvMsgSize(lim.MaxRecvMsgSize), grpc.MaxSendMsgSize(lim.MaxSendMsgSize),
		grpc.MaxConcurrentStreams(lim.MaxConcurrentStreams),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: lim.KeepaliveMinTime, PermitWithoutStream: true}),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: lim.KeepaliveTime, Timeout: lim.KeepaliveTimeout}),
		grpc.InitialWindowSize(lim.WindowSize), grpc.InitialConnWindowSize(lim.WindowSize),
		grpc.ReadBufferSize(lim.BufferSize), grpc.WriteBufferSize(lim.BufferSize))...)
	Register(gs, svc)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gs.Serve(lis) }()
	fmt.Printf("ADDR %s\n", lis.Addr())
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	stdin := make(chan struct{})
	go func() {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		close(stdin)
	}()
	for {
		select {
		case <-stdin:
			gs.Stop()
			return
		case <-tick.C:
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			b, _ := json.Marshal(childStats{Goroutines: runtime.NumGoroutine(), HeapInuse: ms.HeapInuse, StackInuse: ms.StackInuse,
				Sys: ms.Sys, Streams: svc.LocalStreams(), PGWrites: svc.Recorder().Writes()})
			fmt.Printf("STATS %s\n", b)
		}
	}
}

// procSample — CPU (тики) и RSS процесса из /proc.
type procSample struct {
	cpuTicks uint64
	rssKB    uint64
	at       time.Time
}

func sampleProc(t *testing.T, pid int) procSample {
	t.Helper()
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	// поля после ")" : state(3) ... utime(14) stime(15)
	f := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+2:]))
	ut, _ := strconv.ParseUint(f[11], 10, 64)
	st, _ := strconv.ParseUint(f[12], 10, 64)
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	var rss uint64
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			rss, _ = strconv.ParseUint(strings.Fields(line)[1], 10, 64)
		}
	}
	return procSample{cpuTicks: ut + st, rssKB: rss, at: time.Now()}
}

const clkTck = 100 // sysconf(_SC_CLK_TCK) на Linux

// pgRowsWritten — строки клуба, изменённые с момента since: каждая запись присутствия ставит updated_at,
// каждая команда — created_at (pg_stat_user_tables не годится: backend сбрасывает статистику с задержкой до 10 с).
func pgRowsWritten(t *testing.T, club int64, since time.Time) int64 {
	t.Helper()
	var n int64
	if err := testEnv.db.Owner.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM club_computers WHERE club_id = $1 AND updated_at >= $2)
		     + (SELECT count(*) FROM agent_commands WHERE club_id = $1 AND (created_at >= $2 OR completed_at >= $2))`,
		club, since).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLoad(t *testing.T) {
	if os.Getenv(loadChildEnv) != "" {
		t.Skip("parent only")
	}
	ctx := context.Background()
	pcs := envInt("ZAFIRO_LOAD_PCS", 10000)
	statusEvery := envDur("ZAFIRO_LOAD_STATUS_EVERY", 30*time.Second)
	window := envDur("ZAFIRO_LOAD_WINDOW", 60*time.Second)

	// ПК клуба (пачкой).
	club := testEnv.club(t)
	var zone int64
	if err := testEnv.db.Owner.QueryRow(ctx,
		"INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Зал', now(), now()) RETURNING id", club).Scan(&zone); err != nil {
		t.Fatal(err)
	}
	rows, err := testEnv.db.Owner.Query(ctx, `INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at)
		SELECT $1, $2, g, now(), now() FROM generate_series(1, $3) g RETURNING id`, club, zone, pcs)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	// Процесс-сервер.
	dsn := testEnv.db.AppDSN
	if strings.Contains(dsn, "?") {
		dsn += "&pool_max_conns=32"
	} else {
		dsn += "?pool_max_conns=32"
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLoadServerChild$", "-test.v", "-test.timeout=0") // #nosec G204 -- сам тестовый бинарник
	cmd.Env = append(os.Environ(), loadChildEnv+"=1", "ZAFIRO_LOAD_DSN="+dsn, "ZAFIRO_LOAD_VALKEY="+testEnv.vkAddr, "ZAFIRO_LOAD_NATS="+testEnv.natsURL)
	// mTLS: dev-PKI (корень, выпускающий CA агентов, серверный сертификат localhost) и сертификат на каждый ПК.
	tlsVersion := loadTLSVersion(t)
	clientCreds := map[int64]*tls.Config{}
	if tlsVersion != 0 {
		dir := filepath.Join(t.TempDir(), "pki")
		po := pki.Options{DevDir: dir}
		ca, err := pki.LoadCA(po)
		if err != nil {
			t.Fatal(err)
		}
		rootPEM, err := os.ReadFile(filepath.Join(dir, pki.DevRootCertFile)) // #nosec G304 -- временный каталог теста
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM(rootPEM)
		for _, id := range ids {
			clientCreds[id] = clientTLS(t, ca, roots, club, id, tlsVersion)
		}
		cmd.Env = append(cmd.Env, "ZAFIRO_LOAD_PKI_DIR="+dir)
	}
	stdout, _ := cmd.StdoutPipe()
	stdin, _ := cmd.StdinPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = stdin.Write([]byte("\n"))
		_ = cmd.Wait()
	}()
	addrCh := make(chan string, 1)
	var lastStats atomic.Pointer[childStats]
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "ADDR "):
				addrCh <- strings.TrimPrefix(line, "ADDR ")
			case strings.HasPrefix(line, "STATS "):
				var s childStats
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "STATS ")), &s) == nil {
					lastStats.Store(&s)
				}
			}
		}
	}()
	addr := <-addrCh
	stats := func() childStats {
		time.Sleep(1100 * time.Millisecond)
		return *lastStats.Load()
	}
	idle := stats()
	idleProc := sampleProc(t, cmd.Process.Pid)
	t.Logf("idle server: goroutines=%d heap=%d KB rss=%d KB", idle.Goroutines, idle.HeapInuse/1024, idleProc.rssKB)

	// Агенты: TCP-соединение и стрим на ПК.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var connected, failed, statuses atomic.Int64
	gotCmd := make(map[int64]chan time.Time, len(ids))
	for _, id := range ids {
		gotCmd[id] = make(chan time.Time, 4)
	}
	sem := make(chan struct{}, 200)
	start := time.Now()
	for _, id := range ids {
		sem <- struct{}{}
		wg.Add(1)
		go func(pc int64) {
			defer wg.Done()
			tc := insecure.NewCredentials()
			if cfg := clientCreds[pc]; cfg != nil {
				tc = credentials.NewTLS(cfg)
			}
			cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(tc),
				grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}))
			if err != nil {
				failed.Add(1)
				<-sem
				return
			}
			defer cc.Close()
			sctx, cancel := context.WithCancel(metadata.AppendToOutgoingContext(context.Background(),
				"x-test-club", strconv.FormatInt(club, 10), "x-test-computer", strconv.FormatInt(pc, 10)))
			defer cancel()
			st, err := agentv1.NewAgentServiceClient(cc).Connect(sctx)
			if err == nil {
				err = st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Hello{Hello: defaultHello()}})
			}
			var m *agentv1.ServerMessage
			if err == nil {
				m, err = st.Recv()
			}
			<-sem
			if err != nil || m.GetWelcome() == nil {
				failed.Add(1)
				return
			}
			connected.Add(1)
			go func() {
				for {
					m, err := st.Recv()
					if err != nil {
						return
					}
					if c := m.GetCommand(); c != nil {
						select {
						case gotCmd[pc] <- time.Now():
						default:
						}
						if c.GetSeq() > 0 {
							_ = st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_CommandResult{CommandResult: &agentv1.CommandResult{
								CommandId: c.GetCommandId(), Seq: c.GetSeq(), Status: agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED}}})
						}
					}
				}
			}()
			// Статус раз в statusEvery с равномерным сдвигом (как 10 000 независимых ПК).
			first := time.NewTimer(time.Duration(mrand.Int64N(int64(statusEvery)))) // #nosec G404 -- распределение нагрузки
			defer first.Stop()
			select {
			case <-stop:
				return
			case <-first.C:
			}
			tk := time.NewTicker(statusEvery)
			defer tk.Stop()
			for {
				if st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: &agentv1.Status{
					State: agentv1.ComputerState_COMPUTER_STATE_IDLE,
					Load:  &agentv1.ComputerLoad{CpuBasisPoints: uint32(mrand.IntN(10000)), RamBasisPoints: 4000}}}}) == nil { // #nosec G404,G115
					statuses.Add(1)
				}
				select {
				case <-stop:
					return
				case <-tk.C:
				}
			}
		}(id)
	}
	for connected.Load()+failed.Load() < int64(len(ids)) {
		time.Sleep(100 * time.Millisecond)
	}
	connectTime := time.Since(start)
	t.Logf("connected %d (failed %d) in %v", connected.Load(), failed.Load(), connectTime)
	if failed.Load() > 0 {
		t.Errorf("%d streams failed", failed.Load())
	}

	// Дать отработать дебаунсу и записям подключения.
	time.Sleep(10 * time.Second)
	before := stats()
	beforeProc := sampleProc(t, cmd.Process.Pid)
	windowStart := time.Now()
	statusesBefore := statuses.Load()
	time.Sleep(window)
	after := stats()
	afterProc := sampleProc(t, cmd.Process.Pid)
	pgRows := pgRowsWritten(t, club, windowStart)
	statusesWindow := statuses.Load() - statusesBefore

	n := float64(connected.Load())
	cpuSec := float64(afterProc.cpuTicks-beforeProc.cpuTicks) / clkTck
	wall := afterProc.at.Sub(beforeProc.at).Seconds()

	// Команда 1 000 ПК с «другого инстанса» (Sender в процессе теста → NATS → процесс-сервер).
	nc := testEnv.nats(t)
	sender := commands.NewSender(commands.SenderOptions{Pool: testEnv.db.App, Presence: presence.NewStore(testEnv.valkey, "load:", 0), NATS: nc, SubjectPrefix: "load"})
	targets := ids
	if len(targets) > 1000 {
		targets = targets[:1000]
	}
	for _, pc := range targets {
		for len(gotCmd[pc]) > 0 {
			<-gotCmd[pc]
		}
	}
	sendStart := time.Now()
	var maxLat time.Duration
	for _, pc := range targets {
		if _, err := sender.Send(ctx, club, pc, message("нагрузка")); err != nil {
			t.Fatal(err)
		}
	}
	sendDur := time.Since(sendStart)
	for _, pc := range targets {
		select {
		case at := <-gotCmd[pc]:
			if d := at.Sub(sendStart); d > maxLat {
				maxLat = d
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("command to %d not delivered", pc)
		}
	}

	report := map[string]any{
		"tls":                          os.Getenv("ZAFIRO_LOAD_TLS"),
		"streams":                      connected.Load(),
		"connect_seconds":              connectTime.Seconds(),
		"status_interval_seconds":      statusEvery.Seconds(),
		"window_seconds":               wall,
		"statuses_in_window":           statusesWindow,
		"server_cpu_percent_of_core":   100 * cpuSec / wall,
		"server_rss_mb":                float64(afterProc.rssKB) / 1024,
		"server_rss_kb_per_stream":     float64(int64(afterProc.rssKB)-int64(idleProc.rssKB)) / n,
		"heap_kb_per_stream":           float64(int64(after.HeapInuse)-int64(idle.HeapInuse)) / 1024 / n,
		"stack_kb_per_stream":          float64(int64(after.StackInuse)-int64(idle.StackInuse)) / 1024 / n,
		"goroutines_per_stream":        float64(after.Goroutines-idle.Goroutines) / n,
		"recorder_writes_in_window":    after.PGWrites - before.PGWrites,
		"pg_rows_written_in_window":    pgRows,
		"commands_1000_send_seconds":   sendDur.Seconds(),
		"commands_1000_max_latency_ms": maxLat.Milliseconds(),
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	t.Logf("load report:\n%s", b)
	if p := os.Getenv("ZAFIRO_LOAD_REPORT"); p != "" {
		_ = os.WriteFile(p, b, 0o600)
	}
	if pgRows != 0 || after.PGWrites != before.PGWrites {
		t.Errorf("unchanged statuses wrote to Postgres: %d rows, %d recorder writes", pgRows, after.PGWrites-before.PGWrites)
	}
	close(stop)
}
