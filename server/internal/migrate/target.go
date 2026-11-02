package migrate

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pgQuerier — пул (plan, verify) или транзакция переноса (apply).
type pgQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	// CHECK (col IN ('a', 'b')) в выводе pg_get_constraintdef:
	// CHECK (((status)::text = ANY ((ARRAY['open'::character varying, 'closed'::character varying])::text[])))
	reCheckIn    = regexp.MustCompile(`^CHECK \(\(\(?(\w+)\)?::text = ANY \(\(?ARRAY\[(.*)\]\)?::text\[\]\)\)\)$`)
	reCheckValue = regexp.MustCompile(`'((?:[^']|'')*)'::(?:character varying|text)`)
)

// pgColumns — колонки таблицы Postgres и значения enum-CHECK (Q-026).
func pgColumns(ctx context.Context, q pgQuerier, tableName string) ([]pgColumn, error) {
	rows, err := q.Query(ctx, `SELECT a.attname, format_type(a.atttypid, NULL), a.attnotnull
		FROM pg_attribute a
		WHERE a.attrelid = to_regclass('public.' || quote_ident($1)) AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, tableName)
	if err != nil {
		return nil, fmt.Errorf("pg: columns of %s: %w", tableName, err)
	}
	cols, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pgColumn, error) {
		var c pgColumn
		err := r.Scan(&c.Name, &c.Type, &c.NotNull)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("pg: columns of %s: %w", tableName, err)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("pg: table %s not found", tableName)
	}
	enums, err := pgEnums(ctx, q, tableName)
	if err != nil {
		return nil, err
	}
	for i := range cols {
		cols[i].Enum = enums[cols[i].Name]
	}
	return cols, nil
}

// pgEnums разбирает CHECK (col IN (…)) таблицы: колонка → допустимые значения.
func pgEnums(ctx context.Context, q pgQuerier, tableName string) (map[string]map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c
		WHERE c.conrelid = to_regclass('public.' || quote_ident($1)) AND c.contype = 'c'`, tableName)
	if err != nil {
		return nil, fmt.Errorf("pg: checks of %s: %w", tableName, err)
	}
	defs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("pg: checks of %s: %w", tableName, err)
	}
	out := map[string]map[string]bool{}
	for _, def := range defs {
		col, values, ok := parseCheckIn(def)
		if !ok {
			continue
		}
		out[col] = values
	}
	return out, nil
}

func parseCheckIn(def string) (string, map[string]bool, bool) {
	m := reCheckIn.FindStringSubmatch(def)
	if m == nil {
		return "", nil, false
	}
	values := map[string]bool{}
	for _, v := range reCheckValue.FindAllStringSubmatch(m[2], -1) {
		values[strings.ReplaceAll(v[1], "''", "'")] = true
	}
	if len(values) == 0 {
		return "", nil, false
	}
	return m[1], values, true
}

// insertSQL — вставка пачки строк: каждая колонка — массив text, приведение к типу колонки в SQL
// (точные значения без float: суммы уже целые тиыны строкой, время — RFC 3339 с поясом).
func insertSQL(tableName string, cols []column, extra []string) string {
	var names, selects, params []string
	n := 0
	add := func(name, cast string) {
		n++
		ident := pgx.Identifier{name}.Sanitize()
		names = append(names, ident)
		params = append(params, fmt.Sprintf("$%d::text[]", n))
		selects = append(selects, fmt.Sprintf("u.c%d::%s", n, cast))
	}
	for _, c := range cols {
		add(c.Name, c.Cast)
	}
	for _, e := range extra {
		add(e, "text")
	}
	aliases := make([]string, n)
	for i := range aliases {
		aliases[i] = fmt.Sprintf("c%d", i+1)
	}
	return fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM unnest(%s) AS u(%s)",
		pgx.Identifier{tableName}.Sanitize(), strings.Join(names, ", "), strings.Join(selects, ", "),
		strings.Join(params, ", "), strings.Join(aliases, ", "))
}

// pgWhere подставляет параметры в условие сверки: {club} — id клуба, {ids} — список id, {cutoff} — граница срока
// хранения. Возвращает условие и аргументы (только используемые — Postgres не выводит тип неиспользуемого).
func pgWhere(where string, clubID int64, ids []int64, cutoff any) (string, []any) {
	var args []any
	for _, p := range []struct {
		token string
		val   any
	}{{"{club}", clubID}, {"{ids}", ids}, {"{cutoff}", cutoff}} {
		if strings.Contains(where, p.token) {
			args = append(args, p.val)
			where = strings.ReplaceAll(where, p.token, fmt.Sprintf("$%d", len(args)))
		}
	}
	return where, args
}
