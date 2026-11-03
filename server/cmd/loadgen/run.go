//go:build loadgen

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"

	"github.com/google/uuid"
)

type runConfig struct {
	statusEvery   time.Duration
	guestShare    float64
	guestIdleMean time.Duration
	playMin       time.Duration
	playMax       time.Duration
	orderShare    float64
	autoBillShare float64
	replayShare   float64
	readEvery     time.Duration
	topUpEvery    time.Duration
	saleEvery     time.Duration
	pcsPerClub    int
}

type counters struct {
	up, streamErrors, drained, statusSent, commands, endSessionCmds, expiredSessions, replayMismatch atomic.Int64
	wsUp, wsMessages, wsReconnects                                                                   atomic.Int64
}

// keyLog — ключи идемпотентности успешных денежных операций (проверка check: каждая — ровно одна операция ledger).
type keyLog struct {
	mu   sync.Mutex
	keys map[string]int64
}

func (k *keyLog) add(key string, amount int64) {
	k.mu.Lock()
	k.keys[key] = amount
	k.mu.Unlock()
}

// sessions — ПК с идущей сессией гостя (для пиковой минуты: дополнительные покупки и заказы).
type sessions struct {
	mu sync.Mutex
	m  map[*pcSim]string
}

func (s *sessions) add(p *pcSim, token string) { s.mu.Lock(); s.m[p] = token; s.mu.Unlock() }
func (s *sessions) remove(p *pcSim)            { s.mu.Lock(); delete(s.m, p); s.mu.Unlock() }
func (s *sessions) random() (*pcSim, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := rndIntN(len(s.m) + 1)
	for p, t := range s.m {
		if n--; n <= 0 {
			return p, t
		}
	}
	return nil, ""
}

type runner struct {
	cfg       runConfig
	t         *target
	stats     *stats
	counters  counters
	ledger    keyLog
	inSession sessions
	errMu     sync.Mutex
	errKinds  map[string]int
}

