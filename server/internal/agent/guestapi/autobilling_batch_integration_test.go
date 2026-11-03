//go:build integration

package guestapi_test

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/guestapi"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

var abPCNumber atomic.Int64

// abSeedPC — ПК клуба в зоне (строка club_computers), без агента.
func (h *harness) abSeedPC(club, zone int64) int64 {
	h.t.Helper()
	var id int64
	h.scan(&id, `INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at)
		VALUES ($1, $2, $3, now(), now()) RETURNING id`, club, zone, 200000+abPCNumber.Add(1))
	return id
}

// abSeedUser — клиент клуба: членство со скидкой и тратами (member = false — без членства), строка баланса
// (balance < 0 — без строки).
func (h *harness) abSeedUser(club int64, member bool, discount, spent, balance int64) int64 {
	h.t.Helper()
	var id int64
	h.scan(&id, "INSERT INTO users (name, phone, password, created_at, updated_at) VALUES ('Гость', $1, 'x', now(), now()) RETURNING id", uniqPhone())
	if member {
		h.exec(`INSERT INTO club_memberships (club_id, user_id, role, discount, total_spent, created_at, updated_at)
			VALUES ($1, $2, 'client', $3, $4, now(), now())`, club, id, discount, spent)
	}
	if balance >= 0 {
		h.exec("INSERT INTO member_balances (club_id, user_id, created_at, updated_at) VALUES ($1, $2, now(), now())", club, id)
	}
	if balance > 0 {
		if err := db.InTenantTx(context.Background(), env.db.App, club, func(q *store.Queries) error {
			_, err := billing.SetInitialBalance(context.Background(), q, billing.InitialBalanceParams{ClubID: club, UserID: id, Main: money.Tiyn(balance)})
			return err
		}); err != nil {
			h.t.Fatal(err)
		}
	}
	return id
}

// abSetSession — сессия на ПК (user 0 — без гостя; guest — гостевая с кассы).
func (h *harness) abSetSession(pc, user int64, guest bool, ends time.Time) {
	h.t.Helper()
	var u *int64
	if user > 0 {
		u = &user
	}
	h.exec(`UPDATE club_computers SET current_session_id = $2, current_session_user_id = $3, session_guest = $4,
		session_ends_at = $5 WHERE id = $1`, pc, uniq("sess-"), u, guest, ends)
}

