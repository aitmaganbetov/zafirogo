//go:build loadgen

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"
)

// monitor — снимки ресурсов раз в interval: docker stats контейнеров, /metrics инстансов (внутренний порт 9090),
// Postgres (TPS, WAL, блокировки, записи club_computers), Valkey INFO, NATS /varz.
type monitor struct {
	interval time.Duration
	project  string
	pg       *pgxpool.Pool
	vk       valkey.Client
	start    time.Time
	gen      func() map[string]float64 // счётчики генератора

	mu   sync.Mutex
	rows []map[string]float64
	prev map[string]float64
}

func (m *monitor) run(ctx context.Context) {
	tk := time.NewTicker(m.interval)
	defer tk.Stop()
	for {
		m.sample(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}

func (m *monitor) ip(ctx context.Context, name string) string {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", m.project+"-"+name+"-1").Output() // #nosec G204 -- имя контейнера staging
	if err != nil {
		return ""
	}
	ip := strings.TrimSpace(string(out))
	if net.ParseIP(ip) == nil { // остановленный контейнер: "invalid IP"
		return ""
	}
	return ip
}

func (m *monitor) sample(ctx context.Context) {
	row := map[string]float64{"t": time.Since(m.start).Seconds()}
	var wg sync.WaitGroup
	var mu sync.Mutex
	set := func(k string, v float64) { mu.Lock(); row[k] = v; mu.Unlock() }
	wg.Add(5)
	go func() { defer wg.Done(); m.dockerStats(ctx, set) }()
	go func() {
		defer wg.Done()
		for _, app := range []string{"app1", "app2"} {
			m.appMetrics(ctx, app, set)
		}
	}()
	go func() { defer wg.Done(); m.pgStats(ctx, set) }()
	go func() { defer wg.Done(); m.valkeyStats(ctx, set) }()
	go func() { defer wg.Done(); m.natsStats(ctx, set) }()
	wg.Wait()
	for k, v := range m.gen() {
		row[k] = v
	}
	m.mu.Lock()
	// Счётчики → скорость за интервал (ключи *_total, pg.*_cum).
	if m.prev != nil {
		dt := row["t"] - m.prev["t"]
		for k, v := range row {
			if strings.HasSuffix(k, "_cum") {
				if pv, ok := m.prev[k]; ok && dt > 0 && v >= pv {
					row[strings.TrimSuffix(k, "_cum")+"_per_s"] = (v - pv) / dt
				}
			}
		}
	}
	m.prev = row
	m.rows = append(m.rows, row)
	m.mu.Unlock()
}

func (m *monitor) dockerStats(ctx context.Context, set func(string, float64)) {
	out, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{.Name}};{{.CPUPerc}};{{.MemUsage}}").Output() // #nosec G204
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(line, ";")
		if len(parts) != 3 || !strings.HasPrefix(parts[0], m.project+"-") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(parts[0], m.project+"-"), "-1")
		if cpu, err := strconv.ParseFloat(strings.TrimSuffix(parts[1], "%"), 64); err == nil {
			set("cpu_pct."+name, cpu)
		}
		mem := strings.TrimSpace(strings.Split(parts[2], "/")[0])
		set("mem_mib."+name, parseMiB(mem))
	}
}

func parseMiB(s string) float64 {
	units := []struct {
		suf string
		mul float64
	}{{"GiB", 1024}, {"MiB", 1}, {"KiB", 1.0 / 1024}, {"B", 1.0 / 1024 / 1024}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suf) {
			v, _ := strconv.ParseFloat(strings.TrimSuffix(s, u.suf), 64)
			return v * u.mul
		}
	}
	return 0
}

// appMetrics — выборка рядов Prometheus инстанса.
var appSeries = []string{
	"process_cpu_seconds_total", "process_resident_memory_bytes", "go_goroutines", "go_memstats_heap_inuse_bytes",
	"zafiro_agent_streams_active", "zafiro_agent_online_computers", "zafiro_grpc_streams_active",
	"zafiro_db_pool_acquire_waits_total", "zafiro_db_pool_acquire_wait_seconds_total", "zafiro_db_pool_acquired_connections",
	"zafiro_db_pool_total_connections", "zafiro_db_pool_max_connections",
	"zafiro_outbox_pending_events", "zafiro_outbox_oldest_unpublished_age_seconds", "zafiro_webhook_deliveries_pending",
	"zafiro_webhook_delivery_attempts_total", "zafiro_ledger_operations_total", "zafiro_ledger_errors_total",
	"zafiro_worker_run_duration_seconds_sum", "zafiro_worker_run_duration_seconds_count", "zafiro_worker_errors_total",
	"zafiro_valkey_commands_total", "zafiro_nats_messages_published_total", "zafiro_rate_limited_total",
	"zafiro_agent_commands_total", "zafiro_db_queries_total",
}

func (m *monitor) appMetrics(ctx context.Context, app string, set func(string, float64)) {
	ip := m.ip(ctx, app)
	if ip == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, "http://"+ip+":9090/metrics", nil)
	if err != nil {
		set(app+".up", 0)
		return
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		set(app+".up", 0)
		return
	}
	defer resp.Body.Close()
	set(app+".up", 1)
	sums := map[string]float64{}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 32<<20))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		sp := strings.LastIndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		v, err := strconv.ParseFloat(line[sp+1:], 64)
		if err != nil {
			continue
		}
		series := line[:sp]
		name, labels, _ := strings.Cut(series, "{")
		for _, want := range appSeries {
			if name != want {
				continue
			}
			key := name
			// Метки, которые различаем: pool, job (воркеры), kind/result (ledger), event (команды).
			for _, l := range []string{"pool", "job", "result", "event", "kind"} {
				if i := strings.Index(labels, l+"=\""); i >= 0 {
					rest := labels[i+len(l)+2:]
					key += "." + rest[:strings.IndexByte(rest, '"')]
				}
			}
			sums[key] += v
		}
	}
	for k, v := range sums {
		if strings.HasSuffix(k, "_total") || strings.Contains(k, "_total.") || strings.Contains(k, "_sum") || strings.Contains(k, "_count") {
			set(app+"."+k+"_cum", v)
			continue
		}
		set(app+"."+k, v)
	}
}

