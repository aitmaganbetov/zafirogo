package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// derived — расширение M2 «производные данные из activity_logs»: колонки и таблицы нового сервера, которых не было
// в старой схеме (старый код вычислял их из журнала действий при каждом чтении):
//   - tariff_sales.sold (Q-007, SV-2) — число записей tariff.purchased по тарифу
//     (AgentTariffPurchaseService::purchaseCount()/availableFor()); кассовые гостевые продажи журнал не писали;
//   - cash_transactions.income_kind (CSQ-2) — ShiftReport::for() $logged: запись cash.income этой операции с
//     details.balance_type (пополнение) или details.computers (гостевая сессия); payment_method (Q-013) — из
//     details.payment_method (подпись способа на языке оператора → cash|card|qr по переводам старой базы);
//   - session_purchases.zone_id/source (SV-3) — части ИДУЩИХ сессий: tariff.purchased → tariff (зона ПК покупки),
//     time.purchased → minutes (subject — зона), time.package_started → package (subject — ПК).
type derived struct{}

// Derived — расширение M2 «производные данные».
func Derived() Extension { return derived{} }

func (derived) Name() string { return "derived" }

func (e derived) Plan(ctx context.Context, env *Env) error  { return e.run(ctx, env, modePlan) }
func (e derived) Apply(ctx context.Context, env *Env) error { return e.run(ctx, env, modeApply) }
func (e derived) Verify(ctx context.Context, env *Env) error {
	return e.run(ctx, env, modeVerify)
}

func (e derived) run(ctx context.Context, env *Env, mode extMode) error {
	if err := e.tariffSales(ctx, env, mode); err != nil {
		return err
	}
	if err := e.incomeKinds(ctx, env, mode); err != nil {
		return err
	}
	return e.sessionZones(ctx, env, mode)
}

// Morph-классы subject_type (Model::getMorphClass() — имя класса).
const (
	morphTariff          = `App\Models\Tariff`
	morphCashTransaction = `App\Models\CashTransaction`
)

// tariffSales — Q-007: sold = COUNT(activity_logs tariff.purchased) по тарифу клуба.
func (e derived) tariffSales(ctx context.Context, env *Env, mode extMode) error {
	rows, err := env.Source.QueryContext(ctx, `SELECT subject_id, COUNT(*) FROM activity_logs
		WHERE event = 'tariff.purchased' AND subject_type = ? AND club_id = ?
		  AND subject_id IN (SELECT id FROM tariffs WHERE club_id = ?)
		GROUP BY subject_id ORDER BY subject_id`, morphTariff, env.ClubID, env.ClubID)
	if err != nil {
		return fmt.Errorf("mysql: tariff sales: %w", err)
	}
	want := map[int64]int64{}
	var ids, sold []int64
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			_ = rows.Close()
			return err
		}
		want[id] = n
		ids, sold = append(ids, id), append(sold, n)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	stat := env.Report.table("tariff_sales")
	stat.Source = int64(len(want))
	switch mode {
	case modeApply:
		if len(ids) == 0 {
			return nil
		}
		tag, err := env.Target.Exec(ctx, `INSERT INTO tariff_sales (club_id, tariff_id, sold, updated_at)
			SELECT $1, u.id, u.n, now() FROM unnest($2::bigint[], $3::bigint[]) AS u(id, n)`, env.ClubID, ids, sold)
		if err != nil {
			return fmt.Errorf("pg: tariff_sales: %w", err)
		}
		stat.Written = tag.RowsAffected()
	case modeVerify:
		pgRows, err := env.Target.Query(ctx, "SELECT tariff_id, sold FROM tariff_sales WHERE club_id = $1", env.ClubID)
		if err != nil {
			return fmt.Errorf("pg: tariff_sales: %w", err)
		}
		defer pgRows.Close()
		got := map[int64]int64{}
		for pgRows.Next() {
			var id, n int64
			if err := pgRows.Scan(&id, &n); err != nil {
				return err
			}
			got[id] = n
		}
		if err := pgRows.Err(); err != nil {
			return err
		}
		stat.Target = int64(len(got))
		for id, n := range want {
			if got[id] != n {
				env.Add(Blocker, CodeVerifyMismatch, "tariff_sales", "sold", "", strconv.FormatInt(id, 10),
					fmt.Sprintf("продажи тарифа: журнал MySQL %d, tariff_sales %d", n, got[id]))
			}
		}
		for id, n := range got {
			if _, ok := want[id]; !ok && n != 0 {
				env.Add(Blocker, CodeVerifyMismatch, "tariff_sales", "sold", "", strconv.FormatInt(id, 10), "лишняя строка tariff_sales")
			}
		}
	}
	return nil
}