func (r *runner) noteStreamErr(err error) {
	k := outcome(err)
	if st, ok := status.FromError(err); ok {
		k = st.Code().String() + ": " + truncate(st.Message(), 50)
	}
	r.errMu.Lock()
	r.errKinds[k]++
	r.errMu.Unlock()
}

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dir := fs.String("staging", "../deploy/staging", "каталог deploy/staging")
	certs := fs.String("certs", "", "файл prepare -certs")
	out := fs.String("out", "", "каталог отчёта (report.md, timeline.json, samples.json, keys.json)")
	lbIP := fs.String("lb", "172.29.66.10", "адрес nginx в сети staging")
	src := fs.String("src", "", "первый адрес источника клубов (tools/loadgen/net.sh)")
	limit := fs.Int("pcs", 0, "ограничить число ПК (0 — все из -certs)")
	clubsLimit := fs.Int("clubs", 0, "ограничить число клубов (0 — все)")
	staffPerClub := fs.Int("staff", 2, "сотрудников на клуб (1 — владелец, 2 — + кассир)")
	ramp := fs.Duration("ramp", 2*time.Minute, "разнесение подключения ПК (включение клуба)")
	steady := fs.Duration("steady", 30*time.Minute, "сценарий (а): стабильная работа")
	peak := fs.Duration("peak", time.Minute, "сценарий (в): пиковая минута (0 — без)")
	peakMult := fs.Float64("peak-mult", 10, "во сколько раз больше денежных операций в пик")
	restart := fs.String("restart", "graceful,kill", "сценарий (б): graceful (docker stop app1), kill (docker kill app2); пусто — без")
	settle := fs.Duration("settle", 3*time.Minute, "пауза между сценариями")
	project := fs.String("project", "zafirogo-staging", "имя проекта compose")
	cfg := runConfig{}
	fs.DurationVar(&cfg.statusEvery, "status-every", 30*time.Second, "Status без изменений")
	fs.Float64Var(&cfg.guestShare, "guest-share", 0.6, "доля ПК с гостями")
	fs.DurationVar(&cfg.guestIdleMean, "guest-idle", 8*time.Minute, "средняя пауза ПК между гостями")
	fs.DurationVar(&cfg.playMin, "play-min", 4*time.Minute, "игра: от")
	fs.DurationVar(&cfg.playMax, "play-max", 12*time.Minute, "игра: до")
	fs.Float64Var(&cfg.orderShare, "order-share", 0.5, "доля сессий с заказом в магазине")
	fs.Float64Var(&cfg.autoBillShare, "autobill-share", 0.15, "доля входов без покупки в клубах с автобиллингом")
	fs.Float64Var(&cfg.replayShare, "replay-share", 0.03, "доля покупок минут, повторённых тем же ключом")
	fs.DurationVar(&cfg.readEvery, "read-every", 30*time.Second, "сотрудник: запрос списка раз в")
	fs.DurationVar(&cfg.topUpEvery, "topup-every", 3*time.Minute, "кассир: пополнение раз в (среднее)")
	fs.DurationVar(&cfg.saleEvery, "sale-every", 6*time.Minute, "кассир: продажа на кассе раз в (среднее)")
	_ = fs.Parse(args)
	if *certs == "" || *out == "" {
		return errors.New("run: -certs и -out обязательны")
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		return err
	}
	env, err := loadStaging(*dir)
	if err != nil {
		return err
	}
	creds, err := readCreds(*certs)
	if err != nil {
		return err
	}
	if *clubsLimit > 0 {
		kept := creds[:0]
		for _, c := range creds {
			if c.Club <= *clubsLimit {
				kept = append(kept, c)
			}
		}
		creds = kept
	}
	if *limit > 0 && *limit < len(creds) {
		creds = creds[:*limit]
	}
	clubs := map[int]int{}
	for _, c := range creds {
		clubs[c.Club]++
	}
	for _, n := range clubs {
		cfg.pcsPerClub = max(cfg.pcsPerClub, n)
	}
	t, err := newTarget(*dir, *lbIP, *src)
	if err != nil {
		return err
	}
	r := &runner{cfg: cfg, t: t, stats: newStats(), ledger: keyLog{keys: map[string]int64{}},
		inSession: sessions{m: map[*pcSim]string{}}, errKinds: map[string]int{}}

	pg, err := pgxpool.New(ctx, env.superDSN())
	if err != nil {
		return err
	}
	defer pg.Close()
	vk, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{"127.0.0.1:16379"}, Password: env.v("STG_VALKEY_PASSWORD", ""), DisableCache: true})
	if err != nil {
		return err
	}
	defer vk.Close()
	mon := &monitor{interval: 10 * time.Second, project: *project, pg: pg, vk: vk, start: r.stats.start, gen: func() map[string]float64 {
		return map[string]float64{"gen.streams_up": float64(r.counters.up.Load()), "gen.ws_up": float64(r.counters.wsUp.Load()),
			"gen.status_sent_cum": float64(r.counters.statusSent.Load()), "gen.stream_errors_cum": float64(r.counters.streamErrors.Load()),
			"gen.in_session": float64(len(r.inSession.m))}
	}}

	runCtx, stopAll := context.WithCancel(ctx)
	defer stopAll()
	go mon.run(runCtx)

	// ПК и гости.
	fmt.Printf("run: %d ПК в %d клубах, гостей %.0f%%, сотрудников %d/клуб, ramp %s\n", len(creds), len(clubs), cfg.guestShare*100, *staffPerClub, *ramp)
	r.stats.beginPhase("ramp")
	pcs := make([]*pcSim, 0, len(creds))
	var wg sync.WaitGroup
	for i, c := range creds {
		p, err := newPC(r, c, rndFloat() < cfg.guestShare)
		if err != nil {
			return err
		}
		pcs = append(pcs, p)
		delay := time.Duration(int64(*ramp) * int64(i) / int64(len(creds)))
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !sleepCtx(runCtx, delay) {
				return
			}
			if p.guest {
				go p.runGuest(runCtx)
			}
			p.runStream(runCtx)
		}()
	}
	for club := range clubs {
		for k := 1; k <= *staffPerClub; k++ {
			go r.runStaff(runCtx, club, k)
		}
	}
	go r.progress(runCtx, len(pcs))

	waitUp := func(what string, limit time.Duration) time.Duration {
		t0 := time.Now()
		for time.Since(t0) < limit && ctx.Err() == nil {
			if int(r.counters.up.Load()) >= len(pcs) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		d := time.Since(t0)
		fmt.Printf("run: %s: на связи %d/%d через %s\n", what, r.counters.up.Load(), len(pcs), d.Round(100*time.Millisecond))
		return d
	}
	events := map[string]any{}
	events["ramp_all_connected_s"] = waitUp("подключение", *ramp+5*time.Minute).Seconds()

	r.stats.beginPhase("steady")
	moneyBefore := r.moneyCount()
	steadyFrom := time.Now()
	if !sleepCtx(ctx, *steady) {
		return r.finish(*out, mon, events, stopAll, &wg)
	}
	baseRate := float64(r.moneyCount()-moneyBefore) / time.Since(steadyFrom).Seconds()
	events["steady_money_ops_per_s"] = baseRate

	if *peak > 0 {
		r.stats.beginPhase("peak")
		extra := baseRate * (*peakMult - 1)
		fmt.Printf("run: пиковая минута: база %.2f ден. оп/с, добавляем %.1f/с\n", baseRate, extra)
		r.peakLoad(ctx, *peak, extra)
		r.stats.beginPhase("after-peak")
		if !sleepCtx(ctx, *settle) {
			return r.finish(*out, mon, events, stopAll, &wg)
		}
	}
	for _, mode := range strings.Split(*restart, ",") {
		mode = strings.TrimSpace(mode)
		if mode == "" {
			continue
		}
		target := map[string]string{"graceful": "app1", "kill": "app2"}[mode]
		if target == "" {
			return fmt.Errorf("-restart: неизвестный режим %q", mode)
		}
		name := *project + "-" + target + "-1"
		r.stats.beginPhase("restart-" + mode)
		t0 := time.Now()
		rec := r.watchRecovery(len(pcs), 5*time.Minute)
		var cmd *exec.Cmd
		if mode == "graceful" {
			cmd = exec.CommandContext(ctx, "docker", "stop", "-t", "45", name) // #nosec G204
		} else {
			cmd = exec.CommandContext(ctx, "docker", "kill", name) // #nosec G204
		}
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w %s", cmd.String(), err, b)
		}
		events["restart_"+mode+"_stop_s"] = time.Since(t0).Seconds()
		res := <-rec
		events["restart_"+mode+"_min_up"] = res.minUp
		events["restart_"+mode+"_recovered_s"] = res.recovered.Seconds()
		fmt.Printf("run: %s %s: минимум на связи %d, все снова на связи через %s\n", mode, target, res.minUp, res.recovered.Round(100*time.Millisecond))
		t1 := time.Now()
		if b, err := exec.CommandContext(ctx, "docker", "start", name).CombinedOutput(); err != nil { // #nosec G204
			return fmt.Errorf("docker start %s: %w %s", name, err, b)
		}
		waitReady(ctx, target, 2*time.Minute)
		events["restart_"+mode+"_start_ready_s"] = time.Since(t1).Seconds()
		r.stats.beginPhase("after-" + mode)
		if !sleepCtx(ctx, *settle) {
			break
		}
	}
	return r.finish(*out, mon, events, stopAll, &wg)
}

