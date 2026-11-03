//go:build loadgen

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sample — одна операция генератора.
type sample struct {
	At      time.Duration `json:"at"` // от начала прогона
	Op      string        `json:"op"`
	Dur     time.Duration `json:"dur"`
	Outcome string        `json:"outcome"` // ok | business:<reason> | error:<code>/<reason>
}

// stats — все операции прогона в памяти (десятки тысяч записей) + фазы для отчёта.
type stats struct {
	mu      sync.Mutex
	start   time.Time
	samples []sample
	phases  []phase
}

type phase struct {
	Name     string        `json:"name"`
	From, To time.Duration `json:"-"`
	FromS    float64       `json:"from_s"`
	ToS      float64       `json:"to_s"`
}

func newStats() *stats { return &stats{start: time.Now()} }

func (s *stats) since() time.Duration { return time.Since(s.start) }

func (s *stats) rec(op string, t0 time.Time, err error) string {
	o := outcome(err)
	s.mu.Lock()
	s.samples = append(s.samples, sample{At: t0.Sub(s.start), Op: op, Dur: time.Since(t0), Outcome: o})
	s.mu.Unlock()
	return o
}

func (s *stats) beginPhase(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Since(s.start)
	if n := len(s.phases); n > 0 && s.phases[n-1].To == 0 {
		s.phases[n-1].To = now
	}
	s.phases = append(s.phases, phase{Name: name, From: now})
}

func (s *stats) endPhases() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.phases); n > 0 && s.phases[n-1].To == 0 {
		s.phases[n-1].To = time.Since(s.start)
	}
	for i := range s.phases {
		s.phases[i].FromS, s.phases[i].ToS = s.phases[i].From.Seconds(), s.phases[i].To.Seconds()
	}
}

// businessReasons — ожидаемые бизнес-отказы (не ошибки системы): нехватка денег, сессия уже кончилась и т.п.
var businessReasons = map[string]bool{
	"ERROR_CODE_INSUFFICIENT_FUNDS": true, "ERROR_CODE_NO_ACTIVE_SESSION": true, "ERROR_CODE_SESSION_MISMATCH": true,
	"ERROR_CODE_OUT_OF_STOCK": true, "ERROR_CODE_SIGNED_IN_ELSEWHERE": true, "ERROR_CODE_RATE_LIMITED": true,
	"ERROR_CODE_TARIFF_UNAVAILABLE": true, "ERROR_CODE_COMPUTER_RESERVED": true,
}

func outcome(err error) string {
	if err == nil {
		return "ok"
	}
	var ae *apiError
	if errors.As(err, &ae) {
		if ae.Status == 422 || ae.Status == 409 || ae.Status == 429 {
			return fmt.Sprintf("business:http_%d", ae.Status)
		}
		return fmt.Sprintf("error:http_%d", ae.Status)
	}
	if st, ok := status.FromError(err); ok && st.Code() != codes.Unknown {
		reason := ""
		for _, d := range st.Details() {
			if ei, ok := d.(*errdetails.ErrorInfo); ok {
				reason = ei.GetReason()
			}
		}
		if businessReasons[reason] {
			return "business:" + st.Code().String() + "/" + reason
		}
		return "error:" + st.Code().String() + "/" + reason
	}
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "error:timeout"
	case errors.Is(err, io.EOF), strings.Contains(err.Error(), "connection reset"), strings.Contains(err.Error(), "EOF"):
		return "error:conn"
	}
	return "error:" + truncate(err.Error(), 60)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// opReport — строка таблицы отчёта.
type opReport struct {
	Op       string         `json:"op"`
	Count    int            `json:"count"`
	PerSec   float64        `json:"per_sec"`
	P50ms    float64        `json:"p50_ms"`
	P95ms    float64        `json:"p95_ms"`
	P99ms    float64        `json:"p99_ms"`
	MaxMs    float64        `json:"max_ms"`
	Outcomes map[string]int `json:"outcomes"`
	durs     []time.Duration
}

func (s *stats) report(ph phase) []opReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	by := map[string]*opReport{}
	for _, x := range s.samples {
		if x.At < ph.From || x.At >= ph.To {
			continue
		}
		r := by[x.Op]
		if r == nil {
			r = &opReport{Op: x.Op, Outcomes: map[string]int{}}
			by[x.Op] = r
		}
		r.Count++
		r.Outcomes[x.Outcome]++
		if x.Outcome == "ok" {
			r.durs = append(r.durs, x.Dur)
		}
	}
	secs := (ph.To - ph.From).Seconds()
	out := make([]opReport, 0, len(by))
	for _, r := range by {
		sort.Slice(r.durs, func(i, j int) bool { return r.durs[i] < r.durs[j] })
		q := func(p float64) float64 {
			if len(r.durs) == 0 {
				return 0
			}
			return float64(r.durs[int(float64(len(r.durs)-1)*p)].Microseconds()) / 1000
		}
		r.P50ms, r.P95ms, r.P99ms, r.MaxMs = q(0.5), q(0.95), q(0.99), q(1)
		if secs > 0 {
			r.PerSec = float64(r.Count) / secs
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Op < out[j].Op })
	return out
}

// moneyOps — денежные операции (ledger): для итога «0 денежных ошибок».
var moneyOps = map[string]bool{
	"guest.purchase_minutes": true, "guest.purchase_tariff": true, "guest.end_session": true, "shop.place_order": true,
	"guest.replay": true, "cash.top_up": true, "cash.sale": true, "peak.purchase_minutes": true, "peak.place_order": true,
}

func printReport(w io.Writer, name string, ph phase, rows []opReport) {
	fmt.Fprintf(w, "\n### %s (%.0f–%.0f с)\n\n", name, ph.From.Seconds(), ph.To.Seconds())
	fmt.Fprintln(w, "| Операция | Кол-во | в с | p50, мс | p95, мс | p99, мс | max, мс | Итоги |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|")
	for _, r := range rows {
		var oc []string
		for k, v := range r.Outcomes {
			if k != "ok" {
				oc = append(oc, fmt.Sprintf("%s ×%d", k, v))
			}
		}
		sort.Strings(oc)
		fmt.Fprintf(w, "| %s | %d | %.2f | %.1f | %.1f | %.1f | %.1f | ok ×%d %s |\n", r.Op, r.Count, r.PerSec, r.P50ms, r.P95ms, r.P99ms, r.MaxMs,
			r.Outcomes["ok"], strings.Join(oc, ", "))
	}
}