// incomeKind — вид и способ оплаты прихода.
type incomeKind struct {
	ID            int64
	Kind, Payment *string
}

// paymentCodes — подпись способа оплаты (как её писал старый код: __('наличные') на языке оператора) → код.
// Подписи — ключи и переводы из translations старой базы (CashRegisterService::topUpBalance(), GuestSessions::sell()).
func paymentCodes(ctx context.Context, env *Env) (map[string]string, error) {
	keys := map[string]string{"наличные": "cash", "карта": "card", "QR-код": "qr"}
	out := map[string]string{}
	for k, v := range keys {
		out[strings.ToLower(k)] = v
	}
	rows, err := env.Source.QueryContext(ctx, "SELECT `key`, value FROM translations WHERE `group` = '*' AND `key` IN ('наличные', 'карта', 'QR-код')")
	if err != nil {
		return nil, fmt.Errorf("mysql: payment labels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		label := strings.ToLower(strings.TrimSpace(v))
		if prev, ok := out[label]; ok && prev != keys[k] {
			out[label] = "" // одна подпись у разных способов — не угадываем
			continue
		}
		out[label] = keys[k]
	}
	return out, rows.Err()
}

// incomeKinds — CSQ-2 и Q-013 для приходов клуба.
func (e derived) incomeKinds(ctx context.Context, env *Env, mode extMode) error {
	codes, err := paymentCodes(ctx, env)
	if err != nil {
		return err
	}
	// whereNotNull('details->balance_type') Laravel = json_unquote(json_extract(details, '$."balance_type"')) is not null.
	rows, err := env.Source.QueryContext(ctx, `SELECT t.id,
		  EXISTS (SELECT 1 FROM activity_logs l WHERE l.event = 'cash.income' AND l.subject_type = ? AND l.subject_id = t.id
		          AND json_unquote(json_extract(l.details, '$."balance_type"')) IS NOT NULL),
		  EXISTS (SELECT 1 FROM activity_logs l WHERE l.event = 'cash.income' AND l.subject_type = ? AND l.subject_id = t.id
		          AND json_unquote(json_extract(l.details, '$."computers"')) IS NOT NULL),
		  (SELECT json_unquote(json_extract(l.details, '$."payment_method"')) FROM activity_logs l
		    WHERE l.event = 'cash.income' AND l.subject_type = ? AND l.subject_id = t.id ORDER BY l.id LIMIT 1)
		FROM cash_transactions t WHERE t.club_id = ? AND t.type = 'income' ORDER BY t.id`,
		morphCashTransaction, morphCashTransaction, morphCashTransaction, env.ClubID)
	if err != nil {
		return fmt.Errorf("mysql: income kinds: %w", err)
	}
	var list []incomeKind
	topUps, guests := 0, 0
	for rows.Next() {
		var ik incomeKind
		var topUp, guest bool
		var pm sql.NullString
		if err := rows.Scan(&ik.ID, &topUp, &guest, &pm); err != nil {
			_ = rows.Close()
			return err
		}
		id := strconv.FormatInt(ik.ID, 10)
		switch {
		case topUp && guest:
			env.Add(Warning, CodeIncomeKind, "cash_transactions", "income_kind", "", id,
				"журнал отмечает приход и пополнением, и гостевой сессией (старый X/Z считал его дважды) — перенесён как top_up")
			fallthrough
		case topUp:
			k := "top_up"
			ik.Kind = &k
			topUps++
		case guest:
			k := "guest_session"
			ik.Kind = &k
			guests++
		}
		if pm.Valid && pm.String != "null" {
			if code := codes[strings.ToLower(strings.TrimSpace(pm.String))]; code != "" {
				ik.Payment = &code
			} else {
				env.Add(Info, CodeIncomeKind, "cash_transactions", "payment_method", pm.String, id,
					"способ оплаты из журнала не распознан — payment_method не заполнен")
			}
		}
		if ik.Kind != nil || ik.Payment != nil {
			list = append(list, ik)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	env.Report.Totals["income_kind: пополнений / гостевых сессий"] = fmt.Sprintf("%d / %d", topUps, guests)
	switch mode {
	case modeApply:
		if len(list) == 0 {
			return nil
		}
		ids := make([]int64, len(list))
		kinds := make([]*string, len(list))
		pms := make([]*string, len(list))
		for i, ik := range list {
			ids[i], kinds[i], pms[i] = ik.ID, ik.Kind, ik.Payment
		}
		tag, err := env.Target.Exec(ctx, `UPDATE cash_transactions AS t SET income_kind = u.k, payment_method = u.p
			FROM unnest($1::bigint[], $2::text[], $3::text[]) AS u(id, k, p) WHERE t.club_id = $4 AND t.id = u.id`,
			ids, kinds, pms, env.ClubID)
		if err != nil {
			return fmt.Errorf("pg: income_kind: %w", err)
		}
		if tag.RowsAffected() != int64(len(list)) {
			return fmt.Errorf("pg: income_kind: updated %d of %d", tag.RowsAffected(), len(list))
		}
	case modeVerify:
		want := make(map[int64]incomeKind, len(list))
		for _, ik := range list {
			want[ik.ID] = ik
		}
		pgRows, err := env.Target.Query(ctx,
			"SELECT id, income_kind, payment_method FROM cash_transactions WHERE club_id = $1 AND type = 'income'", env.ClubID)
		if err != nil {
			return fmt.Errorf("pg: income_kind: %w", err)
		}
		defer pgRows.Close()
		for pgRows.Next() {
			var got incomeKind
			if err := pgRows.Scan(&got.ID, &got.Kind, &got.Payment); err != nil {
				return err
			}
			w := want[got.ID]
			if deref(w.Kind) != deref(got.Kind) || deref(w.Payment) != deref(got.Payment) {
				env.Add(Blocker, CodeVerifyMismatch, "cash_transactions", "income_kind", "", strconv.FormatInt(got.ID, 10),
					fmt.Sprintf("вид/способ прихода: ожидалось %s/%s, в Postgres %s/%s", deref(w.Kind), deref(w.Payment),
						deref(got.Kind), deref(got.Payment)))
			}
		}
		return pgRows.Err()
	}
	return nil
}

// sessionPart — часть идущей сессии и что о ней сказал журнал.
type sessionPart struct {
	ID      int64
	Session string
	Kind    string
	Source  *string
	ZoneID  *int64
}

type purchaseLog struct {
	Event      string
	SubjectID  int64
	ComputerID sql.NullInt64
}

// sessionZones — SV-3: zone_id/source частей идущих сессий (части живут ≤ 8 дней; для закрытых сессий не нужны).
// Части сессии и записи журнала tariff.purchased|time.purchased|time.package_started с details.session_id
// сопоставляются по порядку (одна покупка — одна часть и одна запись в той же транзакции); вид должен совпасть,
// иначе части сессии остаются NULL (новый сервер читает их по kind — S1-SES-02).
func (e derived) sessionZones(ctx context.Context, env *Env, mode extMode) error {
	parts, err := e.currentParts(ctx, env)
	if err != nil || len(parts) == 0 {
		return err
	}
	sessions := map[string][]*sessionPart{}
	var order []string
	for i := range parts {
		p := &parts[i]
		if _, ok := sessions[p.Session]; !ok {
			order = append(order, p.Session)
		}
		sessions[p.Session] = append(sessions[p.Session], p)
	}
	zoneOf, err := computerZones(ctx, env)
	if err != nil {
		return err
	}
	matched := 0
	for _, sid := range order {
		logs, err := sessionLogs(ctx, env, sid)
		if err != nil {
			return err
		}
		ps := sessions[sid]
		if !partsMatchLogs(ps, logs) {
			env.Add(Warning, CodeSessionZone, "session_purchases", "zone_id", "", strconv.FormatInt(ps[0].ID, 10),
				"части идущей сессии не сопоставляются с журналом — zone_id/source не заполнены (сервер читает по kind)")
			continue
		}
		for i, l := range logs {
			p := ps[i]
			var src string
			var zone int64
			switch l.Event {
			case "tariff.purchased":
				src, zone = "tariff", zoneOf[l.ComputerID.Int64]
			case "time.purchased":
				src, zone = "minutes", l.SubjectID
			case "time.package_started":
				src, zone = "package", zoneOf[l.SubjectID]
			}
			p.Source = &src
			if zone != 0 {
				z := zone
				p.ZoneID = &z
			}
			matched++
		}
	}
	env.Report.Totals["SV-3: части идущих сессий с зоной/источником"] = fmt.Sprintf("%d из %d", matched, len(parts))
	switch mode {
	case modeApply:
		var ids []int64
		var srcs []*string
		var zones []*int64
		for _, p := range parts {
			if p.Source == nil {
				continue
			}
			ids, srcs, zones = append(ids, p.ID), append(srcs, p.Source), append(zones, p.ZoneID)
		}
		if len(ids) == 0 {
			return nil
		}
		_, err := env.Target.Exec(ctx, `UPDATE session_purchases AS s SET source = u.src, zone_id = u.zone
			FROM unnest($1::bigint[], $2::text[], $3::bigint[]) AS u(id, src, zone) WHERE s.club_id = $4 AND s.id = u.id`,
			ids, srcs, zones, env.ClubID)
		if err != nil {
			return fmt.Errorf("pg: session_purchases zone: %w", err)
		}
	case modeVerify:
		for _, p := range parts {
			var src *string
			var zone *int64
			if err := env.Target.QueryRow(ctx, "SELECT source, zone_id FROM session_purchases WHERE club_id = $1 AND id = $2",
				env.ClubID, p.ID).Scan(&src, &zone); err != nil {
				return fmt.Errorf("pg: session_purchases %d: %w", p.ID, err)
			}
			if deref(src) != deref(p.Source) || derefInt(zone) != derefInt(p.ZoneID) {
				env.Add(Blocker, CodeVerifyMismatch, "session_purchases", "zone_id", "", strconv.FormatInt(p.ID, 10),
					"источник/зона части идущей сессии отличаются от журнала")
			}
		}
	}
	return nil
}

func derefInt(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// partsMatchLogs — части и записи журнала одной сессии соответствуют по порядку и виду.
func partsMatchLogs(parts []*sessionPart, logs []purchaseLog) bool {
	if len(parts) != len(logs) {
		return false
	}
	for i, l := range logs {
		want := "tariff"
		if l.Event == "time.purchased" {
			want = "minutes"
		}
		if parts[i].Kind != want {
			return false
		}
	}
	return true
}

func (e derived) currentParts(ctx context.Context, env *Env) ([]sessionPart, error) {
	rows, err := env.Source.QueryContext(ctx, `SELECT p.id, p.session_id, p.kind FROM session_purchases p
		WHERE p.club_id = ? AND p.session_id IN (
		    SELECT c.current_session_id FROM club_computers c WHERE c.club_id = ? AND c.current_session_id IS NOT NULL)
		ORDER BY p.id`, env.ClubID, env.ClubID)
	if err != nil {
		return nil, fmt.Errorf("mysql: current session parts: %w", err)
	}
	defer rows.Close()
	var out []sessionPart
	for rows.Next() {
		var p sessionPart
		if err := rows.Scan(&p.ID, &p.Session, &p.Kind); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func sessionLogs(ctx context.Context, env *Env, sessionID string) ([]purchaseLog, error) {
	rows, err := env.Source.QueryContext(ctx, `SELECT event, COALESCE(subject_id, 0),
		  CAST(json_unquote(json_extract(details, '$."computer_id"')) AS UNSIGNED)
		FROM activity_logs
		WHERE club_id = ? AND event IN ('tariff.purchased', 'time.purchased', 'time.package_started')
		  AND json_unquote(json_extract(details, '$."session_id"')) = ?
		ORDER BY id`, env.ClubID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("mysql: session logs: %w", err)
	}
	defer rows.Close()
	var out []purchaseLog
	for rows.Next() {
		var l purchaseLog
		if err := rows.Scan(&l.Event, &l.SubjectID, &l.ComputerID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func computerZones(ctx context.Context, env *Env) (map[int64]int64, error) {
	rows, err := env.Source.QueryContext(ctx, "SELECT id, club_zone_id FROM club_computers WHERE club_id = ? AND club_zone_id IS NOT NULL", env.ClubID)
	if err != nil {
		return nil, fmt.Errorf("mysql: computer zones: %w", err)
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, zone int64
		if err := rows.Scan(&id, &zone); err != nil {
			return nil, err
		}
		out[id] = zone
	}
	return out, rows.Err()
}
