package guestapi

// Автобиллинг на сервере (решение Q-008, docs/open-questions/stage5-agent.md AG-12).
//
// Старый агент делал его сам раз в секунду (WindowsAgent/Frontend/main.js autoBillingOn/autoBillingWaits/
// startAutoBilling/autoBillingTick, ~стр. 1001–1046; BR-023, GS-055): гость вошёл, сессии нет, клуб включил
// автобиллинг, у зоны есть цена часа и основного баланса хватает хотя бы на минуту → пауза
// auto_billing_pause_minutes на выбор тарифа → покупка минут на весь основной баланс; после конца тарифа по времени
// (sessionEnded 'expired') — сразу; гость сам завершил сессию, её завершили в клубе или перенесли, покупка не удалась
// — до нового входа не начинать (autoStopped). Решение — pricing.AutoBilling, покупка — billing
// PurchaseWholeBalance (минуты пересчитываются по балансу под блокировкой).
//
// Состояние ПК — в Valkey (рядом с токеном гостя, живёт столько же):
//
//	<prefix>ab:<pc>   HASH {club, user, signin (мс входа), mode (pause|after|stopped), pause (мс начала паузы, 0 —
//	                  не начата), step (номер покупки), en/w/pe (последнее отправленное агенту состояние)}
//	<prefix>ab:due    ZSET pc → когда пересмотреть (мс)
//
// События меняют состояние на любом инстансе: вход гостя (signIn), сессия началась/кончилась и баланс
// (WrapNotifier — обёртка уведомлений billing/кассы), истечение сессии воркером (OnExpired). Решение и покупку
// делает один исполнитель на кластер (worker.AutoBilling, leader.RunOnce на тик) — Tick. Ключ идемпотентности покупки —
// ПК + гость + вход + номер шага: повтор после сбоя не спишет дважды. Данные Postgres для решения тик читает пакетом —
// одной транзакцией на клуб (abLoadClub, S7-AB-2); перед покупкой ПК перечитывается поштучно (abLoad).

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/valkey-io/valkey-go"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Режимы автобиллинга ПК.
const (
	abPause   = "pause"   // ждёт паузу (или идёт сессия)
	abAfter   = "after"   // время тарифа вышло — покупать сразу (autoAfterTariff)
	abStopped = "stopped" // до нового входа не начинать (autoStopped)
)

// Периоды пересмотра.
const (
	// AutoBillingRecheck — как часто пересматривать гостя без сессии, которому автобиллинг сейчас не положен
	// (нет денег, выключен в клубе): пополнение на кассе, включение в настройках. Старый агент проверял раз в секунду.
	// Срок — по слоту клуба (abRecheckAt): не позже чем через AutoBillingRecheck.
	AutoBillingRecheck = 30 * time.Second
	// AutoBillingRetry — повтор после сбоя (БД, Valkey), не после отказа покупки.
	AutoBillingRetry = 10 * time.Second
	// AutoBillingBatch — сколько ПК выбирать из zset за раз (тик берёт пакеты, пока есть просроченные и не вышел
	// бюджет тика).
	AutoBillingBatch = 200
	// DefaultAutoBillingWorkers — сколько ПК (разных гостей) пересматривать параллельно: каждый — тенантная
	// транзакция чтения (и иногда покупка); пул zafiro_app — не меньше 16 соединений (db.ParsePoolConfig).
	DefaultAutoBillingWorkers = 8
	// DefaultAutoBillingTickBudget — сколько тик (период 1 с) выбирает новые пакеты; начатый пакет доделывается.
	DefaultAutoBillingTickBudget = 800 * time.Millisecond
)

func (s *Server) abKey(pc int64) string { return s.o.KeyPrefix + "ab:" + strconv.FormatInt(pc, 10) }
func (s *Server) abDue() string         { return s.o.KeyPrefix + "ab:due" }
func ms(t time.Time) string             { return strconv.FormatInt(t.UnixMilli(), 10) }

// abState — состояние ПК из Valkey.
type abState struct {
	club, user, signin, pause, step int64
	mode                            string
	status                          string // en/w/pe последнего отправленного состояния
}

func parseAB(m map[string]string) (abState, bool) {
	var st abState
	var err error
	num := func(k string) int64 {
		if err != nil {
			return 0
		}
		var v int64
		v, err = strconv.ParseInt(m[k], 10, 64)
		return v
	}
	st.club, st.user, st.signin, st.pause, st.step = num("club"), num("user"), num("signin"), num("pause"), num("step")
	st.mode = m["mode"]
	st.status = m["en"] + "/" + m["w"] + "/" + m["pe"]
	return st, err == nil && st.club > 0 && st.user > 0
}