type recovery struct {
	minUp     int64
	recovered time.Duration
}

// watchRecovery — с этого момента: минимум потоков на связи и время, когда после провала снова на связи все total.
func (r *runner) watchRecovery(total int, limit time.Duration) <-chan recovery {
	ch := make(chan recovery, 1)
	t0 := time.Now()
	go func() {
		res := recovery{minUp: r.counters.up.Load()}
		dropped := false
		for time.Since(t0) < limit {
			up := r.counters.up.Load()
			res.minUp = min(res.minUp, up)
			if int(up) < total {
				dropped = true
			} else if dropped {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		res.recovered = time.Since(t0)
		ch <- res
	}()
	return ch
}

func waitReady(ctx context.Context, app string, limit time.Duration) time.Duration {
	t0 := time.Now()
	port := map[string]string{"app1": "18081", "app2": "18082"}[app]
	cl := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	for time.Since(t0) < limit && ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/readyz", nil)
		if err != nil {
			return time.Since(t0)
		}
		resp, err := cl.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return time.Since(t0)
}

func (r *runner) moneyCount() int {
	r.stats.mu.Lock()
	defer r.stats.mu.Unlock()
	n := 0
	for _, s := range r.stats.samples {
		if moneyOps[s.Op] {
			n++
		}
	}
	return n
}

// peakLoad — дополнительные денежные операции с постоянной частотой extra/с: продление сессии минутами или заказ
// в магазине у случайного гостя с идущей сессией.
func (r *runner) peakLoad(ctx context.Context, d time.Duration, extra float64) {
	if extra <= 0 {
		sleepCtx(ctx, d)
		return
	}
	pctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	interval := time.Duration(float64(time.Second) / extra)
	tk := time.NewTicker(interval)
	defer tk.Stop()
	var wg sync.WaitGroup
	for {
		select {
		case <-pctx.Done():
			wg.Wait()
			return
		case <-tk.C:
		}
		p, token := r.inSession.random()
		if p == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			gctx := guestCtx(ctx, token)
			if rndFloat() < 0.5 {
				p.placeOrder(gctx, "peak.place_order")
				return
			}
			key := uuid.NewString()
			req := &agentv1.PurchaseMinutesRequest{IdempotencyKey: key, Minutes: 5, BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN}
			res, err := money(gctx, p, "peak.purchase_minutes", func(c context.Context, conn *grpc.ClientConn) (*agentv1.PurchaseMinutesResponse, error) {
				return agentv1.NewGuestServiceClient(conn).PurchaseMinutes(c, req)
			})
			if err == nil {
				r.ledger.add(key, res.GetPurchase().GetChargedMainTiyn())
			}
		}()
	}
}

func (r *runner) progress(ctx context.Context, total int) {
	tk := time.NewTicker(30 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		r.stats.mu.Lock()
		n, errs := len(r.stats.samples), 0
		for _, s := range r.stats.samples {
			if strings.HasPrefix(s.Outcome, "error") {
				errs++
			}
		}
		r.stats.mu.Unlock()
		fmt.Printf("[%s] потоков %d/%d, ws %d, операций %d (ошибок %d), ден. %d, в сессии %d, обрывов %d, drain %d\n",
			r.stats.since().Round(time.Second), r.counters.up.Load(), total, r.counters.wsUp.Load(), n, errs, r.moneyCount(),
			len(r.inSession.m), r.counters.streamErrors.Load(), r.counters.drained.Load())
	}
}

func (r *runner) finish(out string, mon *monitor, events map[string]any, stop context.CancelFunc, wg *sync.WaitGroup) error {
	r.stats.endPhases()
	mon.sample(context.Background())
	stop()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
	}
	events["replay_mismatch"] = r.counters.replayMismatch.Load()
	events["expired_sessions_left_to_worker"] = r.counters.expiredSessions.Load()
	events["end_session_commands"] = r.counters.endSessionCmds.Load()
	events["commands_total"] = r.counters.commands.Load()
	events["ws_messages"] = r.counters.wsMessages.Load()
	events["ws_reconnects"] = r.counters.wsReconnects.Load()
	events["stream_errors"] = r.counters.streamErrors.Load()
	events["drained"] = r.counters.drained.Load()
	r.errMu.Lock()
	events["stream_error_kinds"] = r.errKinds
	r.errMu.Unlock()

	f, err := os.Create(filepath.Clean(filepath.Join(out, "report.md"))) // #nosec G304 -- каталог отчёта задаёт оператор
	if err != nil {
		return err
	}
	defer f.Close()
	w := io.MultiWriter(f, os.Stdout)
	fmt.Fprintf(w, "# Прогон loadgen %s\n", time.Now().Format(time.RFC3339))
	eb, _ := json.MarshalIndent(events, "", "  ")
	fmt.Fprintf(w, "\n```json\n%s\n```\n", eb)
	phases := map[string]any{}
	for _, ph := range r.stats.phases {
		rows := r.stats.report(ph)
		printReport(w, ph.Name, ph, rows)
		sum := mon.summary(ph)
		printSummary(w, sum)
		phases[ph.Name] = map[string]any{"phase": ph, "ops": rows, "resources": sum}
	}
	write := func(name string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(out, name), b, 0o600)
	}
	r.ledger.mu.Lock()
	keys := make([]string, 0, len(r.ledger.keys))
	for k := range r.ledger.keys {
		keys = append(keys, k)
	}
	r.ledger.mu.Unlock()
	sort.Strings(keys)
	for name, v := range map[string]any{"phases.json": phases, "timeline.json": mon.dump(), "keys.json": keys, "events.json": events} {
		if err := write(name, v); err != nil {
			return err
		}
	}
	r.stats.mu.Lock()
	err = write("samples.json", r.stats.samples)
	r.stats.mu.Unlock()
	return err
}
