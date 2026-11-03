//go:build loadgen

package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// staffReads — запросы списков панели (как открытые страницы сотрудника: карта клуба, ПК, касса, X-отчёт, клиенты,
// заказы, журнал, бейджи меню).
var staffReads = []struct{ op, path string }{
	{"rest.club_map_state", "/api/club-map/state"},
	{"rest.club_computers", "/api/club-computers"},
	{"rest.cash_shift", "/api/cash/shift"},
	{"rest.cash_x_report", "/api/cash/reports/x"},
	{"rest.customers", "/api/customers"},
	{"rest.shop_orders", "/api/shop-orders"},
	{"rest.activity_logs", "/api/activity-logs"},
	{"rest.layout_badges", "/api/layout/badges"},
	{"rest.dashboard", "/api/dashboard"},
}

// runStaff — сотрудник клуба: WebSocket (события между инстансами) + запросы списков раз в readEvery; кассир ещё
// пополняет балансы гостей и продаёт товары на кассе (деньги через REST с Idempotency-Key).
func (r *runner) runStaff(ctx context.Context, club, k int) {
	s := &staffClient{http: r.t.webClient(club), phone: phone(club, k)}
	if !sleepCtx(ctx, uniform(0, 20*time.Second)) {
		return
	}
	for ctx.Err() == nil {
		t0 := time.Now()
		err := s.login(ctx)
		r.stats.rec("rest.login", t0, err)
		if err == nil {
			break
		}
		sleepCtx(ctx, uniform(5*time.Second, 20*time.Second))
	}
	go r.staffWS(ctx, s)
	cashier := k == cashierK
	nextTopUp := time.Now().Add(uniform(0, 2*r.cfg.topUpEvery))
	nextSale := time.Now().Add(uniform(0, 2*r.cfg.saleEvery))
	for ctx.Err() == nil {
		if !sleepCtx(ctx, uniform(r.cfg.readEvery/2, r.cfg.readEvery*3/2)) {
			return
		}
		rd := staffReads[rndIntN(len(staffReads))]
		if cashier && (rd.op == "rest.activity_logs" || rd.op == "rest.dashboard") {
			rd = staffReads[rndIntN(4)] // журнал и дашборд кассиру закрыты правами (403) — как в старом
		}
		t0 := time.Now()
		err := s.call(ctx, http.MethodGet, rd.path, nil, nil, nil)
		r.stats.rec(rd.op, t0, err)
		if !cashier {
			continue
		}
		if time.Now().After(nextTopUp) {
			nextTopUp = time.Now().Add(expDur(r.cfg.topUpEvery))
			r.topUp(ctx, s, club)
		}
		if time.Now().After(nextSale) {
			nextSale = time.Now().Add(expDur(r.cfg.saleEvery))
			key := uuid.NewString()
			body := map[string]any{"items": []map[string]any{{"product_id": zoneID(club, 1+rndIntN(3)), "quantity": 1 + rndIntN(3)}}}
			r.restMoney(ctx, s, "cash.sale", "/api/cash/sales", key, body, nil)
		}
	}
}

func (r *runner) topUp(ctx context.Context, s *staffClient, club int) {
	pc := 1 + rndIntN(r.cfg.pcsPerClub)
	key := uuid.NewString()
	var out struct {
		Transaction struct {
			Amount int64 `json:"amount_tiyn"`
		} `json:"transaction"`
	}
	amount := int64(200000 + 100000*rndIntN(5))
	body := map[string]any{"user_id": guestUserID(club, pc), "amount_tiyn": amount, "balance_type": "balance",
		"payment_method": []string{"cash", "card", "qr"}[rndIntN(3)]}
	if r.restMoney(ctx, s, "cash.top_up", "/api/cash/balance-top-ups", key, body, &out) {
		r.ledger.add(key, amount)
	}
}

// restMoney — денежный REST с повтором тем же ключом при обрыве/5xx (как SPA, CSQ-4).
func (r *runner) restMoney(ctx context.Context, s *staffClient, op, path, key string, body, out any) bool {
	t0 := time.Now()
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			r.stats.rec(op+".retry", time.Now(), err)
			if !sleepCtx(ctx, time.Duration(attempt)*time.Second) {
				return false
			}
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = s.call(cctx, http.MethodPost, path, map[string]string{"Idempotency-Key": key}, body, out)
		cancel()
		if err == nil {
			break
		}
		if ae := (*apiError)(nil); errors.As(err, &ae) && ae.Status < 500 {
			break
		}
	}
	r.stats.rec(op, t0, err)
	return err == nil
}

func (r *runner) staffWS(ctx context.Context, s *staffClient) {
	for ctx.Err() == nil {
		t0 := time.Now()
		c, err := s.ws(ctx)
		r.stats.rec("ws.connect", t0, err)
		if err != nil {
			sleepCtx(ctx, uniform(2*time.Second, 10*time.Second))
			continue
		}
		r.counters.wsUp.Add(1)
		for {
			_, _, err := c.Read(ctx)
			if err != nil {
				break
			}
			r.counters.wsMessages.Add(1)
		}
		r.counters.wsUp.Add(-1)
		_ = c.CloseNow()
		if ctx.Err() == nil {
			r.counters.wsReconnects.Add(1)
			sleepCtx(ctx, uniform(time.Second, 5*time.Second))
		}
	}
}
