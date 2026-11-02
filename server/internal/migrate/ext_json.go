package migrate

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/shop"
)

// jsonV2 — расширение M2 «JSON v2» (решение Q-003, docs/schema-changes.md §10.11): receipt_data чеков кассы,
// shop_orders.items и уровни в clubs.settings — в формат v2 (тиыны, базисные пункты, "v": 2). Конвертация — через
// те же функции чтения v1, что у сервера (cashsvc.DecodeReceipt, shop.DecodeItems, bonus.ParseSettings): значение,
// прочитанное сервером до и после, совпадает до тиына (проверяется при записи и в Verify). Заодно:
//   - CQ-9 — product_id в строки старых чеков без product_id, где имя товара однозначно;
//   - Q-023/BQ-3 — нормализация настроек (settings_norm.go).
type jsonV2 struct{}

// JSONv2 — расширение M2 «JSON v2».
func JSONv2() Extension { return jsonV2{} }

func (jsonV2) Name() string { return "json-v2" }

func (e jsonV2) Plan(ctx context.Context, env *Env) error  { return e.run(ctx, env, modeWrite(false)) }
func (e jsonV2) Apply(ctx context.Context, env *Env) error { return e.run(ctx, env, modeWrite(true)) }
func (e jsonV2) Verify(ctx context.Context, env *Env) error {
	return e.run(ctx, env, modeVerify)
}

// extMode — что делает проход расширения: только отчёт (plan), запись (apply), сверка (verify).
type extMode int

const (
	modePlan extMode = iota
	modeApply
	modeVerify
)

func modeWrite(write bool) extMode {
	if write {
		return modeApply
	}
	return modePlan
}

func (e jsonV2) run(ctx context.Context, env *Env, mode extMode) error {
	if err := e.receipts(ctx, env, mode); err != nil {
		return err
	}
	if err := e.shopItems(ctx, env, mode); err != nil {
		return err
	}
	return e.settings(ctx, env, mode)
}

// converted — значение JSON после конвертации и что сервер должен прочитать.
type converted struct {
	ID   int64
	JSON string // v2
}