// S7-AB-2: пакетное чтение автобиллинга (одна транзакция на клуб, запросы со списком) даёт те же данные и то же
// решение, что прежнее поштучное (abLoad — эталон), на наборе сценариев: нет денег, есть деньги, скидка члена
// клуба (не действует на минуты / действует с уровнем лояльности), своя сессия идёт / кончилась по времени, сессия
// другого гостя (гость сменился) идёт / кончилась, гостевая сессия с кассы, нет членства и строки баланса, зона без
// цены, автобиллинг выключен, другая пауза; ПК не из этого клуба — в пакете его нет (читается поштучно, с той же
// ошибкой).
func TestAutoBillingBatchReadMatchesSingle(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: wholeAndAutoSettings})
	ctx := context.Background()
	now := h.clock.Now()

	clubB := env.db.Club(t, uniq("ab-batch-b-"))
	h.exec(`UPDATE clubs SET settings = $2::jsonb WHERE id = $1`, clubB,
		`{"finances": {"auto_billing": "1", "auto_billing_pause_minutes": 1, "discount_scope": "all",
		  "loyalty_enabled": "1", "loyalty_levels": [{"spent": 1000, "percent": 15}]}}`)
	var zoneB, zoneBFree int64
	h.scan(&zoneB, "INSERT INTO club_zones (club_id, name, price_per_hour, created_at, updated_at) VALUES ($1, 'VIP', 90000, now(), now()) RETURNING id", clubB)
	h.scan(&zoneBFree, "INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Free', now(), now()) RETURNING id", clubB)
	clubC := env.db.Club(t, uniq("ab-batch-c-"))
	h.exec(`UPDATE clubs SET settings = $2::jsonb WHERE id = $1`, clubC, `{"finances": {"allow_whole_balance": true}}`)
	var zoneC int64
	h.scan(&zoneC, "INSERT INTO club_zones (club_id, name, price_per_hour, created_at, updated_at) VALUES ($1, 'Hall', 60000, now(), now()) RETURNING id", clubC)

	type sc struct {
		name     string
		club, pc int64
		user     int64
		want     string // подстрока решения (mode pause, пауза кончилась) — сценарий действительно тот
	}
	var scs []sc
	add := func(name string, club, zone int64, member bool, discount, spent, balance int64, want string, setup func(pc, user int64)) {
		pc, user := h.abSeedPC(club, zone), h.abSeedUser(club, member, discount, spent, balance)
		if setup != nil {
			setup(pc, user)
		}
		scs = append(scs, sc{name: name, club: club, pc: pc, user: user, want: want})
	}
	A, zA := h.club, h.zone
	add("A/нет денег", A, zA, true, 0, 0, 0, "enabled=true main=0 price=60000 waiting=false", nil)
	add("A/есть деньги", A, zA, true, 0, 0, 200000, "main=200000 price=60000 waiting=true", nil)
	add("A/скидка не на минуты", A, zA, true, 10, 500000, 150000, "price=60000", nil)
	add("A/своя сессия идёт", A, zA, true, 0, 0, 100000, "enabled=true", func(pc, user int64) {
		h.abSetSession(pc, user, false, now.Add(time.Hour))
	})
	add("A/своя сессия кончилась", A, zA, true, 0, 0, 100000, "session=- expired=true", func(pc, user int64) {
		h.abSetSession(pc, user, false, now.Add(-time.Minute))
	})
	other := h.abSeedUser(A, true, 0, 0, 0)
	add("A/сессия другого гостя идёт", A, zA, true, 0, 0, 100000, "expired=false", func(pc, _ int64) {
		h.abSetSession(pc, other, false, now.Add(time.Hour))
	})
	add("A/сессия другого гостя кончилась", A, zA, true, 0, 0, 100000, "session=- expired=false", func(pc, _ int64) {
		h.abSetSession(pc, other, false, now.Add(-time.Minute))
	})
	add("A/гостевая сессия с кассы", A, zA, true, 0, 0, 100000, "expired=false", func(pc, _ int64) {
		h.abSetSession(pc, 0, true, now.Add(time.Hour))
	})
	add("A/нет членства и баланса", A, zA, false, 0, 0, -1, "main=0 price=60000 waiting=false", nil)
	add("B/лояльность на минуты", clubB, zoneB, true, 5, 200000, 100000, "price=76500 waiting=true", nil)
	add("B/личная скидка больше уровня", clubB, zoneB, true, 20, 200000, 100000, "price=72000", nil)
	add("B/зона без цены", clubB, zoneBFree, true, 0, 0, 100000, "enabled=false", nil)
	add("B/нет денег", clubB, zoneB, true, 0, 0, 0, "waiting=false", nil)
	add("C/автобиллинг выключен", clubC, zoneC, true, 0, 0, 100000, "enabled=false main=100000 price=60000 waiting=false", nil)

	foreignPC := h.abSeedPC(clubB, zoneB) // ПК клуба B в пакете клуба A
	byClub := map[int64][][2]int64{}
	for _, s := range scs {
		byClub[s.club] = append(byClub[s.club], [2]int64{s.pc, s.user})
	}
	byClub[A] = append(byClub[A], [2]int64{foreignPC, scs[0].user})

	batch := map[int64]guestapi.ABData{}
	for club, pairs := range byClub {
		got, err := h.srv.ABLoadClub(ctx, club, pairs)
		if err != nil {
			t.Fatalf("club %d: %v", club, err)
		}
		for pc, d := range got {
			batch[pc] = d
		}
	}
	if _, ok := batch[foreignPC]; ok {
		t.Fatal("ПК чужого клуба прочитан пакетом клуба A")
	}
	if _, err := h.srv.ABLoadOne(ctx, A, foreignPC, scs[0].user); err == nil {
		t.Fatal("поштучное чтение ПК чужого клуба без ошибки")
	}
	if len(batch) != len(scs) {
		t.Fatalf("batch read %d computers, want %d", len(batch), len(scs))
	}

	pause := now.Add(-time.Hour) // пауза любой длины кончилась
	states := []struct {
		mode  string
		pause time.Time
		at    time.Time
	}{
		{"pause", time.Time{}, now}, {"pause", now, now.Add(30 * time.Second)}, {"pause", pause, now},
		{"after", time.Time{}, now}, {"after", pause, now.Add(2 * time.Hour)},
	}
	for _, s := range scs {
		t.Run(s.name, func(t *testing.T) {
			one, err := h.srv.ABLoadOne(ctx, s.club, s.pc, s.user)
			if err != nil {
				t.Fatal(err)
			}
			got := batch[s.pc]
			if !reflect.DeepEqual(got, one) {
				t.Fatalf("data differ:\nbatch  %+v\nsingle %+v", got, one)
			}
			for _, st := range states {
				b := guestapi.ABDecide(got, s.user, st.mode, st.pause, st.at)
				o := guestapi.ABDecide(one, s.user, st.mode, st.pause, st.at)
				if b != o {
					t.Fatalf("%s/%v: decision differs:\nbatch  %s\nsingle %s", st.mode, st.pause, b, o)
				}
			}
			if d := guestapi.ABDecide(got, s.user, "pause", time.Time{}, now); !strings.Contains(d, s.want) {
				t.Fatalf("scenario: %s; want %q", d, s.want)
			}
		})
	}
}

