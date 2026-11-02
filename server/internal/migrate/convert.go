package migrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// mysqlColumn — колонка таблицы MySQL (information_schema.COLUMNS).
type mysqlColumn struct {
	Name     string
	DataType string // decimal, datetime, timestamp, json, tinyint, …
	Type     string // COLUMN_TYPE: decimal(12,2), tinyint(1), bigint unsigned …
	Nullable bool
}

// pgColumn — колонка таблицы Postgres (pg_attribute).
type pgColumn struct {
	Name    string
	Type    string // format_type без модификатора: bigint, boolean, timestamp with time zone, jsonb …
	NotNull bool
	// Enum — допустимые значения из CHECK (col IN (…)); nil — ограничения нет.
	Enum map[string]bool
}

// kind — вид преобразования значения MySQL → Postgres (все значения передаются текстом и приводятся в SQL).
type kind int

const (
	kindText    kind = iota // как есть
	kindTiyn                // decimal(…,2) тенге → bigint тиыны (× 100 точно, строкой)
	kindBool                // tinyint(1) → boolean
	kindTime                // datetime/timestamp (строка в SourceTZ) → timestamptz
	kindJSON                // json → jsonb (формат v1 как есть; v2 — M2)
	kindNumeric             // decimal (не деньги) → numeric как есть
)

func (k kind) String() string {
	return [...]string{"текст", "тиыны ×100", "boolean", "timestamptz", "jsonb", "numeric"}[k]
}

// column — пара колонок и способ преобразования.
type column struct {
	Name  string
	Kind  kind
	Cast  string // тип Postgres для приведения текста
	My    mysqlColumn
	PG    pgColumn
	Index int // позиция в строке MySQL
}

// planColumns сопоставляет колонки MySQL и Postgres. Колонка MySQL без пары и не из omit — ошибка схемы.
func planColumns(t table, my []mysqlColumn, pg []pgColumn) ([]column, []string, error) {
	pgByName := make(map[string]pgColumn, len(pg))
	for _, c := range pg {
		pgByName[c.Name] = c
	}
	var cols []column
	var unmapped []string
	for i, m := range my {
		p, ok := pgByName[m.Name]
		if _, omitted := t.Omit[m.Name]; omitted {
			continue
		}
		if !ok {
			unmapped = append(unmapped, m.Name)
			continue
		}
		c := column{Name: m.Name, My: m, PG: p, Index: i, Cast: p.Type}
		switch {
		case p.Type == "boolean":
			c.Kind = kindBool
		case p.Type == "timestamp with time zone":
			if m.DataType != "datetime" && m.DataType != "timestamp" {
				return nil, nil, fmt.Errorf("%s.%s: %s → timestamptz не поддерживается", t.Name, m.Name, m.Type)
			}
			c.Kind = kindTime
		case m.DataType == "decimal" && p.Type == "bigint":
			// Деньги: decimal(12,2)/decimal(14,2) в тенге → тиыны (docs/schema-changes.md §3.1).
			if !strings.HasSuffix(strings.TrimSuffix(m.Type, " unsigned"), ",2)") {
				return nil, nil, fmt.Errorf("%s.%s: денежная колонка %s, ожидался масштаб 2", t.Name, m.Name, m.Type)
			}
			c.Kind = kindTiyn
		case m.DataType == "decimal":
			c.Kind = kindNumeric
		case p.Type == "jsonb":
			c.Kind = kindJSON
		default:
			c.Kind = kindText
		}
		cols = append(cols, c)
	}
	return cols, unmapped, nil
}

var (
	errBadTiyn = errors.New("сумма не в формате decimal с ≤ 2 знаками")
	errBadTime = errors.New("время не разбирается")
	errBadJSON = errors.New("невалидный JSON")
	errNulJSON = errors.New(`JSON с \u0000 (jsonb не принимает)`)
)

// convert преобразует текстовое значение MySQL в текст для Postgres. nil — NULL.
func (c *column) convert(v *string, loc *time.Location) (*string, error) {
	if v == nil {
		return nil, nil
	}
	s := *v
	switch c.Kind {
	case kindTiyn:
		t, err := tiynFromDecimal(s)
		if err != nil {
			return nil, err
		}
		out := strconv.FormatInt(int64(t), 10)
		return &out, nil
	case kindBool:
		out := "t"
		if s == "0" {
			out = "f"
		}
		return &out, nil
	case kindTime:
		t, err := parseMySQLTime(s, loc)
		if err != nil {
			return nil, err
		}
		out := t.UTC().Format(time.RFC3339Nano)
		return &out, nil
	case kindJSON:
		b := []byte(s)
		if !json.Valid(b) {
			return nil, errBadJSON
		}
		if bytes.Contains(b, []byte(`\u0000`)) {
			return nil, errNulJSON
		}
		return v, nil
	default:
		return v, nil
	}
}

// tiynFromDecimal — decimal MySQL (строка «1500.50», «-12.30») → тиыны: × 100 точно, через строку, без float
// (решение Q-001; тот же строгий разбор, что у ввода сумм, money.ParseTenge).
func tiynFromDecimal(s string) (money.Tiyn, error) {
	t, err := money.ParseTenge(s)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %w", errBadTiyn, s, err)
	}
	return t, nil
}

// parseMySQLTime разбирает строку DATETIME/TIMESTAMP MySQL («2026-10-13 04:00:00[.ffffff]») в поясе loc.
// Laravel пишет время строкой в config('app.timezone') (в старом проекте — UTC, config/app.php ~74);
// TIMESTAMP MySQL при чтении в той же сессии возвращает ту же строку, поэтому пояс сессии не меняем.
func parseMySQLTime(s string, loc *time.Location) (time.Time, error) {
	if strings.HasPrefix(s, "0000-00-00") {
		return time.Time{}, fmt.Errorf("%w: нулевая дата %q", errBadTime, s)
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q", errBadTime, s)
	}
	return t, nil
}