// receipts — cash_transactions.receipt_data v1 → v2 (+ CQ-9).
func (e jsonV2) receipts(ctx context.Context, env *Env, mode extMode) error {
	rows, err := env.Source.QueryContext(ctx,
		"SELECT id, receipt_data FROM cash_transactions WHERE club_id = ? AND receipt_data IS NOT NULL ORDER BY id", env.ClubID)
	if err != nil {
		return fmt.Errorf("mysql: receipts: %w", err)
	}
	type src struct {
		ID  int64
		Raw string
	}
	var list []src
	for rows.Next() {
		var s src
		if err := rows.Scan(&s.ID, &s.Raw); err != nil {
			_ = rows.Close()
			return err
		}
		list = append(list, s)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	names := newProductNames(env)
	var out []converted
	want := map[int64]*cash.Receipt{}
	filled := 0
	for _, s := range list {
		id := strconv.FormatInt(s.ID, 10)
		before, err := cashsvc.DecodeReceipt(json.RawMessage(s.Raw))
		if err != nil {
			// Сервер не прочитает этот чек и сейчас (сумма не разбирается) — оставляем как есть.
			env.Add(Warning, CodeJSONv2, "cash_transactions", "receipt_data", "", id, "чек не читается сервером (v1) — оставлен как есть")
			continue
		}
		if before == nil {
			continue
		}
		if isV2Object(s.Raw) {
			want[s.ID] = before
			continue
		}
		// CQ-9: товар старого чека без product_id — по имени, если имя однозначно (возврат искал where('name')->first()).
		for i := range before.Items {
			l := &before.Items[i]
			if l.ProductID != 0 || l.ServiceID != 0 {
				continue
			}
			pid, err := names.unique(ctx, l.Name)
			if err != nil {
				return err
			}
			if pid == 0 {
				env.Add(Info, CodeReceiptID, "cash_transactions", "receipt_data", "", id,
					"строка старого чека без product_id: имя товара не найдено или не однозначно — оставлена без product_id")
				continue
			}
			l.ProductID = pid
			filled++
		}
		v2, err := encodeReceiptV2(before)
		if err != nil {
			return err
		}
		after, err := cashsvc.DecodeReceipt(v2)
		if err != nil || !reflect.DeepEqual(before, after) {
			env.Add(Blocker, CodeJSONv2, "cash_transactions", "receipt_data", "", id, "чек v2 читается иначе, чем v1 (ошибка конвертации)")
			continue
		}
		want[s.ID] = before
		out = append(out, converted{ID: s.ID, JSON: string(v2)})
	}
	env.Report.Totals["receipt_data → v2"] = strconv.Itoa(len(out))
	if filled > 0 {
		env.Report.Totals["CQ-9: product_id в старых чеках"] = strconv.Itoa(filled)
	}
	switch mode {
	case modeApply:
		return updateJSON(ctx, env, "cash_transactions", "receipt_data", out)
	case modeVerify:
		return verifyJSON(ctx, env, "cash_transactions", "receipt_data", func(id int64, raw []byte) bool {
			w, ok := want[id]
			if !ok {
				return true // не конвертировался (предупреждение выше) — ядро сверило строку
			}
			got, err := cashsvc.DecodeReceipt(raw)
			return err == nil && isV2Object(string(raw)) && reflect.DeepEqual(w, got)
		}, keysOf(want))
	}
	return nil
}

// encodeReceiptV2 — receipt_data v2 тем же форматом, что cash/service encodeReceipt() (там не экспортирован):
// {"v":2,"items":[{"product_id"|"service_id","name","quantity","price_tiyn","total_tiyn"}],"top_up":{…},
// "discount_amount_tiyn"}. Совпадение формата проверяет чтение обратно cashsvc.DecodeReceipt.
func encodeReceiptV2(r *cash.Receipt) (json.RawMessage, error) {
	out := map[string]any{"v": cashsvc.ReceiptVersion}
	if r.Items != nil {
		items := make([]map[string]any, 0, len(r.Items))
		for _, l := range r.Items {
			line := map[string]any{"name": l.Name, "quantity": l.Quantity, "price_tiyn": int64(l.Price), "total_tiyn": int64(l.Total)}
			if l.ServiceID != 0 {
				line["service_id"] = l.ServiceID
			} else if l.ProductID != 0 {
				line["product_id"] = l.ProductID
			}
			items = append(items, line)
		}
		out["items"] = items
	}
	if r.TopUp != nil {
		out["top_up"] = map[string]any{"user_id": r.TopUp.UserID, "balance_type": string(r.TopUp.Field), "bonus_tiyn": int64(r.TopUp.Bonus)}
	}
	if r.DiscountAmount != 0 {
		out["discount_amount_tiyn"] = int64(r.DiscountAmount)
	}
	return json.Marshal(out)
}

func isV2Object(raw string) bool { return isLevelsV2JSON(json.RawMessage(raw)) }

// productNames — CQ-9: id товара клуба по имени, если имя однозначно. Сравнение — в MySQL (сортировка
// utf8mb4_unicode_ci, как where('name', …) старого кода), только не удалённые (SoftDeletes).
type productNames struct {
	env   *Env
	cache map[string]int64
}

func newProductNames(env *Env) *productNames {
	return &productNames{env: env, cache: map[string]int64{}}
}

func (p *productNames) unique(ctx context.Context, name string) (int64, error) {
	if id, ok := p.cache[name]; ok {
		return id, nil
	}
	rows, err := p.env.Source.QueryContext(ctx,
		"SELECT id FROM products WHERE club_id = ? AND deleted_at IS NULL AND name = ? ORDER BY id LIMIT 2", p.env.ClubID, name)
	if err != nil {
		return 0, fmt.Errorf("mysql: product by name: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	var id int64
	if len(ids) == 1 {
		id = ids[0]
	}
	p.cache[name] = id
	return id, rows.Err()
}

// shopItems — shop_orders.items v1 → v2.
func (e jsonV2) shopItems(ctx context.Context, env *Env, mode extMode) error {
	rows, err := env.Source.QueryContext(ctx, "SELECT id, items FROM shop_orders WHERE club_id = ? ORDER BY id", env.ClubID)
	if err != nil {
		return fmt.Errorf("mysql: shop_orders: %w", err)
	}
	type src struct {
		ID  int64
		Raw sql.NullString
	}
	var list []src
	for rows.Next() {
		var s src
		if err := rows.Scan(&s.ID, &s.Raw); err != nil {
			_ = rows.Close()
			return err
		}
		list = append(list, s)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var out []converted
	want := map[int64][]shop.Line{}
	for _, s := range list {
		if !s.Raw.Valid || isV2Object(s.Raw.String) {
			continue
		}
		id := strconv.FormatInt(s.ID, 10)
		before, err := shop.DecodeItems(json.RawMessage(s.Raw.String))
		if err != nil {
			env.Add(Warning, CodeJSONv2, "shop_orders", "items", "", id, "состав заказа не читается сервером (v1) — оставлен как есть")
			continue
		}
		if before == nil {
			continue
		}
		v2, err := shop.EncodeItems(before)
		if err != nil {
			return err
		}
		after, err := shop.DecodeItems(v2)
		if err != nil || !reflect.DeepEqual(before, after) {
			env.Add(Blocker, CodeJSONv2, "shop_orders", "items", "", id, "заказ v2 читается иначе, чем v1 (ошибка конвертации)")
			continue
		}
		want[s.ID] = before
		out = append(out, converted{ID: s.ID, JSON: string(v2)})
	}
	env.Report.Totals["shop_orders.items → v2"] = strconv.Itoa(len(out))
	switch mode {
	case modeApply:
		return updateJSON(ctx, env, "shop_orders", "items", out)
	case modeVerify:
		return verifyJSON(ctx, env, "shop_orders", "items", func(id int64, raw []byte) bool {
			got, err := shop.DecodeItems(raw)
			return err == nil && isV2Object(string(raw)) && reflect.DeepEqual(want[id], got)
		}, keysOf(want))
	}
	return nil
}

// settings — clubs.settings: нормализация (Q-023) и уровни v2 (Q-003, BQ-3).
func (e jsonV2) settings(ctx context.Context, env *Env, mode extMode) error {
	stored, err := sourceSettings(ctx, env)
	if err != nil || stored == nil {
		return err
	}
	norm, changes, err := normalizeSettings(stored)
	if err != nil {
		return err
	}
	if mode != modeVerify {
		for _, c := range changes {
			sev, msg := Info, "нормализовано: "+c.From+" → "+c.To
			if c.Warning != "" {
				sev, msg = Warning, c.Warning
			}
			env.Add(sev, CodeSettingsNrm, "clubs", "settings", c.Path, strconv.FormatInt(env.ClubID, 10), msg)
		}
	}
	expected := stored
	if norm != nil {
		expected = norm
	}
	switch mode {
	case modeApply:
		if norm == nil {
			return nil
		}
		_, err := env.Target.Exec(ctx, "UPDATE clubs SET settings = $1::jsonb WHERE id = $2", string(norm), env.ClubID)
		return err
	case modeVerify:
		var got []byte
		if err := env.Target.QueryRow(ctx, "SELECT settings::text FROM clubs WHERE id = $1", env.ClubID).Scan(&got); err != nil {
			return fmt.Errorf("pg: club settings: %w", err)
		}
		if !jsonEqual(expected, got) {
			env.Add(Blocker, CodeVerifyMismatch, "clubs", "settings", "", strconv.FormatInt(env.ClubID, 10),
				"настройки клуба в Postgres не совпадают с нормализованными настройками MySQL")
		}
		// Чтение сервера (bonus.ParseSettings) до и после — те же уровни и флаги.
		before, err1 := bonus.ParseSettings(stored)
		after, err2 := bonus.ParseSettings(got)
		if (err1 == nil) != (err2 == nil) || !reflect.DeepEqual(before, after) {
			env.Add(Blocker, CodeVerifyMismatch, "clubs", "settings", "finances", strconv.FormatInt(env.ClubID, 10),
				"бонусы/лояльность читаются сервером иначе, чем до переноса")
		}
	}
	return nil
}

// sourceSettings — clubs.settings клуба в MySQL так, как их записало ядро (клуб legacy без settings — файл, Q-022).
func sourceSettings(ctx context.Context, env *Env) ([]byte, error) {
	var raw sql.NullString
	if err := env.Source.QueryRowContext(ctx, "SELECT settings FROM clubs WHERE id = ?", env.ClubID).Scan(&raw); err != nil {
		return nil, fmt.Errorf("mysql: club settings: %w", err)
	}
	if raw.Valid {
		return []byte(raw.String), nil
	}
	if env.ClubSlug == legacySlug && env.Options.LegacySettingsFile != nil && json.Valid(env.Options.LegacySettingsFile) {
		return env.Options.LegacySettingsFile, nil
	}
	return nil, nil
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}

// updateJSON — UPDATE <table>.<col> = v2 по id строк клуба (пачками через unnest).
func updateJSON(ctx context.Context, env *Env, tableName, col string, list []converted) error {
	const batch = 2000
	for start := 0; start < len(list); start += batch {
		part := list[start:min(start+batch, len(list))]
		ids := make([]int64, len(part))
		vals := make([]string, len(part))
		for i, c := range part {
			ids[i], vals[i] = c.ID, c.JSON
		}
		// Таблица и колонка — константы кода.
		q := fmt.Sprintf(`UPDATE %s AS t SET %s = u.v::jsonb FROM unnest($1::bigint[], $2::text[]) AS u(id, v)
			WHERE t.club_id = $3 AND t.id = u.id`, tableName, col) // #nosec G201 -- см. выше
		tag, err := env.Target.Exec(ctx, q, ids, vals, env.ClubID)
		if err != nil {
			return fmt.Errorf("pg: %s.%s v2: %w", tableName, col, err)
		}
		if tag.RowsAffected() != int64(len(part)) {
			return fmt.Errorf("pg: %s.%s v2: updated %d of %d", tableName, col, tag.RowsAffected(), len(part))
		}
	}
	return nil
}

// verifyJSON — сверка: каждое значение Postgres из ids проходит check.
func verifyJSON(ctx context.Context, env *Env, tableName, col string, check func(id int64, raw []byte) bool, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	q := fmt.Sprintf("SELECT id, %s::text FROM %s WHERE club_id = $1 AND id = ANY($2)", col, tableName) // #nosec G201 -- константы
	rows, err := env.Target.Query(ctx, q, env.ClubID, ids)
	if err != nil {
		return fmt.Errorf("pg: %s.%s: %w", tableName, col, err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		seen++
		if !check(id, raw) {
			env.Add(Blocker, CodeVerifyMismatch, tableName, col, "", strconv.FormatInt(id, 10),
				"значение v2 в Postgres читается иначе, чем v1 в MySQL")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if seen != len(ids) {
		env.Add(Blocker, CodeVerifyMismatch, tableName, col, "", "", fmt.Sprintf("найдено %d из %d строк", seen, len(ids)))
	}
	return nil
}

func keysOf[V any](m map[int64]V) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