func (m *monitor) pgStats(ctx context.Context, set func(string, float64)) {
	if m.pg == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var xact, ins, upd, del, deadlocks, wal, ccUpd, ccIns float64
	var waiting, active, total, longest float64
	err := m.pg.QueryRow(cctx, `SELECT (xact_commit + xact_rollback)::float8, tup_inserted::float8, tup_updated::float8, tup_deleted::float8,
		deadlocks::float8, pg_wal_lsn_diff(pg_current_wal_lsn(), '0/0')::float8,
		(SELECT coalesce(n_tup_upd, 0)::float8 FROM pg_stat_user_tables WHERE relname = 'club_computers'),
		(SELECT coalesce(n_tup_ins, 0)::float8 FROM pg_stat_user_tables WHERE relname = 'agent_commands'),
		(SELECT count(*)::float8 FROM pg_stat_activity WHERE wait_event_type = 'Lock'),
		(SELECT count(*)::float8 FROM pg_stat_activity WHERE state = 'active' AND backend_type = 'client backend'),
		(SELECT count(*)::float8 FROM pg_stat_activity WHERE backend_type = 'client backend'),
		(SELECT coalesce(max(extract(epoch FROM now() - xact_start)), 0)::float8 FROM pg_stat_activity WHERE state <> 'idle' AND backend_type = 'client backend' AND pid <> pg_backend_pid())
		FROM pg_stat_database WHERE datname = current_database()`).Scan(&xact, &ins, &upd, &del, &deadlocks, &wal, &ccUpd, &ccIns, &waiting, &active, &total, &longest)
	if err != nil {
		set("pg.error", 1)
		return
	}
	set("pg.xact_cum", xact)
	set("pg.tup_inserted_cum", ins)
	set("pg.tup_updated_cum", upd)
	set("pg.tup_deleted_cum", del)
	set("pg.deadlocks", deadlocks)
	set("pg.wal_bytes_cum", wal)
	set("pg.club_computers_upd_cum", ccUpd)
	set("pg.agent_commands_ins_cum", ccIns)
	set("pg.lock_waiting", waiting)
	set("pg.active", active)
	set("pg.connections", total)
	set("pg.longest_xact_s", longest)
}

func (m *monitor) valkeyStats(ctx context.Context, set func(string, float64)) {
	if m.vk == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s, err := m.vk.Do(cctx, m.vk.B().Info().Build()).ToString()
	if err != nil {
		return
	}
	for _, line := range strings.Split(s, "\r\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			continue
		}
		switch k {
		case "total_commands_processed":
			set("valkey.commands_cum", f)
		case "used_memory":
			set("valkey.used_memory_mib", f/1024/1024)
		case "connected_clients":
			set("valkey.clients", f)
		case "used_cpu_sys", "used_cpu_user":
			set("valkey."+k+"_cum", f)
		}
	}
}

func (m *monitor) natsStats(ctx context.Context, set func(string, float64)) {
	ip := m.ip(ctx, "nats")
	if ip == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, "http://"+ip+":8222/varz", nil)
	if err != nil {
		return
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var v struct {
		CPU     float64 `json:"cpu"`
		Mem     float64 `json:"mem"`
		InMsgs  float64 `json:"in_msgs"`
		OutMsgs float64 `json:"out_msgs"`
		Conns   float64 `json:"connections"`
	}
	if json.NewDecoder(resp.Body).Decode(&v) != nil {
		return
	}
	set("nats.cpu_pct", v.CPU)
	set("nats.mem_mib", v.Mem/1024/1024)
	set("nats.in_msgs_cum", v.InMsgs)
	set("nats.out_msgs_cum", v.OutMsgs)
	set("nats.connections", v.Conns)
}

// summary — среднее и максимум каждого ряда в окне фазы.
func (m *monitor) summary(ph phase) map[string][2]float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	acc := map[string][]float64{}
	for _, r := range m.rows {
		t := r["t"]
		if t < ph.From.Seconds() || t >= ph.To.Seconds() {
			continue
		}
		for k, v := range r {
			if k != "t" && !strings.HasSuffix(k, "_cum") {
				acc[k] = append(acc[k], v)
			}
		}
	}
	out := map[string][2]float64{}
	for k, vs := range acc {
		var sum, mx float64
		for _, v := range vs {
			sum += v
			mx = max(mx, v)
		}
		out[k] = [2]float64{sum / float64(len(vs)), mx}
	}
	return out
}

func printSummary(w io.Writer, sum map[string][2]float64) {
	keys := make([]string, 0, len(sum))
	for k := range sum {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintln(w, "\n| Ряд | среднее | максимум |\n|---|---|---|")
	for _, k := range keys {
		fmt.Fprintf(w, "| %s | %.2f | %.2f |\n", k, sum[k][0], sum[k][1])
	}
}

func (m *monitor) dump() []map[string]float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]map[string]float64(nil), m.rows...)
}