// abState — поле состояния автобиллинга ПК в Valkey.
func (h *harness) abField(pc int64, field string) string {
	h.t.Helper()
	v, err := env.valkey.Do(context.Background(), env.valkey.B().Hget().Key(h.prefix+"guest:ab:"+strconv.FormatInt(pc, 10)).
		Field(field).Build()).ToString()
	if err != nil {
		h.t.Fatal(err)
	}
	return v
}

func (h *harness) abSet(pc int64, due time.Time, fields ...string) {
	h.t.Helper()
	ps := strconv.FormatInt(pc, 10)
	vc := env.valkey
	cmd := vc.B().Hset().Key(h.prefix + "guest:ab:" + ps).FieldValue()
	for i := 0; i < len(fields); i += 2 {
		cmd = cmd.FieldValue(fields[i], fields[i+1])
	}
	for _, r := range vc.DoMulti(context.Background(), cmd.Build(),
		vc.B().Zadd().Key(h.prefix+"guest:ab:due").ScoreMember().ScoreMember(float64(due.UnixMilli()), ps).Build()) {
		if err := r.Error(); err != nil {
			h.t.Fatal(err)
		}
	}
}

// S7-AB-2: гость на двух ПК в одном пакете (вход на втором ПК при живом первом запрещён, но состояние первого ПК
// может ещё ждать пересмотра; токены — напрямую). Первый ПК (пауза кончилась) покупает на весь баланс; второй
// (пауза не начата) — по данным после покупки, как раньше поштучно: денег нет — не ждёт, пересмотр через 30 с. По
// данным пакета (прочитанным до покупки) второй ПК начал бы паузу.
func TestAutoBillingBatchSameGuestTwoPCs(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: wholeAndAutoSettings})
	user := h.customer("Дос", uniqPhone(), "password", 60000, 0)
	p1, p2 := h.enrollPC(), h.enrollPC()
	for _, p := range []*pc{p1, p2} {
		if _, _, err := h.srv.Tokens().Issue(context.Background(), identity.Identity{ClubID: h.club, ComputerID: p.id}, user); err != nil {
			t.Fatal(err)
		}
	}
	now := h.clock.Now()
	for _, p := range []*pc{p1, p2} {
		h.abSet(p.id, now, "club", strconv.FormatInt(h.club, 10), "user", strconv.FormatInt(user, 10), "signin", "1",
			"mode", "pause", "pause", "0", "step", "0", "en", "1", "w", "0", "pe", "0")
	}
	h.abSet(p1.id, now.Add(-2*time.Second), "pause", strconv.FormatInt(now.Add(-time.Hour).UnixMilli(), 10))
	h.abSet(p2.id, now.Add(-time.Second), "pause", "0")

	if n, err := h.srv.TickAutoBilling(context.Background()); err != nil || n != 2 {
		t.Fatalf("tick = %d, %v", n, err)
	}
	h.wantBalances(user, 0, 0)
	if sid, _ := h.pcSession(p1.id); sid == "" {
		t.Fatal("first PC: no purchase")
	}
	if w, pause, mode := h.abField(p2.id, "w"), h.abField(p2.id, "pause"), h.abField(p2.id, "mode"); w != "0" || pause != "0" || mode != "pause" {
		t.Fatalf("second PC: w=%s pause=%s mode=%s; want not waiting (no money after the first purchase)", w, pause, mode)
	}
	h.noDrift()
}

// S7-AB-2: данные пакета устарели (гость потратил баланс после чтения пакета) — перед покупкой ПК читается заново,
// и решение — по свежим данным, как раньше: денег нет — покупки нет, автобиллинг не останавливается (а по старым
// данным была бы попытка покупки, отказ и стоп до нового входа).
func TestAutoBillingBatchRereadsBeforePurchase(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: wholeAndAutoSettings})
	phone := uniqPhone()
	user := h.customer("Ая", phone, "password", 0, 0)
	p := h.enrollPC()
	p.mustLogin(phone, "password")
	now := h.clock.Now()
	h.abSet(p.id, now, "mode", "after")
	fresh, err := h.srv.ABLoadOne(context.Background(), h.club, p.id, user)
	if err != nil {
		t.Fatal(err)
	}
	stale := guestapi.ABWithMain(fresh, 120000)
	if d := guestapi.ABDecide(stale, user, "after", time.Time{}, now); !strings.Contains(d, "buy=120") {
		t.Fatalf("stale decision: %s", d)
	}
	if err := h.srv.ABProcessWith(context.Background(), p.id, h.club, user, stale); err != nil {
		t.Fatal(err)
	}
	if n := h.int("SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind = 'minutes_purchase'", h.club); n != 0 {
		t.Fatalf("minutes purchases = %d", n)
	}
	if mode, w := h.abField(p.id, "mode"), h.abField(p.id, "w"); mode != "after" || w != "0" {
		t.Fatalf("mode=%s w=%s; want after, not waiting", mode, w)
	}
	h.wantBalances(user, 0, 0)
}