// abSignInScript: новое состояние входа. KEYS: hash, due. ARGV: club, user, signin, ttl_ms, pc, due_ms.
var abSignInScript = valkey.NewLuaScript(`
redis.call('DEL', KEYS[1])
redis.call('HSET', KEYS[1], 'club', ARGV[1], 'user', ARGV[2], 'signin', ARGV[3], 'mode', 'pause', 'pause', '0', 'step', '0')
redis.call('PEXPIRE', KEYS[1], ARGV[4])
redis.call('ZADD', KEYS[2], ARGV[6], ARGV[5])
return 1
`)

// abUpdateScript: изменить поля, если состояние есть (и вход тот же, если ARGV[1] ≠ ""); due: число — ZADD,
// "-" — ZREM, "" — не трогать. KEYS: hash, due. ARGV: signin, pc, due_ms, field, value, ...
var abUpdateScript = valkey.NewLuaScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
if ARGV[1] ~= '' and redis.call('HGET', KEYS[1], 'signin') ~= ARGV[1] then return 0 end
for i = 4, #ARGV, 2 do redis.call('HSET', KEYS[1], ARGV[i], ARGV[i + 1]) end
if ARGV[3] == '-' then redis.call('ZREM', KEYS[2], ARGV[2])
elseif ARGV[3] ~= '' then redis.call('ZADD', KEYS[2], ARGV[3], ARGV[2]) end
return 1
`)

// abSignedIn — гость вошёл на ПК: автобиллинг с чистого листа (как сброс autoStopped/autoAfterTariff при новом
// входе в autoBillingTick). Решение — в ближайшем тике (пауза начинается с него). Ошибка — только в лог.
func (s *Server) abSignedIn(ctx context.Context, id identity.Identity, userID int64) {
	now := s.o.Now()
	err := abSignInScript.Exec(ctx, s.o.Valkey, []string{s.abKey(id.ComputerID), s.abDue()},
		[]string{itoa(id.ClubID), itoa(userID), ms(now), millis(s.o.TokenTTL), itoa(id.ComputerID), ms(now)}).Error()
	if err != nil {
		s.o.Log.WarnContext(ctx, "auto billing: sign-in state not saved", "computer_id", id.ComputerID, "err", err)
	}
}

// abUpdate — поля состояния ПК (только если оно есть; signin ≠ 0 — только для этого входа).
func (s *Server) abUpdate(ctx context.Context, pc, signin int64, due string, fields ...string) error {
	sg := ""
	if signin != 0 {
		sg = itoa(signin)
	}
	return abUpdateScript.Exec(ctx, s.o.Valkey, []string{s.abKey(pc), s.abDue()},
		append([]string{sg, itoa(pc), due}, fields...)).Error()
}

func (s *Server) abForget(ctx context.Context, pc int64) error {
	vc := s.o.Valkey
	return errors.Join(
		vc.Do(ctx, vc.B().Del().Key(s.abKey(pc)).Build()).Error(),
		vc.Do(ctx, vc.B().Zrem().Key(s.abDue()).Member(itoa(pc)).Build()).Error())
}

// ---------------------------------------------------------------------------
// Уведомления billing/кассы: сессия и баланс
// ---------------------------------------------------------------------------

// WrapNotifier — уведомления агенту (next) + автобиллинг и название тарифа в StartSession (AG-17):
//   - SessionStarted: tariff_name последней части сессии; пересмотр автобиллинга — к концу сессии;
//   - SessionEnded: EXPIRED — купить сразу (autoAfterTariff), иначе (гость, сотрудник, пересадка) — стоп до нового
//     входа (autoStopped);
//   - BalanceChanged: пересмотреть ПК гостя сейчас (пополнение на кассе начинает паузу, как autoBillingWaits()).
//
// Подключать в Billing.SetNotifier, Cash.Notifier и обработчике истечения сессий (OnExpired).
func (s *Server) WrapNotifier(next commands.SessionNotifier) commands.SessionNotifier {
	return &abNotifier{s: s, next: next}
}

type abNotifier struct {
	s    *Server
	next commands.SessionNotifier
}

func (n *abNotifier) SessionStarted(ctx context.Context, clubID, computerID int64, as *agentv1.ActiveSession, issuedBy int64) error {
	s := n.s
	if as.GetTariffName() == "" && as.GetSessionId() != "" {
		if name, err := s.tariffName(ctx, clubID, as.GetSessionId()); err == nil {
			as.TariffName = name
		}
	}
	if end := as.GetEndsAt(); end != nil {
		if err := s.abUpdate(ctx, computerID, 0, ms(end.AsTime()), "pause", "0"); err != nil {
			s.o.Log.WarnContext(ctx, "auto billing: session start not saved", "computer_id", computerID, "err", err)
		}
	}
	if n.next == nil {
		return nil
	}
	return n.next.SessionStarted(ctx, clubID, computerID, as, issuedBy)
}

func (n *abNotifier) SessionEnded(ctx context.Context, clubID, computerID int64, e *agentv1.EndSession, issuedBy int64) error {
	n.s.abSessionEnded(ctx, computerID, e.GetReason())
	if n.next == nil {
		return nil
	}
	return n.next.SessionEnded(ctx, clubID, computerID, e, issuedBy)
}

func (n *abNotifier) BalanceChanged(ctx context.Context, clubID, userID int64, b *agentv1.Balance) error {
	s := n.s
	if s.o.Presence != nil {
		// ПК гостя — по индексу «гость → ПК» (S6-O4-11), не разбором снимков всего клуба.
		if pcs, err := s.o.Presence.SignedInOrList(ctx, clubID, userID); err == nil {
			for _, st := range pcs {
				if st.SignedInUserID == userID {
					_ = s.abUpdate(ctx, st.ComputerID, 0, ms(s.o.Now()))
				}
			}
		}
	}
	if n.next == nil {
		return nil
	}
	return n.next.BalanceChanged(ctx, clubID, userID, b)
}

// abSessionEnded — сессия ПК кончилась (main.js, событие sessionEnded: 'expired' → autoAfterTariff, другое →
// autoStopped).
func (s *Server) abSessionEnded(ctx context.Context, computerID int64, reason agentv1.SessionEndReason) {
	var err error
	if reason == agentv1.SessionEndReason_SESSION_END_REASON_EXPIRED {
		err = s.abUpdate(ctx, computerID, 0, ms(s.o.Now()), "mode", abAfter, "pause", "0")
	} else {
		err = s.abUpdate(ctx, computerID, 0, ms(s.o.Now()), "mode", abStopped, "pause", "0")
	}
	if err != nil {
		s.o.Log.WarnContext(ctx, "auto billing: session end not saved", "computer_id", computerID, "err", err)
	}
}

// OnExpired — истёкшие сессии (worker.ExpireSessions.OnExpired): autoAfterTariff. Уведомление агенту шлёт свой
// обработчик (app.notifyExpired) — здесь только автобиллинг.
func (s *Server) OnExpired(ctx context.Context, sessions []billingsvc.ExpiredSession) {
	for _, e := range sessions {
		if e.UserID > 0 {
			s.abSessionEnded(ctx, e.ComputerID, agentv1.SessionEndReason_SESSION_END_REASON_EXPIRED)
		}
	}
}

// tariffName — название последней части сессии (session_purchases.name, новые сверху) — «Тариф» в профиле (AG-17).
func (s *Server) tariffName(ctx context.Context, clubID int64, sessionID string) (string, error) {
	var name string
	err := db.InTenantTx(ctx, s.o.Pool, clubID, func(q *store.Queries) error {
		var err error
		name, err = sessionTariffName(ctx, q, clubID, sessionID)
		return err
	})
	return name, err
}

// ---------------------------------------------------------------------------
// Решение и покупка (один исполнитель на кластер)
// ---------------------------------------------------------------------------

// abView — всё, что нужно решению автобиллинга по ПК.
type abView struct {
	offer   pricing.MinuteOffer
	main    money.Tiyn
	session *agentv1.ActiveSession // идущая сессия (кто угодно)
	expired bool                   // сессия гостя кончилась по времени, но ещё не снята воркером истечения
}

// minuteOfferFor — условия поминутной оплаты гостя на ПК (GuestMinutesController::show()): скидка — только при
// членстве и если клуб применяет её к поминутному времени (hourlyPrice()).
func minuteOfferFor(ctx context.Context, q *store.Queries, id identity.Identity, userID int64) (pricing.MinuteOffer, store.GuestComputerRow, clubInfo, error) {
	ci, err := loadClub(ctx, q, id)
	if err != nil {
		return pricing.MinuteOffer{}, store.GuestComputerRow{}, ci, err
	}
	pcRow, err := q.GuestComputer(ctx, store.GuestComputerParams{ClubID: id.ClubID, ID: id.ComputerID})
	if err != nil {
		return pricing.MinuteOffer{}, pcRow, ci, err
	}
	var discount float64
	if u, err := q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: id.ClubID, UserID: userID}); err == nil {
		discount = memberDiscount(ci.settings, u.Discount, u.TotalSpent)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return pricing.MinuteOffer{}, pcRow, ci, err
	}
	o, err := offerFor(ci.settings, pcRow, discount)
	return o, pcRow, ci, err
}

// memberDiscount — скидка члена клуба на поминутное время (уровень лояльности или персональная).
func memberDiscount(cs clubSettings, discount float64, totalSpent int64) float64 {
	return bonus.LoyaltyDiscountFor(cs.bonus.LoyaltyLevelsInForce(), discount, money.Tiyn(totalSpent))
}

// offerFor — pricing.MinuteOfferFor по цене часа зоны ПК.
func offerFor(cs clubSettings, pcRow store.GuestComputerRow, discount float64) (pricing.MinuteOffer, error) {
	var price *money.Tiyn
	if pcRow.PricePerHour != nil {
		p := money.Tiyn(*pcRow.PricePerHour)
		price = &p
	}
	return pricing.MinuteOfferFor(price, cs.rules, discount)
}

// abData — прочитанное из Postgres для решения по ПК: условия минут, основной баланс, строка ПК (сессия).
type abData struct {
	offer pricing.MinuteOffer
	main  money.Tiyn
	pc    store.GuestComputerRow
}

// view — данные решения на момент now (идёт ли сессия — по часам пересмотра, а не чтения).
func (d abData) view(userID int64, now time.Time) abView {
	v := abView{offer: d.offer, main: d.main, session: activeSession(d.pc, now)}
	v.expired = v.session == nil && d.pc.CurrentSessionID != nil && d.pc.CurrentSessionUserID != nil &&
		*d.pc.CurrentSessionUserID == userID && d.pc.SessionEndsAt != nil
	return v
}

// abLoad — чтение одного ПК своей транзакцией (поштучный путь): ПК без пакетных данных, ПК одного гостя после
// первого в пакете, перепроверка перед покупкой. Эталон для пакетного abLoadClub (тест сравнения).
func (s *Server) abLoad(ctx context.Context, id identity.Identity, userID int64) (abData, error) {
	var d abData
	err := db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		o, pcRow, _, err := minuteOfferFor(ctx, q, id, userID)
		if err != nil {
			return err
		}
		d = abData{offer: o, pc: pcRow}
		b, err := q.GetMemberBalance(ctx, store.GetMemberBalanceParams{ClubID: id.ClubID, UserID: userID})
		if err == nil {
			d.main = money.Tiyn(b.Balance)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return nil
	})
	return d, err
}

// abReq — ПК пакета и гость, вошедший на нём (по состоянию в Valkey).
type abReq struct{ pc, user int64 }

// abLoadClub — то же, что abLoad, для всех ПК пакета одного клуба одной тенантной транзакцией (S7-AB-2): четыре
// запроса со списком (= ANY) вместо четырёх на каждый ПК. В ответе только ПК, для которых всё прочитано так же, как
// прочитал бы abLoad без ошибки; остальные (ПК нет в клубе, ошибка условий) читаются поштучно — с той же ошибкой.
// Ошибка запроса — ошибка всего клуба (тогда все его ПК читаются поштучно).
func (s *Server) abLoadClub(ctx context.Context, clubID int64, reqs []abReq) (map[int64]abData, error) {
	pcIDs := make([]int64, 0, len(reqs))
	userIDs := make([]int64, 0, len(reqs))
	for _, r := range reqs {
		pcIDs = append(pcIDs, r.pc)
		userIDs = append(userIDs, r.user)
	}
	out := make(map[int64]abData, len(reqs))
	err := db.InTenantTx(ctx, s.o.Pool, clubID, func(q *store.Queries) error {
		clear(out) // повтор транзакции (withRetry)
		raw, err := q.GuestClubSettingsForAutoBilling(ctx, clubID)
		if err != nil {
			return err
		}
		cs := parseClubSettings(raw)
		pcs, err := q.GuestComputersForAutoBilling(ctx, store.GuestComputersForAutoBillingParams{ClubID: clubID, Ids: pcIDs})
		if err != nil {
			return err
		}
		members, err := q.GuestMembersForAutoBilling(ctx, store.GuestMembersForAutoBillingParams{ClubID: clubID, UserIds: userIDs})
		if err != nil {
			return err
		}
		balances, err := q.GuestBalancesForAutoBilling(ctx, store.GuestBalancesForAutoBillingParams{ClubID: clubID, UserIds: userIDs})
		if err != nil {
			return err
		}
		pcByID := make(map[int64]store.GuestComputerRow, len(pcs))
		for _, r := range pcs {
			pcByID[r.ID] = store.GuestComputerRow(r)
		}
		discount := make(map[int64]float64, len(members))
		for _, m := range members {
			discount[m.ID] = memberDiscount(cs, m.Discount, m.TotalSpent)
		}
		main := make(map[int64]money.Tiyn, len(balances))
		for _, b := range balances {
			main[b.UserID] = money.Tiyn(b.Balance)
		}
		for _, r := range reqs {
			pcRow, ok := pcByID[r.pc]
			if !ok {
				continue
			}
			o, err := offerFor(cs, pcRow, discount[r.user])
			if err != nil {
				continue
			}
			out[r.pc] = abData{offer: o, main: main[r.user], pc: pcRow}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// abStatus — состояние для агента.
func abStatus(enabled bool, d pricing.AutoBillingDecision, stopped bool) *agentv1.AutoBillingStatus {
	st := &agentv1.AutoBillingStatus{Enabled: enabled, Waiting: d.Waiting, Stopped: stopped}
	if d.Waiting && !d.PauseEndsAt.IsZero() {
		st.PauseEndsAt = timestamppb.New(d.PauseEndsAt)
	}
	return st
}

func statusFields(st *agentv1.AutoBillingStatus) []string {
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	pe := "0"
	if st.GetPauseEndsAt() != nil {
		pe = ms(st.GetPauseEndsAt().AsTime())
	}
	if st.GetStopped() {
		pe = "s"
	}
	return []string{"en", b(st.GetEnabled()), "w", b(st.GetWaiting()), "pe", pe}
}

// TickAutoBilling — один проход: пересмотреть ПК, срок которых пришёл к началу тика. Пакеты по AutoBillingBatch
// выбираются, пока просроченные есть и не вышел бюджет тика (Options.AutoBillingTickBudget; начатый пакет
// доделывается), — на 50 000 ПК просроченные не копятся (S6-O4-10). Внутри пакета ПК разных гостей пересматриваются
// параллельно (Options.AutoBillingWorkers), ПК одного гостя — по очереди в порядке срока, как раньше. Вызывать
// только у одного исполнителя на кластер (worker.AutoBilling). n — сколько ПК пересмотрено.
func (s *Server) TickAutoBilling(ctx context.Context) (int, error) {
	vc := s.o.Valkey
	started := time.Now() // бюджет — по настоящим часам (s.o.Now в тестах стоит на месте)
	now := s.o.Now()
	seen := map[string]struct{}{}
	var errs []error
	n := 0
	for ctx.Err() == nil {
		pcs, err := vc.Do(ctx, vc.B().Zrangebyscore().Key(s.abDue()).Min("-inf").Max(ms(now)).Limit(0, AutoBillingBatch).Build()).AsStrSlice()
		if err != nil {
			errs = append(errs, err)
			break
		}
		// ПК, уже пересмотренный в этом тике, второй раз не берётся (его срок мог остаться прежним, например,
		// если состояние исчезло между чтением и записью) — иначе тик крутился бы на нём.
		fresh := pcs[:0:0]
		for _, raw := range pcs {
			if _, ok := seen[raw]; !ok {
				seen[raw] = struct{}{}
				fresh = append(fresh, raw)
			}
		}
		if len(fresh) == 0 {
			break
		}
		errs = append(errs, s.abBatch(ctx, fresh, now)...)
		n += len(fresh)
		if len(pcs) < AutoBillingBatch || time.Since(started) >= s.o.AutoBillingTickBudget {
			break
		}
	}
	return n, errors.Join(errs...)
}

// abBatch — пересмотр пакета ПК (в порядке срока). ПК одного гостя (по состоянию в Valkey) — одной очередью по
// порядку: две покупки одного гостя на разных ПК не идут параллельно; разные гости — в AutoBillingWorkers потоков.
//
// Данные Postgres для решения читаются заранее, одной транзакцией на клуб (abLoadClub, S7-AB-2), — для первого ПК
// каждой очереди: следующие ПК того же гостя читаются поштучно после предыдущих (покупка на первом ПК меняет баланс).
// Состояние в Valkey и токен гостя каждый ПК, как и раньше, читает сам перед решением (abProcess).
func (s *Server) abBatch(ctx context.Context, pcs []string, now time.Time) []error {
	vc := s.o.Valkey
	cmds := make(valkey.Commands, len(pcs))
	for i, raw := range pcs {
		cmds[i] = vc.B().Hmget().Key(s.o.KeyPrefix+"ab:"+raw).Field("club", "user", "mode").Build()
	}
	groups := map[string]int{}
	var queues [][]string
	reqs := map[int64][]abReq{} // клуб → первые ПК очередей
	for i, r := range vc.DoMulti(ctx, cmds...) {
		var club, user int64
		var mode string
		if f, err := r.ToArray(); err == nil && len(f) == 3 {
			cs, _ := f[0].ToString()
			us, _ := f[1].ToString()
			mode, _ = f[2].ToString()
			club, _ = strconv.ParseInt(cs, 10, 64)
			user, _ = strconv.ParseInt(us, 10, 64)
		}
		g := "pc:" + pcs[i] // гость неизвестен (нет состояния, сбой) — отдельная очередь; abProcess разберётся
		if user > 0 {
			g = "user:" + itoa(user)
		}
		k, ok := groups[g]
		if !ok {
			k = len(queues)
			groups[g] = k
			queues = append(queues, nil)
			pc, err := strconv.ParseInt(pcs[i], 10, 64)
			if err == nil && club > 0 && user > 0 && mode != abStopped {
				reqs[club] = append(reqs[club], abReq{pc: pc, user: user})
			}
		}
		queues[k] = append(queues[k], pcs[i])
	}
	pre := s.abPreload(ctx, reqs)
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	next := make(chan []string)
	for range min(s.o.AutoBillingWorkers, len(queues)) {
		wg.Go(func() {
			for q := range next {
				for i, raw := range q {
					var p *abPre
					if i == 0 {
						p = pre[raw]
					}
					if err := s.abOne(ctx, raw, now, p); err != nil {
						mu.Lock()
						errs = append(errs, err)
						mu.Unlock()
					}
				}
			}
		})
	}
	for _, q := range queues {
		next <- q
	}
	close(next)
	wg.Wait()
	return errs
}

// abPre — данные решения ПК, прочитанные пакетом, и для какого клуба и гостя они прочитаны.
type abPre struct {
	club, user int64
	data       abData
}

// abPreload — abLoadClub по клубам пакета (клубы — в AutoBillingWorkers потоков). Сбой клуба — его ПК читаются
// поштучно (abProcess), ошибка вернётся оттуда.
func (s *Server) abPreload(ctx context.Context, reqs map[int64][]abReq) map[string]*abPre {
	out := make(map[string]*abPre)
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	next := make(chan int64)
	for range min(s.o.AutoBillingWorkers, len(reqs)) {
		wg.Go(func() {
			for club := range next {
				data, err := s.abLoadClub(ctx, club, reqs[club])
				if err != nil {
					s.o.Log.WarnContext(ctx, "auto billing: batch read failed, reading one by one", "club_id", club, "err", err)
					continue
				}
				mu.Lock()
				for _, r := range reqs[club] {
					if d, ok := data[r.pc]; ok {
						out[itoa(r.pc)] = &abPre{club: club, user: r.user, data: d}
					}
				}
				mu.Unlock()
			}
		})
	}
	for club := range reqs {
		next <- club
	}
	close(next)
	wg.Wait()
	return out
}

// abOne — пересмотр одного ПК из zset; сбой — повтор через AutoBillingRetry, а не в каждом тике.
func (s *Server) abOne(ctx context.Context, raw string, now time.Time, pre *abPre) error {
	vc := s.o.Valkey
	pc, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		_ = vc.Do(ctx, vc.B().Zrem().Key(s.abDue()).Member(raw).Build()).Error()
		return nil
	}
	if err := s.abProcess(ctx, pc, pre); err != nil && ctx.Err() == nil {
		_ = vc.Do(ctx, vc.B().Zadd().Key(s.abDue()).ScoreMember().ScoreMember(float64(now.Add(AutoBillingRetry).UnixMilli()), raw).Build()).Error()
		return err
	}
	return nil
}

// AutoBillingBacklog — отставание автобиллинга (метрики zafiro_agent_auto_billing_*): сколько ПК ждут пересмотра
// (срок пришёл) и насколько просрочен самый старый срок. Ключ общий для кластера — все инстансы видят одно и то же.
func (s *Server) AutoBillingBacklog(ctx context.Context) (due int64, oldest time.Duration, err error) {
	vc := s.o.Valkey
	now := s.o.Now()
	res := vc.DoMulti(ctx,
		vc.B().Zcount().Key(s.abDue()).Min("-inf").Max(ms(now)).Build(),
		vc.B().Zrange().Key(s.abDue()).Min("0").Max("0").Withscores().Build())
	if due, err = res[0].AsInt64(); err != nil {
		return 0, 0, err
	}
	first, err := res[1].AsZScores()
	if err != nil {
		return 0, 0, err
	}
	if len(first) > 0 {
		if at := time.UnixMilli(int64(first[0].Score)); at.Before(now) {
			oldest = now.Sub(at)
		}
	}
	return due, oldest, nil
}

// abProcess — шаг автобиллинга ПК (autoBillingTick старого агента).
func (s *Server) abProcess(ctx context.Context, pc int64, pre *abPre) error {
	vc := s.o.Valkey
	// Состояние и гость, вошедший на ПК (токен), — одним обращением к Valkey.
	m, cur, err := s.tokens.computerUserAnd(ctx, pc, s.abKey(pc))
	if err != nil {
		return err
	}
	st, ok := parseAB(m)
	if !ok {
		return vc.Do(ctx, vc.B().Zrem().Key(s.abDue()).Member(itoa(pc)).Build()).Error()
	}
	// Гость всё ещё вошёл на этом ПК? Нет — состояние забывается (userName === null: сброс всего).
	if cur.UserID != st.user || cur.ClubID != st.club {
		return s.abForget(ctx, pc)
	}
	id := identity.Identity{ClubID: st.club, ComputerID: pc}
	now := s.o.Now()
	if st.mode == abStopped {
		return s.abPush(ctx, id, st, &agentv1.AutoBillingStatus{Stopped: true}, "-")
	}
	// Данные пакета — если прочитаны для того же клуба и гостя, что в состоянии сейчас; иначе — поштучно.
	var data abData
	fresh := pre == nil || pre.club != st.club || pre.user != st.user
	if fresh {
		if data, err = s.abLoad(ctx, id, st.user); err != nil {
			return err
		}
	} else {
		data = pre.data
	}
	v := data.view(st.user, now)
	d := abDecide(st, v, now)
	if d.BuyMinutes > 0 && !fresh {
		// Покупка — только по данным, прочитанным прямо перед ней, как раньше: пакет читался до пересмотра других
		// ПК (за это время гость мог купить сам, пополнить или потратить баланс).
		if data, err = s.abLoad(ctx, id, st.user); err != nil {
			return err
		}
		v = data.view(st.user, now)
		d = abDecide(st, v, now)
	}
	if v.session != nil {
		// Идёт сессия: пересмотр — к её концу (после него — сразу, autoAfterTariff); пауза сбрасывается.
		return s.abPush(ctx, id, st, abStatus(v.offer.AutoBilling && v.offer.Available, pricing.AutoBillingDecision{}, false),
			ms(v.session.GetEndsAt().AsTime()), "pause", "0")
	}
	enabled := v.offer.AutoBilling && v.offer.Available
	switch {
	case d.BuyMinutes > 0:
		return s.abBuy(ctx, id, st, d)
	case d.Waiting:
		fields := []string{}
		if st.pause == 0 {
			fields = append(fields, "pause", ms(now))
		}
		return s.abPush(ctx, id, st, abStatus(enabled, d, false), ms(d.PauseEndsAt), fields...)
	default:
		// Не ждёт (нет денег, выключен): пауза сбрасывается, пересмотр позже. Режим «после тарифа» остаётся, пока
		// гость не купит или не выйдет — как autoAfterTariff старого агента.
		return s.abPush(ctx, id, st, abStatus(enabled, d, false), ms(abRecheckAt(id.ClubID, now)), "pause", "0")
	}
}

// abRecheckAt — срок следующего пересмотра ПК, которому автобиллинг сейчас не положен: ближайший момент позже now в
// слоте клуба — смещении внутри периода AutoBillingRecheck, своём у каждого клуба (номер клуба × 7919 по модулю
// периода в миллисекундах: 7919 — простое, взаимно простое с 30 000, у 30 000 клубов подряд слоты разные). Пересмотр не позже чем через AutoBillingRecheck (первый — раньше, дальше — ровно
// через период). ПК клуба, ждущие пересмотра, приходят в одну миллисекунду, стоят в zset подряд и читаются пакетом
// одной транзакцией (S7-AB-2); клубы разнесены по периоду, а не приходят все в один тик.
func abRecheckAt(club int64, now time.Time) time.Time {
	period := AutoBillingRecheck.Milliseconds()
	off := (club%period + period) % period * 7919 % period
	t := now.UnixMilli()
	return time.UnixMilli(t - ((t-off)%period+period)%period + period)
}

// abDecide — решение автобиллинга без идущей сессии (pricing.AutoBilling по состоянию ПК и данным из Postgres).
func abDecide(st abState, v abView, now time.Time) pricing.AutoBillingDecision {
	if v.session != nil {
		return pricing.AutoBillingDecision{}
	}
	var pauseStart time.Time
	if st.pause > 0 {
		pauseStart = time.UnixMilli(st.pause)
	}
	return pricing.AutoBilling(v.offer, pricing.AutoBillingState{SignedIn: true, AfterTariff: st.mode == abAfter || v.expired,
		PauseStartedAt: pauseStart}, v.main, now)
}

// abPush — сохранить поля и срок; если состояние для агента изменилось — команда AutoBillingChanged.
func (s *Server) abPush(ctx context.Context, id identity.Identity, st abState, status *agentv1.AutoBillingStatus, due string, fields ...string) error {
	sf := statusFields(status)
	changed := st.status != sf[1]+"/"+sf[3]+"/"+sf[5]
	if err := s.abUpdate(ctx, id.ComputerID, st.signin, due, append(fields, sf...)...); err != nil {
		return err
	}
	if changed && s.o.Commands != nil {
		if _, err := s.o.Commands.Send(ctx, id.ClubID, id.ComputerID, &agentv1.Command{Kind: &agentv1.Command_AutoBillingChanged{
			AutoBillingChanged: &agentv1.AutoBillingChanged{UserId: st.user, Status: status}}}); err != nil {
			s.o.Log.WarnContext(ctx, "auto billing: notify agent", "computer_id", id.ComputerID, "err", err)
		}
	}
	return nil
}

// abBuy — покупка минут на весь основной баланс (startAutoBilling: purchaseMinutes(affordableMinutes(), 'main')).
// Отказ (нет денег, бронь, чужая сессия…) — стоп до нового входа (purchaseResult: autoBuying && !success →
// autoStopped). Неизвестная ошибка (БД) — повтор тем же ключом.
func (s *Server) abBuy(ctx context.Context, id identity.Identity, st abState, d pricing.AutoBillingDecision) error {
	key := "autobill:" + itoa(id.ComputerID) + ":" + itoa(st.user) + ":" + itoa(st.signin) + ":" + itoa(st.step)
	// Системное действие от имени гостя: журнал time.purchased требует IP (как у истечения сессий).
	ctx = activity.WithRequestMeta(ctx, activity.RequestMeta{IP: "127.0.0.1", UserAgent: "ZafiroAgent/auto-billing"})
	res, err := s.o.Billing.PurchaseWholeBalance(ctx, billingsvc.WholeBalanceRequest{
		ClubID: id.ClubID, UserID: st.user, ComputerID: id.ComputerID, IdempotencyKey: key,
		Actor: billingsvc.Actor{UserID: st.user, Kind: "agent"}, AutoBilling: true, MinutesHint: d.BuyMinutes,
	})
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if known, _ := toStatus(err); !known || errors.Is(err, billingsvc.ErrIdempotencyConflict) {
			return err
		}
		s.o.Log.InfoContext(ctx, "auto billing stopped: purchase refused", "computer_id", id.ComputerID, "user_id", st.user, "err", err)
		return s.abPush(ctx, id, st, &agentv1.AutoBillingStatus{Enabled: true, Stopped: true}, "-", "mode", abStopped, "pause", "0")
	}
	s.o.Log.InfoContext(ctx, "auto billing purchased minutes", "computer_id", id.ComputerID, "user_id", st.user,
		"minutes", res.MinutesAdded, "replayed", res.Replayed)
	return s.abPush(ctx, id, st, &agentv1.AutoBillingStatus{Enabled: true}, ms(res.SessionEndsAt),
		"mode", abPause, "pause", "0", "step", itoa(st.step+1))
}

// autoBillingStatus — состояние для GetAutoBilling: последнее отправленное агенту; нет состояния (Valkey очищен) —
// автобиллинг заводится заново, как после входа.
func (s *Server) autoBillingStatus(ctx context.Context, id identity.Identity, g Guest) (*agentv1.AutoBillingStatus, error) {
	vc := s.o.Valkey
	m, err := vc.Do(ctx, vc.B().Hgetall().Key(s.abKey(id.ComputerID)).Build()).AsStrMap()
	if err != nil {
		return nil, err
	}
	st, ok := parseAB(m)
	if !ok || st.user != g.UserID {
		s.abSignedIn(ctx, id, g.UserID)
		return &agentv1.AutoBillingStatus{}, nil
	}
	out := &agentv1.AutoBillingStatus{Enabled: m["en"] == "1", Waiting: m["w"] == "1", Stopped: st.mode == abStopped}
	if pe, err := strconv.ParseInt(m["pe"], 10, 64); err == nil && pe > 0 && out.Waiting {
		out.PauseEndsAt = timestamppb.New(time.UnixMilli(pe))
	}
	return out, nil
}
